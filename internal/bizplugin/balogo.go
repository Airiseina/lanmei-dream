package bizplugin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	pluginpkg "github.com/DaWesen/lanmei-dream/internal/plugin"
	"github.com/zrurf/conduit"
)

// ============================================================
// BaLogoPlugin 蔚蓝档案风格LOGO插件
// ============================================================

// BaLogoPlugin 生成蔚蓝档案（Blue Archive）风格LOGO图片。
//
// 功能：
//   - 触发命令：/balogo 左文字 右文字
//   - 通过外部服务 balogo.huankong.top 代理生成 BA 风格 LOGO 图片
//   - 参数格式错误时回复用法提示
//
// 行为树：
//
//	subtree.balogo → Sequence(IsBaLogoCommand, Action("pipeline.plugin.balogo"))
//
// 管线：
//
//	pipeline.plugin.balogo → [executePass, replyPass]
type BaLogoPlugin struct{}

// NewBaLogoPlugin 创建 BA-LOGO 插件。
func NewBaLogoPlugin() *BaLogoPlugin {
	return &BaLogoPlugin{}
}

// baLogoURL 外部 BA-LOGO 生成服务地址（与上游 LanMei 一致）。
// textL/textR 分别对应左右两部分文字，由服务端渲染成图片。
var baLogoURL = "https://balogo.huankong.top/?textL=%v&textR=%v"

// Info 返回 BA-LOGO 插件元信息。
func (p *BaLogoPlugin) Info() pluginpkg.PluginInfo {
	return pluginpkg.PluginInfo{
		ID:          "balogo",
		Name:        "BA-LOGO",
		Description: "生成蔚蓝档案风格LOGO",
		Version:     "1.0.0",
		Commands: []pluginpkg.CommandDef{
			{Name: "balogo", Description: "生成蔚蓝档案风格LOGO，格式：/balogo 左文字 右文字"},
		},
		SubtreeID: pluginpkg.SubtreeID("balogo"),
		Tools: []pluginpkg.ToolDef{
			{
				Name:        "balogo_generate",
				Description: "生成蔚蓝档案风格LOGO图片，需提供左文字和右文字两个参数，返回图片URL",
				Handler:     p.toolBaLogoGenerate,
			},
		},
	}
}

// OnInit 初始化 BA-LOGO 插件，注册 Pass、Pipeline 和 Subtree。
func (p *BaLogoPlugin) OnInit(ctx *pluginpkg.PluginContext) error {
	// 注册 Pass
	executePassID := pluginpkg.PassID("balogo", "execute")
	replyPassID := pluginpkg.PassID("balogo", "reply")

	executePass := &balogoExecutePass{}
	replyPass := &balogoReplyPass{}

	if err := ctx.Engine.RegisterPass(executePassID, executePass); err != nil {
		return fmt.Errorf("register execute pass: %w", err)
	}
	if err := ctx.Engine.RegisterPass(replyPassID, replyPass); err != nil {
		return fmt.Errorf("register reply pass: %w", err)
	}

	// 跟踪 Pass
	ctx.Registry.TrackPass("balogo", executePassID)
	ctx.Registry.TrackPass("balogo", replyPassID)

	// 注册管线
	pipelineID := pluginpkg.PipelineID("balogo", "main")
	pl := conduit.NewPipelineFromIDs(
		pipelineID,
		executePassID,
		replyPassID,
	)
	if err := ctx.Engine.RegisterPipeline(pl); err != nil {
		return fmt.Errorf("register pipeline: %w", err)
	}

	// 跟踪 Pipeline
	ctx.Registry.TrackPipeline("balogo", pipelineID)

	// 注册行为树子树
	subtree := conduit.NewSequence(
		conduit.NewCondition(isBaLogoCommand),
		conduit.NewAction(pipelineID),
	)
	if err := ctx.Engine.RegisterSubtree(pluginpkg.SubtreeID("balogo"), subtree); err != nil {
		return fmt.Errorf("register subtree: %w", err)
	}

	return nil
}

