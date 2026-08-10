package bizplugin

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"sort"
	"strings"
	"time"

	"github.com/DaWesen/lanmei-dream/internal/database"
	pluginpkg "github.com/DaWesen/lanmei-dream/internal/plugin"
	"github.com/zrurf/conduit"
	"go.uber.org/zap"
)

// ============================================================
// SigninPlugin 签到插件
// ============================================================

// SigninPlugin 实现每日签到和试试手气功能。
//
// 功能（与上游 LanMei 保持一致）：
//   - /签到：用户每日签到，固定获得 5 积分，附带随机事件描述
//   - /试试手气：随机签到，概率获得不同积分（可能为正也可能为负），附带随机事件描述
//
// 数据持久化：积分状态与排行榜存放在插件受限 KV 存储（PluginContext.KV，
// PostgreSQL 后端）而非 Redis StateStore，重启不丢失。
//
// 行为树：
//
//	subtree.signin → Selector [
//	  Sequence(isSigninCommand, Action(pipeline.signin.normal))
//	  Sequence(isRandomSigninCommand, Action(pipeline.signin.random))
//	]
//
// 管线：
//
//	pipeline.signin.normal  → [executePass, replyPass]
//	pipeline.signin.random  → [executePass, replyPass]
type SigninPlugin struct {
	kv     *database.PluginKVStore // 受限键值存储（持久化）
	logger *zap.Logger
}

// NewSigninPlugin 创建签到插件。
func NewSigninPlugin(logger *zap.Logger) *SigninPlugin {
	return &SigninPlugin{logger: logger}
}

// Info 返回签到插件元信息。
func (p *SigninPlugin) Info() pluginpkg.PluginInfo {
	return pluginpkg.PluginInfo{
		ID:          "signin",
		Name:        "签到",
		Description: "每日签到和试试手气",
		Version:     "2.0.0",
		Commands: []pluginpkg.CommandDef{
			{Name: "签到", Description: "每日签到，获取积分奖励"},
			{Name: "试试手气", Description: "试试手气，随机获取积分"},
		},
		SubtreeID: pluginpkg.SubtreeID("signin"),
		Tools: []pluginpkg.ToolDef{
			{
				Name:        "signin_status",
				Description: "查询用户签到状态和积分",
				Handler:     p.toolSigninStatus,
			},
			{
				Name:        "signin_random",
				Description: "帮用户试试手气，进行随机签到",
				Handler:     p.toolSigninRandom,
			},
		},
	}
}

