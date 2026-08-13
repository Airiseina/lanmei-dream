// 内容管理（M3）数据访问层：
// 知识库分块 / 记忆 / 表情包 / 用户 / 群组聚合。
// 全部直接复用主服务连接池，仅查询/删除，不介入各子系统内部逻辑。
package store

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"

	"github.com/DaWesen/lanmei-dream/internal/model"
)

// ─────────────────────────────────────────────
// 知识库分块
// ─────────────────────────────────────────────

// ListKnowledgeChunks 分页查询知识库分块（排除大字段 Embedding）。
func (s *Store) ListKnowledgeChunks(ctx context.Context, kbID, keyword string, offset, limit int) ([]model.KnowledgeChunk, int64, error) {
	var list []model.KnowledgeChunk
	var total int64
	q := s.db.Orm.WithContext(ctx).Model(&model.KnowledgeChunk{})
	if kbID != "" {
		q = q.Where("knowledge_base_id = ?", kbID)
	}
	if keyword != "" {
		like := "%" + keyword + "%"
		q = q.Where("(title ILIKE ? OR content ILIKE ?)", like, like)
	}
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	err := q.Omit("embedding").
		Order("id DESC").Offset(offset).Limit(limit).Find(&list).Error
	return list, total, err
}

// DeleteKnowledgeChunk 删除单个知识库分块。
func (s *Store) DeleteKnowledgeChunk(ctx context.Context, id int64) error {
	return s.db.Orm.WithContext(ctx).Delete(&model.KnowledgeChunk{}, id).Error
}

// CountKnowledgeChunks 统计某知识库的分块数。
func (s *Store) CountKnowledgeChunks(ctx context.Context, kbID string) (int64, error) {
	var n int64
	err := s.db.Orm.WithContext(ctx).Model(&model.KnowledgeChunk{}).
		Where("knowledge_base_id = ?", kbID).Count(&n).Error
	return n, err
}

// ─────────────────────────────────────────────
// 记忆
// ─────────────────────────────────────────────

// ListMemories 分页查询记忆（排除向量大字段）。
// userID/groupID 为空表示不限制；keyword 按内容模糊匹配。
func (s *Store) ListMemories(ctx context.Context, userID, groupID, keyword string, offset, limit int) ([]model.MemoryVector, int64, error) {
	var list []model.MemoryVector
	var total int64
	q := s.db.Orm.WithContext(ctx).Model(&model.MemoryVector{})
	if userID != "" {
		q = q.Where("user_id = ?", userID)
	}
	if groupID != "" {
		q = q.Where("group_id = ?", groupID)
	}
	if keyword != "" {
		q = q.Where("content ILIKE ?", "%"+keyword+"%")
	}
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	err := q.Omit("embedding", "search_vec").
		Order("id DESC").Offset(offset).Limit(limit).Find(&list).Error
	return list, total, err
}

// DeleteMemory 删除单条记忆。
func (s *Store) DeleteMemory(ctx context.Context, id int64) error {
	return s.db.Orm.WithContext(ctx).Delete(&model.MemoryVector{}, id).Error
}

// ─────────────────────────────────────────────
// 表情包
// ─────────────────────────────────────────────

// ListStickers 分页查询表情包（keyword 按标签/来源模糊匹配）。
func (s *Store) ListStickers(ctx context.Context, keyword string, offset, limit int) ([]model.StickerLibrary, int64, error) {
	var list []model.StickerLibrary
	var total int64
	q := s.db.Orm.WithContext(ctx).Model(&model.StickerLibrary{})
	if keyword != "" {
		like := "%" + keyword + "%"
		q = q.Where("(tags ILIKE ? OR source ILIKE ?)", like, like)
	}
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	err := q.Order("id DESC").Offset(offset).Limit(limit).Find(&list).Error
	return list, total, err
}

