package random_beauty

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/DaWesen/lanmei-dream/internal/config"
	"github.com/DaWesen/lanmei-dream/internal/database"
	"github.com/DaWesen/lanmei-dream/internal/media"
	pluginpkg "github.com/DaWesen/lanmei-dream/internal/plugin"
	"github.com/zrurf/conduit"
	"go.uber.org/zap"
)

const (
	pluginID        = "random_beauty"
	sendSegmentsKey = "bot.send.segments"
)

const (
	messageFailure     = "图难产了，稍等一会再试试吧~(￣ω￣;)"
	messageRateLimited = "请求太频繁啦，请稍后再试"
)

// Plugin 是严格安全审核的 Pixiv 随机美图插件（预审核图池模式）。
//
// 插件 ID random_beauty；命令 /随机美图（无参数、无工具）。
// 依赖：图源 Random Mage 兼容 API（New 时校验地址必须是 HTTPS 且无凭据，配置非法直接返回错误）、
// 视觉审核服务（moderator，未配置时 fail-closed 不补图，仅能发送池内既有图片）、
// Postgres 图池（db 由 PluginContext 在 OnInit 注入，不可用时取图一律回复失败提示）、
// RustFS 对象存储（store 为 nil 时图池不可用，取图降级为失败提示）与 StateStore
// （冷却计数；未注入或冷却参数 <=0 时不限流）。
//
// 安全策略：只发送视觉模型明确判定为安全且置信度达到阈值的图片，不确定即拒绝
// （判定语义见 ai.ImageSafetyResult.IsSafe）；存储或数据库故障时禁止回退到实时管道，
// 安全标准不因故障放松。
//
// 用户路径只读池（Postgres + RustFS）毫秒级出图；
// 「候选 → 下载 → vision 审核」全部前置到补图后台 goroutine（见 refill.go）。
type Plugin struct {
	cfg        config.RandomBeautyConfig
	provider   CandidateProvider
	downloader ImageDownloader
	moderator  ImageModerator
	store      *media.ObjectStore
	db         *database.DB
	logger     *zap.Logger

	// 补图后台编排：refillCtx 独立于消息 ctx，OnStop 取消并等待 wg 退出；
	// sem 限制补图并发峰值，seed 保证进程内只触发一次种子补图，
	// warnStoreUnavailable 保证对象存储缺失只告警一次。
	refillCtx            context.Context
	refillCancel         context.CancelFunc
	wg                   sync.WaitGroup
	sem                  chan struct{}
	seed                 func()
	warnStoreUnavailable func()
}

// New 使用 Random Mage 兼容 API 构建随机美图插件。
// moderator 为 nil 时无法补图（fail-closed，仅能发送池内既有图片）；
// store 为 nil 时图池不可用，取图一律降级回复失败提示。
func New(cfg config.RandomBeautyConfig, moderator ImageModerator, store *media.ObjectStore, logger *zap.Logger) (*Plugin, error) {
	cfg = normalizedConfig(cfg)
	if logger == nil {
		logger = zap.NewNop()
	}
	client := &http.Client{Timeout: time.Duration(cfg.TimeoutSeconds) * time.Second}
	provider, err := newRandomMageClient(cfg.APIBaseURL, client, cfg.MinWidth, cfg.MinHeight, cfg.MinBookmarks, false)
	if err != nil {
		return nil, err
	}
	provider.logger = logger
	downloader, err := newImageDownloader(cfg.APIBaseURL, client, cfg.MaxImageBytes, cfg.MinWidth, cfg.MinHeight, false)
	if err != nil {
		return nil, err
	}
	if moderator == nil {
		logger.Warn("random_beauty: 视觉审核未配置，无法补图，仅能发送池内既有图片")
	}
	return newPluginWithDeps(cfg, provider, downloader, moderator, store, logger), nil
}

// newPluginWithDeps 按依赖组装插件，供测试注入替身。
func newPluginWithDeps(cfg config.RandomBeautyConfig, provider CandidateProvider, downloader ImageDownloader, moderator ImageModerator, store *media.ObjectStore, logger *zap.Logger) *Plugin {
	if logger == nil {
		logger = zap.NewNop()
	}
	cfg = normalizedConfig(cfg)
	refillCtx, cancel := context.WithCancel(context.Background())
	p := &Plugin{
		cfg:          cfg,
		provider:     provider,
		downloader:   downloader,
		moderator:    moderator,
		store:        store,
		logger:       logger,
		refillCtx:    refillCtx,
		refillCancel: cancel,
		sem:          make(chan struct{}, refillConcurrency),
	}
	p.seed = sync.OnceFunc(func() {
		p.wg.Go(p.seedLoop)
	})
	p.warnStoreUnavailable = sync.OnceFunc(func() {
		p.logger.Warn("random_beauty: 对象存储未配置，图池不可用")
	})
	return p
}

