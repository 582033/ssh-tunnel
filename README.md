## 功能

将 SSH 连接转发为本地 SOCKS5 代理，让需要联网的软件通过 SSH 服务器出网。

* macOS 图形界面应用，窗口内即可填写配置、启停隧道、查看日志
* 可保存多份连接配置，顶部下拉一键切换（同一时刻只连一个）
* SSH 断开后自动重连（指数退避 2s → 60s）
* 支持自定义 DNS，应对 DNS 污染
* 可选一键拉起无痕 Chrome，自动指向该代理
* 保留 CLI 模式（`-cli`），可在服务器上跑

## 构建

```bash
./build.sh                    # 生成 build/ssh-tunnel.app（amd64 + arm64 通用二进制）
cp -R build/ssh-tunnel.app /Applications/
```

构建脚本会做 ad-hoc 签名，避免 Gatekeeper 直接拦下未签名的 .app。首次打开若仍提示「无法验证开发者」，在「系统设置 → 隐私与安全性」里点「仍要打开」。

## 界面

顶部状态卡：左侧状态灯与状态文字（绿=已连接 / 黄=连接或重连中 / 红=失败 / 灰=已停止），右侧是连接下拉和 ＋ / 🗑 两个按钮。下拉里选哪一条，下面三个标签页和底部按钮就都作用在那一条上。

三个标签页：

* **状态** — 当前连接的服务器、用户名、认证方式、本地代理地址、域名解析方式、Chrome 行为，以及配置文件路径。
* **设置** — 编辑当前选中的那条连接。最上面的「连接名称」就是下拉里显示的名字，改它即为重命名。「保存」写回配置文件，「保存并连接」保存后立即重连。隧道运行中改配置会自动重启隧道使其生效。
* **日志** — 实时日志（保留最近 1000 条），每条出站连接带编号、目标和耗时，便于定位「打不开某个网站」是解析失败还是服务器出不去网。可一键复制全部或清空。

窗口底部固定三个按钮：连接/断开、打开 Chrome、复制代理地址。

## 多个连接

用顶部下拉旁的 ＋ 新建，🗑 删除（至少保留一条）。新建时会自动挑一个没被占用的本地端口（1081 起顺延），因此几条连接的代理地址互不冲突，浏览器里配好一次就不用改。

切换连接时，正在跑的隧道会先断开，再连上新选的那条；原本没连接则只是切过去、不自动连。「设置」页有未保存的修改时切换会先询问一次。选中项本身也会落盘，下次打开 App 还是这一条。

「打开 App 后自动连接」是每条连接各自的开关，只有被选中的那条生效。

首次打开若还没有配置文件，App 会提示并直接跳到「设置」页；填完保存即写入 `~/.config/ssh-tunnel/config.yaml`（权限 0600）。勾选「打开 App 后自动连接」后，之后双击 App 就会自动建立隧道并按需拉起 Chrome。关闭窗口或退出 App 时，隧道和它启动的 Chrome 会一并关掉。

## 域名解析

这一项直接决定「能不能打开网页」，有两种模式：

* **由 SSH 服务器解析（默认，推荐）** — 域名原样通过 SSH 转发请求送到服务器，由服务器完成解析。本机不发出任何 DNS 查询，不会泄漏，CDN 也会按服务器所在地返回就近节点。
* **指定 DNS** — 用指定的 DNS 服务器解析，且这条查询本身也走隧道（DNS over TCP），所以结果同样是服务器视角的。适合服务器本地 DNS 不可信的场景。

无论哪种模式，DNS 都不会从本机直接发出。

## 配置

配置文件按以下顺序查找第一个存在的：

1. `~/Library/Application Support/ssh-tunnel/config.yaml`（推荐，符合 macOS 惯例）
2. `~/.config/ssh-tunnel/config.yaml`（旧默认路径，仍会被读取）
3. `~/.ssh-tunnel.yaml`
4. 可执行文件同级的 `config.yaml` / `config/config.yaml`
5. `.app` 内的 `Contents/Resources/config.yaml`
6. `./config/config.yaml`

