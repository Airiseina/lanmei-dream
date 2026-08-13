// Package audit 提供操作审计能力（零信任设计：敏感操作全量留痕）。
package audit

import (
	"context"
	"time"

	"github.com/DaWesen/lanmei-dream/internal/manager/store"
	"github.com/DaWesen/lanmei-dream/internal/model"
)

// Log 审计日志写入器。
type Log struct {
	store *store.Store
}

// New 创建审计写入器。
func New(s *store.Store) *Log {
	return &Log{store: s}
}

// Record 写入一条审计记录。
// action 形如 "admin.create" / "llm.switch"；detail 为可选的变更 diff（JSON 文本）。
// 本方法同步写入，失败不影响主流程（仅被调用方记录）。
func (a *Log) Record(ctx context.Context, adminID *uint, username, action, targetType, targetID, detail, ip, result string) {
	entry := &model.AuditLog{
		AdminID:    adminID,
		Username:   username,
		Action:     action,
		TargetType: targetType,
		TargetID:   targetID,
		Detail:     detail,
		IP:         ip,
		Result:     result,
		CreatedAt:  time.Now(),
	}
	// 使用独立超时上下文，避免慢操作拖慢主请求
	c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := a.store.CreateAuditLog(c, entry); err != nil {
		// 审计失败不阻断业务，由调用方日志兜底（此处静默，防止循环记录）
		return
	}
}