// OnInit 初始化签到插件，注册 Pass、Pipeline 和 Subtree。
func (p *SigninPlugin) OnInit(ctx *pluginpkg.PluginContext) error {
	p.kv = ctx.KV

	// ── 注册 Pass ──

	// 普通签到
	normalExecPassID := pluginpkg.PassID("signin", "normal_execute")
	normalReplyPassID := pluginpkg.PassID("signin", "normal_reply")
	normalExecPass := &signinNormalExecutePass{kv: p.kv, logger: p.logger}
	normalReplyPass := &signinNormalReplyPass{}

	if err := ctx.Engine.RegisterPass(normalExecPassID, normalExecPass); err != nil {
		return fmt.Errorf("register normal execute pass: %w", err)
	}
	if err := ctx.Engine.RegisterPass(normalReplyPassID, normalReplyPass); err != nil {
		return fmt.Errorf("register normal reply pass: %w", err)
	}
	ctx.Registry.TrackPass("signin", normalExecPassID)
	ctx.Registry.TrackPass("signin", normalReplyPassID)

	// 随机签到
	randomExecPassID := pluginpkg.PassID("signin", "random_execute")
	randomReplyPassID := pluginpkg.PassID("signin", "random_reply")
	randomExecPass := &signinRandomExecutePass{kv: p.kv, logger: p.logger}
	randomReplyPass := &signinRandomReplyPass{}

	if err := ctx.Engine.RegisterPass(randomExecPassID, randomExecPass); err != nil {
		return fmt.Errorf("register random execute pass: %w", err)
	}
	if err := ctx.Engine.RegisterPass(randomReplyPassID, randomReplyPass); err != nil {
		return fmt.Errorf("register random reply pass: %w", err)
	}
	ctx.Registry.TrackPass("signin", randomExecPassID)
	ctx.Registry.TrackPass("signin", randomReplyPassID)

	// ── 注册管线 ──

	normalPipelineID := pluginpkg.PipelineID("signin", "normal")
	normalPl := conduit.NewPipelineFromIDs(
		normalPipelineID,
		normalExecPassID,
		normalReplyPassID,
	)
	if err := ctx.Engine.RegisterPipeline(normalPl); err != nil {
		return fmt.Errorf("register normal pipeline: %w", err)
	}
	ctx.Registry.TrackPipeline("signin", normalPipelineID)

	randomPipelineID := pluginpkg.PipelineID("signin", "random")
	randomPl := conduit.NewPipelineFromIDs(
		randomPipelineID,
		randomExecPassID,
		randomReplyPassID,
	)
	if err := ctx.Engine.RegisterPipeline(randomPl); err != nil {
		return fmt.Errorf("register random pipeline: %w", err)
	}
	ctx.Registry.TrackPipeline("signin", randomPipelineID)

	// ── 注册行为树子树：签到命令路由 ──
	subtree := conduit.NewSelector(
		conduit.NewSequence(
			conduit.NewCondition(isSigninCommand),
			conduit.NewAction(normalPipelineID),
		),
		conduit.NewSequence(
			conduit.NewCondition(isRandomSigninCommand),
			conduit.NewAction(randomPipelineID),
		),
	)
	if err := ctx.Engine.RegisterSubtree(pluginpkg.SubtreeID("signin"), subtree); err != nil {
		return fmt.Errorf("register subtree: %w", err)
	}

	return nil
}

// OnStart 签到插件无需后台任务。
func (p *SigninPlugin) OnStart(_ *pluginpkg.PluginContext) error { return nil }

// OnStop 签到插件无需清理资源。
func (p *SigninPlugin) OnStop(_ *pluginpkg.PluginContext) error { return nil }

// ============================================================
// RankPlugin 排名插件
// ============================================================

// RankPlugin 实现签到积分排行榜功能。
//
// 功能：
//   - /排名 或 /rank：查询积分排行榜 Top 10
//
// 行为树：
//
//	subtree.signin_rank → Sequence(isRankCommand, Action(pipeline.signin_rank.main))
//
// 管线：
//
//	pipeline.signin_rank.main → [executePass, replyPass]
type RankPlugin struct {
	kv     *database.PluginKVStore // 受限键值存储（持久化）
	logger *zap.Logger
}

// NewRankPlugin 创建排名插件。
func NewRankPlugin(logger *zap.Logger) *RankPlugin {
	return &RankPlugin{logger: logger}
}

// Info 返回排名插件元信息。
func (p *RankPlugin) Info() pluginpkg.PluginInfo {
	return pluginpkg.PluginInfo{
		ID:          "signin_rank",
		Name:        "签到排行",
		Description: "签到积分排行榜",
		Version:     "1.0.0",
		Commands: []pluginpkg.CommandDef{
			{Name: "排名", Description: "查看签到积分排行榜"},
			{Name: "rank", Description: "查看签到积分排行榜"},
		},
		SubtreeID: pluginpkg.SubtreeID("signin_rank"),
		Tools: []pluginpkg.ToolDef{
			{
				Name:        "signin_rank",
				Description: "查询签到积分排行榜",
				Handler:     p.toolSigninRank,
			},
		},
	}
}

