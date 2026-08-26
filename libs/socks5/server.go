package socks5

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/ssh"

	goSocks5 "github.com/armon/go-socks5"
)

// logWriter 把 go-socks5 的 log 输出接到上层日志回调
type logWriter struct {
	fn func(string)
}

func (w logWriter) Write(p []byte) (int, error) {
	if w.fn != nil {
		w.fn(strings.TrimRight(string(p), "\n"))
	}
	return len(p), nil
}

type Socks5Server struct {
	*goSocks5.Server
	ProxyPort string
	CustomDNS string
	// LogFunc 可选，用于把 DNS 解析等信息交给上层展示
	LogFunc func(string)

	// connSeq 给每条连接一个编号，日志里便于把
	// 「发起 → 成功/失败」这几行对上
	connSeq atomic.Uint64

	mu       sync.Mutex
	listener net.Listener
	quitChan chan struct{}
	closed   bool
}

// dialTimeout 单条出站连接的上限。sshClient.Dial 本身不带超时，
// 目标不可达时会一直挂着，界面上看起来就是「转圈但没反应」。
const dialTimeout = 15 * time.Second

func (s *Socks5Server) logf(format string, args ...any) {
	if s.LogFunc != nil {
		s.LogFunc(fmt.Sprintf(format, args...))
	}
}

// ProxyStart 阻塞运行 socks5 服务，直到 Close 被调用或监听出错。
// 所有出站连接都通过 sshClient 建立，即走 SSH 隧道。
func (s *Socks5Server) ProxyStart(sshClient *ssh.Client) error {
	// rawDial 不打日志，供 DNS 查询复用（否则每解析一次就多两行噪音）
	rawDial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		type result struct {
			conn net.Conn
			err  error
		}
		ch := make(chan result, 1)
		go func() {
			c, err := sshClient.Dial(network, addr)
			ch <- result{c, err}
		}()

		timer := time.NewTimer(dialTimeout)
		defer timer.Stop()

		select {
		case r := <-ch:
			return r.conn, r.err
		case <-ctx.Done():
			// 顺带回收后到的连接，避免泄漏
			go func() {
				if r := <-ch; r.conn != nil {
					r.conn.Close()
				}
			}()
			return nil, ctx.Err()
		case <-timer.C:
			go func() {
				if r := <-ch; r.conn != nil {
					r.conn.Close()
				}
			}()
			return nil, fmt.Errorf("经隧道连接 %s 超时（%s）", addr, dialTimeout)
		}
	}

	// dial 是给 go-socks5 用的，逐条记录出站连接的去向和结果，
	// 这样「打不开网页」时能直接看出是解析问题还是服务器出不去网。
	dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		id := s.connSeq.Add(1)
		s.logf("#%d → %s %s", id, network, addr)

		start := time.Now()
		conn, err := rawDial(ctx, network, addr)
		cost := time.Since(start).Round(time.Millisecond)

		if err != nil {
			s.logf("#%d ✗ %s 失败（耗时 %s）: %v", id, addr, cost, err)
			return nil, err
		}
		s.logf("#%d ✓ %s 已建立（耗时 %s）", id, addr, cost)
		return conn, nil
	}

	config := &goSocks5.Config{
		Dial:     dial,
		Resolver: MyResolver{CustomDNS: s.CustomDNS, Dial: rawDial, LogFunc: s.LogFunc},
		// 域名交给服务器解析，见 remoteRewriter
		Rewriter: remoteRewriter{},
		// go-socks5 默认把每个请求错误打到 stdout，.app 里看不到；
		// 交给上层日志。
		Logger: log.New(logWriter{s.LogFunc}, "", 0),
	}

	server, err := goSocks5.New(config)
	if err != nil {
		return err
	}

	// 只监听回环地址：原来的 0.0.0.0 会把代理暴露给整个局域网，
	// 任何同网段的人都能拿它当开放代理用。
	l, err := net.Listen("tcp", "127.0.0.1:"+s.ProxyPort)
	if err != nil {
		return err
	}

	s.mu.Lock()
	if s.closed {
		// Close 在 Listen 之前就被调用了
		s.mu.Unlock()
		l.Close()
		return nil
	}
	s.Server = server
	s.listener = l
	s.quitChan = make(chan struct{})
	quit := s.quitChan
	s.mu.Unlock()

	if s.CustomDNS == "" {
		s.logf("域名将由 SSH 服务器解析")
	} else {
		s.logf("域名将通过隧道向 %s 查询（DNS over TCP）", s.CustomDNS)
	}

	for {
		conn, err := l.Accept()
		if err != nil {
			// 主动关闭时 Accept 会立刻返回 ErrClosed，不算错误
			select {
			case <-quit:
				return nil
			default:
			}
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		go server.ServeConn(conn)
	}
}

// Close 关闭监听并释放端口。可重复调用。
func (s *Socks5Server) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return
	}
	s.closed = true

	if s.quitChan != nil {
		close(s.quitChan)
	}
	// 直接关掉 listener，让阻塞中的 Accept 立即返回；
	// 否则要等下一个连接进来才会发现退出信号。
	if s.listener != nil {
		s.listener.Close()
		s.listener = nil
	}
}
