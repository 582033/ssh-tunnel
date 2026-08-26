package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v2"
)

const defaultProfileName = "默认连接"

// Store 是一个配置文件里的全部连接。界面顶部的下拉从这里取列表，
// Active 指向当前选中的那一份。
//
// 文件格式：
//
//	active: 公司
//	connections:
//	  - name: 公司
//	    serverAddr: ...
//	  - name: 家里
//	    ...
//
// 旧版单连接格式（字段直接写在顶层）仍能读，读进来会被包成一条连接，
// 下次保存时自动升级为新格式。
type Store struct {
	Active      string    `yaml:"active"`
	Connections []*Config `yaml:"connections"`

	// Path 记录配置实际来源，供「保存」写回同一文件
	Path string `yaml:"-"`
}

// DefaultPaths 返回按优先级排列的配置搜索路径。
// .app 内的进程工作目录是 /，不能依赖相对路径，因此把用户目录和
// bundle 内的 Resources 目录都纳入搜索范围。
func DefaultPaths() []string {
	var paths []string

	if home, err := os.UserHomeDir(); err == nil {
		paths = append(paths,
			filepath.Join(home, ".config", "ssh-tunnel", "config.yaml"),
			filepath.Join(home, ".ssh-tunnel.yaml"),
		)
	}

	// 可执行文件同级 / bundle 内的 Resources
	if exe, err := os.Executable(); err == nil {
		if exe, err = filepath.EvalSymlinks(exe); err == nil {
			dir := filepath.Dir(exe)
			paths = append(paths,
				filepath.Join(dir, "config.yaml"),
				filepath.Join(dir, "config", "config.yaml"),
				// Contents/MacOS/bin -> Contents/Resources/config.yaml
				filepath.Join(dir, "..", "Resources", "config.yaml"),
			)
		}
	}

	return append(paths, "./config/config.yaml")
}

// Load 读取配置并校验当前选中的连接。
// path 为空时按 DefaultPaths 顺序查找第一个存在的文件。
func Load(path string) (*Store, error) {
	s, err := read(path)
	if err != nil {
		return nil, err
	}
	if err := s.Current().Validate(); err != nil {
		return nil, err
	}
	return s, nil
}

// LoadForUI 供图形界面使用：始终返回一个可编辑的 Store，
// 即使文件不存在或字段不合法（此时 err 说明问题，界面据此提示用户补全）。
func LoadForUI(path string) (*Store, error) {
	s, err := read(path)
	if err != nil {
		def := NewStore()
		if path != "" {
			def.Path, _ = filepath.Abs(path)
		} else {
			def.Path = DefaultPaths()[0]
		}
		return def, err
	}
	return s, s.Current().Validate()
}

// NewStore 返回只含一条空白连接的 Store，供首次使用。
func NewStore() *Store {
	c := Default()
	c.Name = defaultProfileName
	return &Store{
		Active:      c.Name,
		Connections: []*Config{c},
		Path:        DefaultPaths()[0],
	}
}

func read(path string) (*Store, error) {
	if path == "" {
		for _, p := range DefaultPaths() {
			if _, err := os.Stat(p); err == nil {
				path = p
				break
			}
		}
		if path == "" {
			return nil, fmt.Errorf("未找到配置文件，请在界面中填写后保存")
		}
	}

	if !strings.HasSuffix(path, ".yaml") && !strings.HasSuffix(path, ".yml") {
		return nil, fmt.Errorf("配置文件后缀必须为 .yaml 或 .yml")
	}

	yamlFile, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	s := &Store{}
	if err := yaml.Unmarshal(yamlFile, s); err != nil {
		return nil, fmt.Errorf("解析配置文件失败: %w", err)
	}

	// 旧格式：字段直接写在顶层，没有 connections 列表。
	// 单独解一次包成一条连接，用户的老配置不至于打开就空白。
	if len(s.Connections) == 0 {
		legacy := &Config{}
		if err := yaml.Unmarshal(yamlFile, legacy); err != nil {
			return nil, fmt.Errorf("解析配置文件失败: %w", err)
		}
		if legacy.Name == "" {
			legacy.Name = defaultProfileName
		}
		s.Connections = []*Config{legacy}
		s.Active = legacy.Name
	}

	s.normalize()
	s.Path, _ = filepath.Abs(path)
	return s, nil
}