// OnInit 初始化排名插件，注册 Pass、Pipeline 和 Subtree。
func (p *RankPlugin) OnInit(ctx *pluginpkg.PluginContext) error {
	p.kv = ctx.KV

	// ── 注册 Pass ──
	rankExecPassID := pluginpkg.PassID("signin_rank", "execute")
	rankReplyPassID := pluginpkg.PassID("signin_rank", "reply")
	rankExecPass := &signinRankExecutePass{kv: p.kv, logger: p.logger}
	rankReplyPass := &signinRankReplyPass{}

	if err := ctx.Engine.RegisterPass(rankExecPassID, rankExecPass); err != nil {
		return fmt.Errorf("register rank execute pass: %w", err)
	}
	if err := ctx.Engine.RegisterPass(rankReplyPassID, rankReplyPass); err != nil {
		return fmt.Errorf("register rank reply pass: %w", err)
	}
	ctx.Registry.TrackPass("signin_rank", rankExecPassID)
	ctx.Registry.TrackPass("signin_rank", rankReplyPassID)

	// ── 注册管线 ──
	rankPipelineID := pluginpkg.PipelineID("signin_rank", "main")
	rankPl := conduit.NewPipelineFromIDs(
		rankPipelineID,
		rankExecPassID,
		rankReplyPassID,
	)
	if err := ctx.Engine.RegisterPipeline(rankPl); err != nil {
		return fmt.Errorf("register rank pipeline: %w", err)
	}
	ctx.Registry.TrackPipeline("signin_rank", rankPipelineID)

	// ── 注册行为树子树 ──
	subtree := conduit.NewSequence(
		conduit.NewCondition(isRankCommand),
		conduit.NewAction(rankPipelineID),
	)
	if err := ctx.Engine.RegisterSubtree(pluginpkg.SubtreeID("signin_rank"), subtree); err != nil {
		return fmt.Errorf("register rank subtree: %w", err)
	}

	return nil
}

// OnStart 排名插件无需后台任务。
func (p *RankPlugin) OnStart(_ *pluginpkg.PluginContext) error { return nil }

// OnStop 排名插件无需清理资源。
func (p *RankPlugin) OnStop(_ *pluginpkg.PluginContext) error { return nil }

// ============================================================
// 条件判断
// ============================================================

// isSigninCommand 判断消息是否为普通签到命令。
func isSigninCommand(ctx *conduit.MessageContext) bool {
	return strings.TrimSpace(ctx.RawMsg) == "/签到"
}

// isRandomSigninCommand 判断消息是否为试试手气命令。
func isRandomSigninCommand(ctx *conduit.MessageContext) bool {
	return strings.TrimSpace(ctx.RawMsg) == "/试试手气"
}

// isRankCommand 判断消息是否为排名命令。
func isRankCommand(ctx *conduit.MessageContext) bool {
	trimmed := strings.TrimSpace(ctx.RawMsg)
	return trimmed == "/排名" || trimmed == "/rank"
}

// ============================================================
// 签到结果
// ============================================================

// signinResult 签到结果，Pass 间通过 MessageContext 传递
type signinResult struct {
	TodaySigned bool   // 今日是否已签到
	Points      int    // 本次获得积分（已签到时为 0）
	TotalPoints int    // 累计总积分
	Event       string // 随机事件描述
	Rank        int    // 当前积分排名（-1 表示无排名数据）
	Mode        string // "normal" 或 "random"
}

const (
	signinResultKey = "plugin.signin.result" // MessageContext 中签到结果的键

	signinNormalPoints = 5 // 普通签到固定积分（与上游一致）

	// 受限 KV 存储键（持久化到 PostgreSQL，重启不丢）：
	//   命名空间 kvPluginID = "signin"
	//   state:<userID>:date   → 用户最后签到日期（"2006-01-02"）
	//   state:<userID>:total  → 用户累计积分
	//   leaderboard           → 积分排行榜 JSON（leaderboardEntry 数组）
	kvPluginID         = "signin"
	kvSigninStateDate  = "state:%s:date"
	kvSigninStateTotal = "state:%s:total"
	kvLeaderboardKey   = "leaderboard"
	leaderboardCap     = 100 // 排行榜最大条目数
)

// ============================================================
// 排行榜数据结构
// ============================================================

