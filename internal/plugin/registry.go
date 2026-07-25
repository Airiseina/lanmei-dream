package plugin

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"sync"

	"github.com/DaWesen/lanmei-dream/internal/command"
	"github.com/DaWesen/lanmei-dream/internal/database"
	"github.com/zrurf/conduit"
)

// ============================================================
// Registry 插件注册表
// ============================================================

// Registry 管理所有插件的生命周期，是插件系统的核心。
//
// 职责：
//   - 维护已注册插件的列表
//   - 按序调用插件的 OnInit → OnStart 生命周期
//   - 反序调用插件的 OnStop 进行清理
//   - 动态重建主行为树，将插件子树挂载到决策流程中
//   - 跟踪每个插件注册的 Pass/Pipeline/Subtree/Command，卸载时自动清理
//
// 使用流程：
//
//	reg := plugin.NewRegistry(engine, store, db, cmdSys)
//	reg.Register(&SigninPlugin{})
//	reg.Register(&FestivalPlugin{})
//	reg.InitPlugins(ctx)  // 初始化所有插件
//	reg.StartPlugins(ctx) // 启动所有插件
//	// ... 运行 ...
//	reg.StopPlugins(ctx)  // 停止所有插件
type Registry struct {
	mu sync.RWMutex

	// engine Conduit 引擎，用于注册/注销 Pass、Pipeline、Subtree
	engine *conduit.Engine
	// store 全局状态存储
	store conduit.StateStore
	// db 数据库访问层
	db *database.DB
	// cmdSys 命令系统
	cmdSys *command.System
	// logger 日志记录器
	logger *slog.Logger

	// plugins 已注册的插件实例（按注册顺序）
	plugins []Plugin
	// pluginState 每个插件的运行状态
	pluginState map[string]pluginState

	// rebuildBT 行为树重建回调，由 Bot 层设置
	// 当插件注册/卸载后，Registry 调用此函数通知 Bot 重建主行为树
	rebuildBT func()
}

// pluginState 记录插件的运行状态和注册的资源
type pluginState struct {
	state        stateKind // 当前状态
	subtreeID    string    // 注册的子树 ID（空表示无子树）
	passIDs      []string  // 注册的 Pass ID 列表
	pipelineIDs  []string  // 注册的 Pipeline ID 列表
	commandNames []string  // 注册的命令名列表
}

type stateKind int

const (
	stateRegistered  stateKind = iota // 已注册但未初始化
	stateInitialized                  // 已初始化（OnInit 已调用）
	stateStarted                      // 已启动（OnStart 已调用）
	stateStopped                      // 已停止
)

// NewRegistry 创建插件注册表。
// engine 可以为 nil，稍后通过 SetEngine 设置（适用于引擎在 Bot.New 中创建的场景）。
func NewRegistry(engine *conduit.Engine, store conduit.StateStore, db *database.DB, cmdSys *command.System) *Registry {
	return &Registry{
		engine:      engine,
		store:       store,
		db:          db,
		cmdSys:      cmdSys,
		logger:      slog.Default(),
		pluginState: make(map[string]pluginState),
	}
}

// SetEngine 设置 Conduit 引擎。
// 当引擎在 Registry 创建之后才初始化时（如 bot.New 中），通过此方法注入。
func (r *Registry) SetEngine(engine *conduit.Engine) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.engine = engine
}

// SetRebuildBT 设置行为树重建回调。
// Bot 层在创建 Registry 后调用此方法，传入行为树重建函数。
// 当插件注册/卸载导致子树变化时，Registry 通过此回调通知 Bot 重建主行为树。
func (r *Registry) SetRebuildBT(fn func()) {
	r.rebuildBT = fn
}

// ============================================================
// 注册与卸载
// ============================================================

// Register 注册一个插件到注册表。
// 插件 ID 不可重复，否则返回错误。
// 注册后需调用 InitPlugins 进行初始化。
func (r *Registry) Register(p Plugin) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	info := p.Info()
	if info.ID == "" {
		return fmt.Errorf("plugin: ID must not be empty")
	}
	if _, exists := r.pluginState[info.ID]; exists {
		return fmt.Errorf("plugin: %q already registered", info.ID)
	}

	r.plugins = append(r.plugins, p)
	r.pluginState[info.ID] = pluginState{state: stateRegistered}
	r.logger.Info("plugin registered", "id", info.ID, "name", info.Name, "version", info.Version)
	return nil
}