// normalize 补齐名字、去重、确保 Active 指向真实存在的一条。
func (s *Store) normalize() {
	if len(s.Connections) == 0 {
		s.Connections = []*Config{Default()}
	}

	used := make(map[string]bool, len(s.Connections))
	for i, c := range s.Connections {
		if c == nil {
			s.Connections[i] = Default()
			c = s.Connections[i]
		}
		name := strings.TrimSpace(c.Name)
		if name == "" {
			name = c.Label()
		}
		// 同名会让下拉无法区分，也让 Active 指向变得含糊，这里加后缀区分
		if used[name] {
			name = uniqueName(used, name)
		}
		c.Name = name
		used[name] = true
	}

	if s.Index(s.Active) < 0 {
		s.Active = s.Connections[0].Name
	}
}

func uniqueName(used map[string]bool, base string) string {
	for i := 2; ; i++ {
		name := fmt.Sprintf("%s %d", base, i)
		if !used[name] {
			return name
		}
	}
}

// Index 返回名字对应的下标，不存在返回 -1。
func (s *Store) Index(name string) int {
	for i, c := range s.Connections {
		if c.Name == name {
			return i
		}
	}
	return -1
}

// Current 返回当前选中的连接。始终非 nil。
func (s *Store) Current() *Config {
	if i := s.Index(s.Active); i >= 0 {
		return s.Connections[i]
	}
	s.normalize()
	return s.Connections[s.Index(s.Active)]
}

// Names 返回下拉列表要显示的名字，顺序与 Connections 一致。
func (s *Store) Names() []string {
	names := make([]string, len(s.Connections))
	for i, c := range s.Connections {
		names[i] = c.Name
	}
	return names
}

// SetActive 切换当前连接。名字不存在时返回错误而不是静默忽略。
func (s *Store) SetActive(name string) error {
	if s.Index(name) < 0 {
		return fmt.Errorf("没有名为 %q 的连接", name)
	}
	s.Active = name
	return nil
}

// Replace 用 c 覆盖 name 对应的那一条，并把选中项指向它。
// c.Name 可以与 name 不同（即重命名），重名时返回错误。
func (s *Store) Replace(name string, c *Config) error {
	i := s.Index(name)
	if i < 0 {
		return fmt.Errorf("没有名为 %q 的连接", name)
	}
	c.Name = strings.TrimSpace(c.Name)
	if c.Name == "" {
		c.Name = c.Label()
	}
	if j := s.Index(c.Name); j >= 0 && j != i {
		return fmt.Errorf("已存在名为 %q 的连接，请换一个名字", c.Name)
	}
	s.Connections[i] = c
	s.Active = c.Name
	return nil
}

// Add 追加一条连接并选中它。名字为空时自动取「新连接」，重名自动加后缀。
func (s *Store) Add(c *Config) {
	used := make(map[string]bool, len(s.Connections))
	for _, e := range s.Connections {
		used[e.Name] = true
	}

	name := strings.TrimSpace(c.Name)
	if name == "" {
		name = "新连接"
	}
	if used[name] {
		name = uniqueName(used, name)
	}
	c.Name = name

	s.Connections = append(s.Connections, c)
	s.Active = name
}

// Remove 删除一条连接。最后一条不允许删除——否则界面上无处可填。
func (s *Store) Remove(name string) error {
	if len(s.Connections) <= 1 {
		return fmt.Errorf("至少要保留一个连接")
	}
	i := s.Index(name)
	if i < 0 {
		return fmt.Errorf("没有名为 %q 的连接", name)
	}

	s.Connections = append(s.Connections[:i], s.Connections[i+1:]...)
	if s.Active == name {
		// 选中被删的那条时，落到它原来的位置（删的是最后一条则落到新的末尾）
		if i >= len(s.Connections) {
			i = len(s.Connections) - 1
		}
		s.Active = s.Connections[i].Name
	}
	return nil
}

// Save 把全部连接写回 Path（0600，因为含密码/私钥路径）。
func (s *Store) Save() error {
	if s.Path == "" {
		s.Path = DefaultPaths()[0]
	}
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o700); err != nil {
		return err
	}
	s.normalize()

	data, err := yaml.Marshal(s)
	if err != nil {
		return err
	}
	header := []byte("# ssh-tunnel 配置（由 App 生成，可手动编辑）\n" +
		"# active 指向当前选中的连接，connections 下可以放多份。\n")
	return os.WriteFile(s.Path, append(header, data...), 0o600)
}
