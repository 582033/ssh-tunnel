use serde::{Deserialize, Serialize};
use std::env;
use std::fmt;
use std::fs;
use std::path::{Path, PathBuf};

pub const DEFAULT_PROFILE_NAME: &str = "默认连接";
const DEFAULT_CHROME_PATH: &str = "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome";
const CONFIG_HEADER: &str = "# ssh-tunnel 配置（由 App 生成，可手动编辑）\n# active 指向当前选中的连接，connections 下可以放多份。\n";

#[derive(Clone, Debug)]
pub enum AuthType {
    Password,
    PrivateKey,
}

impl fmt::Display for AuthType {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(f, "{}", if matches!(self, AuthType::PrivateKey) { "private_key" } else { "password" })
    }
}

#[derive(Clone, Debug, Serialize, Deserialize, PartialEq)]
#[serde(rename_all = "camelCase", default)]
pub struct Config {
    pub name: String,
    pub username: String,
    pub password: String,
    #[serde(rename = "privateKey")]
    pub private_key: String,
    #[serde(rename = "serverAddr")]
    pub server_addr: String,
    #[serde(rename = "serverPort")]
    pub server_port: String,
    #[serde(rename = "localPort")]
    pub local_port: String,
    #[serde(rename = "chromePath")]
    pub chrome_path: String,
    #[serde(rename = "useChrome")]
    pub use_chrome: bool,
    #[serde(rename = "customDNS")]
    pub custom_dns: String,
    #[serde(rename = "autoConnect")]
    pub auto_connect: bool,
}

impl Default for Config {
    /// 供 serde 缺字段回退与新建连接时使用；行为与 new_default 一致。
    fn default() -> Self {
        Config::new_default()
    }
}

impl Config {
    pub fn new_default() -> Self {
        Config {
            name: String::new(),
            username: String::new(),
            password: String::new(),
            private_key: String::new(),
            server_addr: String::new(),
            server_port: "22".to_string(),
            local_port: "1081".to_string(),
            chrome_path: DEFAULT_CHROME_PATH.to_string(),
            use_chrome: true,
            custom_dns: String::new(),
            auto_connect: true,
        }
    }

    /// 校验并填充默认值。名字的唯一性由 Store 负责。
    pub fn validate(&mut self) -> Result<(), String> {
        self.name = self.name.trim().to_string();
        self.server_addr = self.server_addr.trim().to_string();
        self.username = self.username.trim().to_string();
        self.server_port = self.server_port.trim().to_string();
        self.local_port = self.local_port.trim().to_string();
        self.custom_dns = self.custom_dns.trim().to_string();
        self.private_key = self.private_key.trim().to_string();

        if self.server_addr.is_empty() {
            return Err("请填写服务器地址".to_string());
        }
        if self.username.is_empty() {
            return Err("请填写用户名".to_string());
        }
        if self.server_port.is_empty() {
            self.server_port = "22".to_string();
        } else if !valid_port(&self.server_port) {
            return Err(format!("服务器端口无效: {}", self.server_port));
        }
        if self.local_port.is_empty() {
            self.local_port = "1081".to_string();
        } else if !valid_port(&self.local_port) {
            return Err(format!("本地端口无效: {}", self.local_port));
        }

        if !self.private_key.is_empty() {
            self.private_key = expand_home(&self.private_key);
        } else if self.password.is_empty() {
            return Err("请填写密码或私钥路径（至少一项）".to_string());
        }

        if !self.custom_dns.is_empty() && !self.custom_dns.contains(':') {
            self.custom_dns.push_str(":53");
        }

        if self.use_chrome && self.chrome_path.is_empty() {
            self.chrome_path = DEFAULT_CHROME_PATH.to_string();
        }
        Ok(())
    }

    /// 本地 socks5 代理地址
    pub fn proxy_addr(&self) -> String {
        format!("socks5://127.0.0.1:{}", self.local_port)
    }

    /// 展示用名称，未命名时退回「用户名@地址」
    pub fn label(&self) -> String {
        let n = self.name.trim();
        if !n.is_empty() {
            return n.to_string();
        }
        if self.server_addr.is_empty() {
            return DEFAULT_PROFILE_NAME.to_string();
        }
        if self.username.is_empty() {
            return self.server_addr.clone();
        }
        format!("{}@{}", self.username, self.server_addr)
    }
}

fn valid_port(s: &str) -> bool {
    match s.parse::<i32>() {
        Ok(n) => n > 0 && n < 65536,
        Err(_) => false,
    }
}

