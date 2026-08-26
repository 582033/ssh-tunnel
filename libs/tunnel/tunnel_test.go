package tunnel

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"ssh-tunnel/libs/config"
)

func TestManagerStartStop(t *testing.T) {
	sshAddr, stopSSH := newTestSSHServer(t)
	defer stopSSH()
	host, port, _ := net.SplitHostPort(sshAddr)

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "ok-via-manager")
	}))
	defer backend.Close()

	proxyPort := freePort(t)
	mgr := NewManager(&config.Config{
		Username:   "test",
		Password:   "x",
		AuthType:   config.AuthTypePassword,
		ServerAddr: host,
		ServerPort: port,
		LocalPort:  proxyPort,
	})

	var states []State
	var mu sync.Mutex
	mgr.OnState = func(s State, _ string) {
		mu.Lock()
		states = append(states, s)
		mu.Unlock()
	}

	if err := mgr.Start(); err != nil {
		t.Fatal(err)
	}
	// 重复 Start 应报错
	if err := mgr.Start(); err == nil {
		t.Fatal("重复 Start 应返回错误")
	}

	waitState(t, mgr, StateConnected)

	// 通过代理访问 backend
	body := httpViaProxy(t, proxyPort, backend.URL)
	if !strings.Contains(body, "ok-via-manager") {
		t.Fatalf("响应异常: %q", body)
	}

	mgr.Stop()
	if mgr.IsRunning() {
		t.Fatal("Stop 后 IsRunning 应为 false")
	}
	if s, _ := mgr.State(); s != StateStopped {
		t.Fatalf("Stop 后状态应为 StateStopped，实际 %v", s)
	}

	// 端口应已释放
	l, err := net.Listen("tcp", "127.0.0.1:"+proxyPort)
	if err != nil {
		t.Fatalf("Stop 后端口未释放: %v", err)
	}
	l.Close()

	// 重复 Stop 不应 panic 或阻塞
	mgr.Stop()
}

// TestManagerReconnects 验证 SSH 断开后能自动重连并恢复代理
func TestManagerReconnects(t *testing.T) {
	sshAddr, stopSSH := newTestSSHServer(t)
	defer stopSSH()
	host, port, _ := net.SplitHostPort(sshAddr)

	proxyPort := freePort(t)
	mgr := NewManager(&config.Config{
		Username: "test", Password: "x", AuthType: config.AuthTypePassword,
		ServerAddr: host, ServerPort: port, LocalPort: proxyPort,
	})
	if err := mgr.Start(); err != nil {
		t.Fatal(err)
	}
	defer mgr.Stop()

	waitState(t, mgr, StateConnected)

	// 从服务端强杀底层连接，模拟网络中断
	mgr.mu.Lock()
	conn := mgr.sshConn
	mgr.mu.Unlock()
	if conn == nil {
		t.Fatal("sshConn 为 nil")
	}
	conn.Close()

	// 应先离开 Connected，再重新回到 Connected
	waitStateChange(t, mgr, StateConnected)
	waitState(t, mgr, StateConnected)
}

// TestManagerStopWhileConnecting 在连接失败重试期间 Stop 不应卡死
func TestManagerStopWhileConnecting(t *testing.T) {
	// 指向一个必然拒绝连接的端口
	deadPort := freePort(t)
	mgr := NewManager(&config.Config{
		Username: "test", Password: "x", AuthType: config.AuthTypePassword,
		ServerAddr: "127.0.0.1", ServerPort: deadPort, LocalPort: freePort(t),
	})
	if err := mgr.Start(); err != nil {
		t.Fatal(err)
	}
	waitState(t, mgr, StateFailed)

	done := make(chan struct{})
	go func() { mgr.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("退避等待期间 Stop 卡死超过 5s")
	}
}

// TestChromeArgsNoHostResolverRules 回归测试：
// 曾经带过 --host-resolver-rules="MAP * ~NOTFOUND , EXCLUDE localhost"，
// 它把 127.0.0.1（代理自身）也解析成 NOTFOUND，
// 于是每个页面都是 ERR_PROXY_CONNECTION_FAILED，表现为「浏览器打不开网页」。
// socks5:// 代理本身已经是远端解析，这条规则纯属多余。
func TestChromeArgsNoHostResolverRules(t *testing.T) {
	args := chromeArgs("socks5://127.0.0.1:1081")

	for _, a := range args {
		if strings.HasPrefix(a, "--host-resolver-rules") {
			t.Fatalf("不应再传 host-resolver-rules，它会挡掉代理自身: %s", a)
		}
	}

	want := []string{"--incognito", "--proxy-server=socks5://127.0.0.1:1081"}
	for _, w := range want {
		if !slices.Contains(args, w) {
			t.Fatalf("缺少参数 %s，实际 %v", w, args)
		}
	}
}

func waitState(t *testing.T, mgr *Manager, want State) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if s, _ := mgr.State(); s == want {
			return
		}
		time.Sleep(30 * time.Millisecond)
	}
	s, d := mgr.State()
	t.Fatalf("等待状态 %v 超时，当前 %v (%s)", want, s, d)
}

func waitStateChange(t *testing.T, mgr *Manager, from State) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if s, _ := mgr.State(); s != from {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("状态始终停留在 %v", from)
}
