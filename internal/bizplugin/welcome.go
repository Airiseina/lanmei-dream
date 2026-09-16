package bizplugin

import (
	"context"
	_ "embed"
	"fmt"
	"time"

	"github.com/DaWesen/lanmei-dream/internal/media"
	pluginpkg "github.com/DaWesen/lanmei-dream/internal/plugin"
	"github.com/zrurf/conduit"
	"go.uber.org/zap"
)

// WelcomePlugin 实现新人入群欢迎：收到 group_increase 事件时，一条消息内 @ 新人 +
// 固定文案 + 欢迎图。欢迎图内嵌于二进制、懒上传 RustFS（内容寻址幂等），不可用时逐级降级纯文本，
// 不依赖外链图床；所有群生效，不做按群配置与防刷限流。事件键由 bot 层写入黑板 Extra，插件不导 bot/gateway 包。
//
// 插件 ID welcome；无命令、无工具；依赖 RustFS 对象存储（store 为 nil 时跳过欢迎图，仅发文本欢迎语），
// 不使用 LLM、视觉服务、StateStore 与受限 KV。
type WelcomePlugin struct {
	store  *media.ObjectStore // RustFS 对象存储（未配置时欢迎图降级为纯文本）
	logger *zap.Logger
}

// NewWelcomePlugin 创建入群欢迎插件。
func NewWelcomePlugin(store *media.ObjectStore, logger *zap.Logger) *WelcomePlugin {
	return &WelcomePlugin{store: store, logger: logger}
}

// Info 返回入群欢迎插件元信息。
func (p *WelcomePlugin) Info() pluginpkg.PluginInfo {
	return pluginpkg.PluginInfo{
		ID:          "welcome",
		Name:        "入群欢迎",
		Description: "新人入群时发送欢迎消息",
		Version:     "1.0.0",
		SubtreeID:   pluginpkg.SubtreeID("welcome"),
	}
}

// OnInit 初始化入群欢迎插件，注册 Pass、Pipeline 和 Subtree。
func (p *WelcomePlugin) OnInit(ctx *pluginpkg.PluginContext) error {
	passID := pluginpkg.PassID("welcome", "welcome")
	pass := &welcomePass{store: p.store, logger: p.logger}

	if err := ctx.Engine.RegisterPass(passID, pass); err != nil {
		return fmt.Errorf("register welcome pass: %w", err)
	}

	ctx.Registry.TrackPass("welcome", passID)

	pipelineID := pluginpkg.PipelineID("welcome", "main")
	pl := conduit.NewPipelineFromIDs(pipelineID, passID)
	if err := ctx.Engine.RegisterPipeline(pl); err != nil {
		return fmt.Errorf("register pipeline: %w", err)
	}

	ctx.Registry.TrackPipeline("welcome", pipelineID)

	subtree := conduit.NewSequence(
		conduit.NewCondition(isGroupIncreaseEvent),
		conduit.NewAction(pipelineID),
	)
	if err := ctx.Engine.RegisterSubtree(pluginpkg.SubtreeID("welcome"), subtree); err != nil {
		return fmt.Errorf("register subtree: %w", err)
	}

	return nil
}

// OnStart 入群欢迎插件无需后台任务。
func (p *WelcomePlugin) OnStart(_ *pluginpkg.PluginContext) error { return nil }

// OnStop 入群欢迎插件无需清理资源。
func (p *WelcomePlugin) OnStop(_ *pluginpkg.PluginContext) error { return nil }

// 黑板事件键（由 bot 层 OnMessage 写入，键定义见 internal/bot/passes.go）。
// 插件按"不导包"约定直接使用字符串字面量。
const (
	eventKeyType = "bot.event.type"
	eventKeyData = "bot.event.data"
)

// 出站段键（由 bot 层回调读取后按段发送，键定义见 internal/bot/passes.go）。
// 插件按"不导包"约定直接使用字符串字面量。
const sendSegmentsKey = "bot.send.segments"

// welcomeImage 欢迎配图（蓝山工作室 2026 秋季招新横幅）。
// 内嵌于二进制：发送前懒上传 RustFS（内容寻址幂等），不再依赖外链图床。
//
//go:embed assets/welcome_joinus_2026.png
var welcomeImage []byte

// groupIncreaseEventType 规范化入群事件类型（对应 gateway 包的 EventTypeGroupIncrease）。
const groupIncreaseEventType = "group_increase"

// isGroupIncreaseEvent 判断当前消息是否为新人入群事件。
func isGroupIncreaseEvent(ctx *conduit.MessageContext) bool {
	eventType, _ := ctx.Extra[eventKeyType].(string)
	return eventType == groupIncreaseEventType
}

