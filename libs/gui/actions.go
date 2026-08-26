package gui

import (
	"fmt"
	"image/color"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"ssh-tunnel/libs/config"
	"ssh-tunnel/libs/tunnel"
)

// onToggle 连接 / 断开
func (a *App) onToggle() {
	if a.mgr.IsRunning() {
		a.btnToggle.Disable()
		go func() {
			a.mgr.Stop()
			fyne.Do(a.btnToggle.Enable)
		}()
		return
	}

	if err := a.mgr.Config().Validate(); err != nil {
		a.appendLog("无法连接: " + err.Error())
		a.tabs.SelectIndex(1)
		return
	}
	go func() {
		if err := a.mgr.Start(); err != nil {
			a.appendLog("启动失败: " + err.Error())
		}
	}()
}

func (a *App) onOpenChrome() {
	if !a.mgr.IsRunning() {
		a.appendLog("请先连接隧道")
		return
	}
	a.mgr.StartBrowser()
}

func (a *App) onCopy() {
	addr := a.mgr.Config().ProxyAddr()
	a.fyneApp.Clipboard().SetContent(addr)
	a.appendLog("已复制代理地址 " + addr)
}

// onPickConnection 下拉切换连接。同一时刻只连一个，
// 所以先断开当前的，再把 Manager 指向新的一份配置；
// 原来在连着就顺势连上新的，符合「切过去」的预期。
func (a *App) onPickConnection(name string) {
	if a.suppressPick || name == "" || name == a.store.Active {
		return
	}

	// 直接切走会把没保存的修改丢掉，先问一句
	if a.form != nil && a.form.dirty() {
		from := a.store.Active
		dialog.ShowConfirm("放弃未保存的修改？",
			"「"+from+"」有修改尚未保存，切换连接会丢弃它们。",
			func(ok bool) {
				if !ok {
					a.setPicker(from)
					return
				}
				a.switchTo(name)
			}, a.win)
		return
	}
	a.switchTo(name)
}

func (a *App) switchTo(name string) {
	wasRunning := a.mgr.IsRunning()
	if wasRunning {
		a.mgr.Stop()
	}

	if err := a.store.SetActive(name); err != nil {
		dialog.ShowError(err, a.win)
		a.setPicker(a.store.Active)
		return
	}
	// 选中项也要落盘，下次打开还是这一个
	if err := a.store.Save(); err != nil {
		a.appendLog("保存选中连接失败: " + err.Error())
	}

	if err := a.applyCurrent(); err != nil {
		dialog.ShowError(err, a.win)
		return
	}
	a.appendLog("已切换到连接「" + name + "」")

	if wasRunning {
		go func() {
			if err := a.mgr.Start(); err != nil {
				a.appendLog("启动失败: " + err.Error())
			}
		}()
	}
}

// applyCurrent 把 store 当前选中的配置推给 Manager，并刷新界面各处。
// 调用前必须确保隧道已停止——SetConfig 在运行中会拒绝。
func (a *App) applyCurrent() error {
	cur := a.store.Current()
	if err := a.mgr.SetConfig(cur); err != nil {
		return err
	}

	a.form.load(cur)
	if a.refreshStatusTab != nil {
		a.refreshStatusTab()
	}
	if !a.mgr.IsRunning() {
		a.onState(tunnel.StateStopped, "")
	}
	return nil
}

// onAddConnection 新建一条连接。名字必填且不能与已有重复，
// 否则下拉里两个同名项没法区分。
func (a *App) onAddConnection() {
	d := dialog.NewEntryDialog("新建连接", "名称", func(name string) {
		name = strings.TrimSpace(name)
		if name == "" {
			return
		}
		if a.store.Index(name) >= 0 {
			dialog.ShowError(fmt.Errorf("已存在名为 %q 的连接，请换一个名字", name), a.win)
			return
		}

		c := config.Default()
		c.Name = name
		// 新连接默认不自动连：它还没填完，下次打开 App 不该去连它
		c.AutoConnect = false
		// 端口顺延，省得用户自己找一个没被占用的
		c.LocalPort = a.nextFreePort()

		if a.mgr.IsRunning() {
			a.mgr.Stop()
		}
		a.store.Add(c)
		if err := a.store.Save(); err != nil {
			a.appendLog("保存失败: " + err.Error())
		}

		a.refreshPicker()
		if err := a.applyCurrent(); err != nil {
			dialog.ShowError(err, a.win)
			return
		}
		a.appendLog("已新建连接「" + name + "」，请在「设置」中填写")
		a.tabs.SelectIndex(1)
	}, a.win)
	d.SetPlaceholder("如「公司」「家里」")
	d.Show()
}

// nextFreePort 在已有连接的本地端口之后顺延一个，
// 让多份配置默认不互相占用端口。
func (a *App) nextFreePort() string {
	used := make(map[string]bool, len(a.store.Connections))
	for _, c := range a.store.Connections {
		used[c.LocalPort] = true
	}
	for p := 1081; p < 1181; p++ {
		s := itoa(p)
		if !used[s] {
			return s
		}
	}
	return "1081"
}

