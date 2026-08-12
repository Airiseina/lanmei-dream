package model

import "time"

// StickerLibrary 自定义表情库表（sticker_library）。
//
// 存储 bot 收藏的自定义表情（梗图/动图），供 LLM 按语义匹配后发送：
//   - 表情文件本身存 RustFS（对象存储），本表只存对象键与元数据；
//   - Tags 为语义标签（JSON 数组字符串），检索按标签模糊匹配；
//   - 无使用计数字段，避免 LLM 对"使用次数"做无意义的判断干扰。
type StickerLibrary struct {
	ID        uint      `json:"id"        gorm:"primaryKey;autoIncrement;comment:表情ID"`
	ObjectKey string    `json:"object_key"  gorm:"not null;size:128;uniqueIndex;comment:RustFS对象键（内容寻址）"`
	FileID    string    `json:"file_id"     gorm:"size:128;comment:媒体文件ID（media_files 关联）"`
	Tags      string    `json:"tags"        gorm:"type:text;not null;default:'[]';comment:语义标签（JSON数组字符串，如 [\"大怨种\",\"无语\"]）"`
	Source    string    `json:"source"      gorm:"size:64;comment:来源（emoji-recv/手动添加）"`
	CreatedAt time.Time `json:"created_at"  gorm:"autoCreateTime;comment:创建时间"`
}

// TableName 指定表名。
func (StickerLibrary) TableName() string { return "sticker_library" }
