package bizplugin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	pluginpkg "github.com/DaWesen/lanmei-dream/internal/plugin"
	"github.com/zrurf/conduit"
	"go.uber.org/zap"
)

// ============================================================
// MusicPlugin 网易云点歌插件
// ============================================================

// MusicPlugin 实现网易云音乐搜索与点歌功能。
//
// 功能：
//   - /music 歌曲名 → 搜索网易云音乐，展示前 3 首结果
//   - 用户回复序号（1/2/3）→ 播放所选歌曲
//   - 会话 60 秒后自动过期
//   - 会话按群+用户隔离
//
// 行为树：
//
//	subtree.music → Selector [
//	  Sequence(isMusicCommand, Action(pipeline.music.search))
//	  Sequence(isMusicSelect, Action(pipeline.music.select))
//	]
//
// 管线：
//
//	pipeline.music.search  → [musicSearchPass]
//	pipeline.music.select  → [musicSelectPass]
type MusicPlugin struct {
	ncmURL string // 网易云音乐 API 基础 URL
	store  conduit.StateStore
	logger *zap.Logger
}

// NewMusicPlugin 创建网易云点歌插件。
// ncmURL 为网易云音乐 API 基础 URL（如 http://ncm-api:3000），为空时插件仍可初始化但使用时报错。
func NewMusicPlugin(ncmURL string, logger *zap.Logger) *MusicPlugin {
	return &MusicPlugin{ncmURL: ncmURL, logger: logger}
}

// Info 返回网易云点歌插件元信息。
func (p *MusicPlugin) Info() pluginpkg.PluginInfo {
	return pluginpkg.PluginInfo{
		ID:          "music",
		Name:        "网易云点歌",
		Description: "搜索网易云音乐并点歌",
		Version:     "1.0.0",
		Commands: []pluginpkg.CommandDef{
			{Name: "music", Description: "搜索网易云音乐，格式：/music 歌曲名"},
		},
		SubtreeID: pluginpkg.SubtreeID("music"),
		Tools: []pluginpkg.ToolDef{
			{
				Name:        "music_search",
				Description: "搜索网易云音乐，返回匹配的歌曲列表",
				Handler:     p.toolMusicSearch,
			},
		},
	}
}

// OnInit 初始化网易云点歌插件，注册 Pass、Pipeline 和 Subtree。
func (p *MusicPlugin) OnInit(ctx *pluginpkg.PluginContext) error {
	p.store = ctx.Store

	// ── 注册 Pass ──

	searchPassID := pluginpkg.PassID("music", "search")
	searchPass := &musicSearchPass{ncmURL: p.ncmURL, store: p.store, logger: p.logger}

	if err := ctx.Engine.RegisterPass(searchPassID, searchPass); err != nil {
		return fmt.Errorf("register music search pass: %w", err)
	}
	ctx.Registry.TrackPass("music", searchPassID)

	selectPassID := pluginpkg.PassID("music", "select")
	selectPass := &musicSelectPass{store: p.store, logger: p.logger}

	if err := ctx.Engine.RegisterPass(selectPassID, selectPass); err != nil {
		return fmt.Errorf("register music select pass: %w", err)
	}
	ctx.Registry.TrackPass("music", selectPassID)

	// ── 注册管线 ──

	searchPipelineID := pluginpkg.PipelineID("music", "search")
	searchPl := conduit.NewPipelineFromIDs(searchPipelineID, searchPassID)
	if err := ctx.Engine.RegisterPipeline(searchPl); err != nil {
		return fmt.Errorf("register music search pipeline: %w", err)
	}
	ctx.Registry.TrackPipeline("music", searchPipelineID)

	selectPipelineID := pluginpkg.PipelineID("music", "select")
	selectPl := conduit.NewPipelineFromIDs(selectPipelineID, selectPassID)
	if err := ctx.Engine.RegisterPipeline(selectPl); err != nil {
		return fmt.Errorf("register music select pipeline: %w", err)
	}
	ctx.Registry.TrackPipeline("music", selectPipelineID)

	// ── 注册行为树子树 ──
	// 搜索命令优先匹配；若不匹配则检查是否为序号选择（需有活跃会话）
	subtree := conduit.NewSelector(
		conduit.NewSequence(
			conduit.NewCondition(isMusicCommand),
			conduit.NewAction(searchPipelineID),
		),
		conduit.NewSequence(
			conduit.NewCondition(isMusicSelect(p.store)),
			conduit.NewAction(selectPipelineID),
		),
	)
	if err := ctx.Engine.RegisterSubtree(pluginpkg.SubtreeID("music"), subtree); err != nil {
		return fmt.Errorf("register music subtree: %w", err)
	}

	return nil
}

