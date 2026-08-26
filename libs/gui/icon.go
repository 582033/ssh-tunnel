package gui

import (
	_ "embed"

	"fyne.io/fyne/v2"
)

// appIconPNG 由 assets/make-icon.sh 从 assets/icon.svg 生成。
// 内嵌而不是从磁盘读：.app 里没有可靠的相对路径，
// 直接 -cli 运行时也不该依赖工作目录。
//
//go:embed appicon.png
var appIconPNG []byte

// appIcon 供 fyne.App.SetIcon 使用。
// .app 的 Dock/Finder 图标来自 bundle 里的 icon.icns，
// 这份 PNG 管的是 Fyne 自己画的窗口图标和未打包运行时的显示。
func appIcon() fyne.Resource {
	return fyne.NewStaticResource("icon.png", appIconPNG)
}
