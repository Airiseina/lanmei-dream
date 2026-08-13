// Package store 提供管理面板专属表的数据访问层。
// 直接复用主服务共享的 *database.DB（同一连接池）。
package store

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/DaWesen/lanmei-dream/internal/database"
	"github.com/DaWesen/lanmei-dream/internal/model"
)

// Store 管理面板数据访问层。
type Store struct {
	db *database.DB
}

// New 创建 Store。
func New(db *database.DB) *Store {
	return &Store{db: db}
}

// Orm 暴露底层 GORM（供复杂查询使用）。
func (s *Store) Orm() *gorm.DB {
	return s.db.Orm
}

// DB 暴露底层 database.DB（供事务等使用）。
func (s *Store) DB() *database.DB {
	return s.db
}

// ─────────────────────────────────────────────
// 管理员
// ─────────────────────────────────────────────

// CreateAdmin 创建管理员。
func (s *Store) CreateAdmin(ctx context.Context, admin *model.ManagerAdmin) error {
	return s.db.Orm.WithContext(ctx).Create(admin).Error
}

// GetAdminByUsername 按用户名查询管理员。
func (s *Store) GetAdminByUsername(ctx context.Context, username string) (*model.ManagerAdmin, error) {
	var admin model.ManagerAdmin
	err := s.db.Orm.WithContext(ctx).Where("username = ?", username).First(&admin).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &admin, nil
}

// GetAdminByID 按 ID 查询管理员。
func (s *Store) GetAdminByID(ctx context.Context, id uint) (*model.ManagerAdmin, error) {
	var admin model.ManagerAdmin
	err := s.db.Orm.WithContext(ctx).First(&admin, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &admin, nil
}

// ListAdmins 分页列出管理员。
func (s *Store) ListAdmins(ctx context.Context, offset, limit int) ([]model.ManagerAdmin, int64, error) {
	var list []model.ManagerAdmin
	var total int64
	q := s.db.Orm.WithContext(ctx).Model(&model.ManagerAdmin{})
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	if err := q.Order("id ASC").Offset(offset).Limit(limit).Find(&list).Error; err != nil {
		return nil, 0, err
	}
	return list, total, nil
}

// UpdateAdmin 更新管理员字段（Save 全量更新）。
func (s *Store) UpdateAdmin(ctx context.Context, admin *model.ManagerAdmin) error {
	return s.db.Orm.WithContext(ctx).Save(admin).Error
}

// DeleteAdmin 删除管理员及其凭据、会话（事务）。
func (s *Store) DeleteAdmin(ctx context.Context, id uint) error {
	return s.db.Orm.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("admin_id = ?", id).Delete(&model.AuthCredential{}).Error; err != nil {
			return err
		}
		if err := tx.Where("admin_id = ?", id).Delete(&model.AuthSession{}).Error; err != nil {
			return err
		}
		return tx.Delete(&model.ManagerAdmin{}, id).Error
	})
}

// ─────────────────────────────────────────────
// 认证凭据
// ─────────────────────────────────────────────

// CreateCredential 创建凭据。
func (s *Store) CreateCredential(ctx context.Context, cred *model.AuthCredential) error {
	return s.db.Orm.WithContext(ctx).Create(cred).Error
}

// ListCredentials 列出某管理员全部凭据。
func (s *Store) ListCredentials(ctx context.Context, adminID uint) ([]model.AuthCredential, error) {
	var list []model.AuthCredential
	err := s.db.Orm.WithContext(ctx).Where("admin_id = ?", adminID).Order("id ASC").Find(&list).Error
	return list, err
}

// GetCredentialByID 按凭据 ID 查询（跨管理员，供 webauthn 校验）。
func (s *Store) GetCredentialByID(ctx context.Context, credentialID string) (*model.AuthCredential, error) {
	var cred model.AuthCredential
	err := s.db.Orm.WithContext(ctx).Where("credential_id = ?", credentialID).First(&cred).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &cred, nil
}

// GetCredentialByKind 按管理员+类型查询凭据（如 TOTP 唯一）。
func (s *Store) GetCredentialByKind(ctx context.Context, adminID uint, kind model.CredentialKind) (*model.AuthCredential, error) {
	var cred model.AuthCredential
	err := s.db.Orm.WithContext(ctx).Where("admin_id = ? AND kind = ?", adminID, kind).First(&cred).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &cred, nil
}

