// Package prompt 提供 System Prompt 的组件化组装能力。
//
// 设计目标：
//   - 将硬编码的 System Prompt 拆分为多个命名的 Fragment（Markdown 文件）
//   - 通过组装模板（assembly_template.md）定义各 Fragment 的拼接顺序
//   - 支持 Prefix Cache 优化：静态内容在前，动态内容在后
//   - 支持变量注入（{{ .Vars.xxx }}），使 Prompt 可配置化
//   - 支持技能注入（{{ .Skills }}），将启用的技能内容批量注入
package prompt

import "text/template"

// AssemblyContext 提供组装 System Prompt 所需的全部运行时数据。
type AssemblyContext struct {
	// Vars 从 prompts.toml [vars] 注入的变量，在 Markdown 中以 {{ .Vars.xxx }} 引用
	Vars map[string]any

	// CurrentTime 当前时间字符串（格式由调用方决定）
	CurrentTime string

	// UserName 当前用户的昵称
	UserName string

	// GroupName 当前群组名称（私聊时为空字符串）
	GroupName string

	// Conversation 对话历史文本（由 ChatService 在运行时注入）
	Conversation string

	// Skills 已启用技能的拼接内容（由 Manager 在组装时自动填充）
	Skills string
}

// funcMap 返回 text/template 可用的自定义函数映射。
//
// 可用函数：
//   - {{ fragment "id" }} → 按 ID 返回指定 Fragment 的内容
//
// 可用数据字段（通过 {{ .FieldName }} 访问）：
//   - .Vars, .CurrentTime, .UserName, .GroupName, .Conversation, .Skills
func (m *Manager) funcMap() template.FuncMap {
	return template.FuncMap{
		"fragment": func(id string) string {
			frag, ok := m.fragments[id]
			if !ok {
				return ""
			}
			return frag.content
		},
	}
}