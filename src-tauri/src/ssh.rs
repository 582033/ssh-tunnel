use crate::config::Config;
use russh::client::{
    self, AuthResult, Handle, KeyboardInteractiveAuthResponse, Prompt, Session,
};
use russh::keys::{load_secret_key, PrivateKeyWithHashAlg, PublicKeyOrCertificate};
use russh::Disconnect;
use std::collections::HashMap;
use std::net::SocketAddr;
use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant};
use tokio::net::lookup_host;
use tokio::time::timeout;

/// 建立 SSH 连接（TCP + 握手 + 认证）的超时。
pub const CONNECT_TIMEOUT: Duration = Duration::from_secs(15);
/// 单条隧道连接的等待上限。服务器连不上目标时往往既不成功也不拒绝，
/// 一直挂着——8s 是个折中：慢站点够用，浏览器也不会白等太久。
pub const DIAL_TIMEOUT: Duration = Duration::from_secs(8);
/// 不可达目标的记忆时长：超时过的目标在这段时间内直接快速失败。
const STALL_TTL: Duration = Duration::from_secs(60);

/// 一条经隧道的 TCP 连接（SSH channel 的读写流）。
pub type Tunnel = russh::ChannelStream<client::Msg>;

/// 经 SSH 隧道建立 TCP 连接的会话外壳。所有拨号都通过
/// channel_direct_tcpip（动态端口转发）。
///
/// 与之前的 libssh2 实现不同，russh 的每一次 channel-open 都是独立的
/// 请求/确认，服务端互不干扰：被墙域名只会让它自己那条连接超时失败，
/// 不会像 libssh2 那样占住会话里唯一的 open 槽、拖垮整条隧道。
pub struct SshConn {
    handle: Arc<Handle<Handler>>,
    /// 超时过的「host:port」及发生时间。被墙/不可达的目标会被反复访问，
    /// 记住它们可以立刻失败，省掉浏览器每次白等一个完整拨号超时。
    stalled: Mutex<HashMap<String, Instant>>,
}

impl SshConn {
    pub fn new(handle: Handle<Handler>) -> Self {
        SshConn {
            handle: Arc::new(handle),
            stalled: Mutex::new(HashMap::new()),
        }
    }

    /// 链路是否仍然可用。会话任务结束（对端断开、TCP 断掉）即为 false。
    pub fn is_connected(&self) -> bool {
        !self.handle.is_closed()
    }

    /// 打开一条到 (host, port) 的隧道 channel。被墙/不可达的目标最多等
    /// `wait`，超时只影响本次拨号，不影响会话上的其它连接。
    pub async fn dial(&self, host: &str, port: u32, wait: Duration) -> Result<Tunnel, String> {
        if self.handle.is_closed() {
            return Err("SSH 连接已断开".to_string());
        }
        let key = format!("{}:{}", host, port);
        if let Some(at) = self.stalled.lock().unwrap().get(&key) {
            if at.elapsed() < STALL_TTL {
                return Err(format!(
                    "经隧道连接 {} 不可达（{}s 内刚超时过，快速失败）",
                    key,
                    STALL_TTL.as_secs()
                ));
            }
        }

        let open = self
            .handle
            .channel_open_direct_tcpip(host, port, "127.0.0.1", 0);
        match timeout(wait, open).await {
            Ok(Ok(ch)) => {
                // 通了就忘掉它不可达这件事
                self.stalled.lock().unwrap().remove(&key);
                Ok(ch.into_stream())
            }
            Ok(Err(e)) => Err(format!("经隧道连接 {}:{} 失败: {}", host, port, e)),
            Err(_) => {
                let mut stalled = self.stalled.lock().unwrap();
                stalled.retain(|_, at| at.elapsed() < STALL_TTL);
                stalled.insert(key.clone(), Instant::now());
                Err(format!(
                    "经隧道连接 {} 超时（{}s）",
                    key,
                    wait.as_secs()
                ))
            }
        }
    }

    /// 主动断开会话。
    pub async fn close(&self) {
        let _ = self
            .handle
            .disconnect(Disconnect::ByApplication, "shutdown", "")
            .await;
    }
}

/// 按 cfg 建立 SSH 连接并完成认证，返回连接外壳与服务器版本串。
/// auth_fail 回调用于逐条记录不可用方式。
pub async fn connect(
    cfg: &Config,
    auth_fail: &dyn Fn(String),
) -> Result<(SshConn, String), String> {
    let host = &cfg.server_addr;
    let port: u16 = cfg.server_port.parse().unwrap_or(22);

    let addrs: Vec<SocketAddr> = timeout(CONNECT_TIMEOUT, lookup_host((host.as_str(), port)))
        .await
        .map_err(|_| format!("解析服务器地址 {} 超时（15s）", host))?
        .map_err(|e| format!("解析服务器地址失败（{}:{}）: {}", host, port, e))?
        .collect();
    if addrs.is_empty() {
        return Err(format!("解析服务器地址失败（{}:{}）: 无结果", host, port));
    }

    let mut last_err = String::from("TCP 连接失败");
    for addr in addrs {
        let banner = Arc::new(Mutex::new(String::new()));
        let config = Arc::new(client_config());
        match timeout(
            CONNECT_TIMEOUT,
            client::connect(config, addr, Handler { banner: banner.clone() }),
        )
        .await
        {
            Ok(Ok(mut handle)) => {
                // 认证也要有上限：服务器不回应认证请求时（如 dropbear 对同 IP
                // 的并发未认证连接做限制）会一直不回包，没有超时会把重连线程
                // 永久挂住，表现就是隧道再也不重连。
                match timeout(CONNECT_TIMEOUT, auth(&mut handle, cfg, auth_fail)).await {
                    Ok(Ok(())) => {
                        let version = banner.lock().unwrap().clone();
                        return Ok((SshConn::new(handle), version));
                    }
                    Ok(Err(e)) => {
                        let _ = handle
                            .disconnect(Disconnect::ByApplication, "auth failed", "")
                            .await;
                        last_err = e;
                    }
                    Err(_) => {
                        let _ = handle
                            .disconnect(Disconnect::ByApplication, "auth timeout", "")
                            .await;
                        last_err = format!("认证超时（{}s）", CONNECT_TIMEOUT.as_secs());
                    }
                }
            }
            Ok(Err(e)) => last_err = format!("SSH 握手失败: {}", e),
            Err(_) => last_err = format!("SSH 连接超时（{}s）", CONNECT_TIMEOUT.as_secs()),
        }
    }
    Err(last_err)
}