// leaderboardEntry 排行榜条目
type leaderboardEntry struct {
	UserID      string `json:"user_id"`
	Nickname    string `json:"nickname"`
	TotalPoints int    `json:"total_points"`
}

// updateLeaderboard 更新排行榜中的用户条目（持久化到受限 KV 存储）。
// kv 为 nil（未注入受限存储）时静默跳过，保证插件在测试/降级环境下不崩溃。
func updateLeaderboard(kv *database.PluginKVStore, ctx context.Context, userID, nickname string, totalPoints int) {
	if kv == nil {
		return
	}
	data, err := kv.Get(ctx, kvPluginID, kvLeaderboardKey)
	if err != nil {
		return
	}

	var entries []leaderboardEntry
	if data != "" {
		_ = json.Unmarshal([]byte(data), &entries)
	}

	// 查找并更新用户条目
	found := false
	for i := range entries {
		if entries[i].UserID == userID {
			entries[i].Nickname = nickname
			entries[i].TotalPoints = totalPoints
			found = true
			break
		}
	}
	if !found {
		entries = append(entries, leaderboardEntry{
			UserID:      userID,
			Nickname:    nickname,
			TotalPoints: totalPoints,
		})
	}

	// 按积分降序排序
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].TotalPoints > entries[j].TotalPoints
	})

	// 保留前 N 名
	if len(entries) > leaderboardCap {
		entries = entries[:leaderboardCap]
	}

	out, _ := json.Marshal(entries)
	_ = kv.Set(ctx, kvPluginID, kvLeaderboardKey, string(out))
}

// getLeaderboard 读取排行榜数据
func getLeaderboard(kv *database.PluginKVStore, ctx context.Context) []leaderboardEntry {
	if kv == nil {
		return nil
	}
	data, err := kv.Get(ctx, kvPluginID, kvLeaderboardKey)
	if err != nil || data == "" {
		return nil
	}
	var entries []leaderboardEntry
	_ = json.Unmarshal([]byte(data), &entries)
	return entries
}

// getRank 查询用户在当前排行榜中的排名（1 起），不在榜内返回 -1。
func getRank(kv *database.PluginKVStore, ctx context.Context, userID string) int {
	entries := getLeaderboard(kv, ctx)
	for i, e := range entries {
		if e.UserID == userID {
			return i + 1
		}
	}
	return -1
}

// ============================================================
// 签到事件
// ============================================================

// signinEvent 事件模板（与上游 LanMei 保持一致）
type signinEvent struct {
	Template string   // 句子模板（%s=人物，%s=动作，%v=积分）
	Persons  []string // 人物
	Acts     []string // 动作
}

// 负面事件模板
var negativeEvents = []signinEvent{
	{
		Template: "你被%s狠狠地%s了一顿，扣除了%v积分",
		Persons:  []string{"同学", "舍友", "学长", "学姐", "朋友"},
		Acts:     []string{"欺负", "吐槽", "蛐蛐"},
	},
	{
		Template: "你在和%s的%s中败下阵来，扣除了%v积分",
		Persons:  []string{"同学", "舍友", "学长", "学姐", "朋友"},
		Acts:     []string{"辩论", "讨论"},
	},
	{
		Template: "%s在背后对你进行了%s，你损失了%v积分",
		Persons:  []string{"同学", "朋友"},
		Acts:     []string{"背刺", "吐槽", "打小报告", "挂校园墙"},
	},
}

// 正面事件模板
var positiveEvents = []signinEvent{
	{
		Template: "你和%s一起%s，获得了%v积分",
		Persons:  []string{"同学", "舍友", "学长", "学姐", "朋友"},
		Acts:     []string{"原神", "三国杀", "鸣潮", "三角洲", "打瓦", "Go", "打篮球", "学习", "讨论代码"},
	},
	{
		Template: "%s偷偷给你%s，心里暖暖的，获得了%v积分",
		Persons:  []string{"舍友", "朋友", "暗恋对象"},
		Acts:     []string{"塞了糖", "送早餐", "点了外卖"},
	},
	{
		Template: "你和%s在食堂一起%s，聊得很开心，获得了%v积分",
		Persons:  []string{"朋友", "舍友", "学长", "学姐"},
		Acts:     []string{"吃饭", "分享", "打饭"},
	},
}

