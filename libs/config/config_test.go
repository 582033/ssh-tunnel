package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidatePrivateKeyPreferred(t *testing.T) {
	c := &Config{Username: "u", Password: "p", PrivateKey: "/tmp/key", ServerAddr: "example.com"}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if c.AuthType != AuthTypePrivateKey {
		t.Fatalf("同时配置时应优先私钥，实际 %v", c.AuthType)
	}
	// 默认值填充
	if c.ServerPort != "22" || c.LocalPort != "1081" {
		t.Fatalf("默认端口未填充: %s / %s", c.ServerPort, c.LocalPort)
	}
}

func TestValidatePasswordOnly(t *testing.T) {
	c := &Config{Username: "u", Password: "p", ServerAddr: "example.com"}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if c.AuthType != AuthTypePassword {
		t.Fatalf("应为密码认证，实际 %v", c.AuthType)
	}
}

func TestValidateRejectsMissingAuth(t *testing.T) {
	c := &Config{Username: "u", ServerAddr: "example.com"}
	if err := c.Validate(); err == nil {
		t.Fatal("密码和私钥都为空时应报错")
	}
}

func TestValidateRejectsMissingServerAddr(t *testing.T) {
	c := &Config{Username: "u", Password: "p"}
	if err := c.Validate(); err == nil {
		t.Fatal("serverAddr 为空时应报错")
	}
}

func TestValidateRejectsMissingUsername(t *testing.T) {
	c := &Config{Password: "p", ServerAddr: "example.com"}
	if err := c.Validate(); err == nil {
		t.Fatal("username 为空时应报错")
	}
}

func TestCustomDNSPortDefault(t *testing.T) {
	c := &Config{Username: "u", Password: "p", ServerAddr: "example.com", CustomDNS: "8.8.8.8"}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if c.CustomDNS != "8.8.8.8:53" {
		t.Fatalf("未补默认 53 端口，实际 %q", c.CustomDNS)
	}
}

func TestPrivateKeyTildeExpanded(t *testing.T) {
	c := &Config{Username: "u", PrivateKey: "~/.ssh/id_rsa", ServerAddr: "example.com"}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	home, _ := os.UserHomeDir()
	want := filepath.Join(home, ".ssh/id_rsa")
	if c.PrivateKey != want {
		t.Fatalf("~ 未展开: %q，期望 %q", c.PrivateKey, want)
	}
}

func TestProxyAddr(t *testing.T) {
	c := &Config{LocalPort: "1081"}
	if got := c.ProxyAddr(); got != "socks5://127.0.0.1:1081" {
		t.Fatalf("ProxyAddr = %q", got)
	}
}

func TestValidateRejectsBadPort(t *testing.T) {
	for _, port := range []string{"0", "70000", "abc", "-1"} {
		c := &Config{Username: "u", Password: "p", ServerAddr: "h", LocalPort: port}
		if err := c.Validate(); err == nil {
			t.Fatalf("本地端口 %q 应被拒绝", port)
		}
	}
	for _, port := range []string{"0", "99999", "x"} {
		c := &Config{Username: "u", Password: "p", ServerAddr: "h", ServerPort: port}
		if err := c.Validate(); err == nil {
			t.Fatalf("服务器端口 %q 应被拒绝", port)
		}
	}
}

func TestValidateTrimsWhitespace(t *testing.T) {
	c := &Config{Username: "  u  ", Password: "p", ServerAddr: "  h  ", ServerPort: " 22 "}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if c.Username != "u" || c.ServerAddr != "h" || c.ServerPort != "22" {
		t.Fatalf("未去除空白: %q / %q / %q", c.Username, c.ServerAddr, c.ServerPort)
	}
}

func TestClone(t *testing.T) {
	a := &Config{Username: "u", Password: "p"}
	b := a.Clone()
	b.Username = "changed"
	if a.Username != "u" {
		t.Fatal("Clone 应返回独立副本")
	}
}

// Label 是下拉列表里显示的文字，没起名字时要退回一个人能认出来的字符串，
// 否则多连接场景下会出现几个空白项。
func TestLabel(t *testing.T) {
	cases := []struct {
		c    Config
		want string
	}{
		{Config{Name: "公司"}, "公司"},
		{Config{Name: "  公司  "}, "公司"},
		{Config{Username: "u", ServerAddr: "h.example"}, "u@h.example"},
		{Config{ServerAddr: "h.example"}, "h.example"},
		{Config{}, defaultProfileName},
	}
	for _, tc := range cases {
		if got := tc.c.Label(); got != tc.want {
			t.Errorf("Label(%+v) = %q，期望 %q", tc.c, got, tc.want)
		}
	}
}
