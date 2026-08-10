package database

import (
	"context"
	"errors"

	"github.com/DaWesen/lanmei-dream/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// PluginKVStore 受限 KV 存储：供插件持久化私有业务数据（PostgreSQL 后端）。
//
// 设计定位类似前端 IndexedDB：只暴露 Get/Set/Delete/List 基础键值操作，
// 按 pluginID 隔离命名空间，不将裸 *gorm.DB 交给插件自由操作，
// 避免插件绕过约束直接读写数据库。
//
// 与 conduit.StateStore（Redis，易失）的区别：本存储落 PostgreSQL，重启不丢失。
type PluginKVStore struct {
	orm *gorm.DB
}

// NewPluginKVStore 创建受限 KV 存储。
// orm 为 nil 时 Get/Set/Delete/List 返回错误，调用方应自行降级。
func NewPluginKVStore(orm *gorm.DB) *PluginKVStore {
	return &PluginKVStore{orm: orm}
}

// Get 读取键值；键不存在返回空字符串与 nil 错误。
// 使用 Scan 而非 First：0 行命中时 Scan 返回 nil（不触发 gorm 的
// RecordNotFound 错误日志），"键不存在"是插件数据首次读写的正常流程。
func (s *PluginKVStore) Get(ctx context.Context, pluginID, key string) (string, error) {
	if s.orm == nil {
		return "", errors.New("plugin kv: orm is nil")
	}
	var value string
	err := s.orm.WithContext(ctx).
		Model(&model.PluginKV{}).
		Where("plugin_id = ? AND key = ?", pluginID, key).
		Select("value").
		Scan(&value).Error
	if err != nil {
		return "", err
	}
	return value, nil
}

// Set 写入键值（已存在则更新 value 与 updated_at）。
func (s *PluginKVStore) Set(ctx context.Context, pluginID, key, value string) error {
	if s.orm == nil {
		return errors.New("plugin kv: orm is nil")
	}
	return s.orm.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "plugin_id"}, {Name: "key"}},
			DoUpdates: clause.AssignmentColumns([]string{"value", "updated_at"}),
		}).
		Create(&model.PluginKV{PluginID: pluginID, Key: key, Value: value}).Error
}

// Delete 删除键值。
func (s *PluginKVStore) Delete(ctx context.Context, pluginID, key string) error {
	if s.orm == nil {
		return errors.New("plugin kv: orm is nil")
	}
	return s.orm.WithContext(ctx).
		Where("plugin_id = ? AND key = ?", pluginID, key).
		Delete(&model.PluginKV{}).Error
}

// List 返回指定插件命名空间下的全部键值对。
func (s *PluginKVStore) List(ctx context.Context, pluginID string) (map[string]string, error) {
	if s.orm == nil {
		return nil, errors.New("plugin kv: orm is nil")
	}
	var kvs []model.PluginKV
	if err := s.orm.WithContext(ctx).
		Where("plugin_id = ?", pluginID).
		Find(&kvs).Error; err != nil {
		return nil, err
	}
	out := make(map[string]string, len(kvs))
	for _, kv := range kvs {
		out[kv.Key] = kv.Value
	}
	return out, nil
}
