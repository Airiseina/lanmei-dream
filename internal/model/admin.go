package model

import "time"

// BotAdmin 动态管理员表（bot_admin）。
//
// 存放 /添加管理员 命令写入的管理员，是 LANMEI_BOT_SUPER_USERS 静态配置的补充；
// Bot 启动时把本表并入内存超管集合，静态与动态合并判定，重启不丢。
type BotAdmin struct {
	ID        uint      `json:"id"         gorm:"primaryKey;autoIncrement;comment:主键"`
	Platform  string    `json:"platform"   gorm:"uniqueIndex:idx_bot_admin;not null;size:32;comment:平台标识（qq/napcat/wechat/telegram）"`
	UserID    string    `json:"user_id"    gorm:"uniqueIndex:idx_bot_admin;not null;size:64;comment:平台用户ID"`
	CreatedBy string    `json:"created_by" gorm:"size:128;comment:添加者（platform:userID）"`
	CreatedAt time.Time `json:"created_at" gorm:"autoCreateTime;comment:创建时间"`
}

// TableName 指定 GORM 表名为 bot_admin。
func (BotAdmin) TableName() string { return "bot_admin" }
