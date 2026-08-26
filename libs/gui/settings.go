package gui

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/storage"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"ssh-tunnel/libs/config"
)

// DNS 解析位置的两个选项
const (
	dnsRemote = "由 SSH 服务器解析（推荐）"
	dnsCustom = "指定 DNS，经隧道查询"
)

// settingsForm 是「设置」页，编辑的始终是下拉里当前选中的那一条连接，
// 保存时整份配置文件一起写回，不需要用户手动改文件。
type settingsForm struct {
	app *App

	name       *widget.Entry
	serverAddr *widget.Entry
	serverPort *widget.Entry
	username   *widget.Entry
	password   *widget.Entry
	privateKey *widget.Entry
	localPort  *widget.Entry
	dnsMode    *widget.RadioGroup
	customDNS  *widget.Entry
	useChrome  *widget.Check
	chromePath *widget.Entry
	autoConn   *widget.Check
}

func newSettingsForm(app *App) *settingsForm {
	f := &settingsForm{app: app}

	f.name = entry("如「公司」「家里」")
	f.serverAddr = entry("example.com")
	f.serverPort = entry("22")
	f.username = entry("root")
	f.password = widget.NewPasswordEntry()
	f.password.SetPlaceHolder("留空则仅用私钥")
	f.privateKey = entry("~/.ssh/id_rsa，留空则仅用密码")
	f.localPort = entry("1081")
	f.customDNS = entry("8.8.8.8:53")
	f.chromePath = entry("")

	f.dnsMode = widget.NewRadioGroup([]string{dnsRemote, dnsCustom}, func(sel string) {
		if sel == dnsCustom {
			f.customDNS.Enable()
		} else {
			f.customDNS.Disable()
		}
	})

	f.useChrome = widget.NewCheck("连接成功后自动打开无痕 Chrome", func(on bool) {
		if on {
			f.chromePath.Enable()
		} else {
			f.chromePath.Disable()
		}
	})
	f.autoConn = widget.NewCheck("打开 App 后自动连接这条", nil)

	f.load(app.store.Current())
	return f
}

func entry(placeholder string) *widget.Entry {
	e := widget.NewEntry()
	if placeholder != "" {
		e.SetPlaceHolder(placeholder)
	}
	return e
}

// load 把配置填进表单控件
func (f *settingsForm) load(c *config.Config) {
	f.name.SetText(c.Name)
	f.serverAddr.SetText(c.ServerAddr)
	f.serverPort.SetText(c.ServerPort)
	f.username.SetText(c.Username)
	f.password.SetText(c.Password)
	f.privateKey.SetText(c.PrivateKey)
	f.localPort.SetText(c.LocalPort)
	f.chromePath.SetText(c.ChromePath)

	f.customDNS.SetText(c.CustomDNS)
	if c.CustomDNS == "" {
		f.dnsMode.SetSelected(dnsRemote)
	} else {
		f.dnsMode.SetSelected(dnsCustom)
	}

	f.useChrome.SetChecked(c.UseChrome)
	f.autoConn.SetChecked(c.AutoConnect)
}

// collect 从表单读回一份副本，不动 store 里的原件
func (f *settingsForm) collect() *config.Config {
	c := f.app.store.Current().Clone()

	c.Name = f.name.Text
	c.ServerAddr = f.serverAddr.Text
	c.ServerPort = f.serverPort.Text
	c.Username = f.username.Text
	c.Password = f.password.Text
	c.PrivateKey = f.privateKey.Text
	c.LocalPort = f.localPort.Text
	c.UseChrome = f.useChrome.Checked
	c.ChromePath = f.chromePath.Text
	c.AutoConnect = f.autoConn.Checked

	if f.dnsMode.Selected == dnsCustom {
		c.CustomDNS = f.customDNS.Text
	} else {
		c.CustomDNS = ""
	}
	return c
}

// dirty 表单是否与 store 里的当前连接有差异。
// 切换连接前用它决定要不要问「放弃未保存的修改？」。
// 这里故意不调 Validate——它会 trim 并补默认值，
// 未改动的表单也会因为归一化差异被判成脏。
func (f *settingsForm) dirty() bool {
	return *f.collect() != *f.app.store.Current()
}

func (f *settingsForm) canvas() fyne.CanvasObject {
	body := container.New(layout.NewCustomPaddedVBoxLayout(10),
		card("连接名称", f.nameCard()),
		card("服务器", f.serverCard()),
		card("本地代理", f.proxyCard()),
		card("浏览器", f.chromeCard()),
	)

	return container.NewBorder(
		nil, f.buttons(), nil, nil,
		container.NewVScroll(
			container.New(layout.NewCustomPaddedLayout(12, 12, 14, 14), body)),
	)
}

// nameCard 名称和「自动连接」放一起：这两项描述的是「这条连接本身」，
// 而不是怎么连。
func (f *settingsForm) nameCard() fyne.CanvasObject {
	return container.NewVBox(
		f.name,
		f.autoConn,
		hint("名称显示在顶部下拉里；改这里即为重命名"),
	)
}

