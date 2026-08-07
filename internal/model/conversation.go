package model

import "time"

// ConversationSource 标记对话来源，用于压缩器区分真实对话与插件输出
type ConversationSource string

const (
	SourceChat   ConversationSource = "chat"   // 真实对话
	SourcePlugin ConversationSource = "plugin" // 插件交互
)

// Conversation 对应 conversations 表，存储对话历史（L0 原始层）
type Conversation struct {
	ID        int64              `json:"id"         gorm:"primaryKey;autoIncrement;comment:对话ID"`
	UserID    int64              `json:"user_id"    gorm:"index;not null;comment:用户ID"`
	Role      string             `json:"role"       gorm:"size:20;not null;comment:角色(user/assistant)"`
	Content   string             `json:"content"    gorm:"type:text;not null;comment:对话内容"`
	Source    ConversationSource `json:"source"     gorm:"size:20;not null;default:'chat';comment:来源(chat/plugin)"`
	PluginTag string             `json:"plugin_tag" gorm:"size:64;comment:插件标识(如 fortune)"`
	CreatedAt time.Time          `json:"created_at" gorm:"autoCreateTime;index:idx_conversations_created;comment:创建时间"`
}
