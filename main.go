package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	socks5 "github.com/armon/go-socks5"
	"github.com/gookit/color"
	"golang.org/x/crypto/ssh"
	"gopkg.in/yaml.v2"
)

// GlobalConfig 定义一个 config结构体变量
var GlobalConfig Config

type AuthType string

const AuthTypePassword AuthType = "password"
const AuthTypePrivateKey AuthType = "private_key"

// Config 声明 配置结构体
type Config struct {
	Username   string `yaml:"username"`
	Password   string `yaml:"password"`
	PrivateKey string `yaml:"privateKey"`
	ServerAddr string `yaml:"serverAddr"`
	ServerPort string `yaml:"serverPort"`
	LocalPort  string `yaml:"localPort"`
	ChromePath string `yaml:"chromePath"`
	UseChrome  bool   `yaml:"useChrome"`
	CustomDNS  string `yaml:"customDNS"`
	AuthType   AuthType
}

// 读取配置文件
func (c *Config) getConfig() (*Config, error) {
	var configFile string
	flag.StringVar(&configFile, "config", "config.yaml", "配置文件路径,默认为config.yaml")
	flag.Parse()

	//如果配置文件后缀不是yaml结尾的，就报错
	if configFile[len(configFile)-5:] != ".yaml" {
		return nil, fmt.Errorf("配置文件后缀必须为.yaml")
	}

	yamlFile, err := os.ReadFile(configFile)
	if err != nil {
		return nil, err
	}
	err = yaml.Unmarshal(yamlFile, c)
	if err != nil {
		return nil, err
	}

	if c.Password == "" && c.PrivateKey == "" {
		return nil, fmt.Errorf("配置文件的密码和私钥不能都为空")
	}
	if c.PrivateKey != "" {
		c.AuthType = AuthTypePrivateKey
	}

	if c.Password != "" {
		c.AuthType = AuthTypePassword
	}

	return c, nil
}

type MyResolver struct{}

func (d MyResolver) Resolve(ctx context.Context, name string) (context.Context, net.IP, error) {
	//如果没有设置自定义DNS，则使用系统DNS
	if GlobalConfig.CustomDNS == "" {
		addr, _ := net.ResolveIPAddr("ip", name)
		color.Info.Println("访问的域名:" + name + ", 本地解析为:" + addr.IP.String())
		return socks5.DNSResolver{}.Resolve(ctx, name)
	}

	//设置自定义DNS
	dnsServer := GlobalConfig.CustomDNS
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

func socks5ProxyStart(sshClient *ssh.Client) {
	resolve := MyResolver{}

	config := &socks5.Config{
		Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return sshClient.Dial(network, addr)
		},
		Resolver: resolve,
	}

	server, err := socks5.New(config)
	if err != nil {
		color.Error.Println("创建socks5代理失败")
		panic(0)
	}
	if err := server.ListenAndServe("tcp", "0.0.0.0:"+GlobalConfig.LocalPort); err != nil {
		color.Error.Println("启动socks5代理失败")
		panic(0)
	}
}

func connectToSSH() (*ssh.Client, error) {
	// 设置SSH配置
	config := &ssh.ClientConfig{
		User:            GlobalConfig.Username,
		Timeout:         30 * time.Second,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	}

	switch GlobalConfig.AuthType {
	case AuthTypePassword:
		config.Auth = []ssh.AuthMethod{
			ssh.Password(GlobalConfig.Password),
		}
		break
	case AuthTypePrivateKey:
		// 读取私钥文件内容
		privateKeyBytes, err := os.ReadFile(GlobalConfig.PrivateKey)
		if err != nil {
			log.Fatal(err)
		}
		// 解析私钥
		privateKey, err := ssh.ParsePrivateKey(privateKeyBytes)
		if err != nil {
			log.Fatal(err)
		}
		config.Auth = []ssh.AuthMethod{
			ssh.PublicKeys(privateKey),
		}

		break
	default:
		return nil, fmt.Errorf("缺少private_key 或 password")
	}

	client, err := ssh.Dial("tcp", GlobalConfig.ServerAddr+":"+GlobalConfig.ServerPort, config)
	if err != nil {
		return nil, err
	}
	// 连接远程服务器成功
	color.Success.Println("连接远程服务器成功")
	color.Success.Println("本地端口：" + GlobalConfig.LocalPort)
	return client, nil
}

// 启动本地chrome
func startChrome() {
	cmd := exec.Command(GlobalConfig.ChromePath, "--incognito", "--dns-prefetch-disable", "--single-process", "--proxy-server=socks5://localhost:"+GlobalConfig.LocalPort, "--user-data-dir=/tmp/chrome")
	if err := cmd.Start(); err == nil {
		color.Info.Println("启动本地chromec成功")
	}
}

// 初始化读取配置
func init() {
	// 直接赋值给结构体
	if _, err := GlobalConfig.getConfig(); err != nil {
		color.Error.Println(err.Error())
	}
}

func main() {
	sshClient, err := connectToSSH()
	if err != nil {
		color.Error.Println("连接远程服务器失败")
		panic(0)
	}

	// 开始监听本地端口
	go socks5ProxyStart(sshClient)
	// 启动本地chrome
	if GlobalConfig.UseChrome {
		go startChrome()
	} else {
		color.Info.Println("当前启动方式为: 仅启动socks5代理")
	}
	ch := make(chan os.Signal)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	<-ch
	return
}
