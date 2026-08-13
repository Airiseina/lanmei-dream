// 内容管理（M3）HTTP API 处理器：
// 群组 / 用户 / 知识库 / 记忆 / 插件 / Skills / Prompt 模板 / 表情包 / 命令。
//
// 安全约定：
//   - 所有读操作走 protected 组（CSRF + Bearer）；
//   - 所有写操作额外 super + stepUp 双重校验；
//   - 写操作一律经 audit.Record 留痕（auditOK/auditDeny）。
package handlers

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/gofiber/fiber/v3"

	"github.com/DaWesen/lanmei-dream/internal/ai/prompt"
	"github.com/DaWesen/lanmei-dream/internal/ai/skill"
	"github.com/DaWesen/lanmei-dream/internal/command"
	"github.com/DaWesen/lanmei-dream/internal/database"
	"github.com/DaWesen/lanmei-dream/internal/kb"
	"github.com/DaWesen/lanmei-dream/internal/manager/store"
	"github.com/DaWesen/lanmei-dream/internal/model"
	"github.com/DaWesen/lanmei-dream/internal/plugin"
)

// ─────────────────────────────────────────────
// 视图结构
// ─────────────────────────────────────────────

// GroupView 群组视图。
type GroupView struct {
	GroupID       string   `json:"group_id"`
	Platform      string   `json:"platform"`
	HasConfig     bool     `json:"has_config"`
	BotEnabled    *bool    `json:"bot_enabled"`
	TopicEnabled  *bool    `json:"topic_enabled"`
	CreditEnabled *bool    `json:"credit_enabled"`
	Whitelist     []string `json:"whitelist"`
	Blacklist     []string `json:"blacklist"`
	WelcomeMsg    string   `json:"welcome_msg"`
	Remark        string   `json:"remark"`
}

// UserView 用户视图。
type UserView struct {
	ID             int64      `json:"id"`
	Platform       string     `json:"platform"`
	PlatformUserID string     `json:"platform_user_id"`
	Nickname       string     `json:"nickname"`
	BannedAt       *time.Time `json:"banned_at"`
	BanReason      string     `json:"ban_reason"`
	CreatedAt      time.Time  `json:"created_at"`
}

// KnowledgeBaseView 知识库视图（含分块数）。
type KnowledgeBaseView struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Provider    string `json:"provider"`
	Enabled     bool   `json:"enabled"`
	Chunks      int64  `json:"chunks"`
}

// MemoryView 记忆视图（不含向量字段）。
type MemoryView struct {
	ID        int64     `json:"id"`
	UserID    int64     `json:"user_id"`
	GroupID   string    `json:"group_id"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"created_at"`
}

