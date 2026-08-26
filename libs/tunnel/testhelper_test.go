package tunnel

import (
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// newTestSSHServer 起一个进程内 SSH 服务器，支持 direct-tcpip 通道。
func newTestSSHServer(t *testing.T) (addr string, stop func()) {
	t.Helper()

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}

	cfg := &ssh.ServerConfig{
		PasswordCallback: func(ssh.ConnMetadata, []byte) (*ssh.Permissions, error) {
			return nil, nil
		},
	}
	cfg.AddHostKey(signer)

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go handleSSHConn(c, cfg)
		}
	}()

	return l.Addr().String(), func() { l.Close() }
}

func handleSSHConn(c net.Conn, cfg *ssh.ServerConfig) {
	conn, chans, reqs, err := ssh.NewServerConn(c, cfg)
	if err != nil {
		c.Close()
		return
	}
	defer conn.Close()
	go ssh.DiscardRequests(reqs)

	for newChan := range chans {
		if newChan.ChannelType() != "direct-tcpip" {
			newChan.Reject(ssh.UnknownChannelType, "only direct-tcpip")
			continue
		}
		var payload struct {
			DestAddr string
			DestPort uint32
			OrigAddr string
			OrigPort uint32
		}
		if err := ssh.Unmarshal(newChan.ExtraData(), &payload); err != nil {
			newChan.Reject(ssh.Prohibited, "bad payload")
			continue
		}

		upstream, err := net.DialTimeout("tcp",
			net.JoinHostPort(payload.DestAddr, fmt.Sprint(payload.DestPort)), 5*time.Second)
		if err != nil {
			newChan.Reject(ssh.ConnectionFailed, err.Error())
			continue
		}

		ch, chReqs, err := newChan.Accept()
		if err != nil {
			upstream.Close()
			continue
		}
		go ssh.DiscardRequests(chReqs)
		go func() {
			defer ch.Close()
			defer upstream.Close()
			go io.Copy(upstream, ch)
			io.Copy(ch, upstream)
		}()
	}
}

func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	_, port, _ := net.SplitHostPort(l.Addr().String())
	return port
}

// httpViaProxy 通过本地 socks5 代理发一次 HTTP GET
func httpViaProxy(t *testing.T, proxyPort, target string) string {
	t.Helper()
	proxyURL, err := url.Parse("socks5://127.0.0.1:" + proxyPort)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{
		Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)},
		Timeout:   10 * time.Second,
	}
	resp, err := client.Get(target)
	if err != nil {
		t.Fatalf("经代理请求失败: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}
