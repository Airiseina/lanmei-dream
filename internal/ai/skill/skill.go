// Package skill 提供技能（Skill）管理能力。
//
// 技能是一个自包含目录 skills/<id>/，含元数据 manifest.toml（id/name/description/
// version/author/tags）与注入 System Prompt 的 SKILL.md，assets/ 为可选资源。
// 启用状态由 config/skills.toml 控制，支持热重载与运行时动态注册。
package skill

import (
	"os"
	"path/filepath"

	"github.com/pelletier/go-toml/v2"
)

// Skill 表示一个已加载的技能。
// Content 为 SKILL.md 正文，启用时由 prompt.Manager 注入 System Prompt；
// Assets 记录 assets/ 下文件，供技能声明引用。
type Skill struct {
	ID          string   `toml:"id"`
	Name        string   `toml:"name"`
	Description string   `toml:"description"`
	Version     string   `toml:"version"`
	Author      string   `toml:"author"`
	Tags        []string `toml:"tags"`
	Dir         string
	Content     string
	Assets      map[string]string // assets/ 文件：相对路径 → 绝对路径
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
	manifestPath := filepath.Join(dir, "manifest.toml")
	rawManifest, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, err
	}

	var m Manifest
	if err := toml.Unmarshal(rawManifest, &m); err != nil {
		return nil, err
	}

	skillPath := filepath.Join(dir, "SKILL.md")
	content, err := os.ReadFile(skillPath)
	if err != nil {
		return nil, err
	}

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
