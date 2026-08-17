package bizplugin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/DaWesen/lanmei-dream/internal/database"
	"github.com/DaWesen/lanmei-dream/internal/media"
	"github.com/DaWesen/lanmei-dream/internal/model"
	pluginpkg "github.com/DaWesen/lanmei-dream/internal/plugin"
	"github.com/cloudwego/eino/schema"
	"github.com/zrurf/conduit"
	"go.uber.org/zap"
)

// ============================================================
// StickerPlugin 自定义表情库插件
// ============================================================

// StickerPlugin 实现自定义表情的收藏与发送：
//   - /添加表情 <标签>（兼容旧命令 /收表情）：管理员（普通管理员 + 超管）收藏消息中的图片（上传 RustFS + 写 sticker_library）
//   - /发表情 <标签>：按标签发送一张表情；无参数时发送一张随机表情（默认行为）
//   - /表情列表：仅管理员（普通管理员 + 超管）可查看表情库清单（必须是 /表情列表 完整命令）
//   - pick_sticker 工具：LLM 按语义（情绪/语境）检索表情库，返回可发送的图片 URL
//
// 行为树：
//
//	subtree.sticker → Selector(
//	  Sequence(isCollectStickerCommand, Action("pipeline.plugin.sticker.collect")),
//	  Sequence(isListStickerCommand,    Action("pipeline.plugin.sticker.list")),
//	  Sequence(isSendStickerCommand,    Action("pipeline.plugin.sticker.send")),
//	)
//
// 管线：
//
//	pipeline.plugin.sticker.collect → [stickerCollectPass]
//	pipeline.plugin.sticker.list    → [stickerListPass]
//	pipeline.plugin.sticker.send    → [stickerSendPass]
//
// 数据来源约定（"不导包"）：
//   - 管理员标记：黑板 "bot.is_super_user"（bot 层注入，普通管理员与超管统一标记）
//   - 消息图片：黑板 "bot.image_urls"（bot 层注入的图片段 url 列表）
type StickerPlugin struct {
	db     *database.DB
	store  *media.ObjectStore
	logger *zap.Logger
}

// NewStickerPlugin 创建表情库插件。store 为 nil 时收藏功能不可用（检索仅返回库内记录）。
// db 与 logger 在 OnInit 阶段从 PluginContext 注入。
func NewStickerPlugin(store *media.ObjectStore, logger *zap.Logger) *StickerPlugin {
	return &StickerPlugin{store: store, logger: logger}
}

// Info 返回表情库插件元信息。
func (p *StickerPlugin) Info() pluginpkg.PluginInfo {
	return pluginpkg.PluginInfo{
		ID:          "sticker",
		Name:        "表情库",
		Description: "自定义表情收藏与按语义发送",
		Version:     "1.0.0",
		Commands: []pluginpkg.CommandDef{
			{Name: "添加表情", Description: "收藏消息中的图片为表情（仅管理员），格式：/添加表情 标签1 标签2", Order: 170},
			{Name: "发表情", Description: "发送匹配标签的表情；无参数时发送随机表情，格式：/发表情 标签", Order: 80},
			{Name: "表情列表", Description: "查看表情库清单（仅管理员），格式：/表情列表", Order: 90},
		},
		SubtreeID: pluginpkg.SubtreeID("sticker"),
		Tools: []pluginpkg.ToolDef{
			{
				Name:        "pick_sticker",
				Description: "根据情绪/语境从表情库挑选一张表情，返回图片URL；请在回复中把该URL作为唯一内容原样输出，不要用反引号/代码块包裹，也不要编造其他图片URL",
				Parameters: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
					"emotion": {
						Type:     schema.String,
						Desc:     "情绪/语境描述，如\"开心\"\"无语\"\"被坑了\"",
						Required: true,
					},
				}),
				Handler: p.toolPickSticker,
			},
		},
	}
}

