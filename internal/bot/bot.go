package bot

import (
	"errors"
	"math/rand/v2"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

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
	"github.com/DaWesen/lanmei-dream/internal/media"
	pluginpkg "github.com/DaWesen/lanmei-dream/internal/plugin"
	"github.com/DaWesen/lanmei-dream/internal/topic"
)

// Bot 封装 Conduit 引擎 + 网关服务 + 意图分析器引用（用于动态更新命令/工具列表）
type Bot struct {
	engine        *conduit.Engine
	plugins       *pluginpkg.Registry
	gw            *gateway.Server
	analyzer      *intent.Analyzer // 意图分析器引用，供插件加载后刷新命令/工具列表
	cmdSys        *command.System  // 命令系统引用
	toolReg       *tool.Registry   // 工具注册表引用
	dedup         *Deduper         // 消息去重（message_id SETNX）
	typingSpeedMS int              // 打字速度（毫秒/字），0 禁用间隔
	minIntervalMS int              // 最小发送间隔（毫秒）
	maxIntervalMS int              // 最大发送间隔（毫秒），0 不限
	jitterPct     float64          // 间隔抖动比例（0.0-1.0）
	logger        *zap.Logger
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

// MediaDeps 多媒体处理依赖（可选注入，nil 时媒体管线降级工作）。
type MediaDeps struct {
	Store  *media.ObjectStore // RustFS 对象存储（nil 时跳过媒体缓存）
	Vision *ai.VisionService  // 视觉理解（nil 时图片仅缓存不描述）
}

// New 创建 Bot 实例，初始化 Conduit 引擎、行为树和插件系统。
//
// store 为 Conduit 状态存储（由 infra 包提供，RedisStore 用于生产，MemoryStore 用于测试）
// llmClient 为 LLM 客户端，用于意图分析（nil 时降级为纯聊天路由）
// pluginReg 为插件注册表（nil 时跳过插件初始化）
// gwServer 为网关服务端（反向 WS，由 gateway 包提供）
// mediaDeps 为多媒体处理依赖（nil 时媒体管线降级，仅记录）
// topicMgr 为群聊话题管理器（nil 时 topic 系统未启用，群聊退化为全量意图分析）
func New(cfg *config.BotConfig, cmdSys *command.System, chatSvc *ai.ChatService, db *database.DB, store conduit.StateStore, llmClient llm.LLMClient, pluginReg *pluginpkg.Registry, gwServer *gateway.Server, toolReg *tool.Registry, logger *zap.Logger, mediaDeps *MediaDeps, topicMgr *topic.Manager) *Bot {
	nick := cfg.NickName
	if nick == "" {
		nick = "蓝妹"
	}

	// ── Conduit 引擎 ──
	// 超时 20s：意图分析/命令等慢路径（如 LLM 慢响应）在超时前完成，
	// 避免"慢 LLM 调用超时被丢弃"导致群聊消息静默。
	engine := conduit.New(store,
		conduit.WithWorkers(4),
		conduit.WithTimeout(20*time.Second),
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
			&RoleplayStreamPass{Chat: chatSvc, DB: db, Logger: logger, TopicMgr: topicMgr},
		))
	}

	// 流式段落交付管线（每个段落作为子消息重入引擎后走此管线）
	engine.MustRegisterPipeline(conduit.NewPipeline("pipeline.roleplay_segment",
		&RoleplaySegmentPass{},
	))

	engine.MustRegisterPipeline(conduit.NewPipeline("pipeline.intent_ignore",
		&IntentIgnorePass{DB: db},
	))

	engine.MustRegisterPipeline(conduit.NewPipeline("pipeline.intent_command_exec",
		&IntentCommandExecPass{CmdSys: cmdSys},
	))

	// ── 互动事件预留节点（具体逻辑由插件子树实现）──
	engine.MustRegisterPipeline(conduit.NewPipeline("pipeline.notice",
		&NoticeGatePass{Logger: logger},
	))

	// ── 多媒体处理管线（下载/缓存/理解 → RouterPass 路由）──
	var mediaStore *media.ObjectStore
	var visionSvc *ai.VisionService
	if mediaDeps != nil {
		mediaStore = mediaDeps.Store
		visionSvc = mediaDeps.Vision
	}
	engine.MustRegisterPipeline(conduit.NewPipeline("pipeline.media",
		&MediaPass{Store: mediaStore, Vision: visionSvc, DB: db, Cfg: &cfg.Media, Logger: logger},
		&MediaRouterPass{},
	))

	engine.MustRegisterPipeline(conduit.NewPipeline("pipeline.fallback",
		&FallbackPass{},
	))

	// ── 群聊话题管线（选择性放行；topicMgr 为 nil 时 TopicGatePass 全放行）──
	engine.MustRegisterPipeline(conduit.NewPipeline("pipeline.topic_gate",
		&TopicGatePass{Manager: topicMgr, Analyzer: analyzer, Logger: logger},
	))
	engine.MustRegisterPipeline(conduit.NewPipeline("pipeline.topic_ignore",
		&TopicIgnorePass{DB: db},
	))

	// ── 行为树核心分支 ──
	// 段落分支优先级最高：流式段落重入消息直接走交付管线，不经过意图分析
	coreSegment := conduit.NewSequence(
		conduit.NewCondition(IsSegment),
		conduit.NewAction("pipeline.roleplay_segment"),
	)
	// 互动事件分支：notice/request 事件路由到预留节点（插件子树可先行消费）
	coreNotice := conduit.NewSequence(
		conduit.NewCondition(IsNotice),
		conduit.NewAction("pipeline.notice"),
	)
	coreAdmin := conduit.NewSequence(
		conduit.NewCondition(IsAdminCommand),
		conduit.NewAction("pipeline.admin"),
	)
	coreCommand := conduit.NewSequence(
		conduit.NewCondition(IsCommand),
		conduit.NewAction("pipeline.command"),
	)
	// 多媒体分支：含图片/音频/视频/文件段的消息先经 pipeline.media 处理
	// （MediaRouterPass 内部再路由到 intent_analysis / intent_ignore）
	coreMedia := conduit.NewSequence(
		conduit.NewCondition(IsMedia),
		conduit.NewAction("pipeline.media"),
	)

	// 意图感知路由核心（非命令消息）：
	// 群聊先经 pipeline.topic_gate 做话题决策（RouterPass 路由到 intent_analysis / topic_ignore），
	// 私聊由 TopicGatePass 直接放行到 intent_analysis。
	// 使用 RouterPass 简化行为树 —— Execute 执行分析，Route 动态路由到对应管线，
	// 不再需要 Condition 节点（避免 BT Tick 时分析结果尚未写入的时序问题）。
	coreIntent := conduit.NewAction("pipeline.topic_gate")

	// ── 插件系统 ──
	if pluginReg != nil {
		pluginReg.SetEngine(engine)
		pluginReg.SetRebuildBT(func() {
			bt := buildBehaviorTree(pluginReg, coreAdmin, coreCommand, coreIntent, coreSegment, coreNotice, coreMedia)
			engine.SetBehaviorTree(bt)
		})
	}

	bt := buildBehaviorTree(pluginReg, coreAdmin, coreCommand, coreIntent, coreSegment, coreNotice, coreMedia)
	engine.SetBehaviorTree(bt)

	return &Bot{
		engine:        engine,
		plugins:       pluginReg,
		gw:            gwServer,
		analyzer:      analyzer,
		cmdSys:        cmdSys,
		toolReg:       toolReg,
		dedup:         NewDeduper(store, 0),
		typingSpeedMS: cfg.Stream.TypingSpeedMS,
		minIntervalMS: cfg.Stream.MinIntervalMS,
		maxIntervalMS: cfg.Stream.MaxIntervalMS,
		jitterPct:     cfg.Stream.JitterPct,
		logger:        logger,
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
//	  Sequence [IsSegment, Action(segment)]  // 流式段落重入（优先于命令/意图）
//	  Sequence [IsNotice, Action(notice)]    // 互动事件（预留节点）
//	  Sequence [IsAdminCommand, Action(admin)]  // 管理员命令
//	  Sequence [IsCommand, Action(command)]  // 斜杠命令
//	  Sequence [IsMedia, Action(media)]      // 多媒体（内部路由到意图分析/忽略）
//	  Action(intent)                         // 意图分析（兜底）
//	]
func buildBehaviorTree(pluginReg *pluginpkg.Registry, coreAdmin, coreCommand, coreIntent, coreSegment, coreNotice, coreMedia conduit.BTNode) conduit.BTNode {
	var branches []conduit.BTNode

	// 插件子树优先（最先匹配，最高优先级）
	if pluginReg != nil {
		for _, ref := range pluginReg.SubtreeRefs() {
			branches = append(branches, ref)
		}
	}

	// 段落分支优先于命令/意图（流式段落直接交付，不走分析）
	branches = append(branches, coreSegment)

	// 核心分支：事件 → 管理员命令 → 斜杠命令 → 媒体 → 意图分析（兜底）
	branches = append(branches, coreNotice, coreAdmin, coreCommand, coreMedia, coreIntent)

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

// OnMessage 实现 gateway.EventHandler 接口，将网关消息转为 Conduit 输入。
// 使用异步 Submit + ResponseCallback，避免阻塞网关事件处理。
//
// 入口职责：
//  1. 空文本消息过滤（但含媒体段或为事件的消息放行）
//  2. message_id 去重（Deduper）
//  3. 将完整事件上下文（含多模态段 / notice 信息）注入 InputMessage.Extra
//
// 通知事件（notice/request）无文本内容，与普通消息分流：
// 事件信息经 Extra 写入黑板供插件消费；事件不产生回复，出错也保持静默。
func (b *Bot) OnMessage(msg *gateway.NormalizedMessage) {
	if msg == nil {
		return
	}
	// 普通消息必须非空（纯图片/事件消息无文本时放行，交给媒体/事件管线处理）
	if msg.MessageType == gateway.MessageTypeMessage && msg.Content == "" && len(msg.Segments) == 0 {
		return
	}
	// ── 消息去重：重复 message_id 直接丢弃（存储故障时放行）──
	if b.dedup != nil && !b.dedup.Accept(msg) {
		b.logger.Debug("bot: 重复消息已丢弃",
			zap.String("conn", msg.ConnID), zap.String("message_id", msg.MessageID))
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
			// ── 事件输入（只读 Extra）──
			KeyMessageType:  msg.MessageType,
			KeySegments:     msg.Segments,
			KeyMimeTypes:    msg.MimeTypes,
			KeyAtTargets:    msg.AtTargets,
			KeyEventType:    msg.EventType,
			KeyEventSubType: msg.EventSubType,
			KeyEventData:    msg.EventData,
		},
	}
	// 事件消息（notice/request）：出错也绝不向群里发消息（事件落空发"迷糊话术"是 bug）
	isEvent := msg.MessageType == gateway.MessageTypeNotice || msg.MessageType == gateway.MessageTypeRequest
	if isEvent {
		input.ResponseCallback = b.makeEventCallback(msg)
	} else {
		input.ResponseCallback = b.makeResponseCallback(msg)
	}

	if err := b.engine.Submit(input); err != nil {
		b.logger.Error("conduit: submit failed", zap.Error(err))
		if !isEvent {
			b.reply(msg, "蓝妹现在有点迷糊，稍后再试~")
		}
	}
}

// makeEventCallback 构造通知事件专用回调：事件处理出错时只记日志、绝不回复；
// 正常完成时遍历 Output 发送（供未来事件消费插件写入输出，如入群欢迎）。
func (b *Bot) makeEventCallback(msg *gateway.NormalizedMessage) func(*conduit.MessageContext, error) {
	return func(ctx *conduit.MessageContext, err error) {
		if err != nil {
			b.logger.Error("bot: event process failed",
				zap.String("event", msg.EventType),
				zap.String("group", msg.GroupID),
				zap.Error(err),
			)
			return
		}
		for _, out := range ctx.Output {
			b.reply(msg, out.Content)
		}
	}
}

// makeResponseCallback 构造消息级回调，处理两种情况：
//   - 正常完成（未 yield）：遍历 ctx.Output 逐条发送
//   - 流式挂起（yield）：启动 goroutine 消费段落通道，逐条重入引擎投递
func (b *Bot) makeResponseCallback(msg *gateway.NormalizedMessage) func(*conduit.MessageContext, error) {
	return func(ctx *conduit.MessageContext, err error) {
		if err != nil {
			b.logger.Error("conduit: process failed", zap.Error(err))
			b.reply(msg, "蓝妹现在有点迷糊，稍后再试~")
			return
		}
		if conduit.IsYielded(ctx) {
			// 流式回复：启动 goroutine 逐条投递段落
			go b.streamSegments(ctx, msg)
			return
		}
		// 正常回复：发送所有输出消息
		for _, out := range ctx.Output {
			b.reply(msg, out.Content)
		}
	}
}

// streamSegments 消费段落通道，逐条创建子消息重入引擎。
//
// 每个段落通过 NewChildInput 派生子消息，标记 KeyIsSegment 后 Submit 到引擎。
// 段落顺序投递：前一段的回调完成后才提交下一段（<-done），保证天然时序。
// 段落间发送间隔由 calcSegmentInterval 按下一段字数动态计算，模拟真人打字节奏，
// 避免 QQ 等平台短时间内快速发送消息导致乱序。
// 流式 goroutine 关闭通道后，range 循环自然退出。
func (b *Bot) streamSegments(ctx *conduit.MessageContext, msg *gateway.NormalizedMessage) {
	segCh, ok := conduit.Get[chan string](ctx, KeyStreamChannel)
	if !ok {
		b.logger.Error("bot: stream channel not found in context")
		b.reply(msg, "蓝妹现在有点迷糊，稍后再试~")
		return
	}

	first := true
	for segment := range segCh {
		// 首段无延迟（LLM 生成期间已"打字"完毕）；
		// 后续段落等待打字间隔，模拟真人逐段输入的节奏。
		if !first {
			if d := b.calcSegmentInterval(segment); d > 0 {
				time.Sleep(d)
			}
		}
		first = false

		child := ctx.NewChildInput(segment)
		child.Extra[KeyIsSegment] = true

		// 段落回调：发送该段输出，完成后关闭 done 通知主循环
		done := make(chan struct{})
		child.ResponseCallback = func(childCtx *conduit.MessageContext, childErr error) {
			defer close(done)
			if childErr != nil {
				b.logger.Error("bot: segment delivery failed", zap.Error(childErr))
				return
			}
			for _, out := range childCtx.Output {
				b.reply(msg, out.Content)
			}
		}

		if err := b.engine.Submit(child); err != nil {
			b.logger.Error("bot: submit segment failed", zap.Error(err))
			close(done) // 不阻塞，继续下一段
			if errors.Is(err, conduit.ErrEngineStopped) {
				return // 引擎已停止，无需继续
			}
		}

		// 顺序等待：前一段发送完成后才投递下一段
		<-done
	}
}

// calcSegmentInterval 根据段落文本长度计算发送间隔。
//
// 算法：基础间隔 = 字数 × typingSpeedMS，
// 先叠加 ±jitterPct 的随机抖动（模拟真人打字的不均匀节奏），
// 再 clamp 到 [minIntervalMS, maxIntervalMS]，
// 保证抖动后仍不超过上限（长消息不会被无限拖慢）。
//
// 返回 0 表示不延迟（typingSpeedMS 为 0 时禁用整个间隔机制）。
func (b *Bot) calcSegmentInterval(text string) time.Duration {
	if b.typingSpeedMS <= 0 {
		return 0
	}

	charCount := utf8.RuneCountInString(text)
	baseMS := charCount * b.typingSpeedMS

	// 叠加抖动：实际间隔 = base × (1 + uniform[-jitter, +jitter])
	if b.jitterPct > 0 {
		jitter := (rand.Float64()*2 - 1) * b.jitterPct // [-jitterPct, +jitterPct]
		baseMS = int(float64(baseMS) * (1 + jitter))
	}

	// clamp 到最小值
	if b.minIntervalMS > 0 && baseMS < b.minIntervalMS {
		baseMS = b.minIntervalMS
	}
	// clamp 到最大值（0 表示不设上限）
	if b.maxIntervalMS > 0 && baseMS > b.maxIntervalMS {
		baseMS = b.maxIntervalMS
	}

	return time.Duration(baseMS) * time.Millisecond
}

// reply 通过网关回复消息。
// 回复内容为纯 http(s) URL 时按图片消息发送（富媒体段），供 cat/balogo/github_card 等图片类插件使用。
func (b *Bot) reply(msg *gateway.NormalizedMessage, text string) {
	// 剔除 markdown 代码块围栏（``` 边界行）：QQ 等平台不支持 markdown 渲染，
	// 直接发送代码原文，避免把 ``` 符号原样发给用户。
	text = stripCodeFences(text)

	if isImageURL(text) {
		// 纯 URL → 图片消息（走上游富媒体发送体系）
		if err := b.gw.Hub().SendSegments(msg.ConnID, msg, []gateway.NormalizedSegment{{
			Type: "image",
			Data: map[string]string{"url": strings.TrimSpace(text)},
		}}); err != nil {
			b.logger.Error("bot: 图片回复失败", zap.String("conn", msg.ConnID), zap.Error(err))
		}
		return
	}
	if err := b.gw.Hub().SendMessageTo(msg.ConnID, msg, text); err != nil {
		b.logger.Error("bot: 回复失败", zap.String("conn", msg.ConnID), zap.Error(err))
	}
}

// codeFenceRe 匹配 markdown 代码块围栏（``` 起始行 → ``` 结束行）。
// 兼容：开围栏可选语言标注；闭围栏可缺失（LLM 漏写结尾时从开围栏处剔除到末尾）。
var codeFenceRe = regexp.MustCompile("```[^\n]*\n?([\\s\\S]*?)\n?(?:```|\\z)")

// stripCodeFences 剔除文本中的 markdown 代码块围栏行，仅保留代码内容。
// 例如 "```go\nfmt.Println(\"hi\")\n```" → "fmt.Println(\"hi\")"。
// 代码块与后续文字之间的换行保留；未闭合代码块（LLM 漏写结尾 ```）原样保留内容。
func stripCodeFences(text string) string {
	return codeFenceRe.ReplaceAllStringFunc(text, func(m string) string {
		idx := codeFenceRe.FindStringSubmatchIndex(m)
		if idx == nil || idx[2] < 0 {
			return m
		}
		content := m[idx[2]:idx[3]]
		// 闭合代码块：去掉闭围栏前的换行；未闭合：保持原样
		if strings.HasSuffix(m, "```") {
			return strings.TrimRight(content, "\n")
		}
		return content
	})
}

// isImageURL 判断文本是否为纯图片 URL。
// 规则：去除首尾空白后是完整的 http(s) URL（不含内部空格/换行）。
func isImageURL(text string) bool {
	t := strings.TrimSpace(text)
	if !strings.HasPrefix(t, "http://") && !strings.HasPrefix(t, "https://") {
		return false
	}
	return !strings.ContainsAny(t, " \t\n\r")
}
