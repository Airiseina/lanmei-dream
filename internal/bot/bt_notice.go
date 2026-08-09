package bot

import (
	"github.com/zrurf/conduit"
	"go.uber.org/zap"

	"github.com/DaWesen/lanmei-dream/internal/gateway"
)

// ── NoticeGatePass：互动事件预留节点 ──

// NoticeGatePass 是互动事件（进群/戳一戳/退群/撤回/禁言等）的预留兜底节点。
//
// 职责（本轮）：
//  1. 事件上下文已在 OnMessage 阶段完整写入黑板（KeyEventType/KeyEventData 等）；
//  2. 记录事件日志（含操作者/被操作者等细节）；
//  3. 不产出任何回复 —— 具体互动逻辑（进群欢迎、戳一戳回应等）由插件子树实现，
//     插件通过 IsNotice 条件 + 黑板块字段消费事件。
//
// 插件消费范式（见 docs/multimodal-design.md 第 7.3 节）：
//
//	subtree := conduit.NewSequence(
//	    conduit.NewCondition(bot.IsNotice),
//	    conduit.NewCondition(func(ctx *conduit.MessageContext) bool {
//	        return bot.EventTypeFromCtx(ctx) == gateway.EventTypeGroupIncrease
//	    }),
//	    conduit.NewAction("pipeline.plugin.welcome"),
//	)
type NoticeGatePass struct {
	Logger *zap.Logger
}

// Execute 记录事件日志并静默结束（不回复）。
func (p *NoticeGatePass) Execute(ctx *conduit.MessageContext) error {
	eventType := EventTypeFromCtx(ctx)
	data := EventDataFromCtx(ctx)

	logger := p.Logger
	if logger == nil {
		logger = zap.NewNop()
	}

	fields := []zap.Field{
		zap.String("type", eventType),
		zap.String("user", ctx.UserID),
		zap.String("group", ctx.GroupID),
		zap.String("platform", platformFromCtx(ctx)),
	}
	if data != nil {
		if v, ok := data["operator_id"].(string); ok && v != "" {
			fields = append(fields, zap.String("operator", v))
		}
		if v, ok := data["target_id"].(string); ok && v != "" {
			fields = append(fields, zap.String("target", v))
		}
		if v, ok := data["duration"].(int64); ok && v > 0 {
			fields = append(fields, zap.Int64("duration", v))
		}
		if v, ok := data["message_id"].(string); ok && v != "" {
			fields = append(fields, zap.String("recall_msg_id", v))
		}
		if v, ok := data["sub_type"].(string); ok && v != "" {
			fields = append(fields, zap.String("sub_type", v))
		}
	}
	logger.Info("notice: 事件已记录（未被插件消费）", fields...)
	return nil // 静默：不回复
}

// noticeGatePassPlaceholder 保证 gateway 包类型被使用（NoticeGatePass 事件类型常量引用）。
var _ = gateway.EventTypeGroupIncrease