func normalizedConfig(cfg config.RandomBeautyConfig) config.RandomBeautyConfig {
	if strings.TrimSpace(cfg.APIBaseURL) == "" {
		cfg.APIBaseURL = "https://i.mukyu.ru"
	}
	// 补图单张拉取预算（候选+下载）：上限放宽到 30，超范围回退默认 18。
	if cfg.TimeoutSeconds < 1 || cfg.TimeoutSeconds > 30 {
		cfg.TimeoutSeconds = 18
	}
	if cfg.MaxAttempts < 1 {
		cfg.MaxAttempts = 2
	}
	if cfg.MaxAttempts > 2 {
		cfg.MaxAttempts = 2
	}
	if cfg.CooldownSeconds < 0 {
		cfg.CooldownSeconds = 30
	}
	if cfg.MaxImageBytes <= 0 || cfg.MaxImageBytes > 10*1024*1024 {
		cfg.MaxImageBytes = 10 * 1024 * 1024
	}
	if cfg.MinWidth <= 0 {
		cfg.MinWidth = 720
	}
	if cfg.MinHeight <= 0 {
		cfg.MinHeight = 720
	}
	if cfg.MinBookmarks < 0 {
		cfg.MinBookmarks = 100
	}
	if cfg.SafeConfidence <= 0 || cfg.SafeConfidence > 1 {
		cfg.SafeConfidence = 0.9
	}
	if cfg.ModerationTimeoutSeconds < 1 || cfg.ModerationTimeoutSeconds > 20 {
		cfg.ModerationTimeoutSeconds = 8
	}
	// 图池种子目标张数：默认 100，夹在 10~500。
	if cfg.PoolInitSize < 10 || cfg.PoolInitSize > 500 {
		cfg.PoolInitSize = 100
	}
	// 补图单张审核（含上传）预算：默认 25，夹在 5~30。
	if cfg.RefillModerationTimeoutSeconds < 5 || cfg.RefillModerationTimeoutSeconds > 30 {
		cfg.RefillModerationTimeoutSeconds = 25
	}
	return cfg
}

// Info 返回随机美图插件元信息（ID random_beauty，命令 /随机美图）。
func (p *Plugin) Info() pluginpkg.PluginInfo {
	return pluginpkg.PluginInfo{
		ID:          pluginID,
		Name:        "随机美图",
		Description: "获取经过成人内容与擦边内容安全审核的 Pixiv 随机插画",
		Version:     "1.1.0",
		Commands: []pluginpkg.CommandDef{
			{Name: "随机美图", Description: "发送一张经过严格安全审核的 Pixiv 随机插画；不需要参数", Order: 60},
		},
		SubtreeID: pluginpkg.SubtreeID(pluginID),
	}
}

// OnInit 初始化随机美图插件：注入数据库与日志，注册取图 Pass、Pipeline 和 Subtree，
// 并把 StateStore 传给 Pass 用作按平台+会话+用户维度的冷却；上游与安全阈值在 New 阶段已归一化。
func (p *Plugin) OnInit(ctx *pluginpkg.PluginContext) error {
	p.db = ctx.DB
	p.logger = ctx.Logger

	passID := pluginpkg.PassID(pluginID, "fetch")
	pass := &randomBeautyPass{
		plugin:     p,
		stateStore: ctx.Store,
		cooldown:   time.Duration(p.cfg.CooldownSeconds) * time.Second,
	}
	if err := ctx.Engine.RegisterPass(passID, pass); err != nil {
		return fmt.Errorf("register random_beauty pass: %w", err)
	}
	ctx.Registry.TrackPass(pluginID, passID)

	pipelineID := pluginpkg.PipelineID(pluginID, "main")
	if err := ctx.Engine.RegisterPipeline(conduit.NewPipelineFromIDs(pipelineID, passID)); err != nil {
		return fmt.Errorf("register random_beauty pipeline: %w", err)
	}
	ctx.Registry.TrackPipeline(pluginID, pipelineID)

	subtree := conduit.NewSequence(
		conduit.NewCondition(isRandomBeautyCommand),
		conduit.NewAction(pipelineID),
	)
	if err := ctx.Engine.RegisterSubtree(pluginpkg.SubtreeID(pluginID), subtree); err != nil {
		return fmt.Errorf("register random_beauty subtree: %w", err)
	}
	return nil
}

// OnStart 池为空时触发一次性种子补图（进程内仅跑一次）。
func (p *Plugin) OnStart(_ *pluginpkg.PluginContext) error {
	if p.store == nil || p.db == nil {
		return nil
	}
	count, err := p.db.CountRandomBeautyPool(p.refillCtx)
	if err != nil {
		p.logger.Warn("random_beauty: 统计图池数量失败，跳过启动种子", zap.Error(err))
		return nil
	}
	if count == 0 {
		p.seed()
	}
	return nil
}

