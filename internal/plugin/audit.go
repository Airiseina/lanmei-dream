package plugin

import (
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// AuditEntry 记录一次权限检查的完整信息。
// 插件每次访问受保护资源时，无论 allow 还是 deny 都必须记录，
// 用于回溯安全事件、发现异常 deny 模式并满足合规审计要求。
type AuditEntry struct {
	Timestamp      time.Time `json:"timestamp"`        // 零值时自动填充当前时间
	Principal      string    `json:"principal"`        // 操作主体标识（格式 "plugin:<pluginID>:<installationID>"）
	Permission     string    `json:"permission"`       // 请求的权限标识（如 "state:read"、"http:get"）
	Scope          string    `json:"scope,omitempty"`  // 涉及的 Scope 约束（如 "host=api.example.com"）
	Decision       string    `json:"decision"`         // 授权决策："allow" 或 "deny"
	Reason         string    `json:"reason,omitempty"` // 决策原因（如 "host not in allow_hosts"）
	PluginID       string    `json:"plugin_id,omitempty"`
	InstallationID string    `json:"installation_id,omitempty"`
}

// AuditLogger 为插件系统的所有权限检查提供统一的日志记录。
// 使用独立的 zap.Named("audit") 子命名空间，使审计日志可与应用日志分开采集、
// 归档和分析；各 Access 组件在权限检查后均通过它落审计条目。
type AuditLogger struct {
	logger *zap.Logger
}

// NewAuditLogger 创建审计日志器。
// 使用 zap.Logger.Named("audit") 创建子命名空间，使审计日志可独立于应用日志进行配置。
//
// 参数：
//   - logger：基础日志器，不能为 nil
//
// 返回：绑定 audit 命名空间的审计日志器。
func NewAuditLogger(logger *zap.Logger) *AuditLogger {
	return &AuditLogger{logger: logger.Named("audit")}
}

// Log 记录一条审计日志：Timestamp 为零值时填充当前时间，
// 可选字段仅在非空时输出；allow 用 Info、deny 用 Warn 级别以便告警。
//
// 参数：
//   - entry：审计条目，不能为 nil；Principal、Permission、Decision 为必填字段
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

	if entry.Decision == "allow" {
		al.logger.Info("audit", fields...)
	} else {
		al.logger.Warn("audit_deny", fields...)
	}
}
