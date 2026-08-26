package socks5

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// socks5Get 用 SOCKS5 协议（无认证、IPv4 地址类型）向 proxy 发起一次
// 到 targetHost:targetPort 的连接，并做一次极简 HTTP GET。
func socks5Get(proxyPort, targetHost, targetPort string) (string, error) {
	conn, err := net.DialTimeout("tcp", "127.0.0.1:"+proxyPort, 5*time.Second)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(10 * time.Second))

	// 握手：VER=5, NMETHODS=1, METHOD=0(无认证)
	if _, err := conn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		return "", err
	}
	resp := make([]byte, 2)
	if _, err := io.ReadFull(conn, resp); err != nil {
		return "", err
	}
	if resp[0] != 0x05 || resp[1] != 0x00 {
		return "", fmt.Errorf("握手失败: %v", resp)
	}

	// CONNECT 请求，ATYP=1(IPv4)
	ip := net.ParseIP(targetHost).To4()
	if ip == nil {
		return "", fmt.Errorf("需要 IPv4 地址")
	}
	port, _ := strconv.Atoi(targetPort)
	req := []byte{0x05, 0x01, 0x00, 0x01}
	req = append(req, ip...)
	req = binary.BigEndian.AppendUint16(req, uint16(port))
	if _, err := conn.Write(req); err != nil {
		return "", err
	}

	// 回复：VER REP RSV ATYP + BND.ADDR + BND.PORT
	head := make([]byte, 4)
	if _, err := io.ReadFull(conn, head); err != nil {
		return "", err
	}
	if head[1] != 0x00 {
		return "", fmt.Errorf("CONNECT 被拒绝, REP=%d", head[1])
	}
	switch head[3] {
	case 0x01:
		io.ReadFull(conn, make([]byte, 4+2))
	case 0x04:
		io.ReadFull(conn, make([]byte, 16+2))
	case 0x03:
		n := make([]byte, 1)
		io.ReadFull(conn, n)
		io.ReadFull(conn, make([]byte, int(n[0])+2))
	}

	// 隧道已建立，发一个 HTTP 请求验证数据双向可通
	fmt.Fprintf(conn, "GET / HTTP/1.0\r\nHost: %s:%s\r\n\r\n", targetHost, targetPort)
	body, err := io.ReadAll(conn)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// TestProxyForwardsThroughSSH 验证完整链路：
// 客户端 → socks5 → SSH direct-tcpip → 目标 HTTP 服务
func TestProxyForwardsThroughSSH(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "hello-through-tunnel")
	}))
	defer backend.Close()
	host, port, _ := net.SplitHostPort(backend.Listener.Addr().String())

	sshAddr, stopSSH := newTestSSHServer(t)
	defer stopSSH()
	client := dialTestSSH(t, sshAddr)
	defer client.Close()

	proxyPort := freePort(t)
	srv := &Socks5Server{ProxyPort: proxyPort}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.ProxyStart(client) }()
	defer srv.Close()

	waitPort(t, proxyPort)

	body, err := socks5Get(proxyPort, host, port)
	if err != nil {
		t.Fatalf("经代理请求失败: %v", err)
	}
	if want := "hello-through-tunnel"; !strings.Contains(body, want) {
		t.Fatalf("响应中未包含 %q，实际: %q", want, body)
	}
}

// TestCloseReleasesPort 验证 Close 会立即释放端口（原实现要等下一个连接才退出）
func TestCloseReleasesPort(t *testing.T) {
	sshAddr, stopSSH := newTestSSHServer(t)
	defer stopSSH()
	client := dialTestSSH(t, sshAddr)
	defer client.Close()

	proxyPort := freePort(t)
	srv := &Socks5Server{ProxyPort: proxyPort}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.ProxyStart(client) }()

	waitPort(t, proxyPort)
	srv.Close()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Close 后 ProxyStart 应正常返回，实际: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Close 后 ProxyStart 未在 3s 内返回（端口未及时释放）")
	}

	// 端口应可被重新监听
	l, err := net.Listen("tcp", "127.0.0.1:"+proxyPort)
	if err != nil {
		t.Fatalf("端口未释放: %v", err)
	}
	l.Close()

	// 重复 Close 不应 panic
	srv.Close()
	srv.Close()
}