func (a *App) onDeleteConnection() {
	name := a.store.Active
	if len(a.store.Connections) <= 1 {
		dialog.ShowInformation("无法删除",
			"至少要保留一个连接，否则没有地方填写配置。", a.win)
		return
	}

	dialog.ShowConfirm("删除连接", "确定删除「"+name+"」？此操作不可撤销。",
		func(ok bool) {
			if !ok {
				return
			}
			if a.mgr.IsRunning() {
				a.mgr.Stop()
			}
			if err := a.store.Remove(name); err != nil {
				dialog.ShowError(err, a.win)
				return
			}
			if err := a.store.Save(); err != nil {
				a.appendLog("保存失败: " + err.Error())
			}

			a.refreshPicker()
			if err := a.applyCurrent(); err != nil {
				dialog.ShowError(err, a.win)
				return
			}
			a.appendLog("已删除连接「" + name + "」，当前为「" + a.store.Active + "」")
		}, a.win)
}

// refreshPicker 重建下拉选项。SetOptions/SetSelected 都会触发 OnChanged，
// 用 suppressPick 挡住，否则会把自己的刷新当成用户切换而递归。
func (a *App) refreshPicker() {
	a.suppressPick = true
	a.picker.SetOptions(a.store.Names())
	a.picker.SetSelected(a.store.Active)
	a.suppressPick = false
}

func (a *App) setPicker(name string) {
	a.suppressPick = true
	a.picker.SetSelected(name)
	a.suppressPick = false
}

// onState 由 tunnel.Manager 在后台 goroutine 调用，
// 所有 UI 更新必须走 fyne.Do 回到主线程。
func (a *App) onState(s tunnel.State, detail string) {
	fyne.Do(func() {
		switch s {
		case tunnel.StateConnected:
			a.setStatus(colorConnected, "已连接", detail)
			a.setToggle("断开", theme.MediaStopIcon(), widget.MediumImportance)
			a.btnChrome.Enable()
			a.btnCopy.Enable()
		case tunnel.StateConnecting, tunnel.StateReconnecting:
			a.setStatus(colorPending, s.String(), detail)
			a.setToggle("断开", theme.MediaStopIcon(), widget.MediumImportance)
			a.btnChrome.Disable()
			a.btnCopy.Disable()
		case tunnel.StateFailed:
			a.setStatus(colorFailed, "连接失败", truncate(detail, 70))
			a.setToggle("断开", theme.MediaStopIcon(), widget.MediumImportance)
			a.btnChrome.Disable()
			a.btnCopy.Disable()
		default:
			a.setStatus(colorStopped, "已停止", "代理未启动")
			a.setToggle("连接", theme.MediaPlayIcon(), widget.HighImportance)
			a.btnChrome.Disable()
			a.btnCopy.Disable()
		}
	})
}

// setToggle 只在停止态把主按钮做成高亮：连着的时候「断开」不该是最抢眼的那个。
func (a *App) setToggle(text string, icon fyne.Resource, imp widget.Importance) {
	a.btnToggle.SetText(text)
	a.btnToggle.SetIcon(icon)
	if a.btnToggle.Importance != imp {
		a.btnToggle.Importance = imp
		a.btnToggle.Refresh()
	}
}

// setStatus 更新顶部状态卡，只能在主线程调用
func (a *App) setStatus(dot color.NRGBA, title, detail string) {
	a.dot.FillColor = dot
	a.dot.Refresh()

	a.statusText.Text = title
	a.statusText.Refresh()

	if detail == "" {
		detail = "—"
	}
	a.proxyText.Text = detail
	a.proxyText.Refresh()
}

// appendLog 可从任意 goroutine 调用；只写内存，由 logFlusher 定时刷到界面。
// DNS 解析日志刷得很快，逐条 SetText 会把 UI 拖死。
func (a *App) appendLog(msg string) {
	line := time.Now().Format("15:04:05") + "  " + msg

	a.logMu.Lock()
	a.logLines = append(a.logLines, line)
	if len(a.logLines) > maxLogLines {
		a.logLines = a.logLines[len(a.logLines)-maxLogLines:]
	}
	a.logDirty = true
	a.logMu.Unlock()
}

func (a *App) logFlusher() {
	ticker := time.NewTicker(logFlushEvery)
	defer ticker.Stop()

	for {
		select {
		case <-a.stopFlush:
			return
		case <-ticker.C:
			a.logMu.Lock()
			if !a.logDirty {
				a.logMu.Unlock()
				continue
			}
			text := strings.Join(a.logLines, "\n")
			a.logDirty = false
			a.logMu.Unlock()

			fyne.Do(func() {
				a.logEntry.SetText(text)
				a.logEntry.CursorRow = strings.Count(text, "\n")
			})
		}
	}
}

func (a *App) clearLog() {
	a.logMu.Lock()
	a.logLines = nil
	a.logDirty = false
	a.logMu.Unlock()
	a.logEntry.SetText("")
}

// copyLog 把整段日志复制到剪贴板，便于贴出来排查问题
func (a *App) copyLog() {
	a.logMu.Lock()
	text := strings.Join(a.logLines, "\n")
	n := len(a.logLines)
	a.logMu.Unlock()

	if text == "" {
		a.appendLog("日志为空，无内容可复制")
		return
	}
	a.fyneApp.Clipboard().SetContent(text)
	a.appendLog("已复制 " + itoa(n) + " 行日志到剪贴板")
}