// OnStop 取消补图上下文并等待所有补图 goroutine 退出（幂等，可重复调用）。
func (p *Plugin) OnStop(_ *pluginpkg.PluginContext) error {
	p.refillCancel()
	p.wg.Wait()
	return nil
}

func isRandomBeautyCommand(ctx *conduit.MessageContext) bool {
	return ctx != nil && strings.TrimSpace(ctx.RawMsg) == "/随机美图"
}

// randomBeautyPass 是用户取图 Pass：通过冷却检查后从池中随机取一张既有审核图直接发送，
// 不在用户路径做任何实时审核或补图。
type randomBeautyPass struct {
	plugin     *Plugin
	stateStore conduit.StateStore
	cooldown   time.Duration
}

// Execute 处理 /随机美图：先做冷却检查（StateStore SetIfNotExists，冷却期内回复限流提示，
// 状态写入失败时放行），再从图池随机取一张已审核图片、读出字节并组装
// [图片段 + 署名文本] 写入出站段键 bot.send.segments，最后异步补一张维持池水位（不阻塞用户路径）。
// 由 plugin.random_beauty.pipeline.main 在 isRandomBeautyCommand 完全匹配 /随机美图 后调用。
// 失败分支：对象存储未配置（仅告警一次）、数据库不可用、图池取图失败或对象读取失败，
// 均回复失败提示；取图失败会触发一次性种子补图，但绝不回退到未经审核的实时管道。
func (pass *randomBeautyPass) Execute(ctx *conduit.MessageContext) error {
	p := pass.plugin
	if p.store == nil {
		p.warnStoreUnavailable()
		pass.reply(ctx, messageFailure)
		return nil
	}
	if !pass.acquireCooldown(ctx) {
		pass.reply(ctx, messageRateLimited)
		return nil
	}
	if p.db == nil {
		pass.reply(ctx, messageFailure)
		return nil
	}

	rec, err := p.db.GetRandomBeautyImage(ctx.Ctx)
	if err != nil {
		p.logger.Warn("random_beauty: 图池取图失败", zap.Error(err))
		pass.reply(ctx, messageFailure)
		// 池空/不可达兜底：触发种子补图（sync.Once 保证进程内只跑一次）。
		p.seed()
		return nil
	}
	data, err := p.store.Get(ctx.Ctx, rec.ObjectKey)
	if err != nil {
		p.logger.Warn("random_beauty: 读取池内图片对象失败",
			zap.String("object_key", rec.ObjectKey), zap.Error(err))
		pass.reply(ctx, messageFailure)
		// 禁止回退实时管道：安全标准不因存储故障而放松。
		return nil
	}

	file := "base64://" + base64.StdEncoding.EncodeToString(data)
	conduit.Set(ctx, sendSegmentsKey, []map[string]any{
		{"type": "image", "data": map[string]any{"file": file}},
		{"type": "text", "data": map[string]any{"text": formatAttribution(rec.IllustID, rec.Title, rec.Author)}},
	})

	// 异步补一张维持池水位，绝不阻塞用户路径。
	p.wg.Go(func() { p.refillOne(p.refillCtx) })
	return nil
}

func (pass *randomBeautyPass) acquireCooldown(ctx *conduit.MessageContext) bool {
	if pass.stateStore == nil || pass.cooldown <= 0 {
		return true
	}
	key := pluginpkg.StoreKey(pluginID, cooldownScope(ctx))
	acquired, err := pass.stateStore.SetIfNotExists(ctx.Ctx, key, "1", pass.cooldown)
	if err != nil {
		pass.plugin.logger.Warn("random_beauty: 冷却状态写入失败，放行请求", zap.Error(err))
		return true
	}
	return acquired
}

func cooldownScope(ctx *conduit.MessageContext) string {
	platform, _ := ctx.Extra["platform"].(string)
	if platform == "" {
		platform = "unknown"
	}
	scope := "private"
	if ctx.IsGroup {
		scope = "group:" + ctx.GroupID
	}
	return fmt.Sprintf("cooldown:%s:%s:user:%s", platform, scope, ctx.UserID)
}

// formatAttribution 生成图片署名文本。
func formatAttribution(illustID int64, title, author string) string {
	title = singleLine(title, "未命名")
	author = singleLine(author, "未知画师")
	return fmt.Sprintf("\n《%s》\n作者：%s\nPixiv：https://www.pixiv.net/artworks/%d", title, author, illustID)
}

func singleLine(value, fallback string) string {
	value = strings.Join(strings.Fields(value), " ")
	if value == "" {
		return fallback
	}
	return value
}

func (pass *randomBeautyPass) reply(ctx *conduit.MessageContext, content string) {
	conduit.AppendOutput(ctx, &conduit.Message{
		UserID: ctx.UserID, GroupID: ctx.GroupID, IsGroup: ctx.IsGroup,
		Content: content,
	})
}