// Unregister 卸载一个插件。
// 如果插件已启动，会先调用 OnStop 停止，然后清理所有注册的资源。
func (r *Registry) Unregister(pluginID string) error {
	// 在锁内快照插件信息
	r.mu.Lock()

	idx := -1
	for i, p := range r.plugins {
		if p.Info().ID == pluginID {
			idx = i
			break
		}
	}
	if idx == -1 {
		r.mu.Unlock()
		return fmt.Errorf("plugin: %q not found", pluginID)
	}

	p := r.plugins[idx]
	st := r.pluginState[pluginID]
	wasStarted := st.state == stateStarted

	// 先标记为已停止，防止并发操作
	st.state = stateStopped
	r.pluginState[pluginID] = st

	r.mu.Unlock()

	// 如果已启动，先停止（在锁外调用，避免 OnStop 内部访问 Registry 导致死锁）
	if wasStarted {
		pCtx := r.newPluginContext()
		if err := p.OnStop(pCtx); err != nil {
			r.logger.Error("plugin OnStop failed", "id", pluginID, "error", err)
		}
	}

	// 清理注册的资源（engine 方法不需要 Registry 锁）
	r.cleanupResources(pluginID, st)

	// 重新获取锁，从列表移除
	r.mu.Lock()
	// 重新查找索引（可能在释放锁期间发生变化）
	idx = -1
	for i, pp := range r.plugins {
		if pp.Info().ID == pluginID {
			idx = i
			break
		}
	}
	if idx != -1 {
		r.plugins = slices.Delete(r.plugins, idx, idx+1)
	}
	delete(r.pluginState, pluginID)
	r.mu.Unlock()

	r.logger.Info("plugin unregistered", "id", pluginID)

	// 通知重建行为树（在锁外调用，避免 rebuildBT → SubtreeRefs → r.mu.RLock 死锁）
	if r.rebuildBT != nil {
		r.rebuildBT()
	}

	return nil
}

// ============================================================
// 生命周期
// ============================================================

// InitPlugins 初始化所有已注册的插件。
// 按注册顺序依次调用每个插件的 OnInit，同时注册命令到 command.System。
// 如果任一插件的 OnInit 失败，返回错误并停止后续初始化。
func (r *Registry) InitPlugins(ctx context.Context) error {
	// 快照待初始化的插件列表，然后释放锁。
	// OnInit 可能调用 TrackPass/TrackPipeline，它们也需要获取 r.mu，
	// 因此不能在持有锁的情况下调用 OnInit（否则死锁）。
	r.mu.Lock()
	var toInit []Plugin
	for _, p := range r.plugins {
		st := r.pluginState[p.Info().ID]
		if st.state == stateRegistered {
			toInit = append(toInit, p)
		}
	}
	r.mu.Unlock()

	pCtx := r.newPluginContextWith(ctx)

	for _, p := range toInit {
		info := p.Info()

		// 调用插件 OnInit（不在锁内，避免 TrackPass/TrackPipeline 死锁）
		if err := p.OnInit(pCtx); err != nil {
			return fmt.Errorf("plugin %q OnInit failed: %w", info.ID, err)
		}

		// 注册命令到 command.System
		var cmdNames []string
		for _, cmd := range info.Commands {
			r.cmdSys.Register(command.Command{
				Name:        cmd.Name,
				Description: cmd.Description,
				Handler:     r.makeCommandHandler(p, cmd),
			})
			cmdNames = append(cmdNames, cmd.Name)
		}

		// 确定子树 ID（仅当插件显式声明时才使用，留空表示不注册子树）
		subID := info.SubtreeID

		// 更新状态（重新获取锁，保留 TrackPass/TrackPipeline 写入的跟踪数据）
		r.mu.Lock()
		st := r.pluginState[info.ID]
		r.pluginState[info.ID] = pluginState{
			state:        stateInitialized,
			subtreeID:    subID,
			passIDs:      st.passIDs,
			pipelineIDs:  st.pipelineIDs,
			commandNames: cmdNames,
		}
		r.mu.Unlock()

		r.logger.Info("plugin initialized", "id", info.ID, "commands", cmdNames)
	}

	// 插件初始化后，行为树需要重建以包含新注册的子树
	// 在锁外调用，避免 rebuildBT → SubtreeRefs → r.mu.RLock 死锁
	if r.rebuildBT != nil {
		r.rebuildBT()
	}

	return nil
}