// DeleteCredential 删除凭据。
func (s *Store) DeleteCredential(ctx context.Context, id uint) error {
	return s.db.Orm.WithContext(ctx).Delete(&model.AuthCredential{}, id).Error
}

// UpdateCredential 更新凭据。
func (s *Store) UpdateCredential(ctx context.Context, cred *model.AuthCredential) error {
	return s.db.Orm.WithContext(ctx).Save(cred).Error
}

// SaveCredentialUpsert 凭据 upsert（ID=0 时创建，否则更新）。
func (s *Store) SaveCredentialUpsert(ctx context.Context, cred *model.AuthCredential) error {
	if cred.ID == 0 {
		return s.CreateCredential(ctx, cred)
	}
	return s.UpdateCredential(ctx, cred)
}

// ─────────────────────────────────────────────
// 会话
// ─────────────────────────────────────────────

// CreateSession 创建会话。
func (s *Store) CreateSession(ctx context.Context, sess *model.AuthSession) error {
	return s.db.Orm.WithContext(ctx).Create(sess).Error
}

// GetSessionByRefreshHash 按 refresh token 指纹查询会话。
func (s *Store) GetSessionByRefreshHash(ctx context.Context, hash string) (*model.AuthSession, error) {
	var sess model.AuthSession
	err := s.db.Orm.WithContext(ctx).Where("refresh_hash = ?", hash).First(&sess).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &sess, nil
}

// ListSessionsByAdmin 列出某管理员未吊销会话。
func (s *Store) ListSessionsByAdmin(ctx context.Context, adminID uint) ([]model.AuthSession, error) {
	var list []model.AuthSession
	err := s.db.Orm.WithContext(ctx).
		Where("admin_id = ? AND revoked_at IS NULL", adminID).
		Order("id DESC").Find(&list).Error
	return list, err
}

// UpdateSession 更新会话（轮换/时间戳）。
func (s *Store) UpdateSession(ctx context.Context, sess *model.AuthSession) error {
	return s.db.Orm.WithContext(ctx).Save(sess).Error
}

// RevokeSession 吊销会话。
func (s *Store) RevokeSession(ctx context.Context, id uint) error {
	now := time.Now()
	return s.db.Orm.WithContext(ctx).Model(&model.AuthSession{}).
		Where("id = ?", id).Update("revoked_at", &now).Error
}

// RevokeSessionsByAdmin 吊销某管理员全部会话。
func (s *Store) RevokeSessionsByAdmin(ctx context.Context, adminID uint) error {
	now := time.Now()
	return s.db.Orm.WithContext(ctx).Model(&model.AuthSession{}).
		Where("admin_id = ? AND revoked_at IS NULL", adminID).Update("revoked_at", &now).Error
}

// RevokeExpiredSessions 吊销过期会话，返回处理条数。
func (s *Store) RevokeExpiredSessions(ctx context.Context) (int64, error) {
	now := time.Now()
	res := s.db.Orm.WithContext(ctx).Model(&model.AuthSession{}).
		Where("expires_at < ? AND revoked_at IS NULL", now).Update("revoked_at", &now)
	return res.RowsAffected, res.Error
}

// OldestActiveSessionID 返回某管理员最旧的活跃会话 ID（超出上限时吊销它）。
func (s *Store) OldestActiveSessionID(ctx context.Context, adminID uint) (*model.AuthSession, error) {
	var sess model.AuthSession
	err := s.db.Orm.WithContext(ctx).
		Where("admin_id = ? AND revoked_at IS NULL", adminID).
		Order("id ASC").First(&sess).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &sess, nil
}

// ─────────────────────────────────────────────
// 登录尝试
// ─────────────────────────────────────────────

// CreateLoginAttempt 记录登录尝试。
func (s *Store) CreateLoginAttempt(ctx context.Context, att *model.LoginAttempt) error {
	return s.db.Orm.WithContext(ctx).Create(att).Error
}

// CountRecentLoginFails 统计某用户名在时间窗口内的连续失败次数。
func (s *Store) CountRecentLoginFails(ctx context.Context, username string, since time.Time) (int64, error) {
	var n int64
	err := s.db.Orm.WithContext(ctx).Model(&model.LoginAttempt{}).
		Where("username = ? AND success = ? AND created_at >= ?", username, false, since).
		Count(&n).Error
	return n, err
}

