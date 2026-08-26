package chrome

import (
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"
)

// fakeChrome 生成一个会派生子进程的脚本，用来模拟 Chrome 的
// 「主进程 + 一堆 Helper」结构，验证 Close 能整组收掉。
func fakeChrome(t *testing.T, childPidFile string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-chrome.sh")
	script := "#!/bin/sh\n" +
		"sleep 300 &\n" +
		"echo $! > " + childPidFile + "\n" +
		"wait\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func alive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

// TestCloseKillsChildProcesses 覆盖用户反馈的问题：
// App 退出后 Chrome 窗口还在。只 Kill 主进程收不干净子进程，
// 现在按进程组结束。
func TestCloseKillsChildProcesses(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	p := &StartupParams{ChromePath: fakeChrome(t, pidFile)}

	done := make(chan error, 1)
	go func() { done <- p.Start() }()

	// 等子进程 pid 落盘
	var childPID int
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(pidFile); err == nil && len(b) > 1 {
			childPID, _ = strconv.Atoi(string(b[:len(b)-1]))
			if childPID > 0 {
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if childPID == 0 {
		t.Fatal("未能取得子进程 pid")
	}
	if !alive(childPID) {
		t.Fatal("子进程应处于运行中")
	}

	p.Close()

	select {
	case <-done:
	case <-time.After(6 * time.Second):
		t.Fatal("Close 后主进程未退出")
	}

	// 子进程也必须走掉
	for i := 0; i < 40; i++ {
		if !alive(childPID) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("子进程 %d 仍在运行，Close 没有收掉整个进程组", childPID)
}

// TestCloseIdempotent Close 可重复调用，且未启动时调用不应 panic/阻塞
func TestCloseIdempotent(t *testing.T) {
	var p StartupParams
	p.Close() // 从未 Start
	p.Close()

	pidFile := filepath.Join(t.TempDir(), "child.pid")
	q := &StartupParams{ChromePath: fakeChrome(t, pidFile)}
	go q.Start()
	time.Sleep(300 * time.Millisecond)

	done := make(chan struct{})
	go func() {
		q.Close()
		q.Close()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(8 * time.Second):
		t.Fatal("重复 Close 阻塞了")
	}
}

func TestStartMissingBinaryReturnsError(t *testing.T) {
	p := &StartupParams{ChromePath: "/nonexistent/chrome-binary"}
	if err := p.Start(); err == nil {
		t.Fatal("路径不存在时应返回错误")
	}
	// 启动失败后 Close 不应阻塞
	done := make(chan struct{})
	go func() { p.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("启动失败后 Close 阻塞了")
	}
}
