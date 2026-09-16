package tool

import (
	"context"
	"fmt"
	"sync"

	"github.com/cloudwego/eino/schema"
)

// Registry 工具注册表，管理所有已注册工具。
//
// 并发安全：内部以读写锁保护注册表本身，可多 goroutine 并发注册/查询/调用；
// 注意 Call 在锁外执行 Handler，工具实现自身的并发安全由其自行负责。
type Registry struct {
	mu    sync.RWMutex
	tools map[string]*Tool // key = ToolInfo.Name
}

// NewRegistry 创建工具注册表。
func NewRegistry() *Registry {
	return &Registry{tools: make(map[string]*Tool)}
}

// Register 注册工具，名称不可重复。
//
// 参数：
//   - t：工具定义，Info、Info.Name 与 Handler 任一缺失时返回错误
//
// 返回：参数非法或名称已注册时返回错误。
func (r *Registry) Register(t *Tool) error {
	if t.Info == nil || t.Info.Name == "" {
		return fmt.Errorf("tool: name is required")
	}
	if t.Handler == nil {
		return fmt.Errorf("tool: handler is required for %q", t.Info.Name)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.tools[t.Info.Name]; exists {
		return fmt.Errorf("tool: %q already registered", t.Info.Name)
	}
	r.tools[t.Info.Name] = t
	return nil
}

// Unregister 注销工具
func (r *Registry) Unregister(name string) {
	r.mu.Lock()
	delete(r.tools, name)
	r.mu.Unlock()
}

// Get 根据名称获取工具
func (r *Registry) Get(name string) (*Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[name]
	return t, ok
}

// Call 调用指定工具。
//
// 参数：
//   - ctx：调用上下文；经 ChatService 工具循环调用时已注入 CallerIdentity（可经 CallerFrom 读取）
//   - name：工具名
//   - argsJSON：JSON 编码的调用参数（由 LLM 生成，未经校验）
//
// 返回：Handler 的输出文本；工具不存在时返回错误，Handler 的错误原样返回。
func (r *Registry) Call(ctx context.Context, name string, argsJSON string) (string, error) {
	r.mu.RLock()
	t, ok := r.tools[name]
	r.mu.RUnlock()
	if !ok {
		return "", fmt.Errorf("tool: %q not found", name)
	}
	return t.Handler(ctx, argsJSON)
}

// ToolInfos 返回所有工具的 Eino ToolInfo 列表（用于 WithTools）。
// 注意：顺序不保证稳定（内部按 map 遍历）。
func (r *Registry) ToolInfos() []*schema.ToolInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()
	infos := make([]*schema.ToolInfo, 0, len(r.tools))
	for _, t := range r.tools {
		infos = append(infos, t.Info)
	}
	return infos
}

// List 返回所有已注册工具名称
func (r *Registry) List() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}
	return names
}