// OnStart 网易云点歌插件无需后台任务。
func (p *MusicPlugin) OnStart(_ *pluginpkg.PluginContext) error { return nil }

// OnStop 网易云点歌插件无需清理资源。
func (p *MusicPlugin) OnStop(_ *pluginpkg.PluginContext) error { return nil }

// ============================================================
// 条件判断
// ============================================================

// isMusicCommand 判断消息是否为 /music 命令。
func isMusicCommand(ctx *conduit.MessageContext) bool {
	msg := strings.TrimSpace(ctx.RawMsg)
	return strings.HasPrefix(msg, "/music ") && len(strings.TrimSpace(strings.TrimPrefix(msg, "/music"))) > 0
}

// isMusicSelect 返回一个条件函数，判断消息是否为点歌序号选择（1/2/3）且存在活跃会话。
// 使用闭包捕获 StateStore 以检查会话。
func isMusicSelect(store conduit.StateStore) func(*conduit.MessageContext) bool {
	return func(ctx *conduit.MessageContext) bool {
		msg := strings.TrimSpace(ctx.RawMsg)
		if msg != "1" && msg != "2" && msg != "3" {
			return false
		}
		// 检查是否存在活跃会话
		sessionKey := musicSessionKey(ctx.GroupID, ctx.UserID)
		data, err := store.Get(ctx.Ctx, sessionKey)
		if err != nil || data == "" {
			return false
		}
		return true
	}
}

// ============================================================
// 会话数据结构
// ============================================================

// musicSession 点歌会话数据，存储在 StateStore 中。
type musicSession struct {
	Songs []musicSong `json:"songs"`
}

// musicSong 单首歌曲信息。
type musicSong struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Artists    string `json:"artists"` // 逗号分隔的歌手名
	Album      string `json:"album"`
	DurationMs int64  `json:"duration_ms"`
}

// musicSessionTTL 会话过期时间。
const musicSessionTTL = 60 * time.Second

// musicSessionKey 生成会话的 StateStore 键，按群+用户隔离。
func musicSessionKey(groupID, userID string) string {
	return pluginpkg.StoreKey("music", "session:"+groupID+":"+userID)
}

// ============================================================
// 网易云音乐 API 响应结构
// ============================================================

// ncmSearchResponse 网易云音乐搜索 API 响应。
type ncmSearchResponse struct {
	Result ncmSearchResult `json:"result"`
}

// ncmSearchResult 搜索结果。
type ncmSearchResult struct {
	Songs []ncmSong `json:"songs"`
}

// ncmSong 单首歌曲 API 数据。
type ncmSong struct {
	ID         int64       `json:"id"`
	Name       string      `json:"name"`
	Artists    []ncmArtist `json:"artists"`
	Album      ncmAlbum    `json:"album"`
	DurationMs int64       `json:"duration"`
}

// ncmArtist 歌手。
type ncmArtist struct {
	Name string `json:"name"`
}

// ncmAlbum 专辑。
type ncmAlbum struct {
	Name string `json:"name"`
}

// ============================================================
// NCM API 搜索
// ============================================================