fn expand_home(p: &str) -> String {
    if !p.starts_with('~') {
        return p.to_string();
    }
    match home_dir() {
        Some(home) => {
            // 与 Go filepath.Join 语义一致：去掉 ~ 与开头的斜杠后拼到 home 下
            let rest = p.trim_start_matches('~').trim_start_matches('/');
            home.join(rest).display().to_string()
        }
        None => p.to_string(),
    }
}

fn home_dir() -> Option<PathBuf> {
    dirs::home_dir()
}

/// 所有连接共用的顶层默认值。某条连接自己填了就用自己的，留空才回退。
#[derive(Clone, Debug, Default, Serialize, Deserialize, PartialEq)]
pub struct Defaults {
    #[serde(rename = "chromePath", skip_serializing_if = "String::is_empty")]
    pub chrome_path: String,
    #[serde(rename = "customDNS", skip_serializing_if = "String::is_empty")]
    pub custom_dns: String,
}

impl Defaults {
    /// 把已填字段回填进 connections 里对应为空的字段
    pub fn apply_to(&self, connections: &mut [Config]) {
        let persist = !self.chrome_path.is_empty();
        let dns = !self.custom_dns.is_empty();
        for c in connections.iter_mut() {
            if persist && c.chrome_path.is_empty() {
                c.chrome_path = self.chrome_path.clone();
            }
            if dns && c.custom_dns.is_empty() {
                c.custom_dns = self.custom_dns.clone();
            }
        }
    }

    /// apply_to 的逆操作：把等于 d 对应值的字段清空
    pub fn strip_from(&self, connections: &mut [Config]) {
        let persist = !self.chrome_path.is_empty();
        let dns = !self.custom_dns.is_empty();
        for c in connections.iter_mut() {
            if persist && c.chrome_path == self.chrome_path {
                c.chrome_path.clear();
            }
            if dns && c.custom_dns == self.custom_dns {
                c.custom_dns.clear();
            }
        }
    }
}

#[derive(Clone, Debug, Serialize, Deserialize)]
pub struct Store {
    #[serde(rename = "active", default)]
    pub active: String,
    #[serde(rename = "defaults", skip_serializing_if = "Option::is_none")]
    pub defaults: Option<Defaults>,
    #[serde(rename = "connections", default)]
    pub connections: Vec<Config>,

    /// 配置实际来源（YAML 中不落盘）
    #[serde(skip)]
    pub path: PathBuf,
}

impl Default for Store {
    fn default() -> Self {
        let mut c = Config::new_default();
        c.name = DEFAULT_PROFILE_NAME.to_string();
        Store {
            active: c.name.clone(),
            connections: vec![c],
            defaults: None,
            path: default_paths()[0].clone(),
        }
    }
}

fn app_support_config_path() -> Option<PathBuf> {
    home_dir().map(|h| h.join("Library/Application Support/ssh-tunnel/config.yaml"))
}

fn legacy_config_path() -> Option<PathBuf> {
    home_dir().map(|h| h.join(".config/ssh-tunnel/config.yaml"))
}

/// 按优先级排列的配置搜索路径
pub fn default_paths() -> Vec<PathBuf> {
    let mut paths: Vec<PathBuf> = Vec::new();
    if let Some(p) = app_support_config_path() {
        paths.push(p);
    }
    if let Some(p) = legacy_config_path() {
        paths.push(p);
    }
    if let Some(home) = home_dir() {
        paths.push(home.join(".ssh-tunnel.yaml"));
    }
    if let Ok(exe) = env::current_exe() {
        if let Ok(exe) = fs::canonicalize(&exe) {
            if let Some(dir) = exe.parent() {
                paths.push(dir.join("config.yaml"));
                paths.push(dir.join("config/config.yaml"));
                paths.push(dir.join("../Resources/config.yaml"));
            }
        }
    }
    paths.push(PathBuf::from("./config/config.yaml"));
    paths
}

/// 把旧路径配置复制到 Application Support。不删除旧文件；出错不阻塞。
fn migrate_legacy_config() -> (bool, Option<String>) {
    let (Some(new_path), Some(old_path)) = (app_support_config_path(), legacy_config_path()) else {
        return (false, None);
    };
    if new_path.exists() {
        return (false, None);
    }
    let Ok(data) = fs::read(&old_path) else {
        return (false, None);
    };
    if let Some(parent) = new_path.parent() {
        if fs::create_dir_all(parent).is_err() {
            return (false, None);
        }
    }
    match write_private(&new_path, &data) {
        Ok(_) => (true, None),
        Err(e) => (false, Some(e)),
    }
}

