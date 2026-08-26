package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/gookit/color"

	"ssh-tunnel/libs/config"
	"ssh-tunnel/libs/gui"
	"ssh-tunnel/libs/tunnel"
)

func main() {
	var (
		configFile string
		cliMode    bool
	)
	flag.StringVar(&configFile, "config", "", "配置文件路径，默认按 ~/.config/ssh-tunnel/config.yaml → ./config/config.yaml 顺序查找")
	flag.BoolVar(&cliMode, "cli", false, "以命令行模式运行（无图形界面）")
	flag.Parse()

	if cliMode {
		store, err := config.Load(configFile)
		if err != nil {
			color.Error.Println(err.Error())
			os.Exit(1)
		}
		cur := store.Current()
		color.Info.Println("使用连接: " + cur.Label())
		runCLI(tunnel.NewManager(cur))
		return
	}

	// 图形界面即使配置缺失也要能打开——否则用户没有地方填配置。
	// loadErr 交给界面提示，并自动切到「设置」页。
	store, loadErr := config.LoadForUI(configFile)
	gui.Run(store, loadErr)
}

func runCLI(mgr *tunnel.Manager) {
	mgr.OnLog = func(s string) { color.Info.Println(s) }
	mgr.OnState = func(s tunnel.State, detail string) {
		switch s {
		case tunnel.StateConnected:
			color.Success.Println("已连接，代理地址 " + detail)
		case tunnel.StateFailed:
			color.Error.Println("连接失败: " + detail)
		default:
			color.Info.Println(s.String() + " " + detail)
		}
	}

	if err := mgr.Start(); err != nil {
		color.Error.Println(err.Error())
		os.Exit(1)
	}

	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	<-ch

	fmt.Println()
	color.Info.Println("正在退出…")
	mgr.Stop()
}
