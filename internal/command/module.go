package command

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Context 是命令处理函数的上下文。
//
// 各字段由 bot 层在调用 Handler 前从消息上下文注入；插件作者只读使用，修改不会回传。
// 其中 CommandName / CommandArgs 由 bot 层的命令分发 Pass 填充，直接调用 System.Process
// 分发时为空；ReplySegments / SuppressRequesterAt 同样仅由 bot 层填充，插件使用前需判空。
type Context struct {
	// Platform 平台标识（qq / wechat / telegram / ding / napcat 等），按平台分支时使用。
	Platform string
	// PlatformUserID 发送者的平台用户 ID（平台内唯一，如 QQ 号），用于身份识别与数据绑定。
	PlatformUserID string
	// GroupID 平台群 ID；私聊时为空字符串，可配合 IsGroup 判断会话类型。
	GroupID string
	// IsGroup 是否为群聊消息（私聊为 false）。
	IsGroup bool
	// CommandName 命中的命令名（不含 / 前缀）；由 bot 层命令分发 Pass 填充，
	// 直接调用 System.Process 时不填充（为空）。
	CommandName string
	// CommandArgs 命令参数列表（按空白切分，不含命令名）；由 bot 层命令分发 Pass 填充，
	// 直接调用 System.Process 时不填充（为 nil）。
	CommandArgs []string
	// Message 命令原文（含 / 前缀与参数）；System.Process 会把空白归一化后的
	// "/命令名 参数" 写回该字段（如 "/echo  a  b" 归一化为 "/echo a b"）。
	Message string
	// Reply 发送一条文本回复（由 bot 层填充）；可多次调用，按调用顺序逐条发出。
	// 群聊中命令回复默认自动 @ 请求者，可用 SuppressRequesterAt 关闭。
	// 注意：一旦通过 ReplySegments 传出非空段列表，本次命令的文本回复不会发送（段优先）。
	Reply func(string)
	// ReplySegments 回传 OneBot 原生消息段（at/text/image 等，由 bot 层填充）。
	// 主要用于插件命令经自然语言意图重入行为树后，将子消息上下文中的 at 等段传回原始消息；
	// 段列表非空时优先于 Reply 文本发送，传空列表等同于未调用。
	ReplySegments func([]map[string]any)
	// SuppressRequesterAt 禁止 Bot 为本次命令的文本回复自动添加 @请求者（由 bot 层填充），
	// 适合播报类等不宜点名请求者的回复；走 ReplySegments 段路径时该回调无实际影响。
	SuppressRequesterAt func()

	// 消息上下文（插件命令重入引擎时用于还原完整上下文）。
	//
	// SelfID 机器人自身平台 ID，用于判断消息是否 at 了机器人。
	SelfID string
	// AtTargets 消息中被 @ 用户的平台 ID 列表（由 bot 层从消息段解析填充，
	// 可能包含机器人自身），无 at 段时为空。
	AtTargets []string
	// Nickname 发送者昵称（群名片或用户名），平台未提供时为空字符串。
	Nickname string
	// MessageID 平台消息 ID，部分平台事件可能为空。
	MessageID string
	// ConnID 来源网关连接 ID（用于回复路由），多连接部署时定位消息来源；插件一般无需读取。
	ConnID string

	// IsSuperUser 当前消息发送者是否为超管（bot 层注入，命令 handler 据此做权限校验）；
	// 由静态配置管理员与动态管理员合并得出，标记缺失时一律按非超管处理。
	IsSuperUser bool

	// CommandReentry 标记本次 handler 调用来自插件命令重入（防止插件子树未匹配时
	// "命令分支 → 重入 → 命令分支"的无限递归）；由 bot 层从消息 Extra 读取，
	// 插件命令的占位 handler 收到 true 时应直接报错退出，不要继续重入。
	CommandReentry bool
}

