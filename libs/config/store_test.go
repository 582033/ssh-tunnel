package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadMultiConnections(t *testing.T) {
	s, err := Load(writeTemp(t, `
active: 家里
connections:
  - name: 公司
    username: u1
    password: p1
    serverAddr: office.example
    localPort: "1081"
  - name: 家里
    username: u2
    password: p2
    serverAddr: home.example
    localPort: "1082"
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Connections) != 2 {
		t.Fatalf("应读到 2 个连接，实际 %d", len(s.Connections))
	}
	if s.Current().Name != "家里" || s.Current().LocalPort != "1082" {
		t.Fatalf("active 未生效，当前是 %+v", s.Current())
	}
	if got := s.Names(); got[0] != "公司" || got[1] != "家里" {
		t.Fatalf("Names 顺序应与 connections 一致，实际 %v", got)
	}
}

// 旧版单连接配置（字段写在顶层，没有 connections）必须还能打开，
// 否则升级 App 等于把用户已有配置弄丢。
func TestLoadLegacySingleConnection(t *testing.T) {
	s, err := Load(writeTemp(t, `
username: "alice"
password: "secret"
serverAddr: "old.example"
serverPort: "2222"
localPort: "1080"
useChrome: true
autoConnect: true
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Connections) != 1 {
		t.Fatalf("旧格式应包成 1 条连接，实际 %d", len(s.Connections))
	}
	c := s.Current()
	if c.Username != "alice" || c.ServerAddr != "old.example" || c.ServerPort != "2222" {
		t.Fatalf("旧格式字段丢失: %+v", c)
	}
	if !c.UseChrome || !c.AutoConnect {
		t.Fatalf("旧格式布尔字段丢失: %+v", c)
	}
	if c.Name != defaultProfileName {
		t.Fatalf("旧格式应补默认名字，实际 %q", c.Name)
	}
	if s.Active != defaultProfileName {
		t.Fatalf("active 应指向那唯一一条，实际 %q", s.Active)
	}
}

// 旧格式读进来、保存一次，就该变成新格式；再读回来内容不变。
func TestLegacyUpgradesOnSave(t *testing.T) {
	path := writeTemp(t, `
username: "alice"
password: "secret"
serverAddr: "old.example"
`)
	s, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "connections:") {
		t.Fatalf("保存后应是新格式，实际:\n%s", raw)
	}

	again, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if again.Current().Username != "alice" || again.Current().ServerAddr != "old.example" {
		t.Fatalf("升级后内容变了: %+v", again.Current())
	}
}

func TestLoadRejectsBadExtension(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.txt")
	if err := os.WriteFile(path, []byte("username: u"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("非 yaml 后缀应报错")
	}
}

// 原实现用 configFile[len-5:] 切片判断后缀，文件名短于 5 字符会 panic
func TestLoadShortPathNoPanic(t *testing.T) {
	if _, err := Load("a"); err == nil {
		t.Fatal("应返回错误而非 panic")
	}
}

// active 指向一个不存在的名字时不能让 Current 返回 nil 或 panic
func TestActiveFallsBackWhenMissing(t *testing.T) {
	s, err := Load(writeTemp(t, `
active: 不存在的连接
connections:
  - name: 唯一
    username: u
    password: p
    serverAddr: h.example
`))
	if err != nil {
		t.Fatal(err)
	}
	if s.Active != "唯一" || s.Current().Name != "唯一" {
		t.Fatalf("active 无效时应退回第一条，实际 %q", s.Active)
	}
}

// 同名连接会让下拉无法区分，也让 active 指向含糊，读入时必须去重
func TestDuplicateNamesDisambiguated(t *testing.T) {
	s, err := Load(writeTemp(t, `
connections:
  - name: 公司
    username: u
    password: p
    serverAddr: a.example
  - name: 公司
    username: u
    password: p
    serverAddr: b.example
  - name: 公司
    username: u
    password: p
    serverAddr: c.example
`))
	if err != nil {
		t.Fatal(err)
	}
	names := s.Names()
	seen := map[string]bool{}
	for _, n := range names {
		if seen[n] {
			t.Fatalf("名字未去重: %v", names)
		}
		seen[n] = true
	}
}

// 没写 name 的连接要自动取一个可读的名字，不能在下拉里显示成空白
func TestUnnamedConnectionGetsLabel(t *testing.T) {
	s, err := Load(writeTemp(t, `
connections:
  - username: alice
    password: p
    serverAddr: h.example
`))
	if err != nil {
		t.Fatal(err)
	}
	if s.Current().Name != "alice@h.example" {
		t.Fatalf("未命名连接应取 用户名@地址，实际 %q", s.Current().Name)
	}
}
