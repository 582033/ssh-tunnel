package gui

import (
	"image/color"
	"os"
	"strconv"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// surface 一块圆角底板。界面里所有分组都坐在它上面，
// 靠明度差和背景区分，不画边框——线一多就显得杂。
func surface(content fyne.CanvasObject, padV, padH float32) fyne.CanvasObject {
	bg := canvas.NewRectangle(theme.Color(theme.ColorNameInputBackground))
	bg.CornerRadius = 10
	bg.StrokeColor = theme.Color(theme.ColorNameSeparator)
	bg.StrokeWidth = 1

	return container.NewStack(bg,
		container.New(layout.NewCustomPaddedLayout(padV, padV, padH, padH), content))
}

// card 分组容器：小标题 + 内容，整体坐在圆角底板上。
// 不用 widget.NewCard，它的标题字号偏大、留白也过多。
func card(title string, content fyne.CanvasObject) fyne.CanvasObject {
	return surface(container.New(layout.NewCustomPaddedVBoxLayout(6),
		sectionTitle(title), content), 12, 14)
}

// sectionTitle 卡片小标题：全大写式的弱化标签，靠字号和颜色降权，
// 不与正文抢注意力
func sectionTitle(text string) fyne.CanvasObject {
	t := canvas.NewText(text, theme.Color(theme.ColorNamePlaceHolder))
	t.TextSize = 11
	t.TextStyle = fyne.TextStyle{Bold: true}
	return t
}

// infoRow 只读信息的一行：左侧灰色标签，右侧取值。
// 比 widget.Form 更可控——Form 的标签列宽由最长项决定，
// 几组信息并排时列宽不一致，看起来是歪的。
func infoRow(label string, value *widget.Label) fyne.CanvasObject {
	l := canvas.NewText(label, theme.Color(theme.ColorNamePlaceHolder))
	l.TextSize = 12

	return container.NewBorder(nil, nil,
		container.NewGridWrap(fyne.NewSize(78, value.MinSize().Height),
			container.New(layout.NewCustomPaddedLayout(3, 0, 0, 0), l)),
		nil, value)
}

// newValueLabel 用于只读的取值展示
func newValueLabel() *widget.Label {
	l := widget.NewLabel("")
	l.Truncation = fyne.TextTruncateEllipsis
	return l
}

// hint 表单项下方的灰色小字说明
func hint(text string) fyne.CanvasObject {
	t := canvas.NewText(text, theme.Color(theme.ColorNamePlaceHolder))
	t.TextSize = 11
	return container.New(layout.NewCustomPaddedLayout(2, 2, 2, 0), t)
}

// dot 状态灯。canvas.Circle 会被拉成椭圆，固定尺寸后再居中。
func fixedDot(c color.NRGBA, size float32) (*canvas.Circle, fyne.CanvasObject) {
	dot := canvas.NewCircle(c)
	return dot, container.NewCenter(
		container.NewGridWrap(fyne.NewSize(size, size), dot))
}

func orDash(s string) string {
	s = strings.TrimSpace(strings.Trim(strings.TrimSpace(s), ":"))
	if s == "" {
		return "—"
	}
	return s
}

func authSummary(privateKey, password string) string {
	var methods []string
	if privateKey != "" {
		methods = append(methods, "私钥")
	}
	if password != "" {
		methods = append(methods, "密码")
	}
	if len(methods) == 0 {
		return "未配置"
	}
	return strings.Join(methods, " + ")
}

func itoa(n int) string { return strconv.Itoa(n) }

func homeDir() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return "/"
	}
	return h
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