// OnInit 初始化表情库插件，注册 Pass、Pipeline 和 Subtree。
func (p *StickerPlugin) OnInit(ctx *pluginpkg.PluginContext) error {
	p.db = ctx.DB
	p.logger = ctx.Logger

	// 注册 Pass
	collectPassID := pluginpkg.PassID("sticker", "collect")
	collectPass := &stickerCollectPass{db: p.db, store: p.store, logger: p.logger}
	if err := ctx.Engine.RegisterPass(collectPassID, collectPass); err != nil {
		return fmt.Errorf("register sticker collect pass: %w", err)
	}
	ctx.Registry.TrackPass("sticker", collectPassID)

	sendPassID := pluginpkg.PassID("sticker", "send")
	sendPass := &stickerSendPass{db: p.db, store: p.store, logger: p.logger}
	if err := ctx.Engine.RegisterPass(sendPassID, sendPass); err != nil {
		return fmt.Errorf("register sticker send pass: %w", err)
	}
	ctx.Registry.TrackPass("sticker", sendPassID)

	listPassID := pluginpkg.PassID("sticker", "list")
	listPass := &stickerListPass{db: p.db, logger: p.logger}
	if err := ctx.Engine.RegisterPass(listPassID, listPass); err != nil {
		return fmt.Errorf("register sticker list pass: %w", err)
	}
	ctx.Registry.TrackPass("sticker", listPassID)

	// 注册管线
	collectPipelineID := pluginpkg.PipelineID("sticker", "collect")
	if err := ctx.Engine.RegisterPipeline(conduit.NewPipelineFromIDs(collectPipelineID, collectPassID)); err != nil {
		return fmt.Errorf("register sticker collect pipeline: %w", err)
	}
	ctx.Registry.TrackPipeline("sticker", collectPipelineID)

	sendPipelineID := pluginpkg.PipelineID("sticker", "send")
	if err := ctx.Engine.RegisterPipeline(conduit.NewPipelineFromIDs(sendPipelineID, sendPassID)); err != nil {
		return fmt.Errorf("register sticker send pipeline: %w", err)
	}
	ctx.Registry.TrackPipeline("sticker", sendPipelineID)

	listPipelineID := pluginpkg.PipelineID("sticker", "list")
	if err := ctx.Engine.RegisterPipeline(conduit.NewPipelineFromIDs(listPipelineID, listPassID)); err != nil {
		return fmt.Errorf("register sticker list pipeline: %w", err)
	}
	ctx.Registry.TrackPipeline("sticker", listPipelineID)

	// 注册行为树子树：添加表情 / 表情列表 / 发表情 命令路由
	subtree := conduit.NewSelector(
		conduit.NewSequence(
			conduit.NewCondition(isCollectStickerCommand),
			conduit.NewAction(collectPipelineID),
		),
		conduit.NewSequence(
			conduit.NewCondition(isListStickerCommand),
			conduit.NewAction(listPipelineID),
		),
		conduit.NewSequence(
			conduit.NewCondition(isSendStickerCommand),
			conduit.NewAction(sendPipelineID),
		),
	)
	if err := ctx.Engine.RegisterSubtree(pluginpkg.SubtreeID("sticker"), subtree); err != nil {
		return fmt.Errorf("register sticker subtree: %w", err)
	}

	return nil
}

// OnStart 表情库插件无需后台任务。
func (p *StickerPlugin) OnStart(_ *pluginpkg.PluginContext) error { return nil }

// OnStop 表情库插件无需清理资源。
func (p *StickerPlugin) OnStop(_ *pluginpkg.PluginContext) error { return nil }

// ============================================================
// 条件判断
// ============================================================

// stickerCollectPrefixes 添加表情命令前缀：正式 /添加表情，兼容旧命令 /收表情。
var stickerCollectPrefixes = []string{"/添加表情", "/收表情"}

// hasStickerCollectPrefix 判断消息是否命中添加表情命令前缀。
func hasStickerCollectPrefix(msg string) bool {
	msg = strings.TrimSpace(msg)
	for _, p := range stickerCollectPrefixes {
		if strings.HasPrefix(msg, p) {
			return true
		}
	}
	return false
}

// stripStickerCollectPrefix 去掉添加表情命令前缀，返回剩余参数部分。
func stripStickerCollectPrefix(msg string) string {
	msg = strings.TrimSpace(msg)
	for _, p := range stickerCollectPrefixes {
		if strings.HasPrefix(msg, p) {
			return strings.TrimSpace(strings.TrimPrefix(msg, p))
		}
	}
	return msg
}

// isCollectStickerCommand 判断消息是否为添加表情命令（兼容旧命令 /收表情）。
func isCollectStickerCommand(ctx *conduit.MessageContext) bool {
	return hasStickerCollectPrefix(ctx.RawMsg)
}

