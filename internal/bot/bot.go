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
)

// Bot 封装 ZeroBot + Conduit 引擎
type Bot struct {
	cfg    *zero.Config
	engine *conduit.Engine
}

// New 创建 Bot 实例，初始化 Conduit 引擎和行为树
// store 为 Conduit 状态存储（由 infra 包提供，RedisStore 用于生产，MemoryStore 用于测试）
// llmClient 为 LLM 客户端，用于意图分析（nil 时降级为纯聊天路由）
func New(cfg *config.BotConfig, cmdSys *command.System, chatSvc *ai.ChatService, db *database.DB, store conduit.StateStore, llmClient llm.LLMClient) *Bot {
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

	// ── 行为树 ──
	bt := conduit.NewBehaviorTree(
		// 优先级1：管理员命令（显式 /admin）
		conduit.NewSequence(
			conduit.NewCondition(IsAdminCommand),
			conduit.NewAction("pipeline.admin"),
		),
		// 优先级2：显式斜杠命令（/签到、/帮助 等）
		conduit.NewSequence(
			conduit.NewCondition(IsCommand),
			conduit.NewAction("pipeline.command"),
		),
		// 优先级3：意图分析（自然语言路由）
		conduit.NewAction("pipeline.intent"),
	)
	engine.SetBehaviorTree(bt)

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
		cfg:    zeroCfg,
		engine: engine,
	}
}

// Run 启动 Conduit 引擎 + ZeroBot，阻塞运行
func (b *Bot) Run() {
	b.engine.Start()
	defer b.engine.Stop()

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
