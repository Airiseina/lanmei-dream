package bot

import (
	"context"
	"fmt"
	"time"

	"github.com/zrurf/conduit"
	"go.uber.org/zap"

	"github.com/DaWesen/lanmei-dream/internal/ai"
	"github.com/DaWesen/lanmei-dream/internal/ai/llm"
	"github.com/DaWesen/lanmei-dream/internal/database"
	"github.com/DaWesen/lanmei-dream/internal/model"
	"github.com/DaWesen/lanmei-dream/internal/topic"
)

// streamTimeout 流式回复的最大持续时间。
// 覆盖绝大多数 LLM 响应场景（含多轮工具调用 + 长文本生成）。
const streamTimeout = 60 * time.Second

// segmentChannelBufferSize 段落通道缓冲区大小。
// 较大的缓冲区允许 LLM 流式生成不被段落投递阻塞，
// 但段落仍按序逐条投递（由 streamSegments 的 <-done 同步点保证）。
const segmentChannelBufferSize = 32

// RoleplayStreamPass 流式角色扮演 Pass。
//
// 启动 LLM 流式生成，将段落通道存入上下文后挂起管线（yield）。
// 引擎回调检测到 yield 后，由 Bot.streamSegments 消费段落通道，
// 逐条创建子消息重入引擎，走 pipeline.roleplay_segment 交付管线。
//
// 流式 goroutine 使用独立 context（不复用 ctx.Ctx），
// 因为 yield 后引擎会调用 ctx.Cancel() 取消消息上下文。
//
// TopicMgr 非 nil 时，群聊话题命中（黑板块 KeyTopicContext）的回复完成后
// 会调用 TopicManager.RecordBotReply 记录 Bot 回复（追加窗口、授回复配额）。
type RoleplayStreamPass struct {
	Chat     *ai.ChatService
	DB       *database.DB
	Logger   *zap.Logger
	TopicMgr *topic.Manager // 可 nil：topic 系统未启用
}

// Execute 启动流式生成并挂起管线。
func (p *RoleplayStreamPass) Execute(ctx *conduit.MessageContext) error {
	userMsg := ctx.RawMsg
	if userMsg == "" {
		return nil
	}

	// 防御性检查：LLM 未配置时 chatSvc 为 nil，直接返回错误以触发 fallback
	if p.Chat == nil {
		return fmt.Errorf("roleplay: chat service not available")
	}

	platform := platformFromCtx(ctx)
	platformUserID := platformUserIDFromCtx(ctx)
	nickname := nicknameFromCtx(ctx)

	// 确保用户存在
	user, err := p.DB.GetOrCreateUser(ctx.Ctx, platform, platformUserID, nickname)
	if err != nil {
		return fmt.Errorf("roleplay: get_or_create_user: %w", err)
	}

	// 群聊话题上下文（TopicGatePass 命中话题时写入黑板；nil = 私聊/无话题）
	var topicCtx *llm.TopicContext
	var topicID, selfID string
	if p.TopicMgr != nil {
		if tc, ok := conduit.Get[*llm.TopicContext](ctx, KeyTopicContext); ok && tc != nil {
			topicCtx = tc
			topicID = tc.TopicID
		}
		selfID = SelfIDFromCtx(ctx)
	}

	// 段落通道：流式 goroutine 写入，Bot.streamSegments 读取
	segCh := make(chan string, segmentChannelBufferSize)

	// 独立 context：yield 后 ctx.Ctx 会被取消，流式必须使用独立上下文
	streamCtx, streamCancel := context.WithTimeout(context.Background(), streamTimeout)

	// 启动流式生成 goroutine
	go p.runStream(streamCtx, streamCancel, segCh, userMsg, user.ID, nickname, ctx.GroupID, ctx.UserID, platform, topicID, selfID, topicCtx)

	// 将段落通道存入上下文，供回调消费
	conduit.Set(ctx, KeyStreamChannel, segCh)

	// 挂起管线，引擎将调用 ResponseCallback
	return conduit.ErrPassYielded
}