// isSendStickerCommand 判断消息是否为发表情命令。
func isSendStickerCommand(ctx *conduit.MessageContext) bool {
	return strings.HasPrefix(strings.TrimSpace(ctx.RawMsg), "/发表情")
}

// isListStickerCommand 判断消息是否为 /表情列表 命令。
// 必须完整匹配 "/表情列表"（trim 后相等），不接受前缀/多余参数，
// 避免 /表情列表XXX 之类的变体误入该分支。
func isListStickerCommand(ctx *conduit.MessageContext) bool {
	return strings.TrimSpace(ctx.RawMsg) == "/表情列表"
}

// ============================================================
// 黑板键（bot 层注入，插件按"不导包"约定用字符串字面量）
// ============================================================

const (
	blackboardIsSuperUser = "bot.is_super_user" // bool 当前用户是否超管（普通管理员与超管统一注入）
	blackboardImageURLs   = "bot.image_urls"    // []string 消息中图片段 url 列表
)

// ============================================================
// Pass 实现：添加表情入库
// ============================================================

// stickerCollectPass 收藏表情：校验超管 → 提取图片 → 上传 RustFS → 写库。
type stickerCollectPass struct {
	db     *database.DB
	store  *media.ObjectStore
	logger *zap.Logger
}

func (pass *stickerCollectPass) Execute(ctx *conduit.MessageContext) error {
	// 超管校验
	isSuper, _ := ctx.Extra[blackboardIsSuperUser].(bool)
	if !isSuper {
		conduit.AppendOutput(ctx, &conduit.Message{
			UserID: ctx.UserID, GroupID: ctx.GroupID, IsGroup: ctx.IsGroup,
			Content: "只有管理员能收藏表情哦~",
		})
		return nil
	}

	// 对象存储未配置时收藏不可用
	if pass.store == nil {
		conduit.AppendOutput(ctx, &conduit.Message{
			UserID: ctx.UserID, GroupID: ctx.GroupID, IsGroup: ctx.IsGroup,
			Content: "表情存储未配置（RustFS 不可用），无法收藏",
		})
		return nil
	}

	// 解析标签：/添加表情 标签1 标签2
	raw := stripStickerCollectPrefix(ctx.RawMsg)
	tags := strings.Fields(raw)
	if len(tags) == 0 {
		conduit.AppendOutput(ctx, &conduit.Message{
			UserID: ctx.UserID, GroupID: ctx.GroupID, IsGroup: ctx.IsGroup,
			Content: "格式错误！用法：/添加表情 标签1 标签2（需附带一张图片）",
		})
		return nil
	}

	// 提取消息图片 URL
	imageURLs, _ := ctx.Extra[blackboardImageURLs].([]string)
	if len(imageURLs) == 0 {
		conduit.AppendOutput(ctx, &conduit.Message{
			UserID: ctx.UserID, GroupID: ctx.GroupID, IsGroup: ctx.IsGroup,
			Content: "没有检测到图片，请附带一张图片再收藏",
		})
		return nil
	}

	// 下载并上传（仅处理第一张图片）
	imgURL := imageURLs[0]
	data, mime, err := downloadImage(ctx.Ctx, imgURL)
	if err != nil {
		pass.logger.Warn("sticker: 图片下载失败", zap.String("url", imgURL), zap.Error(err))
		conduit.AppendOutput(ctx, &conduit.Message{
			UserID: ctx.UserID, GroupID: ctx.GroupID, IsGroup: ctx.IsGroup,
			Content: "图片下载失败，请重试",
		})
		return nil
	}
	if len(data) == 0 {
		// llonebot 本地图片（file:// 路径）在容器内不可达时可能返回空内容，
		// 直接拒绝，避免把 0 字节空对象存进表情库（发送时会失败）。
		pass.logger.Warn("sticker: 图片内容为空，拒绝入库",
			zap.String("url", imgURL), zap.String("mime", mime))
		conduit.AppendOutput(ctx, &conduit.Message{
			UserID: ctx.UserID, GroupID: ctx.GroupID, IsGroup: ctx.IsGroup,
			Content: "图片内容为空（本地文件可能无法从服务器访问），请换一张网络图片试试",
		})
		return nil
	}
	pass.logger.Info("sticker: 图片下载完成",
		zap.String("url", imgURL), zap.String("mime", mime), zap.Int("size", len(data)))

	objectKey, err := pass.store.Put(ctx.Ctx, data, mime)
	if err != nil {
		pass.logger.Error("sticker: 上传表情失败", zap.Error(err))
		conduit.AppendOutput(ctx, &conduit.Message{
			UserID: ctx.UserID, GroupID: ctx.GroupID, IsGroup: ctx.IsGroup,
			Content: "表情上传失败，请稍后重试",
		})
		return nil
	}

	// 内容寻址幂等：同图已收藏则提示
	if existing, _ := pass.db.GetStickerByObjectKey(ctx.Ctx, objectKey); existing != nil {
		conduit.AppendOutput(ctx, &conduit.Message{
			UserID: ctx.UserID, GroupID: ctx.GroupID, IsGroup: ctx.IsGroup,
			Content: fmt.Sprintf("这张表情已在表情库啦（ID %d），无需重复收藏", existing.ID),
		})
		return nil
	}

	// 写库
	tagsJSON, _ := json.Marshal(tags)
	sticker := &model.StickerLibrary{
		ObjectKey: objectKey,
		FileID:    "",
		Tags:      string(tagsJSON),
		Source:    "manual",
	}
	if err := pass.db.CreateSticker(ctx.Ctx, sticker); err != nil {
		pass.logger.Error("sticker: 写库失败", zap.Error(err))
		conduit.AppendOutput(ctx, &conduit.Message{
			UserID: ctx.UserID, GroupID: ctx.GroupID, IsGroup: ctx.IsGroup,
			Content: "表情入库失败，请稍后重试",
		})
		return nil
	}

	conduit.AppendOutput(ctx, &conduit.Message{
		UserID: ctx.UserID, GroupID: ctx.GroupID, IsGroup: ctx.IsGroup,
		Content: fmt.Sprintf("已收藏表情 ✅ 标签：%s", strings.Join(tags, " / ")),
	})
	return nil
}

