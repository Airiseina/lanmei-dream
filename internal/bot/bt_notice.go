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
//  1. 事件上下文已在 OnMessage 阶段完整写入黑板（KeyNoticeType/KeyNoticeDetail 等）；
//  2. 记录事件日志（含操作者/被操作者等细节）；
//  3. 不产出任何回复 —— 具体互动逻辑（进群欢迎、戳一戳回应等）由插件子树实现，
//     插件通过 IsNotice 条件 + 黑板块字段消费事件。
//
// 插件消费范式（见 docs/multimodal-design.md 第 7.3 节）：
//
//	subtree := conduit.NewSequence(
//	    conduit.NewCondition(bot.IsNotice),
//	    conduit.NewCondition(func(ctx *conduit.MessageContext) bool {
//	        return bot.NoticeTypeFromCtx(ctx) == gateway.NoticeGroupMemberIncrease
//	    }),
//	    conduit.NewAction("pipeline.plugin.welcome"),
//	)
type NoticeGatePass struct {
	Logger *zap.Logger
}

// Execute 记录事件日志并静默结束（不回复）。
func (p *NoticeGatePass) Execute(ctx *conduit.MessageContext) error {
	nt := NoticeTypeFromCtx(ctx)
	detail := NoticeDetailFromCtx(ctx)

	logger := p.Logger
	if logger == nil {
		logger = zap.NewNop()
	}

	fields := []zap.Field{
		zap.String("type", string(nt)),
		zap.String("user", ctx.UserID),
		zap.String("group", ctx.GroupID),
		zap.String("platform", platformFromCtx(ctx)),
	}
	if detail != nil {
		if detail.OperatorID != "" {
			fields = append(fields, zap.String("operator", detail.OperatorID))
		}
		if detail.TargetID != "" {
			fields = append(fields, zap.String("target", detail.TargetID))
		}
		if detail.Duration > 0 {
			fields = append(fields, zap.Int("duration", detail.Duration))
		}
		if detail.MessageID != "" {
			fields = append(fields, zap.String("recall_msg_id", detail.MessageID))
		}
	}
	logger.Info("notice: 事件已记录（未被插件消费）", fields...)
	return nil // 静默：不回复
}

// noticeGatePassPlaceholder 保证 gateway 包类型被使用（NoticeGatePass 事件详情类型引用）。
var _ = gateway.NoticeGroupMemberIncrease
