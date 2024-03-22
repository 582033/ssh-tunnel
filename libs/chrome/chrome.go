package chrome

import (
	"os/exec"

	"github.com/gookit/color"
)

var quit chan bool

type StartupParams struct {
	ChromePath string
	RunParams  []string
}

func (p *StartupParams) Start() {
	// 启动本地chrome
	// cmd := exec.Command(GlobalConfig.ChromePath, "--incognito", "--dns-prefetch-disable", "--single-process", "--proxy-server=socks5://localhost:"+GlobalConfig.LocalPort, "--user-data-dir=/tmp/chrome")

	cmd := exec.Command(p.ChromePath, p.RunParams...)
	if err := cmd.Start(); err == nil {
		color.Info.Println("启动本地chromec成功")
	}

	err := cmd.Wait()
	if err != nil {
		color.Error.Println("chromec进程退出:%s", err.Error())
	}
	quit <- true
}

//todo 退出chrome有问题
func (p *StartupParams) Close() {
	color.Info.Println("通知chromec进程退出")
	quit <- true
	<-quit
}