func (f *settingsForm) serverCard() fyne.CanvasObject {
	browseKey := widget.NewButtonWithIcon("", theme.FolderOpenIcon(), func() {
		d := dialog.NewFileOpen(func(r fyne.URIReadCloser, err error) {
			if err != nil || r == nil {
				return
			}
			defer r.Close()
			f.privateKey.SetText(r.URI().Path())
		}, f.app.win)
		// 私钥通常在 ~/.ssh，默认定位到那里
		if home, err := storage.ListerForURI(storage.NewFileURI(homeDir())); err == nil {
			d.SetLocation(home)
		}
		d.Show()
	})

	// 地址和端口放一行，省一行高度
	addrRow := container.NewBorder(nil, nil, nil,
		container.NewGridWrap(fyne.NewSize(72, f.serverPort.MinSize().Height), f.serverPort),
		f.serverAddr)

	form := widget.NewForm(
		widget.NewFormItem("地址 / 端口", addrRow),
		widget.NewFormItem("用户名", f.username),
		widget.NewFormItem("密码", f.password),
		widget.NewFormItem("私钥", container.NewBorder(nil, nil, nil, browseKey, f.privateKey)),
	)

	return container.NewVBox(form, hint("密码和私钥至少填一项；都填则由服务器决定用哪种"))
}

func (f *settingsForm) proxyCard() fyne.CanvasObject {
	form := widget.NewForm(
		widget.NewFormItem("本地端口", f.localPort),
		widget.NewFormItem("域名解析", f.dnsMode),
		widget.NewFormItem("DNS 服务器", f.customDNS),
	)
	return container.NewVBox(form,
		hint("多条连接建议用不同端口，切换时不必改浏览器设置"))
}

func (f *settingsForm) chromeCard() fyne.CanvasObject {
	browseChrome := widget.NewButtonWithIcon("", theme.FolderOpenIcon(), func() {
		dialog.NewFileOpen(func(r fyne.URIReadCloser, err error) {
			if err != nil || r == nil {
				return
			}
			defer r.Close()
			f.chromePath.SetText(r.URI().Path())
		}, f.app.win).Show()
	})

	form := widget.NewForm(
		widget.NewFormItem("", f.useChrome),
		widget.NewFormItem("Chrome 路径", container.NewBorder(nil, nil, nil, browseChrome, f.chromePath)),
	)
	return container.NewVBox(form, hint("App 退出时会一并关闭它启动的 Chrome 窗口"))
}

func (f *settingsForm) buttons() fyne.CanvasObject {
	btnRevert := widget.NewButtonWithIcon("放弃修改", theme.ContentUndoIcon(), func() {
		f.load(f.app.store.Current())
	})
	btnSave := widget.NewButtonWithIcon("保存", theme.DocumentSaveIcon(), func() {
		f.save(false)
	})
	btnSaveConnect := widget.NewButtonWithIcon("保存并连接", theme.MediaPlayIcon(), func() {
		f.save(true)
	})
	// 三个都不做高亮：底部那排的「连接」才是全窗口唯一的主按钮，
	// 这里再来一个蓝的，两排蓝按钮上下叠着反而看不出主次

	row := container.NewGridWithColumns(3, btnRevert, btnSave, btnSaveConnect)
	return container.NewVBox(
		widget.NewSeparator(),
		container.New(layout.NewCustomPaddedLayout(8, 8, 14, 14), row),
	)
}

// save 校验并把整份配置写回文件；connect 为 true 时保存后立即重连。
// 名字可能被改过，所以是先 Replace（用旧名字定位）再 Save。
func (f *settingsForm) save(connect bool) {
	oldName := f.app.store.Active

	c := f.collect()
	if err := c.Validate(); err != nil {
		dialog.ShowError(err, f.app.win)
		return
	}

	// 改配置需要重启隧道才能生效
	wasRunning := f.app.mgr.IsRunning()
	if wasRunning || connect {
		f.app.mgr.Stop()
	}

	if err := f.app.store.Replace(oldName, c); err != nil {
		dialog.ShowError(err, f.app.win)
		return
	}
	if err := f.app.store.Save(); err != nil {
		dialog.ShowError(err, f.app.win)
		return
	}
	if err := f.app.mgr.SetConfig(c); err != nil {
		dialog.ShowError(err, f.app.win)
		return
	}

	// 名字变了要同步下拉，否则选项还是旧名字
	f.app.refreshPicker()
	f.load(f.app.store.Current())
	f.app.appendLog("已保存连接「" + c.Name + "」到 " + f.app.store.Path)
	if f.app.refreshStatusTab != nil {
		f.app.refreshStatusTab()
	}

	if connect || wasRunning {
		go func() {
			if err := f.app.mgr.Start(); err != nil {
				f.app.appendLog("启动失败: " + err.Error())
			}
		}()
		f.app.tabs.SelectIndex(0)
	} else {
		dialog.ShowInformation("已保存", "配置已写入：\n"+f.app.store.Path, f.app.win)
	}
}
