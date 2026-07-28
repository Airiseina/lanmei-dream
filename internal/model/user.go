package model

import "time"

// User 对应 users 表
type User struct {
	ID             int64     `json:"id"               gorm:"primaryKey;autoIncrement;comment:用户ID"`
	Platform       string    `json:"platform"         gorm:"uniqueIndex:idx_platform_user;not null;size:32;comment:平台标识"`
	PlatformUserID string    `json:"platform_user_id" gorm:"uniqueIndex:idx_platform_user;not null;size:255;comment:平台用户ID"`
	Nickname       string    `json:"nickname"         gorm:"size:255;comment:昵称"`
	CreatedAt      time.Time `json:"created_at"       gorm:"autoCreateTime;comment:创建时间"`
	UpdatedAt      time.Time `json:"updated_at"       gorm:"autoUpdateTime;comment:更新时间"`
}