fn write_private(path: &Path, data: &[u8]) -> Result<(), String> {
    use std::io::Write;
    use std::os::unix::fs::OpenOptionsExt;
    let mut f = fs::OpenOptions::new()
        .write(true)
        .create(true)
        .truncate(true)
        .mode(0o600)
        .open(path)
        .map_err(|e| e.to_string())?;
    f.write_all(data).map_err(|e| e.to_string())?;
    f.sync_all().map_err(|e| e.to_string())
}

fn abs_path(p: &Path) -> PathBuf {
    if p.is_absolute() {
        p.to_path_buf()
    } else if let Ok(cwd) = env::current_dir() {
        cwd.join(p)
    } else {
        p.to_path_buf()
    }
}

/// 读取配置并校验当前选中的连接。path 为空时按默认顺序查找。
pub fn load(path: Option<&Path>) -> Result<Store, String> {
    let s = read(path)?;
    let mut cur = s.current().clone();
    cur.validate()?;
    Ok(s)
}

/// 供界面使用：始终返回可编辑 Store，即使文件缺失或字段不合法。
pub fn load_for_ui(path: Option<&Path>) -> (Store, Option<String>) {
    let mut def = Store::default();
    match path {
        Some(p) => def.path = abs_path(p),
        None => def.path = default_paths()[0].clone(),
    }
    match read(path) {
        Ok(s) => {
            let err = s.current().validate().err();
            (s, err)
        }
        Err(e) => (def, Some(e)),
    }
}

pub fn new_store() -> Store {
    Store::default()
}

fn read(path: Option<&Path>) -> Result<Store, String> {
    let path: PathBuf = match path {
        Some(p) => p.to_path_buf(),
        None => {
            migrate_legacy_config();
            let p = default_paths().iter().find(|p| p.exists()).cloned();
            match p {
                Some(p) => p,
                None => return Err("未找到配置文件，请在界面中填写后保存".to_string()),
            }
        }
    };

    let ext = path
        .extension()
        .and_then(|e| e.to_str())
        .unwrap_or("")
        .to_lowercase();
    if ext != "yaml" && ext != "yml" {
        return Err("配置文件后缀必须为 .yaml 或 .yml".to_string());
    }

    let data = fs::read(&path).map_err(|e| e.to_string())?;
    let text = String::from_utf8_lossy(&data).into_owned();
    let mut s: Store = serde_yaml::from_str(&text)
        .map_err(|e| format!("解析配置文件失败: {}", e))?;

    // 旧格式：字段直接写在顶层，没有 connections 列表
    if s.connections.is_empty() {
        let mut legacy: Config = serde_yaml::from_str(&text)
            .map_err(|e| format!("解析配置文件失败: {}", e))?;
        if legacy.name.trim().is_empty() {
            legacy.name = DEFAULT_PROFILE_NAME.to_string();
        }
        let name = legacy.name.clone();
        s.connections = vec![legacy];
        s.active = name;
    }

    s.normalize();
    if let Some(d) = s.defaults.clone() {
        d.apply_to(&mut s.connections);
    }
    s.path = abs_path(&path);
    Ok(s)
}

/// 补齐名字、去重、确保 Active 指向真实存在的一条
impl Store {
    fn normalize(&mut self) {
        if self.connections.is_empty() {
            self.connections = vec![Config::new_default()];
        }
        let mut used: Vec<String> = Vec::with_capacity(self.connections.len());
        let mut conns: Vec<Config> = Vec::with_capacity(self.connections.len());
        for c in self.connections.drain(..) {
            let mut name = c.name.trim().to_string();
            let c2: Config = c;
            let label = c2.label();
            if name.is_empty() {
                name = label;
            }
            if used.contains(&name) {
                name = unique_name(&used, &name);
            }
            let mut cc = c2;
            cc.name = name.clone();
            used.push(name);
            conns.push(cc);
        }
        self.connections = conns;
        if self.index_of(&self.active) < 0 {
            self.active = self.connections[0].name.clone();
        }
    }

    pub fn index_of(&self, name: &str) -> isize {
        for (i, c) in self.connections.iter().enumerate() {
            if c.name == name {
                return i as isize;
            }
        }
        -1
    }

