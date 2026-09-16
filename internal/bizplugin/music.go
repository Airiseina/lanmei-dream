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

// MusicPlugin 实现网易云音乐搜索与点歌：/music 歌曲名 搜索并展示前 3 首，用户回复序号 1/2/3 播放。
//
// 选择会话按群+用户隔离、20 秒过期，且只有发起搜索的当事人能触发，避免仅数字的对话被误判。
//
// 插件 ID music；命令 /music <歌曲名>，工具 music_search；
// 依赖外部 ncm-api（ncmURL 为空时搜索与取音频均提示「音乐服务未配置」）与 StateStore
// （ctx.Store，未注入时会话读写失败，序号选择无法命中）；musicSendMode 控制发送方式（空值按 auto）。
type MusicPlugin struct {
	ncmURL        string
	musicSendMode string // 点歌发送方式：auto/card/link
	store         conduit.StateStore
	logger        *zap.Logger
}

// NewMusicPlugin 创建网易云点歌插件。ncmURL 为空时插件仍可初始化，但使用时会报错；
// musicSendMode 为点歌结果发送方式（auto/card/link），用于适配不同反向代理工具。
func NewMusicPlugin(ncmURL, musicSendMode string, logger *zap.Logger) *MusicPlugin {
	return &MusicPlugin{ncmURL: ncmURL, musicSendMode: musicSendMode, logger: logger}
}

