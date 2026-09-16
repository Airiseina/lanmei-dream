package database

import (
	"context"
	"fmt"

	"github.com/DaWesen/lanmei-dream/internal/model"
	"gorm.io/gorm"
)

// PluginInstallationStore 管理 PluginInstallation 的持久化。
type PluginInstallationStore struct {
	db *gorm.DB
}

// NewPluginInstallationStore 创建安装记录存储。
func NewPluginInstallationStore(db *gorm.DB) *PluginInstallationStore {
	return &PluginInstallationStore{db: db}
}

// Create 插入一条插件安装记录（plugin_installations 表）。
//
// 参数：
//   - inst：待写入的记录，ID 由调用方生成；plugin_id 受唯一索引约束。
//
// 返回：插入错误；plugin_id 与既有记录重复时返回数据库唯一键冲突错误，不做去重或 upsert。
func (s *PluginInstallationStore) Create(ctx context.Context, inst *model.PluginInstallation) error {
	return s.db.WithContext(ctx).Create(inst).Error
}

// Update 按主键整行保存安装记录（GORM Save，零值字段一并写入）。
//
// 参数：
//   - inst：待保存的记录；主键在表中不存在时 GORM Save 退化为插入。
//
// 返回：写入错误，原样返回。
func (s *PluginInstallationStore) Update(ctx context.Context, inst *model.PluginInstallation) error {
	return s.db.WithContext(ctx).Save(inst).Error
}

// UpdateEnabled 原子更新启用状态和错误信息。
func (s *PluginInstallationStore) UpdateEnabled(ctx context.Context, id string, enabled bool, loadErr string) error {
	return s.db.WithContext(ctx).
		Model(&model.PluginInstallation{}).
		Where("id = ?", id).
		Updates(map[string]interface{}{
			"enabled":    enabled,
			"load_error": loadErr,
		}).Error
}

// Delete 按主键硬删除安装记录（模型无软删除字段）。
//
// 参数：
//   - id：安装记录主键。
//
// 返回：删除错误；目标不存在时不报错（影响行数为 0，幂等）。
func (s *PluginInstallationStore) Delete(ctx context.Context, id string) error {
	return s.db.WithContext(ctx).Delete(&model.PluginInstallation{}, "id = ?", id).Error
}

// FindByID 按主键查询单条安装记录。
//
// 参数：
//   - id：安装记录主键。
//
// 返回：命中的记录；不存在时返回 gorm.ErrRecordNotFound（原样透传，由调用方判定）。
func (s *PluginInstallationStore) FindByID(ctx context.Context, id string) (*model.PluginInstallation, error) {
	var inst model.PluginInstallation
	if err := s.db.WithContext(ctx).First(&inst, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &inst, nil
}

// FindByPluginID 按 plugin_id 查询（首版最多一条）。
func (s *PluginInstallationStore) FindByPluginID(ctx context.Context, pluginID string) (*model.PluginInstallation, error) {
	var inst model.PluginInstallation
	if err := s.db.WithContext(ctx).First(&inst, "plugin_id = ?", pluginID).Error; err != nil {
		return nil, err
	}
	return &inst, nil
}

// ListEnabled 列出所有 enabled=true 的安装记录，供启动时恢复插件。
//
// 返回：命中记录切片；查询失败时原样返回错误。无分页与排序，顺序由数据库决定。
func (s *PluginInstallationStore) ListEnabled(ctx context.Context) ([]model.PluginInstallation, error) {
	var insts []model.PluginInstallation
	if err := s.db.WithContext(ctx).Where("enabled = ?", true).Find(&insts).Error; err != nil {
		return nil, err
	}
	return insts, nil
}

// ListAll 列出全部安装记录（含已禁用），供管理面板展示。
//
// 返回：记录切片；查询失败时以「查询安装记录」前缀包装返回。无分页与排序。
func (s *PluginInstallationStore) ListAll(ctx context.Context) ([]model.PluginInstallation, error) {
	var insts []model.PluginInstallation
	if err := s.db.WithContext(ctx).Find(&insts).Error; err != nil {
		return nil, fmt.Errorf("查询安装记录: %w", err)
	}
	return insts, nil
}
