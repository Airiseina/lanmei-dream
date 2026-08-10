package plugin

import (
	"context"

	"github.com/DaWesen/lanmei-dream/internal/ai/tool"
	"github.com/DaWesen/lanmei-dream/internal/command"
	"github.com/DaWesen/lanmei-dream/internal/database"
	"github.com/cloudwego/eino/schema"
	"github.com/zrurf/conduit"
	"go.uber.org/zap"
)

// ============================================================
// Plugin 接口
// ============================================================

// Plugin 是插件的顶层接口，定义生命周期钩子。
//
// 每个插件需要实现四个方法：
//   - Info:    返回元信息（ID、名称、命令列表等），用于注册和展示
//   - OnInit:  初始化阶段，注册 Pass、Pipeline、Subtree 到 Conduit 引擎
//   - OnStart: 启动阶段，开始对外提供服务（如定时任务、事件监听）
//   - OnStop:  停止阶段，清理资源、取消定时任务
//
// 典型实现：
//
//	type SigninPlugin struct { db *database.DB }
//	func (p *SigninPlugin) Info() PluginInfo { ... }
//	func (p *SigninPlugin) OnInit(ctx *PluginContext) error { ... }
//	func (p *SigninPlugin) OnStart(ctx *PluginContext) error { return nil }
//	func (p *SigninPlugin) OnStop(ctx *PluginContext) error { return nil }
type Plugin interface {
	// Info 返回插件元信息。
	// 此方法在 OnInit 之前调用，返回值用于 Registry 的注册和展示。
	Info() PluginInfo

	// OnInit 初始化插件。
	// 在此方法中，插件应完成以下操作：
	//   1. 通过 engine.RegisterPass 注册 Pass 实现
	//   2. 通过 engine.RegisterPipeline 或 NewPipelineFromIDs 注册管线
	//   3. 通过 engine.RegisterSubtree 注册行为树子树
	//   4. 通过 cmdSys.Register 注册斜杠命令
	//   5. 初始化内部状态（如数据库表检查、缓存预热等）
	OnInit(ctx *PluginContext) error

	// OnStart 启动插件。
	// 在 OnInit 之后调用。适合启动后台任务（如定时检查、事件监听）。
	// 如果插件不需要后台任务，直接返回 nil 即可。
	OnStart(ctx *PluginContext) error

	// OnStop 停止插件。
	// 在插件卸载或引擎关闭时调用。应清理所有资源：
	//   - 停止后台 goroutine
	//   - 关闭网络连接
	//   - 释放文件句柄
	OnStop(ctx *PluginContext) error
}

// ============================================================
// PluginInfo 插件元信息
// ============================================================

// PluginInfo 描述插件的元数据。
// Registry 使用这些信息进行展示、命令注册和依赖检查。
type PluginInfo struct {
	// ID 插件唯一标识，全局不可重复。
	// 命名规范：小写字母+下划线，如 "signin"、"festival"、"minigame_riddle"
	// 用于生成 Pass/Pipeline/Subtree 的 ID 前缀，如 "plugin.signin.pass.execute"
	ID string

	// Name 插件显示名称，如 "签到"
	Name string

	// Description 插件功能描述
	Description string

	// Version 插件版本号，如 "1.0.0"
	Version string

	// Commands 插件提供的斜杠命令列表。
	// Registry 在 OnInit 阶段将这些命令注册到 command.System。
	Commands []CommandDef

	// SubtreeID 插件注册的行为树子树 ID。
	// 如果插件不需要子树（如纯命令插件），留空。
	// Registry 会将此子树以 SubtreeRef 的形式挂载到主行为树。
	SubtreeID string

	// Tools 插件提供的 AI 工具列表。
	// Registry 在 OnInit 阶段将这些工具注册到 ToolReg。
	Tools []ToolDef
}

// CommandDef 描述一个斜杠命令。
type CommandDef struct {
	// Name 命令名（不含 / 前缀），如 "签到"
	Name string

	// Description 命令描述，用于帮助信息和意图分析
	Description string
}

// ToolDef 描述插件提供的 AI 工具
type ToolDef struct {
	Name        string
	Description string
	Parameters  *schema.ParamsOneOf // 使用 Eino 标准参数定义
	Handler     func(ctx context.Context, argsJSON string) (string, error)
}

// ============================================================
// PluginContext 插件上下文
// ============================================================

// PluginContext 是插件生命周期方法的上下文参数，
// 提供对引擎、存储、数据库、命令系统和注册表的访问。
//
// 此上下文在 Registry.InitPlugins 时创建，
// 所有插件的 OnInit/OnStart/OnStop 共享同一个 PluginContext。
type PluginContext struct {
	// Engine Conduit 消息处理引擎，插件通过它注册 Pass、Pipeline、Subtree
	Engine *conduit.Engine

	// Store 全局状态存储，用于读写跨请求的持久化数据（Redis，易失）
	Store conduit.StateStore

	// KV 受限键值存储（PostgreSQL 后端，持久化不丢）。
	// 插件私有业务数据应优先使用本存储（语义类似前端 IndexedDB），
	// 而非直接操作 DB；按 pluginID 自动隔离命名空间。
	KV *database.PluginKVStore

	// DB 数据库访问层，用于读写业务数据
	DB *database.DB

	// CmdSys 命令系统，插件通过它注册斜杠命令
	CmdSys *command.System

	// Registry 插件注册表，插件通过它跟踪注册的资源（TrackPass/TrackPipeline），
	// 以便卸载时自动清理。
	Registry *Registry

	// ToolReg AI 工具注册表，插件通过它注册 AI 工具
	ToolReg *tool.Registry

	// Logger 日志记录器
	Logger *zap.Logger

	// Ctx 标准上下文，用于控制超时和取消
	Ctx context.Context
}

// ============================================================
// 辅助函数
// ============================================================

// PassID 生成插件 Pass 的注册 ID。
// 格式：plugin.{pluginID}.pass.{passName}
// 例如：plugin.signin.pass.execute
func PassID(pluginID, passName string) string {
	return "plugin." + pluginID + ".pass." + passName
}

// PipelineID 生成插件 Pipeline 的注册 ID。
// 格式：plugin.{pluginID}.pipeline.{pipelineName}
// 例如：plugin.signin.pipeline.main
func PipelineID(pluginID, pipelineName string) string {
	return "plugin." + pluginID + ".pipeline." + pipelineName
}

// SubtreeID 生成插件 Subtree 的注册 ID。
// 格式：plugin.{pluginID}.subtree
// 例如：plugin.signin.subtree
func SubtreeID(pluginID string) string {
	return "plugin." + pluginID + ".subtree"
}

// StoreKey 生成插件状态存储的键。
// 格式：plugin:{pluginID}:{key}
// 使用 conduit.MakeStoreKey 保证命名空间隔离。
func StoreKey(pluginID, key string) string {
	return conduit.MakeStoreKey("plugin", pluginID, key)
}
