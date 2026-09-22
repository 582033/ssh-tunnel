use crate::chrome::Chrome;
use crate::config::Config;
use crate::socks5::Server as SocksServer;
use crate::ssh::{self, SshConn};
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{Arc, Mutex};
use std::thread::JoinHandle;
use std::time::{Duration, Instant};
use tokio::sync::mpsc::{self, UnboundedReceiver, UnboundedSender};
use tokio::sync::oneshot;
use tokio::task::JoinHandle as TaskHandle;

const MIN_BACKOFF: Duration = Duration::from_secs(2);
const MAX_BACKOFF: Duration = Duration::from_secs(60);
/// 监听启动后的观察窗口：端口占用等失败会在窗口内回报。
const CONNECT_STALL_WINDOW: Duration = Duration::from_millis(300);

#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum State {
    Stopped,
    Connecting,
    Connected,
    Reconnecting,
    Failed,
}

impl State {
    pub fn as_str(&self) -> &'static str {
        match self {
            State::Stopped => "stopped",
            State::Connecting => "connecting",
            State::Connected => "connected",
            State::Reconnecting => "reconnecting",
            State::Failed => "failed",
        }
    }
}

/// 与 Go 版 tunnel.Manager 对应：连接 → 起 socks5 代理 → 等断开 → 退避重连，
/// 可反复 Start / Stop。实例恒存放于 AppState（Arc<Manager>）中。
///
/// 对外保持同步接口：重连状态机跑在一条独立线程自带的 tokio runtime 上，
/// SSH / socks5 内部则是异步的。
pub struct Manager {
    cfg: Mutex<Config>,

    running: Mutex<bool>,
    stop_tx: Mutex<Option<UnboundedSender<()>>>,
    join: Mutex<Option<JoinHandle<()>>>,

    ssh: Mutex<Option<Arc<SshConn>>>,
    socks: Mutex<Option<Arc<SocksServer>>>,
    browser: Mutex<Option<Arc<Chrome>>>,
    shutdown: Mutex<Option<Arc<AtomicBool>>>,

    state: Mutex<(State, String)>,
    on_state: Mutex<Box<dyn Fn(State, String) + Send + Sync>>,
    on_log: Mutex<Box<dyn Fn(String) + Send + Sync>>,
}

impl Manager {
    pub fn new(cfg: &Config) -> Self {
        Manager {
            cfg: Mutex::new(cfg.clone()),
            running: Mutex::new(false),
            stop_tx: Mutex::new(None),
            join: Mutex::new(None),
            ssh: Mutex::new(None),
            socks: Mutex::new(None),
            browser: Mutex::new(None),
            shutdown: Mutex::new(None),
            state: Mutex::new((State::Stopped, String::new())),
            on_state: Mutex::new(Box::new(|_, _| {})),
            on_log: Mutex::new(Box::new(|_| {})),
        }
    }

    pub fn set_callbacks(
        &self,
        on_state: Box<dyn Fn(State, String) + Send + Sync>,
        on_log: Box<dyn Fn(String) + Send + Sync>,
    ) {
        *self.on_state.lock().unwrap() = on_state;
        *self.on_log.lock().unwrap() = on_log;
    }

    /// 可复用的日志回调（捕获 Arc，供 socks/chrome 后台线程调用）。
    fn log_fun(self: &Arc<Self>) -> Box<dyn Fn(String) + Send + Sync> {
        let me = self.clone();
        Box::new(move |msg| {
            let cb = me.on_log.lock().unwrap();
            cb(msg);
        })
    }

    pub fn config(&self) -> Config {
        self.cfg.lock().unwrap().clone()
    }

    /// 替换配置。运行中也能改：supervise 每轮重连都会重新读配置，改动会在
    /// 下一次重连生效；当前这条已连上的隧道不受影响（本地端口、代理等还是旧的）。
    /// 之前这里会直接拒绝，导致「连接失败」态（重连线程还在跑）保存不了配置。
    pub fn set_config(&self, cfg: &Config) {
        *self.cfg.lock().unwrap() = cfg.clone();
        if self.is_running() {
            self.log("配置已更新，将在下次重连后生效（当前连接继续使用旧配置）".to_string());
        }
    }