// randomSigninPoints 按概率生成随机签到积分
//
//	 2% 概率: -4~4  积分（可能负数）
//	78% 概率: 4~8   积分
//	18% 概率: 8~13  积分
//	 2% 概率: 11~16 积分
func randomSigninPoints() int {
	roll := rand.IntN(100) // 0~99
	switch {
	case roll < 2: // 2%: -4~4
		return rand.IntN(9) - 4 // -4 到 4
	case roll < 80: // 78%: 4~8
		return rand.IntN(5) + 4 // 4 到 8
	case roll < 98: // 18%: 8~13
		return rand.IntN(6) + 8 // 8 到 13
	default: // 2%: 11~16
		return rand.IntN(6) + 11 // 11 到 16
	}
}

// getEventByPoint 根据积分正负生成随机事件描述（与上游 LanMei 保持一致）。
// 积分非负时使用正面事件模板，为负时使用负面事件模板（积分取绝对值展示）。
func getEventByPoint(point int) string {
	var events []signinEvent
	if point >= 0 {
		events = positiveEvents
	} else {
		point = -point
		events = negativeEvents
	}
	event := events[rand.IntN(len(events))]
	person := event.Persons[rand.IntN(len(event.Persons))]
	act := event.Acts[rand.IntN(len(event.Acts))]
	return fmt.Sprintf(event.Template, person, act, point)
}

// ============================================================
// 通用签到逻辑
// ============================================================

// ensureUser 不再需要：插件私有数据统一走受限 KV 存储，
// users 表由消息主流程（角色扮演/话题）按需创建。

// nicknameFromCtx 从 MessageContext.Extra 读取用户昵称
func nicknameFromCtx(ctx *conduit.MessageContext) string {
	if raw, ok := ctx.Extra["nickname"]; ok {
		if s, ok := raw.(string); ok {
			return s
		}
	}
	return ""
}

// ============================================================
// 普通签到 Pass 实现
// ============================================================

// signinNormalExecutePass 执行普通签到逻辑：读取状态 → 固定5分 → 事件生成 → 写入状态 → 更新排行榜
type signinNormalExecutePass struct {
	kv     *database.PluginKVStore
	logger *zap.Logger
}

func (pass *signinNormalExecutePass) Execute(ctx *conduit.MessageContext) error {
	now := time.Now()
	today := now.Format("2006-01-02")

	// 从受限 KV 存储（PostgreSQL 持久化）读取签到记录
	stateDateKey := fmt.Sprintf(kvSigninStateDate, ctx.UserID)
	stateTotalKey := fmt.Sprintf(kvSigninStateTotal, ctx.UserID)
	lastDate, err := kvGet(pass.kv, ctx.Ctx, stateDateKey)
	if err != nil {
		pass.logger.Warn("signin: failed to read last sign-in date", zap.String("user", ctx.UserID), zap.Error(err))
	}
	totalPoints := kvGetInt(pass.kv, ctx.Ctx, stateTotalKey)

	// 检查今日是否已签到
	todaySigned := lastDate == today

	// 计算本次签到（固定 5 积分，与上游一致）
	var points int
	var event string
	if !todaySigned {
		points = signinNormalPoints
		totalPoints += points
		event = getEventByPoint(points)

		// 更新受限 KV 存储（持久化）
		if err := kvSet(pass.kv, ctx.Ctx, stateDateKey, today); err != nil {
			pass.logger.Error("signin: failed to save sign-in date", zap.String("user", ctx.UserID), zap.Error(err))
		}
		if err := kvSet(pass.kv, ctx.Ctx, stateTotalKey, fmt.Sprintf("%d", totalPoints)); err != nil {
			pass.logger.Error("signin: failed to save total points", zap.String("user", ctx.UserID), zap.Error(err))
		}

		// 更新排行榜
		updateLeaderboard(pass.kv, ctx.Ctx, ctx.UserID, nicknameFromCtx(ctx), totalPoints)
	}

	// 将结果写入 MessageContext
	conduit.Set(ctx, signinResultKey, &signinResult{
		TodaySigned: todaySigned,
		Points:      points,
		TotalPoints: totalPoints,
		Event:       event,
		Rank:        getRank(pass.kv, ctx.Ctx, ctx.UserID),
		Mode:        "normal",
	})

	return nil
}

