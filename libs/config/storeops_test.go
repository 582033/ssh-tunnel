package config

import (
	"os"
	"path/filepath"
	"testing"
)

func twoConnStore(t *testing.T) *Store {
	t.Helper()
	return &Store{
		Active: "公司",
		Connections: []*Config{
			{Name: "公司", Username: "u1", Password: "p1", ServerAddr: "office.example", LocalPort: "1081"},
			{Name: "家里", Username: "u2", Password: "p2", ServerAddr: "home.example", LocalPort: "1082"},
		},
		Path: filepath.Join(t.TempDir(), "config.yaml"),
	}
}

func TestSetActive(t *testing.T) {
	s := twoConnStore(t)
	if err := s.SetActive("家里"); err != nil {
		t.Fatal(err)
	}
	if s.Current().LocalPort != "1082" {
		t.Fatalf("切换后 Current 未跟上: %+v", s.Current())
	}
	if err := s.SetActive("不存在"); err == nil {
		t.Fatal("切到不存在的名字应报错，而不是静默忽略")
	}
	if s.Active != "家里" {
		t.Fatalf("失败的切换不应改动 Active，实际 %q", s.Active)
	}
}

func TestAddSelectsNew(t *testing.T) {
	s := twoConnStore(t)
	c := Default()
	c.Name = "海外"
	s.Add(c)

	if len(s.Connections) != 3 {
		t.Fatalf("应有 3 条，实际 %d", len(s.Connections))
	}
	if s.Active != "海外" || s.Current().Name != "海外" {
		t.Fatalf("新增后应自动选中它，实际 %q", s.Active)
	}
}

func TestAddDeduplicatesName(t *testing.T) {
	s := twoConnStore(t)
	c := Default()
	c.Name = "公司"
	s.Add(c)

	if s.Active == "公司" {
		t.Fatal("重名的新连接不应顶掉原有的「公司」")
	}
	if len(s.Connections) != 3 {
		t.Fatalf("应有 3 条，实际 %d", len(s.Connections))
	}
	if s.Index("公司") < 0 {
		t.Fatal("原有的「公司」应仍然存在")
	}
}

func TestReplaceRename(t *testing.T) {
	s := twoConnStore(t)
	c := s.Current().Clone()
	c.Name = "公司-新"
	c.LocalPort = "1090"

	if err := s.Replace("公司", c); err != nil {
		t.Fatal(err)
	}
	if s.Index("公司") >= 0 {
		t.Fatal("旧名字应已不存在")
	}
	if s.Active != "公司-新" || s.Current().LocalPort != "1090" {
		t.Fatalf("重命名后应选中新名字，实际 %q / %+v", s.Active, s.Current())
	}
	if len(s.Connections) != 2 {
		t.Fatalf("重命名不该增删条目，实际 %d 条", len(s.Connections))
	}
}

// 改名撞上另一条的名字时必须报错，否则两条连接同名、下拉无法区分
func TestReplaceRejectsNameCollision(t *testing.T) {
	s := twoConnStore(t)
	c := s.Current().Clone()
	c.Name = "家里"

	if err := s.Replace("公司", c); err == nil {
		t.Fatal("改成已存在的名字应报错")
	}
	if s.Index("公司") < 0 {
		t.Fatal("失败的 Replace 不应破坏原有条目")
	}
}

// 改成自己原来的名字（只改其他字段）不算冲突
func TestReplaceSameNameAllowed(t *testing.T) {
	s := twoConnStore(t)
	c := s.Current().Clone()
	c.ServerAddr = "office2.example"

	if err := s.Replace("公司", c); err != nil {
		t.Fatal(err)
	}
	if s.Current().ServerAddr != "office2.example" {
		t.Fatalf("字段未更新: %+v", s.Current())
	}
}

func TestRemove(t *testing.T) {
	s := twoConnStore(t)
	if err := s.Remove("公司"); err != nil {
		t.Fatal(err)
	}
	if len(s.Connections) != 1 || s.Index("公司") >= 0 {
		t.Fatalf("未删除: %v", s.Names())
	}
	if s.Active != "家里" {
		t.Fatalf("删掉选中项后应落到剩下的那条，实际 %q", s.Active)
	}
}

