package handlers

import (
	"strconv"

	"github.com/gofiber/fiber/v3"

	"github.com/DaWesen/lanmei-dream/internal/manager/crypto"
	"github.com/DaWesen/lanmei-dream/internal/model"
)

// ─────────────────────────────────────────────
// 管理员管理（super + step-up）
// ─────────────────────────────────────────────

// createAdminReq 创建管理员请求。
type createAdminReq struct {
	Username    string `json:"username"`
	Password    string `json:"password"`
	Role        string `json:"role"` // super_admin / admin
	DisplayName string `json:"display_name"`
}

// ListAdmins 列出管理员（super 可看全部；admin 只看自己）。
func (h *Handler) ListAdmins(c fiber.Ctx) error {
	admin := currentAdmin(c)
	if admin == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "未登录"})
	}
	offset, limit := pageQuery(c)
	if admin.Role != model.AdminRoleSuper {
		list := []model.ManagerAdmin{*admin}
		return c.JSON(fiber.Map{"items": list, "total": 1})
	}
	items, total, err := h.store.ListAdmins(c.Context(), offset, limit)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "查询失败"})
	}
	return c.JSON(fiber.Map{"items": items, "total": total})
}

// CreateAdmin 创建管理员（仅 super）。
func (h *Handler) CreateAdmin(c fiber.Ctx) error {
	admin := currentAdmin(c)
	if admin == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "未登录"})
	}
	var req createAdminReq
	if err := h.bind(c, &req); err != nil {
		return err
	}
	if req.Username == "" || len(req.Password) < 8 {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "用户名不能为空，密码至少 8 位"})
	}
	role := model.AdminRole(req.Role)
	if role != model.AdminRoleSuper && role != model.AdminRoleNormal {
		role = model.AdminRoleNormal
	}

	exists, err := h.store.GetAdminByUsername(c.Context(), req.Username)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "内部错误"})
	}
	if exists != nil {
		h.auditDeny(c, admin, "admin.create", "admin", req.Username, "用户名已存在")
		return c.Status(fiber.StatusConflict).JSON(fiber.Map{"error": "用户名已存在"})
	}

	hash, err := crypto.HashPassword(req.Password)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "密码处理失败"})
	}
	newAdmin := &model.ManagerAdmin{
		Username:     req.Username,
		PasswordHash: hash,
		Role:         role,
		Status:       model.AdminStatusActive,
		AuthSource:   model.AuthSourceDB,
		DisplayName:  req.DisplayName,
	}
	if err := h.store.CreateAdmin(c.Context(), newAdmin); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "创建失败"})
	}
	h.auditOK(c, admin, "admin.create", "admin", req.Username, jsonDetail(fiber.Map{
		"role": role, "display_name": req.DisplayName,
	}))
	return c.Status(fiber.StatusCreated).JSON(fiber.Map{"id": newAdmin.ID})
}

// UpdateAdminReq 更新管理员请求（显示名/角色）。
type UpdateAdminReq struct {
	DisplayName string `json:"display_name"`
	Role        string `json:"role"`
}

// UpdateAdmin 更新管理员信息（仅 super）。
func (h *Handler) UpdateAdmin(c fiber.Ctx) error {
	admin := currentAdmin(c)
	if admin == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "未登录"})
	}
	id, err := strconv.ParseUint(c.Params("id"), 10, 64)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "无效的 ID"})
	}
	target, err := h.store.GetAdminByID(c.Context(), uint(id))
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "查询失败"})
	}
	if target == nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "管理员不存在"})
	}
	// 不允许修改自身角色（防止降权自己导致失控）
	if target.ID == admin.ID {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "不能修改自己的角色"})
	}
	var req UpdateAdminReq
	if err := h.bind(c, &req); err != nil {
		return err
	}
	if req.Role != "" {
		role := model.AdminRole(req.Role)
		if role != model.AdminRoleSuper && role != model.AdminRoleNormal {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "非法角色"})
		}
		target.Role = role
	}
	if req.DisplayName != "" {
		target.DisplayName = req.DisplayName
	}
	if err := h.store.UpdateAdmin(c.Context(), target); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "保存失败"})
	}
	h.auditOK(c, admin, "admin.update", "admin", target.Username, jsonDetail(req))
	return c.JSON(fiber.Map{"ok": true})
}