// GetStickerByID 按 ID 查询表情包。
func (s *Store) GetStickerByID(ctx context.Context, id uint) (*model.StickerLibrary, error) {
	var st model.StickerLibrary
	err := s.db.Orm.WithContext(ctx).First(&st, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &st, nil
}

// UpdateSticker 更新表情包元数据。
func (s *Store) UpdateSticker(ctx context.Context, st *model.StickerLibrary) error {
	return s.db.Orm.WithContext(ctx).Save(st).Error
}

// UpdateStickerTags 仅更新表情包语义标签（避免全量 Save 覆盖其余字段）。
func (s *Store) UpdateStickerTags(ctx context.Context, id uint, tagsJSON string) error {
	return s.db.Orm.WithContext(ctx).Model(&model.StickerLibrary{}).
		Where("id = ?", id).Update("tags", tagsJSON).Error
}

// DeleteSticker 删除表情包记录（RustFS 对象按内容寻址保留，不影响其它引用）。
func (s *Store) DeleteSticker(ctx context.Context, id uint) error {
	return s.db.Orm.WithContext(ctx).Delete(&model.StickerLibrary{}, id).Error
}

// ─────────────────────────────────────────────
// 用户
// ─────────────────────────────────────────────

// ListUsers 分页查询用户（keyword 按昵称/平台用户ID匹配）。
func (s *Store) ListUsers(ctx context.Context, keyword string, offset, limit int) ([]model.User, int64, error) {
	var list []model.User
	var total int64
	q := s.db.Orm.WithContext(ctx).Model(&model.User{})
	if keyword != "" {
		like := "%" + keyword + "%"
		q = q.Where("(nickname ILIKE ? OR platform_user_id ILIKE ?)", like, like)
	}
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	err := q.Order("id DESC").Offset(offset).Limit(limit).Find(&list).Error
	return list, total, err
}

// UpdateUserBan 封禁/解封用户：更新 users 表并同步刷新用户映射缓存（封禁立即生效）。
func (s *Store) UpdateUserBan(ctx context.Context, id int64, banned bool, reason string) error {
	var u model.User
	if err := s.Orm().WithContext(ctx).First(&u, id).Error; err != nil {
		return err
	}
	return s.DB().SetUserBanned(ctx, u.Platform, u.PlatformUserID, banned, reason)
}

// ─────────────────────────────────────────────
// 群组（聚合）
// ─────────────────────────────────────────────

// GroupRow 群聚合行：group_id 唯一，附带的 platform/config 来自 group_config（可能为空）。
type GroupRow struct {
	GroupID  string `gorm:"column:group_id"  json:"group_id"`
	Platform string `gorm:"column:platform"  json:"platform"`
	ConfigID uint   `gorm:"column:config_id" json:"config_id"`
}

// ListGroups 聚合出现在各链路表中的群 ID 并分页。
// 来源：group_config / conversations / conduit_trace / token_usage（group_id 非空）。
func (s *Store) ListGroups(ctx context.Context, keyword string, offset, limit int) ([]GroupRow, int64, error) {
	sqlAll := `
		SELECT t.group_id, COALESCE(g.platform, '') AS platform, COALESCE(g.id, 0) AS config_id
		FROM (
			SELECT group_id FROM group_config WHERE group_id <> ''
			UNION SELECT DISTINCT group_id FROM conversations WHERE group_id <> ''
			UNION SELECT DISTINCT group_id FROM conduit_trace WHERE group_id <> ''
			UNION SELECT DISTINCT group_id FROM token_usage WHERE group_id <> ''
		) t
		LEFT JOIN group_config g ON g.group_id = t.group_id`
	where := ""
	args := []any{}
	if keyword != "" {
		where = " WHERE t.group_id ILIKE ?"
		args = append(args, "%"+keyword+"%")
	}
	var total int64
	// GORM 的 Raw().Count() 会将多列 SQL 直接 Scan 到单个值（列数不匹配），
	// 需显式包一层子查询计数。
	countSQL := "SELECT COUNT(*) FROM (" + sqlAll + where + ") sub"
	if err := s.db.Orm.WithContext(ctx).Raw(countSQL, args...).Scan(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("群聚合计数: %w", err)
	}
	var rows []GroupRow
	query := sqlAll + where + " ORDER BY t.group_id ASC LIMIT ? OFFSET ?"
	args = append(args, limit, offset)
	if err := s.db.Orm.WithContext(ctx).Raw(query, args...).Scan(&rows).Error; err != nil {
		return nil, 0, fmt.Errorf("群聚合查询: %w", err)
	}
	return rows, total, nil
}