// ============================================================
// Pass 实现：发表情（发送）
// ============================================================

// stickerSendPass 发送表情：/发表情 标签 → 检索并发送一张；/发表情（无参）→ 发送一张随机表情（默认行为）。
type stickerSendPass struct {
	db     *database.DB
	store  *media.ObjectStore
	logger *zap.Logger
}

func (pass *stickerSendPass) Execute(ctx *conduit.MessageContext) error {
	keyword := strings.TrimSpace(strings.TrimPrefix(ctx.RawMsg, "/发表情"))

	// 无参数（手动 /发表情 或意图路由触发但未提取到标签）：
	// 默认发送一张随机表情，不再列出表情库（列表改由仅管理员的 /表情列表 提供）。
	if keyword == "" {
		pass.sendRandom(ctx)
		return nil
	}

	// 带参数：按标签检索并发一张（取最新匹配）
	stickers, err := pass.db.SearchStickers(ctx.Ctx, keyword, 5)
	if err != nil {
		pass.logger.Error("sticker: 检索失败", zap.String("keyword", keyword), zap.Error(err))
		conduit.AppendOutput(ctx, &conduit.Message{
			UserID: ctx.UserID, GroupID: ctx.GroupID, IsGroup: ctx.IsGroup,
			Content: "表情检索失败，请稍后重试",
		})
		return nil
	}
	if len(stickers) == 0 {
		conduit.AppendOutput(ctx, &conduit.Message{
			UserID: ctx.UserID, GroupID: ctx.GroupID, IsGroup: ctx.IsGroup,
			Content: fmt.Sprintf("表情库里没有匹配「%s」的表情，用 /表情列表 看看有哪些吧", keyword),
		})
		return nil
	}

	hit := stickers[0]
	if pass.store == nil {
		conduit.AppendOutput(ctx, &conduit.Message{
			UserID: ctx.UserID, GroupID: ctx.GroupID, IsGroup: ctx.IsGroup,
			Content: "表情存储未配置（RustFS 不可用），无法发送",
		})
		return nil
	}
	presignedURL, err := pass.store.Presign(ctx.Ctx, hit.ObjectKey, 10*time.Minute)
	if err != nil {
		pass.logger.Error("sticker: 生成图片 URL 失败", zap.Uint("id", hit.ID), zap.Error(err))
		conduit.AppendOutput(ctx, &conduit.Message{
			UserID: ctx.UserID, GroupID: ctx.GroupID, IsGroup: ctx.IsGroup,
			Content: "表情发送失败，请稍后重试",
		})
		return nil
	}
	// 纯 URL 输出 → bot 层识别为图片段发送
	conduit.AppendOutput(ctx, &conduit.Message{
		UserID: ctx.UserID, GroupID: ctx.GroupID, IsGroup: ctx.IsGroup,
		Content: presignedURL,
	})
	return nil
}