// StartPlugins 启动所有已初始化的插件。
// 按注册顺序依次调用每个插件的 OnStart。
func (r *Registry) StartPlugins(ctx context.Context) error {
	// 快照待启动的插件列表，然后释放锁。
	// OnStart 可能间接访问 Registry 方法（如查询其他插件状态），需要获取 r.mu，
	// 因此不能在持有锁的情况下调用 OnStart（否则死锁）。
	r.mu.Lock()
	var toStart []Plugin
	for _, p := range r.plugins {
		st := r.pluginState[p.Info().ID]
		if st.state == stateInitialized {
			toStart = append(toStart, p)
		}
	}
	r.mu.Unlock()

	pCtx := r.newPluginContextWith(ctx)

	for _, p := range toStart {
		info := p.Info()

		if err := p.OnStart(pCtx); err != nil {
			return fmt.Errorf("plugin %q OnStart failed: %w", info.ID, err)
		}

		// 防御性检查：确保插件在 OnStart 期间未被 Unregister
		r.mu.Lock()
		st, exists := r.pluginState[info.ID]
		if exists && st.state == stateInitialized {
			st.state = stateStarted
			r.pluginState[info.ID] = st
		}
		r.mu.Unlock()

		r.logger.Info("plugin started", "id", info.ID)
	}

	return nil
}

// StopPlugins 停止所有已启动的插件。
// 按注册的逆序依次调用每个插件的 OnStop，确保依赖关系正确释放。
func (r *Registry) StopPlugins(ctx context.Context) {
	// 快照待停止的插件列表（逆序），然后释放锁。
	// OnStop 可能间接访问 Registry 方法，需要获取 r.mu，
	// 因此不能在持有锁的情况下调用 OnStop（否则死锁）。
	r.mu.Lock()
	var toStop []Plugin
	for i := len(r.plugins) - 1; i >= 0; i-- {
		p := r.plugins[i]
		st := r.pluginState[p.Info().ID]
		if st.state == stateStarted {
			toStop = append(toStop, p)
		}
	}
	r.mu.Unlock()

	pCtx := r.newPluginContextWith(ctx)

	for _, p := range toStop {
		info := p.Info()

		if err := p.OnStop(pCtx); err != nil {
			r.logger.Error("plugin OnStop failed", "id", info.ID, "error", err)
		}

		// 防御性检查：确保插件在 OnStop 期间未被 Unregister
		r.mu.Lock()
		st, exists := r.pluginState[info.ID]
		if exists && st.state == stateStarted {
			st.state = stateStopped
			r.pluginState[info.ID] = st
		}
		r.mu.Unlock()

		r.logger.Info("plugin stopped", "id", info.ID)
	}
}

// ============================================================
// 行为树管理
// ============================================================

// SubtreeRefs 返回所有已初始化插件的 SubtreeRef 节点列表。
// Bot 层在构建主行为树时，将这些 SubtreeRef 插入到核心分支之前，
// 使插件的路由优先级高于核心逻辑。
//
// 调用时机：Bot 构建或重建主行为树时。
func (r *Registry) SubtreeRefs() []*conduit.SubtreeRef {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var refs []*conduit.SubtreeRef
	for _, p := range r.plugins {
		info := p.Info()
		st := r.pluginState[info.ID]
		if st.state < stateInitialized {
			continue
		}
		if st.subtreeID != "" {
			refs = append(refs, r.engine.NewSubtreeRef(st.subtreeID))
		}
	}
	return refs
}

// ============================================================
// 查询
// ============================================================

// List 返回所有已注册插件的信息列表。
func (r *Registry) List() []PluginInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()

	infos := make([]PluginInfo, 0, len(r.plugins))
	for _, p := range r.plugins {
		infos = append(infos, p.Info())
	}
	return infos
}