    pub fn is_running(&self) -> bool {
        *self.running.lock().unwrap()
    }

    fn set_state(&self, s: State, detail: String) {
        {
            let mut st = self.state.lock().unwrap();
            st.0 = s;
            st.1 = detail.clone();
        }
        let cb = self.on_state.lock().unwrap();
        cb(s, detail);
    }

    fn log(&self, msg: String) {
        let cb = self.on_log.lock().unwrap();
        cb(msg);
    }

    /// 启动隧道。非阻塞，后台维护连接与自动重连。
    pub fn start(self: &Arc<Self>) -> Result<(), String> {
        {
            let mut r = self.running.lock().unwrap();
            if *r {
                return Err("隧道已在运行".to_string());
            }
            *r = true;
        }
        let (tx, rx) = mpsc::unbounded_channel();
        *self.stop_tx.lock().unwrap() = Some(tx);

        let me = self.clone();
        let join = std::thread::spawn(move || supervise(me, rx));
        *self.join.lock().unwrap() = Some(join);
        Ok(())
    }

    /// 停止隧道并等待后台线程收尾（断开 SSH、关代理、关 Chrome）。
    pub fn stop(&self) {
        {
            let mut r = self.running.lock().unwrap();
            if !*r {
                return;
            }
            *r = false;
        }
        let tx = self.stop_tx.lock().unwrap().take();
        if let Some(tx) = tx {
            let _ = tx.send(());
        }

        self.log("正在停止隧道…".to_string());
        // 先关 Chrome：重连线程可能还在等退避，别让它拖着窗口
        self.close_browser();
        if let Some(join) = self.join.lock().unwrap().take() {
            join_limited(join, Duration::from_secs(20));
        }

        self.set_state(State::Stopped, String::new());
        self.log("隧道已停止".to_string());
    }

    /// 手动拉起 Chrome（界面「打开 Chrome」按钮）。
    pub fn start_browser(self: &Arc<Self>) -> Result<(), String> {
        let cfg = self.config();
        if cfg.chrome_path.is_empty() {
            self.log("未配置 chromePath".to_string());
            return Ok(());
        }
        if !self.is_running() {
            return Err("请先连接隧道".to_string());
        }

        let args = browser_args(&cfg);
        let logger = self.log_fun();
        let log = Box::new(move |s: &str| logger(s.to_string()));
        match Chrome::start(&cfg.chrome_path, &args, chrome_user_data_dir(), log) {
            Ok(b) => {
                let old = self.browser.lock().unwrap().replace(b);
                if let Some(old) = old {
                    old.close();
                }
                self.log(format!(
                    "启动 Chrome: {} {}",
                    cfg.chrome_path,
                    args.join(" ")
                ));
                Ok(())
            }
            Err(e) => {
                self.log(e.clone());
                Err(e)
            }
        }
    }

    fn close_browser(&self) {
        if let Some(b) = self.browser.lock().unwrap().take() {
            b.close();
        }
    }

    /// 断开 SSH 并回收本轮 socks5（供 supervise 在每轮结束时调用）。
    async fn teardown(&self) {
        if let Some(shutdown) = self.shutdown.lock().unwrap().take() {
            shutdown.store(true, Ordering::Relaxed);
        }
        if let Some(socks) = self.socks.lock().unwrap().take() {
            socks.close();
        }
        if let Some(conn) = self.ssh.lock().unwrap().take() {
            conn.close().await;
        }
    }
}

fn join_limited<T>(h: JoinHandle<T>, limit: Duration) {
    let deadline = Instant::now() + limit;
    loop {
        if h.is_finished() {
            let _ = h.join();
            return;
        }
        if Instant::now() >= deadline {
            return;
        }
        std::thread::sleep(Duration::from_millis(50));
    }
}

/// 无痕 Chrome 的独立 profile 目录。靠它在关闭时认出属于本 App 的窗口。
fn chrome_user_data_dir() -> String {
    format!("{}/ssh-tunnel-chrome", std::env::temp_dir().display())
}

