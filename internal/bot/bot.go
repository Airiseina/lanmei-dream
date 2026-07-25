package bot

import (
	"log"
	"strconv"
	"time"

	zero "github.com/wdvxdr1123/ZeroBot"
	"github.com/wdvxdr1123/ZeroBot/driver"
	"github.com/zrurf/conduit"

	"github.com/DaWesen/lanmei-dream/internal/ai"
	"github.com/DaWesen/lanmei-dream/internal/ai/llm"
	"github.com/DaWesen/lanmei-dream/internal/command"
	"github.com/DaWesen/lanmei-dream/internal/config"
	"github.com/DaWesen/lanmei-dream/internal/database"
	pluginpkg "github.com/DaWesen/lanmei-dream/internal/plugin"
)

// Bot 封装 ZeroBot + Conduit 引擎
type Bot struct {
	cfg     *zero.Config
	engine  *conduit.Engine
	plugins *pluginpkg.Registry
}

// New 创建 Bot 实例，初始化 Conduit 引擎、行为树和插件系统。
//
// store 为 Conduit 状态存储（由 infra 包提供，RedisStore 用于生产，MemoryStore 用于测试）
// llmClient 为 LLM 客户端，用于意图分析（nil 时降级为纯聊天路由）
// pluginReg 为插件注册表（nil 时跳过插件初始化）
func New(cfg *config.BotConfig, cmdSys *command.System, chatSvc *ai.ChatService, db *database.DB, store conduit.StateStore, llmClient llm.LLMClient, pluginReg *pluginpkg.Registry) *Bot {
	nick := cfg.NickName
	if nick == "" {
		nick = "蓝妹"
	}

	superUsers := cfg.ParseSuperUsers()

	// ── Conduit 引擎 ──
	engine := conduit.New(store,
		conduit.WithWorkers(4),
		conduit.WithTimeout(10*time.Second),
		conduit.WithFallbackPipeline("pipeline.fallback"),
	)

	// ── 注册管线 ──
	engine.MustRegisterPipeline(conduit.NewPipeline("pipeline.admin",
		&CommandPass{CmdSys: cmdSys},
	))

	engine.MustRegisterPipeline(conduit.NewPipeline("pipeline.command",
		&CommandPass{CmdSys: cmdSys},
	))

	// 意图分析管线：LLM 判断 → command / chat / ignore
	engine.MustRegisterPipeline(conduit.NewPipeline("pipeline.intent",
		NewIntentPass(llmClient, cmdSys, chatSvc, db),
	))

	engine.MustRegisterPipeline(conduit.NewPipeline("pipeline.fallback",
		&FallbackPass{},
	))

	// ── 行为树（核心分支） ──
	coreAdmin := conduit.NewSequence(
		conduit.NewCondition(IsAdminCommand),
		conduit.NewAction("pipeline.admin"),
	)
	coreCommand := conduit.NewSequence(
		conduit.NewCondition(IsCommand),
		conduit.NewAction("pipeline.command"),
	)
	coreIntent := conduit.NewAction("pipeline.intent")

	// ── 插件系统 ──
	if pluginReg != nil {
		// 注入引擎到插件注册表（供插件注册 Pass/Pipeline/Subtree）
		pluginReg.SetEngine(engine)
		// 设置行为树重建回调
		pluginReg.SetRebuildBT(func() {
			bt := buildBehaviorTree(pluginReg, coreAdmin, coreCommand, coreIntent)
			engine.SetBehaviorTree(bt)
		})
	}

	// 构建初始行为树（插件子树优先级高于核心分支）
	bt := buildBehaviorTree(pluginReg, coreAdmin, coreCommand, coreIntent)
	engine.SetBehaviorTree(bt)

	// ── ZeroBot 配置 ──
	zeroCfg := &zero.Config{
		NickName:      []string{nick},
		CommandPrefix: "/",
		SuperUsers:    superUsers,
		Driver: []zero.Driver{
			driver.NewWebSocketClient(cfg.WebSocketURL, cfg.AccessToken),
		},
	}

	return &Bot{
		cfg:     zeroCfg,
		engine:  engine,
		plugins: pluginReg,
	}
}

// buildBehaviorTree 构建主行为树。
//
// 结构：
//
//	Selector [
//	  SubtreeRef("plugin.signin.subtree")    // 插件子树（优先级最高）
//	  SubtreeRef("plugin.festival.subtree")  // ...
//	  SubtreeRef("plugin.minigame.subtree")  // ...
//	  Sequence [IsAdminCommand, Action(admin)]  // 管理员命令
//	  Sequence [IsCommand, Action(command)]     // 斜杠命令
//	  Action(intent)                            // 意图分析（兜底）
//	]
func buildBehaviorTree(pluginReg *pluginpkg.Registry, coreAdmin, coreCommand, coreIntent conduit.BTNode) *conduit.Selector {
	var branches []conduit.BTNode

	// 插件子树优先（最先匹配，最高优先级）
	if pluginReg != nil {
		for _, ref := range pluginReg.SubtreeRefs() {
			branches = append(branches, ref)
		}
	}

	// 核心分支
	branches = append(branches, coreAdmin, coreCommand, coreIntent)

	return conduit.NewBehaviorTree(branches...)
}

// Run 启动 Conduit 引擎 + 插件 + ZeroBot，阻塞运行
func (b *Bot) Run() {
	b.engine.Start()
	defer func() { _ = b.engine.Stop() }()

	zero.OnMessage().Handle(b.handleMessage)

	zero.RunAndBlock(b.cfg, nil)
}

// handleMessage 把 ZeroBot 消息转为 Conduit InputMessage，同步处理后回复
func (b *Bot) handleMessage(ctx *zero.Ctx) {
	msg := ctx.ExtractPlainText()
	if msg == "" {
		msg = ctx.Event.RawMessage
	}
	if msg == "" {
		return
	}

	qqID := strconv.FormatInt(ctx.Event.UserID, 10)
	groupID := ""
	isGroup := ctx.Event.GroupID != 0
	if isGroup {
		groupID = strconv.FormatInt(ctx.Event.GroupID, 10)
	}

	input := &conduit.InputMessage{
		UserID:  qqID,
		GroupID: groupID,
		Content: msg,
		IsGroup: isGroup,
		Extra: map[string]any{
			KeyQQUserID: ctx.Event.UserID,
			KeyNickname: ctx.Event.Sender.NickName,
		},
	}

	// 同步处理，直接拿到结果
	result, err := b.engine.Process(input)
	if err != nil {
		log.Printf("conduit: process failed: %v", err)
		ctx.Send("蓝妹现在有点迷糊，稍后再试~")
		return
	}

	// 发送所有输出消息
	for _, out := range result.Output {
		ctx.Send(out.Content)
	}
}
