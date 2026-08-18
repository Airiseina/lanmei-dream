package bizplugin

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	pluginpkg "github.com/DaWesen/lanmei-dream/internal/plugin"
	"github.com/zrurf/conduit"
	"go.uber.org/zap"
)

// ============================================================
// DailyQuotePlugin 每日一句插件
// ============================================================

const (
	dailyQuoteAPIURL       = "https://v1.hitokoto.cn/"
	dailyQuoteHTTPTimeout  = 10 * time.Second
	dailyQuoteMaxBodyBytes = 1 << 20 // 1 MiB，防止异常上游返回过大响应
)

// DailyQuotePlugin 调用一言接口返回一句话及其出处、作者。
//
// 行为树：
//
//	subtree.daily_quote → Sequence(
//	  isDailyQuoteCommand,
//	  Action("pipeline.plugin.daily_quote.main"),
//	)
//
// 管线：
//
//	pipeline.plugin.daily_quote.main → [dailyQuotePass]
type DailyQuotePlugin struct {
	client *http.Client
	logger *zap.Logger
}

// NewDailyQuotePlugin 创建每日一句插件。
func NewDailyQuotePlugin(logger *zap.Logger) *DailyQuotePlugin {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &DailyQuotePlugin{
		client: &http.Client{Timeout: dailyQuoteHTTPTimeout},
		logger: logger,
	}
}

// Info 返回每日一句插件元信息。
func (p *DailyQuotePlugin) Info() pluginpkg.PluginInfo {
	return pluginpkg.PluginInfo{
		ID:          "daily_quote",
		Name:        "每日一句",
		Description: "从一言接口获取一句话及其出处和作者",
		Version:     "1.0.0",
		Commands: []pluginpkg.CommandDef{
			{Name: "每日一句", Description: "获取每日一句、一言或一句话，并显示出处和作者；此命令不需要参数", Order: 50},
		},
		SubtreeID: pluginpkg.SubtreeID("daily_quote"),
	}
}

// OnInit 注册每日一句 Pass、Pipeline 和 Subtree。
func (p *DailyQuotePlugin) OnInit(ctx *pluginpkg.PluginContext) error {
	passID := pluginpkg.PassID("daily_quote", "main")
	pass := &dailyQuotePass{client: p.client, logger: p.logger}
	if err := ctx.Engine.RegisterPass(passID, pass); err != nil {
		return fmt.Errorf("register daily_quote pass: %w", err)
	}
	ctx.Registry.TrackPass("daily_quote", passID)

	pipelineID := pluginpkg.PipelineID("daily_quote", "main")
	if err := ctx.Engine.RegisterPipeline(conduit.NewPipelineFromIDs(pipelineID, passID)); err != nil {
		return fmt.Errorf("register daily_quote pipeline: %w", err)
	}
	ctx.Registry.TrackPipeline("daily_quote", pipelineID)

	subtree := conduit.NewSequence(
		conduit.NewCondition(isDailyQuoteCommand),
		conduit.NewAction(pipelineID),
	)
	if err := ctx.Engine.RegisterSubtree(pluginpkg.SubtreeID("daily_quote"), subtree); err != nil {
		return fmt.Errorf("register daily_quote subtree: %w", err)
	}
	return nil
}

// OnStart 每日一句插件无需后台任务。
func (p *DailyQuotePlugin) OnStart(_ *pluginpkg.PluginContext) error { return nil }

// OnStop 每日一句插件无需清理资源。
func (p *DailyQuotePlugin) OnStop(_ *pluginpkg.PluginContext) error { return nil }

// isDailyQuoteCommand 判断消息是否为每日一句命令。
func isDailyQuoteCommand(ctx *conduit.MessageContext) bool {
	return strings.TrimSpace(ctx.RawMsg) == "/每日一句"
}

// hitokotoResponse 是一言接口中本插件使用的字段。
type hitokotoResponse struct {
	Hitokoto string  `json:"hitokoto"`
	From     *string `json:"from"`
	FromWho  *string `json:"from_who"`
}

// dailyQuotePass 请求一言接口并输出格式化结果。
type dailyQuotePass struct {
	client *http.Client
	logger *zap.Logger
}

func (pass *dailyQuotePass) Execute(ctx *conduit.MessageContext) error {
	quote, err := pass.fetch(ctx)
	if err != nil {
		pass.logger.Warn("daily_quote: 获取每日一句失败", zap.Error(err))
		pass.reply(ctx, "每日一句获取失败，请稍后再试~")
		return nil
	}
	pass.reply(ctx, formatDailyQuote(quote))
	return nil
}

func (pass *dailyQuotePass) fetch(ctx *conduit.MessageContext) (*hitokotoResponse, error) {
	request, err := http.NewRequestWithContext(ctx.Ctx, http.MethodGet, dailyQuoteAPIURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	request.Header.Set("Accept", "application/json")

	response, err := pass.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("request: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("api returned status %d", response.StatusCode)
	}

	var quote hitokotoResponse
	decoder := json.NewDecoder(io.LimitReader(response.Body, dailyQuoteMaxBodyBytes))
	if err := decoder.Decode(&quote); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if strings.TrimSpace(quote.Hitokoto) == "" {
		return nil, fmt.Errorf("response missing hitokoto")
	}
	return &quote, nil
}

// formatDailyQuote 按“正文\n出处:出处，作者:作者”格式输出，缺失字段使用“未知”。
func formatDailyQuote(quote *hitokotoResponse) string {
	from := dailyQuoteSourceOrUnknown(quote.From)
	fromWho := dailyQuoteSourceOrUnknown(quote.FromWho)
	return fmt.Sprintf("%s\n出处:%s，作者:%s", strings.TrimSpace(quote.Hitokoto), from, fromWho)
}

func dailyQuoteSourceOrUnknown(value *string) string {
	if value == nil || strings.TrimSpace(*value) == "" {
		return "未知"
	}
	return strings.TrimSpace(*value)
}

func (pass *dailyQuotePass) reply(ctx *conduit.MessageContext, content string) {
	conduit.AppendOutput(ctx, &conduit.Message{
		UserID:  ctx.UserID,
		GroupID: ctx.GroupID,
		IsGroup: ctx.IsGroup,
		Content: content,
	})
}
