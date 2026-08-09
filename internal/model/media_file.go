package model

import "time"

// MediaFile 对应 media_files 表，记录多媒体文件在 RustFS 中的对象 key。
// 以内容 hash 为唯一键，实现"同一张图片只上传/理解一次"的缓存去重与审计。
type MediaFile struct {
	ID        int64     `json:"id"         gorm:"primaryKey;autoIncrement;comment:文件ID"`
	Hash      string    `json:"hash"       gorm:"size:64;uniqueIndex;comment:文件内容sha256"`
	ObjectKey string    `json:"object_key" gorm:"size:128;not null;comment:RustFS对象key"`
	MimeType  string    `json:"mime_type"  gorm:"size:64;comment:MIME类型"`
	SizeBytes int64     `json:"size_bytes" gorm:"comment:文件大小(字节)"`
	GroupID   string    `json:"group_id"   gorm:"size:64;default:'';comment:来源群(空=私聊)"`
	UserID    string    `json:"user_id"    gorm:"size:64;comment:发送者平台user_id"`
	CreatedAt time.Time `json:"created_at" gorm:"autoCreateTime;comment:创建时间"`
}
