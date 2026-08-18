package bizplugin

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/DaWesen/lanmei-dream/internal/database"
	pluginpkg "github.com/DaWesen/lanmei-dream/internal/plugin"
	"github.com/zrurf/conduit"
)

// ============================================================
// GuessNumberPlugin 猜数字插件
// ============================================================

// GuessNumberPlugin 实现 1～100 猜数字游戏：
//   - /猜数字：开始一局
//   - /猜数字 50：提交猜测，蓝妹提示偏大或偏小
//   - /揭晓数字：直接揭晓答案并结束本局
//
// 同一个群（或同一私聊）同时只能进行一局，10 分钟未完成则自动超时。
// 局状态存入插件受限 KV 存储，按群聊或私聊隔离，服务重启后仍可继续。
//
// 行为树：
//
//	subtree.guess_number → Selector(
//	  Sequence(isGuessNumberCommand,  Action("pipeline.plugin.guess_number.main")),
//	  Sequence(isRevealNumberCommand, Action("pipeline.plugin.guess_number.main")),
//	)
//
// 管线：
//
//	pipeline.plugin.guess_number.main → [guessNumberPass]
type GuessNumberPlugin struct {
	kv *database.PluginKVStore
}

// NewGuessNumberPlugin 创建猜数字插件。
func NewGuessNumberPlugin() *GuessNumberPlugin { return &GuessNumberPlugin{} }

// Info 返回猜数字插件元信息。
func (p *GuessNumberPlugin) Info() pluginpkg.PluginInfo {
	return pluginpkg.PluginInfo{
		ID:          "guess_number",
		Name:        "猜数字",
		Description: "1～100 猜数字游戏（同群单局、可揭晓、10 分钟超时）",
		Version:     "1.0.0",
		Commands: []pluginpkg.CommandDef{
			{Name: "猜数字", Description: "开始游戏或提交猜测，格式：/猜数字 50", Order: 133},
			{Name: "揭晓数字", Description: "揭晓答案并结束当前游戏", Order: 134},
		},
		SubtreeID: pluginpkg.SubtreeID("guess_number"),
	}
}

// OnInit 注册猜数字 Pass、Pipeline 和 Subtree。
func (p *GuessNumberPlugin) OnInit(ctx *pluginpkg.PluginContext) error {
	p.kv = ctx.KV

	passID := pluginpkg.PassID("guess_number", "main")
	pass := &guessNumberPass{kv: p.kv}
	if err := ctx.Engine.RegisterPass(passID, pass); err != nil {
		return fmt.Errorf("register guess_number pass: %w", err)
	}
	ctx.Registry.TrackPass("guess_number", passID)

	pipelineID := pluginpkg.PipelineID("guess_number", "main")
	if err := ctx.Engine.RegisterPipeline(conduit.NewPipelineFromIDs(pipelineID, passID)); err != nil {
		return fmt.Errorf("register guess_number pipeline: %w", err)
	}
	ctx.Registry.TrackPipeline("guess_number", pipelineID)

	subtree := conduit.NewSelector(
		conduit.NewSequence(
			conduit.NewCondition(isGuessNumberCommand),
			conduit.NewAction(pipelineID),
		),
		conduit.NewSequence(
			conduit.NewCondition(isRevealNumberCommand),
			conduit.NewAction(pipelineID),
		),
	)
	if err := ctx.Engine.RegisterSubtree(pluginpkg.SubtreeID("guess_number"), subtree); err != nil {
		return fmt.Errorf("register guess_number subtree: %w", err)
	}
	return nil
}

// OnStart 猜数字插件无需后台任务。
func (p *GuessNumberPlugin) OnStart(_ *pluginpkg.PluginContext) error { return nil }

// OnStop 猜数字插件无需清理资源。
func (p *GuessNumberPlugin) OnStop(_ *pluginpkg.PluginContext) error { return nil }

// ============================================================
// 条件判断
// ============================================================

// isGuessNumberCommand 判断消息是否为开局或猜测命令。
func isGuessNumberCommand(ctx *conduit.MessageContext) bool {
	msg := strings.TrimSpace(ctx.RawMsg)
	return msg == "/猜数字" || strings.HasPrefix(msg, "/猜数字 ")
}

