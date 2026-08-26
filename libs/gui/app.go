package gui

import (
	"image/color"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	fyneapp "fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"ssh-tunnel/libs/config"
	"ssh-tunnel/libs/tunnel"
)

const (
	// 出站连接会逐条记日志，300 条很快就被冲掉，留大一点便于回溯
	maxLogLines   = 1000
	logFlushEvery = 300 * time.Millisecond
)

var (
	colorConnected = color.NRGBA{R: 0x25, G: 0xA5, B: 0x5B, A: 0xFF} // 绿
	colorPending   = color.NRGBA{R: 0xE8, G: 0x9B, B: 0x1C, A: 0xFF} // 黄
	colorFailed    = color.NRGBA{R: 0xDC, G: 0x3B, B: 0x33, A: 0xFF} // 红
	colorStopped   = color.NRGBA{R: 0x94, G: 0x9A, B: 0xA6, A: 0xFF} // 灰
)

type App struct {
	fyneApp fyne.App
	win     fyne.Window

	// store 全部连接配置；mgr 只认当前选中的那一份
	store *config.Store
	mgr   *tunnel.Manager

	dot        *canvas.Circle
	statusText *canvas.Text
	proxyText  *canvas.Text

	// picker 顶部连接下拉。切换时会断开旧连接。
	picker *widget.Select
	// suppressPick 为 true 时忽略 picker 的 OnChanged：
	// 代码里调用 SetOptions/SetSelected 也会触发它
	suppressPick bool

	btnToggle *widget.Button
	btnChrome *widget.Button
	btnCopy   *widget.Button

	tabs *container.AppTabs
	form *settingsForm

	refreshStatusTab func()

	logEntry  *widget.Entry
	logMu     sync.Mutex
	logLines  []string
	logDirty  bool
	stopFlush chan struct{}
	flushOnce sync.Once
}

// Run 创建窗口并进入事件循环。loadErr 非空表示配置尚不可用，
// 此时切到「设置」页提示用户补全。
func Run(store *config.Store, loadErr error) {
	a := fyneapp.NewWithID("cn.yjiang.ssh-tunnel")
	a.SetIcon(appIcon())
	a.Settings().SetTheme(newAppTheme())

	w := a.NewWindow("SSH Tunnel")
	w.SetIcon(appIcon())

	app := &App{
		fyneApp:   a,
		win:       w,
		store:     store,
		mgr:       tunnel.NewManager(store.Current()),
		stopFlush: make(chan struct{}),
	}
	app.build()

	app.mgr.OnState = app.onState
	app.mgr.OnLog = app.appendLog

	go app.logFlusher()

	w.Resize(fyne.NewSize(600, 660))
	w.CenterOnScreen()
	w.SetCloseIntercept(app.quit)
	// 从 Dock 或 Cmd+Q 退出时不会走 CloseIntercept，
	// 这里兜底，确保隧道和 Chrome 一起收掉。
	a.Lifecycle().SetOnStopped(app.cleanup)

	if loadErr != nil {
		app.appendLog("配置需要完善: " + loadErr.Error())
		app.tabs.SelectIndex(1)
		w.Show()
		dialog.ShowInformation("请先完成设置",
			"未找到可用配置：\n\n"+loadErr.Error()+"\n\n请在「设置」中填写，然后点「保存并连接」。", w)
	} else if store.Current().AutoConnect {
		// 配置就绪且开启了自动连接：打开即连
		go func() {
			if err := app.mgr.Start(); err != nil {
				app.appendLog("启动失败: " + err.Error())
			}
		}()
	}

	w.ShowAndRun()
}

func (a *App) quit() {
	a.cleanup()
	a.fyneApp.Quit()
}

// cleanup 停隧道、关 Chrome、停日志刷新。可能被 CloseIntercept 和
// Lifecycle.OnStopped 各调用一次，所以要幂等。
func (a *App) cleanup() {
	a.flushOnce.Do(func() { close(a.stopFlush) })
	a.mgr.Stop()
}

// build 组装界面：顶部状态卡（含连接下拉）+ 中间标签页 + 底部操作按钮。
func (a *App) build() {
	a.tabs = container.NewAppTabs(
		container.NewTabItemWithIcon("状态", theme.HomeIcon(), a.buildStatusTab()),
		container.NewTabItemWithIcon("设置", theme.SettingsIcon(), a.buildSettingsTab()),
		container.NewTabItemWithIcon("日志", theme.ListIcon(), a.buildLogTab()),
	)

	a.win.SetContent(container.NewBorder(
		a.buildBanner(),
		a.buildActions(),
		nil, nil,
		a.tabs,
	))
}

// buildBanner 顶部状态卡：左侧状态灯 + 两行文字，右侧连接下拉。
// 状态和「连的是哪个」是最常看的两件事，放在一起且始终可见。
func (a *App) buildBanner() fyne.CanvasObject {
	var lamp fyne.CanvasObject
	a.dot, lamp = fixedDot(colorStopped, 11)

	a.statusText = canvas.NewText("已停止", theme.Color(theme.ColorNameForeground))
	a.statusText.TextSize = 18
	a.statusText.TextStyle = fyne.TextStyle{Bold: true}

	a.proxyText = canvas.NewText("代理未启动", theme.Color(theme.ColorNamePlaceHolder))
	a.proxyText.TextSize = 12

	texts := container.New(layout.NewCustomPaddedVBoxLayout(3),
		a.statusText, a.proxyText)

	left := container.NewBorder(nil, nil,
		container.New(layout.NewCustomPaddedLayout(0, 0, 2, 8), lamp),
		nil, texts)

	// 下拉固定宽度，否则名字一长就把状态文字挤没了
	pickerBox := a.buildPicker()

	label := canvas.NewText("连接", theme.Color(theme.ColorNamePlaceHolder))
	label.TextSize = 11
	right := container.New(layout.NewCustomPaddedVBoxLayout(2),
		container.New(layout.NewCustomPaddedLayout(0, 0, 4, 0), label),
		pickerBox)

	// 左侧竖直居中但保持左对齐：状态文字居中排会显得没有落脚点
	row := container.NewBorder(nil, nil, nil, right,
		container.NewVBox(layout.NewSpacer(), left, layout.NewSpacer()))

	return container.New(layout.NewCustomPaddedLayout(12, 6, 14, 14),
		surface(container.New(layout.NewCustomPaddedLayout(2, 2, 4, 4), row), 12, 12))
}

