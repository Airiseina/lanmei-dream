package database

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/DaWesen/lanmei-dream/internal/model"
	"gorm.io/gorm"
)

// ErrRandomBeautyDuplicate 表示插入的图片已在池中（ImageKey 唯一冲突），
// 调用方据此做补偿清理（删除刚上传的对象存储内容）。
var ErrRandomBeautyDuplicate = errors.New("database: random beauty image already exists")

// CountRandomBeautyPool 返回图池当前图片总数。
func (db *DB) CountRandomBeautyPool(ctx context.Context) (int64, error) {
	if db.Orm == nil {
		return 0, errors.New("database: orm is nil")
	}
	var count int64
	if err := db.Orm.WithContext(ctx).Model(&model.RandomBeautyPool{}).Count(&count).Error; err != nil {
		return 0, fmt.Errorf("database: count random beauty pool: %w", err)
	}
	return count, nil
}

// GetRandomBeautyImage 随机取一张池内图片；池为空时返回 gorm.ErrRecordNotFound。
func (db *DB) GetRandomBeautyImage(ctx context.Context) (*model.RandomBeautyPool, error) {
	if db.Orm == nil {
		return nil, errors.New("database: orm is nil")
	}
	var rec model.RandomBeautyPool
	// ORDER BY random() LIMIT 1：PostgreSQL 与 SQLite 均支持 random()。
	if err := db.Orm.WithContext(ctx).Order("random()").First(&rec).Error; err != nil {
		return nil, err
	}
	return &rec, nil
}

// HasRandomBeautyImage 判断候选图片是否已入库（按 ImageKey 查重）。
func (db *DB) HasRandomBeautyImage(ctx context.Context, imageKey string) (bool, error) {
	if db.Orm == nil {
		return false, errors.New("database: orm is nil")
	}
	var count int64
	if err := db.Orm.WithContext(ctx).Model(&model.RandomBeautyPool{}).
		Where("image_key = ?", imageKey).
		Count(&count).Error; err != nil {
		return false, fmt.Errorf("database: has random beauty image %q: %w", imageKey, err)
	}
	return count > 0, nil
}

// InsertRandomBeautyImage 插入一条池记录；ImageKey 唯一冲突时返回 ErrRandomBeautyDuplicate。
func (db *DB) InsertRandomBeautyImage(ctx context.Context, rec *model.RandomBeautyPool) error {
	if db.Orm == nil {
		return errors.New("database: orm is nil")
	}
	if err := db.Orm.WithContext(ctx).Create(rec).Error; err != nil {
		if isDuplicateKeyError(err) {
			return ErrRandomBeautyDuplicate
		}
		return fmt.Errorf("database: insert random beauty image: %w", err)
	}
	return nil
}

// DeleteRandomBeautyImage 按 ImageKey 删除池记录（当前补图流程未调用）。
func (db *DB) DeleteRandomBeautyImage(ctx context.Context, imageKey string) error {
	if db.Orm == nil {
		return errors.New("database: orm is nil")
	}
	if err := db.Orm.WithContext(ctx).
		Where("image_key = ?", imageKey).
		Delete(&model.RandomBeautyPool{}).Error; err != nil {
		return fmt.Errorf("database: delete random beauty image %q: %w", imageKey, err)
	}
	return nil
}

// isDuplicateKeyError 判断底层错误是否为唯一约束冲突。
// GORM 未开启 TranslateError 时不会翻译为 ErrDuplicatedKey，
// 因此补充驱动错误文本兜底（PostgreSQL 为 "duplicate key"，SQLite 为 "UNIQUE constraint failed"）。
func isDuplicateKeyError(err error) bool {
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "duplicate key") || strings.Contains(msg, "UNIQUE constraint failed")
}
