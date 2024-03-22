package libs

import (
	"net"

	socks5 "github.com/armon/go-socks5"
)

var Socks5QuitChan = make(chan struct{})

type Socks5Server struct {
	socks5.Server
}

type Socks5Config struct {
	socks5.Config
}

func New(config *Socks5Config) (*socks5.Server, error) {
	return socks5.New(&config.Config)
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
