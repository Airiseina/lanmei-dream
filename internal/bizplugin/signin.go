package bizplugin

import (
	"context"
	"encoding/json"
	"fmt"
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

// SigninPlugin 实现每日签到功能。
//
// 功能：
//   - 用户每日签到，获取积分奖励
//   - 连续签到天数累积，中断则重新计算
//   - 通过 Conduit 行为树子树实现消息路由
//
// 行为树：
//
//	subtree.signin → Sequence(IsSigninCommand, Action("pipeline.plugin.signin"))
//
// 管线（动态模式，支持运行时热替换）：
//
//	pipeline.plugin.signin → [executePass, replyPass]
type SigninPlugin struct {
	db     *database.DB
	store  conduit.StateStore
	logger *zap.Logger
}

// NewSigninPlugin 创建签到插件。
func NewSigninPlugin(db *database.DB, logger *zap.Logger) *SigninPlugin {
	return &SigninPlugin{db: db, logger: logger}
}

// Info 返回签到插件元信息。
func (p *SigninPlugin) Info() pluginpkg.PluginInfo {
	return pluginpkg.PluginInfo{
		ID:          "signin",
		Name:        "签到",
		Description: "每日试试手气",
		Version:     "1.0.0",
		Commands: []pluginpkg.CommandDef{
			{Name: "签到", Description: "每日试试手气"},
		},
		SubtreeID: pluginpkg.SubtreeID("signin"),
		Tools: []pluginpkg.ToolDef{
			{
				Name:        "signin_status",
				Description: "查询用户签到状态和积分",
				Handler:     p.toolSigninStatus,
			},
		},
	}
}

// OnInit 初始化签到插件，注册 Pass、Pipeline 和 Subtree。
func (p *SigninPlugin) OnInit(ctx *pluginpkg.PluginContext) error {
	p.db = ctx.DB
	p.store = ctx.Store

	// 注册 Pass（依赖直接注入 Pass 结构体）
	executePassID := pluginpkg.PassID("signin", "execute")
	replyPassID := pluginpkg.PassID("signin", "reply")

	executePass := &signinExecutePass{db: p.db, store: p.store, logger: p.logger}
	replyPass := &signinReplyPass{}

	if err := ctx.Engine.RegisterPass(executePassID, executePass); err != nil {
		return fmt.Errorf("register execute pass: %w", err)
	}
	if err := ctx.Engine.RegisterPass(replyPassID, replyPass); err != nil {
		return fmt.Errorf("register reply pass: %w", err)
	}

	// 跟踪 Pass，卸载时自动清理
	ctx.Registry.TrackPass("signin", executePassID)
	ctx.Registry.TrackPass("signin", replyPassID)

	// 注册动态管线（通过 Pass ID 引用，支持运行时热替换）
	pipelineID := pluginpkg.PipelineID("signin", "main")
	pl := conduit.NewPipelineFromIDs(
		pipelineID,
		executePassID,
		replyPassID,
	)
	if err := ctx.Engine.RegisterPipeline(pl); err != nil {
		return fmt.Errorf("register pipeline: %w", err)
	}

	// 跟踪 Pipeline，卸载时自动清理
	ctx.Registry.TrackPipeline("signin", pipelineID)

	// 注册行为树子树：签到命令路由
	subtree := conduit.NewSequence(
		conduit.NewCondition(isSigninCommand),
		conduit.NewAction(pipelineID),
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
// 条件判断
// ============================================================

// isSigninCommand 判断消息是否为签到命令。
func isSigninCommand(ctx *conduit.MessageContext) bool {
	return strings.TrimSpace(ctx.RawMsg) == "/签到"
}

// ============================================================
// Pass 实现
// ============================================================

// signinResult 签到结果，Pass 间通过 MessageContext 传递
type signinResult struct {
	TodaySigned bool // 今日是否已签到
	StreakDays  int  // 连续签到天数（含今日）
	Points      int  // 本次获得积分（已签到时为 0）
	TotalPoints int  // 累计总积分
}

const (
	signinResultKey = "plugin.signin.result" // MessageContext 中签到结果的键

	signinBasePoints    = 10 // 基础签到积分
	signinStreakBonus   = 2  // 连续签到每天额外积分
	signinMaxStreakDays = 10 // 连续签到额外积分上限对应天数
)

// signinExecutePass 执行签到逻辑：读取状态 → 计算积分 → 写入状态
type signinExecutePass struct {
	db     *database.DB
	store  conduit.StateStore
	logger *zap.Logger
}

func (pass *signinExecutePass) Execute(ctx *conduit.MessageContext) error {
	now := time.Now()
	today := now.Format("2006-01-02")
	yesterday := now.AddDate(0, 0, -1).Format("2006-01-02")

	// 确保用户存在
	if pass.db != nil {
		platform, _ := conduit.Get[string](ctx, "platform")
		if platform == "" {
			platform = "unknown"
		}
		_, _ = pass.db.GetOrCreateUser(ctx.Ctx, platform, ctx.UserID, "")
	}

	// 从 StateStore 读取签到记录
	stateKey := pluginpkg.StoreKey("signin", "state:"+ctx.UserID)
	lastDate, err := pass.store.Get(ctx.Ctx, stateKey+":date")
	if err != nil {
		pass.logger.Warn("signin: failed to read last sign-in date", zap.String("user", ctx.UserID), zap.Error(err))
	}
	streakDays := storeGetInt(pass.store, ctx.Ctx, stateKey+":streak")
	totalPoints := storeGetInt(pass.store, ctx.Ctx, stateKey+":total")

	// 检查今日是否已签到
	todaySigned := lastDate == today

	// 检查连续性：如果上次签到不是昨天也不是今天，重置连续天数
	if lastDate != "" && lastDate != today && lastDate != yesterday {
		streakDays = 0
	}

	// 计算本次签到
	var points int
	if !todaySigned {
		streakDays++
		bonus := min(streakDays-1, signinMaxStreakDays) * signinStreakBonus
		points = signinBasePoints + bonus
		totalPoints += points

		// 更新 StateStore
		if err := pass.store.Set(ctx.Ctx, stateKey+":date", today, 0); err != nil {
			pass.logger.Error("signin: failed to save sign-in date", zap.String("user", ctx.UserID), zap.Error(err))
		}
		if err := pass.store.Set(ctx.Ctx, stateKey+":streak", fmt.Sprintf("%d", streakDays), 0); err != nil {
			pass.logger.Error("signin: failed to save streak days", zap.String("user", ctx.UserID), zap.Error(err))
		}
		if err := pass.store.Set(ctx.Ctx, stateKey+":total", fmt.Sprintf("%d", totalPoints), 0); err != nil {
			pass.logger.Error("signin: failed to save total points", zap.String("user", ctx.UserID), zap.Error(err))
		}
	}

	// 将结果写入 MessageContext，供 replyPass 使用
	conduit.Set(ctx, signinResultKey, &signinResult{
		TodaySigned: todaySigned,
		StreakDays:  streakDays,
		Points:      points,
		TotalPoints: totalPoints,
	})

	return nil
}

// signinReplyPass 组装签到回复消息
type signinReplyPass struct{}

func (pass *signinReplyPass) Execute(ctx *conduit.MessageContext) error {
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
		content = fmt.Sprintf("今日已签到~\n连续签到: %d天\n累计积分: %d",
			result.StreakDays, result.TotalPoints)
	} else {
		content = fmt.Sprintf("签到成功！\n连续签到: %d天\n本次积分: +%d\n累计积分: %d",
			result.StreakDays, result.Points, result.TotalPoints)
	}

	conduit.AppendOutput(ctx, &conduit.Message{
		UserID: ctx.UserID, GroupID: ctx.GroupID, IsGroup: ctx.IsGroup,
		Content: content,
	})
	return nil
}

// ============================================================
// 辅助函数
// ============================================================

// parseUserID64 将字符串用户 ID 解析为 int64
// 已废弃，仅用于向后兼容
func parseUserID64(s string) int64 {
	var id int64
	fmt.Sscanf(s, "%d", &id)
	return id
}

// storeGetInt 从 StateStore 读取整数值
func storeGetInt(store conduit.StateStore, ctx context.Context, key string) int {
	val, err := store.Get(ctx, key)
	if err != nil || val == "" {
		return 0
	}
	var n int
	fmt.Sscanf(val, "%d", &n)
	return n
}

// toolSigninStatus 是 AI 工具处理器，查询用户签到状态和积分。
func (p *SigninPlugin) toolSigninStatus(ctx context.Context, argsJSON string) (string, error) {
	var args struct {
		UserID string `json:"user_id"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("参数解析失败: %w", err)
	}

	stateKey := pluginpkg.StoreKey("signin", "state:"+args.UserID)
	lastDate, _ := p.store.Get(ctx, stateKey+":date")
	streakDays := storeGetInt(p.store, ctx, stateKey+":streak")
	totalPoints := storeGetInt(p.store, ctx, stateKey+":total")

	return fmt.Sprintf("用户 %s: 最后签到日期=%s, 连续天数=%d, 累计积分=%d",
		args.UserID, lastDate, streakDays, totalPoints), nil
}