// Get 根据插件 ID 获取插件实例。
func (r *Registry) Get(pluginID string) (Plugin, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	for _, p := range r.plugins {
		if p.Info().ID == pluginID {
			return p, true
		}
	}
	return nil, false
}

// State 返回插件的当前状态。
func (r *Registry) State(pluginID string) (stateKind, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	st, ok := r.pluginState[pluginID]
	if !ok {
		return stateRegistered, false
	}
	return st.state, true
}

// ============================================================
// 内部方法
// ============================================================

// newPluginContext 创建不带超时的 PluginContext（用于生命周期调用）。
func (r *Registry) newPluginContext() *PluginContext {
	return &PluginContext{
		Engine:   r.engine,
		Store:    r.store,
		DB:       r.db,
		CmdSys:   r.cmdSys,
		Registry: r,
		Ctx:      context.Background(),
	}
}

// newPluginContextWith 使用指定 context 创建 PluginContext。
func (r *Registry) newPluginContextWith(ctx context.Context) *PluginContext {
	return &PluginContext{
		Engine:   r.engine,
		Store:    r.store,
		DB:       r.db,
		CmdSys:   r.cmdSys,
		Registry: r,
		Ctx:      ctx,
	}
}

// makeCommandHandler 将插件的命令处理包装为 command.Handler。
// 当命令被触发时，构造 InputMessage 并通过引擎处理。
func (r *Registry) makeCommandHandler(p Plugin, cmd CommandDef) func(ctx *command.Context) error {
	return func(cmdCtx *command.Context) error {
		// 通过 Conduit 引擎处理插件命令
		// 将命令转换为 InputMessage，引擎会根据行为树路由到对应管线
		pluginID := p.Info().ID
		input := &conduit.InputMessage{
			UserID:  fmt.Sprintf("%d", cmdCtx.UserID),
			GroupID: cmdCtx.GroupID,
			Content: "/" + cmd.Name,
			IsGroup: cmdCtx.IsGroup,
			Extra: map[string]any{
				"plugin_id":    pluginID,
				"command_name": cmd.Name,
				SkipCommandKey: true,
			},
		}
		result, err := r.engine.Process(input)
		if err != nil {
			return fmt.Errorf("plugin %q command %q process failed: %w", pluginID, cmd.Name, err)
		}
		for _, msg := range result.Output {
			cmdCtx.Reply(msg.Content)
		}
		return nil
	}
}

// cleanupResources 清理插件注册的所有资源（Pass、Pipeline、Subtree、Command）。
// 清理顺序：Subtree → Pipeline → Pass，确保引用关系正确释放。
func (r *Registry) cleanupResources(pluginID string, st pluginState) {
	// 注销子树（停止路由到该插件的管线）
	if st.subtreeID != "" {
		if err := r.engine.UnregisterSubtree(st.subtreeID); err != nil {
			r.logger.Warn("failed to unregister subtree", "id", st.subtreeID, "error", err)
		}
	}

	// 注销 Pipeline（Pipeline 引用 Pass，应先于 Pass 注销）
	for _, id := range st.pipelineIDs {
		if err := r.engine.UnregisterPipeline(id); err != nil {
			r.logger.Warn("failed to unregister pipeline", "id", id, "error", err)
		}
	}

	// 注销 Pass
	for _, id := range st.passIDs {
		if err := r.engine.UnregisterPass(id); err != nil {
			r.logger.Warn("failed to unregister pass", "id", id, "error", err)
		}
	}

	// 注销 Command
	for _, name := range st.commandNames {
		r.cmdSys.Unregister(name)
	}
}

// TrackPass 记录插件注册的 Pass ID，卸载时自动清理。
// 插件在 OnInit 中调用 engine.RegisterPass 后，应调用此方法记录 Pass ID。
func (r *Registry) TrackPass(pluginID, passID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	st := r.pluginState[pluginID]
	st.passIDs = append(st.passIDs, passID)
	r.pluginState[pluginID] = st
}

// TrackPipeline 记录插件注册的 Pipeline ID，卸载时自动清理。
func (r *Registry) TrackPipeline(pluginID, pipelineID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	st := r.pluginState[pluginID]
	st.pipelineIDs = append(st.pipelineIDs, pipelineID)
	r.pluginState[pluginID] = st
}