/// 无痕 Chrome 启动参数（与 Go 版一致）。
fn browser_args(cfg: &Config) -> Vec<String> {
    vec![
        "--incognito".to_string(),
        "--dns-prefetch-disable".to_string(),
        format!("--proxy-server={}", cfg.proxy_addr()),
        format!("--user-data-dir={}", chrome_user_data_dir()),
    ]
}

/// 描述将要提交给服务器的认证方式。
fn auth_desc(cfg: &Config) -> &'static str {
    match (cfg.private_key.is_empty(), cfg.password.is_empty()) {
        (false, false) => "私钥 + 密码",
        (false, true) => "私钥",
        (true, false) => "密码",
        (true, true) => "未配置",
    }
}

fn stop_pending(rx: &mut UnboundedReceiver<()>) -> bool {
    matches!(rx.try_recv(), Ok(()))
}

/// 等待时长或停机信号；返回 true 表示收到停机信号。
async fn wait_or_stop(rx: &mut UnboundedReceiver<()>, d: Duration) -> bool {
    match tokio::time::timeout(d, rx.recv()).await {
        Ok(Some(())) => true,
        Ok(None) => true, // 发送端已释放
        Err(_) => false,
    }
}

/// 「连接 → 起代理 → 等断开 → 退避重连」主循环（对标 Go supervise）。
fn supervise(mgr: Arc<Manager>, stop: UnboundedReceiver<()>) {
    let result = std::panic::catch_unwind(std::panic::AssertUnwindSafe(|| {
        match tokio::runtime::Builder::new_multi_thread()
            .worker_threads(2)
            .enable_all()
            .build()
        {
            Ok(rt) => {
                rt.block_on(supervise_inner(mgr.clone(), stop));
                // 停掉残留的转发任务，避免线程挂在半死的 channel 上
                rt.shutdown_timeout(Duration::from_secs(2));
            }
            Err(e) => mgr.log(format!("创建 tokio runtime 失败: {}", e)),
        }
    }));
    if let Err(e) = result {
        eprintln!("[supervise] PANIC: {:?}", e);
        mgr.log(format!("后台线程崩溃: {:?}", e));
    }
}

