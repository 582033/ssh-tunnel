package socks5

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"io"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// newTestSSHServer 起一个进程内 SSH 服务器，支持 direct-tcpip（端口转发）通道，
// 用于在没有真实 sshd 的环境下验证 socks5 转发链路。
func newTestSSHServer(t *testing.T) (addr string, stop func()) {
	a, _, s := newTestSSHServerRecording(t)
	return a, s
}

// newTestSSHServerRecording 额外返回一个函数，用于读取服务器收到的
// direct-tcpip 目标地址，以验证域名是否原样送到服务器那侧解析。
func newTestSSHServerRecording(t *testing.T) (addr string, dests func() []string, stop func()) {
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

	rec := &destRecorder{}

	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go handleSSHConn(c, cfg, rec)
		}
	}()

	return l.Addr().String(), rec.list, func() { l.Close() }
}

// destRecorder 记录服务器收到的转发目标
type destRecorder struct {
	mu    sync.Mutex
	addrs []string
}

func (r *destRecorder) add(s string) {
	r.mu.Lock()
	r.addrs = append(r.addrs, s)
	r.mu.Unlock()
}

func (r *destRecorder) list() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.addrs...)
}

// serverOnlyHost 模拟「只有 SSH 服务器能解析的域名」。
// 测试里客户端侧一定解析不出它（不在 hosts、也无权威记录），
// 服务器侧则由 handleSSHConn 映射到回环地址。
// 用来区分「域名原样交给服务器解析」和「在本地解析后再拨号」。
const serverOnlyHost = "server-only.example"

func handleSSHConn(c net.Conn, cfg *ssh.ServerConfig, rec *destRecorder) {
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

		if rec != nil {
			rec.add(payload.DestAddr)
		}

		// 记录的是原始地址，拨号时才做映射
		host := payload.DestAddr
		if host == serverOnlyHost {
			host = "127.0.0.1"
		}

		target := net.JoinHostPort(host, strconv.Itoa(int(payload.DestPort)))
		upstream, err := net.DialTimeout("tcp", target, 5*time.Second)
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

func dialTestSSH(t *testing.T, addr string) *ssh.Client {
	t.Helper()
	client, err := ssh.Dial("tcp", addr, &ssh.ClientConfig{
		User:            "test",
		Auth:            []ssh.AuthMethod{ssh.Password("x")},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

// freePort 借一个空闲端口号后立刻释放
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

// newTestDNSServer 起一个极简 DNS-over-TCP 服务器，只为 wantName 返回一条 A 记录，
// 其余一律返回 NXDOMAIN。用来验证「指定 DNS 且解析成功」这条路径。
// 返回 host:port，可直接作为 Socks5Server.CustomDNS。
func newTestDNSServer(t *testing.T, wantName string, answer net.IP) string {
	t.Helper()

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })

	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go serveDNSOverTCP(c, wantName, answer)
		}
	}()

	return l.Addr().String()
}

func serveDNSOverTCP(c net.Conn, wantName string, answer net.IP) {
	defer c.Close()
	c.SetDeadline(time.Now().Add(5 * time.Second))

	for {
		// TCP 上每条消息前有 2 字节长度（RFC 1035 §4.2.2）
		var msgLen uint16
		if err := binary.Read(c, binary.BigEndian, &msgLen); err != nil {
			return
		}
		msg := make([]byte, msgLen)
		if _, err := io.ReadFull(c, msg); err != nil {
			return
		}

		resp := buildDNSResponse(msg, wantName, answer)
		if resp == nil {
			return
		}
		if err := binary.Write(c, binary.BigEndian, uint16(len(resp))); err != nil {
			return
		}
		if _, err := c.Write(resp); err != nil {
			return
		}
	}
}

// buildDNSResponse 手搓一个应答：回显问题段，A 查询命中则附一条 A 记录。
// 只覆盖测试需要的部分，不是通用实现。
func buildDNSResponse(q []byte, wantName string, answer net.IP) []byte {
	const header = 12
	if len(q) < header {
		return nil
	}

	// 走过 QNAME 的 label 序列，定位问题段末尾
	name, i := "", header
	for i < len(q) {
		n := int(q[i])
		i++
		if n == 0 {
			break
		}
		if i+n > len(q) {
			return nil
		}
		name += string(q[i:i+n]) + "."
		i += n
	}
	if i+4 > len(q) {
		return nil
	}
	qtype := binary.BigEndian.Uint16(q[i : i+2])
	i += 4 // 跳过 QTYPE + QCLASS
	question := q[header:i]

	hit := name == wantName && qtype == 1 // 只答 A，AAAA 返回空
	rcode := byte(0)
	if name != wantName {
		rcode = 3 // NXDOMAIN
	}

	resp := make([]byte, 0, len(q)+16)
	resp = append(resp, q[0], q[1])               // 事务 ID 原样返回
	resp = append(resp, 0x81, rcode)              // QR=1 RD=1 + RCODE
	resp = binary.BigEndian.AppendUint16(resp, 1) // QDCOUNT
	if hit {
		resp = binary.BigEndian.AppendUint16(resp, 1) // ANCOUNT
	} else {
		resp = binary.BigEndian.AppendUint16(resp, 0)
	}
	resp = binary.BigEndian.AppendUint16(resp, 0) // NSCOUNT
	resp = binary.BigEndian.AppendUint16(resp, 0) // ARCOUNT
	resp = append(resp, question...)

	if hit {
		resp = append(resp, 0xC0, byte(header))        // 指针压缩，指回问题段的 QNAME
		resp = binary.BigEndian.AppendUint16(resp, 1)  // TYPE=A
		resp = binary.BigEndian.AppendUint16(resp, 1)  // CLASS=IN
		resp = binary.BigEndian.AppendUint32(resp, 60) // TTL
		resp = binary.BigEndian.AppendUint16(resp, 4)  // RDLENGTH
		resp = append(resp, answer.To4()...)
	}
	return resp
}
