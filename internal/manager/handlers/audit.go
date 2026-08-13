package handlers

import (
	"github.com/gofiber/fiber/v3"

	"github.com/DaWesen/lanmei-dream/internal/manager/store"
)

// ─────────────────────────────────────────────
// 审计日志
// ─────────────────────────────────────────────

// ListAuditLogs 分页查询操作审计日志（零信任留痕，前端可按操作/用户过滤）。
func (h *Handler) ListAuditLogs(c fiber.Ctx) error {
	offset, limit := pageQuery(c)
	filter := store.AuditFilter{
		Username: c.Query("username"),
		Action:   c.Query("action"),
	}
	if s := c.Query("since"); s != "" {
		if t, err := timeParse(s); err == nil {
			filter.Since = t
		}
	}
	if u := c.Query("until"); u != "" {
		if t, err := timeParse(u); err == nil {
			filter.Until = t
		}
	}
	items, total, err := h.store.ListAuditLogs(c.Context(), filter, offset, limit)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "查询失败"})
	}
	return c.JSON(fiber.Map{"items": items, "total": total})
}
