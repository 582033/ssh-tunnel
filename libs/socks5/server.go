package socks5

import (
	"context"
	"net"

	"golang.org/x/crypto/ssh"

	goSocks5 "github.com/armon/go-socks5"
	"github.com/gookit/color"
)

var Socks5QuitChan = make(chan struct{})

type Socks5Server struct {
	goSocks5.Server
	ProxyPort string
	CustomDNS string
}

func (s *Socks5Server) New(config *goSocks5.Config) (*goSocks5.Server, error) {
	return goSocks5.New(config)
}

func (s *Socks5Server) ListenAndServe(network, addr string) error {
	l, err := net.Listen(network, addr)
	if err != nil {
		return err
	}
	return s.Serve(l)
}

func (s *Socks5Server) Serve(l net.Listener) error {
	for {
		conn, err := l.Accept()
		if err != nil {
			return err
		}
		select {
		case <-Socks5QuitChan:
			conn.Close()
			return nil
		default:
			go s.ServeConn(conn)
		}
	}
}

func (s *Socks5Server) ServeConn(conn net.Conn) error {
	return s.ServeConn(conn)
}

func (s *Socks5Server) ProxyStart(sshClient *ssh.Client, quit chan struct{}) {
	for {
		select {
		case <-quit:
			color.Error.Println("收到退出信号，关闭socks5代理")
			return
		default:
			resolve := MyResolver{
				CustomDNS: s.CustomDNS,
			}

			config := &goSocks5.Config{
				Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
					return sshClient.Dial(network, addr)
				},
				Resolver: resolve,
			}

			server, err := s.New(config)
			if err != nil {
				color.Error.Printf("创建socks5代理失败:%s", err.Error())
				// panic(0)
				return
			}
			if err := server.ListenAndServe("tcp", "0.0.0.0:"+s.ProxyPort); err != nil {
				color.Error.Printf("启动socks5代理失败:%s", err.Error())
				// panic(0)
				return
			}
		}
	}
}
