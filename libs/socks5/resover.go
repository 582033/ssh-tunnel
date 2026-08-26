package socks5

import (
	"context"
	"fmt"
	"net"
	"time"

	goSocks5 "github.com/armon/go-socks5"
)

// DialFunc 通过 SSH 隧道建立连接
type DialFunc func(ctx context.Context, network, addr string) (net.Conn, error)

// MyResolver 决定域名在哪一侧解析。
//
// CustomDNS 为空时不在本地解析，域名原样交给 SSH 服务器（见 remoteRewriter），
// 这样既不泄漏 DNS，CDN 也会按服务器所在地返回节点。
// CustomDNS 非空时用 TCP 向该 DNS 服务器查询，且这条查询同样走隧道，
// 因此解析结果与服务器直接解析一致。
//
// 原实现在本机解析：DNS 请求会泄漏，被污染的域名拿到假 IP，
// 正常域名也会拿到就近本地的 CDN 节点，服务器往往连不上。
type MyResolver struct {
	CustomDNS string
	// Dial 走隧道拨号，用于把 DNS 查询也送到服务器那侧
	Dial    DialFunc
	LogFunc func(string)
}

func (d MyResolver) logf(format string, args ...any) {
	if d.LogFunc != nil {
		d.LogFunc(fmt.Sprintf(format, args...))
	}
}

// ctxKeyResolveRemote 标记这条请求要由服务器解析域名。
// go-socks5 的 Resolve 必须返回一个 IP，无法表达「我不解析」，
// 只能通过 context 把这个意图传给 remoteRewriter。
type ctxKeyResolveRemote struct{}

// deferToServer 返回占位 IP 并打上标记，由 remoteRewriter 把 IP 抹掉，
// 最终以域名进入 SSH 的 direct-tcpip 请求。
func deferToServer(ctx context.Context) (context.Context, net.IP, error) {
	return context.WithValue(ctx, ctxKeyResolveRemote{}, true), net.IPv4zero, nil
}

func (d MyResolver) Resolve(ctx context.Context, name string) (context.Context, net.IP, error) {
	if d.CustomDNS == "" || d.Dial == nil {
		return deferToServer(ctx)
	}

	resolver := net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
			// 隧道只能转发 TCP，因此固定用 TCP 查询（DNS over TCP，RFC 7766）
			return d.Dial(ctx, "tcp", d.CustomDNS)
		},
	}

	// DNS 不该拖着请求一直等，超时了直接报错比界面卡住好定位。
	// 注意只把带超时的 ctx 用于查询本身：返回值必须是原 ctx，
	// 否则 defer cancel 之后 go-socks5 拿去拨号的 ctx 已经是取消状态。
	lookupCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	start := time.Now()
	ips, err := resolver.LookupIPAddr(lookupCtx, name)
	cost := time.Since(start).Round(time.Millisecond)

	if err != nil {
		d.logf("DNS ✗ %s 解析失败（经 %s，耗时 %s）: %v", name, d.CustomDNS, cost, err)
		d.logf("DNS 改由 SSH 服务器解析 %s（指定的 DNS 走隧道不可达，"+
			"国内服务器到 8.8.8.8 等公共 DNS 的 TCP/53 常被阻断）", name)
		// 不因为一台 DNS 不可用就让整个代理打不开网页：
		// 退回服务器侧解析，与 CustomDNS 留空时的行为一致。
		return deferToServer(ctx)
	}
	if len(ips) == 0 {
		d.logf("DNS ✗ %s 无解析结果（经 %s），改由 SSH 服务器解析", name, d.CustomDNS)
		return deferToServer(ctx)
	}

	d.logf("DNS ✓ %s → %s（经 %s，耗时 %s）", name, ips[0].IP, d.CustomDNS, cost)
	return ctx, ips[0].IP, nil
}

// remoteRewriter 抹掉解析阶段填入的占位 IP，让 go-socks5 直接用域名拨号。
// AddrSpec.Address() 在 IP 为空时会退回 FQDN，于是域名被原样送进
// SSH 的 direct-tcpip 请求，由服务器完成解析。
//
// 只在 Resolve 打了标记时才抹：指定 DNS 且解析成功的情况下，
// 那个 IP 正是我们想用的，不能丢。
type remoteRewriter struct{}

func (remoteRewriter) Rewrite(ctx context.Context, req *goSocks5.Request) (context.Context, *goSocks5.AddrSpec) {
	dest := req.DestAddr
	if dest == nil || dest.FQDN == "" {
		// 客户端本来就给的是 IP，照原样转发
		return ctx, dest
	}
	if v, ok := ctx.Value(ctxKeyResolveRemote{}).(bool); !ok || !v {
		return ctx, dest
	}
	return ctx, &goSocks5.AddrSpec{FQDN: dest.FQDN, Port: dest.Port}
}
