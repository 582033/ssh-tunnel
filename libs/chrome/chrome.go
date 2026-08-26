package chrome

import (
	"fmt"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

type StartupParams struct {
	ChromePath string
	RunParams  []string
	// LogFunc 可选，用于把启动/退出信息交给上层展示
	LogFunc func(string)

	mu   sync.Mutex
	cmd  *exec.Cmd
	pgid int
	done chan struct{}
}

func (p *StartupParams) logf(format string, args ...any) {
	if p.LogFunc != nil {
		p.LogFunc(fmt.Sprintf(format, args...))
	}
}

// Start 启动 Chrome 并阻塞等待其退出。
func (p *StartupParams) Start() error {
	cmd := exec.Command(p.ChromePath, p.RunParams...)
	// 单独开一个进程组：Chrome 会派生一堆 Helper 子进程，
	// 只杀主进程有时收不干净，退出时按进程组整体结束。
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	done := make(chan struct{})

	p.mu.Lock()
	p.cmd = cmd
	p.done = done
	p.pgid = 0
	p.mu.Unlock()

	if err := cmd.Start(); err != nil {
		close(done)
		p.logf("启动 Chrome 失败: %v", err)
		return err
	}

	// Setpgid 后子进程自己就是组长，pgid 等于其 pid
	p.mu.Lock()
	p.pgid = cmd.Process.Pid
	p.mu.Unlock()

	p.logf("已启动 Chrome")

	err := cmd.Wait()
	close(done)
	if err != nil {
		p.logf("Chrome 进程退出: %v", err)
	} else {
		p.logf("Chrome 进程已退出")
	}
	return err
}

// Close 结束 Chrome 及其所有子进程。可重复调用。
//
// 两个坑：
//  1. 原实现用未初始化的包级 quit channel 收发信号，往 nil channel 发送会永久阻塞。
//  2. 只 Kill 主进程时，Chrome 的 Helper 子进程可能残留，窗口也不一定关掉；
//     所以这里对整个进程组先 SIGTERM（让 Chrome 正常保存并退出），
//     超时再 SIGKILL。
func (p *StartupParams) Close() {
	p.mu.Lock()
	done, pgid := p.done, p.pgid
	p.mu.Unlock()

	if pgid <= 0 {
		return
	}

	p.logf("正在关闭 Chrome")
	// 负号表示整个进程组
	_ = syscall.Kill(-pgid, syscall.SIGTERM)

	if done == nil {
		return
	}
	select {
	case <-done:
		return
	case <-time.After(3 * time.Second):
	}

	// 没有在宽限期内退出，强杀
	_ = syscall.Kill(-pgid, syscall.SIGKILL)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		p.logf("Chrome 未能正常退出")
	}
}
