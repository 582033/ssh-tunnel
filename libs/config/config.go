package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type AuthType string

const (
	AuthTypePassword   AuthType = "password"
	AuthTypePrivateKey AuthType = "private_key"
)

// Config 一份连接配置。一个配置文件里可以放多份，由 Store 管理，
// 界面上通过顶部下拉切换；同一时刻只有一份在连接。
type Config struct {
	// Name 下拉列表里显示的名字，在同一个 Store 内唯一
	Name       string `yaml:"name"`
	Username   string `yaml:"username"`
	Password   string `yaml:"password"`
	PrivateKey string `yaml:"privateKey"`
	ServerAddr string `yaml:"serverAddr"`
	ServerPort string `yaml:"serverPort"`
	LocalPort  string `yaml:"localPort"`
	ChromePath string `yaml:"chromePath"`
	UseChrome  bool   `yaml:"useChrome"`
	CustomDNS  string `yaml:"customDNS"`
	// AutoConnect 为 true 时启动 App 即自动连接
	AutoConnect bool `yaml:"autoConnect"`

	AuthType AuthType `yaml:"-"`
}

const defaultChromePath = "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"

// Default 返回带合理默认值的空配置，供界面首次填写或新建连接。
func Default() *Config {
	return &Config{
		ServerPort: "22",
		LocalPort:  "1081",
		// 默认让 SSH 服务器解析域名：不泄漏 DNS，CDN 也会按服务器所在地就近返回
		CustomDNS:   "",
		UseChrome:   true,
		AutoConnect: true,
		ChromePath:  defaultChromePath,
	}
}

// Clone 返回一份副本，便于界面在「取消」时丢弃改动。
func (c *Config) Clone() *Config {
	cp := *c
	return &cp
}

// Validate 校验并填充默认值。不校验 Name——名字的唯一性由 Store 负责。
func (c *Config) Validate() error {
	c.Name = strings.TrimSpace(c.Name)
	c.ServerAddr = strings.TrimSpace(c.ServerAddr)
	c.Username = strings.TrimSpace(c.Username)
	c.ServerPort = strings.TrimSpace(c.ServerPort)
	c.LocalPort = strings.TrimSpace(c.LocalPort)
	c.CustomDNS = strings.TrimSpace(c.CustomDNS)
	c.PrivateKey = strings.TrimSpace(c.PrivateKey)

	if c.ServerAddr == "" {
		return fmt.Errorf("请填写服务器地址")
	}
	if c.Username == "" {
		return fmt.Errorf("请填写用户名")
	}
	if c.ServerPort == "" {
		c.ServerPort = "22"
	} else if !validPort(c.ServerPort) {
		return fmt.Errorf("服务器端口无效: %s", c.ServerPort)
	}
	if c.LocalPort == "" {
		c.LocalPort = "1081"
	} else if !validPort(c.LocalPort) {
		return fmt.Errorf("本地端口无效: %s", c.LocalPort)
	}

	// 两种凭据都可以填，连接时会一并提交给服务器由 SSH 协议协商，
	// 因此这里只要求至少有一个。AuthType 仅用于展示首选方式。
	switch {
	case c.PrivateKey != "":
		c.AuthType = AuthTypePrivateKey
		c.PrivateKey = expandHome(c.PrivateKey)
	case c.Password != "":
		c.AuthType = AuthTypePassword
	default:
		return fmt.Errorf("请填写密码或私钥路径（至少一项）")
	}

	// customDNS 省略端口时补上 53
	if c.CustomDNS != "" && !strings.Contains(c.CustomDNS, ":") {
		c.CustomDNS += ":53"
	}

	if c.UseChrome && c.ChromePath == "" {
		c.ChromePath = defaultChromePath
	}
	return nil
}

func validPort(s string) bool {
	n, err := strconv.Atoi(s)
	return err == nil && n > 0 && n < 65536
}

// ProxyAddr 返回本地 socks5 代理地址
func (c *Config) ProxyAddr() string {
	return "socks5://127.0.0.1:" + c.LocalPort
}

// Label 返回展示用名称，未命名时退回「用户名@地址」。
func (c *Config) Label() string {
	if n := strings.TrimSpace(c.Name); n != "" {
		return n
	}
	if c.ServerAddr == "" {
		return defaultProfileName
	}
	if c.Username == "" {
		return c.ServerAddr
	}
	return c.Username + "@" + c.ServerAddr
}

func expandHome(p string) string {
	if !strings.HasPrefix(p, "~") {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	return filepath.Join(home, strings.TrimPrefix(p, "~"))
}
