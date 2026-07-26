package command

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Context 是命令处理函数的上下文。
type Context struct {
	UserID  int64
	GroupID string
	IsGroup bool
	Message string
	Reply   func(string)
}

// Command 定义一个斜杠命令。
type Command struct {
	Name        string
	Description string
	Handler     func(ctx *Context) error
}

// System 并发安全地管理所有已注册命令。
type System struct {
	mu       sync.RWMutex
	commands map[string]Command
}

// New 创建命令系统。
func New() *System {
	return &System{commands: make(map[string]Command)}
}

// Register 注册命令。重复名称不会覆盖现有命令。
func (s *System) Register(cmd Command) error {
	if strings.TrimSpace(cmd.Name) == "" {
		return fmt.Errorf("命令名不能为空")
	}
	if cmd.Handler == nil {
		return fmt.Errorf("命令 %q 缺少 Handler", cmd.Name)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.commands[cmd.Name]; exists {
		return fmt.Errorf("命令 %q 已注册", cmd.Name)
	}
	s.commands[cmd.Name] = cmd
	return nil
}

// Unregister 注销命令。命令不存在时保持幂等。
func (s *System) Unregister(name string) {
	s.mu.Lock()
	delete(s.commands, name)
	s.mu.Unlock()
}

// Process 解析输入并分发到对应命令。
func (s *System) Process(input string, ctx *Context) error {
	name := strings.TrimPrefix(input, "/")
	parts := strings.Fields(name)
	if len(parts) == 0 {
		return fmt.Errorf("empty command")
	}

	cmdName := parts[0]
	s.mu.RLock()
	cmd, ok := s.commands[cmdName]
	s.mu.RUnlock()
	if !ok {
		ctx.Reply(fmt.Sprintf("未知命令: /%s\n输入 /帮助 查看可用命令", cmdName))
		return fmt.Errorf("unknown command: %s", cmdName)
	}

	if len(parts) > 1 {
		ctx.Message = "/" + cmdName + " " + strings.Join(parts[1:], " ")
	} else {
		ctx.Message = "/" + cmdName
	}
	return cmd.Handler(ctx)
}

// List 返回按名称排序的命令快照。
func (s *System) List() []Command {
	s.mu.RLock()
	commands := make([]Command, 0, len(s.commands))
	for _, cmd := range s.commands {
		commands = append(commands, cmd)
	}
	s.mu.RUnlock()

	sort.Slice(commands, func(i, j int) bool {
		return commands[i].Name < commands[j].Name
	})
	return commands
}

// HelpHandler 是内置帮助命令。
func (s *System) HelpHandler(ctx *Context) error {
	var builder strings.Builder
	builder.WriteString("📋 可用命令:\n")
	for _, cmd := range s.List() {
		builder.WriteString(fmt.Sprintf("  /%s — %s\n", cmd.Name, cmd.Description))
	}
	ctx.Reply(builder.String())
	return nil
}