async fn supervise_inner(mgr: Arc<Manager>, mut stop: UnboundedReceiver<()>) {
    let mut backoff = MIN_BACKOFF;
    let mut first = true;
    let mut attempt: u32 = 0;

    loop {
        if stop_pending(&mut stop) {
            break;
        }
        attempt += 1;

        let cfg = mgr.config();
        let target = format!("{}:{}", cfg.server_addr, cfg.server_port);

        if first {
            mgr.set_state(State::Connecting, target.clone());
            mgr.log(format!(
                "正在连接 {}（用户 {}，认证方式 {}）",
                target,
                cfg.username,
                auth_desc(&cfg)
            ));
        } else {
            mgr.set_state(State::Reconnecting, target.clone());
            mgr.log(format!("第 {} 次重连 {}", attempt, target));
        }

        let dial_start = Instant::now();
        let af = mgr.log_fun();
        match ssh::connect(&cfg, &move |m: String| af(m)).await {
            Err(e) => {
                mgr.log(format!(
                    "SSH 连接失败（耗时 {}）: {}",
                    go_duration(dial_start.elapsed()),
                    e
                ));
                mgr.set_state(State::Failed, e.clone());
                mgr.log(format!("{} 后重试", go_duration(backoff)));

                if wait_or_stop(&mut stop, backoff).await {
                    break;
                }
                backoff = (backoff * 2).min(MAX_BACKOFF);
                first = false;
                continue;
            }
            Ok((conn, banner)) => {
                let conn = Arc::new(conn);
                *mgr.ssh.lock().unwrap() = Some(conn.clone());
                backoff = MIN_BACKOFF;

                mgr.log(format!(
                    "SSH 已连接 {}@{}（耗时 {}，服务器版本 {}）",
                    cfg.username,
                    target,
                    go_duration(dial_start.elapsed()),
                    banner.trim()
                ));

                // socks5 代理。每轮重连都新建停机标志：上一轮 close()
                // 会把它永久置 true，复用会导致新 accept 循环启动即退出。
                let shutdown = Arc::new(AtomicBool::new(false));
                *mgr.shutdown.lock().unwrap() = Some(shutdown.clone());
                let log2 = mgr.log_fun();
                let sock_log = Box::new(move |s: &str| log2(s.to_string()));
                let socks = Arc::new(SocksServer::new(&cfg, conn.clone(), shutdown, sock_log));
                *mgr.socks.lock().unwrap() = Some(socks.clone());
                let (ready_tx, ready_rx) = oneshot::channel();
                let socks_task = tokio::spawn(async move { socks.run(ready_tx).await });

                // 给监听一点时间失败（端口占用等），再宣布连接成功
                if let Ok(Ok(Err(e))) = tokio::time::timeout(CONNECT_STALL_WINDOW, ready_rx).await
                {
                    mgr.log(format!("socks5 启动失败: {}", e));
                    mgr.set_state(State::Failed, e.clone());
                    mgr.teardown().await;
                    let _ = socks_task.await;

                    if wait_or_stop(&mut stop, backoff).await {
                        break;
                    }
                    first = false;
                    continue;
                }

                mgr.set_state(State::Connected, cfg.proxy_addr());
                mgr.log(format!("socks5 代理已就绪 {}", cfg.proxy_addr()));

                if first && cfg.use_chrome {
                    let _ = mgr.start_browser();
                }
                first = false;

                // 等 SSH 断开、socks5 出错或用户停止
                let reason = wait_disconnect(&mgr, &conn, &socks_task, &mut stop).await;
                match reason {
                    DisconnectReason::Stop => {
                        mgr.teardown().await;
                        let _ = socks_task.await;
                        break;
                    }
                    DisconnectReason::SshClosed => mgr.log("SSH 连接断开".to_string()),
                    DisconnectReason::SocksErr => {
                        mgr.log("socks5 代理异常退出".to_string());
                    }
                }

                // 清理本轮资源后重连
                mgr.teardown().await;
                let _ = socks_task.await;

                mgr.log(format!("{} 后重连", go_duration(MIN_BACKOFF)));
                if wait_or_stop(&mut stop, MIN_BACKOFF).await {
                    break;
                }
            }
        }
    }

    // 退出前兜底释放
    mgr.teardown().await;
    mgr.close_browser();
}

enum DisconnectReason {
    Stop,
    SshClosed,
    SocksErr,
}

async fn wait_disconnect(
    mgr: &Manager,
    conn: &SshConn,
    socks_task: &TaskHandle<()>,
    stop: &mut UnboundedReceiver<()>,
) -> DisconnectReason {
    loop {
        if stop_pending(stop) {
            return DisconnectReason::Stop;
        }
        // SSH 链路是否已断（russh 会话任务结束即为断开）
        if !conn.is_connected() {
            return DisconnectReason::SshClosed;
        }
        // socks5 accept 循环退出（非停机时视为异常）
        if socks_task.is_finished() {
            if mgr
                .shutdown
                .lock()
                .unwrap()
                .as_ref()
                .map(|s| !s.load(Ordering::Relaxed))
                .unwrap_or(true)
            {
                return DisconnectReason::SocksErr;
            }
        }
        tokio::time::sleep(Duration::from_millis(500)).await;
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
    fn backoff_caps() {
        let mut b = MIN_BACKOFF;
        for _ in 0..7 {
            b = (b * 2).min(MAX_BACKOFF);
        }
        assert_eq!(b, MAX_BACKOFF);
    }

    #[test]
    fn auth_desc_labels() {
        let mut c = Config::new_default();
        c.server_addr = "h".into();
        c.username = "u".into();
        c.password = "p".into();
        assert_eq!(auth_desc(&c), "密码");
        c.private_key = "/k".into();
        assert_eq!(auth_desc(&c), "私钥 + 密码");
        c.password.clear();
        assert_eq!(auth_desc(&c), "私钥");
        c.private_key.clear();
        assert_eq!(auth_desc(&c), "未配置");
    }

    #[test]
    fn duration_format() {
        assert_eq!(go_duration(Duration::from_millis(35)), "35ms");
        assert_eq!(go_duration(Duration::from_millis(1200)), "1.2s");
        assert_eq!(go_duration(Duration::from_millis(15000)), "15s");
    }
}