func (a *App) buildPicker() fyne.CanvasObject {
	a.picker = widget.NewSelect(a.store.Names(), a.onPickConnection)
	a.picker.SetSelected(a.store.Active)

	// 下拉本身固定宽度：不限的话名字一长就把左边的状态文字挤没了，
	// 太窄又会显示成「默…」，150 大约能放 6 个汉字
	pickerBox := container.NewGridWrap(
		fyne.NewSize(150, a.picker.MinSize().Height), a.picker)

	// 加/删连接放在下拉旁边，而不是埋进「设置」页：
	// 这两个动作和选择连接是同一件事
	btnAdd := widget.NewButtonWithIcon("", theme.ContentAddIcon(), a.onAddConnection)
	btnAdd.Importance = widget.LowImportance
	btnDel := widget.NewButtonWithIcon("", theme.DeleteIcon(), a.onDeleteConnection)
	btnDel.Importance = widget.LowImportance

	return container.NewHBox(pickerBox, btnAdd, btnDel)
}

func (a *App) buildActions() fyne.CanvasObject {
	a.btnToggle = widget.NewButtonWithIcon("连接", theme.MediaPlayIcon(), a.onToggle)
	a.btnToggle.Importance = widget.HighImportance

	a.btnChrome = widget.NewButtonWithIcon("打开 Chrome", theme.ComputerIcon(), a.onOpenChrome)
	a.btnCopy = widget.NewButtonWithIcon("复制代理地址", theme.ContentCopyIcon(), a.onCopy)
	a.btnChrome.Disable()
	a.btnCopy.Disable()

	// 主按钮更宽：三等分时「连接」和次要动作视觉权重一样，不好找
	row := container.NewGridWithColumns(3, a.btnToggle, a.btnChrome, a.btnCopy)
	return container.New(layout.NewCustomPaddedLayout(4, 12, 14, 14), row)
}

func (a *App) buildLogTab() fyne.CanvasObject {
	a.logEntry = widget.NewMultiLineEntry()
	a.logEntry.TextStyle = fyne.TextStyle{Monospace: true}
	a.logEntry.Wrapping = fyne.TextWrapBreak

	toolbar := container.NewBorder(nil, nil,
		hint("保留最近 "+itoa(maxLogLines)+" 条"),
		container.NewHBox(
			widget.NewButtonWithIcon("复制全部", theme.ContentCopyIcon(), a.copyLog),
			widget.NewButtonWithIcon("清空", theme.DeleteIcon(), a.clearLog),
		),
		nil)

	return container.New(layout.NewCustomPaddedLayout(12, 12, 14, 14),
		container.NewBorder(nil, toolbar, nil, nil, a.logEntry))
}

// buildStatusTab 只读信息卡：服务器 / 认证 / 代理 / DNS / 配置路径。
func (a *App) buildStatusTab() fyne.CanvasObject {
	server := newValueLabel()
	user := newValueLabel()
	auth := newValueLabel()
	proxy := newValueLabel()
	dns := newValueLabel()
	chrome := newValueLabel()
	confPath := newValueLabel()
	confPath.Wrapping = fyne.TextWrapBreak
	confPath.Truncation = fyne.TextTruncateOff

	refresh := func() {
		c := a.mgr.Config()
		server.SetText(orDash(c.ServerAddr + ":" + c.ServerPort))
		user.SetText(orDash(c.Username))
		auth.SetText(authSummary(c.PrivateKey, c.Password))
		proxy.SetText(c.ProxyAddr())
		if c.CustomDNS == "" {
			dns.SetText("由 SSH 服务器解析（推荐）")
		} else {
			dns.SetText(c.CustomDNS + "（经隧道查询）")
		}
		if c.UseChrome {
			chrome.SetText("连接后自动打开无痕窗口")
		} else {
			chrome.SetText("不自动打开")
		}
		confPath.SetText(a.store.Path)
	}
	refresh()
	a.refreshStatusTab = refresh

	info := container.New(layout.NewCustomPaddedVBoxLayout(2),
		infoRow("服务器", server),
		infoRow("用户名", user),
		infoRow("认证", auth),
		infoRow("本地代理", proxy),
		infoRow("域名解析", dns),
		infoRow("Chrome", chrome),
	)

	body := container.New(layout.NewCustomPaddedVBoxLayout(10),
		card("当前连接", info),
		card("配置文件", confPath),
	)

	return container.NewVScroll(
		container.New(layout.NewCustomPaddedLayout(12, 12, 14, 14), body))
}

func (a *App) buildSettingsTab() fyne.CanvasObject {
	a.form = newSettingsForm(a)
	return a.form.canvas()
}
