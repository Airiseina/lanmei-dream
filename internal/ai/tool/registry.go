package tool

import (
	"context"
	"fmt"
	"sync"

	"github.com/cloudwego/eino/schema"
)

// Registry 工具注册表，管理所有已注册工具
type Registry struct {
	mu    sync.RWMutex
	tools map[string]*Tool // key = ToolInfo.Name
}

// NewRegistry 创建工具注册表
func NewRegistry() *Registry {
	return &Registry{tools: make(map[string]*Tool)}
}

// Register 注册工具，名称不可重复
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

// Call 调用指定工具
func (r *Registry) Call(ctx context.Context, name string, argsJSON string) (string, error) {
	r.mu.RLock()
	t, ok := r.tools[name]
	r.mu.RUnlock()
	if !ok {
		return "", fmt.Errorf("tool: %q not found", name)
	}
	return t.Handler(ctx, argsJSON)
}

// ToolInfos 返回所有工具的 Eino ToolInfo 列表（用于 WithTools）
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
