package control

import (
	"reflect"
	"sort"
	"time"

	"github.com/zrurf/conduit"
)

// NodeKind 行为树节点类型（与前端 Vue Flow 节点类型一一对应）。
type NodeKind string

const (
	NodeSelector  NodeKind = "selector"  // 选择节点（OR）
	NodeSequence  NodeKind = "sequence"  // 顺序节点（AND）
	NodeCondition NodeKind = "condition" // 条件节点（引用命名条件）
	NodeAction    NodeKind = "action"    // 动作节点（引用管线）
	NodeSubtree   NodeKind = "subtree"   // 子树引用节点
	NodeCustom    NodeKind = "custom"    // 自定义节点（插件实现，只读展示）
)

// Node 行为树节点的可序列化快照（亦是编辑 DSL 的最小结构）。
type Node struct {
	Type       NodeKind `json:"type"`
	Name       string   `json:"name"`                  // DebugName（前端展示）
	Condition  string   `json:"condition,omitempty"`   // 条件名（仅 condition）
	PipelineID string   `json:"pipeline_id,omitempty"` // 管线 ID（仅 action）
	SubtreeID  string   `json:"subtree_id,omitempty"`  // 子树 ID（仅 subtree）
	Children   []*Node  `json:"children,omitempty"`
}

// PipelineView 管线的可序列化快照。
// Readonly 为 true 表示管线含不可序列化的静态 Pass 实例，面板禁止编辑。
type PipelineView struct {
	ID       string   `json:"id"`
	PassIDs  []string `json:"pass_ids"`  // 动态 Pass 引用（可按序编辑）
	Readonly bool     `json:"readonly"`  // 静态管线不可编辑
}

// PassView Pass 注册信息。
type PassView struct {
	ID       string `json:"id"`
	TypeName string `json:"type_name"`
}

// SubtreeView 子树快照。
type SubtreeView struct {
	ID   string `json:"id"`
	Node *Node  `json:"node"`
}

// Snapshot 运行时 Conduit 全量快照（前端可视化 + 编辑底稿）。
type Snapshot struct {
	BehaviorTree *Node          `json:"behavior_tree"`
	Pipelines    []PipelineView `json:"pipelines"`
	Passes       []PassView     `json:"passes"`
	Subtrees     []SubtreeView  `json:"subtrees"`
	Conditions   []string       `json:"conditions"`
	GeneratedAt  time.Time      `json:"generated_at"`
}

// Snapshot 生成当前运行时的全量快照。
func (c *Controller) Snapshot() *Snapshot {
	eng := c.desc.Engine()

	snap := &Snapshot{
		GeneratedAt: TimeNow(),
		Conditions:  c.desc.Conditions(),
	}

	if root := c.desc.BehaviorTree(); root != nil {
		snap.BehaviorTree = snapshotNode(c.desc, root)
	}

	// 管线：PassIDs 为空但已注册 → 静态管线（只读展示）
	for _, id := range eng.PipelineIDs() {
		p, ok := eng.GetPipeline(id)
		if !ok {
			continue
		}
		readonly := len(p.PassIDs) == 0
		snap.Pipelines = append(snap.Pipelines, PipelineView{
			ID:       p.ID,
			PassIDs:  append([]string(nil), p.PassIDs...),
			Readonly: readonly,
		})
	}
	sort.Slice(snap.Pipelines, func(i, j int) bool { return snap.Pipelines[i].ID < snap.Pipelines[j].ID })

	// Pass 注册表
	for _, id := range eng.PassIDs() {
		pass, ok := eng.GetPass(id)
		if !ok {
			continue
		}
		snap.Passes = append(snap.Passes, PassView{ID: id, TypeName: reflect.TypeOf(pass).String()})
	}
	sort.Slice(snap.Passes, func(i, j int) bool { return snap.Passes[i].ID < snap.Passes[j].ID })

	// 子树注册表
	for _, id := range eng.SubtreeIDs() {
		node, ok := eng.GetSubtree(id)
		if !ok {
			continue
		}
		snap.Subtrees = append(snap.Subtrees, SubtreeView{ID: id, Node: snapshotNode(c.desc, node)})
	}
	sort.Slice(snap.Subtrees, func(i, j int) bool { return snap.Subtrees[i].ID < snap.Subtrees[j].ID })

	return snap
}

// snapshotNode 递归遍历行为树节点生成快照。
// 未识别的自定义节点（插件实现）降级为只读展示（DebugName + 可选 Children）。
func snapshotNode(desc Descriptor, n conduit.BTNode) *Node {
	switch v := n.(type) {
	case *conduit.Selector:
		return &Node{Type: NodeSelector, Name: v.DebugName(), Children: snapshotChildren(desc, v.Children())}
	case *conduit.Sequence:
		return &Node{Type: NodeSequence, Name: v.DebugName(), Children: snapshotChildren(desc, v.Children())}
	case *conduit.Condition:
		return &Node{Type: NodeCondition, Name: v.DebugName(), Condition: desc.ConditionName(v)}
	case *conduit.Action:
		return &Node{Type: NodeAction, Name: v.DebugName(), PipelineID: v.PipelineID}
	case *conduit.SubtreeRef:
		return &Node{Type: NodeSubtree, Name: v.DebugName(), SubtreeID: v.SubtreeID()}
	default:
		node := &Node{Type: NodeCustom, Name: debugNameOf(n)}
		if ch, ok := n.(interface{ Children() []conduit.BTNode }); ok {
			node.Children = snapshotChildren(desc, ch.Children())
		}
		return node
	}
}

// snapshotChildren 递归快照子节点列表。
func snapshotChildren(desc Descriptor, children []conduit.BTNode) []*Node {
	if len(children) == 0 {
		return nil
	}
	out := make([]*Node, 0, len(children))
	for _, child := range children {
		out = append(out, snapshotNode(desc, child))
	}
	return out
}

// debugNameOf 提取节点的 DebugName（自定义节点兜底展示）。
func debugNameOf(n conduit.BTNode) string {
	if dn, ok := n.(interface{ DebugName() string }); ok {
		return dn.DebugName()
	}
	return reflect.TypeOf(n).String()
}
