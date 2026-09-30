use std::os::unix::process::CommandExt;
use std::process::{Child, Command, Stdio};
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant};

/// 无痕 Chrome 实例。以独立进程组启动，关闭时对整组发 SIGTERM，
/// 3 秒后仍不退出再发 SIGKILL——避免残留窗口/进程。
pub struct Chrome {
    child: Mutex<Option<Child>>,
    /// 启动时用的 --user-data-dir，用于找出真正承载窗口的 Chrome 进程。
    user_data_dir: String,
    stopping: AtomicBool,
    log: Box<dyn Fn(&str) + Send + Sync>,
}

impl Chrome {
    pub fn start(
        path: &str,
        args: &[String],
        user_data_dir: String,
        log: Box<dyn Fn(&str) + Send + Sync>,
    ) -> Result<Arc<Self>, String> {
        if !std::path::Path::new(path).exists() {
            return Err(format!("Chrome 不存在: {}", path));
        }
        let mut cmd = Command::new(path);
        cmd.args(args);
        cmd.process_group(0);
        cmd.stdin(Stdio::null());
        cmd.stdout(Stdio::null());
        cmd.stderr(Stdio::null());

        let child = cmd.spawn().map_err(|e| format!("启动 Chrome 失败: {}", e))?;
        Ok(Arc::new(Chrome {
            child: Mutex::new(Some(child)),
            user_data_dir,
            stopping: AtomicBool::new(false),
            log,
        }))
    }

    pub fn logf(&self, s: &str) {
        (self.log)(s);
    }

    /// 关闭 Chrome 并等待退出。可重复调用。
    ///
    /// 除了自己拉起的子进程，还要按 user-data-dir 找出「接管」窗口的进程：
    /// Chrome 是单实例的，用同一个 user-data-dir 再启动时，新进程会把窗口
    /// 丢给已存在的实例后立刻退出，这时手里这个子进程句柄是死的，只关它
    /// 会留下上一次拉起的窗口（例如 App 重启、上次异常退出没关干净）。
    pub fn close(&self) {
        if self.stopping.swap(true, Ordering::Relaxed) {
            return;
        }
        let mut child = self.child.lock().unwrap().take();
        let mut victims: Vec<i32> = Vec::new();

        // 已经自行退出 → 说明窗口在别的进程里，只记录待查的进程
        if let Some(c) = child.as_mut() {
            match c.try_wait() {
                Ok(Some(_)) => {}
                _ => victims.push(c.id() as i32),
            }
        }
        victims.extend(pids_using_dir(&self.user_data_dir));
        victims.sort_unstable();
        victims.dedup();

        if let Some(c) = child.as_mut() {
            let _ = c.kill();
            let _ = c.wait();
        }
        if victims.is_empty() {
            return;
        }
        self.logf(&format!("正在关闭 Chrome（{} 个进程）", victims.len()));

        // 先给整个进程组 SIGTERM；不是组长的进程再单独发一次
        for pid in &victims {
            unsafe {
                libc::kill(-*pid, libc::SIGTERM);
                libc::kill(*pid, libc::SIGTERM);
            }
        }

        let deadline = Instant::now() + Duration::from_secs(3);
        loop {
            victims.retain(|p| process_alive(*p));
            if victims.is_empty() || Instant::now() >= deadline {
                break;
            }
            std::thread::sleep(Duration::from_millis(100));
        }

        if !victims.is_empty() {
            self.logf("Chrome 未在 3 秒内退出，发送 SIGKILL");
            for pid in &victims {
                unsafe {
                    libc::kill(-*pid, libc::SIGKILL);
                    libc::kill(*pid, libc::SIGKILL);
                }
            }
        }
    }
}

/// 命令行里带这个 user-data-dir 的进程（Chrome 主进程及它的 Helper）。
fn pids_using_dir(dir: &str) -> Vec<i32> {
    let out = Command::new("pgrep").arg("-f").arg(dir).output();
    let Ok(out) = out else {
        return Vec::new();
    };
    String::from_utf8_lossy(&out.stdout)
        .lines()
        .filter_map(|l| l.trim().parse::<i32>().ok())
        .collect()
}

fn process_alive(pid: i32) -> bool {
    unsafe { libc::kill(pid, 0) == 0 }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn missing_chrome_errors() {
        let r = Chrome::start(
            "/nonexistent/Chrome",
            &[],
            "/tmp/ssh-tunnel-chrome".to_string(),
            Box::new(|_| {}),
        );
        assert!(r.is_err());
    }

    #[test]
    fn pids_of_absent_dir_are_empty() {
        assert!(pids_using_dir("/definitely/not/a/real/chrome/dir").is_empty());
    }
}
