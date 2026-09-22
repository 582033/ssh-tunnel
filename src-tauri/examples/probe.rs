use ssh_tunnel_lib::config;
use ssh_tunnel_lib::ssh;
use std::sync::Arc;
use std::time::{Duration, Instant};
use tokio::io::{AsyncReadExt, AsyncWriteExt};

/// 手动探针：连上配置里的服务器，验证并发拨号与中继。
///   cargo run --example probe
#[tokio::main]
async fn main() {
    let (store, _err) = config::load_for_ui(None);
    let cfg = store.current();
    let (conn, banner) = match ssh::connect(&cfg, &|m| println!("  auth: {m}")).await {
        Ok(v) => v,
        Err(e) => {
            println!("connect failed: {e}");
            return;
        }
    };
    println!("connected: {banner}");
    let conn = Arc::new(conn);

    // 并发拨一批被墙/不可达的域名，验证它们不会拖垮其它连接
    let mut hs = Vec::new();
    for (k, host) in [
        "clients2.google.com",
        "accounts.google.com",
        "www.google.com",
        "android.clients.google.com",
    ]
    .iter()
    .enumerate()
    {
        let conn = conn.clone();
        let host = *host;
        hs.push(tokio::spawn(async move {
            let t0 = Instant::now();
            match conn.dial(host, 443, Duration::from_secs(6)).await {
                Ok(_) => println!("  [{k}] {host} ok in {:?}", t0.elapsed()),
                Err(e) => println!("  [{k}] {host} fail in {:?}: {e}", t0.elapsed()),
            }
        }));
    }

    // 同时验证正常站点依旧能建连 + 中继：被墙域名不该阻塞它
    tokio::time::sleep(Duration::from_millis(200)).await;
    let t0 = Instant::now();
    match conn.dial("ifconfig.me", 80, Duration::from_secs(15)).await {
        Ok(mut ch) => {
            println!("ifconfig.me:80 dialed in {:?}", t0.elapsed());
            let req = b"GET / HTTP/1.0\r\nHost: ifconfig.me\r\n\r\n";
            if ch.write_all(req).await.is_err() {
                println!("  write failed");
                return;
            }
            let _ = ch.flush().await;
            let mut buf = vec![0u8; 2048];
            match tokio::time::timeout(Duration::from_secs(10), ch.read(&mut buf)).await {
                Ok(Ok(n)) => {
                    let head = String::from_utf8_lossy(&buf[..n.min(200)]);
                    println!("  relay got {} bytes: {}", n, head.lines().next().unwrap_or(""));
                }
                Ok(Err(e)) => println!("  read failed: {e}"),
                Err(_) => println!("  read timeout"),
            }
        }
        Err(e) => println!("ifconfig.me:80 dial ERR in {:?}: {e}", t0.elapsed()),
    }

    for h in hs {
        let _ = h.await;
    }
    println!("ssh still connected: {}", conn.is_connected());
    conn.close().await;
}