// welcomeMessage 固定欢迎文案（作为 @ 新人后的文本段）
const welcomeMessage = "欢迎来到蓝山招新群！ヾ(≧▽≦*)o，有什么想问的都可以问我呦，发送/help试试呀 (´,,•ω•,,)♡"

// welcomePass 发送欢迎消息：[@新人 + 固定文案 + 固定图片] 一条消息。
// 经出站段通道（conduit.Set "bot.send.segments"）交给 bot 回调按段发送；
// 事件缺 user_id（异常事件）时降级为纯文本欢迎语；
// RustFS 未配置或图片上传/预签名失败时降级为 [@新人 + 文案]。
type welcomePass struct {
	store  *media.ObjectStore
	logger *zap.Logger
}

// Execute 发送入群欢迎：记录新人 / 拉人者日志后，经出站段键 bot.send.segments 组装
// [@新人 + 固定文案 + 欢迎图] 一条消息发送（at 段永远按 OneBot 12 语义用 user_id，
// 协议差异由 hub.SendSegments 收敛）。
// 由 plugin.welcome.pipeline.main 在 isGroupIncreaseEvent 命中 group_increase 事件后调用；
// 事件缺 user_id（异常事件）时降级为不 @ 任何人的纯文本欢迎语，
// 欢迎图取用失败（RustFS 未配置 / 上传或预签名失败）时降级为 [@新人 + 文案]。
func (pass *welcomePass) Execute(ctx *conduit.MessageContext) error {
	eventData, _ := ctx.Extra[eventKeyData].(map[string]any)
	pass.logger.Info("welcome: 新人入群",
		zap.Any("user_id", eventData["user_id"]),
		zap.Any("operator_id", eventData["operator_id"]),
		zap.Any("sub_type", eventData["sub_type"]),
		zap.String("group_id", ctx.GroupID),
	)

	content := welcomeMessage
	newUserID, _ := eventData["user_id"].(string)

	// 缺 user_id（异常事件）：降级纯文本欢迎语，不 @ 任何人
	if newUserID == "" {
		conduit.AppendOutput(ctx, &conduit.Message{
			UserID: ctx.UserID, GroupID: ctx.GroupID, IsGroup: ctx.IsGroup,
			Content: content,
		})
		return nil
	}

	// 出站段：[@新人 + 固定文案 + 固定图片]，永远按 OneBot 12 语义组装（at 段用 user_id）；
	// at 与正文间的空格由 hub.SendSegments 统一补齐，协议差异也在该层收敛
	conduit.Set(ctx, sendSegmentsKey, buildWelcomeSegments(newUserID, content, pass.welcomeImageFile(ctx.Ctx)))
	return nil
}

// buildWelcomeSegments 组装欢迎出站段。imageFile 为空时不附加图片段（纯文本降级）。
func buildWelcomeSegments(newUserID, content, imageFile string) []map[string]any {
	segs := []map[string]any{
		{"type": "at", "data": map[string]any{"user_id": newUserID}},
		{"type": "text", "data": map[string]any{"text": content}},
	}
	if imageFile != "" {
		segs = append(segs, map[string]any{"type": "image", "data": map[string]any{"file": imageFile}})
	}
	return segs
}

// welcomeImageFile 取欢迎图的发送形式（base64 串或预签名 URL），失败时返回空串（降级纯文本）。
// 流程：内嵌字节懒上传 RustFS（内容寻址幂等，重启/重复发送零成本）→ 预签名 10 分钟 →
// 内网端点 URL 转 base64（NapCat 无法解析容器内网主机名，转换逻辑与 bot 层 sendReply 一致）。
func (pass *welcomePass) welcomeImageFile(ctx context.Context) string {
	if pass.store == nil {
		pass.logger.Warn("welcome: RustFS 未配置，欢迎图跳过，降级纯文本")
		return ""
	}
	key, err := pass.store.Put(ctx, welcomeImage, "image/png")
	if err != nil {
		pass.logger.Warn("welcome: 欢迎图上传失败，降级纯文本", zap.Error(err))
		return ""
	}
	url, err := pass.store.Presign(ctx, key, 10*time.Minute)
	if err != nil {
		pass.logger.Warn("welcome: 欢迎图预签名失败，降级纯文本", zap.Error(err))
		return ""
	}
	file := url
	if uri, err := pass.store.ImageBase64FromURL(ctx, url); err != nil {
		pass.logger.Warn("welcome: 内网图片转 base64 失败，按 URL 降级发送", zap.Error(err))
	} else if uri != "" {
		file = uri
	}
	return file
}