    /// 当前选中的连接副本。调用方按需要 clone。
    pub fn current(&self) -> Config {
        let i = self.index_of(&self.active);
        if i >= 0 {
            self.connections[i as usize].clone()
        } else {
            let mut s = self.clone();
            s.normalize();
            s.current()
        }
    }

    pub fn names(&self) -> Vec<String> {
        self.connections.iter().map(|c| c.name.clone()).collect()
    }

    pub fn set_active(&mut self, name: &str) -> Result<(), String> {
        if self.index_of(name) < 0 {
            return Err(format!("没有名为 {:?} 的连接", name));
        }
        self.active = name.to_string();
        Ok(())
    }

    pub fn replace(&mut self, name: &str, mut c: Config) -> Result<(), String> {
        let i = self.index_of(name);
        if i < 0 {
            return Err(format!("没有名为 {:?} 的连接", name));
        }
        c.name = c.name.trim().to_string();
        if c.name.is_empty() {
            c.name = c.label();
        }
        let j = self.index_of(&c.name);
        if j >= 0 && j != i {
            return Err(format!("已存在名为 {:?} 的连接，请换一个名字", c.name));
        }
        let i = i as usize;
        self.connections[i] = c;
        self.active = self.connections[i].name.clone();
        Ok(())
    }

    pub fn add(&mut self, mut c: Config) {
        let used: Vec<String> = self.connections.iter().map(|e| e.name.clone()).collect();
        let mut name = c.name.trim().to_string();
        if name.is_empty() {
            name = "新连接".to_string();
        }
        if used.contains(&name) {
            name = unique_name(&used, &name);
        }
        c.name = name.clone();
        self.connections.push(c);
        self.active = name.clone();
    }

    pub fn remove(&mut self, name: &str) -> Result<(), String> {
        if self.connections.len() <= 1 {
            return Err("至少要保留一个连接".to_string());
        }
        let i = self.index_of(name);
        if i < 0 {
            return Err(format!("没有名为 {:?} 的连接", name));
        }
        let i = i as usize;
        self.connections.remove(i);
        if self.active == name {
            let mut idx = i;
            if idx >= self.connections.len() {
                idx = self.connections.len() - 1;
            }
            self.active = self.connections[idx].name.clone();
        }
        Ok(())
    }

    /// 写回 Path（0600，含密码/私钥路径）
    pub fn save(&mut self) -> Result<(), String> {
        if self.path.as_os_str().is_empty() {
            self.path = default_paths()[0].clone();
        }
        if let Some(parent) = self.path.parent() {
            fs::create_dir_all(parent)
                .map_err(|e| format!("创建配置目录失败: {}", e))?;
        }
        self.normalize();

        if let Some(d) = self.defaults.clone() {
            d.strip_from(&mut self.connections);
        }

        let body = serde_yaml::to_string(self).map_err(|e| format!("序列化配置失败: {}", e))?;
        let data = format!("{}{}", CONFIG_HEADER, body);
        write_private(&self.path, data.as_bytes())?;
        fs::set_permissions(&self.path, fs::Permissions::from_mode(0o600))
            .map_err(|e| format!("设置配置文件权限失败: {}", e))?;

        if let Some(d) = self.defaults.clone() {
            d.apply_to(&mut self.connections);
        }
        Ok(())
    }
}

fn unique_name(used: &[String], base: &str) -> String {
    for i in 2.. {
        let name = format!("{} {}", base, i);
        if !used.contains(&name) {
            return name;
        }
    }
    unreachable!()
}

use std::os::unix::fs::PermissionsExt;

#[cfg(test)]
mod tests {
    use super::*;
    use std::io::Write;

fn tmp_config_dir(tag: &str) -> PathBuf {
    let d = std::env::temp_dir().join(format!(
        "ssh-tunnel-test-{}-{}",
        std::process::id(),
        tag
    ));
    let _ = fs::remove_dir_all(&d);
    fs::create_dir_all(&d).unwrap();
    d
}

    #[test]
    fn validate_defaults() {
        let mut c = Config::new_default();
        c.server_addr = "h".into();
        c.username = "u".into();
        c.use_chrome = true;
        c.password = "p".into();
        c.validate().unwrap();
        assert_eq!(c.server_port, "22");
        assert_eq!(c.local_port, "1081");
    }

    #[test]
    fn validate_requires_addr() {
        let mut c = Config::new_default();
        let err = c.validate().unwrap_err();
        assert!(err.contains("服务器地址"));
    }

