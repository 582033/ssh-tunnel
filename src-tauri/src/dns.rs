use crate::ssh::SshConn;
use std::time::Duration;
use tokio::io::{AsyncReadExt, AsyncWriteExt};
use tokio::time::timeout;

/// 整个查询 10s 内完成，与 Go 版 Resolver 的超时一致。
const LOOKUP_TIMEOUT: Duration = Duration::from_secs(10);

/// 通过隧道向 DNS 服务器发起 A 记录查询（DNS over TCP，RFC 7766）。
/// 返回第一个 IP。解析超时、格式异常等一律由调用方自行决定回退。
pub async fn lookup_a(conn: &SshConn, server: &str, name: &str) -> Result<String, String> {
    let host_port: Vec<&str> = server.rsplitn(2, ':').collect();
    let (host, port) = match host_port.as_slice() {
        [port, host] => (*host, port.parse::<u32>().unwrap_or(53)),
        _ => (server, 53),
    };

    let query = build_query(name)?;
    let mut req = Vec::with_capacity(query.len() + 2);
    req.extend_from_slice(&(query.len() as u16).to_be_bytes());
    req.extend_from_slice(&query);

    let mut chan = conn.dial(host, port, LOOKUP_TIMEOUT).await?;

    timeout(LOOKUP_TIMEOUT, chan.write_all(&req))
        .await
        .map_err(|_| "DNS 查询超时（10s）".to_string())?
        .map_err(|e| format!("DNS 查询发送失败: {}", e))?;
    let _ = chan.flush().await;

    let mut len_buf = [0u8; 2];
    timeout(LOOKUP_TIMEOUT, chan.read_exact(&mut len_buf))
        .await
        .map_err(|_| "DNS 查询超时（10s）".to_string())?
        .map_err(|e| format!("DNS 查询读取失败: {}", e))?;

    let rlen = u16::from_be_bytes(len_buf) as usize;
    if rlen == 0 || rlen > 4096 {
        return Err("DNS 响应长度异常".to_string());
    }
    let mut buf = vec![0u8; rlen];
    timeout(LOOKUP_TIMEOUT, chan.read_exact(&mut buf))
        .await
        .map_err(|_| "DNS 查询超时（10s）".to_string())?
        .map_err(|e| format!("DNS 查询读取失败: {}", e))?;

    parse_a_response(&buf).ok_or_else(|| "无解析结果".to_string())
}

/// 构造一个标准 A 记录查询包
fn build_query(name: &str) -> Result<Vec<u8>, String> {
    let mut q = Vec::with_capacity(name.len() + 16);
    // ID：随机即可，并发多查询各自独立 channel
    q.extend_from_slice(&[0xAB, 0xCD]);
    q.extend_from_slice(&[0x01, 0x00]); // flags: RD
    q.extend_from_slice(&[0x00, 0x01]); // QDCOUNT
    q.extend_from_slice(&[0x00, 0x00, 0x00, 0x00, 0x00, 0x00]); // AN/NS/AR=0

    for label in name.split('.') {
        if label.is_empty() || label.len() > 63 {
            return Err(format!("域名非法: {}", name));
        }
        q.push(label.len() as u8);
        q.extend_from_slice(label.as_bytes());
    }
    q.push(0); // 根标签
    q.extend_from_slice(&[0x00, 0x01]); // QTYPE: A
    q.extend_from_slice(&[0x00, 0x01]); // QCLASS: IN
    Ok(q)
}

/// 从响应里取出第一个 A 记录的 IP。支持跳指针（0xC0）的压缩名。
fn parse_a_response(resp: &[u8]) -> Option<String> {
    if resp.len() < 12 {
        return None;
    }
    let flags = u16::from_be_bytes([resp[2], resp[3]]);
    if flags & 0x8000 == 0 {
        return None; // 不是响应
    }
    let qdcount = u16::from_be_bytes([resp[4], resp[5]]) as usize;
    let ancount = u16::from_be_bytes([resp[6], resp[7]]) as usize;
    if ancount == 0 {
        return None;
    }

    let mut pos = 12;
    // 跳过问题段
    for _ in 0..qdcount {
        pos = skip_name(resp, pos)?;
        pos += 4; // QTYPE + QCLASS
        if pos > resp.len() {
            return None;
        }
    }

    // 扫描应答段
    for _ in 0..ancount {
        pos = skip_name(resp, pos)?;
        if pos + 10 > resp.len() {
            return None;
        }
        let rtype = u16::from_be_bytes([resp[pos], resp[pos + 1]]);
        let rclass = u16::from_be_bytes([resp[pos + 2], resp[pos + 3]]);
        let rdlen = u16::from_be_bytes([resp[pos + 8], resp[pos + 9]]) as usize;
        let data = pos + 10;
        if data + rdlen > resp.len() {
            return None;
        }
        if rtype == 1 && rclass == 1 && rdlen == 4 {
            let ip = format!(
                "{}.{}.{}.{}",
                resp[data], resp[data + 1], resp[data + 2], resp[data + 3]
            );
            return Some(ip);
        }
        pos = data + rdlen;
    }
    None
}

fn skip_name(resp: &[u8], mut pos: usize) -> Option<usize> {
    loop {
        if pos >= resp.len() {
            return None;
        }
        let len = resp[pos] as usize;
        if len == 0 {
            return Some(pos + 1);
        }
        if len & 0xC0 == 0xC0 {
            // 压缩指针：直接跳过，目标由服务器保证合法
            return Some(pos + 2);
        }
        if len & 0xC0 != 0 {
            return None;
        }
        pos += 1 + len;
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn parse_simple_a() {
        // 手工构造：id 0xABCD, RD 置位, qd=1, an=1
        let mut r = vec![0xAB, 0xCD, 0x81, 0x80, 0x00, 0x01, 0x00, 0x01, 0, 0, 0, 0];
        // 问题: a.b (2) 
        r.extend_from_slice(b"\x01a\x01b\x00");
        r.extend_from_slice(&[0x00, 0x01, 0x00, 0x01]);
        // 答案: name 指针 -> c00c, type A, class IN, ttl, rdlen=4, 1.2.3.4
        r.extend_from_slice(&[0xC0, 0x0C]);
        r.extend_from_slice(&[0x00, 0x01, 0x00, 0x01, 0x00, 0x00, 0x00, 0x3C]);
        r.extend_from_slice(&[0x00, 0x04]);
        r.extend_from_slice(&[1, 2, 3, 4]);
        assert_eq!(parse_a_response(&r).unwrap(), "1.2.3.4");
    }

    #[test]
    fn query_shape() {
        let q = build_query("example.com").unwrap();
        assert_eq!(&q[..2], &[0xAB, 0xCD]);
        assert_eq!(&q[4..6], &[0x00, 0x01]);
        assert!(q.ends_with(&[0x00, 0x01, 0x00, 0x01]));
    }
}