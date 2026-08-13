package handlers

import (
	"strconv"
	"time"

	"github.com/gofiber/fiber/v3"

	"github.com/DaWesen/lanmei-dream/internal/manager/control"
	"github.com/DaWesen/lanmei-dream/internal/manager/store"
	"github.com/DaWesen/lanmei-dream/internal/model"
)

// ─────────────────────────────────────────────
// Conduit 控制平面（快照 / 编辑 / 回滚 / Trace / 流量）
// ─────────────────────────────────────────────

// ConduitSnapshot 返回行为树 + 管线 + Pass + 子树全量快照。
func (h *Handler) ConduitSnapshot(c fiber.Ctx) error {
	return c.JSON(h.control.Snapshot())
}

// applyTreeReq 行为树编辑请求。
type applyTreeReq struct {
	Node    *control.Node `json:"node"`
	Comment string        `json:"comment"`
}

// ApplyBehaviorTree 应用行为树编辑（super + step-up）。
func (h *Handler) ApplyBehaviorTree(c fiber.Ctx) error {
	admin := currentAdmin(c)
	if admin == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "未登录"})
	}
	var req applyTreeReq
	if err := h.bind(c, &req); err != nil {
		return err
	}
	if req.Node == nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "缺少行为树定义"})
	}
	snap, err := h.control.ApplyBehaviorTree(c.Context(), req.Node, req.Comment, &admin.ID, admin.Username)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
	}
	h.auditOK(c, admin, "conduit.bt.update", "conduit", "behavior_tree", req.Comment)
	return c.JSON(fiber.Map{"snapshot": snap})
}

// applySubtreesReq 子树编辑请求。
type applySubtreesReq struct {
	Subtrees []control.SubtreeView `json:"subtrees"`
	Comment  string                `json:"comment"`
}

// ApplySubtrees 应用子树编辑（super + step-up，Sub Blueprint 保存入口）。
func (h *Handler) ApplySubtrees(c fiber.Ctx) error {
	admin := currentAdmin(c)
	if admin == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "未登录"})
	}
	var req applySubtreesReq
	if err := h.bind(c, &req); err != nil {
		return err
	}
	if len(req.Subtrees) == 0 {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "缺少子树定义"})
	}
	snap, err := h.control.ApplySubtrees(c.Context(), req.Subtrees, req.Comment, &admin.ID, admin.Username)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
	}
	h.auditOK(c, admin, "conduit.subtree.update", "conduit", "subtrees", req.Comment)
	return c.JSON(fiber.Map{"snapshot": snap})
}

// applyPipelinesReq 管线编辑请求。
type applyPipelinesReq struct {
	Pipelines []control.PipelineView `json:"pipelines"`
	Comment   string                 `json:"comment"`
}

// ApplyPipelines 应用管线编辑（super + step-up）。
func (h *Handler) ApplyPipelines(c fiber.Ctx) error {
	admin := currentAdmin(c)
	if admin == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "未登录"})
	}
	var req applyPipelinesReq
	if err := h.bind(c, &req); err != nil {
		return err
	}
	snap, err := h.control.ApplyPipelines(c.Context(), req.Pipelines, req.Comment, &admin.ID, admin.Username)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
	}
	h.auditOK(c, admin, "conduit.pipeline.update", "conduit", "pipelines", req.Comment)
	return c.JSON(fiber.Map{"snapshot": snap})
}

// ListConduitRevisions 列出 conduit 配置修订（回滚底稿）。
func (h *Handler) ListConduitRevisions(c fiber.Ctx) error {
	offset, limit := pageQuery(c)
	items, total, err := h.store.ListConfigRevisions(c.Context(), model.ConfigScopeConduit, "", offset, limit)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "查询失败"})
	}
	return c.JSON(fiber.Map{"items": items, "total": total})
}

// RollbackConduit 回滚 conduit 配置到指定修订（super + step-up）。
func (h *Handler) RollbackConduit(c fiber.Ctx) error {
	admin := currentAdmin(c)
	if admin == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "未登录"})
	}
	id, err := strconv.ParseUint(c.Params("id"), 10, 64)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "无效的修订 ID"})
	}
	snap, err := h.control.Rollback(c.Context(), uint(id), "回滚至修订 #"+c.Params("id"), &admin.ID, admin.Username)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
	}
	h.auditOK(c, admin, "conduit.rollback", "conduit", "revision_"+c.Params("id"), "")
	return c.JSON(fiber.Map{"snapshot": snap})
}

// ListTraces 分页查询执行链路 Trace（审计可视化）。
func (h *Handler) ListTraces(c fiber.Ctx) error {
	offset, limit := pageQuery(c)
	filter := store.TraceFilter{
		Pipeline: c.Query("pipeline"),
		Status:   c.Query("status"),
		GroupID:  c.Query("group_id"),
	}
	if s := c.Query("since"); s != "" {
		if t, err := timeParse(s); err == nil {
			filter.Since = t
		}
	}
	items, total, err := h.store.ListTraces(c.Context(), filter, offset, limit)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "查询失败"})
	}
	return c.JSON(fiber.Map{"items": items, "total": total})
}

// QueryTraffic 查询节点流量（pipeline/node 维度 + 时间范围）。
func (h *Handler) QueryTraffic(c fiber.Ctx) error {
	sinceSec, untilSec, err := timeRange(c)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
	}
	until := time.Now()
	since := until.Add(-24 * time.Hour)
	if sinceSec > 0 {
		since = time.Unix(sinceSec, 0)
	}
	if untilSec > 0 {
		until = time.Unix(untilSec, 0)
	}
	items, err := h.store.QueryNodeTraffic(c.Context(), c.Query("pipeline"), c.Query("node"), since, until)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "查询失败"})
	}
	return c.JSON(fiber.Map{"items": items})
}
