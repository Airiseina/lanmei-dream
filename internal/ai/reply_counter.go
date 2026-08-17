package ai

import "sync"

// ReplyCounter 按会话作用域统计 Bot 自然语言回复数。
//
// 用途：表情提示词计数器。count 仅统计"以自然语言触发的对话"——
// 即 Bot 在本群/私聊中实际发出的 LLM 回复条数（命令回复/事件通知不计数），
// 并通过 System Prompt 注入"当前计数{count}"，让 LLM 感知距上次发表情的
// 对话间隔，从而控制发表情频率（约每五到十句一次，不过频也不完全不发）。
//
// scope 约定：
//   - 群聊：groupID
//   - 私聊："dm:" + 平台用户 ID
type ReplyCounter struct {
	mu     sync.Mutex
	counts map[string]int
}

// NewReplyCounter 创建回复计数器。
func NewReplyCounter() *ReplyCounter {
	return &ReplyCounter{counts: make(map[string]int)}
}

// Get 返回指定作用域的当前计数。
func (c *ReplyCounter) Get(scope string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.counts[scope]
}

// Inc 累加指定作用域计数，返回累加后的值。
func (c *ReplyCounter) Inc(scope string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.counts[scope]++
	return c.counts[scope]
}

// Reset 清零指定作用域计数。
func (c *ReplyCounter) Reset(scope string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.counts, scope)
}
