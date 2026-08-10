package bizplugin

import (
	"fmt"
	"math/rand/v2"

	pluginpkg "github.com/DaWesen/lanmei-dream/internal/plugin"
	"github.com/zrurf/conduit"
	"go.uber.org/zap"
)

// ============================================================
// PokePlugin 戳一戳回复插件
// ============================================================

// PokePlugin 实现戳一戳响应：有人戳蓝妹时 @ 对方并回复一条随机文案。
//
// 功能：
//   - 仅响应戳蓝妹本人的事件（target_id == 蓝妹 self_id），群内他人互戳不打扰
//   - 回复 [@戳人者 + 随机文案] 一条消息（经出站段通道发送）
//   - 所有群生效，不做防刷限流
//
// 行为树：
//
//	subtree.poke → Sequence(IsPokeOnSelfEvent, Action("pipeline.plugin.poke.main"))
//
// 管线（动态模式，支持运行时热替换）：
//
//	pipeline.plugin.poke.main → [pokePass]
//
// 事件信息读取：插件不依赖 bot/gateway 包，直接从黑板 Extra 读取事件键
// （"bot.event.type" / "bot.event.data" / "self_id"，由 bot 层 OnMessage 写入）。
type PokePlugin struct {
	logger *zap.Logger
}

// NewPokePlugin 创建戳一戳回复插件。
func NewPokePlugin(logger *zap.Logger) *PokePlugin {
	return &PokePlugin{logger: logger}
}

// Info 返回戳一戳回复插件元信息。
func (p *PokePlugin) Info() pluginpkg.PluginInfo {
	return pluginpkg.PluginInfo{
		ID:          "poke",
		Name:        "戳一戳回复",
		Description: "被戳一戳时回复对方",
		Version:     "1.0.0",
		SubtreeID:   pluginpkg.SubtreeID("poke"),
	}
}

// OnInit 初始化戳一戳回复插件，注册 Pass、Pipeline 和 Subtree。
func (p *PokePlugin) OnInit(ctx *pluginpkg.PluginContext) error {
	// 注册 Pass（依赖直接注入 Pass 结构体）
	passID := pluginpkg.PassID("poke", "poke")
	pass := &pokePass{logger: p.logger}

	if err := ctx.Engine.RegisterPass(passID, pass); err != nil {
		return fmt.Errorf("register poke pass: %w", err)
	}

	// 跟踪 Pass，卸载时自动清理
	ctx.Registry.TrackPass("poke", passID)

	// 注册动态管线（通过 Pass ID 引用，支持运行时热替换）
	pipelineID := pluginpkg.PipelineID("poke", "main")
	pl := conduit.NewPipelineFromIDs(pipelineID, passID)
	if err := ctx.Engine.RegisterPipeline(pl); err != nil {
		return fmt.Errorf("register pipeline: %w", err)
	}

	// 跟踪 Pipeline，卸载时自动清理
	ctx.Registry.TrackPipeline("poke", pipelineID)

	// 注册行为树子树：戳蓝妹事件路由
	subtree := conduit.NewSequence(
		conduit.NewCondition(isPokeOnSelfEvent),
		conduit.NewAction(pipelineID),
	)
	if err := ctx.Engine.RegisterSubtree(pluginpkg.SubtreeID("poke"), subtree); err != nil {
		return fmt.Errorf("register subtree: %w", err)
	}

	return nil
}

// OnStart 戳一戳回复插件无需后台任务。
func (p *PokePlugin) OnStart(_ *pluginpkg.PluginContext) error { return nil }

// OnStop 戳一戳回复插件无需清理资源。
func (p *PokePlugin) OnStop(_ *pluginpkg.PluginContext) error { return nil }

// ============================================================
// 条件判断
// ============================================================

// 黑板事件键（由 bot 层 OnMessage 写入，键定义见 internal/bot/passes.go）。
// 插件按"不导包"约定直接使用字符串字面量。
const eventKeySelfID = "self_id" // string 机器人自身 ID

// pokeEventType 规范化戳一戳事件类型（对应 gateway 包的 EventTypePoke）。
const pokeEventType = "poke"

// isPokeOnSelfEvent 判断当前消息是否为"戳蓝妹本人"事件：
// 事件类型为 poke，且被戳者（target_id）是蓝妹自己（self_id）。
// 群内他人互戳的事件同样会推送给机器人，不满足条件则静默跳过。
func isPokeOnSelfEvent(ctx *conduit.MessageContext) bool {
	eventType, _ := ctx.Extra[eventKeyType].(string)
	if eventType != pokeEventType {
		return false
	}
	eventData, _ := ctx.Extra[eventKeyData].(map[string]any)
	targetID, _ := eventData["target_id"].(string)
	selfID, _ := ctx.Extra[eventKeySelfID].(string)
	return targetID != "" && targetID == selfID
}

// ============================================================
// Pass 实现
// ============================================================

// pokeMessages 内置戳一戳回复文案（随机挑选）
var pokeMessages = []string{
	"戳一下要收费的呦～（计价单位：小鱼干）",
	"收到戳一戳 ×1，已缓存喵 (・∀・)",
	"蓝妹 CPU 占用 99%……哦，是在想你喵 (´｡• ᵕ •｡`)♡",
	"欢迎来蓝山玩喵！FE，BE，UI······总有一款适合你 (≧∇≦)ﾉ",
	"诶嘿～想加入蓝山工作室吗？你戳对人了喵！(๑•̀ㅂ•́)✧",
	"戳我一时爽，一直戳……蓝妹就要宕机了诶 (´；ω；`)",
	"戳戳怪！你已经被蓝山防火墙盯上了 (￣^￣)ゞ",
}

// pokePass 回复戳一戳：[@戳人者 + 随机文案] 一条消息。
// 经出站段通道（conduit.Set "bot.send.segments"）交给 bot 回调按段发送；
// 事件缺 user_id（异常事件）时降级为纯文本文案。
type pokePass struct {
	logger *zap.Logger
}

func (pass *pokePass) Execute(ctx *conduit.MessageContext) error {
	eventData, _ := ctx.Extra[eventKeyData].(map[string]any)
	pass.logger.Info("poke: 被戳一戳",
		zap.Any("user_id", eventData["user_id"]),
		zap.Any("target_id", eventData["target_id"]),
		zap.String("group_id", ctx.GroupID),
	)

	content := pokeMessages[rand.IntN(len(pokeMessages))]
	pokerID, _ := eventData["user_id"].(string)

	// 缺 user_id（异常事件）：降级纯文本文案，不 @ 任何人
	if pokerID == "" {
		conduit.AppendOutput(ctx, &conduit.Message{
			UserID: ctx.UserID, GroupID: ctx.GroupID, IsGroup: ctx.IsGroup,
			Content: content,
		})
		return nil
	}

	// 出站段：[@戳人者 + 随机文案]，永远按 OneBot 12 语义组装（at 段用 user_id），
	// 协议差异（v11 的 at→qq、动作选择）由 bot 回调经 hub.SendSegments 收敛
	conduit.Set(ctx, sendSegmentsKey, []map[string]any{
		{"type": "at", "data": map[string]any{"user_id": pokerID}},
		{"type": "text", "data": map[string]any{"text": content}},
	})
	return nil
}
