package bizplugin

import (
	"fmt"
	"math/rand/v2"
	"strings"

	pluginpkg "github.com/DaWesen/lanmei-dream/internal/plugin"
	"github.com/zrurf/conduit"
)

// ============================================================
// RandomGamesPlugin 随机小游戏插件
// ============================================================

// RandomGamesPlugin 提供无需持久化状态的随机小游戏：
//   - /猜拳：QQ/NapCat 发送原生随机猜拳动画，其他平台返回文字结果
//   - /猜拳 石头（或剪刀、布）：与蓝妹进行一局可判定胜负的猜拳
//   - /骰子：掷一枚固定六面骰子；QQ/NapCat 使用原生随机骰子动画
//
// 行为树：
//
//	subtree.random_games → Selector(
//	  Sequence(isRPSCommand,  Action("pipeline.plugin.random_games.main")),
//	  Sequence(isDiceCommand, Action("pipeline.plugin.random_games.main")),
//	)
//
// 管线：
//
//	pipeline.plugin.random_games.main → [randomGamesPass]
type RandomGamesPlugin struct{}

// NewRandomGamesPlugin 创建随机小游戏插件。
func NewRandomGamesPlugin() *RandomGamesPlugin { return &RandomGamesPlugin{} }

// Info 返回随机小游戏插件元信息。
func (p *RandomGamesPlugin) Info() pluginpkg.PluginInfo {
	return pluginpkg.PluginInfo{
		ID:          "random_games",
		Name:        "随机小游戏",
		Description: "QQ 原生猜拳、六面骰子动画与跨平台文字降级",
		Version:     "1.0.0",
		Commands: []pluginpkg.CommandDef{
			{Name: "猜拳", Description: "开始随机猜拳；仅当用户明确说出石头、剪刀或布时提取该词为参数，否则不传参数", Order: 131},
			{Name: "骰子", Description: "掷一枚六面骰子；此命令不需要参数", Order: 132},
		},
		SubtreeID: pluginpkg.SubtreeID("random_games"),
	}
}

// OnInit 注册随机小游戏 Pass、Pipeline 和 Subtree。
func (p *RandomGamesPlugin) OnInit(ctx *pluginpkg.PluginContext) error {
	passID := pluginpkg.PassID("random_games", "main")
	if err := ctx.Engine.RegisterPass(passID, &randomGamesPass{intN: rand.IntN}); err != nil {
		return fmt.Errorf("register random_games pass: %w", err)
	}
	ctx.Registry.TrackPass("random_games", passID)

	pipelineID := pluginpkg.PipelineID("random_games", "main")
	if err := ctx.Engine.RegisterPipeline(conduit.NewPipelineFromIDs(pipelineID, passID)); err != nil {
		return fmt.Errorf("register random_games pipeline: %w", err)
	}
	ctx.Registry.TrackPipeline("random_games", pipelineID)

	subtree := conduit.NewSelector(
		conduit.NewSequence(
			conduit.NewCondition(isRPSCommand),
			conduit.NewAction(pipelineID),
		),
		conduit.NewSequence(
			conduit.NewCondition(isDiceCommand),
			conduit.NewAction(pipelineID),
		),
	)
	if err := ctx.Engine.RegisterSubtree(pluginpkg.SubtreeID("random_games"), subtree); err != nil {
		return fmt.Errorf("register random_games subtree: %w", err)
	}
	return nil
}

// OnStart 随机小游戏插件无需后台任务。
func (p *RandomGamesPlugin) OnStart(_ *pluginpkg.PluginContext) error { return nil }

// OnStop 随机小游戏插件无需清理资源。
func (p *RandomGamesPlugin) OnStop(_ *pluginpkg.PluginContext) error { return nil }

// ============================================================
// 条件判断
// ============================================================