// ListLoginAttempts 分页查询登录记录。
func (s *Store) ListLoginAttempts(ctx context.Context, username string, offset, limit int) ([]model.LoginAttempt, int64, error) {
	var list []model.LoginAttempt
	var total int64
	q := s.db.Orm.WithContext(ctx).Model(&model.LoginAttempt{})
	if username != "" {
		q = q.Where("username = ?", username)
	}
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	if err := q.Order("id DESC").Offset(offset).Limit(limit).Find(&list).Error; err != nil {
		return nil, 0, err
	}
	return list, total, nil
}

// ─────────────────────────────────────────────
// 审计日志
// ─────────────────────────────────────────────

// CreateAuditLog 写入审计日志。
func (s *Store) CreateAuditLog(ctx context.Context, log *model.AuditLog) error {
	return s.db.Orm.WithContext(ctx).Create(log).Error
}

// ListAuditLogs 分页查询审计日志。
func (s *Store) ListAuditLogs(ctx context.Context, filter AuditFilter, offset, limit int) ([]model.AuditLog, int64, error) {
	var list []model.AuditLog
	var total int64
	q := s.db.Orm.WithContext(ctx).Model(&model.AuditLog{})
	if filter.Username != "" {
		q = q.Where("username = ?", filter.Username)
	}
	if filter.Action != "" {
		q = q.Where("action = ?", filter.Action)
	}
	if !filter.Since.IsZero() {
		q = q.Where("created_at >= ?", filter.Since)
	}
	if !filter.Until.IsZero() {
		q = q.Where("created_at < ?", filter.Until)
	}
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	if err := q.Order("id DESC").Offset(offset).Limit(limit).Find(&list).Error; err != nil {
		return nil, 0, err
	}
	return list, total, nil
}

// AuditFilter 审计日志筛选条件。
type AuditFilter struct {
	Username string
	Action   string
	Since    time.Time
	Until    time.Time
}

// ─────────────────────────────────────────────
// 配置版本
// ─────────────────────────────────────────────

// CreateConfigRevision 保存配置版本快照。
func (s *Store) CreateConfigRevision(ctx context.Context, rev *model.ConfigRevision) error {
	return s.db.Orm.WithContext(ctx).Create(rev).Error
}

// ListConfigRevisions 按作用域列出配置版本。
func (s *Store) ListConfigRevisions(ctx context.Context, scope model.ConfigScope, name string, offset, limit int) ([]model.ConfigRevision, int64, error) {
	var list []model.ConfigRevision
	var total int64
	q := s.db.Orm.WithContext(ctx).Model(&model.ConfigRevision{}).Where("scope = ?", scope)
	if name != "" {
		q = q.Where("name = ?", name)
	}
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	if err := q.Order("id DESC").Offset(offset).Limit(limit).Find(&list).Error; err != nil {
		return nil, 0, err
	}
	return list, total, nil
}

