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

// Create 创建安装记录。
func (s *PluginInstallationStore) Create(ctx context.Context, inst *model.PluginInstallation) error {
	return s.db.WithContext(ctx).Create(inst).Error
}

// Update 更新安装记录。
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

// Delete 删除安装记录。
func (s *PluginInstallationStore) Delete(ctx context.Context, id string) error {
	return s.db.WithContext(ctx).Delete(&model.PluginInstallation{}, "id = ?", id).Error
}

// FindByID 按安装 ID 查询。
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

// ListEnabled 列出所有已启用的安装记录。
func (s *PluginInstallationStore) ListEnabled(ctx context.Context) ([]model.PluginInstallation, error) {
	var insts []model.PluginInstallation
	if err := s.db.WithContext(ctx).Where("enabled = ?", true).Find(&insts).Error; err != nil {
		return nil, err
	}
	return insts, nil
}

// ListAll 列出所有安装记录。
func (s *PluginInstallationStore) ListAll(ctx context.Context) ([]model.PluginInstallation, error) {
	var insts []model.PluginInstallation
	if err := s.db.WithContext(ctx).Find(&insts).Error; err != nil {
		return nil, fmt.Errorf("查询安装记录: %w", err)
	}
	return insts, nil
}
