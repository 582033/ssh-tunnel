# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## 项目概述

ssh-tunnel 是一个 macOS 图形界面应用（基于 Fyne），将 SSH 连接转发为本地 SOCKS5 代理，让需要联网的软件通过 SSH 服务器出网。支持多份连接配置、断线自动重连、自定义 DNS、一键拉起无痕 Chrome，并保留无 GUI 的 CLI 模式（`-cli`）可在服务器上运行。

## 常用命令

```bash
# 运行测试（全部包）
go test ./...

# 运行单个包的测试
go test ./libs/tunnel/...

# 运行单个测试函数
go test ./libs/chrome/... -run TestChromeArgsNoHostResolverRules

# 本地直接运行（GUI 模式）
go run .

# 本地直接运行（CLI 模式，无图形界面）
go run . -cli -config ./config/config.yaml

# 构建 macOS .app（amd64 + arm64 通用二进制，含 ad-hoc 签名）
./build.sh
```

`build.sh` 依赖 `CGO_ENABLED=1`（Fyne 需要 Cocoa）以及 `lipo`/`codesign`，仅在 macOS 上可用。图标源文件是 `assets/icon.svg`，改完后跑 `./assets/make-icon.sh` 重新生成 `assets/icon.icns` 和 `libs/gui/appicon.png`，再执行 `./build.sh`。

## 架构

### 核心数据流

`main.go` 解析 `-config` / `-cli` 两个 flag 后分两条路径：

- **CLI 模式**：`config.Load` → 校验失败直接退出；成功则用 `tunnel.NewManager(cur)` 启动，阻塞等 SIGINT/SIGTERM。
- **GUI 模式**：`config.LoadForUI` 即使配置缺失或非法也返回可编辑的 `Store`，把 `loadErr` 交给 `gui.Run` 处理——界面据此自动跳到「设置」页提示用户补全，而不是直接崩溃退出。

### 包职责

- **`libs/config`**：`Config`（单条连接）+ `Store`（一份配置文件里的多条连接，含 `Active` 指向当前选中项）。`Store` 负责重名去重、旧版单连接格式（字段直接在顶层、无 `connections` 列表）的兼容读取与升级、按 0600 权限落盘（含密码/私钥路径）。配置文件查找顺序见 `DefaultPaths()`：`~/Library/Application Support/ssh-tunnel/config.yaml`（推荐默认位置）→ `~/.config/ssh-tunnel/config.yaml`（旧默认路径，降级为回退项）→ `~/.ssh-tunnel.yaml` → 可执行文件同级 → `.app` 内 `Contents/Resources` → `./config/config.yaml`。首次读取（未显式指定 `-config`）时 `read()` 会调用 `migrateLegacyConfig()`：若旧路径存在配置且新路径尚无文件，自动复制一份到新路径，旧文件保留不删；新路径已有文件则跳过，绝不覆盖用户在新路径上的修改。
- **`libs/tunnel`**：`Manager` 是核心状态机，管理「SSH 连接 → 起 SOCKS5 代理 → 等断开 → 指数退避重连（2s → 60s）」的完整生命周期，通过 `OnState`/`OnLog` 回调把状态和日志推给上层（GUI 或 CLI）。`SetConfig` 只允许在停止状态下替换配置——运行中改配置需先 `Stop()`。两种认证方式（`password`、`private_key`）若都填会一并提交给服务器协商，避免只挑一种时因服务端不支持而连接失败。
- **`libs/socks5`**：`Socks5Server` 包装 `github.com/armon/go-socks5`，只监听 `127.0.0.1`（不对局域网开放），所有出站连接通过 SSH 隧道拨号，每条连接带编号记录耗时和结果。`resover.go` 中的 `MyResolver`/`remoteRewriter` 是关键机制：域名默认不在本机解析，而是通过 context 标记 + rewriter 把占位 IP 抹掉，让域名原样进入 SSH 的 `direct-tcpip` 请求，交给远端服务器解析——这样既不泄漏 DNS，CDN 也会按服务器所在地返回节点。`CustomDNS` 非空时改为 DNS over TCP 且查询本身也走隧道。
- **`libs/chrome`**：`StartupParams.Start`/`Close` 管理 Chrome 子进程生命周期，用进程组（`Setpgid`）而非单个 PID 来清理，因为 Chrome 会派生大量 Helper 子进程；`Close` 先 SIGTERM 给 3 秒宽限期，超时再 SIGKILL。`chromeArgs` 特意不加 `--host-resolver-rules="MAP * ~NOTFOUND"`，因为该规则会把 `127.0.0.1`（代理自身地址）也解析成 NOTFOUND，导致 Chrome 连不上自己的代理（回归测试 `TestChromeArgsNoHostResolverRules` 盯着这一点）。
- **`libs/gui`**：Fyne 界面，`App` 持有 `store`（全部连接）和 `mgr`（只认当前选中的一份 `tunnel.Manager`）。三个标签页——状态（只读展示当前连接信息）、设置（编辑表单，`settings.go`）、日志（`widget.MultiLineEntry`，异步 flush，见 `logFlushEvery`/`maxLogLines`）。切换顶部连接下拉会先断开旧隧道再连新的；「设置」页有未保存修改时切换会先询问。

### 跨包的关键约定

- **重连不是新建 Manager**：`Manager.supervise` 内部循环处理重连，`sshConn`/`socks`/`browser` 在每轮迭代中被替换，`Stop()` 通过关闭 `stopChan` 中断循环并等待 `wg.Wait()`。
- **DNS 解析策略贯穿 socks5 → tunnel → gui**：`Config.CustomDNS` 为空是默认且推荐路径（服务器侧解析），三层代码（`resover.go` 的 `MyResolver`、状态页的展示文案、配置文件注释）都围绕这一约定。修改解析逻辑时要同步检查这几处的说明是否仍然准确。
- **认证信息不做单选**：`Config.Validate` 只要求 password/privateKey 至少填一个，两个都填是合法且推荐的配置（见 `dialSSH` 中的 `authErrs` 处理逻辑），不要假设只有一种认证方式生效。
- **不校验 SSH 主机密钥**（`ssh.InsecureIgnoreHostKey()`），公共网络下有中间人风险——这是已知的现有行为，非本次改动引入的问题时不必修复。

## 测试

各包均有 `_test.go`，`libs/socks5` 和 `libs/tunnel` 有 `testhelper_test.go` 提供测试专用的辅助（如假 SSH 服务器）。改动 `chromeArgs` 或 DNS 解析逻辑时，运行对应包测试确认没有破坏已知回归点（如 `TestChromeArgsNoHostResolverRules`）。