// Command 定义一个斜杠命令。
type Command struct {
	// Name 命令名（不含 / 前缀，如 "签到"）；不能为空且全局唯一，
	// 重名注册会被 Register 拒绝且不覆盖已有命令，帮助列表与意图路由均按此名查找。
	Name string
	// Description 命令说明，展示在 /帮助 与 /help 列表中；可为空（此时说明列留空）。
	Description string
	// Handler 命令处理函数，必填（为 nil 时 Register 返回错误）；
	// 返回错误会中断消息处理管线并触发兜底回复。
	Handler func(ctx *Context) error

	// Order 帮助列表排序权重，小的在前；0（未设置）排在末尾，
	// 同权重按命令名排序。动态安装的插件命令无需设置，自动排在最后。
	Order int
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
//
// 参数：
//   - cmd：命令定义；Name 需为非空且唯一的命令名，Handler 不能为 nil
//
// 返回：成功返回 nil；命令名为空、Handler 为 nil 或名称已注册时返回错误，注册不生效
//
// 注意：并发安全，多个插件可并发注册；插件命令由 Registry 在 OnInit 阶段注册，卸载时自动注销。
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
//
// 参数：
//   - name：命令名（不含 / 前缀）
//
// 注意：并发安全；插件卸载时由 Registry 调用，注销后同名命令可重新注册。
func (s *System) Unregister(name string) {
	s.mu.Lock()
	delete(s.commands, name)
	s.mu.Unlock()
}

// Process 解析输入并分发到对应命令。
//
// 参数：
//   - input：斜杠命令原文（如 "/echo hello"），前导 "/" 可省略
//   - ctx：命令上下文；调用方需预先设置 Reply（未知命令提示会经它发出）
//
// 返回：输入为空返回 "empty command" 错误；未知命令先经 ctx.Reply 回复提示再返回错误；
// 已知命令返回 Handler 的错误
//
// 注意：Process 只把归一化空白后的 "/命令名 参数" 写回 ctx.Message，不填充
// CommandName / CommandArgs，也不设置 ReplySegments / SuppressRequesterAt（这些由 bot 层
// 命令分发 Pass 填充）；插件的斜杠命令经 bot 层的 Pass 分发，不直接走 Process。
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

// Lookup 查找已注册的命令（只读）
//
// 参数：
//   - name：命令名（不含 / 前缀）
//
// 返回：命令值副本与是否存在；ok 为 false 时 Command 为零值
//
// 注意：并发安全；返回的是值拷贝，修改其结果不影响已注册命令。
func (s *System) Lookup(name string) (Command, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cmd, ok := s.commands[name]
	return cmd, ok
}

// List 返回按名称排序的命令快照。
//
// 返回：全部已注册命令的副本，按 Name 升序；无命令时返回空切片
//
// 注意：并发安全；调用方对返回切片的修改不影响命令系统，可用于构建意图分析的命令列表。
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

// HelpHandler 内置帮助命令：遍历全部注册命令并按 Order 权重排序（0 排末尾）。
//
// 参数：
//   - ctx：命令上下文；帮助文本经 ctx.Reply 一次性发出，ctx.Reply 需已由调用方设置
//
// 返回：恒为 nil
//
// 注意：排序规则为 Order 升序（0 视为最大值排末尾），同权重按命令名升序；
// 典型用法是注册为 /帮助 与 /help 两个命令共用同一 Handler，自身也需注册后才会出现在列表中。
func (s *System) HelpHandler(ctx *Context) error {
	commands := s.List()

	sort.SliceStable(commands, func(i, j int) bool {
		oi, oj := commands[i].Order, commands[j].Order
		if oi == 0 {
			oi = 1 << 30 // 未设置权重 → 排末尾
		}
		if oj == 0 {
			oj = 1 << 30
		}
		if oi != oj {
			return oi < oj
		}
		return commands[i].Name < commands[j].Name
	})

	var builder strings.Builder
	builder.WriteString("可用命令:\n")
	for _, cmd := range commands {
		builder.WriteString(fmt.Sprintf("  /%s — %s\n", cmd.Name, cmd.Description))
	}
	ctx.Reply(builder.String())
	return nil
}