// runStream 在独立 goroutine 中执行流式对话。
// 职责：
//  1. 调用 ChatService.ChatStream，将段落增量写入 segCh
//  2. 流结束后保存对话记录（L0 原始记录）
//  3. 群聊话题命中时记录 Bot 回复到话题（RecordBotReply）
//  4. 发生错误时发送错误提示段
//  5. defer close(segCh) 确保消费方能正常退出
func (p *RoleplayStreamPass) runStream(
	streamCtx context.Context,
	streamCancel context.CancelFunc,
	segCh chan<- string,
	userMsg string,
	userID int64,
	nickname string,
	groupID string,
	senderID string,
	platform string,
	topicID string,
	selfID string,
	topicCtx *llm.TopicContext,
) {
	defer streamCancel()
	defer close(segCh)

	resp, err := p.Chat.ChatStream(streamCtx, &llm.ChatRequest{
		Messages:     []llm.Message{{Role: llm.RoleUser, Content: userMsg}},
		UserID:       userID,
		UserName:     nickname,
		GroupName:    groupID,
		GroupID:      groupID,
		TopicContext: topicCtx,
	}, segCh)

	if err != nil {
		p.Logger.Error("roleplay stream: chat stream failed", zap.Error(err))
		// 发送错误提示段，让用户知道出了问题。
		// 使用独立 context 而非 streamCtx（后者可能已超时），
		// 确保 timeout 场景下用户也能收到错误提示。
		sendCtx, sendCancel := context.WithTimeout(context.Background(), 5*time.Second)
		select {
		case segCh <- "蓝妹现在有点迷糊，稍后再试~":
		case <-sendCtx.Done():
			p.Logger.Warn("roleplay stream: send error segment timed out")
		}
		sendCancel()
		return
	}

	// 保存对话记录（L0 原始记录，后续由 Compressor 自动压缩）
	// 使用 context.Background() 因为 streamCtx 可能已接近超时
	bgCtx := context.Background()
	if saveErr := p.DB.SaveConversation(bgCtx, userID, groupID, "user", userMsg, model.SourceChat, ""); saveErr != nil {
		p.Logger.Error("roleplay stream: save user conversation", zap.Error(saveErr))
	}
	// 根据是否调用了工具决定 assistant 消息的来源标记
	source := model.SourceChat
	pluginTag := ""
	if len(resp.InvolvedTools) > 0 {
		source = model.SourcePlugin
		pluginTag = resp.InvolvedTools[0] // 取首个工具名作为标签
	}
	if saveErr := p.DB.SaveConversation(bgCtx, userID, groupID, "assistant", resp.Content, source, pluginTag); saveErr != nil {
		p.Logger.Error("roleplay stream: save assistant conversation", zap.Error(saveErr))
	}

	// 群聊话题命中：记录 Bot 回复（追加窗口、授回复配额，保持话题活跃）
	if p.TopicMgr != nil && topicID != "" && resp.Content != "" {
		p.TopicMgr.RecordBotReply(bgCtx, platform, groupID, topicID, selfID, senderID, resp.Content)
	}
}

// ── RoleplaySegmentPass：流式段落交付 ──

// RoleplaySegmentPass 将流式段落追加到输出，由引擎回调发送。
//
// 每个段落作为子消息重入引擎，走此管线。
// 段落原文存储在 ctx.RawMsg 中（由 NewChildInput 设置）。
type RoleplaySegmentPass struct{}

// Execute 将段落追加到输出队列。
func (p *RoleplaySegmentPass) Execute(ctx *conduit.MessageContext) error {
	conduit.AppendOutput(ctx, &conduit.Message{
		UserID:  ctx.UserID,
		GroupID: ctx.GroupID,
		Content: ctx.RawMsg,
		IsGroup: ctx.IsGroup,
	})
	return nil
}