    #[test]
    fn validate_appends_dns_port() {
        let mut c = Config::new_default();
        c.server_addr = "h".into();
        c.username = "u".into();
        c.password = "p".into();
        c.custom_dns = "8.8.8.8".into();
        c.validate().unwrap();
        assert_eq!(c.custom_dns, "8.8.8.8:53");
    }

    #[test]
    fn validate_expands_home_key() {
        let mut c = Config::new_default();
        c.server_addr = "h".into();
        c.username = "u".into();
        c.private_key = "~/.ssh/id_a".into();
        c.validate().unwrap();
        assert!(c.private_key.starts_with(&home_dir().unwrap().display().to_string()));
        assert!(c.private_key.ends_with(".ssh/id_a"));
    }

    #[test]
    fn normalize_uniquifies_names() {
        let mut s = Store::default();
        s.connections.clear();
        s.connections.push(Config { name: "a".into(), ..Config::new_default() });
        s.connections.push(Config { name: "a".into(), ..Config::new_default() });
        s.active = "a".into();
        s.normalize();
        assert_eq!(s.names(), vec!["a".to_string(), "a 2".to_string()]);
    }

    #[test]
    fn normalize_fixes_active() {
        let mut s = Store::default();
        s.connections = vec![
            Config { name: "a".into(), ..Config::new_default() },
            Config { name: "b".into(), ..Config::new_default() },
        ];
        s.active = "nope".into();
        s.normalize();
        assert_eq!(s.active, "a");
    }

    #[test]
    fn save_then_read_roundtrip() {
        let dir = tmp_config_dir("roundtrip");
        let p = dir.join("config.yaml");
        let mut s = Store::default();
        s.path = p.clone();
        s.connections[0].server_addr = "example.com".into();
        s.connections[0].username = "root".into();
        s.connections[0].password = "secret".into();
        s.connections[0].custom_dns = "1.1.1.1:53".into();
        s.defaults = Some(Defaults { chrome_path: "/x/Chrome".into(), custom_dns: "1.1.1.1:53".into() });
        s.save().unwrap();

        let data = fs::read(&p).unwrap();
        assert!(data.starts_with(CONFIG_HEADER.as_bytes()));
        let text = String::from_utf8(data).unwrap();
        assert!(text.contains("connections:"));
        assert!(text.contains("serverAddr: example.com"));

        // 与 Go 一样用 camelCase 落盘（customDNS / custom_dns 的区别可验证）
        assert!(text.contains("customDNS:"));
        assert!(!text.contains("custom_dns"));

        let s2 = load(Some(&p)).unwrap();
        assert_eq!(s2.current().server_addr, "example.com");
        assert_eq!(s2.current().custom_dns, "1.1.1.1:53");

        fs::remove_dir_all(&dir).ok();
    }

    #[test]
    fn reads_legacy_flat_format() {
        let dir = tmp_config_dir("legacy");
        let p = dir.join("config.yaml");
        {
            let mut f = fs::File::create(&p).unwrap();
            f.write_all(b"serverAddr: old.example\nusername: admin\npassword: pw\nserverPort: '22'\nlocalPort: '1081'\n").unwrap();
        }
        let s = load(Some(&p)).unwrap();
        assert_eq!(s.current().name, DEFAULT_PROFILE_NAME);
        assert_eq!(s.current().server_addr, "old.example");
        fs::remove_dir_all(&dir).ok();
    }

    #[test]
    fn remove_keeps_at_least_one() {
        let mut s = Store::default();
        assert!(s.remove("默认连接").is_err());
    }

    #[test]
    fn replace_renames_and_rejects_dup() {
        let mut s = Store::default();
        s.connections.push(Config { name: "b".into(), ..Config::new_default() });
        let mut c = Config::new_default();
        c.name = "renamed".into();
        s.replace("默认连接", c).unwrap();
        assert_eq!(s.active, "renamed");
        let dup = Config { name: "b".into(), ..Config::new_default() };
        assert!(s.replace("renamed", dup).is_err());
    }

    #[test]
    fn add_defaults_unique_name() {
        let mut s = Store::default();
        let a = Config { name: "新连接".into(), ..Config::new_default() };
        s.add(a);
        let b = Config { name: "新连接".into(), ..Config::new_default() };
        s.add(b);
        assert_eq!(s.names(), vec!["默认连接", "新连接", "新连接 2"]);
    }
}