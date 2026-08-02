// Package skill 提供技能（Skill）管理能力。
//
// 技能是一个自包含的目录，包含元数据和注入 Prompt 的 Markdown 内容。
// 每个技能目录位于 skills/<id>/ 下，包含：
//   - manifest.toml：元数据（id, name, description, version, author, tags）
//   - SKILL.md：注入到 System Prompt 的 Markdown 内容
//   - assets/：可选资源文件（供将来扩展，如图片、参考文档等）
//
// 启用/关闭由 config/skills.toml 控制，支持热重载。
// 插件也可在运行时通过 Manager.Register()/Unregister() 动态注册技能。
package skill

import (
	"os"
	"path/filepath"

	"github.com/pelletier/go-toml/v2"
)

// Skill 表示一个已加载的技能。
type Skill struct {
	ID          string            `toml:"id"`
	Name        string            `toml:"name"`
	Description string            `toml:"description"`
	Version     string            `toml:"version"`
	Author      string            `toml:"author"`
	Tags        []string          `toml:"tags"`
	Dir         string            // 技能目录的绝对路径
	Content     string            // SKILL.md 的已加载内容
	Assets      map[string]string // assets/ 中的文件路径映射（相对路径 → 绝对路径）
}

// Manifest 对应 manifest.toml 的结构
type Manifest struct {
	ID          string   `toml:"id"`
	Name        string   `toml:"name"`
	Description string   `toml:"description"`
	Version     string   `toml:"version"`
	Author      string   `toml:"author"`
	Tags        []string `toml:"tags"`
}

// LoadSkill 从指定目录加载一个技能。
// dir 应为 skills/<id>/ 的绝对路径。
func LoadSkill(dir string) (*Skill, error) {
	// 读取 manifest.toml
	manifestPath := filepath.Join(dir, "manifest.toml")
	rawManifest, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, err
	}

	var m Manifest
	if err := toml.Unmarshal(rawManifest, &m); err != nil {
		return nil, err
	}

	// 读取 SKILL.md
	skillPath := filepath.Join(dir, "SKILL.md")
	content, err := os.ReadFile(skillPath)
	if err != nil {
		return nil, err
	}

	// 扫描 assets/ 目录
	assetsDir := filepath.Join(dir, "assets")
	assets := make(map[string]string)
	if entries, err := os.ReadDir(assetsDir); err == nil {
		for _, entry := range entries {
			if !entry.IsDir() {
				assets[entry.Name()] = filepath.Join(assetsDir, entry.Name())
			}
		}
	}

	return &Skill{
		ID:          m.ID,
		Name:        m.Name,
		Description: m.Description,
		Version:     m.Version,
		Author:      m.Author,
		Tags:        m.Tags,
		Dir:         dir,
		Content:     string(content),
		Assets:      assets,
	}, nil
}
