package database

import (
	"context"
	"fmt"

	"github.com/DaWesen/lanmei-dream/internal/model"
)

// SaveGroupMemory 存储一条群级记忆元数据（memories 表）。
// 群级记忆 user_id 固定为 0，group_id 标识来源群（由话题归档链路写入）。
func (db *DB) SaveGroupMemory(ctx context.Context, mem *model.Memory) error {
	if err := db.Orm.WithContext(ctx).Create(mem).Error; err != nil {
		return fmt.Errorf("save_group_memory: %w", err)
	}
	return nil
}