// signinNormalReplyPass 组装普通签到回复消息
type signinNormalReplyPass struct{}

func (pass *signinNormalReplyPass) Execute(ctx *conduit.MessageContext) error {
	result, ok := conduit.Get[*signinResult](ctx, signinResultKey)
	if !ok {
		conduit.AppendOutput(ctx, &conduit.Message{
			UserID: ctx.UserID, GroupID: ctx.GroupID, IsGroup: ctx.IsGroup,
			Content: "签到状态异常，请重试。",
		})
		return nil
	}

	var content string
	if result.TodaySigned {
		content = fmt.Sprintf("今天已经签到过了，请明天再来吧~\n目前你积分为%d\n排名第%d位",
			result.TotalPoints, result.Rank)
	} else {
		content = fmt.Sprintf("签到成功，%s。\n目前你积分为%d\n排名第%d位",
			result.Event, result.TotalPoints, result.Rank)
	}

	conduit.AppendOutput(ctx, &conduit.Message{
		UserID: ctx.UserID, GroupID: ctx.GroupID, IsGroup: ctx.IsGroup,
		Content: content,
	})
	return nil
}

// ============================================================
// 随机签到 Pass 实现
// ============================================================

// signinRandomExecutePass 执行试试手气签到逻辑：读取状态 → 随机积分 → 事件生成 → 写入状态 → 更新排行榜
type signinRandomExecutePass struct {
	kv     *database.PluginKVStore
	logger *zap.Logger
}

func (pass *signinRandomExecutePass) Execute(ctx *conduit.MessageContext) error {
	now := time.Now()
	today := now.Format("2006-01-02")

	// 从受限 KV 存储（PostgreSQL 持久化）读取签到记录（与普通签到共享状态）
	stateDateKey := fmt.Sprintf(kvSigninStateDate, ctx.UserID)
	stateTotalKey := fmt.Sprintf(kvSigninStateTotal, ctx.UserID)
	lastDate, err := kvGet(pass.kv, ctx.Ctx, stateDateKey)
	if err != nil {
		pass.logger.Warn("signin: failed to read last sign-in date", zap.String("user", ctx.UserID), zap.Error(err))
	}
	totalPoints := kvGetInt(pass.kv, ctx.Ctx, stateTotalKey)

	// 检查今日是否已签到
	todaySigned := lastDate == today

	// 计算本次随机签到
	var points int
	var event string
	if !todaySigned {
		points = randomSigninPoints()
		totalPoints += points

		// 如果负数积分导致总积分低于 0，限制为 0
		if totalPoints < 0 {
			totalPoints = 0
		}

		// 生成随机事件描述
		event = getEventByPoint(points)

		// 更新受限 KV 存储（持久化）
		if err := kvSet(pass.kv, ctx.Ctx, stateDateKey, today); err != nil {
			pass.logger.Error("signin: failed to save sign-in date", zap.String("user", ctx.UserID), zap.Error(err))
		}
		if err := kvSet(pass.kv, ctx.Ctx, stateTotalKey, fmt.Sprintf("%d", totalPoints)); err != nil {
			pass.logger.Error("signin: failed to save total points", zap.String("user", ctx.UserID), zap.Error(err))
		}

		// 更新排行榜
		updateLeaderboard(pass.kv, ctx.Ctx, ctx.UserID, nicknameFromCtx(ctx), totalPoints)
	}

	// 将结果写入 MessageContext
	conduit.Set(ctx, signinResultKey, &signinResult{
		TodaySigned: todaySigned,
		Points:      points,
		TotalPoints: totalPoints,
		Event:       event,
		Rank:        getRank(pass.kv, ctx.Ctx, ctx.UserID),
		Mode:        "random",
	})

	return nil
}

