package bot

import (
	"time"

	"github.com/zrurf/conduit"
	"go.uber.org/zap"

	"github.com/DaWesen/lanmei-dream/internal/ai"
	"github.com/DaWesen/lanmei-dream/internal/ai/intent"
	"github.com/DaWesen/lanmei-dream/internal/ai/llm"
	"github.com/DaWesen/lanmei-dream/internal/ai/tool"
	"github.com/DaWesen/lanmei-dream/internal/command"
	"github.com/DaWesen/lanmei-dream/internal/config"
	"github.com/DaWesen/lanmei-dream/internal/database"
	"github.com/DaWesen/lanmei-dream/internal/gateway"
	pluginpkg "github.com/DaWesen/lanmei-dream/internal/plugin"
)

// Bot 封装 Conduit 引擎 + 网关服务 + 意图分析器引用（用于动态更新命令/工具列表）
type Bot struct {
	engine   *conduit.Engine
	plugins  *pluginpkg.Registry
	gw       *gateway.Server
	analyzer *intent.Analyzer // 意图分析器引用，供插件加载后刷新命令/工具列表
	cmdSys   *command.System  // 命令系统引用
	toolReg  *tool.Registry   // 工具注册表引用
	logger   *zap.Logger
}

// RefreshIntentAnalyzer 在插件注册新的命令或工具后同步更新意图分析器。
// 插件初始化完成后调用此方法，确保 LLM 意图分析能感知最新可用的命令和工具。
func (b *Bot) RefreshIntentAnalyzer() {
	cmdDefs := BuildIntentCommands(b.cmdSys)
	toolDefs := BuildIntentTools(b.toolReg)
	b.analyzer.UpdateCommands(cmdDefs)
	b.analyzer.UpdateTools(toolDefs)
	b.logger.Info("intent: 分析器已刷新",
		zap.Int("commands", len(cmdDefs)),
		zap.Int("tools", len(toolDefs)),
	)
}

