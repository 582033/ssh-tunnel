use crate::config::Config;
use crate::dns;
use crate::ssh::{SshConn, DIAL_TIMEOUT};
use std::sync::atomic::{AtomicBool, AtomicU64, Ordering};
use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant};
use tokio::io::{AsyncReadExt, AsyncWriteExt};
use tokio::net::{TcpListener, TcpStream};
use tokio::sync::oneshot;
use tokio::task::JoinHandle;
use tokio::time::timeout;

/// socks5 握手阶段的读写上限：磨蹭/半开的客户端不至于一直占着线程。
const HANDSHAKE_TIMEOUT: Duration = Duration::from_secs(30);
/// accept 轮询粒度：停机后至多这么久感知并退出。
const ACCEPT_POLL: Duration = Duration::from_millis(300);

/// socks5 服务器（等价 go-socks5），所有出站连接经 SSH 隧道。
pub struct Server {
    local_port: String,
    custom_dns: String,
    ssh: Arc<SshConn>,
    shutdown: Arc<AtomicBool>,
    conn_seq: AtomicU64,
    tasks: Mutex<Vec<JoinHandle<()>>>,
    log: Box<dyn Fn(&str) + Send + Sync>,
}

impl Server {
    pub fn new(
        cfg: &Config,
        ssh: Arc<SshConn>,
        shutdown: Arc<AtomicBool>,
        log: Box<dyn Fn(&str) + Send + Sync>,
    ) -> Self {
        Server {
            local_port: cfg.local_port.clone(),
            custom_dns: cfg.custom_dns.clone(),
            ssh,
            shutdown,
            conn_seq: AtomicU64::new(0),
            tasks: Mutex::new(Vec::new()),
            log,
        }
    }

    fn logf(&self, s: &str) {
        (self.log)(s);
    }

    /// 监听并派发连接，直到停机。启动结果（端口占用等）经 ready 立即回报。
    pub async fn run(self: Arc<Self>, ready: oneshot::Sender<Result<(), String>>) {
        let addr = format!("127.0.0.1:{}", self.local_port);
        let listener = match TcpListener::bind(&addr).await {
            Ok(l) => l,
            Err(e) => {
                let err = format!("监听 {} 失败: {}", addr, e);
                let _ = ready.send(Err(err));
                return;
            }
        };
        let _ = ready.send(Ok(()));

        if self.custom_dns.is_empty() {
            self.logf("域名将由 SSH 服务器解析");
        } else {
            self.logf(&format!(
                "域名将通过隧道向 {} 查询（DNS over TCP）",
                self.custom_dns
            ));
        }

        loop {
            if self.shutdown.load(Ordering::Relaxed) {
                break;
            }
            match timeout(ACCEPT_POLL, listener.accept()).await {
                Ok(Ok((stream, _))) => {
                    let me = self.clone();
                    let h = tokio::spawn(async move { me.handle_conn(stream).await });
                    let mut tasks = self.tasks.lock().unwrap();
                    // 顺手清掉已结束的任务，长时间运行时句柄不至于越积越多
                    tasks.retain(|t| !t.is_finished());
                    tasks.push(h);
                }
                Ok(Err(_)) if self.shutdown.load(Ordering::Relaxed) => break,
                Ok(Err(e)) => {
                    self.logf(&format!("accept 失败: {}", e));
                    break;
                }
                Err(_) => {} // 本轮没有新连接，回头看停机标志
            }
        }

        self.abort_all();
    }

    /// 停机：置标志并中止所有连接任务。可重复调用。
    pub fn close(&self) {
        self.shutdown.store(true, Ordering::Relaxed);
        self.abort_all();
    }

    fn abort_all(&self) {
        let mut tasks = self.tasks.lock().unwrap();
        for h in tasks.drain(..) {
            h.abort();
        }
    }

    /// 单条客户端连接：握手 → 解析目标 → 拨号 → 中继
    async fn handle_conn(self: Arc<Self>, mut stream: TcpStream) {
        let target = match Self::handshake(&mut stream).await {
            Ok(t) => t,
            Err(()) => return,
        };

        let host = if target.is_fqdn && !self.custom_dns.is_empty() {
            match self.resolve(&target.host).await {
                Some(ip) => ip,
                None => target.host.clone(),
            }
        } else {
            target.host.clone()
        };

        let id = self.conn_seq.fetch_add(1, Ordering::Relaxed);
        let disp = format!("{}:{}", host, target.port);
        self.logf(&format!("#{} → tcp {}", id, disp));
        let start = Instant::now();
        match self
            .ssh
            .dial(&host, target.port as u32, DIAL_TIMEOUT)
            .await
        {
            Ok(mut chan) => {
                self.logf(&format!(
                    "#{} ✓ {} 已建立（耗时 {}）",
                    id,
                    disp,
                    go_duration(start.elapsed())
                ));
                let reply = [0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0];
                if timeout(HANDSHAKE_TIMEOUT, stream.write_all(&reply))
                    .await
                    .is_err()
                {
                    return;
                }
                let _ = stream.flush().await;
                // 双向拷贝：任一端 EOF / 出错即结束，channel 由 russh 自动关闭
                let _ = tokio::io::copy_bidirectional(&mut stream, &mut chan).await;
            }
            Err(e) => {
                self.logf(&format!(
                    "#{} ✗ {} 失败（耗时 {}）: {}",
                    id,
                    disp,
                    go_duration(start.elapsed()),
                    e
                ));
                let reply = [0x05, 0x05, 0x00, 0x01, 0, 0, 0, 0, 0, 0];
                let _ = timeout(HANDSHAKE_TIMEOUT, stream.write_all(&reply)).await;
            }
        }
    }