// sendRandom 无参数时的默认行为：从表情库随机取一张发送。
// 库为空、存储未配置或取图失败时给出对应提示。
func (pass *stickerSendPass) sendRandom(ctx *conduit.MessageContext) {
	if pass.db == nil || pass.store == nil {
		conduit.AppendOutput(ctx, &conduit.Message{
			UserID: ctx.UserID, GroupID: ctx.GroupID, IsGroup: ctx.IsGroup,
			Content: "表情存储未配置（RustFS 不可用），无法发送",
		})
		return
	}
	sticker, err := pass.db.RandomSticker(ctx.Ctx)
	if err != nil {
		pass.logger.Error("sticker: 随机取表情失败", zap.Error(err))
		conduit.AppendOutput(ctx, &conduit.Message{
			UserID: ctx.UserID, GroupID: ctx.GroupID, IsGroup: ctx.IsGroup,
			Content: "表情获取失败，请稍后重试",
		})
		return
	}
	if sticker == nil {
		conduit.AppendOutput(ctx, &conduit.Message{
			UserID: ctx.UserID, GroupID: ctx.GroupID, IsGroup: ctx.IsGroup,
			Content: "表情库还是空的，发张图配上 /添加表情 标签 来收藏吧",
		})
		return
	}
	presignedURL, err := pass.store.Presign(ctx.Ctx, sticker.ObjectKey, 10*time.Minute)
	if err != nil {
		pass.logger.Error("sticker: 随机表情 URL 生成失败", zap.Uint("id", sticker.ID), zap.Error(err))
		conduit.AppendOutput(ctx, &conduit.Message{
			UserID: ctx.UserID, GroupID: ctx.GroupID, IsGroup: ctx.IsGroup,
			Content: "表情发送失败，请稍后重试",
		})
		return
	}
	// 纯 URL 输出 → bot 层识别为图片段发送
	conduit.AppendOutput(ctx, &conduit.Message{
		UserID: ctx.UserID, GroupID: ctx.GroupID, IsGroup: ctx.IsGroup,
		Content: presignedURL,
	})
}

// ============================================================
// Pass 实现：表情列表（仅管理员）
// ============================================================

// stickerListPass 查看表情库清单：校验管理员（普通管理员 + 超管）→ 输出列表。
type stickerListPass struct {
	db     *database.DB
	logger *zap.Logger
}

func (pass *stickerListPass) Execute(ctx *conduit.MessageContext) error {
	// 管理员校验：bot 层将普通管理员与超管统一注入 bot.is_super_user
	isAdmin, _ := ctx.Extra[blackboardIsSuperUser].(bool)
	if !isAdmin {
		conduit.AppendOutput(ctx, &conduit.Message{
			UserID: ctx.UserID, GroupID: ctx.GroupID, IsGroup: ctx.IsGroup,
			Content: "只有管理员能查看表情列表哦~",
		})
		return nil
	}

	stickers, err := pass.db.ListStickers(ctx.Ctx, 20)
	if err != nil {
		pass.logger.Error("sticker: 列表查询失败", zap.Error(err))
		conduit.AppendOutput(ctx, &conduit.Message{
			UserID: ctx.UserID, GroupID: ctx.GroupID, IsGroup: ctx.IsGroup,
			Content: "表情库查询失败，请稍后重试",
		})
		return nil
	}
	if len(stickers) == 0 {
		conduit.AppendOutput(ctx, &conduit.Message{
			UserID: ctx.UserID, GroupID: ctx.GroupID, IsGroup: ctx.IsGroup,
			Content: "表情库还是空的，发张图配上 /添加表情 标签 来收藏吧",
		})
		return nil
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("表情库共 %d 张：\n", len(stickers)))
	for i, s := range stickers {
		var tags []string
		if err := json.Unmarshal([]byte(s.Tags), &tags); err != nil {
			tags = nil
		}
		b.WriteString(fmt.Sprintf("%d. [%d] %s\n", i+1, s.ID, strings.Join(tags, "/")))
	}
	conduit.AppendOutput(ctx, &conduit.Message{
		UserID: ctx.UserID, GroupID: ctx.GroupID, IsGroup: ctx.IsGroup,
		Content: strings.TrimRight(b.String(), "\n"),
	})
	return nil
}

