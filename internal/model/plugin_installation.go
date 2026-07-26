package model

import (
	"time"

	"gorm.io/datatypes"
)

// PluginInstallation 记录一个 Wasm 插件安装实例。
// 一个 plugin_id 首版最多有一个 Enabled=true 的安装记录。
type PluginInstallation struct {
	ID          string         `gorm:"primaryKey;size:64"`
	PluginID    string         `gorm:"uniqueIndex;size:64;not null"`
	ABI         string         `gorm:"size:64;not null"`
	Name        string         `gorm:"size:255;not null"`
	Description string         `gorm:"type:text"`
	Version     string         `gorm:"size:64;not null"`
	WasmPath    string         `gorm:"type:text;not null"`
	WasmSHA256  string         `gorm:"size:64;not null"`
	Config      datatypes.JSON `gorm:"type:jsonb;not null"`
	Metadata    datatypes.JSON `gorm:"type:jsonb;not null"`
	Enabled     bool           `gorm:"not null;default:false"`
	LoadError   string         `gorm:"type:text"` // 上次加载失败原因
	CreatedAt   time.Time
	UpdatedAt   time.Time
}
