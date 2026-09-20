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

// defaults 里的字段应回填进没自己填的连接
func TestDefaultsAppliedWhenFieldEmpty(t *testing.T) {
	s, err := Load(writeTemp(t, `
active: 公司
defaults:
  chromePath: "/opt/chrome"
  customDNS: "1.1.1.1:53"
connections:
  - name: 公司
    username: u1
    password: p1
    serverAddr: office.example
  - name: 家里
    username: u2
    password: p2
    serverAddr: home.example
`))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range s.Connections {
		if c.ChromePath != "/opt/chrome" {
			t.Fatalf("%s 未继承 defaults.chromePath: %q", c.Name, c.ChromePath)
		}
		if c.CustomDNS != "1.1.1.1:53" {
			t.Fatalf("%s 未继承 defaults.customDNS: %q", c.Name, c.CustomDNS)
		}
	}
}

// 连接自己填了字段时，defaults 不能覆盖它
func TestConnectionOverridesDefaults(t *testing.T) {
	s, err := Load(writeTemp(t, `
active: 公司
defaults:
  chromePath: "/opt/chrome"
  customDNS: "1.1.1.1:53"
connections:
  - name: 公司
    username: u1
    password: p1
    serverAddr: office.example
    chromePath: "/custom/chrome"
    customDNS: "8.8.8.8:53"
  - name: 家里
    username: u2
    password: p2
    serverAddr: home.example
`))
	if err != nil {
		t.Fatal(err)
	}
	gongsi := s.Connections[0]
	if gongsi.ChromePath != "/custom/chrome" || gongsi.CustomDNS != "8.8.8.8:53" {
		t.Fatalf("显式字段被 defaults 覆盖了: %+v", gongsi)
	}
	jiali := s.Connections[1]
	if jiali.ChromePath != "/opt/chrome" || jiali.CustomDNS != "1.1.1.1:53" {
		t.Fatalf("未填字段应继承 defaults: %+v", jiali)
	}
}

// 没有 defaults 字段的旧配置要能照常读取，不受影响
func TestNoDefaultsFieldStillWorks(t *testing.T) {
	s, err := Load(writeTemp(t, `
connections:
  - name: 公司
    username: u1
    password: p1
    serverAddr: office.example
`))
	if err != nil {
		t.Fatal(err)
	}
	if s.Defaults != nil {
		t.Fatalf("未写 defaults 时应为 nil，实际 %+v", s.Defaults)
	}
	if s.Current().ChromePath != "" {
		t.Fatalf("不应凭空生成 chromePath: %q", s.Current().ChromePath)
	}
}

// defaults 落盘时不应把合并结果写死进每条连接，否则改一次 defaults
// 就不再对已有连接生效了；保存后重新读取应仍然继承 defaults。
func TestDefaultsNotFlattenedOnSave(t *testing.T) {
	s, err := Load(writeTemp(t, `
active: 公司
defaults:
  chromePath: "/opt/chrome"
connections:
  - name: 公司
    username: u1
    password: p1
    serverAddr: office.example
`))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "chromePath: /opt/chrome\n    name:") ||
		strings.Count(string(raw), "/opt/chrome") != 1 {
		t.Fatalf("chromePath 被写死进了 connections，defaults 失去意义:\n%s", raw)
	}

	// 内存里的 Store 保存后应仍可用（不必重新读文件就能拿到合并值）
	if s.Current().ChromePath != "/opt/chrome" {
		t.Fatalf("保存后内存中的合并值丢失: %q", s.Current().ChromePath)
	}

	again, err := Load(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	if again.Current().ChromePath != "/opt/chrome" {
		t.Fatalf("重新读取后应仍继承 defaults，实际 %q", again.Current().ChromePath)
	}

	// 换一个 defaults 值，旧连接应跟着变——证明确实没被拍死成字面值
	raw2 := strings.Replace(string(raw), "/opt/chrome", "/opt/chrome2", 1)
	if err := os.WriteFile(s.Path, []byte(raw2), 0o600); err != nil {
		t.Fatal(err)
	}
	changed, err := Load(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	if changed.Current().ChromePath != "/opt/chrome2" {
		t.Fatalf("改动 defaults 应联动旧连接，实际 %q", changed.Current().ChromePath)
	}
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
// DefaultPaths 第一项必须是 Application Support 路径——这是新的
// 推荐默认位置，旧的 ~/.config/ssh-tunnel 降级为回退项。
func TestDefaultPathsPrefersAppSupport(t *testing.T) {
	paths := DefaultPaths()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("无法获取 HOME")
	}
	want := filepath.Join(home, "Library", "Application Support", "ssh-tunnel", "config.yaml")
	if len(paths) == 0 || paths[0] != want {
		t.Fatalf("第一个默认路径应为 %q，实际 %v", want, paths)
	}

	legacy := filepath.Join(home, ".config", "ssh-tunnel", "config.yaml")
	found := false
	for _, p := range paths {
		if p == legacy {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("旧路径 %q 应仍保留在回退列表中: %v", legacy, paths)
	}
}

// 旧路径有文件、新路径没有时，应该自动搬迁一份过去，且不删除旧文件。
func TestMigrateLegacyConfigCopiesWithoutDeleting(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	oldPath := filepath.Join(home, ".config", "ssh-tunnel", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(oldPath), 0o700); err != nil {
		t.Fatal(err)
	}
	content := []byte("connections:\n  - name: 公司\n    username: u\n    password: p\n    serverAddr: h.example\n")
	if err := os.WriteFile(oldPath, content, 0o600); err != nil {
		t.Fatal(err)
	}

	migrated, err := migrateLegacyConfig()
	if err != nil {
		t.Fatal(err)
	}
	if !migrated {
		t.Fatal("应发生搬迁")
	}

	newPath := filepath.Join(home, "Library", "Application Support", "ssh-tunnel", "config.yaml")
	got, err := os.ReadFile(newPath)
	if err != nil {
		t.Fatalf("新路径应有文件: %v", err)
	}
	if string(got) != string(content) {
		t.Fatalf("搬迁内容不一致: %q", got)
	}

	if _, err := os.Stat(oldPath); err != nil {
		t.Fatal("旧文件不应被删除")
	}
}

// 新路径已经有文件时不能覆盖，避免用户在新路径上的修改被旧文件冲掉。
func TestMigrateLegacyConfigSkipsWhenNewPathExists(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	oldPath := filepath.Join(home, ".config", "ssh-tunnel", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(oldPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(oldPath, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}

	newPath := filepath.Join(home, "Library", "Application Support", "ssh-tunnel", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(newPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newPath, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}

	migrated, err := migrateLegacyConfig()
	if err != nil {
		t.Fatal(err)
	}
	if migrated {
		t.Fatal("新路径已存在时不应搬迁")
	}
	got, _ := os.ReadFile(newPath)
	if string(got) != "new" {
		t.Fatalf("新路径内容被覆盖了: %q", got)
	}
}

// 旧路径没有文件时，搬迁应是无操作，不能报错。
func TestMigrateLegacyConfigNoopWhenOldMissing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	migrated, err := migrateLegacyConfig()
	if err != nil {
		t.Fatal(err)
	}
	if migrated {
		t.Fatal("旧文件不存在时不应报告搬迁")
	}
}

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