// isRPSCommand 判断消息是否为猜拳命令。
func isRPSCommand(ctx *conduit.MessageContext) bool {
	msg := strings.TrimSpace(ctx.RawMsg)
	return msg == "/猜拳" || strings.HasPrefix(msg, "/猜拳 ")
}

// isDiceCommand 判断消息是否为固定六面骰子命令。
func isDiceCommand(ctx *conduit.MessageContext) bool {
	return strings.TrimSpace(ctx.RawMsg) == "/骰子"
}

// ============================================================
// 游戏规则与输出
// ============================================================

func supportsNativeRandomSegment(ctx *conduit.MessageContext) bool {
	platform, _ := ctx.Extra["platform"].(string)
	return platform == "qq" || platform == "napcat"
}

func setNativeRandomSegment(ctx *conduit.MessageContext, segmentType string) {
	conduit.Set(ctx, sendSegmentsKey, []map[string]any{{
		"type": segmentType,
		"data": map[string]any{},
	}})
}

type rpsChoice int

const (
	rpsRock rpsChoice = iota
	rpsScissors
	rpsPaper
)

func (c rpsChoice) String() string {
	switch c {
	case rpsRock:
		return "石头"
	case rpsScissors:
		return "剪刀"
	case rpsPaper:
		return "布"
	default:
		return "未知"
	}
}

func parseRPSChoice(raw string) (rpsChoice, bool) {
	switch strings.TrimSpace(raw) {
	case "石头":
		return rpsRock, true
	case "剪刀":
		return rpsScissors, true
	case "布":
		return rpsPaper, true
	default:
		return 0, false
	}
}

func rpsOutcome(user, bot rpsChoice) string {
	diff := (int(user) - int(bot) + 3) % 3
	switch diff {
	case 0:
		return "平局"
	case 2:
		return "你赢了"
	default:
		return "蓝妹赢了"
	}
}

// ============================================================
// Pass 实现
// ============================================================

// randomGamesPass 按命令分发猜拳与六面骰子请求。
type randomGamesPass struct {
	intN func(int) int
}

func (pass *randomGamesPass) Execute(ctx *conduit.MessageContext) error {
	msg := strings.TrimSpace(ctx.RawMsg)
	switch {
	case msg == "/猜拳":
		pass.randomRPS(ctx)
	case strings.HasPrefix(msg, "/猜拳 "):
		pass.battleRPS(ctx, strings.TrimSpace(strings.TrimPrefix(msg, "/猜拳")))
	case msg == "/骰子":
		pass.dice(ctx)
	}
	return nil
}

func (pass *randomGamesPass) randomRPS(ctx *conduit.MessageContext) {
	if supportsNativeRandomSegment(ctx) {
		setNativeRandomSegment(ctx, "rps")
		return
	}
	choice := rpsChoice(pass.intN(3))
	appendGameText(ctx, "✊ 蓝妹出了"+choice.String())
}

func (pass *randomGamesPass) battleRPS(ctx *conduit.MessageContext, rawChoice string) {
	userChoice, ok := parseRPSChoice(rawChoice)
	if !ok {
		appendGameText(ctx, "请输入石头、剪刀或布，例如：/猜拳 石头")
		return
	}
	botChoice := rpsChoice(pass.intN(3))
	appendGameText(ctx, fmt.Sprintf("你出了%s，蓝妹出了%s——%s！", userChoice, botChoice, rpsOutcome(userChoice, botChoice)))
}

func (pass *randomGamesPass) dice(ctx *conduit.MessageContext) {
	if supportsNativeRandomSegment(ctx) {
		setNativeRandomSegment(ctx, "dice")
		return
	}
	appendGameText(ctx, fmt.Sprintf("🎲 六面骰子掷出了 %d", pass.intN(6)+1))
}

func appendGameText(ctx *conduit.MessageContext, content string) {
	conduit.AppendOutput(ctx, &conduit.Message{
		UserID:  ctx.UserID,
		GroupID: ctx.GroupID,
		IsGroup: ctx.IsGroup,
		Content: content,
	})
}