首次运行（且未用 `-config` 显式指定路径）时，若第 1 项还没有文件、第 2 项的旧配置存在，会自动把旧配置复制一份到新路径，旧文件原样保留、不会被删除；此后两处路径都可用，但 App 只认第 1 项。新路径下已经有文件时不会被覆盖。

也可用 `-config <路径>` 显式指定。一般不需要手写，用 App 的「设置」页即可；下面是文件格式，方便在服务器上跑 CLI 模式时手工准备。

```yaml
active: "公司"                        # 当前选中的连接，须与某条 name 对应

defaults:                             # 所有连接共用的默认值，可省略
  chromePath: "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
  customDNS: ""

connections:
  - name: "公司"                      # 下拉里显示的名字，同一文件内唯一
    username: "user"                  # 服务器用户名
    password: ""                      # 服务器密码
    privateKey: "~/.ssh/id_rsa"       # 私钥路径，支持 ~
    serverAddr: "example.com"         # 服务器地址
    serverPort: "22"                  # 服务器端口，留空默认 22
    localPort: "1081"                 # 本地 socks5 端口，留空默认 1081
    # customDNS、chromePath 不写则跟着 defaults 走
    useChrome: true                   # 是否同时启动本地 Chrome
    autoConnect: true                 # 打开 App 后自动连接这条
  - name: "家里"
    username: "user"
    password: "..."
    serverAddr: "home.example.com"
    localPort: "1082"
```

`defaults` 只放跟服务器身份无关、大概率所有连接都一样的字段，目前支持 `chromePath` 和 `customDNS`。某条连接自己填了同名字段就用自己的，留空才回退到 `defaults`；`defaults` 也留空则走各字段自身的默认值（如 `customDNS` 留空即服务器解析）。`serverAddr`/`username`/`password`/`privateKey`/`serverPort`/`localPort`/`autoConnect` 这类恰恰是用来区分各条连接的差异字段，不支持放进 `defaults`。

`defaults` 是可选的，不写不影响现有配置；App 「保存」时也不会把合并结果拍死写进每条连接，改一次 `defaults` 会联动所有没有自行覆盖该字段的连接。

password 和 privateKey 至少填一个，两个都填时会一并提交给服务器，由 SSH 协议协商用哪种。

`active` 指向不存在的名字时会回落到第一条；有连接没写 `name` 会自动按「用户名@地址」补上，重名则加序号后缀。

旧版的单连接格式（字段直接写在顶层、没有 `connections`）仍然能读，会被当成一条连接载入，在 App 里保存一次即升级为上面的格式。

配置文件含密码或私钥路径，注意不要提交到仓库。

## CLI 模式

```bash
./ssh-tunnel -cli -config ./config/config.yaml
```

CLI 模式连的是配置文件里 `active` 指向的那一条，启动时会打印用的是哪个连接。

## Chrome 启动参数

`--incognito --dns-prefetch-disable --proxy-server=socks5://127.0.0.1:<端口> --user-data-dir=$TMPDIR/ssh-tunnel-chrome`

Chrome 对 `socks5://` 代理默认就把域名交给代理解析（远端 DNS），本机不发出查询，所以不需要额外的解析规则。

曾经额外传过 `--host-resolver-rules="MAP * ~NOTFOUND , EXCLUDE localhost"` 想再兜一层，但 `MAP *` 把 `127.0.0.1`（代理自身的地址）也解析成 NOTFOUND，Chrome 连不上自己的代理，于是每个页面都是 `ERR_PROXY_CONNECTION_FAILED`。这个参数已移除，并有回归测试盯着（`TestChromeArgsNoHostResolverRules`）。

## 图标

`assets/icon.svg` 是源文件。改完执行 `./assets/make-icon.sh` 重新生成 `assets/icon.icns`（.app 图标）和 `libs/gui/appicon.png`（内嵌进程序，用于窗口图标和未打包运行时），然后再跑 `./build.sh`。

## 说明

* SOCKS5 只监听 `127.0.0.1`，不对局域网开放。
* 未校验 SSH 主机密钥（`InsecureIgnoreHostKey`），公共网络下有中间人风险。
* 私钥不支持密码短语，需用无密码私钥或改用 password 认证。