// TestRestartAfterClose 验证重连场景：同一实例 Close 后能否再次启动。
func TestRestartAfterClose(t *testing.T) {
	sshAddr, stopSSH := newTestSSHServer(t)
	defer stopSSH()

	proxyPort := freePort(t)

	for i := 0; i < 3; i++ {
		client := dialTestSSH(t, sshAddr)

		// 每轮用新实例，模拟 tunnel.Manager 的重连行为
		srv := &Socks5Server{ProxyPort: proxyPort}
		errCh := make(chan error, 1)
		go func() { errCh <- srv.ProxyStart(client) }()

		waitPort(t, proxyPort)
		srv.Close()
		client.Close()

		select {
		case err := <-errCh:
			if err != nil {
				t.Fatalf("第 %d 轮返回错误: %v", i+1, err)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("第 %d 轮 Close 后未及时返回", i+1)
		}
	}
}

// TestPortInUseReturnsError 端口被占用时应返回错误而非静默失败
func TestPortInUseReturnsError(t *testing.T) {
	sshAddr, stopSSH := newTestSSHServer(t)
	defer stopSSH()
	client := dialTestSSH(t, sshAddr)
	defer client.Close()

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	_, port, _ := net.SplitHostPort(l.Addr().String())

	srv := &Socks5Server{ProxyPort: port}
	if err := srv.ProxyStart(client); err == nil {
		t.Fatal("端口被占用时应返回错误")
	}
}

// TestListensOnLoopbackOnly 代理不应监听 0.0.0.0（否则局域网内任何人可用）
func TestListensOnLoopbackOnly(t *testing.T) {
	sshAddr, stopSSH := newTestSSHServer(t)
	defer stopSSH()
	client := dialTestSSH(t, sshAddr)
	defer client.Close()

	proxyPort := freePort(t)
	srv := &Socks5Server{ProxyPort: proxyPort}
	go srv.ProxyStart(client)
	defer srv.Close()

	waitPort(t, proxyPort)

	// 若监听在 0.0.0.0，同端口的 0.0.0.0 监听会失败
	l, err := net.Listen("tcp", "0.0.0.0:"+proxyPort)
	if err != nil {
		t.Fatalf("代理疑似监听在 0.0.0.0（应仅监听 127.0.0.1）: %v", err)
	}
	l.Close()
}

// socks5GetFQDN 用域名地址类型（ATYP=3）发起 CONNECT，
// 用于验证域名是被原样转发到服务器，而不是在本地解析。
func socks5GetFQDN(proxyPort, host, targetPort string) (string, error) {
	conn, err := net.DialTimeout("tcp", "127.0.0.1:"+proxyPort, 5*time.Second)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(10 * time.Second))

	if _, err := conn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		return "", err
	}
	resp := make([]byte, 2)
	if _, err := io.ReadFull(conn, resp); err != nil {
		return "", err
	}

	port, _ := strconv.Atoi(targetPort)
	req := []byte{0x05, 0x01, 0x00, 0x03, byte(len(host))}
	req = append(req, host...)
	req = binary.BigEndian.AppendUint16(req, uint16(port))
	if _, err := conn.Write(req); err != nil {
		return "", err
	}

	head := make([]byte, 4)
	if _, err := io.ReadFull(conn, head); err != nil {
		return "", err
	}
	if head[1] != 0x00 {
		return "", fmt.Errorf("CONNECT 被拒绝, REP=%d", head[1])
	}
	switch head[3] {
	case 0x01:
		io.ReadFull(conn, make([]byte, 4+2))
	case 0x04:
		io.ReadFull(conn, make([]byte, 16+2))
	case 0x03:
		n := make([]byte, 1)
		io.ReadFull(conn, n)
		io.ReadFull(conn, make([]byte, int(n[0])+2))
	}

	fmt.Fprintf(conn, "GET / HTTP/1.0\r\nHost: %s:%s\r\n\r\n", host, targetPort)
	body, err := io.ReadAll(conn)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// TestForwardsFQDNToServer 是本次修复的回归测试：
// CustomDNS 为空时域名必须原样送到 SSH 服务器解析。
// 原实现在本机解析，DNS 会泄漏，且被污染/就近 CDN 的域名拿到的 IP
// 服务器往往连不上，表现为「浏览器打不开网页」。
func TestForwardsFQDNToServer(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "resolved-remotely")
	}))
	defer backend.Close()
	_, port, _ := net.SplitHostPort(backend.Listener.Addr().String())

	sshAddr, dests, stopSSH := newTestSSHServerRecording(t)
	defer stopSSH()
	client := dialTestSSH(t, sshAddr)
	defer client.Close()

	proxyPort := freePort(t)
	srv := &Socks5Server{ProxyPort: proxyPort}
	go srv.ProxyStart(client)
	defer srv.Close()

	waitPort(t, proxyPort)

	// localhost 能被服务器（本进程）解析，便于断言
	body, err := socks5GetFQDN(proxyPort, "localhost", port)
	if err != nil {
		t.Fatalf("经代理请求失败: %v", err)
	}
	if !strings.Contains(body, "resolved-remotely") {
		t.Fatalf("响应不符: %q", body)
	}

	got := dests()
	if len(got) == 0 {
		t.Fatal("服务器未收到任何转发请求")
	}
	if got[0] != "localhost" {
		t.Fatalf("应把域名原样转发给服务器，实际收到 %q（说明在本地解析了）", got[0])
	}
}

