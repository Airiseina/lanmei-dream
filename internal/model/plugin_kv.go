package model

import "time"

// PluginKV 插件受限 KV 存储表（plugin_kv）。
//
// 供插件持久化私有业务数据，语义类似前端 IndexedDB 的 key-value：
// 按 (plugin_id, key) 唯一确定一条记录，value 为 TEXT。
// 与 Redis StateStore（易失，重启丢失）不同，本表数据落在 PostgreSQL，持久化不丢。
type PluginKV struct {
	ID        uint      `json:"id"         gorm:"primaryKey;autoIncrement;comment:主键"`
	PluginID  string    `json:"plugin_id"  gorm:"uniqueIndex:idx_plugin_kv;not null;size:64;comment:插件ID（命名空间隔离）"`
	Key       string    `json:"key"        gorm:"uniqueIndex:idx_plugin_kv;not null;size:255;comment:键"`
	Value     string    `json:"value"      gorm:"type:text;not null;comment:值"`
	CreatedAt time.Time `json:"created_at" gorm:"autoCreateTime;comment:创建时间"`
	UpdatedAt time.Time `json:"updated_at" gorm:"autoUpdateTime;comment:更新时间"`
}

// TableName 指定表名。
func (PluginKV) TableName() string { return "plugin_kv" }