// OnStart BA-LOGO 插件无需后台任务。
func (p *BaLogoPlugin) OnStart(_ *pluginpkg.PluginContext) error { return nil }

// OnStop BA-LOGO 插件无需清理资源。
func (p *BaLogoPlugin) OnStop(_ *pluginpkg.PluginContext) error { return nil }

// ============================================================
// 条件判断
// ============================================================

// isBaLogoCommand 判断消息是否为 balogo 命令。
func isBaLogoCommand(ctx *conduit.MessageContext) bool {
	return strings.HasPrefix(strings.TrimSpace(ctx.RawMsg), "/balogo")
}

// ============================================================
// Pass 实现
// ============================================================

// balogoResult BA-LOGO 生成结果，Pass 间通过 MessageContext 传递
type balogoResult struct {
	ImageURL string // 生成的 BA-LOGO 图片 URL
}

const balogoResultKey = "plugin.balogo.result" // MessageContext 中结果的键

// balogoExecutePass 解析命令参数，生成 BA-LOGO 图片 URL
type balogoExecutePass struct{}

func (pass *balogoExecutePass) Execute(ctx *conduit.MessageContext) error {
	// 解析命令：/balogo 左文字 右文字
	raw := strings.TrimSpace(ctx.RawMsg)
	raw = strings.TrimPrefix(raw, "/balogo")
	raw = strings.TrimSpace(raw)

	parts := strings.Fields(raw)

	// 参数校验：必须恰好 2 部分
	if len(parts) != 2 {
		conduit.Set(ctx, balogoResultKey, &balogoResult{ImageURL: ""})
		return nil
	}

	// 生成图片 URL（QueryEscape 编码中文等特殊字符，保证 URL 合法）
	imageURL := fmt.Sprintf(baLogoURL, url.QueryEscape(parts[0]), url.QueryEscape(parts[1]))

	conduit.Set(ctx, balogoResultKey, &balogoResult{ImageURL: imageURL})
	return nil
}

// balogoReplyPass 组装 BA-LOGO 回复消息
type balogoReplyPass struct{}

func (pass *balogoReplyPass) Execute(ctx *conduit.MessageContext) error {
	result, ok := conduit.Get[*balogoResult](ctx, balogoResultKey)
	if !ok {
		conduit.AppendOutput(ctx, &conduit.Message{
			UserID: ctx.UserID, GroupID: ctx.GroupID, IsGroup: ctx.IsGroup,
			Content: "LOGO生成异常，请重试。",
		})
		return nil
	}

	// 参数不足时输出用法提示
	if result.ImageURL == "" {
		conduit.AppendOutput(ctx, &conduit.Message{
			UserID: ctx.UserID, GroupID: ctx.GroupID, IsGroup: ctx.IsGroup,
			Content: "格式错误！用法：/balogo 左文字 右文字\n示例：/balogo 蔚蓝 档案",
		})
		return nil
	}

	// 输出图片 URL（由网关层识别为图片消息发送）
	conduit.AppendOutput(ctx, &conduit.Message{
		UserID: ctx.UserID, GroupID: ctx.GroupID, IsGroup: ctx.IsGroup,
		Content: result.ImageURL,
	})
	return nil
}

// ============================================================
// AI 工具处理器
// ============================================================

// toolBaLogoGenerate 是 AI 工具处理器，生成 BA 风格 LOGO 图片 URL。
func (p *BaLogoPlugin) toolBaLogoGenerate(_ context.Context, argsJSON string) (string, error) {
	var args struct {
		LeftText  string `json:"left_text"`
		RightText string `json:"right_text"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("参数解析失败: %w", err)
	}

	if args.LeftText == "" || args.RightText == "" {
		return "", fmt.Errorf("左文字和右文字都不能为空")
	}

	return fmt.Sprintf(baLogoURL, url.QueryEscape(args.LeftText), url.QueryEscape(args.RightText)), nil
}