// searchNCM 调用网易云音乐搜索 API，返回前 limit 首结果。
// keyword 必须 URL 编码（中文关键词直接拼接会导致服务端返回 400）。
func searchNCM(ctx context.Context, ncmURL, keyword string, limit int) ([]ncmSong, error) {
	url := fmt.Sprintf("%s/search?keywords=%s&limit=%d", ncmURL, url.QueryEscape(keyword), limit)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("api returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	var result ncmSearchResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}

	return result.Result.Songs, nil
}

// ============================================================
// 辅助函数
// ============================================================

// formatDuration 将毫秒时长格式化为 m:ss。
func formatDuration(ms int64) string {
	totalSec := ms / 1000
	min := totalSec / 60
	sec := totalSec % 60
	return fmt.Sprintf("%d:%02d", min, sec)
}

// ncmSongToMusicSong 将 API 歌曲数据转换为会话存储格式。
func ncmSongToMusicSong(s ncmSong) musicSong {
	artistNames := make([]string, 0, len(s.Artists))
	for _, a := range s.Artists {
		if a.Name != "" {
			artistNames = append(artistNames, a.Name)
		}
	}
	return musicSong{
		ID:         s.ID,
		Name:       s.Name,
		Artists:    strings.Join(artistNames, ","),
		Album:      s.Album.Name,
		DurationMs: s.DurationMs,
	}
}

// ============================================================
// Pass 实现：搜索
// ============================================================

// musicSearchPass 搜索网易云音乐，存储结果并输出列表。
type musicSearchPass struct {
	ncmURL string
	store  conduit.StateStore
	logger *zap.Logger
}

func (pass *musicSearchPass) Execute(ctx *conduit.MessageContext) error {
	// 解析歌曲名
	keyword := strings.TrimSpace(strings.TrimPrefix(ctx.RawMsg, "/music"))
	keyword = strings.TrimSpace(keyword)
	if keyword == "" {
		conduit.AppendOutput(ctx, &conduit.Message{
			UserID: ctx.UserID, GroupID: ctx.GroupID, IsGroup: ctx.IsGroup,
			Content: "请输入歌曲名，格式：/music 歌曲名",
		})
		return nil
	}

	// 检查 API 是否配置
	if pass.ncmURL == "" {
		conduit.AppendOutput(ctx, &conduit.Message{
			UserID: ctx.UserID, GroupID: ctx.GroupID, IsGroup: ctx.IsGroup,
			Content: "音乐服务未配置",
		})
		return nil
	}

	// 调用搜索 API
	songs, err := searchNCM(ctx.Ctx, pass.ncmURL, keyword, 3)
	if err != nil {
		pass.logger.Error("music: search failed", zap.String("keyword", keyword), zap.Error(err))
		conduit.AppendOutput(ctx, &conduit.Message{
			UserID: ctx.UserID, GroupID: ctx.GroupID, IsGroup: ctx.IsGroup,
			Content: "搜索失败，请稍后重试",
		})
		return nil
	}

	if len(songs) == 0 {
		conduit.AppendOutput(ctx, &conduit.Message{
			UserID: ctx.UserID, GroupID: ctx.GroupID, IsGroup: ctx.IsGroup,
			Content: fmt.Sprintf("未找到「%s」的相关歌曲", keyword),
		})
		return nil
	}

	// 转换为会话格式并存储
	musicSongs := make([]musicSong, 0, len(songs))
	for _, s := range songs {
		musicSongs = append(musicSongs, ncmSongToMusicSong(s))
	}

	session := musicSession{Songs: musicSongs}
	sessionJSON, err := json.Marshal(session)
	if err != nil {
		pass.logger.Error("music: marshal session failed", zap.Error(err))
		conduit.AppendOutput(ctx, &conduit.Message{
			UserID: ctx.UserID, GroupID: ctx.GroupID, IsGroup: ctx.IsGroup,
			Content: "搜索结果处理失败",
		})
		return nil
	}

	sessionKey := musicSessionKey(ctx.GroupID, ctx.UserID)
	if err := pass.store.Set(ctx.Ctx, sessionKey, string(sessionJSON), musicSessionTTL); err != nil {
		pass.logger.Error("music: save session failed", zap.Error(err))
	}

	// 格式化输出列表
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("找到 %d 首歌曲，请回复序号选择：\n", len(musicSongs)))
	for i, s := range musicSongs {
		sb.WriteString(fmt.Sprintf("%d. %s - %s 《%s》 [%s]\n",
			i+1, s.Name, s.Artists, s.Album, formatDuration(s.DurationMs)))
	}

	conduit.AppendOutput(ctx, &conduit.Message{
		UserID: ctx.UserID, GroupID: ctx.GroupID, IsGroup: ctx.IsGroup,
		Content: sb.String(),
	})
	return nil
}