// signinRandomReplyPass 组装试试手气签到回复消息
type signinRandomReplyPass struct{}

func (pass *signinRandomReplyPass) Execute(ctx *conduit.MessageContext) error {
	result, ok := conduit.Get[*signinResult](ctx, signinResultKey)
	if !ok {
		conduit.AppendOutput(ctx, &conduit.Message{
			UserID: ctx.UserID, GroupID: ctx.GroupID, IsGroup: ctx.IsGroup,
			Content: "签到状态异常，请重试。",
		})
		return nil
	}

	var content string
	if result.TodaySigned {
		content = fmt.Sprintf("今天已经签到过了，请明天再来吧~\n目前你积分为%d\n排名第%d位",
			result.TotalPoints, result.Rank)
	} else {
		content = fmt.Sprintf("签到成功，%s。\n目前你积分为%d\n排名第%d位",
			result.Event, result.TotalPoints, result.Rank)
	}

	conduit.AppendOutput(ctx, &conduit.Message{
		UserID: ctx.UserID, GroupID: ctx.GroupID, IsGroup: ctx.IsGroup,
		Content: content,
	})
	return nil
}

// ============================================================
// 排名 Pass 实现
// ============================================================

// rankResult 排名查询结果
type rankEntry struct {
	Rank        int    `json:"rank"`
	UserID      string `json:"user_id"`
	Nickname    string `json:"nickname"`
	TotalPoints int    `json:"total_points"`
}

const rankResultKey = "plugin.signin.rank_result"

// signinRankExecutePass 执行排名查询：从受限 KV 存储读取排行榜 → 取 Top 10
type signinRankExecutePass struct {
	kv     *database.PluginKVStore
	logger *zap.Logger
}

func (pass *signinRankExecutePass) Execute(ctx *conduit.MessageContext) error {
	entries := getLeaderboard(pass.kv, ctx.Ctx)

	// 补充昵称：如果排行榜中昵称为空，尝试从数据库获取
	top := entries
	if len(top) > 10 {
		top = top[:10]
	}

	rankEntries := make([]rankEntry, 0, len(top))
	for i, e := range top {
		nickname := e.Nickname
		if nickname == "" {
			nickname = e.UserID
		}
		rankEntries = append(rankEntries, rankEntry{
			Rank:        i + 1,
			UserID:      e.UserID,
			Nickname:    nickname,
			TotalPoints: e.TotalPoints,
		})
	}

	conduit.Set(ctx, rankResultKey, rankEntries)
	return nil
}

// signinRankReplyPass 组装排名回复消息
type signinRankReplyPass struct{}

func (pass *signinRankReplyPass) Execute(ctx *conduit.MessageContext) error {
	rankEntries, ok := conduit.Get[[]rankEntry](ctx, rankResultKey)
	if !ok || len(rankEntries) == 0 {
		conduit.AppendOutput(ctx, &conduit.Message{
			UserID: ctx.UserID, GroupID: ctx.GroupID, IsGroup: ctx.IsGroup,
			Content: "暂无签到排行数据~",
		})
		return nil
	}

	var sb strings.Builder
	sb.WriteString("🏆 签到积分排行榜\n")
	sb.WriteString("──────────────\n")
	for _, e := range rankEntries {
		// 显示昵称，按 rune 截断过长的昵称（避免按字节截断破坏 UTF-8 产生乱码）
		name := truncateRunes(e.Nickname, 10)
		sb.WriteString(fmt.Sprintf("%d. %s — %d积分\n", e.Rank, name, e.TotalPoints))
	}
	sb.WriteString("──────────────")

	conduit.AppendOutput(ctx, &conduit.Message{
		UserID: ctx.UserID, GroupID: ctx.GroupID, IsGroup: ctx.IsGroup,
		Content: sb.String(),
	})
	return nil
}