// ============================================================
// AI 工具：pick_sticker 按语义选表情
// ============================================================

// toolPickSticker 是 AI 工具处理器：按情绪/语境检索表情库，
// 命中则返回可发送的图片 URL（预签名），未命中返回空结果提示。
func (p *StickerPlugin) toolPickSticker(ctx context.Context, argsJSON string) (string, error) {
	var args struct {
		Emotion string `json:"emotion"` // 情绪/语境描述，如"无语""开心""被坑了"
		QingXu  string `json:"情绪"`      // 兼容部分 LLM 直接传中文键
	}
	_ = json.Unmarshal([]byte(argsJSON), &args) // 解析失败走下方兜底，不中断
	emotion := strings.TrimSpace(args.Emotion)
	if emotion == "" {
		emotion = strings.TrimSpace(args.QingXu)
	}
	if emotion == "" {
		// 兜底：LLM 可能直接传词而非 JSON（如 "开心"）
		emotion = strings.Trim(strings.TrimSpace(argsJSON), "\"'` \n\t")
	}
	if emotion == "" {
		return "", fmt.Errorf("emotion 不能为空")
	}
	if p.db == nil {
		return "表情库不可用", nil
	}

	stickers, err := p.db.SearchStickers(ctx, emotion, 5)
	if err != nil {
		return "", fmt.Errorf("表情检索失败: %w", err)
	}
	if len(stickers) == 0 {
		return "表情库里没有匹配「" + emotion + "」的表情，直接回复纯文本即可", nil
	}

	// 取匹配度最高的（第一条，按时间最新）生成预签名 URL
	hit := stickers[0]
	if p.store == nil {
		return fmt.Sprintf("找到表情但对象存储不可用，无法生成图片URL"), nil
	}
	presignedURL, err := p.store.Presign(ctx, hit.ObjectKey, 10*time.Minute)
	if err != nil {
		return "", fmt.Errorf("表情URL生成失败: %w", err)
	}
	// 返回 URL 供 LLM 直接作为图片输出
	return presignedURL, nil
}

// Pick 随机取一张表情并返回可发送的预签名 URL（硬性表情规则：Bot 周期性附带表情）。
// 库为空、对象存储未配置或取图失败时返回空串，表示本轮不注入表情。
func (p *StickerPlugin) Pick(ctx context.Context) string {
	if p.db == nil || p.store == nil {
		return ""
	}
	sticker, err := p.db.RandomSticker(ctx)
	if err != nil {
		p.logger.Warn("sticker: 随机取表情失败", zap.Error(err))
		return ""
	}
	if sticker == nil {
		return ""
	}
	url, err := p.store.Presign(ctx, sticker.ObjectKey, 10*time.Minute)
	if err != nil {
		p.logger.Warn("sticker: 随机表情 URL 生成失败", zap.Uint("id", sticker.ID), zap.Error(err))
		return ""
	}
	return url
}

// ============================================================
// 辅助函数
// ============================================================

// stickerHTTPClient 图片下载客户端（10s 超时 + 10MB 上限）。
var stickerHTTPClient = &http.Client{Timeout: 10 * time.Second}

// downloadImage 下载图片内容，限制大小（10MB）。
func downloadImage(ctx context.Context, url string) ([]byte, string, error) {
	const maxBytes = 10 << 20 // 10MB

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, "", fmt.Errorf("构造下载请求失败: %w", err)
	}
	req.Header.Set("User-Agent", "lanmei-dream/1.0")

	resp, err := stickerHTTPClient.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("下载失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("下载 HTTP %d", resp.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, "", fmt.Errorf("读取响应失败: %w", err)
	}
	if len(data) > maxBytes {
		return nil, "", fmt.Errorf("图片超过大小上限")
	}

	mime := resp.Header.Get("Content-Type")
	if mime == "" || mime == "application/octet-stream" {
		mime = media.SniffMime(data)
	}
	return data, mime, nil
}
