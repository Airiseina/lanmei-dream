package control

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/DaWesen/lanmei-dream/internal/model"
	"github.com/zrurf/conduit"
)

// 修订名称（ConfigRevision.Name，scope=conduit）。
const (
	RevisionBehaviorTree = "behavior_tree" // 行为树 DSL 快照
	RevisionPipelines    = "pipelines"     // 管线列表 DSL 快照
	RevisionSubtrees     = "subtrees"      // 子树列表 DSL 快照
)

// ApplyBehaviorTree 应用行为树编辑：
// 先完整校验，再保存"变更前"快照为可回滚修订，最后原子替换主树。
// 返回应用后的最新快照（含校验后的实际树结构）。
func (c *Controller) ApplyBehaviorTree(ctx context.Context, node *Node, comment string, authorID *uint, authorName string) (*Snapshot, error) {
	root, err := c.buildNode(node)
	if err != nil {
		return nil, err
	}

	cur := c.Snapshot()
	prev, err := json.Marshal(cur.BehaviorTree)
	if err != nil {
		return nil, fmt.Errorf("control: 序列化变更前行为树失败: %w", err)
	}
	if err := c.saveRevision(ctx, RevisionBehaviorTree, prev, comment, authorID, authorName); err != nil {
		return nil, err
	}

	c.desc.SetBehaviorTree(root)
	return c.Snapshot(), nil
}

// ApplyPipelines 应用管线编辑（批量）：
// 全部管线先通过校验（任一非法即整体拒绝），再统一替换，保证原子性。
func (c *Controller) ApplyPipelines(ctx context.Context, views []PipelineView, comment string, authorID *uint, authorName string) (*Snapshot, error) {
	if len(views) == 0 {
		return nil, fmt.Errorf("control: 管线列表不能为空")
	}
	pipes := make([]*conduit.Pipeline, 0, len(views))
	for i := range views {
		p, err := c.buildPipeline(&views[i])
		if err != nil {
			return nil, fmt.Errorf("control: 管线[%d]: %w", i, err)
		}
		pipes = append(pipes, p)
	}

	cur := c.Snapshot()
	prev, err := json.Marshal(cur.Pipelines)
	if err != nil {
		return nil, fmt.Errorf("control: 序列化变更前管线失败: %w", err)
	}
	if err := c.saveRevision(ctx, RevisionPipelines, prev, comment, authorID, authorName); err != nil {
		return nil, err
	}

	for _, p := range pipes {
		c.desc.Engine().RegisterOrReplacePipeline(p)
	}
	return c.Snapshot(), nil
}

// ApplySubtrees 应用子树编辑（批量）：
// 全部子树先完成校验（任一非法即整体拒绝），再统一替换引擎子树注册表，保证原子性。
func (c *Controller) ApplySubtrees(ctx context.Context, views []SubtreeView, comment string, authorID *uint, authorName string) (*Snapshot, error) {
	if len(views) == 0 {
		return nil, fmt.Errorf("control: 子树列表不能为空")
	}
	type builtSubtree struct {
		id   string
		node conduit.BTNode
	}
	built := make([]builtSubtree, 0, len(views))
	for i := range views {
		if views[i].ID == "" {
			return nil, fmt.Errorf("control: 子树缺少 ID")
		}
		root, err := c.buildNode(views[i].Node)
		if err != nil {
			return nil, fmt.Errorf("control: 子树[%s]: %w", views[i].ID, err)
		}
		built = append(built, builtSubtree{id: views[i].ID, node: root})
	}

	cur := c.Snapshot()
	prev, err := json.Marshal(cur.Subtrees)
	if err != nil {
		return nil, fmt.Errorf("control: 序列化变更前子树失败: %w", err)
	}
	if err := c.saveRevision(ctx, RevisionSubtrees, prev, comment, authorID, authorName); err != nil {
		return nil, err
	}

	for _, b := range built {
		c.desc.Engine().RegisterOrReplaceSubtree(b.id, b.node)
	}
	return c.Snapshot(), nil
}

// Rollback 按修订 ID 回滚行为树或管线到历史版本。
// 回滚本身也会保存一条修订（审计留痕），由调用方在审计日志中记录。
func (c *Controller) Rollback(ctx context.Context, revisionID uint, comment string, authorID *uint, authorName string) (*Snapshot, error) {
	rev, err := c.store.GetConfigRevision(ctx, revisionID)
	if err != nil {
		return nil, fmt.Errorf("control: 读取修订失败: %w", err)
	}
	if rev.Scope != model.ConfigScopeConduit {
		return nil, fmt.Errorf("control: 修订 %d 不属于 conduit 作用域", revisionID)
	}
	switch rev.Name {
	case RevisionBehaviorTree:
		var node Node
		if err := json.Unmarshal(rev.Content, &node); err != nil {
			return nil, fmt.Errorf("control: 修订内容非法: %w", err)
		}
		return c.ApplyBehaviorTree(ctx, &node, comment, authorID, authorName)
	case RevisionPipelines:
		var views []PipelineView
		if err := json.Unmarshal(rev.Content, &views); err != nil {
			return nil, fmt.Errorf("control: 修订内容非法: %w", err)
		}
		return c.ApplyPipelines(ctx, views, comment, authorID, authorName)
	case RevisionSubtrees:
		var views []SubtreeView
		if err := json.Unmarshal(rev.Content, &views); err != nil {
			return nil, fmt.Errorf("control: 修订内容非法: %w", err)
		}
		return c.ApplySubtrees(ctx, views, comment, authorID, authorName)
	default:
		return nil, fmt.Errorf("control: 未知修订类型 %q", rev.Name)
	}
}

// saveRevision 保存一条配置修订记录（scope=conduit）。
func (c *Controller) saveRevision(ctx context.Context, name string, content []byte, comment string, authorID *uint, authorName string) error {
	rev := &model.ConfigRevision{
		Scope:      model.ConfigScopeConduit,
		Name:       name,
		Content:    content,
		Comment:    comment,
		AuthorID:   authorID,
		AuthorName: authorName,
	}
	if err := c.store.CreateConfigRevision(ctx, rev); err != nil {
		return fmt.Errorf("control: 保存配置修订失败: %w", err)
	}
	return nil
}
