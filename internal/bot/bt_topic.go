// Package bot 中群聊话题（Topic）系统的行为树集成节点。
//
// 设计说明（详见 docs/group-topic-design.md 第 10 节）：
// TopicGatePass 是 RouterPass —— Execute 中调用 TopicManager 做"是否应回复"的确定性决策，
// Route 中根据决策路由到对话管线（pipeline.intent_analysis）或静默保存管线（pipeline.topic_ignore）。
// 命中话题时在黑板写入 TopicContext，供 RoleplayStreamPass 组装对话上下文。
package bot

import (
	"fmt"
	"time"

	"github.com/zrurf/conduit"
	"go.uber.org/zap"

	"github.com/DaWesen/lanmei-dream/internal/database"
	"github.com/DaWesen/lanmei-dream/internal/model"
	"github.com/DaWesen/lanmei-dream/internal/topic"
)

// topicReplyKey 存储 TopicGatePass 的回复决策（data，Route 读取）。
const topicReplyKey = "bot.topic.reply"

// ── TopicGatePass：群聊选择性放行（RouterPass）──

// TopicGatePass 实现 conduit.RouterPass。
// Execute：私聊放行；群聊调用 Manager.HandleGroupMessage 决策，命中话题时写黑板；
// Route：私聊 / 群聊 REPLY → pipeline.intent_analysis；群聊 SKIP → pipeline.topic_ignore。
type TopicGatePass struct {
	Manager *topic.Manager // nil 时退化为全放行（topic 系统未启用）
	Logger  *zap.Logger
}

// Execute 执行群消息话题决策并写入黑板。
func (p *TopicGatePass) Execute(ctx *conduit.MessageContext) error {
	// 私聊或管理器未启用：不决策，直接放行
	if p.Manager == nil || !ctx.IsGroup {
		return nil
	}

	msg := &topic.IncomingMsg{
		Platform:  platformFromCtx(ctx),
		SelfID:    SelfIDFromCtx(ctx),
		GroupID:   ctx.GroupID,
		UserID:    ctx.UserID,
		UserName:  nicknameFromCtx(ctx),
		Content:   ctx.RawMsg,
		AtTargets: AtTargetsFromCtx(ctx),
		SentAt:    time.Now(),
	}
	dec := p.Manager.HandleGroupMessage(ctx.Ctx, msg)
	if dec == nil {
		return nil
	}

	// 命中话题：写入上下文供对话管线消费（排除窗口末尾的当前消息，避免重复）
	if dec.Reply && dec.Topic != nil {
		if tc := p.Manager.BuildTopicContext(dec.Topic, 1); tc != nil {
			conduit.Set(ctx, KeyTopicID, tc.TopicID)
			conduit.Set(ctx, KeyTopicLabel, tc.Label)
			conduit.Set(ctx, KeyTopicContext, tc)
		}
	}
	conduit.Set(ctx, KeyMentionMode, dec.Mention)
	conduit.Set(ctx, topicReplyKey, dec.Reply)
	return nil
}

// Route 根据话题决策路由到下游管线。
func (p *TopicGatePass) Route(ctx *conduit.MessageContext) (string, error) {
	if p.Manager == nil || !ctx.IsGroup {
		return "pipeline.intent_analysis", nil
	}
	reply, _ := conduit.Get[bool](ctx, topicReplyKey)
	if reply {
		return "pipeline.intent_analysis", nil
	}
	return "pipeline.topic_ignore", nil
}

// ── TopicIgnorePass：群聊静默保存 ──

// TopicIgnorePass 保存未命中话题的群消息到对话历史（带 group_id），不生成回复。
// 这些记录供未来"群回忆"查询使用，但不进入话题归档链路。
type TopicIgnorePass struct {
	DB *database.DB
}

// Execute 保存用户消息到群对话历史并静默结束。
func (p *TopicIgnorePass) Execute(ctx *conduit.MessageContext) error {
	if p.DB == nil {
		return nil
	}
	platform := platformFromCtx(ctx)
	platformUserID := platformUserIDFromCtx(ctx)
	nickname := nicknameFromCtx(ctx)
	user, err := p.DB.GetOrCreateUser(ctx.Ctx, platform, platformUserID, nickname)
	if err != nil {
		return conduit.NewSoftError(fmt.Errorf("topic_ignore: get or create user: %w", err))
	}
	if err := p.DB.SaveConversation(ctx.Ctx, user.ID, ctx.GroupID, "user", ctx.RawMsg, model.SourceChat, ""); err != nil {
		return conduit.NewSoftError(fmt.Errorf("topic_ignore: save conversation: %w", err))
	}
	return nil
}