// New 创建 Bot 实例，初始化 Conduit 引擎、行为树和插件系统。
//
// store 为 Conduit 状态存储（由 infra 包提供，RedisStore 用于生产，MemoryStore 用于测试）
// llmClient 为 LLM 客户端，用于意图分析（nil 时降级为纯聊天路由）
// pluginReg 为插件注册表（nil 时跳过插件初始化）
// gwServer 为网关服务端（反向 WS，由 gateway 包提供）
func New(cfg *config.BotConfig, cmdSys *command.System, chatSvc *ai.ChatService, db *database.DB, store conduit.StateStore, llmClient llm.LLMClient, pluginReg *pluginpkg.Registry, gwServer *gateway.Server, toolReg *tool.Registry, logger *zap.Logger) *Bot {
	nick := cfg.NickName
	if nick == "" {
		nick = "蓝妹"
	}

	// ── Conduit 引擎 ──
	engine := conduit.New(store,
		conduit.WithWorkers(4),
		conduit.WithTimeout(10*time.Second),
		conduit.WithFallbackPipeline("pipeline.fallback"),
	)

	// ── 构建意图分析器（LLM 不可用时自动降级为 IntentChat）──
	cmdDefs := BuildIntentCommands(cmdSys)
	toolDefs := BuildIntentTools(toolReg)
	analyzer := intent.NewAnalyzer(llmClient, cmdDefs, toolDefs)

	// ── 注册管线 ──
	engine.MustRegisterPipeline(conduit.NewPipeline("pipeline.admin",
		&CommandPass{CmdSys: cmdSys},
	))

	engine.MustRegisterPipeline(conduit.NewPipeline("pipeline.command",
		&CommandRouterPass{CmdSys: cmdSys},
		&ExecuteCommandPass{},
	))

	// IntentAnalysisPass 是 RouterPass — Execute 执行分析，Route 动态路由
	engine.MustRegisterPipeline(conduit.NewPipeline("pipeline.intent_analysis",
		&IntentAnalysisPass{
			Analyzer: analyzer,
			ChatSvc:  chatSvc, // nil 时 IntentChat/IntentTool 走 fallback
			logger:   logger,
		},
	))

	// 当 chatSvc 为 nil（LLM 未配置）时不注册对话管线
	if chatSvc != nil {
		engine.MustRegisterPipeline(conduit.NewPipeline("pipeline.roleplay",
			&RoleplayPass{Chat: chatSvc, DB: db},
		))
	}

	engine.MustRegisterPipeline(conduit.NewPipeline("pipeline.intent_ignore",
		&IntentIgnorePass{DB: db},
	))

	engine.MustRegisterPipeline(conduit.NewPipeline("pipeline.intent_command_exec",
		&IntentCommandExecPass{CmdSys: cmdSys},
	))

	engine.MustRegisterPipeline(conduit.NewPipeline("pipeline.fallback",
		&FallbackPass{},
	))

	// ── 行为树核心分支 ──
	coreAdmin := conduit.NewSequence(
		conduit.NewCondition(IsAdminCommand),
		conduit.NewAction("pipeline.admin"),
	)
	coreCommand := conduit.NewSequence(
		conduit.NewCondition(IsCommand),
		conduit.NewAction("pipeline.command"),
	)

	// 意图感知路由核心（非命令消息）：
	// 使用 RouterPass 简化行为树 —— Execute 执行分析，Route 动态路由到对应管线，
	// 不再需要 Condition 节点（避免 BT Tick 时分析结果尚未写入的时序问题）。
	coreIntent := conduit.NewAction("pipeline.intent_analysis")

	// ── 插件系统 ──
	if pluginReg != nil {
		pluginReg.SetEngine(engine)
		pluginReg.SetRebuildBT(func() {
			bt := buildBehaviorTree(pluginReg, coreAdmin, coreCommand, coreIntent)
			engine.SetBehaviorTree(bt)
		})
	}

	bt := buildBehaviorTree(pluginReg, coreAdmin, coreCommand, coreIntent)
	engine.SetBehaviorTree(bt)

	return &Bot{
		engine:   engine,
		plugins:  pluginReg,
		gw:       gwServer,
		analyzer: analyzer,
		cmdSys:   cmdSys,
		toolReg:  toolReg,
		logger:   logger,
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

// Run 启动 Conduit 引擎 + 网关服务，阻塞运行
func (b *Bot) Run() {
	b.engine.Start()
	defer func() { _ = b.engine.Stop() }()

	if err := b.gw.Run(); err != nil {
		b.logger.Fatal("gateway: 服务运行失败", zap.Error(err))
	}
}

// OnMessage 实现 gateway.EventHandler 接口，将网关消息转为 Conduit 输入
func (b *Bot) OnMessage(msg *gateway.NormalizedMessage) {
	if msg.Content == "" {
		return
	}

	input := &conduit.InputMessage{
		UserID:  msg.UserID,
		GroupID: msg.GroupID,
		Content: msg.Content,
		IsGroup: msg.IsGroup,
		Extra: map[string]any{
			KeyPlatform:       string(msg.Platform),
			KeyPlatformUserID: msg.UserID,
			KeyNickname:       msg.SenderName,
			KeyMessageID:      msg.MessageID,
			KeyConnID:         msg.ConnID,
			KeySelfID:         msg.SelfID,
		},
	}

	// 同步处理，直接拿到结果
	result, err := b.engine.Process(input)
	if err != nil {
		b.logger.Error("conduit: process failed", zap.Error(err))
		b.reply(msg, "蓝妹现在有点迷糊，稍后再试~")
		return
	}

	// 发送所有输出消息
	for _, out := range result.Output {
		b.reply(msg, out.Content)
	}
}

// reply 通过网关回复消息
func (b *Bot) reply(msg *gateway.NormalizedMessage, text string) {
	if err := b.gw.Hub().SendMessageTo(msg.ConnID, msg, text); err != nil {
		b.logger.Error("bot: 回复失败", zap.String("conn", msg.ConnID), zap.Error(err))
	}
}
