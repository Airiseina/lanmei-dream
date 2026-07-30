package plugin

import (
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// AuditEntry 审计日志条目，记录一次权限检查的完整信息。
//
// 设计原理：
// 审计日志是安全合规的核心组件。每次插件请求受保护资源时，
// 无论授权结果如何（allow 或 deny），都应记录审计条目。
// 这使得管理员可以：
//   - 回溯安全事件（谁在什么时候访问了什么资源）
//   - 检测异常行为模式（如频繁的 deny 记录可能暗示恶意插件）
//   - 满足合规审计要求
//
// 日志级别策略：
//   - allow 决策 → Info 级别（常规操作）
//   - deny 决策 → Warn 级别（潜在安全风险，值得关注）
type AuditEntry struct {
	Timestamp      time.Time `json:"timestamp"`                 // 事件发生时间（未设置时自动填充当前时间）
	Principal      string    `json:"principal"`                 // 操作主体标识（格式 "plugin:<pluginID>:<installationID>"）
	Permission     string    `json:"permission"`                // 请求的权限标识（如 "state:read"、"http:get"）
	Scope          string    `json:"scope,omitempty"`           // 涉及的 Scope 约束（如 "host=api.example.com"）
	Decision       string    `json:"decision"`                  // 授权决策："allow" 或 "deny"
	Reason         string    `json:"reason,omitempty"`          // 决策原因（如 "host not in allow_hosts"）
	PluginID       string    `json:"plugin_id,omitempty"`       // 插件标识符
	InstallationID string    `json:"installation_id,omitempty"` // 安装实例标识符
}

// AuditLogger 审计日志器，为插件系统的所有权限检查提供统一的日志记录。
//
// 设计要点：
//   - 使用独立的 zap.Named logger（"audit" 子命名空间），确保审计日志与
//     普通应用日志分离，便于独立采集、归档和分析
//   - 所有 Access 组件（DBAccess、HTTPAccess 等）在执行权限检查后
//     都通过 AuditLogger 记录审计条目，形成完整的访问审计链
type AuditLogger struct {
	logger *zap.Logger
}

// NewAuditLogger 创建审计日志器。
// 使用 zap.Logger.Named("audit") 创建子命名空间，使审计日志可独立于应用日志进行配置。
func NewAuditLogger(logger *zap.Logger) *AuditLogger {
	return &AuditLogger{logger: logger.Named("audit")}
}

// Log 记录一条审计日志。
//
// 处理流程：
//  1. 如果 entry.Timestamp 为零值，自动填充当前时间
//  2. 构建结构化日志字段（principal、permission、decision 为必填字段）
//  3. 可选字段（scope、reason、plugin_id、installation_id）仅在非空时记录
//  4. 根据 decision 字段选择日志级别：
//     "allow" → Info（常规操作记录）
//     "deny"  → Warn（潜在安全风险，值得监控）
func (al *AuditLogger) Log(entry *AuditEntry) {
	if entry.Timestamp.IsZero() {
		entry.Timestamp = time.Now()
	}

	// 必填字段：每次审计都必须记录的核心信息
	fields := []zapcore.Field{
		zap.String("principal", entry.Principal),
		zap.String("permission", entry.Permission),
		zap.String("decision", entry.Decision),
	}

	// 可选字段：仅在非空时追加，避免日志中出现空字符串噪声
	if entry.Scope != "" {
		fields = append(fields, zap.String("scope", entry.Scope))
	}
	if entry.Reason != "" {
		fields = append(fields, zap.String("reason", entry.Reason))
	}
	if entry.PluginID != "" {
		fields = append(fields, zap.String("plugin_id", entry.PluginID))
	}
	if entry.InstallationID != "" {
		fields = append(fields, zap.String("installation_id", entry.InstallationID))
	}

	// 根据授权决策选择日志级别
	if entry.Decision == "allow" {
		al.logger.Info("audit", fields...)
	} else {
		al.logger.Warn("audit_deny", fields...)
	}
}
