// Package control 提供 Conduit 运行时的控制平面：
// 行为树/管线快照、DSL 编辑、配置版本管理与回滚。
//
// 设计原则：
//   - 只读操作（快照）与写操作（编辑）分离；写操作必须经过严格校验，
//     任一引用非法即整体拒绝，绝不留半提交状态；
//   - 每次写操作前自动保存 ConfigRevision（scope=conduit），支持一键回滚；
//   - 行为树中的 Condition 以"命名条件"引用（由 Descriptor 提供），
//     面板只能引用已注册的条件，无法注入任意 Go 函数（安全边界）。
package control

import (
	"time"

	"github.com/zrurf/conduit"
	"go.uber.org/zap"

	"github.com/DaWesen/lanmei-dream/internal/manager/store"
)

// Descriptor 描述 Bot 的 Conduit 运行时控制面。
// 由 Bot 实例实现（结构满足接口即可，无需依赖本包），经 New 注入。
type Descriptor interface {
	// Engine 返回底层 Conduit 引擎（管线/Pass/子树枚举与替换）。
	Engine() *conduit.Engine
	// BehaviorTree 返回当前行为树根节点（供快照遍历）。
	BehaviorTree() conduit.BTNode
	// SetBehaviorTree 应用编辑后的行为树（记录根节点引用并替换引擎主树）。
	SetBehaviorTree(root conduit.BTNode)
	// Condition 按名称解析命名条件；未注册返回 false。
	Condition(name string) (conduit.ConditionFunc, bool)
	// Conditions 返回全部已注册条件名。
	Conditions() []string
	// ConditionName 反查条件实例的名称（快照还原语义）；未登记返回空串。
	ConditionName(cond *conduit.Condition) string
}

// Controller Conduit 控制平面控制器。
type Controller struct {
	desc   Descriptor
	store  *store.Store
	logger *zap.Logger
}

// New 创建控制器。
func New(desc Descriptor, s *store.Store, logger *zap.Logger) *Controller {
	return &Controller{desc: desc, store: s, logger: logger}
}

// Descriptor 返回底层描述符（供 handlers 访问引擎等）。
func (c *Controller) Descriptor() Descriptor { return c.desc }

// TimeNow 可注入时钟（测试用）；生产环境使用 time.Now。
var TimeNow = time.Now
