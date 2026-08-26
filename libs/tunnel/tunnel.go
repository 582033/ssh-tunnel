package tunnel

import (
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"ssh-tunnel/libs/chrome"
	"ssh-tunnel/libs/config"
	"ssh-tunnel/libs/socks5"
)

type State int

const (
	StateStopped State = iota
	StateConnecting
	StateConnected
	StateReconnecting
	StateFailed
)

func (s State) String() string {
	switch s {
	case StateConnecting:
		return "连接中"
	case StateConnected:
		return "已连接"
	case StateReconnecting:
		return "重连中"
	case StateFailed:
		return "连接失败"
	default:
		return "已停止"
	}
}

// Manager 管理 SSH 隧道 + socks5 代理的生命周期，可反复 Start / Stop。
type Manager struct {
	cfg *config.Config

	// OnState 状态变化回调（用于更新界面状态灯与文案）
	OnState func(State, string)
	// OnLog 日志回调
	OnLog func(string)

	mu       sync.Mutex
	state    State
	detail   string
	running  bool
	stopChan chan struct{}
	sshConn  *ssh.Client
	socks    *socks5.Socks5Server
	browser  *chrome.StartupParams
	wg       sync.WaitGroup
}

func NewManager(cfg *config.Config) *Manager {
	return &Manager{cfg: cfg, state: StateStopped}
}

func (m *Manager) Config() *config.Config {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cfg
}

// SetConfig 替换配置，仅在停止状态下生效。
func (m *Manager) SetConfig(cfg *config.Config) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.running {
		return fmt.Errorf("请先停止隧道再重载配置")
	}
	m.cfg = cfg
	return nil
}

func (m *Manager) State() (State, string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state, m.detail
}

func (m *Manager) IsRunning() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.running
}

func (m *Manager) setState(s State, detail string) {
	m.mu.Lock()
	m.state, m.detail = s, detail
	cb := m.OnState
	m.mu.Unlock()
	if cb != nil {
		cb(s, detail)
	}
}

func (m *Manager) log(format string, args ...any) {
	m.mu.Lock()
	cb := m.OnLog
	m.mu.Unlock()
	if cb != nil {
		cb(fmt.Sprintf(format, args...))
	}
}

// Start 启动隧道。非阻塞，后台维护连接与自动重连。
func (m *Manager) Start() error {
	m.mu.Lock()
	if m.running {
		m.mu.Unlock()
		return fmt.Errorf("隧道已在运行")
	}
	m.running = true
	m.stopChan = make(chan struct{})
	stop := m.stopChan
	m.mu.Unlock()

	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		m.supervise(stop)
	}()
	return nil
}

// Stop 停止隧道并等待所有资源释放。
func (m *Manager) Stop() {
	m.mu.Lock()
	if !m.running {
		m.mu.Unlock()
		return
	}
	m.running = false
	close(m.stopChan)
	sshConn, socks, browser := m.sshConn, m.socks, m.browser
	m.sshConn, m.socks, m.browser = nil, nil, nil
	m.mu.Unlock()

	m.log("正在停止隧道…")

	if socks != nil {
		socks.Close()
	}
	if sshConn != nil {
		sshConn.Close()
	}
	if browser != nil {
		browser.Close()
	}

	m.wg.Wait()
	m.setState(StateStopped, "")
	m.log("隧道已停止")
}

// supervise 负责「连接 → 起代理 → 等断开 → 退避重连」这个循环。
func (m *Manager) supervise(stop <-chan struct{}) {
	const (
		minBackoff = 2 * time.Second
		maxBackoff = 60 * time.Second
	)
	backoff := minBackoff
	first := true
	attempt := 0

	for {
		select {
		case <-stop:
			return
		default:
		}

		attempt++
		target := net.JoinHostPort(m.cfg.ServerAddr, m.cfg.ServerPort)

		if first {
			m.setState(StateConnecting, m.cfg.ServerAddr+":"+m.cfg.ServerPort)
			m.log("正在连接 %s（用户 %s，认证方式 %s）",
				target, m.cfg.Username, authDesc(m.cfg))
		} else {
			m.setState(StateReconnecting, m.cfg.ServerAddr+":"+m.cfg.ServerPort)
			m.log("第 %d 次重连 %s", attempt, target)
		}

		dialStart := time.Now()
		sshConn, err := m.dialSSH()
		if err != nil {
			m.log("SSH 连接失败（耗时 %s）: %v",
				time.Since(dialStart).Round(time.Millisecond), err)
			m.setState(StateFailed, err.Error())
			m.log("%s 后重试", backoff)

			// 指数退避，避免连不上时疯狂重试
			select {
			case <-stop:
				return
			case <-time.After(backoff):
			}
			if backoff < maxBackoff {
				backoff *= 2
				if backoff > maxBackoff {
					backoff = maxBackoff
				}
			}
			first = false
			continue
		}

		backoff = minBackoff
		m.log("SSH 已连接 %s@%s（耗时 %s，服务器版本 %s）",
			m.cfg.Username, target,
			time.Since(dialStart).Round(time.Millisecond),
			string(sshConn.ServerVersion()))

		socks := &socks5.Socks5Server{
			ProxyPort: m.cfg.LocalPort,
			CustomDNS: m.cfg.CustomDNS,
			LogFunc:   func(s string) { m.log("%s", s) },
		}

		m.mu.Lock()
		if !m.running {
			m.mu.Unlock()
			socks.Close()
			sshConn.Close()
			return
		}
		m.sshConn, m.socks = sshConn, socks
		m.mu.Unlock()

		// socks5 服务在独立 goroutine 里阻塞运行
		socksErr := make(chan error, 1)
		go func() { socksErr <- socks.ProxyStart(sshConn) }()

		// 给监听一点时间失败（端口占用等），再宣布连接成功
		select {
		case err := <-socksErr:
			if err != nil {
				m.log("socks5 启动失败: %v", err)
				m.setState(StateFailed, err.Error())
				sshConn.Close()
				select {
				case <-stop:
					return
				case <-time.After(backoff):
				}
				first = false
				continue
			}
		case <-time.After(300 * time.Millisecond):
		}

		m.setState(StateConnected, m.cfg.ProxyAddr())
		m.log("socks5 代理已就绪 %s", m.cfg.ProxyAddr())

		if first && m.cfg.UseChrome {
			m.startBrowser()
		}
		first = false

		// 等 SSH 断开、socks5 出错或用户停止
		sshClosed := make(chan error, 1)
		go func() { sshClosed <- sshConn.Wait() }()

		select {
		case <-stop:
			return
		case err := <-sshClosed:
			m.log("SSH 连接断开: %v", err)
		case err := <-socksErr:
			if err != nil {
				m.log("socks5 代理异常退出: %v", err)
			}
		}

		// 清理本轮资源后重连
		socks.Close()
		sshConn.Close()

		m.mu.Lock()
		if m.sshConn == sshConn {
			m.sshConn = nil
		}
		if m.socks == socks {
			m.socks = nil
		}
		m.mu.Unlock()

		m.log("%s 后重连", minBackoff)
		select {
		case <-stop:
			return
		case <-time.After(minBackoff):
		}
	}
}

