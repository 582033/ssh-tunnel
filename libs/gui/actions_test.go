package gui

import (
	"testing"

	"ssh-tunnel/libs/config"
)

// nextFreePort 决定新建连接的默认端口。多条连接如果默认端口相同，
// 切过去就会撞上前一条留下的监听，所以必须避开已用的。
func TestNextFreePortSkipsUsed(t *testing.T) {
	a := &App{store: &config.Store{
		Connections: []*config.Config{
			{Name: "a", LocalPort: "1081"},
			{Name: "b", LocalPort: "1082"},
		},
	}}

	if got := a.nextFreePort(); got != "1083" {
		t.Fatalf("应顺延到 1083，实际 %q", got)
	}
}

func TestNextFreePortStartsAt1081(t *testing.T) {
	a := &App{store: &config.Store{
		Connections: []*config.Config{{Name: "a", LocalPort: "1090"}},
	}}

	if got := a.nextFreePort(); got != "1081" {
		t.Fatalf("已用端口不在开头时应取 1081，实际 %q", got)
	}
}