    /// socks5 握手 + 请求解析。任何一步失败都直接放弃（客户端会自行重试）。
    async fn handshake(stream: &mut TcpStream) -> Result<Target, ()> {
        let mut head = [0u8; 2];
        read_exact(stream, &mut head).await?;
        if head[0] != 0x05 {
            return Err(());
        }
        let mut methods = vec![0u8; head[1] as usize];
        read_exact(stream, &mut methods).await?;
        // 无需认证
        write_all(stream, &[0x05, 0x00]).await?;

        let mut req = [0u8; 4];
        read_exact(stream, &mut req).await?;
        if req[0] != 0x05 {
            return Err(());
        }
        if req[1] != 0x01 {
            // 只支持 CONNECT；BIND / UDP ASSOCIATE 不支持
            write_all(stream, &[0x05, 0x07, 0x00, 0x01, 0, 0, 0, 0, 0, 0]).await?;
            return Err(());
        }

        let (host, is_fqdn) = match req[3] {
            0x01 => {
                let mut a = [0u8; 4];
                read_exact(stream, &mut a).await?;
                (std::net::Ipv4Addr::from(a).to_string(), false)
            }
            0x04 => {
                let mut a = [0u8; 16];
                read_exact(stream, &mut a).await?;
                (std::net::Ipv6Addr::from(a).to_string(), false)
            }
            0x03 => {
                let mut l = [0u8; 1];
                read_exact(stream, &mut l).await?;
                let mut a = vec![0u8; l[0] as usize];
                read_exact(stream, &mut a).await?;
                (String::from_utf8_lossy(&a).into_owned(), true)
            }
            _ => return Err(()),
        };
        let mut port_b = [0u8; 2];
        read_exact(stream, &mut port_b).await?;
        if host.is_empty() {
            return Err(());
        }
        Ok(Target {
            host,
            port: u16::from_be_bytes(port_b),
            is_fqdn,
        })
    }

    /// 指定 DNS 时经隧道解析；失败或无结果时日志说明并返回 None（回退服务器解析）。
    async fn resolve(&self, name: &str) -> Option<String> {
        let start = Instant::now();
        match dns::lookup_a(&self.ssh, &self.custom_dns, name).await {
            Ok(ip) => {
                self.logf(&format!(
                    "DNS ✓ {} → {}（经 {}，耗时 {}）",
                    name,
                    ip,
                    self.custom_dns,
                    go_duration(start.elapsed())
                ));
                Some(ip)
            }
            Err(e) if e.contains("无解析结果") => {
                self.logf(&format!(
                    "DNS ✗ {} 无解析结果（经 {}），改由 SSH 服务器解析",
                    name, self.custom_dns
                ));
                None
            }
            Err(e) => {
                self.logf(&format!(
                    "DNS ✗ {} 解析失败（经 {}，耗时 {}）: {}",
                    name,
                    self.custom_dns,
                    go_duration(start.elapsed()),
                    e
                ));
                self.logf(&format!(
                    "DNS 改由 SSH 服务器解析 {}（指定的 DNS 走隧道不可达，国内服务器到 8.8.8.8 等公共 DNS 的 TCP/53 常被阻断）",
                    name
                ));
                None
            }
        }
    }
}

struct Target {
    host: String,
    port: u16,
    is_fqdn: bool,
}

/// 带超时的整块读（握手阶段用）。
async fn read_exact(r: &mut TcpStream, buf: &mut [u8]) -> Result<(), ()> {
    match timeout(HANDSHAKE_TIMEOUT, r.read_exact(buf)).await {
        Ok(Ok(_)) => Ok(()),
        _ => Err(()),
    }
}

async fn write_all(w: &mut TcpStream, data: &[u8]) -> Result<(), ()> {
    match timeout(HANDSHAKE_TIMEOUT, w.write_all(data)).await {
        Ok(Ok(())) => Ok(()),
        _ => Err(()),
    }
}

/// 与 Go time.Duration 字符串一致的耗时格式。
fn go_duration(d: Duration) -> String {
    let ms = d.as_millis();
    if ms < 1000 {
        return format!("{}ms", ms);
    }
    let s = format!("{:.3}", ms as f64 / 1000.0);
    let s = s.trim_end_matches('0').trim_end_matches('.');
    format!("{}s", s)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn duration_format() {
        assert_eq!(go_duration(Duration::from_millis(35)), "35ms");
        assert_eq!(go_duration(Duration::from_millis(1200)), "1.2s");
        assert_eq!(go_duration(Duration::from_millis(1236)), "1.236s");
        assert_eq!(go_duration(Duration::from_millis(15000)), "15s");
    }

    /// 完整握手 + 一次域名形式的 CONNECT 请求解析。
    #[tokio::test]
    async fn handshake_parses_fqdn() {
        let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
        let addr = listener.local_addr().unwrap();
        let mut client = TcpStream::connect(addr).await.unwrap();
        let (mut server_side, _) = listener.accept().await.unwrap();

        let parsed = tokio::spawn(async move { Server::handshake(&mut server_side).await });

        let mut body = vec![0x05, 0x01, 0x00]; // 握手：只支持无认证
        body.extend_from_slice(&[0x05, 0x01, 0x00, 0x03, 0x0b]); // CONNECT + FQDN + 长度
        body.extend_from_slice(b"example.com");
        body.extend_from_slice(&443u16.to_be_bytes());
        client.write_all(&body).await.unwrap();

        let t = parsed.await.unwrap().unwrap();
        assert_eq!(t.host, "example.com");
        assert_eq!(t.port, 443);
        assert!(t.is_fqdn);

        // 服务端回的两个字节：版本 + 无需认证
        let mut greeting = [0u8; 2];
        client.read_exact(&mut greeting).await.unwrap();
        assert_eq!(greeting, [0x05, 0x00]);
    }
}