// ============================================================
// 辅助函数
// ============================================================

// kvGet 从受限 KV 存储读取字符串值；kv 为 nil 时返回空串。
func kvGet(kv *database.PluginKVStore, ctx context.Context, key string) (string, error) {
	if kv == nil {
		return "", nil
	}
	return kv.Get(ctx, kvPluginID, key)
}

// kvGetInt 从受限 KV 存储读取整数值；缺失或非法时返回 0。
func kvGetInt(kv *database.PluginKVStore, ctx context.Context, key string) int {
	val, err := kvGet(kv, ctx, key)
	if err != nil || val == "" {
		return 0
	}
	var n int
	fmt.Sscanf(val, "%d", &n)
	return n
}

// kvSet 写入受限 KV 存储；kv 为 nil 时静默跳过。
func kvSet(kv *database.PluginKVStore, ctx context.Context, key, value string) error {
	if kv == nil {
		return nil
	}
	return kv.Set(ctx, kvPluginID, key, value)
}

// truncateRunes 按 rune 截断字符串到 n 个字符，超出部分以省略号"…"结尾。
// 相比按字节截断（s[:n]），按 rune 截断不会切断多字节 UTF-8 字符导致乱码。
func truncateRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// ============================================================
// AI 工具处理器
// ============================================================

// toolSigninStatus 查询用户签到状态和积分
func (p *SigninPlugin) toolSigninStatus(ctx context.Context, argsJSON string) (string, error) {
	var args struct {
		UserID string `json:"user_id"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("参数解析失败: %w", err)
	}

	lastDate, _ := kvGet(p.kv, ctx, fmt.Sprintf(kvSigninStateDate, args.UserID))
	totalPoints := kvGetInt(p.kv, ctx, fmt.Sprintf(kvSigninStateTotal, args.UserID))

	return fmt.Sprintf("用户 %s: 最后签到日期=%s, 累计积分=%d",
		args.UserID, lastDate, totalPoints), nil
}

// toolSigninRandom 帮用户试试手气
func (p *SigninPlugin) toolSigninRandom(ctx context.Context, argsJSON string) (string, error) {
	var args struct {
		UserID string `json:"user_id"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("参数解析失败: %w", err)
	}

	now := time.Now()
	today := now.Format("2006-01-02")

	dateKey := fmt.Sprintf(kvSigninStateDate, args.UserID)
	totalKey := fmt.Sprintf(kvSigninStateTotal, args.UserID)
	lastDate, _ := kvGet(p.kv, ctx, dateKey)
	totalPoints := kvGetInt(p.kv, ctx, totalKey)

	if lastDate == today {
		return fmt.Sprintf("用户 %s 今日已签到，累计%d积分",
			args.UserID, totalPoints), nil
	}

	points := randomSigninPoints()
	totalPoints += points
	if totalPoints < 0 {
		totalPoints = 0
	}

	event := getEventByPoint(points)

	_ = kvSet(p.kv, ctx, dateKey, today)
	_ = kvSet(p.kv, ctx, totalKey, fmt.Sprintf("%d", totalPoints))
	updateLeaderboard(p.kv, ctx, args.UserID, "", totalPoints)

	return fmt.Sprintf("用户 %s 试试手气: %s (积分%+d, 累计%d积分)",
		args.UserID, event, points, totalPoints), nil
}

// toolSigninRank 查询签到积分排行榜
func (p *RankPlugin) toolSigninRank(ctx context.Context, argsJSON string) (string, error) {
	entries := getLeaderboard(p.kv, ctx)
	if len(entries) == 0 {
		return "暂无签到排行数据", nil
	}

	top := entries
	if len(top) > 10 {
		top = top[:10]
	}

	var parts []string
	for i, e := range top {
		name := e.Nickname
		if name == "" {
			name = e.UserID
		}
		parts = append(parts, fmt.Sprintf("%d. %s(%d积分)", i+1, name, e.TotalPoints))
	}

	return "签到排行榜: " + strings.Join(parts, ", "), nil
}
