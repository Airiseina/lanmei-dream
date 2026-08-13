package control

import (
	"errors"
	"fmt"

	"github.com/zrurf/conduit"
)

// buildNode 将快照节点（编辑 DSL）校验并转换为行为树节点。
// 校验规则：
//   - selector/sequence 的子节点递归构建，任一失败整体失败；
//   - condition 必须引用已注册的命名条件（杜绝注入任意函数）；
//   - action 引用的管线必须已注册；
//   - subtree 引用的子树必须已注册（防止悬空引用导致静默失败）。
func (c *Controller) buildNode(node *Node) (conduit.BTNode, error) {
	if node == nil {
		return nil, errors.New("control: 节点不能为空")
	}
	switch node.Type {
	case NodeSelector, NodeSequence:
		children := make([]conduit.BTNode, 0, len(node.Children))
		for _, child := range node.Children {
			n, err := c.buildNode(child)
			if err != nil {
				return nil, err
			}
			children = append(children, n)
		}
		if node.Type == NodeSelector {
			return conduit.NewSelector(children...), nil
		}
		return conduit.NewSequence(children...), nil

	case NodeCondition:
		if node.Condition == "" {
			return nil, errors.New("control: condition 节点缺少 condition 名称")
		}
		fn, ok := c.desc.Condition(node.Condition)
		if !ok {
			return nil, fmt.Errorf("control: 条件 %q 未注册", node.Condition)
		}
		return conduit.NewCondition(fn), nil

	case NodeAction:
		if node.PipelineID == "" {
			return nil, errors.New("control: action 节点缺少 pipeline_id")
		}
		if _, ok := c.desc.Engine().GetPipeline(node.PipelineID); !ok {
			return nil, fmt.Errorf("control: 管线 %q 未注册", node.PipelineID)
		}
		return conduit.NewAction(node.PipelineID), nil

	case NodeSubtree:
		if node.SubtreeID == "" {
			return nil, errors.New("control: subtree 节点缺少 subtree_id")
		}
		if _, ok := c.desc.Engine().GetSubtree(node.SubtreeID); !ok {
			return nil, fmt.Errorf("control: 子树 %q 未注册", node.SubtreeID)
		}
		return c.desc.Engine().NewSubtreeRef(node.SubtreeID), nil

	default:
		return nil, fmt.Errorf("control: 不支持的节点类型 %q", node.Type)
	}
}

// buildPipeline 将管线编辑 DSL 校验并转换为动态管线。
// 校验规则：
//   - 仅允许替换已存在的管线（禁止面板创建任意新管线）；
//   - 静态管线（含不可序列化 Pass 实例）只读，禁止编辑；
//   - Pass 列表不能为空，且每个 Pass ID 必须已注册。
func (c *Controller) buildPipeline(view *PipelineView) (*conduit.Pipeline, error) {
	if view == nil || view.ID == "" {
		return nil, errors.New("control: 管线 ID 不能为空")
	}
	old, ok := c.desc.Engine().GetPipeline(view.ID)
	if !ok {
		return nil, fmt.Errorf("control: 管线 %q 不存在", view.ID)
	}
	if len(old.PassIDs) == 0 {
		return nil, fmt.Errorf("control: 管线 %q 为静态管线（含不可编辑的 Pass 实例），仅支持动态管线编辑", view.ID)
	}
	if len(view.PassIDs) == 0 {
		return nil, fmt.Errorf("control: 管线 %q 的 Pass 列表不能为空", view.ID)
	}
	for _, pid := range view.PassIDs {
		if _, ok := c.desc.Engine().GetPass(pid); !ok {
			return nil, fmt.Errorf("control: Pass %q 未注册", pid)
		}
	}
	return conduit.NewPipelineFromIDs(view.ID, view.PassIDs...), nil
}