// isRevealNumberCommand 判断消息是否为揭晓命令。
func isRevealNumberCommand(ctx *conduit.MessageContext) bool {
	return strings.TrimSpace(ctx.RawMsg) == "/揭晓数字"
}

// ============================================================
// 局状态
// ============================================================

const (
	guessNumberPluginID = "guess_number"
	guessNumberMaxAge   = 10 * time.Minute
)

// guessNumberGame 保存一局猜数字游戏的持久化状态。
type guessNumberGame struct {
	Answer    int    `json:"answer"`
	Creator   string `json:"creator"`
	Attempts  int    `json:"attempts"`
	CreatedAt int64  `json:"created_at"`
}

// guessNumberKey 生成局的 KV 键：群聊按群 ID 隔离，私聊按用户 ID 隔离。
func guessNumberKey(groupID, userID string) string {
	if groupID != "" {
		return "state:group:" + groupID
	}
	return "state:dm:" + userID
}

// ============================================================
// Pass 实现
// ============================================================

// guessNumberPass 按命令处理开局、猜测与揭晓，并串行保护状态读写。
type guessNumberPass struct {
	kv *database.PluginKVStore
	mu sync.Mutex
}

func (pass *guessNumberPass) Execute(ctx *conduit.MessageContext) error {
	if pass.kv == nil {
		pass.reply(ctx, "猜数字功能暂时不可用，请稍后再试~")
		return nil
	}

	msg := strings.TrimSpace(ctx.RawMsg)
	switch {
	case msg == "/猜数字":
		return pass.start(ctx)
	case strings.HasPrefix(msg, "/猜数字 "):
		return pass.guess(ctx, strings.TrimSpace(strings.TrimPrefix(msg, "/猜数字")))
	case msg == "/揭晓数字":
		return pass.reveal(ctx)
	default:
		return nil
	}
}

func (pass *guessNumberPass) reply(ctx *conduit.MessageContext, content string) {
	conduit.AppendOutput(ctx, &conduit.Message{
		UserID:  ctx.UserID,
		GroupID: ctx.GroupID,
		IsGroup: ctx.IsGroup,
		Content: content,
	})
}

// replyWinner 在 QQ 群聊中 @ 猜中答案的用户；其他平台或私聊降级为普通文本。
// at 段统一使用 OneBot 12 的 user_id，网关发送 OB11 时会转换为 qq 字段。
func (pass *guessNumberPass) replyWinner(ctx *conduit.MessageContext, content string) {
	platform, _ := ctx.Extra["platform"].(string)
	if !ctx.IsGroup || ctx.UserID == "" || (platform != "qq" && platform != "napcat") {
		pass.reply(ctx, strings.TrimSpace(content))
		return
	}
	conduit.Set(ctx, sendSegmentsKey, []map[string]any{
		{"type": "at", "data": map[string]any{"user_id": ctx.UserID}},
		{"type": "text", "data": map[string]any{"text": content}},
	})
}

// start 开始一局；同一群或私聊已有未过期游戏时拒绝重复开局。
func (pass *guessNumberPass) start(ctx *conduit.MessageContext) error {
	pass.mu.Lock()
	defer pass.mu.Unlock()

	key := guessNumberKey(ctx.GroupID, ctx.UserID)
	existing, expired, err := pass.load(ctx.Ctx, key)
	if err != nil {
		return fmt.Errorf("guess_number: load game: %w", err)
	}
	if existing != nil {
		pass.reply(ctx, "本群已经有一局猜数字在进行啦！直接用 `/猜数字 50` 继续猜，或用 `/揭晓数字` 结束本局。")
		return nil
	}

	game := &guessNumberGame{
		// rand.IntN(100) 均匀生成 [0, 100) 的伪随机整数，加 1 后得到 [1, 100]。
		Answer:    rand.IntN(100) + 1,
		Creator:   ctx.UserID,
		CreatedAt: time.Now().Unix(),
	}
	if err := pass.save(ctx.Ctx, key, game); err != nil {
		return fmt.Errorf("guess_number: save game: %w", err)
	}
	prefix := ""
	if expired {
		prefix = "上一局已超过 10 分钟，自动结束啦。\n"
	}
	pass.reply(ctx, prefix+"🎯 猜数字开始！答案是 1～100 之间的整数。\n用 `/猜数字 50` 提交猜测，10 分钟内有效；想直接看答案可用 `/揭晓数字`。")
	return nil
}