// authDesc 描述将要提交给服务器的认证方式，日志里说明「用什么在连」。
func authDesc(cfg *config.Config) string {
	switch {
	case cfg.PrivateKey != "" && cfg.Password != "":
		return "私钥 + 密码"
	case cfg.PrivateKey != "":
		return "私钥"
	case cfg.Password != "":
		return "密码"
	default:
		return "未配置"
	}
}

func (m *Manager) dialSSH() (*ssh.Client, error) {
	cfg := m.cfg
	clientConfig := &ssh.ClientConfig{
		User:    cfg.Username,
		Timeout: 15 * time.Second,
		// 沿用原实现：不校验主机密钥。
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	}

	// 两种凭据都提供时全部加入，由 SSH 协议按服务端支持的方式依次尝试。
	// 只挑一种的话，服务端若不接受该方式就直接失败（例如密钥没加到
	// authorized_keys，即使密码是对的也连不上）。
	var authErrs []string

	if cfg.PrivateKey != "" {
		signer, err := loadPrivateKey(cfg.PrivateKey)
		if err != nil {
			authErrs = append(authErrs, err.Error())
			m.log("私钥不可用，将尝试其他认证方式: %v", err)
		} else {
			clientConfig.Auth = append(clientConfig.Auth, ssh.PublicKeys(signer))
		}
	}

	if cfg.Password != "" {
		clientConfig.Auth = append(clientConfig.Auth,
			ssh.Password(cfg.Password),
			// 部分服务器只开 keyboard-interactive，不接受 password
			ssh.KeyboardInteractive(func(_, _ string, questions []string, _ []bool) ([]string, error) {
				answers := make([]string, len(questions))
				for i := range answers {
					answers[i] = cfg.Password
				}
				return answers, nil
			}),
		)
	}

	if len(clientConfig.Auth) == 0 {
		if len(authErrs) > 0 {
			return nil, fmt.Errorf("没有可用的认证方式: %s", strings.Join(authErrs, "; "))
		}
		return nil, fmt.Errorf("缺少 privateKey 或 password")
	}

	return ssh.Dial("tcp", net.JoinHostPort(cfg.ServerAddr, cfg.ServerPort), clientConfig)
}

func loadPrivateKey(path string) (ssh.Signer, error) {
	keyBytes, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取私钥失败: %w", err)
	}
	signer, err := ssh.ParsePrivateKey(keyBytes)
	if err != nil {
		return nil, fmt.Errorf("解析私钥失败（如私钥有密码短语，请改用无密码私钥或 password 认证）: %w", err)
	}
	return signer, nil
}

// StartBrowser 手动拉起 Chrome（界面「打开 Chrome」按钮用）
func (m *Manager) StartBrowser() {
	m.startBrowser()
}

// chromeArgs 无痕 Chrome 的启动参数。
//
// Chrome 对 socks5:// 代理默认就把域名交给代理解析（远端 DNS），
// 本机不会发出查询，因此不需要额外的解析规则。
// 曾经加过 --host-resolver-rules="MAP * ~NOTFOUND , EXCLUDE localhost"
// 想再兜一层，但那条规则连代理自身的地址一起挡掉了，
// 结果任何网页都是 ERR_PROXY_CONNECTION_FAILED。
func chromeArgs(proxyAddr string) []string {
	return []string{
		"--incognito",
		// 不做 DNS 预取，避免 Chrome 在代理之外抢跑解析
		"--dns-prefetch-disable",
		"--proxy-server=" + proxyAddr,
		"--user-data-dir=" + os.TempDir() + "/ssh-tunnel-chrome",
	}
}

func (m *Manager) startBrowser() {
	cfg := m.cfg
	if cfg.ChromePath == "" {
		m.log("未配置 chromePath")
		return
	}

	browser := &chrome.StartupParams{
		ChromePath: cfg.ChromePath,
		RunParams:  chromeArgs(cfg.ProxyAddr()),
		LogFunc:    func(s string) { m.log("%s", s) },
	}

	m.mu.Lock()
	old := m.browser
	m.browser = browser
	m.mu.Unlock()

	if old != nil {
		old.Close()
	}
	m.log("启动 Chrome: %s %s", cfg.ChromePath, strings.Join(browser.RunParams, " "))
	go browser.Start()
}
