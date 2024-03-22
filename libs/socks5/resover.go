package socks5

import (
	"context"
	"net"

	goSocks5 "github.com/armon/go-socks5"
	"github.com/gookit/color"
)

type MyResolver struct {
	CustomDNS string
}

func (d MyResolver) Resolve(ctx context.Context, name string) (context.Context, net.IP, error) {
	// 如果没有设置自定义DNS，则使用系统DNS
	if d.CustomDNS == "" {
		addr, _ := net.ResolveIPAddr("ip", name)
		color.Info.Println("访问的域名:" + name + ", 本地解析为:" + addr.IP.String())
		return goSocks5.DNSResolver{}.Resolve(ctx, name)
	}

	// 设置自定义DNS
	dnsServer := d.CustomDNS
	resolver := net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			d := net.Dialer{}
			return d.DialContext(ctx, "udp", dnsServer)
		},
	}

	ips, err := resolver.LookupIPAddr(ctx, name)
	if err != nil {
		return ctx, nil, err
	}
	color.Info.Println("访问的域名:" + name + ", 解析为:" + ips[0].String())

	if err != nil {
		return ctx, nil, err
	}
	return ctx, ips[0].IP, err
}
