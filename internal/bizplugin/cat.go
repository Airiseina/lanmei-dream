package bizplugin

import (
	"context"
	"fmt"
	"math/rand/v2"
	"strconv"
	"strings"

	pluginpkg "github.com/DaWesen/lanmei-dream/internal/plugin"
	"github.com/zrurf/conduit"
)

// CatPlugin 返回 HTTP Cat 图片：/猫猫、/哈基米 随机取图，带数字参数时取该状态码的图片，
// 状态码不在 http.cat 支持列表内则回退 404 猫猫。
//
// 插件 ID cat；命令 /猫猫、/哈基米，工具 cat_image；
// 仅依赖外部 http.cat 图床（无本地配置项与降级开关）。
type CatPlugin struct{}

// NewCatPlugin 创建猫猫插件。
func NewCatPlugin() *CatPlugin {
	return &CatPlugin{}
}

// Info 返回猫猫插件元信息。
func (p *CatPlugin) Info() pluginpkg.PluginInfo {
	return pluginpkg.PluginInfo{
		ID:          "cat",
		Name:        "猫猫",
		Description: "随机猫猫图片",
		Version:     "1.0.0",
		Commands: []pluginpkg.CommandDef{
			{Name: "猫猫", Description: "随机猫猫图片", Order: 91}, // /哈基米 的别名，紧跟其后
			{Name: "哈基米", Description: "随机猫猫图片", Order: 90},
		},
		SubtreeID: pluginpkg.SubtreeID("cat"),
		Tools: []pluginpkg.ToolDef{
			{
				Name:        "cat_image",
				Description: "获取随机猫猫图片",
				Handler:     p.toolCatImage,
			},
		},
	}
}

// OnInit 初始化猫猫插件，注册 Pass、Pipeline 和 Subtree。
func (p *CatPlugin) OnInit(ctx *pluginpkg.PluginContext) error {
	passID := pluginpkg.PassID("cat", "main")
	pass := &catPass{}

	if err := ctx.Engine.RegisterPass(passID, pass); err != nil {
		return fmt.Errorf("register cat pass: %w", err)
	}
	ctx.Registry.TrackPass("cat", passID)

	pipelineID := pluginpkg.PipelineID("cat", "main")
	pl := conduit.NewPipelineFromIDs(pipelineID, passID)
	if err := ctx.Engine.RegisterPipeline(pl); err != nil {
		return fmt.Errorf("register cat pipeline: %w", err)
	}
	ctx.Registry.TrackPipeline("cat", pipelineID)

	subtree := conduit.NewSequence(
		conduit.NewCondition(isCatCommand),
		conduit.NewAction(pipelineID),
	)
	if err := ctx.Engine.RegisterSubtree(pluginpkg.SubtreeID("cat"), subtree); err != nil {
		return fmt.Errorf("register cat subtree: %w", err)
	}

	return nil
}

// OnStart 猫猫插件无需后台任务。
func (p *CatPlugin) OnStart(_ *pluginpkg.PluginContext) error { return nil }

// OnStop 猫猫插件无需清理资源。
func (p *CatPlugin) OnStop(_ *pluginpkg.PluginContext) error { return nil }

// isCatCommand 判断消息是否为猫猫命令。
// 匹配 /猫猫、/哈基米（无参数）或 /猫猫 <数字>、/哈基米 <数字>。
func isCatCommand(ctx *conduit.MessageContext) bool {
	msg := strings.TrimSpace(ctx.RawMsg)
	if !strings.HasPrefix(msg, "/") {
		return false
	}
	parts := strings.SplitN(msg, " ", 2)
	cmd := parts[0]
	return cmd == "/猫猫" || cmd == "/哈基米"
}

// httpCatStatusCodes 是 HTTP Cat 支持的状态码列表（与上游 LanMei 保持一致）。
// 来源：https://http.cat
var httpCatStatusCodes = []int{
	100, 101, 102, 103,
	200, 201, 202, 203, 204, 205, 206, 207, 208, 214, 226,
	300, 301, 302, 303, 304, 305, 307, 308,
	400, 401, 402, 403, 404, 405, 406, 407, 408, 409, 410, 411, 412, 413, 414, 415, 416, 417, 418, 419, 420, 421, 422, 423, 424, 425, 426, 428, 429, 431, 444, 450, 451, 495, 496, 497, 498, 499,
	500, 501, 502, 503, 504, 506, 507, 508, 509, 510, 511, 521, 522, 523, 525, 530, 599,
}

// httpCatURL 返回指定状态码的 HTTP Cat 图片 URL。
func httpCatURL(code int) string {
	return fmt.Sprintf("https://http.cat/%d.jpg", code)
}

// isValidCatCode 检查状态码是否在 HTTP Cat 列表中。
func isValidCatCode(code int) bool {
	for _, c := range httpCatStatusCodes {
		if c == code {
			return true
		}
	}
	return false
}

// catPass 解析命令参数并输出猫猫图片 URL。
type catPass struct{}

// Execute 输出猫猫图片 URL：带数字参数时取该状态码的 http.cat 图片，
// 参数非法或不在支持列表内时回退 404 猫猫，无参数时从支持列表随机取一个状态码。
// 由 plugin.cat.pipeline.main 在 isCatCommand 命中 /猫猫、/哈基米 后调用，不读写上下文键。
func (pass *catPass) Execute(ctx *conduit.MessageContext) error {
	msg := strings.TrimSpace(ctx.RawMsg)
	parts := strings.SplitN(msg, " ", 2)

	var imageURL string

	if len(parts) == 2 && strings.TrimSpace(parts[1]) != "" {
		arg := strings.TrimSpace(parts[1])
		code, err := strconv.Atoi(arg)
		if err != nil || !isValidCatCode(code) {
			imageURL = httpCatURL(404)
		} else {
			imageURL = httpCatURL(code)
		}
	} else {
		code := httpCatStatusCodes[rand.IntN(len(httpCatStatusCodes))]
		imageURL = httpCatURL(code)
	}

	conduit.AppendOutput(ctx, &conduit.Message{
		UserID: ctx.UserID, GroupID: ctx.GroupID, IsGroup: ctx.IsGroup,
		Content: imageURL,
	})
	return nil
}

// toolCatImage 是 AI 工具处理器，返回随机猫猫图片 URL。
func (p *CatPlugin) toolCatImage(_ context.Context, _ string) (string, error) {
	code := httpCatStatusCodes[rand.IntN(len(httpCatStatusCodes))]
	return httpCatURL(code), nil
}