// guess 提交一次猜测，猜中后立即结算并清除本局。
func (pass *guessNumberPass) guess(ctx *conduit.MessageContext, raw string) error {
	number, err := strconv.Atoi(raw)
	if err != nil || number < 1 || number > 100 {
		pass.reply(ctx, "请输入 1～100 的整数，例如：/猜数字 50")
		return nil
	}

	pass.mu.Lock()
	defer pass.mu.Unlock()

	key := guessNumberKey(ctx.GroupID, ctx.UserID)
	game, expired, err := pass.load(ctx.Ctx, key)
	if err != nil {
		return fmt.Errorf("guess_number: load game: %w", err)
	}
	if game == nil {
		if expired {
			pass.reply(ctx, "这局已经超过 10 分钟，自动结束啦。用 `/猜数字` 重新开一局吧。")
		} else {
			pass.reply(ctx, "现在没有进行中的猜数字游戏，用 `/猜数字` 开一局吧。")
		}
		return nil
	}

	game.Attempts++
	if number == game.Answer {
		if err := pass.kv.Delete(ctx.Ctx, guessNumberPluginID, key); err != nil {
			return fmt.Errorf("guess_number: finish game: %w", err)
		}
		pass.replyWinner(ctx, fmt.Sprintf(" 🎉 猜对啦！答案就是 %d，本局一共猜了 %d 次。", game.Answer, game.Attempts))
		return nil
	}
	if err := pass.save(ctx.Ctx, key, game); err != nil {
		return fmt.Errorf("guess_number: update game: %w", err)
	}
	if number < game.Answer {
		pass.reply(ctx, fmt.Sprintf("%d 太小了，再猜大一点~", number))
	} else {
		pass.reply(ctx, fmt.Sprintf("%d 太大了，再猜小一点~", number))
	}
	return nil
}

// reveal 揭晓答案并清除当前局。
func (pass *guessNumberPass) reveal(ctx *conduit.MessageContext) error {
	pass.mu.Lock()
	defer pass.mu.Unlock()

	key := guessNumberKey(ctx.GroupID, ctx.UserID)
	game, expired, err := pass.load(ctx.Ctx, key)
	if err != nil {
		return fmt.Errorf("guess_number: load game: %w", err)
	}
	if game == nil {
		if expired {
			pass.reply(ctx, "这局已经超过 10 分钟，自动结束啦。用 `/猜数字` 重新开一局吧。")
		} else {
			pass.reply(ctx, "现在没有进行中的猜数字游戏，用 `/猜数字` 开一局吧。")
		}
		return nil
	}
	if err := pass.kv.Delete(ctx.Ctx, guessNumberPluginID, key); err != nil {
		return fmt.Errorf("guess_number: reveal game: %w", err)
	}
	pass.reply(ctx, fmt.Sprintf("答案是 %d！本局结束，共猜了 %d 次。再用 `/猜数字` 就能开新一局。", game.Answer, game.Attempts))
	return nil
}

// load 读取当前局；过期时删除状态并通过 expired 返回超时信息。
func (pass *guessNumberPass) load(ctx context.Context, key string) (*guessNumberGame, bool, error) {
	raw, err := pass.kv.Get(ctx, guessNumberPluginID, key)
	if err != nil || raw == "" {
		return nil, false, err
	}
	var game guessNumberGame
	if err := json.Unmarshal([]byte(raw), &game); err != nil {
		return nil, false, fmt.Errorf("decode state: %w", err)
	}
	if time.Since(time.Unix(game.CreatedAt, 0)) >= guessNumberMaxAge {
		if err := pass.kv.Delete(ctx, guessNumberPluginID, key); err != nil {
			return nil, false, err
		}
		return nil, true, nil
	}
	return &game, false, nil
}

// save 保存当前局。
func (pass *guessNumberPass) save(ctx context.Context, key string, game *guessNumberGame) error {
	data, err := json.Marshal(game)
	if err != nil {
		return err
	}
	return pass.kv.Set(ctx, guessNumberPluginID, key, string(data))
}
