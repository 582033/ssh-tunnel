package gui

import (
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"
)

// appTheme 在 Fyne 默认主题上做一层覆盖：换掉那身饱和的蓝、
// 把圆角和留白调大。只改颜色和尺寸，字体和图标沿用默认，
// 这样系统切换深浅色时仍然自动跟随。
type appTheme struct{ fyne.Theme }

func newAppTheme() fyne.Theme { return &appTheme{Theme: theme.DefaultTheme()} }

var (
	// 主色取图标上的蓝，界面和图标是同一套颜色
	primaryLight = color.NRGBA{R: 0x2E, G: 0x6B, B: 0xE8, A: 0xFF}
	primaryDark  = color.NRGBA{R: 0x5A, G: 0x93, B: 0xFF, A: 0xFF}

	// 背景与卡片：浅色下用一点冷灰而非纯白，卡片再压一档，
	// 卡片边界不用描边也能看出来
	bgLight    = color.NRGBA{R: 0xF4, G: 0xF6, B: 0xFA, A: 0xFF}
	surfLight  = color.NRGBA{R: 0xFF, G: 0xFF, B: 0xFF, A: 0xFF}
	bgDark     = color.NRGBA{R: 0x16, G: 0x1A, B: 0x21, A: 0xFF}
	surfDark   = color.NRGBA{R: 0x1E, G: 0x24, B: 0x2E, A: 0xFF}
	borderLite = color.NRGBA{R: 0xD8, G: 0xDF, B: 0xEA, A: 0xFF}
	borderDark = color.NRGBA{R: 0x34, G: 0x3C, B: 0x4A, A: 0xFF}
)

func (t *appTheme) Color(name fyne.ThemeColorName, v fyne.ThemeVariant) color.Color {
	dark := v == theme.VariantDark

	switch name {
	case theme.ColorNamePrimary, theme.ColorNameHyperlink:
		if dark {
			return primaryDark
		}
		return primaryLight

	case theme.ColorNameBackground:
		if dark {
			return bgDark
		}
		return bgLight

	// 输入框和卡片共用这一档：比背景亮（浅色）或暗（深色）
	case theme.ColorNameInputBackground, theme.ColorNameMenuBackground,
		theme.ColorNameOverlayBackground:
		if dark {
			return surfDark
		}
		return surfLight

	case theme.ColorNameInputBorder, theme.ColorNameSeparator:
		if dark {
			return borderDark
		}
		return borderLite

	// 顶部状态条：比背景略深一点，形成分区而不需要画线
	case theme.ColorNameHeaderBackground:
		if dark {
			return color.NRGBA{R: 0x1A, G: 0x20, B: 0x29, A: 0xFF}
		}
		return color.NRGBA{R: 0xEA, G: 0xEF, B: 0xF7, A: 0xFF}

	case theme.ColorNameSuccess:
		return colorConnected
	case theme.ColorNameWarning:
		return colorPending
	case theme.ColorNameError:
		return colorFailed
	}

	return t.Theme.Color(name, v)
}

func (t *appTheme) Size(name fyne.ThemeSizeName) float32 {
	switch name {
	// 默认圆角偏小，显得方；统一放大到 10/8
	case theme.SizeNameInputRadius, theme.SizeNameButtonRadius:
		return 8
	case theme.SizeNameCardRadius, theme.SizeNameDialogRadius,
		theme.SizeNamePopupRadius, theme.SizeNameMenuRadius:
		return 10
	case theme.SizeNameSelectionRadius:
		return 6
	// 控件之间松一点，密排的表单最容易显得廉价
	case theme.SizeNamePadding:
		return 5
	case theme.SizeNameInnerPadding:
		return 10
	case theme.SizeNameSeparatorThickness:
		return 1
	case theme.SizeNameScrollBar:
		return 10
	case theme.SizeNameSubHeadingText:
		return 15
	}
	return t.Theme.Size(name)
}
