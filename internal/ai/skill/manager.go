package skill

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// SkillsConfig 对应 config/skills.toml 的结构
type SkillsConfig struct {
	Skill map[string]*SkillEnableConfig `toml:"skill"`
}

// SkillEnableConfig 对应 config/skills.toml 中 [skill.xxx] 的配置
type SkillEnableConfig struct {
	Enabled bool `toml:"enabled"`
}

// Manager 管理技能的全生命周期：扫描 skills/ 目录发现技能、读取 config/skills.toml
// 决定启用状态，并支持运行时切换与插件动态注册/注销。
//
// 并发模型：内部无锁，所有方法均非并发安全；加载/切换（LoadAll/ReloadConfig/SetEnabled/
// Register/Unregister）与读取（GetEnabledContent 等）需由调用方自行串行化。
type Manager struct {
	skillsDir  string
	configPath string
	skills     map[string]*Skill // 全部已发现的技能
	enabled    map[string]bool
}

// NewManager 创建技能管理器；参数分别为 skills/ 目录与 config/skills.toml 的路径。
func NewManager(skillsDir, configPath string) *Manager {
	return &Manager{
		skillsDir:  skillsDir,
		configPath: configPath,
		skills:     make(map[string]*Skill),
		enabled:    make(map[string]bool),
	}
}

// LoadAll 扫描 skills/ 目录下所有子目录，加载每个技能。
// 每个技能目录需含 manifest.toml 与 SKILL.md，assets/ 可选。
func (m *Manager) LoadAll() error {
	entries, err := os.ReadDir(m.skillsDir)
	if err != nil {
		return fmt.Errorf("skill: 读取目录 %s 失败: %w", m.skillsDir, err)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		skillDir := filepath.Join(m.skillsDir, entry.Name())

		skill, err := LoadSkill(skillDir)
		if err != nil {
			// 单个技能加载失败不中断整体流程，仅跳过
			continue
		}
		m.skills[skill.ID] = skill
	}

	if err := m.ReloadConfig(); err != nil {
		return err
	}

	return nil
}

// ReloadConfig 重新加载 config/skills.toml。
func (m *Manager) ReloadConfig() error {
	raw, err := os.ReadFile(m.configPath)
	if err != nil {
		if os.IsNotExist(err) {
			// 配置缺失时默认启用全部技能（与 Register() 的"配置未声明则默认启用"
			// 语义一致）：曾因 Docker 只挂载 config.toml 而漏挂 skills.toml，
			// 此处静默禁用全部技能，默认启用可避免这种"技能全部失效"的坑。
			for id := range m.skills {
				m.enabled[id] = true
			}
			return nil
		}
		return fmt.Errorf("skill: 读取 %s 失败: %w", m.configPath, err)
	}

	var cfg SkillsConfig
	if err := toml.Unmarshal(raw, &cfg); err != nil {
		return fmt.Errorf("skill: 解析 %s 失败: %w", m.configPath, err)
	}

	for id := range m.skills {
		m.enabled[id] = false
	}
	for id, sc := range cfg.Skill {
		if sc.Enabled {
			m.enabled[id] = true
		}
	}

	return nil
}

// GetEnabledContent 返回所有已启用技能的 SKILL.md 内容拼接。
// 多个技能之间用 \n---\n 分隔。
func (m *Manager) GetEnabledContent() string {
	var parts []string
	for id, skill := range m.skills {
		if m.enabled[id] {
			parts = append(parts, skill.Content)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "\n---\n")
}

// IsEnabled 检查指定技能是否启用。
func (m *Manager) IsEnabled(id string) bool {
	return m.enabled[id]
}

// SetEnabled 运行时切换技能启用状态。
// 调用后会同步更新 config/skills.toml 文件。
func (m *Manager) SetEnabled(id string, enabled bool) error {
	if _, ok := m.skills[id]; !ok {
		return fmt.Errorf("skill: %q 不存在", id)
	}
	m.enabled[id] = enabled
	return m.saveConfig()
}

// List 返回所有已发现的技能列表。
func (m *Manager) List() []*Skill {
	list := make([]*Skill, 0, len(m.skills))
	for _, s := range m.skills {
		list = append(list, s)
	}
	return list
}

// Get 按 ID 获取技能。
func (m *Manager) Get(id string) (*Skill, bool) {
	s, ok := m.skills[id]
	return s, ok
}

// Register 供插件运行时注册一个技能。
// 注册的技能会立即生效，启用状态默认由 config/skills.toml 控制；
// 若配置文件中未声明此技能，则默认启用。
func (m *Manager) Register(skill *Skill) error {
	if skill.ID == "" {
		return fmt.Errorf("skill: 注册时 ID 不能为空")
	}
	if _, exists := m.skills[skill.ID]; exists {
		return fmt.Errorf("skill: %q 已存在", skill.ID)
	}
	m.skills[skill.ID] = skill

	if _, configured := m.enabled[skill.ID]; !configured {
		m.enabled[skill.ID] = true
	}
	return nil
}

// Unregister 供插件运行时注销一个技能。
func (m *Manager) Unregister(id string) error {
	if _, ok := m.skills[id]; !ok {
		return fmt.Errorf("skill: %q 不存在", id)
	}
	delete(m.skills, id)
	delete(m.enabled, id)
	return nil
}

// saveConfig 将当前的启用状态写回 config/skills.toml。
func (m *Manager) saveConfig() error {
	cfg := SkillsConfig{
		Skill: make(map[string]*SkillEnableConfig),
	}
	for id := range m.skills {
		cfg.Skill[id] = &SkillEnableConfig{
			Enabled: m.enabled[id],
		}
	}
	data, err := toml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("skill: 序列化配置失败: %w", err)
	}
	if err := os.WriteFile(m.configPath, data, 0644); err != nil {
		return fmt.Errorf("skill: 写入 %s 失败: %w", m.configPath, err)
	}
	return nil
}