// ============================================================
// Pass 实现：选择
// ============================================================

// musicSelectPass 根据用户输入的序号选择歌曲并输出详情。
type musicSelectPass struct {
	store  conduit.StateStore
	logger *zap.Logger
}

func (pass *musicSelectPass) Execute(ctx *conduit.MessageContext) error {
	// 解析序号
	selection, err := strconv.Atoi(strings.TrimSpace(ctx.RawMsg))
	if err != nil || selection < 1 || selection > 3 {
		conduit.AppendOutput(ctx, &conduit.Message{
			UserID: ctx.UserID, GroupID: ctx.GroupID, IsGroup: ctx.IsGroup,
			Content: "请回复 1、2 或 3 选择歌曲",
		})
		return nil
	}

	// 读取会话
	sessionKey := musicSessionKey(ctx.GroupID, ctx.UserID)
	data, err := pass.store.Get(ctx.Ctx, sessionKey)
	if err != nil || data == "" {
		conduit.AppendOutput(ctx, &conduit.Message{
			UserID: ctx.UserID, GroupID: ctx.GroupID, IsGroup: ctx.IsGroup,
			Content: "点歌会话已过期，请重新搜索",
		})
		return nil
	}

	var session musicSession
	if err := json.Unmarshal([]byte(data), &session); err != nil {
		pass.logger.Error("music: unmarshal session failed", zap.Error(err))
		conduit.AppendOutput(ctx, &conduit.Message{
			UserID: ctx.UserID, GroupID: ctx.GroupID, IsGroup: ctx.IsGroup,
			Content: "会话数据异常，请重新搜索",
		})
		return nil
	}

	if selection > len(session.Songs) {
		conduit.AppendOutput(ctx, &conduit.Message{
			UserID: ctx.UserID, GroupID: ctx.GroupID, IsGroup: ctx.IsGroup,
			Content: fmt.Sprintf("序号 %d 超出范围，请选择 1~%d", selection, len(session.Songs)),
		})
		return nil
	}

	song := session.Songs[selection-1]

	// 输出选中歌曲
	content := fmt.Sprintf("🎵 %s - %s 《%s》\n网易云音乐: https://music.163.com/#/song?id=%d",
		song.Name, song.Artists, song.Album, song.ID)

	conduit.AppendOutput(ctx, &conduit.Message{
		UserID: ctx.UserID, GroupID: ctx.GroupID, IsGroup: ctx.IsGroup,
		Content: content,
	})

	// 选择后删除会话
	_ = pass.store.Delete(ctx.Ctx, sessionKey)

	return nil
}

// ============================================================
// AI 工具处理器
// ============================================================

// toolMusicSearch 是 AI 工具处理器，搜索网易云音乐。
func (p *MusicPlugin) toolMusicSearch(ctx context.Context, argsJSON string) (string, error) {
	var args struct {
		Keyword string `json:"keyword"`
		Limit   int    `json:"limit"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("参数解析失败: %w", err)
	}

	if args.Keyword == "" {
		return "", fmt.Errorf("关键词不能为空")
	}
	if p.ncmURL == "" {
		return "", fmt.Errorf("音乐服务未配置")
	}

	limit := args.Limit
	if limit <= 0 || limit > 10 {
		limit = 3
	}

	songs, err := searchNCM(ctx, p.ncmURL, args.Keyword, limit)
	if err != nil {
		return "", fmt.Errorf("搜索失败: %w", err)
	}

	if len(songs) == 0 {
		return fmt.Sprintf("未找到「%s」的相关歌曲", args.Keyword), nil
	}

	var parts []string
	for i, s := range songs {
		artistNames := make([]string, 0, len(s.Artists))
		for _, a := range s.Artists {
			if a.Name != "" {
				artistNames = append(artistNames, a.Name)
			}
		}
		artists := strings.Join(artistNames, ",")
		parts = append(parts, fmt.Sprintf("%d. %s - %s 《%s》 [%s] (id:%d)",
			i+1, s.Name, artists, s.Album.Name, formatDuration(s.DurationMs), s.ID))
	}

	return strings.Join(parts, "\n"), nil
}
