pub mod chrome;
pub mod config;
pub mod dns;
pub mod manager;
pub mod socks5;
pub mod ssh;

use crate::config::Config;
use crate::manager::{Manager, State};
use serde::Serialize;
use std::sync::OnceLock;
use std::sync::{Arc, Mutex};
use tauri::{Emitter, Manager as _, WindowEvent};

/// 全局应用状态：配置 store + 隧道 manager，进程内单例。
struct AppState {
    store: Mutex<config::Store>,
    load_error: Option<String>,
    mgr: Arc<Manager>,
    handle: Mutex<Option<tauri::AppHandle>>,
}

static STATE: OnceLock<Arc<AppState>> = OnceLock::new();

#[derive(Serialize)]
#[serde(rename_all = "camelCase")]
struct StoreView {
    names: Vec<String>,
    active: String,
    current: Config,
    path: String,
    load_error: Option<String>,
}

#[derive(Clone, Serialize)]
struct StatePayload {
    state: &'static str,
    detail: String,
}

#[derive(Clone, Serialize)]
struct LogPayload {
    line: String,
}

pub fn run() {
    let (store, load_error) = config::load_for_ui(None);
    let mgr = Arc::new(Manager::new(&store.current()));
    let st = AppState {
        store: Mutex::new(store),
        load_error,
        mgr,
        handle: Mutex::new(None),
    };
    if STATE.set(Arc::new(st)).is_err() {
        panic!("AppState already initialized");
    }

    tauri::Builder::default()
        .setup(|app| {
            let handle = app.handle().clone();
            let st = STATE.get().expect("state").clone();
            *st.handle.lock().unwrap() = Some(handle.clone());

            let h_state = handle.clone();
            let h_log = handle.clone();
            st.mgr.set_callbacks(
                Box::new(move |s: State, d: String| {
                    let _ = h_state.emit("state", StatePayload { state: s.as_str(), detail: d });
                }),
                Box::new(move |line: String| {
                    let _ = h_log.emit("log", LogPayload { line });
                }),
            );
            Ok(())
        })
        .on_window_event(|window, event| {
            // 单窗口应用：关窗即退出，由下面 RunEvent::Exit 兜底停隧道/关 Chrome
            if let WindowEvent::CloseRequested { api, .. } = event {
                api.prevent_close();
                window.app_handle().exit(0);
            }
        })
        .invoke_handler(tauri::generate_handler![
            get_store,
            save_config,
            set_active,
            add_connection,
            delete_connection,
            start,
            stop,
            start_browser
        ])
        .build(tauri::generate_context!())
        .expect("error while building tauri application")
        .run(|_app, event| {
            // 从 Dock / Cmd+Q / 关窗退出时兜底收尾：停隧道、关 Chrome
            if let tauri::RunEvent::Exit = event {
                if let Some(st) = STATE.get() {
                    st.mgr.stop();
                }
            }
        });
}

/// 全部连接与当前选中的连接（供界面初始化）。
#[tauri::command]
async fn get_store() -> StoreView {
    let st = STATE.get().expect("state").clone();
    let s = st.store.lock().unwrap();
    StoreView {
        names: s.names(),
        active: s.active.clone(),
        current: s.current(),
        path: s.path.display().to_string(),
        load_error: st.load_error.clone(),
    }
}

/// 校验并写回当前选中的连接（允许重命名），保存后同步给 Manager。
#[tauri::command]
async fn save_config(mut cfg: Config) -> Result<(), String> {
    cfg.validate()?;
    let st = STATE.get().expect("state").clone();
    {
        let mut s = st.store.lock().unwrap();
        let old = s.active.clone();
        s.replace(&old, cfg.clone())?;
        s.save()?;
    }
    st.mgr.set_config(&cfg);
    Ok(())
}

/// 切换当前选中的连接并落盘，同步给 Manager。
#[tauri::command]
async fn set_active(name: String) -> Result<(), String> {
    let st = STATE.get().expect("state").clone();
    let cur;
    {
        let mut s = st.store.lock().unwrap();
        s.set_active(&name)?;
        s.save()?;
        cur = s.current();
    }
    st.mgr.set_config(&cur);
    Ok(())
}

/// 新建一条连接并选中。端口顺延，默认不自动连。
/// 新建的连接会成为当前连接，而运行中不允许替换配置，所以先停隧道。
#[tauri::command]
async fn add_connection(name: String) -> Result<(), String> {
    let name = name.trim().to_string();
    if name.is_empty() {
        return Ok(());
    }
    let st = STATE.get().expect("state").clone();
    st.mgr.stop();
    let cur;
    {
        let mut s = st.store.lock().unwrap();
        if s.index_of(&name) >= 0 {
            return Err(format!(
                "已存在名为 {:?} 的连接，请换一个名字",
                name
            ));
        }
        let mut c = Config::new_default();
        c.name = name;
        c.auto_connect = false;
        c.local_port = next_free_port(&s);
        s.add(c);
        s.save()?;
        cur = s.current();
    }
    st.mgr.set_config(&cur);
    Ok(())
}

/// 删除一条连接（最后一条不允许删除）。删掉当前连接会顺延选中别的，
/// 因此同样要先停隧道。
#[tauri::command]
async fn delete_connection(name: String) -> Result<(), String> {
    let st = STATE.get().expect("state").clone();
    st.mgr.stop();
    let cur;
    {
        let mut s = st.store.lock().unwrap();
        s.remove(&name)?;
        s.save()?;
        cur = s.current();
    }
    st.mgr.set_config(&cur);
    Ok(())
}

/// 连接（或已连接时不动）。
#[tauri::command]
async fn start() -> Result<(), String> {
    let mgr = STATE.get().expect("state").mgr.clone();
    let cfg = mgr.config();
    let mut c = cfg;
    if let Err(e) = c.validate() {
        return Err(e);
    }
    mgr.start()
}

/// 断开并清理。
#[tauri::command]
async fn stop() {
    let mgr = STATE.get().expect("state").mgr.clone();
    mgr.stop();
}

/// 手动拉起无痕 Chrome。
#[tauri::command]
async fn start_browser() -> Result<(), String> {
    let mgr = STATE.get().expect("state").mgr.clone();
    mgr.start_browser()
}

/// 在已有连接的本地端口之后顺延一个空端口。
fn next_free_port(s: &config::Store) -> String {
    let used: Vec<String> = s.connections.iter().map(|c| c.local_port.clone()).collect();
    for p in 1081..1181 {
        let n = p.to_string();
        if !used.contains(&n) {
            return n;
        }
    }
    "1081".to_string()
}