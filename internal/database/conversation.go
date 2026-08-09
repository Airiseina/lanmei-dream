package database

import (
	"context"
	"fmt"

	"github.com/DaWesen/lanmei-dream/internal/model"
)

// SaveConversation 存储一条对话记录。
// groupID 为来源群（空字符串 = 私聊）；群聊对话与私聊对话按 group_id 隔离。
// source: 来源标记（chat/plugin），pluginTag: 插件标识（source=plugin 时必填）
func (db *DB) SaveConversation(ctx context.Context, userID int64, groupID, role, content string, source model.ConversationSource, pluginTag string) error {
	c := model.Conversation{
		UserID:    userID,
		GroupID:   groupID,
		Role:      role,
		Content:   content,
		Source:    source,
		PluginTag: pluginTag,
	}
	if err := db.Orm.WithContext(ctx).Create(&c).Error; err != nil {
		return fmt.Errorf("save_conversation: %w", err)
	}
	return nil
}

// GetRecentConversations 获取用户在某维度（私聊 groupID="" 或指定群）最近的 N 条对话（按时间正序）
func (db *DB) GetRecentConversations(ctx context.Context, userID int64, groupID string, limit int) ([]*model.Conversation, error) {
	var convs []*model.Conversation
	err := db.Orm.WithContext(ctx).
		Where("user_id = ? AND group_id = ?", userID, groupID).
		Order("created_at DESC").
		Limit(limit).
		Find(&convs).Error
	if err != nil {
		return nil, fmt.Errorf("get_recent_conversations: %w", err)
	}

	// 反转为时间正序
	for i, j := 0, len(convs)-1; i < j; i, j = i+1, j-1 {
		convs[i], convs[j] = convs[j], convs[i]
	}
	return convs, nil
}

// SearchConversations 按关键词模糊搜索用户对话（仅私聊维度，避免群聊记录污染个人检索）
// 使用参数化查询防止 SQL 注入
func (db *DB) SearchConversations(ctx context.Context, userID int64, keyword string, limit int) ([]*model.Conversation, error) {
	var convs []*model.Conversation
	err := db.Orm.WithContext(ctx).
		Where("user_id = ? AND group_id = '' AND content ILIKE ?", userID, "%"+keyword+"%").
		Order("created_at DESC").
		Limit(limit).
		Find(&convs).Error
	if err != nil {
		return nil, fmt.Errorf("search_conversations: %w", err)
	}
	return convs, nil
}
