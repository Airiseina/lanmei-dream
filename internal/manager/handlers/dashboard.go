package handlers

import (
	"time"

	"github.com/gofiber/fiber/v3"
)

// ─────────────────────────────────────────────
// 仪表盘统计
// ─────────────────────────────────────────────

// DashboardStats 返回仪表盘概览数据。
func (h *Handler) DashboardStats(c fiber.Ctx) error {
	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())

	// 今日消息处理与错误
	msgCount, err := h.store.CountTraces(c.Context(), today, "")
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "统计失败"})
	}
	errCount, err := h.store.CountTraces(c.Context(), today, "error")
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "统计失败"})
	}

	// 今日 LLM 调用与费用
	calls, costCents, err := h.store.SumTokenCallsSince(c.Context(), today)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "统计失败"})
	}

	// 运行时状态
	active := h.llmMgr.ActiveProvider()
	activeName := ""
	activeModel := ""
	if active != nil {
		activeName = active.Name
		activeModel = active.Model
	}
	providerCount := len(h.llmMgr.Providers())

	// 管理员数
	_, adminTotal, err := h.store.ListAdmins(c.Context(), 0, 1)
	if err != nil {
		adminTotal = 0
	}

	// 插件数
	pluginCount := 0
	if h.bot != nil && h.bot.Plugins() != nil {
		pluginCount = len(h.bot.Plugins().List())
	}

	return c.JSON(fiber.Map{
		"today": fiber.Map{
			"messages_processed": msgCount,
			"messages_error":     errCount,
			"llm_calls":          calls,
			"cost_cents":         costCents,
		},
		"runtime": fiber.Map{
			"active_provider": activeName,
			"active_model":    activeModel,
			"provider_count":  providerCount,
			"plugin_count":    pluginCount,
			"admin_count":     adminTotal,
			"engine_running":  h.bot != nil && h.bot.Engine() != nil && h.bot.Engine().IsRunning(),
			"queue_len":       engineQueueLen(h),
		},
		"server_time": now,
	})
}

// engineQueueLen 读取引擎队列深度（nil 安全）。
func engineQueueLen(h *Handler) int64 {
	if h.bot == nil || h.bot.Engine() == nil {
		return 0
	}
	return h.bot.Engine().QueueLen()
}
