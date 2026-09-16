package prompt

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"text/template"

	"github.com/DaWesen/lanmei-dream/internal/ai/skill"
	"github.com/pelletier/go-toml/v2"
)

// Manager 管理 Prompt 组件的加载与组装：Load() 读取 prompts.toml 并加载各
// Fragment 的 Markdown 与组装模板，Assemble() 用 text/template 渲染出 System Prompt。
//
// 组装模板可用：{{ fragment "id" }}、{{ .Skills }}、{{ .Conversation }}、
// {{ .Vars.xxx }}、{{ .CurrentTime }}、{{ .UserName }}、{{ .GroupName }}。
//
// 并发模型：内部无锁，Load/Reload/UpdateFragment 与 Assemble 不可并发执行；
// 管理面板热重载与消息组装需由调用方串行化。
type Manager struct {
	rootDir    string
	configPath string // prompts.toml 路径（Reload/编辑回写用）
	config     *PromptsConfig
	fragments  map[string]*Fragment // ID → Fragment
	assembly   string
	skills     *skill.Manager // 可选
}

// PromptsConfig 对应 prompts.toml 的结构
type PromptsConfig struct {
	Vars map[string]any `toml:"vars"`

	Fragment map[string]*FragmentConfig `toml:"fragment"`
	Assembly AssemblyConfig             `toml:"assembly"`
}

// FragmentConfig 对应 prompts.toml 中 [fragment.xxx] 的配置
type FragmentConfig struct {
	File    string `toml:"file"`
	Builtin bool   `toml:"builtin"`
}

// AssemblyConfig 对应 prompts.toml 中 [assembly] 的配置
type AssemblyConfig struct {
	TemplateFile string `toml:"template_file"`
}

// Fragment 表示一个已加载的 Prompt 片段
type Fragment struct {
	ID      string
	File    string
	Builtin bool
	content string
}

// NewManager 创建 Prompt 管理器；参数分别为 prompts/ 目录与 prompts.toml 的路径。
func NewManager(rootDir, configPath string) *Manager {
	return &Manager{
		rootDir:   rootDir,
		fragments: make(map[string]*Fragment),
	}
}

// SetSkills 关联 Skill 管理器，供组装时填充 {{ .Skills }}
func (m *Manager) SetSkills(sm *skill.Manager) {
	m.skills = sm
}

// Load 加载 prompts.toml → 所有 Fragment 的 Markdown 内容 → 组装模板
func (m *Manager) Load(configPath string) error {
	raw, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("prompt: 读取 %s 失败: %w", configPath, err)
	}
	m.configPath = configPath

	var cfg PromptsConfig
	if err := toml.Unmarshal(raw, &cfg); err != nil {
		return fmt.Errorf("prompt: 解析 %s 失败: %w", configPath, err)
	}
	m.config = &cfg

	for id, fc := range cfg.Fragment {
		fragPath := filepath.Join(m.rootDir, fc.File)
		content, err := os.ReadFile(fragPath)
		if err != nil {
			return fmt.Errorf("prompt: 读取 fragment %q (%s) 失败: %w", id, fragPath, err)
		}
		m.fragments[id] = &Fragment{
			ID:      id,
			File:    fc.File,
			Builtin: fc.Builtin,
			content: string(content),
		}
	}

	// 3. 加载组装模板
	tplPath := filepath.Join(m.rootDir, cfg.Assembly.TemplateFile)
	tplRaw, err := os.ReadFile(tplPath)
	if err != nil {
		return fmt.Errorf("prompt: 读取组装模板 %s 失败: %w", tplPath, err)
	}
	m.assembly = string(tplRaw)

	return nil
}

// Assemble 按组装模板拼接完整 System Prompt，并填充已启用技能内容。
func (m *Manager) Assemble(ctx AssemblyContext) (string, error) {
	if m.config == nil || m.assembly == "" {
		return "", fmt.Errorf("prompt: 未加载配置，请先调用 Load()")
	}

	if m.skills != nil {
		ctx.Skills = m.skills.GetEnabledContent()
	}

	tmpl, err := template.New("assembly").Funcs(m.funcMap()).Parse(m.assembly)
	if err != nil {
		return "", fmt.Errorf("prompt: 解析组装模板失败: %w", err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, ctx); err != nil {
		return "", fmt.Errorf("prompt: 渲染组装模板失败: %w", err)
	}

	return buf.String(), nil
}

// StaticPrefix 返回前缀缓存友好的静态前缀，即组装模板中按 {{ fragment "id" }} 出现顺序
// 找到的第一个 builtin 片段内容。只取第一个：完整拼接可能超过前缀缓存长度，
// 而首个片段已足以命中缓存。
func (m *Manager) StaticPrefix() string {
	if m.config == nil {
		return ""
	}

	re := regexp.MustCompile(`\{\{-?\s*fragment\s+"([^"]+)"\s*-?\}\}`)
	matches := re.FindAllStringSubmatch(m.assembly, -1)

	var parts []string
	for _, match := range matches {
		id := match[1]
		if frag, ok := m.fragments[id]; ok && frag.Builtin {
			parts = append(parts, frag.content)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return parts[0]
}

// Vars 返回从 prompts.toml 加载的注入变量
func (m *Manager) Vars() map[string]any {
	if m.config == nil {
		return nil
	}
	return m.config.Vars
}

// ListFragments 返回所有已加载的 Fragment 列表
func (m *Manager) ListFragments() []*Fragment {
	list := make([]*Fragment, 0, len(m.fragments))
	for _, f := range m.fragments {
		list = append(list, f)
	}
	return list
}

// GetFragment 按 ID 获取 Fragment
func (m *Manager) GetFragment(id string) (*Fragment, bool) {
	f, ok := m.fragments[id]
	return f, ok
}

// Reload 重新加载 prompts/ 目录下的所有文件
func (m *Manager) Reload(configPath string) error {
	m.fragments = make(map[string]*Fragment)
	m.assembly = ""
	m.config = nil
	return m.Load(configPath)
}

// FragmentContent 返回指定 Fragment 的已加载内容（供管理面板展示）。
func (m *Manager) FragmentContent(id string) (string, bool) {
	f, ok := m.fragments[id]
	if !ok {
		return "", false
	}
	return f.content, true
}

// UpdateFragment 更新指定 Fragment 内容：写回 Markdown 文件后热重载。
// builtin 片段为只读，拒绝修改（避免覆盖工程自带的核心 System Prompt）。
func (m *Manager) UpdateFragment(id, content string) error {
	f, ok := m.fragments[id]
	if !ok {
		return fmt.Errorf("prompt: fragment %q 不存在", id)
	}
	if f.Builtin {
		return fmt.Errorf("prompt: fragment %q 为 builtin 只读片段", id)
	}
	path := filepath.Join(m.rootDir, f.File)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return fmt.Errorf("prompt: 写入 %s 失败: %w", path, err)
	}
	if m.configPath == "" {
		return fmt.Errorf("prompt: 未加载配置，无法热重载")
	}
	return m.Reload(m.configPath)
}