// Info 返回网易云点歌插件元信息。
func (p *MusicPlugin) Info() pluginpkg.PluginInfo {
	return pluginpkg.PluginInfo{
		ID:          "music",
		Name:        "网易云点歌",
		Description: "搜索网易云音乐并点歌",
		Version:     "1.0.0",
		Commands: []pluginpkg.CommandDef{
			{Name: "music", Description: "搜索网易云音乐，格式：/music 歌曲名", Order: 20},
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

	searchPassID := pluginpkg.PassID("music", "search")
	searchPass := &musicSearchPass{ncmURL: p.ncmURL, store: p.store, logger: p.logger}

	if err := ctx.Engine.RegisterPass(searchPassID, searchPass); err != nil {
		return fmt.Errorf("register music search pass: %w", err)
	}
	ctx.Registry.TrackPass("music", searchPassID)

	selectPassID := pluginpkg.PassID("music", "select")
	selectPass := &musicSelectPass{store: p.store, ncmURL: p.ncmURL, sendMode: p.musicSendMode, logger: p.logger}

	if err := ctx.Engine.RegisterPass(selectPassID, selectPass); err != nil {
		return fmt.Errorf("register music select pass: %w", err)
	}
	ctx.Registry.TrackPass("music", selectPassID)

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

	// 搜索命令优先匹配，不匹配时才检查序号选择（需要活跃会话）
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

// isMusicCommand 判断消息是否为 /music 命令。
func isMusicCommand(ctx *conduit.MessageContext) bool {
	msg := strings.TrimSpace(ctx.RawMsg)
	return strings.HasPrefix(msg, "/music ") && len(strings.TrimSpace(strings.TrimPrefix(msg, "/music"))) > 0
}

// isMusicSelect 返回判断序号选择（1/2/3）的条件函数，闭包捕获 StateStore 用于检查活跃会话。
// 序号无需 @ 机器人即可触发；会话按群+用户隔离（musicSessionKey）且 20s 过期（musicSessionTTL），
// 因此只有发起点歌的当事人能命中，非当事人不会误触发。
func isMusicSelect(store conduit.StateStore) func(*conduit.MessageContext) bool {
	return func(ctx *conduit.MessageContext) bool {
		raw := strings.TrimSpace(ctx.RawMsg)
		sel := extractSelection(raw)
		if sel != "1" && sel != "2" && sel != "3" {
			return false
		}
		// 检查是否存在活跃会话（会话按群+用户隔离：仅当事人可触发）
		sessionKey := musicSessionKey(ctx.GroupID, ctx.UserID)
		data, err := store.Get(ctx.Ctx, sessionKey)
		if err != nil || data == "" {
			return false
		}
		return true
	}
}

// extractSelection 从消息中提取独立出现的序号（1/2/3），容忍 @机器人 等前缀。
// 按空白切词后仅接受独立的 1/2/3 token，忽略 @QQ号 等长数字串，
// 否则会提取到 "20551942911" 这类粘连值导致选择失效。
func extractSelection(msg string) string {
	for _, f := range strings.Fields(msg) {
		f = strings.Trim(f, "，。,.、!！?？")
		if f == "1" || f == "2" || f == "3" {
			return f
		}
	}
	return ""
}

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

// musicSessionTTL 点歌选择会话有效期。
// 缩到 20 秒：得到歌曲列表后短时间内回复序号才触发，避免"仅数字的对话"被误判。
const musicSessionTTL = 20 * time.Second

// musicSessionKey 生成会话的 StateStore 键，按群+用户隔离。
func musicSessionKey(groupID, userID string) string {
	return pluginpkg.StoreKey("music", "session:"+groupID+":"+userID)
}

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

// searchNCM 调用网易云音乐搜索 API，返回前 limit 首结果。
// keyword 必须 URL 编码，中文关键词直接拼接会导致服务端返回 400。
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

// getSongURL 获取歌曲音频 URL（ncm-api /song/url/v1）。
// 返回空串表示该曲无可用音频（VIP/版权受限）。
func getSongURL(ctx context.Context, ncmURL string, songID int64) (string, error) {
	if ncmURL == "" {
		return "", fmt.Errorf("音乐服务未配置")
	}
	u := fmt.Sprintf("%s/song/url/v1?id=%d&level=standard", ncmURL, songID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("api returned status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}
	var result struct {
		Data []struct {
			URL string `json:"url"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("parse response: %w", err)
	}
	if len(result.Data) > 0 {
		return result.Data[0].URL, nil
	}
	return "", fmt.Errorf("no audio url")
}

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

// musicSearchPass 搜索网易云音乐，存储结果并输出列表。
type musicSearchPass struct {
	ncmURL string
	store  conduit.StateStore
	logger *zap.Logger
}

// Execute 搜索网易云音乐：解析 /music 后的关键词，调用 ncm-api 取前 3 首，
// 将结果序列化为点歌会话写入 StateStore（键按群+用户隔离、20 秒过期）后输出带序号的候选列表。
// 由 plugin.music.pipeline.search 在 isMusicCommand 命中 /music <关键词> 后调用；
// 关键词为空、ncmURL 未配置、搜索失败、无结果或会话序列化失败时只回复对应提示（不写会话）。
func (pass *musicSearchPass) Execute(ctx *conduit.MessageContext) error {
	keyword := strings.TrimSpace(strings.TrimPrefix(ctx.RawMsg, "/music"))
	keyword = strings.TrimSpace(keyword)
	if keyword == "" {
		conduit.AppendOutput(ctx, &conduit.Message{
			UserID: ctx.UserID, GroupID: ctx.GroupID, IsGroup: ctx.IsGroup,
			Content: "请输入歌曲名，格式：/music 歌曲名",
		})
		return nil
	}

	if pass.ncmURL == "" {
		conduit.AppendOutput(ctx, &conduit.Message{
			UserID: ctx.UserID, GroupID: ctx.GroupID, IsGroup: ctx.IsGroup,
			Content: "音乐服务未配置",
		})
		return nil
	}

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

// musicSelectPass 根据用户输入的序号选择歌曲并输出详情。
type musicSelectPass struct {
	store    conduit.StateStore
	ncmURL   string // 语音方案取音频 URL 用
	sendMode string // auto/card/link
	logger   *zap.Logger
}

// Execute 处理序号选择：从 StateStore 读取点歌会话（按群+用户隔离、20 秒过期），
// 按序号取出歌曲后按 musicSendMode 组装发送内容——auto（默认）发语音段（record，取不到音频时降级文字链接）、
// card 发音乐段（music）、link 发纯文字链接；发送后删除会话，保证一次搜索只能点一首。
// 由 plugin.music.pipeline.select 在 isMusicSelect 命中独立序号 1/2/3（且存在活跃会话）后调用；
// 序号非法、会话过期或数据异常、序号越界时只回复对应提示。
func (pass *musicSelectPass) Execute(ctx *conduit.MessageContext) error {
	// 解析序号（容忍 @机器人 前缀，与 isMusicSelect 的匹配逻辑一致）
	selection, err := strconv.Atoi(extractSelection(ctx.RawMsg))
	if err != nil || selection < 1 || selection > 3 {
		conduit.AppendOutput(ctx, &conduit.Message{
			UserID: ctx.UserID, GroupID: ctx.GroupID, IsGroup: ctx.IsGroup,
			Content: "请回复 1、2 或 3 选择歌曲",
		})
		return nil
	}

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

	// 点歌结果发送方式（适配不同反向代理工具）：
	//   - auto（默认）：发语音（record 段，OneBot 端自动转码，点击即听）；取不到音频时降级文字链接
	//   - card：强制 OB11 music 段（真正的音乐卡片，需工具支持签名，如 NapCat 配可用 musicSignUrl）
	//   - link：强制纯文字链接
	mode := pass.sendMode
	if mode == "" {
		mode = "auto"
	}
	if mode != "link" {
		if mode == "card" {
			// 音乐段必须单独发送（llonebot 校验"音乐消息不能与其他类型混发"），
			// 经出站段键（bot.send.segments）发送；gateway 对未知段类型原样透传。
			conduit.Set(ctx, "bot.send.segments", []map[string]any{{
				"type": "music",
				"data": map[string]any{"type": "163", "id": fmt.Sprintf("%d", song.ID)},
			}})
		} else {
			// 语音：调 ncm-api 获取音频 URL，OneBot 端（napcat/llonebot 自带 ffmpeg）转码为 QQ 语音
			audioURL, aerr := getSongURL(ctx.Ctx, pass.ncmURL, song.ID)
			if aerr == nil && audioURL != "" {
				conduit.Set(ctx, "bot.send.segments", []map[string]any{{
					"type": "record",
					"data": map[string]any{"file": audioURL},
				}})
			} else {
				// 取不到音频（VIP/版权受限）→ 降级文字链接
				if aerr != nil {
					pass.logger.Warn("music: 获取音频失败，降级文字链接",
						zap.Int64("song", song.ID), zap.Error(aerr))
				}
				content := fmt.Sprintf("🎵 %s - %s 《%s》\n网易云音乐: https://music.163.com/#/song?id=%d",
					song.Name, song.Artists, song.Album, song.ID)
				conduit.AppendOutput(ctx, &conduit.Message{
					UserID: ctx.UserID, GroupID: ctx.GroupID, IsGroup: ctx.IsGroup,
					Content: content,
				})
			}
		}
	} else {
		content := fmt.Sprintf("🎵 %s - %s 《%s》\n网易云音乐: https://music.163.com/#/song?id=%d",
			song.Name, song.Artists, song.Album, song.ID)
		conduit.AppendOutput(ctx, &conduit.Message{
			UserID: ctx.UserID, GroupID: ctx.GroupID, IsGroup: ctx.IsGroup,
			Content: content,
		})
	}

	_ = pass.store.Delete(ctx.Ctx, sessionKey)

	return nil
}

// toolMusicSearch 搜索网易云音乐，返回带序号的歌曲列表。
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