// 删到一条不剩，界面上就没有任何地方可填配置了
func TestRemoveKeepsLastOne(t *testing.T) {
	s := twoConnStore(t)
	if err := s.Remove("公司"); err != nil {
		t.Fatal(err)
	}
	if err := s.Remove("家里"); err == nil {
		t.Fatal("最后一条不应允许删除")
	}
	if len(s.Connections) != 1 {
		t.Fatalf("应保留 1 条，实际 %d", len(s.Connections))
	}
}

// 删非选中项时，选中的那条不能被带偏
func TestRemoveOtherKeepsActive(t *testing.T) {
	s := twoConnStore(t)
	if err := s.Remove("家里"); err != nil {
		t.Fatal(err)
	}
	if s.Active != "公司" {
		t.Fatalf("Active 应保持不变，实际 %q", s.Active)
	}
}

// 删掉末尾的选中项时下标会越界，必须回落到新的最后一条
func TestRemoveLastActiveFallsBack(t *testing.T) {
	s := twoConnStore(t)
	if err := s.SetActive("家里"); err != nil {
		t.Fatal(err)
	}
	if err := s.Remove("家里"); err != nil {
		t.Fatal(err)
	}
	if s.Active != "公司" {
		t.Fatalf("应回落到「公司」，实际 %q", s.Active)
	}
}

// SaveRoundTrip 覆盖 GUI「保存」路径：多连接写盘后能原样读回
func TestSaveRoundTrip(t *testing.T) {
	s := twoConnStore(t)
	s.Connections[0].CustomDNS = "1.1.1.1:53"
	s.Connections[0].UseChrome = true
	s.Connections[0].AutoConnect = true
	s.Connections[0].ChromePath = "/tmp/chrome"
	if err := s.SetActive("家里"); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}

	// 含密码，权限必须是 0600
	info, err := os.Stat(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("配置权限应为 0600，实际 %o", perm)
	}

	got, err := Load(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Connections) != 2 {
		t.Fatalf("回读条数不对: %d", len(got.Connections))
	}
	if got.Active != "家里" || got.Current().LocalPort != "1082" {
		t.Fatalf("选中项未持久化: %q / %+v", got.Active, got.Current())
	}
	c := got.Connections[0]
	if c.Password != "p1" || c.CustomDNS != "1.1.1.1:53" || !c.UseChrome || !c.AutoConnect {
		t.Fatalf("字段回读不一致: %+v", c)
	}
}

func TestSaveCreatesParentDir(t *testing.T) {
	s := twoConnStore(t)
	s.Path = filepath.Join(t.TempDir(), "nested", "deep", "config.yaml")
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.Path); err != nil {
		t.Fatalf("父目录未创建: %v", err)
	}
}

// LoadForUI 在配置缺失时也要返回可编辑的默认值，否则界面没东西可填
func TestLoadForUIReturnsDefaultsOnMissing(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "absent.yaml")
	s, err := LoadForUI(missing)
	if err == nil {
		t.Fatal("文件不存在时应返回 err 供界面提示")
	}
	if s == nil || len(s.Connections) == 0 {
		t.Fatal("即使出错也必须返回可编辑的 Store")
	}
	c := s.Current()
	if c.LocalPort != "1081" || c.ServerPort != "22" {
		t.Fatalf("未填充默认值: %+v", c)
	}
	if s.Path != missing {
		t.Fatalf("Path 应指向请求的路径，实际 %q", s.Path)
	}
}

func TestLoadForUIInvalidFieldsStillEditable(t *testing.T) {
	// 缺 serverAddr：应返回 err 但保留已填字段供界面修正
	path := writeTemp(t, "connections:\n  - name: x\n    username: u\n    password: p\n")
	s, err := LoadForUI(path)
	if err == nil {
		t.Fatal("serverAddr 缺失应返回错误")
	}
	if s.Current().Username != "u" {
		t.Fatalf("已填字段应保留，实际 %+v", s.Current())
	}
}

func TestNewStoreHasOneEditableConnection(t *testing.T) {
	s := NewStore()
	if len(s.Connections) != 1 {
		t.Fatalf("应有 1 条，实际 %d", len(s.Connections))
	}
	if s.Active != s.Connections[0].Name || s.Current().Name == "" {
		t.Fatalf("Active 未指向那一条: %q", s.Active)
	}
	if s.Current().LocalPort != "1081" {
		t.Fatalf("未带默认值: %+v", s.Current())
	}
}