// PluginView 插件视图（内置插件与 Wasm 安装记录统一展示）。
type PluginView struct {
	PluginID       string    `json:"plugin_id"`
	Name           string    `json:"name"`
	Description    string    `json:"description"`
	Version        string    `json:"version"`
	Kind           string    `json:"kind"` // builtin / wasm
	State          string    `json:"state"`
	Enabled        bool      `json:"enabled"`
	InstallationID string    `json:"installation_id"`
	Commands       []string  `json:"commands"`
	SubtreeID      string    `json:"subtree_id"`
	Tools          []string  `json:"tools"`
	LoadError      string    `json:"load_error"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// SkillView 技能视图。
type SkillView struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Version     string   `json:"version"`
	Author      string   `json:"author"`
	Tags        []string `json:"tags"`
	Dir         string   `json:"dir"`
	Enabled     bool     `json:"enabled"`
	ContentLen  int      `json:"content_len"`
}

// PromptFragmentView Prompt 片段视图。
type PromptFragmentView struct {
	ID      string `json:"id"`
	File    string `json:"file"`
	Builtin bool   `json:"builtin"`
	Content string `json:"content"`
}

// StickerView 表情包视图（Tags 解析为数组）。
type StickerView struct {
	ID        uint      `json:"id"`
	ObjectKey string    `json:"object_key"`
	FileID    string    `json:"file_id"`
	Tags      []string  `json:"tags"`
	Source    string    `json:"source"`
	CreatedAt time.Time `json:"created_at"`
}

// CommandView 命令视图。
type CommandView struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Source      string `json:"source"` // builtin / plugin:<id>
}

// ─────────────────────────────────────────────
// 依赖访问辅助
// ─────────────────────────────────────────────

func (h *Handler) pluginRegistry() *plugin.Registry {
	if h.bot == nil {
		return nil
	}
	return h.bot.Plugins()
}

// parseTagsJSON 解析表情包标签 JSON（非法时返回空数组）。
func parseTagsJSON(s string) []string {
	var tags []string
	if err := json.Unmarshal([]byte(s), &tags); err != nil {
		return []string{}
	}
	if tags == nil {
		return []string{}
	}
	return tags
}

// jsonString 序列化任意值为 JSON 字符串（失败时返回 "[]"，用于 JSON 数组字段）。
func jsonString(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "[]"
	}
	return string(b)
}

// ─────────────────────────────────────────────
// 群组
// ─────────────────────────────────────────────

// ListGroups 群列表（跨链路表聚合 + 群配置）。
func (h *Handler) ListGroups(c fiber.Ctx) error {
	offset, limit := pageQuery(c)
	ctx := c.Context()
	rows, total, err := h.store.ListGroups(ctx, c.Query("keyword"), offset, limit)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "群列表加载失败"})
	}

	// 批量读取群配置（一次 IN 查询，避免逐行查询）
	ids := make([]string, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.GroupID)
	}
	cfgByGroup := map[string]model.GroupConfig{}
	if len(ids) > 0 {
		var cfgs []model.GroupConfig
		if err := h.store.Orm().WithContext(ctx).Where("group_id IN ?", ids).Find(&cfgs).Error; err == nil {
			for _, cfg := range cfgs {
				cfgByGroup[cfg.GroupID] = cfg
			}
		}
	}

	items := make([]GroupView, 0, len(rows))
	for _, r := range rows {
		gv := GroupView{GroupID: r.GroupID, Platform: r.Platform}
		if cfg, ok := cfgByGroup[r.GroupID]; ok {
			gv.HasConfig = true
			gv.Platform = cfg.Platform
			gv.BotEnabled = cfg.BotEnabled
			gv.TopicEnabled = cfg.TopicEnabled
			gv.CreditEnabled = cfg.CreditEnabled
			gv.Whitelist = parseTagsJSON(cfg.Whitelist)
			gv.Blacklist = parseTagsJSON(cfg.Blacklist)
			gv.WelcomeMsg = cfg.WelcomeMsg
			gv.Remark = cfg.Remark
		}
		items = append(items, gv)
	}
	return c.JSON(fiber.Map{"items": items, "total": total})
}

// GetGroupConfig 查询单个群配置（无配置时返回空对象）。
// 平台参数可能缺失（前端列表行平台未知时以 "all" 占位）：忽略平台按 group_id 命中。
func (h *Handler) GetGroupConfig(c fiber.Ctx) error {
	platform := c.Params("platform")
	if platform == "all" {
		platform = ""
	}
	cfg, err := h.store.GetGroupConfig(c.Context(), platform, c.Params("group_id"))
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "群配置加载失败"})
	}
	if cfg == nil {
		return c.JSON(fiber.Map{"group_id": c.Params("group_id"), "platform": platform})
	}
	gv := GroupView{
		GroupID:       cfg.GroupID,
		Platform:      cfg.Platform,
		HasConfig:     true,
		BotEnabled:    cfg.BotEnabled,
		TopicEnabled:  cfg.TopicEnabled,
		CreditEnabled: cfg.CreditEnabled,
		Whitelist:     parseTagsJSON(cfg.Whitelist),
		Blacklist:     parseTagsJSON(cfg.Blacklist),
		WelcomeMsg:    cfg.WelcomeMsg,
		Remark:        cfg.Remark,
	}
	return c.JSON(gv)
}

// groupConfigReq 群配置保存请求体（指针字段缺省=不修改/保持默认）。
type groupConfigReq struct {
	BotEnabled    *bool    `json:"bot_enabled"`
	TopicEnabled  *bool    `json:"topic_enabled"`
	CreditEnabled *bool    `json:"credit_enabled"`
	Whitelist     []string `json:"whitelist"`
	Blacklist     []string `json:"blacklist"`
	WelcomeMsg    string   `json:"welcome_msg"`
	Remark        string   `json:"remark"`
}

// SaveGroupConfig 保存群配置（有则更新，无则创建）。
func (h *Handler) SaveGroupConfig(c fiber.Ctx) error {
	admin := currentAdmin(c)
	var req groupConfigReq
	if err := h.bind(c, &req); err != nil {
		return err
	}
	if req.Whitelist == nil {
		req.Whitelist = []string{}
	}
	if req.Blacklist == nil {
		req.Blacklist = []string{}
	}
	platform, groupID := c.Params("platform"), c.Params("group_id")
	if platform == "all" {
		platform = ""
	}
	cfg, err := h.store.GetGroupConfig(c.Context(), platform, groupID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "群配置加载失败"})
	}
	if cfg == nil {
		cfg = &model.GroupConfig{Platform: platform, GroupID: groupID}
	} else if platform != "" {
		// 命中已有配置时回填其真实平台，避免列表行平台未知时覆盖成空值
		cfg.Platform = platform
	}
	if req.BotEnabled != nil {
		cfg.BotEnabled = req.BotEnabled
	}
	if req.TopicEnabled != nil {
		cfg.TopicEnabled = req.TopicEnabled
	}
	if req.CreditEnabled != nil {
		cfg.CreditEnabled = req.CreditEnabled
	}
	cfg.Whitelist = jsonString(req.Whitelist)
	cfg.Blacklist = jsonString(req.Blacklist)
	cfg.WelcomeMsg = req.WelcomeMsg
	cfg.Remark = req.Remark

	if err := h.store.SaveGroupConfig(c.Context(), cfg); err != nil {
		h.auditDeny(c, admin, "group.config.save", "group", groupID, err.Error())
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "群配置保存失败"})
	}
	h.auditOK(c, admin, "group.config.save", "group", groupID, jsonDetail(req))
	return c.JSON(fiber.Map{"ok": true})
}

// ─────────────────────────────────────────────
// 用户
// ─────────────────────────────────────────────

// ListUsers 用户列表（分页 + 关键字）。
func (h *Handler) ListUsers(c fiber.Ctx) error {
	offset, limit := pageQuery(c)
	list, total, err := h.store.ListUsers(c.Context(), c.Query("keyword"), offset, limit)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "用户列表加载失败"})
	}
	items := make([]UserView, 0, len(list))
	for _, u := range list {
		items = append(items, UserView{
			ID:             u.ID,
			Platform:       u.Platform,
			PlatformUserID: u.PlatformUserID,
			Nickname:       u.Nickname,
			BannedAt:       u.BannedAt,
			BanReason:      u.BanReason,
			CreatedAt:      u.CreatedAt,
		})
	}
	return c.JSON(fiber.Map{"items": items, "total": total})
}

// setUserBanReq 用户封禁/解封请求。
type setUserBanReq struct {
	Banned bool   `json:"banned"`
	Reason string `json:"reason"`
}

// SetUserBan 封禁/解封用户（super + step-up，写操作留审计）。
func (h *Handler) SetUserBan(c fiber.Ctx) error {
	admin := currentAdmin(c)
	if admin == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "未登录"})
	}
	id, err := strconv.ParseInt(c.Params("id"), 10, 64)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "无效的用户 ID"})
	}
	var req setUserBanReq
	if err := h.bind(c, &req); err != nil {
		return err
	}
	action := "ban"
	if !req.Banned {
		action = "unban"
	}
	if err := h.store.UpdateUserBan(c.Context(), id, req.Banned, req.Reason); err != nil {
		h.auditDeny(c, admin, "user."+action, "user", strconv.FormatInt(id, 10), req.Reason)
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "操作失败：用户不存在或数据库错误"})
	}
	h.auditOK(c, admin, "user."+action, "user", strconv.FormatInt(id, 10), req.Reason)
	return c.JSON(fiber.Map{"ok": true})
}

// ─────────────────────────────────────────────
// 知识库
// ─────────────────────────────────────────────

// ListKnowledgeBases 知识库列表（配置元信息 + 分块数）。
func (h *Handler) ListKnowledgeBases(c fiber.Ctx) error {
	if h.knowledge == nil {
		return c.JSON(fiber.Map{"items": []KnowledgeBaseView{}, "total": 0})
	}
	bases := h.knowledge.List()
	items := make([]KnowledgeBaseView, 0, len(bases))
	ctx := c.Context()
	for _, b := range bases {
		chunks, err := h.store.CountKnowledgeChunks(ctx, b.ID)
		if err != nil {
			chunks = 0
		}
		items = append(items, KnowledgeBaseView{
			ID:          b.ID,
			Name:        b.Name,
			Description: b.Description,
			Provider:    b.Provider,
			Enabled:     b.Enabled,
			Chunks:      chunks,
		})
	}
	return c.JSON(fiber.Map{"items": items, "total": len(items)})
}

// ListKnowledgeChunks 知识库分块列表。
func (h *Handler) ListKnowledgeChunks(c fiber.Ctx) error {
	offset, limit := pageQuery(c)
	list, total, err := h.store.ListKnowledgeChunks(c.Context(), c.Query("base"), c.Query("keyword"), offset, limit)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "分块列表加载失败"})
	}
	return c.JSON(fiber.Map{"items": list, "total": total})
}

// DeleteKnowledgeChunk 删除单个知识库分块。
func (h *Handler) DeleteKnowledgeChunk(c fiber.Ctx) error {
	admin := currentAdmin(c)
	id, err := strconv.ParseInt(c.Params("id"), 10, 64)
	if err != nil || id <= 0 {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "分块 ID 非法"})
	}
	if err := h.store.DeleteKnowledgeChunk(c.Context(), id); err != nil {
		h.auditDeny(c, admin, "knowledge.chunk.delete", "chunk", c.Params("id"), err.Error())
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "分块删除失败"})
	}
	h.auditOK(c, admin, "knowledge.chunk.delete", "chunk", c.Params("id"), "")
	return c.JSON(fiber.Map{"ok": true})
}

// SyncKnowledge 触发知识库内容重同步（?base= 指定单个，缺省全部）。
func (h *Handler) SyncKnowledge(c fiber.Ctx) error {
	admin := currentAdmin(c)
	if h.knowledge == nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "知识库未启用"})
	}
	if err := h.knowledge.Sync(c.Context(), c.Query("base")); err != nil {
		h.auditDeny(c, admin, "knowledge.sync", "knowledge", c.Query("base"), err.Error())
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "知识库同步失败：" + err.Error()})
	}
	h.auditOK(c, admin, "knowledge.sync", "knowledge", c.Query("base"), "")
	return c.JSON(fiber.Map{"ok": true})
}

// ─────────────────────────────────────────────
// 记忆
// ─────────────────────────────────────────────

// ListMemories 记忆列表（按用户/群/关键字过滤）。
func (h *Handler) ListMemories(c fiber.Ctx) error {
	offset, limit := pageQuery(c)
	list, total, err := h.store.ListMemories(c.Context(), c.Query("user_id"), c.Query("group_id"), c.Query("keyword"), offset, limit)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "记忆列表加载失败"})
	}
	items := make([]MemoryView, 0, len(list))
	for _, m := range list {
		items = append(items, MemoryView{
			ID:        m.ID,
			UserID:    m.UserID,
			GroupID:   m.GroupID,
			Content:   m.Content,
			CreatedAt: m.CreatedAt,
		})
	}
	return c.JSON(fiber.Map{"items": items, "total": total})
}

// DeleteMemory 删除单条记忆。
func (h *Handler) DeleteMemory(c fiber.Ctx) error {
	admin := currentAdmin(c)
	id, err := strconv.ParseInt(c.Params("id"), 10, 64)
	if err != nil || id <= 0 {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "记忆 ID 非法"})
	}
	if err := h.store.DeleteMemory(c.Context(), id); err != nil {
		h.auditDeny(c, admin, "memory.delete", "memory", c.Params("id"), err.Error())
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "记忆删除失败"})
	}
	h.auditOK(c, admin, "memory.delete", "memory", c.Params("id"), "")
	return c.JSON(fiber.Map{"ok": true})
}

// ─────────────────────────────────────────────
// 插件
// ─────────────────────────────────────────────

// ListPlugins 插件列表：运行时注册表 + Wasm 安装记录合并。
func (h *Handler) ListPlugins(c fiber.Ctx) error {
	ctx := c.Context()
	byID := map[string]*PluginView{}
	order := []string{}

	reg := h.pluginRegistry()
	if reg != nil {
		for _, info := range reg.List() {
			cmds := make([]string, 0, len(info.Commands))
			for _, cmd := range info.Commands {
				cmds = append(cmds, cmd.Name)
			}
			tools := make([]string, 0, len(info.Tools))
			for _, t := range info.Tools {
				tools = append(tools, t.Name)
			}
			byID[info.ID] = &PluginView{
				PluginID:    info.ID,
				Name:        info.Name,
				Description: info.Description,
				Version:     info.Version,
				Kind:        "builtin",
				State:       reg.StateName(info.ID),
				Enabled:     reg.StateName(info.ID) == "started",
				Commands:    cmds,
				SubtreeID:   info.SubtreeID,
				Tools:       tools,
			}
			order = append(order, info.ID)
		}
	}

	// Wasm 安装记录（含未加载/加载失败的）
	if h.wasm != nil {
		insts, err := database.NewPluginInstallationStore(h.store.Orm()).ListAll(ctx)
		if err != nil {
			h.logger.Warn("manager: 插件安装记录查询失败", zapErr(err))
		}
		for _, inst := range insts {
			pv, ok := byID[inst.PluginID]
			if ok {
				// 补充 wasm 专属字段（运行时状态优先保留）
				pv.Kind = "wasm"
				pv.InstallationID = inst.ID
				pv.LoadError = inst.LoadError
				pv.Enabled = inst.Enabled || pv.Enabled
				pv.CreatedAt = inst.CreatedAt
				pv.UpdatedAt = inst.UpdatedAt
			} else {
				byID[inst.PluginID] = &PluginView{
					PluginID:       inst.PluginID,
					Name:           inst.Name,
					Description:    inst.Description,
					Version:        inst.Version,
					Kind:           "wasm",
					State:          "not_loaded",
					Enabled:        inst.Enabled,
					InstallationID: inst.ID,
					LoadError:      inst.LoadError,
					CreatedAt:      inst.CreatedAt,
					UpdatedAt:      inst.UpdatedAt,
				}
				order = append(order, inst.PluginID)
			}
		}
	}

	items := make([]PluginView, 0, len(order))
	for _, id := range order {
		if pv, ok := byID[id]; ok {
			items = append(items, *pv)
		}
	}
	return c.JSON(fiber.Map{"items": items, "total": len(items)})
}

// wasmInstallation 按 plugin_id 查询 Wasm 安装记录。
func (h *Handler) wasmInstallation(ctx context.Context, pluginID string) (string, bool) {
	if h.wasm == nil {
		return "", false
	}
	inst, err := database.NewPluginInstallationStore(h.store.Orm()).FindByPluginID(ctx, pluginID)
	if err != nil || inst == nil {
		return "", false
	}
	return inst.ID, true
}

// EnablePlugin 启用插件：Wasm 走持久化启停；内置插件为运行时启停（重启按配置恢复）。
func (h *Handler) EnablePlugin(c fiber.Ctx) error { return h.setPluginEnabled(c, true) }

// DisablePlugin 停用插件。
func (h *Handler) DisablePlugin(c fiber.Ctx) error { return h.setPluginEnabled(c, false) }

func (h *Handler) setPluginEnabled(c fiber.Ctx, enabled bool) error {
	admin := currentAdmin(c)
	id := c.Params("id")
	action := "plugin.disable"
	if enabled {
		action = "plugin.enable"
	}
	ctx := c.Context()

	// Wasm 插件优先（有安装记录）
	if instID, ok := h.wasmInstallation(ctx, id); ok {
		var err error
		if enabled {
			err = h.wasm.Start(ctx, plugin.SystemPrincipal("manager"), instID)
		} else {
			err = h.wasm.Unload(ctx, plugin.SystemPrincipal("manager"), instID)
		}
		if err != nil {
			h.auditDeny(c, admin, action, "plugin", id, err.Error())
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
		}
		h.auditOK(c, admin, action, "plugin", id, "")
		return c.JSON(fiber.Map{"ok": true})
	}

	// 内置插件运行时启停
	reg := h.pluginRegistry()
	if reg != nil {
		if _, ok := reg.Get(id); ok {
			var err error
			if enabled {
				err = reg.StartPlugin(ctx, id)
			} else {
				err = reg.StopPlugin(ctx, id)
			}
			if err != nil {
				h.auditDeny(c, admin, action, "plugin", id, err.Error())
				return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
			}
			h.auditOK(c, admin, action, "plugin", id, "内置插件运行时启停（重启后按配置恢复）")
			return c.JSON(fiber.Map{"ok": true})
		}
	}
	return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "插件不存在"})
}

// DeletePlugin 删除 Wasm 插件（含安装记录与文件）。
func (h *Handler) DeletePlugin(c fiber.Ctx) error {
	admin := currentAdmin(c)
	id := c.Params("id")
	ctx := c.Context()
	instID, ok := h.wasmInstallation(ctx, id)
	if !ok {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "插件不存在或非 Wasm 插件"})
	}
	if err := h.wasm.Delete(ctx, plugin.SystemPrincipal("manager"), instID); err != nil {
		h.auditDeny(c, admin, "plugin.delete", "plugin", id, err.Error())
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "插件删除失败：" + err.Error()})
	}
	h.auditOK(c, admin, "plugin.delete", "plugin", id, "")
	return c.JSON(fiber.Map{"ok": true})
}

// ─────────────────────────────────────────────
// Skills
// ─────────────────────────────────────────────

// ListSkills 技能列表。
func (h *Handler) ListSkills(c fiber.Ctx) error {
	if h.skills == nil {
		return c.JSON(fiber.Map{"items": []SkillView{}, "total": 0})
	}
	list := h.skills.List()
	items := make([]SkillView, 0, len(list))
	for _, sk := range list {
		items = append(items, SkillView{
			ID:          sk.ID,
			Name:        sk.Name,
			Description: sk.Description,
			Version:     sk.Version,
			Author:      sk.Author,
			Tags:        sk.Tags,
			Dir:         sk.Dir,
			Enabled:     h.skills.IsEnabled(sk.ID),
			ContentLen:  len(sk.Content),
		})
	}
	return c.JSON(fiber.Map{"items": items, "total": len(items)})
}

// EnableSkill 启用技能（运行时切换并同步写回 skills.toml）。
func (h *Handler) EnableSkill(c fiber.Ctx) error { return h.setSkillEnabled(c, true) }

// DisableSkill 停用技能。
func (h *Handler) DisableSkill(c fiber.Ctx) error { return h.setSkillEnabled(c, false) }

func (h *Handler) setSkillEnabled(c fiber.Ctx, enabled bool) error {
	admin := currentAdmin(c)
	id := c.Params("id")
	action := "skill.disable"
	if enabled {
		action = "skill.enable"
	}
	if h.skills == nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Skill 系统未启用"})
	}
	if _, ok := h.skills.Get(id); !ok {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "技能不存在"})
	}
	if err := h.skills.SetEnabled(id, enabled); err != nil {
		h.auditDeny(c, admin, action, "skill", id, err.Error())
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "技能启停失败：" + err.Error()})
	}
	h.auditOK(c, admin, action, "skill", id, "")
	return c.JSON(fiber.Map{"ok": true})
}

// ─────────────────────────────────────────────
// Prompt 模板
// ─────────────────────────────────────────────

// ListPromptFragments Prompt 片段列表（不含内容）。
func (h *Handler) ListPromptFragments(c fiber.Ctx) error {
	if h.prompts == nil {
		return c.JSON(fiber.Map{"items": []PromptFragmentView{}, "total": 0})
	}
	frags := h.prompts.ListFragments()
	items := make([]PromptFragmentView, 0, len(frags))
	for _, f := range frags {
		items = append(items, PromptFragmentView{ID: f.ID, File: f.File, Builtin: f.Builtin})
	}
	return c.JSON(fiber.Map{"items": items, "total": len(items)})
}

// GetPromptFragment 单个 Prompt 片段（含内容）。
func (h *Handler) GetPromptFragment(c fiber.Ctx) error {
	if h.prompts == nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Prompt 系统未启用"})
	}
	frag, ok := h.prompts.GetFragment(c.Params("id"))
	if !ok {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "片段不存在"})
	}
	content, _ := h.prompts.FragmentContent(frag.ID)
	return c.JSON(PromptFragmentView{ID: frag.ID, File: frag.File, Builtin: frag.Builtin, Content: content})
}

// UpdatePromptFragment 更新 Prompt 片段内容（写回文件 + 热重载；builtin 只读）。
func (h *Handler) UpdatePromptFragment(c fiber.Ctx) error {
	admin := currentAdmin(c)
	id := c.Params("id")
	var req struct {
		Content string `json:"content"`
	}
	if err := h.bind(c, &req); err != nil {
		return err
	}
	if h.prompts == nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Prompt 系统未启用"})
	}
	if err := h.prompts.UpdateFragment(id, req.Content); err != nil {
		h.auditDeny(c, admin, "prompt.update", "prompt", id, err.Error())
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
	}
	h.auditOK(c, admin, "prompt.update", "prompt", id, jsonDetail(fiber.Map{"len": len(req.Content)}))
	return c.JSON(fiber.Map{"ok": true})
}

// ─────────────────────────────────────────────
// 表情包
// ─────────────────────────────────────────────

// ListStickers 表情包列表（分页 + 关键字）。
func (h *Handler) ListStickers(c fiber.Ctx) error {
	offset, limit := pageQuery(c)
	list, total, err := h.store.ListStickers(c.Context(), c.Query("keyword"), offset, limit)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "表情包列表加载失败"})
	}
	items := make([]StickerView, 0, len(list))
	for _, s := range list {
		items = append(items, StickerView{
			ID:        s.ID,
			ObjectKey: s.ObjectKey,
			FileID:    s.FileID,
			Tags:      parseTagsJSON(s.Tags),
			Source:    s.Source,
			CreatedAt: s.CreatedAt,
		})
	}
	return c.JSON(fiber.Map{"items": items, "total": total})
}

// UpdateSticker 更新表情包语义标签。
func (h *Handler) UpdateSticker(c fiber.Ctx) error {
	admin := currentAdmin(c)
	id, err := strconv.ParseUint(c.Params("id"), 10, 32)
	if err != nil || id == 0 {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "表情 ID 非法"})
	}
	var req struct {
		Tags []string `json:"tags"`
	}
	if err := h.bind(c, &req); err != nil {
		return err
	}
	if req.Tags == nil {
		req.Tags = []string{}
	}
	if err := h.store.UpdateStickerTags(c.Context(), uint(id), jsonString(req.Tags)); err != nil {
		h.auditDeny(c, admin, "sticker.update", "sticker", c.Params("id"), err.Error())
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "表情包更新失败"})
	}
	h.auditOK(c, admin, "sticker.update", "sticker", c.Params("id"), jsonDetail(req))
	return c.JSON(fiber.Map{"ok": true})
}

// DeleteSticker 删除表情包记录（对象存储内容按寻址保留）。
func (h *Handler) DeleteSticker(c fiber.Ctx) error {
	admin := currentAdmin(c)
	id, err := strconv.ParseUint(c.Params("id"), 10, 32)
	if err != nil || id == 0 {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "表情 ID 非法"})
	}
	if err := h.store.DeleteSticker(c.Context(), uint(id)); err != nil {
		h.auditDeny(c, admin, "sticker.delete", "sticker", c.Params("id"), err.Error())
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "表情包删除失败"})
	}
	h.auditOK(c, admin, "sticker.delete", "sticker", c.Params("id"), "")
	return c.JSON(fiber.Map{"ok": true})
}

// ─────────────────────────────────────────────
// 命令（只读）
// ─────────────────────────────────────────────

// ListCommands 命令列表：内置命令 + 插件注册命令。
func (h *Handler) ListCommands(c fiber.Ctx) error {
	items := []CommandView{}
	seen := map[string]struct{}{}
	add := func(name, desc, source string) {
		if _, dup := seen[name]; dup {
			return
		}
		seen[name] = struct{}{}
		items = append(items, CommandView{Name: name, Description: desc, Source: source})
	}
	if h.commands != nil {
		for _, cmd := range h.commands.List() {
			add(cmd.Name, cmd.Description, "builtin")
		}
	}
	reg := h.pluginRegistry()
	if reg != nil {
		for _, info := range reg.List() {
			for _, cmd := range info.Commands {
				add(cmd.Name, cmd.Description, "plugin:"+info.ID)
			}
		}
	}
	return c.JSON(fiber.Map{"items": items, "total": len(items)})
}

// ─────────────────────────────────────────────
// 编译期断言：确保依赖类型未被误删
// ─────────────────────────────────────────────

var (
	_ = prompt.Manager{}
	_ = skill.Manager{}
	_ = kb.Service{}
	_ = command.System{}
	_ = store.GroupRow{}
)
