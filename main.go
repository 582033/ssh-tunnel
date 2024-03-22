package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gookit/color"
	"golang.org/x/crypto/ssh"
	"gopkg.in/yaml.v2"
	"ssh-tunnel/libs/chrome"
	"ssh-tunnel/libs/socks5"
)

// GlobalConfig 定义一个 config结构体变量
var GlobalConfig Config

type AuthType string

const (
	AuthTypePassword   AuthType = "password"
	AuthTypePrivateKey AuthType = "private_key"
)

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
	flag.StringVar(&configFile, "config", "./config/config.yaml", "配置文件路径,默认为./config/config.yaml")
	flag.Parse()

	// 如果配置文件后缀不是yaml结尾的，就报错
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
	color.Success.Println("连接远程服务器成功, 本地端口：" + GlobalConfig.LocalPort)
	return client, nil
}

// 启动本地chrome
func startChrome() {
	startUp := &chrome.StartupParams{
		ChromePath: GlobalConfig.ChromePath,
		RunParams: []string{
			"--incognito",
			"--dns-prefetch-disable",
			"--single-process",
			"--proxy-server=socks5://localhost:" + GlobalConfig.LocalPort,
			"--user-data-dir=/tmp/chrome",
		},
	}
	startUp.Start()
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
	sock5Server := &socks5.Socks5Server{
		ProxyPort: GlobalConfig.LocalPort,
		CustomDNS: GlobalConfig.CustomDNS,
	}
	go sock5Server.ProxyStart(sshClient)
	// 启动本地chrome
	if GlobalConfig.UseChrome {
		go startChrome()
	} else {
		color.Info.Println("当前启动方式为: 仅启动socks5代理")
	}

	// 监测ssh连接状态
	go func() {
		quit := make(chan struct{})
		defer close(quit)
		for {
			select {
			case <-quit:
				return
			default:
				if sshClient.Conn.Wait() != nil {
					color.Error.Println("SSH连接断开，正在尝试重新连接...")
					sshClient.Close()
					sock5Server.Close()

					// 添加适当的延迟
					time.Sleep(5 * time.Second)

					newSSHClient, err := connectToSSH()
					if err != nil {
						color.Error.Println("重新连接失败:", err)
					} else {
						sshClient = newSSHClient
						color.Success.Println("重新连接成功")
						go sock5Server.ProxyStart(sshClient)
					}
				}
			}
		}
	}()
	ch := make(chan os.Signal)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	<-ch
}
