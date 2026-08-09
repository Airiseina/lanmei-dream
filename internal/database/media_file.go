package database

import (
	"context"
	"errors"
	"fmt"

	"github.com/DaWesen/lanmei-dream/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// isRecordNotFound 判断是否为 GORM 记录不存在错误。
func isRecordNotFound(err error) bool {
	return errors.Is(err, gorm.ErrRecordNotFound)
}

// UpsertMediaFile 记录一个已缓存到 RustFS 的媒体文件。
// 以 Hash 为唯一键（内容寻址），已存在时静默跳过（幂等），不重复记录。
func (db *DB) UpsertMediaFile(ctx context.Context, mf *model.MediaFile) error {
	if err := db.Orm.WithContext(ctx).Clauses(
		clause.OnConflict{Columns: []clause.Column{{Name: "hash"}}, DoNothing: true},
	).Create(mf).Error; err != nil {
		return fmt.Errorf("upsert_media_file: %w", err)
	}
	return nil
}

// GetMediaFileByHash 按内容 hash 查询媒体文件记录（缓存命中判定）。
// 未命中时返回 (nil, nil)。
func (db *DB) GetMediaFileByHash(ctx context.Context, hash string) (*model.MediaFile, error) {
	var mf model.MediaFile
	err := db.Orm.WithContext(ctx).Where("hash = ?", hash).First(&mf).Error
	if err != nil {
		if isRecordNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("get_media_file_by_hash: %w", err)
	}
	return &mf, nil
}
