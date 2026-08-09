package bizplugin

import (
	"crypto/rand"
	"fmt"
	"regexp"

	pluginpkg "github.com/DaWesen/lanmei-dream/internal/plugin"
	"github.com/zrurf/conduit"
)

// ============================================================
// GitHubCardPlugin GitHub卡片插件
// ============================================================

// GitHubCardPlugin 检测消息中的 GitHub 仓库链接，自动返回 OpenGraph 预览卡片。
//
// 功能：
//   - 自动触发：消息中包含 GitHub 仓库 URL 时自动响应
//   - 返回 OpenGraph 预览卡片图片
//
// 行为树：
//
//	subtree.github_card → Sequence(hasGitHubURL, Action("pipeline.plugin.github_card"))
//
// 管线：
//
//	pipeline.plugin.github_card → [githubCardPass]
type GitHubCardPlugin struct{}

// NewGitHubCardPlugin 创建 GitHub 卡片插件。
func NewGitHubCardPlugin() *GitHubCardPlugin {
	return &GitHubCardPlugin{}
}

// Info 返回 GitHub 卡片插件元信息。
func (p *GitHubCardPlugin) Info() pluginpkg.PluginInfo {
	return pluginpkg.PluginInfo{
		ID:          "github_card",
		Name:        "GitHub卡片",
		Description: "检测到GitHub链接自动返回卡片预览",
		Version:     "1.0.0",
		Commands:    nil, // 无斜杠命令，自动触发
		SubtreeID:   pluginpkg.SubtreeID("github_card"),
		Tools:       nil, // 无 AI 工具
	}
}

// OnInit 初始化 GitHub 卡片插件，注册 Pass、Pipeline 和 Subtree。
func (p *GitHubCardPlugin) OnInit(ctx *pluginpkg.PluginContext) error {
	// 注册 Pass
	passID := pluginpkg.PassID("github_card", "main")
	pass := &githubCardPass{}

	if err := ctx.Engine.RegisterPass(passID, pass); err != nil {
		return fmt.Errorf("register github_card pass: %w", err)
	}
	ctx.Registry.TrackPass("github_card", passID)

	// 注册管线
	pipelineID := pluginpkg.PipelineID("github_card", "main")
	pl := conduit.NewPipelineFromIDs(pipelineID, passID)
	if err := ctx.Engine.RegisterPipeline(pl); err != nil {
		return fmt.Errorf("register github_card pipeline: %w", err)
	}
	ctx.Registry.TrackPipeline("github_card", pipelineID)

	// 注册行为树子树：GitHub URL 正则匹配路由
	subtree := conduit.NewSequence(
		conduit.NewCondition(hasGitHubURL),
		conduit.NewAction(pipelineID),
	)
	if err := ctx.Engine.RegisterSubtree(pluginpkg.SubtreeID("github_card"), subtree); err != nil {
		return fmt.Errorf("register github_card subtree: %w", err)
	}

	return nil
}

// OnStart GitHub 卡片插件无需后台任务。
func (p *GitHubCardPlugin) OnStart(_ *pluginpkg.PluginContext) error { return nil }

// OnStop GitHub 卡片插件无需清理资源。
func (p *GitHubCardPlugin) OnStop(_ *pluginpkg.PluginContext) error { return nil }

// ============================================================
// 条件判断
// ============================================================

// githubURLRe 匹配 GitHub 仓库 URL。
// 示例：github.com/user/repo、www.github.com/org/project/issues/1
var githubURLRe = regexp.MustCompile(
	`(?i)(?:https?://)?(?:www\.)?github\.com/([a-z0-9_.-]+/[a-z0-9_.-]+(?:/[^\s?#]*)?)`,
)

// hasGitHubURL 判断消息是否包含 GitHub 仓库 URL。
func hasGitHubURL(ctx *conduit.MessageContext) bool {
	return githubURLRe.MatchString(ctx.RawMsg)
}

// ============================================================
// Pass 实现
// ============================================================

// githubCardExtractKey 是 MessageContext 中提取到的 GitHub 路径的键。
const githubCardExtractKey = "plugin.github_card.path"

// githubCardPass 从消息中提取 GitHub 仓库 URL，构建 OpenGraph 卡片链接并输出。
type githubCardPass struct{}

func (pass *githubCardPass) Execute(ctx *conduit.MessageContext) error {
	match := githubURLRe.FindStringSubmatch(ctx.RawMsg)
	if len(match) < 2 {
		// 正则未匹配（理论上不会发生，条件函数已过滤），静默返回
		return nil
	}

	path := match[1]

	// 生成随机字符串用于 OpenGraph URL 的缓存键
	randStr, err := randomHex(8)
	if err != nil {
		randStr = "0" // 降级处理
	}

	imageURL := fmt.Sprintf("https://opengraph.githubassets.com/%s/%s", randStr, path)

	conduit.AppendOutput(ctx, &conduit.Message{
		UserID: ctx.UserID, GroupID: ctx.GroupID, IsGroup: ctx.IsGroup,
		Content: imageURL,
	})
	return nil
}

// ============================================================
// 辅助函数
// ============================================================

// randomHex 生成 n 字节的随机十六进制字符串。
func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", b), nil
}