// TestCustomDNSQueriesThroughTunnel 指定 DNS 时，查询本身也要走隧道，
// 否则解析结果仍是本地视角的，且会泄漏 DNS。
func TestCustomDNSQueriesThroughTunnel(t *testing.T) {
	sshAddr, dests, stopSSH := newTestSSHServerRecording(t)
	defer stopSSH()
	client := dialTestSSH(t, sshAddr)
	defer client.Close()

	proxyPort := freePort(t)
	// 指向一个不存在的 DNS 端口：解析必然失败，
	// 但足以观察到查询是否被送进隧道。
	srv := &Socks5Server{ProxyPort: proxyPort, CustomDNS: "127.0.0.1:59"}
	go srv.ProxyStart(client)
	defer srv.Close()

	waitPort(t, proxyPort)

	if _, err := socks5GetFQDN(proxyPort, "example.invalid", "80"); err == nil {
		t.Fatal("域名不可解析时应失败")
	}

	for _, d := range dests() {
		if d == "127.0.0.1" {
			return // 查询确实经过了隧道
		}
	}
	t.Fatalf("DNS 查询未经隧道发出，服务器只收到 %v", dests())
}

// TestCustomDNSFallsBackToServer 指定的 DNS 不可用时不应让整个代理罢工，
// 而是退回服务器侧解析——否则「填了个连不通的 DNS」等于所有网页都打不开，
// 而日志里只有一句解析失败，用户很难联想到是这项配置的问题。
func TestCustomDNSFallsBackToServer(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "fallback-ok")
	}))
	defer backend.Close()
	_, port, _ := net.SplitHostPort(backend.Listener.Addr().String())

	sshAddr, dests, stopSSH := newTestSSHServerRecording(t)
	defer stopSSH()
	client := dialTestSSH(t, sshAddr)
	defer client.Close()

	proxyPort := freePort(t)
	// 127.0.0.1:59 上没有 DNS 服务，解析注定失败
	srv := &Socks5Server{ProxyPort: proxyPort, CustomDNS: "127.0.0.1:59"}
	go srv.ProxyStart(client)
	defer srv.Close()

	waitPort(t, proxyPort)

	// 用一个本机解析不出、只有测试 SSH 服务器认识的域名，
	// 否则 localhost 之类会被 /etc/hosts 直接解析掉，走不到退回逻辑
	body, err := socks5GetFQDN(proxyPort, serverOnlyHost, port)
	if err != nil {
		t.Fatalf("DNS 不可用时应退回服务器解析，实际直接失败: %v", err)
	}
	if !strings.Contains(body, "fallback-ok") {
		t.Fatalf("响应不符: %q", body)
	}

	// 退回后必须把域名原样交给服务器，而不是送一个占位 IP 过去
	var sawFQDN bool
	for _, d := range dests() {
		if d == serverOnlyHost {
			sawFQDN = true
		}
		if d == "0.0.0.0" {
			t.Fatal("占位 IP 泄漏到了转发请求里，remoteRewriter 未生效")
		}
	}
	if !sawFQDN {
		t.Fatalf("服务器未收到域名，只收到 %v", dests())
	}
}

// TestCustomDNSSuccessUsesResolvedIP 指定 DNS 解析成功时必须用解析出的 IP 拨号，
// 不能被 remoteRewriter 一律改回域名——那样等于配置的 DNS 白填了。
func TestCustomDNSSuccessUsesResolvedIP(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "via-custom-dns")
	}))
	defer backend.Close()
	_, port, _ := net.SplitHostPort(backend.Listener.Addr().String())

	dnsAddr := newTestDNSServer(t, "fixed.example.", net.IPv4(127, 0, 0, 1))

	sshAddr, dests, stopSSH := newTestSSHServerRecording(t)
	defer stopSSH()
	client := dialTestSSH(t, sshAddr)
	defer client.Close()

	proxyPort := freePort(t)
	srv := &Socks5Server{ProxyPort: proxyPort, CustomDNS: dnsAddr}
	go srv.ProxyStart(client)
	defer srv.Close()

	waitPort(t, proxyPort)

	body, err := socks5GetFQDN(proxyPort, "fixed.example", port)
	if err != nil {
		t.Fatalf("经自定义 DNS 请求失败: %v", err)
	}
	if !strings.Contains(body, "via-custom-dns") {
		t.Fatalf("响应不符: %q", body)
	}

	for _, d := range dests() {
		if d == "fixed.example" {
			t.Fatal("解析成功时应用解析出的 IP 拨号，却把域名送给了服务器")
		}
	}
}

func waitPort(t *testing.T, port string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp", "127.0.0.1:"+port, 200*time.Millisecond)
		if err == nil {
			c.Close()
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("端口 %s 未在 5s 内就绪", port)
}
