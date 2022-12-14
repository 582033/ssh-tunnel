package main

import (
	"context"
	"flag"
	"fmt"
	"io/ioutil"
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

// Config 声明 配置结构体
type Config struct {
	Username   string `yaml:"username"`
	Password   string `yaml:"password"`
	ServerAddr string `yaml:"serverAddr"`
	ServerPort string `yaml:"serverPort"`
	LocalPort  string `yaml:"localPort"`
	ChromePath string `yaml:"chromePath"`
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
	yamlFile, err := ioutil.ReadFile(configFile)
	if err != nil {
		return nil, err
	}
	err = yaml.Unmarshal(yamlFile, c)
	if err != nil {
		return nil, err
	}
	return c, nil
}

func socks5ProxyStart(sshClient *ssh.Client) {
	config := &socks5.Config{
		Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return sshClient.Dial(network, addr)
		},
		Resolver: socks5.DNSResolver{},
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
		User: GlobalConfig.Username,
		Auth: []ssh.AuthMethod{
			ssh.Password(GlobalConfig.Password),
		},
		Timeout:         30 * time.Second,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	}
	client, err := ssh.Dial("tcp", GlobalConfig.ServerAddr+":"+GlobalConfig.ServerPort, config)
	if err != nil {
		return nil, err
	}
	// 连接远程服务器成功
	color.Success.Println("连接远程服务器成功")
	return client, nil
}

//启动本地chrome
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
		color.Error.Println("配置文件读取失败")
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
	go startChrome()
	ch := make(chan os.Signal)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	<-ch
	return
}