fn client_config() -> client::Config {
    let mut c = client::Config::default();
    // 30s 无流量发一次保活，连丢 3 个回应就判定链路断了交给 manager 重连
    c.keepalive_interval = Some(Duration::from_secs(30));
    c.keepalive_max = 3;
    c.nodelay = true;
    c
}

/// 客户端回调。不校验主机密钥（与 Go 版 InsecureIgnoreHostKey 一致），
/// 并在密钥交换完成时记录服务器版本串供日志展示。
pub struct Handler {
    banner: Arc<Mutex<String>>,
}

impl client::Handler for Handler {
    type Error = russh::Error;

    async fn check_server_key(
        &mut self,
        _server_public_key: &PublicKeyOrCertificate,
    ) -> Result<bool, Self::Error> {
        Ok(true)
    }

    async fn kex_done(
        &mut self,
        _shared_secret: Option<&[u8]>,
        _names: &russh::Names,
        session: &mut Session,
    ) -> Result<(), Self::Error> {
        *self.banner.lock().unwrap() = String::from_utf8_lossy(session.remote_sshid()).to_string();
        Ok(())
    }
}

/// 依次尝试配置的认证方式，任一种成功即可。语义与 Go 版一致：
/// 密钥和密码都填了就都试，服务器接受哪种用哪种。
async fn auth(
    handle: &mut Handle<Handler>,
    cfg: &Config,
    auth_fail: &dyn Fn(String),
) -> Result<(), String> {
    let user = cfg.username.as_str();
    let mut last_errs: Vec<String> = Vec::new();

    if !cfg.private_key.is_empty() {
        match load_secret_key(&cfg.private_key, None) {
            Ok(key) => {
                // RSA 需要挑哈希算法，服务器的支持情况问它自己
                let hash = handle.best_supported_rsa_hash().await.ok().flatten().flatten();
                let key = PrivateKeyWithHashAlg::new(Arc::new(key), hash);
                match handle.authenticate_publickey(user, key).await {
                    Ok(AuthResult::Success) => return Ok(()),
                    Ok(_) => {
                        let msg = "密钥认证失败: 服务器拒绝了私钥".to_string();
                        auth_fail(msg.clone());
                        last_errs.push(msg);
                    }
                    Err(e) => {
                        let msg = format!("密钥认证失败: {}", e);
                        auth_fail(msg.clone());
                        last_errs.push(msg);
                    }
                }
            }
            Err(e) => {
                let msg = format!("密钥认证失败: 读取私钥 {}: {}", cfg.private_key, e);
                auth_fail(msg.clone());
                last_errs.push(msg);
            }
        }
    }

    if cfg.password.is_empty() {
        if last_errs.is_empty() {
            return Err("缺少 privateKey 或 password".to_string());
        }
        return Err(format!("没有可用的认证方式: {}", last_errs.join("; ")));
    }

    match handle.authenticate_password(user, &cfg.password).await {
        Ok(AuthResult::Success) => return Ok(()),
        Ok(_) => {}
        Err(e) => last_errs.push(format!("密码认证失败: {}", e)),
    }

    // 部分服务器只开 keyboard-interactive，不接受 password
    if keyboard_interactive(handle, user, &cfg.password).await {
        return Ok(());
    }
    last_errs.push("密码/keyboard-interactive 认证失败".to_string());
    Err(format!("没有可用的认证方式: {}", last_errs.join("; ")))
}

/// keyboard-interactive 认证：所有提示都用配置的密码作答，最多应答 8 轮。
async fn keyboard_interactive(handle: &mut Handle<Handler>, user: &str, password: &str) -> bool {
    let mut resp = match handle
        .authenticate_keyboard_interactive_start(user, None)
        .await
    {
        Ok(r) => r,
        Err(_) => return false,
    };
    for _ in 0..8 {
        match resp {
            KeyboardInteractiveAuthResponse::Success => return true,
            KeyboardInteractiveAuthResponse::Failure { .. } => return false,
            KeyboardInteractiveAuthResponse::InfoRequest { prompts, .. } => {
                let answers = prompts
                    .iter()
                    .map(|_: &Prompt| password.to_string())
                    .collect::<Vec<String>>();
                resp = match handle
                    .authenticate_keyboard_interactive_respond(answers)
                    .await
                {
                    Ok(r) => r,
                    Err(_) => return false,
                };
            }
        }
    }
    false
}
