package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"unicode"

	"go.uber.org/zap"
	"gorm.io/gorm"
)

// DBAccess 为插件提供受限的数据库访问能力（IndexedDB 隔离模型）
// 每个插件只能访问自己的隔离命名空间：表名前缀为 plugin_<pluginID>_
type DBAccess struct {
	db             *gorm.DB
	scopeChecker   *ScopeChecker
	audit          *AuditLogger
	pluginID       string
	installationID string
	logger         *zap.Logger
}

// NewDBAccess 创建数据库访问设施
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

// Query 在授权表上执行只读查询（隔离命名空间）。
//
// IndexedDB 隔离模型：
// 每个插件只能访问 plugin_<pluginID>_<tableName> 前缀的表，
// 即使不同插件声明了相同的逻辑表名，物理上也是隔离的。
// 此方法先将逻辑表名转换为隔离表名，再用隔离表名执行查询。
//
// SQL 注入防护策略：
//   - 表名：通过 validateTableName 白名单校验（仅允许字母、数字、下划线），
//     再通过 IsolatedTableName 添加隔离前缀，杜绝通过表名注入的可能性
//   - WHERE 子句：使用 GORM 的参数化查询（Where("key = ?", args...)），
//     WHERE 模板由插件提供，但参数值通过占位符绑定，由 GORM 自动转义
//   - 注意：WHERE 模板字符串本身来自插件输入，插件可以构造任意 WHERE 条件，
//     但由于表名是隔离的，插件只能在自己的数据上执行查询，影响范围可控
//
// 参数：
//   - ctx: 上下文
//   - table: 逻辑表名（插件视角的表名，会自动转换为隔离表名）
//   - queryJSON: 查询参数 JSON，格式：{"where": "key = ?", "args": ["value"], "limit": 10}
//   - result: 查询结果将反序列化到此指针
//
// 返回：权限不足、表名非法、查询执行等错误
func (d *DBAccess) Query(ctx context.Context, table string, queryJSON string, result any) error {
	if err := validateTableName(table); err != nil {
		return err
	}
	isolatedTable := IsolatedTableName(d.pluginID, table)
	principal := fmt.Sprintf("plugin:%s:%s", d.pluginID, d.installationID)

	// 权限检查：验证插件是否有权读取该表
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

	// 审计日志：记录允许访问的决策
	d.audit.Log(&AuditEntry{
		Principal:      principal,
		Permission:     string(PermDBRead),
		Scope:          "table=" + isolatedTable,
		Decision:       "allow",
		PluginID:       d.pluginID,
		InstallationID: d.installationID,
	})

	// 使用参数化查询防止 SQL 注入
	// queryJSON 格式：{"where": "key = ?", "args": ["value"], "limit": 10}
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

// Exec 在授权表上执行写入操作（隔离命名空间）。
//
// 支持三种写入动作：
//   - insert：插入新记录，data 字段为要插入的数据对象
//   - update：更新记录，需提供 where 条件限定更新范围；若不提供 where 则更新全表
//   - delete：删除记录，必须提供 where 条件（防止误删全表）
//
// SQL 注入防护与 Query 方法一致：
//   - 表名通过 validateTableName + IsolatedTableName 双重保护
//   - WHERE 子句使用参数化查询
//   - Data 通过 GORM 的结构化映射，不会拼接为原始 SQL
//
// 参数：
//   - ctx: 上下文
//   - table: 逻辑表名
//   - execJSON: 执行参数 JSON，格式：
//     {"action": "insert|update|delete", "data": {...}, "where": "id = ?", "where_args": [...]}
//
// 返回：权限不足、表名非法、操作执行等错误
func (d *DBAccess) Exec(ctx context.Context, table string, execJSON string) error {
	if err := validateTableName(table); err != nil {
		return err
	}
	isolatedTable := IsolatedTableName(d.pluginID, table)
	principal := fmt.Sprintf("plugin:%s:%s", d.pluginID, d.installationID)

	// 权限检查：验证插件是否有权写入该表
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

	// 审计日志：记录允许访问的决策
	d.audit.Log(&AuditEntry{
		Principal:      principal,
		Permission:     string(PermDBWrite),
		Scope:          "table=" + isolatedTable,
		Decision:       "allow",
		PluginID:       d.pluginID,
		InstallationID: d.installationID,
	})

	// execJSON 格式：{"action": "insert|update|delete", "data": {...}, "where": "id = ?", "where_args": [...]}
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

// validateTableName 校验表名合法性，是 SQL 注入防护的第一道防线。
//
// 检查规则：
//   - 不为空
//   - 长度不超过 64 个字符
//   - 仅允许字母、数字和下划线（白名单策略）
//
// 白名单策略的优势：与其尝试过滤危险字符（黑名单），不如只允许安全字符，
// 彻底杜绝通过表名进行 SQL 注入的可能性。表名最终会与隔离前缀拼接，
// 拼接后的完整表名同样符合此安全字符集。
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
