package database

import (
	"context"
	"errors"
	"fmt"

	"github.com/DaWesen/lanmei-dream/internal/model"
	"gorm.io/gorm/clause"
)

// ── 动态管理员数据访问（bot_admin 表）──

// AddAdmin 幂等写入一个动态管理员。
// 同 (platform, user_id) 已存在时不写入、不报错，返回 existed=true；
// 新增成功返回 existed=false。
func (db *DB) AddAdmin(ctx context.Context, platform, userID, createdBy string) (existed bool, err error) {
	if db.Orm == nil {
		return false, errors.New("database: orm is nil")
	}
	if platform == "" || userID == "" {
		return false, errors.New("database: admin platform/user_id 不能为空")
	}
	stmt := db.Orm.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "platform"}, {Name: "user_id"}},
		DoNothing: true, // 已存在则静默跳过（幂等）
	}).Create(&model.BotAdmin{
		Platform:  platform,
		UserID:    userID,
		CreatedBy: createdBy,
	})
	if stmt.Error != nil {
		return false, fmt.Errorf("database: add admin %s:%s: %w", platform, userID, stmt.Error)
	}
	// RowsAffected = 0 表示冲突未插入（已存在）
	return stmt.RowsAffected == 0, nil
}

// ListAdmins 返回全部动态管理员。
func (db *DB) ListAdmins(ctx context.Context) ([]model.BotAdmin, error) {
	if db.Orm == nil {
		return nil, errors.New("database: orm is nil")
	}
	var admins []model.BotAdmin
	if err := db.Orm.WithContext(ctx).Find(&admins).Error; err != nil {
		return nil, fmt.Errorf("database: list admins: %w", err)
	}
	return admins, nil
}
