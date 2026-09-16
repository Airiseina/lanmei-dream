package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"unicode"

	"go.uber.org/zap"
	"gorm.io/gorm"
)

// DBAccess 为插件提供受限的数据库访问能力（IndexedDB 隔离模型）：
// 每个插件只能访问自己的隔离命名空间（表名前缀 plugin_<pluginID>_），
// 每次读写都做 Scope 白名单检查并记录审计。
type DBAccess struct {
	db             *gorm.DB
	scopeChecker   *ScopeChecker
	audit          *AuditLogger
	pluginID       string
	installationID string
	logger         *zap.Logger
}

// NewDBAccess 创建数据库访问设施。
//
// 参数：
//   - db：GORM 连接
//   - scopeChecker：Scope 检查器，按 db:read/db:write 校验目标表
//   - audit：审计日志器，每次检查（放行与拒绝）都记录
//   - pluginID：插件 ID，用于生成隔离表名
//   - installationID：安装实例 ID，用于审计主体标识
//   - logger：日志器
//
// 返回：数据库访问设施实例。
func NewDBAccess(db *gorm.DB, scopeChecker *ScopeChecker, audit *AuditLogger, pluginID, installationID string, logger *zap.Logger) *DBAccess {
	return &DBAccess{
		db:             db,
		scopeChecker:   scopeChecker,
		audit:          audit,
		pluginID:       pluginID,
		installationID: installationID,
		logger:         logger,
	}
}

// Query 在授权表上执行只读查询：隔离命名空间使不同插件的同名逻辑表物理隔离
// （表名前缀 plugin_<pluginID>_）。table 为逻辑表名，queryJSON 形如
// {"where": "key = ?", "args": ["value"], "limit": 10}，查询结果反序列化到 result。
//
// 表名经 validateTableName 白名单校验并加隔离前缀，WHERE 子句由 GORM 参数化绑定自动转义；
// WHERE 模板虽来自插件、可构造任意条件，但表名隔离使影响范围仅限插件自身数据。
//
// 参数：
//   - ctx：查询上下文
//   - table：逻辑表名，须通过 validateTableName 且被 db:read 的 Scope 允许
//   - queryJSON：JSON 查询描述；where 为空串时不施加条件，limit<=0 时不施加行数限制
//   - result：GORM 扫描目标（切片或结构体指针）
//
// 返回：Scope 拒绝、JSON 非法或查询失败时返回错误；成功时 result 被填充。
func (d *DBAccess) Query(ctx context.Context, table string, queryJSON string, result any) error {
	if err := validateTableName(table); err != nil {
		return err
	}
	isolatedTable := IsolatedTableName(d.pluginID, table)
	principal := fmt.Sprintf("plugin:%s:%s", d.pluginID, d.installationID)

	if !d.scopeChecker.CheckDBTable(PermDBRead, d.pluginID, table) {
		d.audit.Log(&AuditEntry{
			Principal:      principal,
			Permission:     string(PermDBRead),
			Scope:          "table=" + isolatedTable,
			Decision:       "deny",
			Reason:         "table not in allowed list",
			PluginID:       d.pluginID,
			InstallationID: d.installationID,
		})
		return fmt.Errorf("db: table %q not allowed", table)
	}

	d.audit.Log(&AuditEntry{
		Principal:      principal,
		Permission:     string(PermDBRead),
		Scope:          "table=" + isolatedTable,
		Decision:       "allow",
		PluginID:       d.pluginID,
		InstallationID: d.installationID,
	})

	// 使用参数化查询防止 SQL 注入
	var q struct {
		Where string        `json:"where"`
		Args  []interface{} `json:"args"`
		Limit int           `json:"limit"`
	}
	if err := json.Unmarshal([]byte(queryJSON), &q); err != nil {
		return fmt.Errorf("db: invalid query JSON: %w", err)
	}

	// 使用隔离表名替代插件提供的逻辑表名，确保只访问自己的命名空间
	query := d.db.WithContext(ctx).Table(isolatedTable)
	if q.Where != "" {
		// GORM 的 Where 方法使用参数化查询，args 通过占位符绑定而非字符串拼接，
		// 这是防止 SQL 注入的关键防线
		query = query.Where(q.Where, q.Args...)
	}
	if q.Limit > 0 {
		query = query.Limit(q.Limit)
	}

	return query.Find(result).Error
}

