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

// Manager 管理 Prompt 组件的加载、缓存和组装。
//
// 工作流程：
//  1. Load() 读取 prompts.toml → 加载所有 Fragment 的 Markdown 文件 → 加载组装模板
//  2. Assemble() 使用 text/template + FuncMap 将组装模板渲染为最终 System Prompt
//
// 组装模板中支持：
//   - {{ fragment "id" }}    — 按 ID 注入指定 Fragment 的内容
//   - {{ .Skills }}          — 注入所有已启用技能的拼接内容
//   - {{ .Conversation }}    — 注入对话历史
//   - {{ .Vars.xxx }}        — 注入来自 prompts.toml 的变量
//   - {{ .CurrentTime }}     — 当前时间
//   - {{ .UserName }}        — 用户昵称
//   - {{ .GroupName }}       — 群组名称
type Manager struct {
	rootDir   string                 // prompts/ 目录路径
	config    *PromptsConfig         // 从 prompts.toml 解析的配置
	fragments map[string]*Fragment   // ID → Fragment
	assembly  string                 // 组装模板原文
	skills    *skill.Manager         // Skill 管理器（可选）
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
	content string // 已加载的 Markdown 内容
}

// NewManager 创建 Prompt 管理器。
// rootDir: prompts/ 目录的绝对或相对路径
// configPath: prompts.toml 的路径
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
	// 1. 读取 prompts.toml
	raw, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("prompt: 读取 %s 失败: %w", configPath, err)
	}

	var cfg PromptsConfig
	if err := toml.Unmarshal(raw, &cfg); err != nil {
		return fmt.Errorf("prompt: 解析 %s 失败: %w", configPath, err)
	}
	m.config = &cfg

	// 2. 加载所有 Fragment 的 Markdown 内容
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

// Assemble 按组装模板拼接完整 System Prompt。
//
// 实现步骤：
//  1. 从 skill.Manager 获取已启用技能的拼接内容，填充到 ctx.Skills
//  2. 解析组装模板为 text/template，注册 fragment() 函数
//  3. 用 AssemblyContext 渲染模板，输出最终 System Prompt
func (m *Manager) Assemble(ctx AssemblyContext) (string, error) {
	if m.config == nil || m.assembly == "" {
		return "", fmt.Errorf("prompt: 未加载配置，请先调用 Load()")
	}

	// 填充 Skills 字段
	if m.skills != nil {
		ctx.Skills = m.skills.GetEnabledContent()
	}

	// 解析模板
	tmpl, err := template.New("assembly").Funcs(m.funcMap()).Parse(m.assembly)
	if err != nil {
		return "", fmt.Errorf("prompt: 解析组装模板失败: %w", err)
	}

	// 渲染
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, ctx); err != nil {
		return "", fmt.Errorf("prompt: 渲染组装模板失败: %w", err)
	}

	return buf.String(), nil
}

// StaticPrefix 返回前缀缓存友好的静态前缀。
//
// 策略：查找组装模板中 {{ fragment "builtin_xxx" }} 引用（非动态部分），
// 提取这些 Fragment 的内容作为静态前缀。
// 具体实现：简单地将所有 builtin Fragment 的内容按声明顺序拼接。
func (m *Manager) StaticPrefix() string {
	if m.config == nil {
		return ""
	}

	// 按 fragment 声明顺序获取 builtin 片段内容
	// （prompts.toml 中 fragment 的 map 顺序不确定，此处先收集 builtin 片段）
	// 更精确的做法：解析组装模板中的 {{ fragment "id" }} 顺序（见下方优化）
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
	return parts[0] // 只需返回第一个 builtin 片段即可命中缓存前缀
	// 注：返回完整拼接可能会超过前缀缓存长度，第一个片段通常已足够
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
	// 清空后重新加载
	m.fragments = make(map[string]*Fragment)
	m.assembly = ""
	m.config = nil
	return m.Load(configPath)
}