// GetConfigRevision 按 ID 查询配置版本。
func (s *Store) GetConfigRevision(ctx context.Context, id uint) (*model.ConfigRevision, error) {
	var rev model.ConfigRevision
	err := s.db.Orm.WithContext(ctx).First(&rev, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &rev, nil
}

// ─────────────────────────────────────────────
// Conduit Trace
// ─────────────────────────────────────────────

// BatchCreateTraces 批量写入 trace。
func (s *Store) BatchCreateTraces(ctx context.Context, traces []model.ConduitTrace) error {
	if len(traces) == 0 {
		return nil
	}
	return s.db.Orm.WithContext(ctx).Create(&traces).Error
}

// ListTraces 分页查询 trace。
func (s *Store) ListTraces(ctx context.Context, filter TraceFilter, offset, limit int) ([]model.ConduitTrace, int64, error) {
	var list []model.ConduitTrace
	var total int64
	q := s.db.Orm.WithContext(ctx).Model(&model.ConduitTrace{})
	if filter.Pipeline != "" {
		q = q.Where("pipeline = ?", filter.Pipeline)
	}
	if filter.Status != "" {
		q = q.Where("status = ?", filter.Status)
	}
	if filter.GroupID != "" {
		q = q.Where("group_id = ?", filter.GroupID)
	}
	if !filter.Since.IsZero() {
		q = q.Where("created_at >= ?", filter.Since)
	}
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	if err := q.Order("id DESC").Offset(offset).Limit(limit).Find(&list).Error; err != nil {
		return nil, 0, err
	}
	return list, total, nil
}

// TraceFilter trace 筛选条件。
type TraceFilter struct {
	Pipeline string
	Status   string
	GroupID  string
	Since    time.Time
}

// DeleteTracesBefore 删除某时间之前的 trace（保留策略）。
func (s *Store) DeleteTracesBefore(ctx context.Context, before time.Time) (int64, error) {
	res := s.db.Orm.WithContext(ctx).Where("created_at < ?", before).Delete(&model.ConduitTrace{})
	return res.RowsAffected, res.Error
}

// CountTraces 统计某时间之后的 trace 数（status 为空表示全部，供仪表盘）。
func (s *Store) CountTraces(ctx context.Context, since time.Time, status string) (int64, error) {
	q := s.db.Orm.WithContext(ctx).Model(&model.ConduitTrace{}).Where("created_at >= ?", since)
	if status != "" {
		q = q.Where("status = ?", status)
	}
	var n int64
	err := q.Count(&n).Error
	return n, err
}

// ─────────────────────────────────────────────
// 节点流量
// ─────────────────────────────────────────────

// UpsertNodeTraffic 按分钟桶累加节点流量。
func (s *Store) UpsertNodeTraffic(ctx context.Context, bucket time.Time, pipelineID, nodeName string, count, errCount, durMS int64) error {
	// 先更新已存在的桶；影响 0 行则插入（并发场景由 unique 兜底）。
	res := s.db.Orm.WithContext(ctx).Model(&model.NodeTraffic{}).
		Where("bucket = ? AND pipeline_id = ? AND node_name = ?", bucket, pipelineID, nodeName).
		Updates(map[string]any{
			"count":             gorm.Expr("count + ?", count),
			"error_count":       gorm.Expr("error_count + ?", errCount),
			"total_duration_ms": gorm.Expr("total_duration_ms + ?", durMS),
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected > 0 {
		return nil
	}
	return s.db.Orm.WithContext(ctx).Create(&model.NodeTraffic{
		Bucket:          bucket.Truncate(time.Minute),
		PipelineID:      pipelineID,
		NodeName:        nodeName,
		Count:           count,
		ErrorCount:      errCount,
		TotalDurationMS: durMS,
	}).Error
}

// QueryNodeTraffic 查询节点流量（时间范围聚合）。
func (s *Store) QueryNodeTraffic(ctx context.Context, pipelineID, nodeName string, since, until time.Time) ([]model.NodeTraffic, error) {
	q := s.db.Orm.WithContext(ctx).Model(&model.NodeTraffic{}).
		Where("bucket >= ? AND bucket < ?", since, until)
	if pipelineID != "" {
		q = q.Where("pipeline_id = ?", pipelineID)
	}
	if nodeName != "" {
		q = q.Where("node_name = ?", nodeName)
	}
	var list []model.NodeTraffic
	err := q.Order("bucket ASC").Find(&list).Error
	return list, err
}

// DeleteNodeTrafficBefore 清理过期流量数据。
func (s *Store) DeleteNodeTrafficBefore(ctx context.Context, before time.Time) (int64, error) {
	res := s.db.Orm.WithContext(ctx).Where("bucket < ?", before).Delete(&model.NodeTraffic{})
	return res.RowsAffected, res.Error
}

// ─────────────────────────────────────────────
// LLM Provider
// ─────────────────────────────────────────────

// ListLLMProviders 列出全部 Provider。
func (s *Store) ListLLMProviders(ctx context.Context) ([]model.LLMProvider, error) {
	var list []model.LLMProvider
	err := s.db.Orm.WithContext(ctx).Order("id ASC").Find(&list).Error
	return list, err
}

// CreateLLMProvider 创建 Provider。
func (s *Store) CreateLLMProvider(ctx context.Context, p *model.LLMProvider) error {
	return s.db.Orm.WithContext(ctx).Create(p).Error
}

// UpdateLLMProvider 更新 Provider。
func (s *Store) UpdateLLMProvider(ctx context.Context, p *model.LLMProvider) error {
	return s.db.Orm.WithContext(ctx).Save(p).Error
}

// DeleteLLMProvider 删除 Provider（活跃项禁止删除，调用方校验）。
func (s *Store) DeleteLLMProvider(ctx context.Context, id uint) error {
	return s.db.Orm.WithContext(ctx).Delete(&model.LLMProvider{}, id).Error
}

// GetLLMProvider 按 ID 查询 Provider。
func (s *Store) GetLLMProvider(ctx context.Context, id uint) (*model.LLMProvider, error) {
	var p model.LLMProvider
	err := s.db.Orm.WithContext(ctx).First(&p, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// GetActiveLLMProvider 查询当前活跃 Provider。
func (s *Store) GetActiveLLMProvider(ctx context.Context) (*model.LLMProvider, error) {
	var p model.LLMProvider
	err := s.db.Orm.WithContext(ctx).Where("is_active = ?", true).First(&p).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// SetLLMProviderActive 事务性设置活跃 Provider（旧活跃置 false）。
func (s *Store) SetLLMProviderActive(ctx context.Context, id uint) error {
	return s.db.Orm.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.LLMProvider{}).Where("is_active = ?", true).Update("is_active", false).Error; err != nil {
			return err
		}
		return tx.Model(&model.LLMProvider{}).Where("id = ?", id).Update("is_active", true).Error
	})
}

// ─────────────────────────────────────────────
// Token 用量
// ─────────────────────────────────────────────

// BatchCreateTokenUsage 批量写入 token 用量。
func (s *Store) BatchCreateTokenUsage(ctx context.Context, usages []model.TokenUsage) error {
	if len(usages) == 0 {
		return nil
	}
	return s.db.Orm.WithContext(ctx).Create(&usages).Error
}

// SumTokenUsage 汇总 token 用量（时间范围 + 可选维度）。
func (s *Store) SumTokenUsage(ctx context.Context, since, until time.Time, by Dimension) ([]TokenUsageAgg, error) {
	if !by.Valid() {
		by = DimModel
	}
	col := by.String()
	// 初始化空切片：无数据时返回 [] 而非 null（避免前端遍历空值报错）
	rows := make([]TokenUsageAgg, 0)
	q := s.db.Orm.WithContext(ctx).Model(&model.TokenUsage{}).
		Select("COALESCE("+col+", '') AS dimension, SUM(input_tokens) AS input_tokens, SUM(output_tokens) AS output_tokens, SUM(total_tokens) AS total_tokens, SUM(cost_cents) AS cost_cents").
		Where("ts >= ? AND ts < ?", since, until).
		Group(col).
		Order("total_tokens DESC")
	err := q.Scan(&rows).Error
	return rows, err
}

// TokenUsageAgg 聚合结果行。
type TokenUsageAgg struct {
	Dimension    string `gorm:"column:dimension" json:"dimension"`
	InputTokens  int64  `gorm:"column:input_tokens" json:"input_tokens"`
	OutputTokens int64  `gorm:"column:output_tokens" json:"output_tokens"`
	TotalTokens  int64  `gorm:"column:total_tokens" json:"total_tokens"`
	CostCents    int64  `gorm:"column:cost_cents" json:"cost_cents"`
}

// Dimension 聚合维度（对应 TokenUsage 列名，白名单校验防注入）。
type Dimension string

const (
	DimModel    Dimension = "model"
	DimProvider Dimension = "provider"
	DimScene    Dimension = "scene"
	DimUser     Dimension = "user_id"
	DimGroup    Dimension = "group_id"
	DimPlatform Dimension = "platform"
)

// Valid 校验维度合法。
func (d Dimension) Valid() bool {
	switch d {
	case DimModel, DimProvider, DimScene, DimUser, DimGroup, DimPlatform:
		return true
	}
	return false
}

// String 返回维度列名。
func (d Dimension) String() string { return string(d) }

// UsageSeries 按分钟/小时/天分桶的时间序列（供图表）。
func (s *Store) UsageSeries(ctx context.Context, since, until time.Time, step string) ([]UsagePoint, error) {
	// 初始化空切片：无数据时返回 [] 而非 null（避免前端图表/遍历空值报错）
	rows := make([]UsagePoint, 0)
	// step ∈ {minute,hour,day}（白名单校验）
	switch step {
	case "minute":
		step = "date_trunc('minute', ts)"
	case "hour":
		step = "date_trunc('hour', ts)"
	default:
		step = "date_trunc('day', ts)"
	}
	err := s.db.Orm.WithContext(ctx).Model(&model.TokenUsage{}).
		Select(step+" AS ts, SUM(total_tokens) AS total_tokens, SUM(cost_cents) AS cost_cents, COUNT(*) AS calls").
		Where("ts >= ? AND ts < ?", since, until).
		Group(step).
		Order("ts ASC").
		Scan(&rows).Error
	return rows, err
}

// UsagePoint 时间序列点。
type UsagePoint struct {
	Ts          time.Time `gorm:"column:ts" json:"ts"`
	TotalTokens int64     `gorm:"column:total_tokens" json:"total_tokens"`
	CostCents   int64     `gorm:"column:cost_cents" json:"cost_cents"`
	Calls       int64     `gorm:"column:calls" json:"calls"`
}

// DeleteTokenUsageBefore 清理过期用量数据。
func (s *Store) DeleteTokenUsageBefore(ctx context.Context, before time.Time) (int64, error) {
	res := s.db.Orm.WithContext(ctx).Where("ts < ?", before).Delete(&model.TokenUsage{})
	return res.RowsAffected, res.Error
}

// SumTokenCallsSince 统计某时间之后的 LLM 调用数与总费用（仪表盘）。
func (s *Store) SumTokenCallsSince(ctx context.Context, since time.Time) (calls, costCents int64, err error) {
	var row struct {
		Calls     int64
		CostCents int64
	}
	err = s.db.Orm.WithContext(ctx).Model(&model.TokenUsage{}).
		Select("COUNT(*) AS calls, COALESCE(SUM(cost_cents), 0) AS cost_cents").
		Where("ts >= ?", since).
		Scan(&row).Error
	if err != nil {
		return 0, 0, err
	}
	return row.Calls, row.CostCents, nil
}

// ─────────────────────────────────────────────
// 群配置
// ─────────────────────────────────────────────

// GetGroupConfig 查询群配置（无则返回 nil）。
// 平台为空时忽略 platform 条件（兼容前端列表行未携带平台的情况，按 group_id 命中即可）。
func (s *Store) GetGroupConfig(ctx context.Context, platform, groupID string) (*model.GroupConfig, error) {
	var g model.GroupConfig
	q := s.db.Orm.WithContext(ctx)
	if platform != "" {
		q = q.Where("platform = ?", platform)
	}
	err := q.Where("group_id = ?", groupID).First(&g).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &g, nil
}

// ListGroupConfigs 分页查询群配置。
func (s *Store) ListGroupConfigs(ctx context.Context, platform string, offset, limit int) ([]model.GroupConfig, int64, error) {
	var list []model.GroupConfig
	var total int64
	q := s.db.Orm.WithContext(ctx).Model(&model.GroupConfig{})
	if platform != "" {
		q = q.Where("platform = ?", platform)
	}
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	if err := q.Order("id DESC").Offset(offset).Limit(limit).Find(&list).Error; err != nil {
		return nil, 0, err
	}
	return list, total, nil
}

// SaveGroupConfig 保存群配置（有则更新，无则创建）。
func (s *Store) SaveGroupConfig(ctx context.Context, g *model.GroupConfig) error {
	if g.ID == 0 {
		existing, err := s.GetGroupConfig(ctx, g.Platform, g.GroupID)
		if err != nil {
			return err
		}
		if existing != nil {
			g.ID = existing.ID
			g.CreatedAt = existing.CreatedAt
		}
	}
	return s.db.Orm.WithContext(ctx).Save(g).Error
}

// ─────────────────────────────────────────────
// 定时任务
// ─────────────────────────────────────────────

// ListScheduledJobs 列出定时任务。
func (s *Store) ListScheduledJobs(ctx context.Context) ([]model.ScheduledJob, error) {
	var list []model.ScheduledJob
	err := s.db.Orm.WithContext(ctx).Order("id ASC").Find(&list).Error
	return list, err
}

// CreateScheduledJob 创建定时任务。
func (s *Store) CreateScheduledJob(ctx context.Context, j *model.ScheduledJob) error {
	return s.db.Orm.WithContext(ctx).Create(j).Error
}

// UpdateScheduledJob 更新定时任务。
func (s *Store) UpdateScheduledJob(ctx context.Context, j *model.ScheduledJob) error {
	return s.db.Orm.WithContext(ctx).Save(j).Error
}

// DeleteScheduledJob 删除定时任务。
func (s *Store) DeleteScheduledJob(ctx context.Context, id uint) error {
	return s.db.Orm.WithContext(ctx).Delete(&model.ScheduledJob{}, id).Error
}

// GetScheduledJob 按 ID 查询定时任务。
func (s *Store) GetScheduledJob(ctx context.Context, id uint) (*model.ScheduledJob, error) {
	var j model.ScheduledJob
	err := s.db.Orm.WithContext(ctx).First(&j, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &j, nil
}