// Exec 在授权表上执行写入操作（隔离命名空间），支持 insert/update/delete：
// update 不提供 where 时更新全表，delete 必须提供 where 以防误删全表。
// table 为逻辑表名，execJSON 形如
// {"action": "insert|update|delete", "data": {...}, "where": "id = ?", "where_args": [...]}。
//
// SQL 注入防护同 Query：表名经 validateTableName + IsolatedTableName 双重保护，
// WHERE 参数化绑定，Data 走 GORM 结构化映射而非拼接原始 SQL。
//
// 参数：
//   - ctx：执行上下文
//   - table：逻辑表名，须通过 validateTableName 且被 db:write 的 Scope 允许
//   - execJSON：JSON 执行描述，action 仅支持 insert/update/delete
//
// 返回：Scope 拒绝、JSON 非法、action 未知、delete 缺少 where 或执行失败时返回错误。
func (d *DBAccess) Exec(ctx context.Context, table string, execJSON string) error {
	if err := validateTableName(table); err != nil {
		return err
	}
	isolatedTable := IsolatedTableName(d.pluginID, table)
	principal := fmt.Sprintf("plugin:%s:%s", d.pluginID, d.installationID)

	if !d.scopeChecker.CheckDBTable(PermDBWrite, d.pluginID, table) {
		d.audit.Log(&AuditEntry{
			Principal:      principal,
			Permission:     string(PermDBWrite),
			Scope:          "table=" + isolatedTable,
			Decision:       "deny",
			Reason:         "table not in allowed list",
			PluginID:       d.pluginID,
			InstallationID: d.installationID,
		})
		return fmt.Errorf("db: table %q not allowed", table)
	}

	d.audit.Log(&AuditEntry{
		Principal:      principal,
		Permission:     string(PermDBWrite),
		Scope:          "table=" + isolatedTable,
		Decision:       "allow",
		PluginID:       d.pluginID,
		InstallationID: d.installationID,
	})

	var e struct {
		Action    string        `json:"action"`
		Data      interface{}   `json:"data"`
		Where     string        `json:"where"`
		WhereArgs []interface{} `json:"where_args"`
	}
	if err := json.Unmarshal([]byte(execJSON), &e); err != nil {
		return fmt.Errorf("db: invalid exec JSON: %w", err)
	}

	query := d.db.WithContext(ctx).Table(isolatedTable)

	switch e.Action {
	case "insert":
		return query.Create(e.Data).Error
	case "update":
		if e.Where != "" {
			// 参数化 WHERE 子句，防止 SQL 注入
			return query.Where(e.Where, e.WhereArgs...).Updates(e.Data).Error
		}
		return query.Updates(e.Data).Error
	case "delete":
		// delete 操作强制要求 where 条件，防止插件误删全表数据
		if e.Where != "" {
			return query.Where(e.Where, e.WhereArgs...).Delete(nil).Error
		}
		return fmt.Errorf("db: delete requires where clause")
	default:
		return fmt.Errorf("db: unknown action %q", e.Action)
	}
}

// validateTableName 校验表名合法性，是 SQL 注入防护的第一道防线：
// 非空、长度不超过 64、仅含字母/数字/下划线。采用白名单而非过滤危险字符，
// 且拼接隔离前缀后的完整表名同样符合该字符集。
func validateTableName(table string) error {
	if table == "" {
		return fmt.Errorf("db: table name cannot be empty")
	}
	if len(table) > 64 {
		return fmt.Errorf("db: table name too long (max 64)")
	}
	for _, r := range table {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' {
			return fmt.Errorf("db: table name %q contains invalid character (only letters, digits, underscore allowed)", table)
		}
	}
	return nil
}