// SetAdminStatusReq 启停管理员请求。
type SetAdminStatusReq struct {
	Status string `json:"status"` // active / disabled
}

// SetAdminStatus 启用/禁用管理员（仅 super；env 超管与本人不可禁用）。
func (h *Handler) SetAdminStatus(c fiber.Ctx) error {
	admin := currentAdmin(c)
	if admin == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "未登录"})
	}
	id, err := strconv.ParseUint(c.Params("id"), 10, 64)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "无效的 ID"})
	}
	target, err := h.store.GetAdminByID(c.Context(), uint(id))
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "查询失败"})
	}
	if target == nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "管理员不存在"})
	}
	if target.ID == admin.ID {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "不能禁用自己的账号"})
	}
	if target.AuthSource == model.AuthSourceEnv {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "env 超管不可禁用（避免锁死）"})
	}
	var req SetAdminStatusReq
	if err := h.bind(c, &req); err != nil {
		return err
	}
	switch model.AdminStatus(req.Status) {
	case model.AdminStatusActive, model.AdminStatusDisabled:
		target.Status = model.AdminStatus(req.Status)
	default:
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "非法状态"})
	}
	if err := h.store.UpdateAdmin(c.Context(), target); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "保存失败"})
	}
	// 禁用时吊销其全部会话
	if target.Status == model.AdminStatusDisabled {
		_ = h.authSvc.RevokeAllSessions(c.Context(), target.ID)
	}
	h.auditOK(c, admin, "admin.status", "admin", target.Username, jsonDetail(req))
	return c.JSON(fiber.Map{"ok": true})
}

// DeleteAdmin 删除管理员（仅 super；env 超管与本人不可删除）。
func (h *Handler) DeleteAdmin(c fiber.Ctx) error {
	admin := currentAdmin(c)
	if admin == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "未登录"})
	}
	id, err := strconv.ParseUint(c.Params("id"), 10, 64)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "无效的 ID"})
	}
	target, err := h.store.GetAdminByID(c.Context(), uint(id))
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "查询失败"})
	}
	if target == nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "管理员不存在"})
	}
	if target.ID == admin.ID {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "不能删除自己的账号"})
	}
	if target.AuthSource == model.AuthSourceEnv {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "env 超管不可删除（避免锁死）"})
	}
	if err := h.store.DeleteAdmin(c.Context(), target.ID); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "删除失败"})
	}
	_ = h.authSvc.RevokeAllSessions(c.Context(), target.ID)
	h.auditOK(c, admin, "admin.delete", "admin", target.Username, "")
	return c.JSON(fiber.Map{"ok": true})
}

// ResetPasswordReq 重置密码请求。
type ResetPasswordReq struct {
	NewPassword string `json:"new_password"`
}

// ResetPassword 重置指定管理员密码（仅 super；env 超管禁止）。
func (h *Handler) ResetPassword(c fiber.Ctx) error {
	admin := currentAdmin(c)
	if admin == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "未登录"})
	}
	id, err := strconv.ParseUint(c.Params("id"), 10, 64)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "无效的 ID"})
	}
	target, err := h.store.GetAdminByID(c.Context(), uint(id))
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "查询失败"})
	}
	if target == nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "管理员不存在"})
	}
	if target.AuthSource == model.AuthSourceEnv {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "env 超管密码由环境变量管理，不可重置"})
	}
	var req ResetPasswordReq
	if err := h.bind(c, &req); err != nil {
		return err
	}
	if len(req.NewPassword) < 8 {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "新密码至少 8 位"})
	}
	hash, err := crypto.HashPassword(req.NewPassword)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "密码处理失败"})
	}
	target.PasswordHash = hash
	if err := h.store.UpdateAdmin(c.Context(), target); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "保存失败"})
	}
	// 密码重置后吊销其全部会话（强制重新登录）
	_ = h.authSvc.RevokeAllSessions(c.Context(), target.ID)
	h.auditOK(c, admin, "admin.password.reset", "admin", target.Username, "")
	return c.JSON(fiber.Map{"ok": true})
}
