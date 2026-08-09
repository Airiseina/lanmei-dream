package bizplugin

import (
	"fmt"

	"github.com/DaWesen/lanmei-dream/internal/config"
	"github.com/DaWesen/lanmei-dream/internal/database"
	pluginpkg "github.com/DaWesen/lanmei-dream/internal/plugin"
	"go.uber.org/zap"
)

// ============================================================
// BusinessRegistry 内置业务插件注册表
// ============================================================

// BusinessRegistry 负责按配置文件开关动态注册内置业务插件，
// 替代在 main.go 中硬编码 if 块逐个注册的写法。
//
// 设计要点：
//   - 插件启用与否完全由配置（[plugin.builtins]）决定，改配置即可启停，无需改代码；
//   - 注册前先查询注册表：若同名插件已由 Wasm 动态加载（如用户安装了 wasm 版签到/欢迎），
//     则跳过内置注册，避免 ID 冲突，保证"注册表优先、Wasm 优先"的动态性；
//   - 内置插件与 Wasm 插件走同一注册表，后续生命周期（Init/Start/Stop）统一由 Registry 管理。
type BusinessRegistry struct {
	cfg      *config.PluginBuiltinsConfig // 内置插件开关配置
	registry *pluginpkg.Registry          // 插件注册表
	db       *database.DB                 // 数据库（签到等插件依赖）
	logger   *zap.Logger
}

// NewBusinessRegistry 创建内置业务插件注册表。
func NewBusinessRegistry(cfg *config.PluginBuiltinsConfig, registry *pluginpkg.Registry, db *database.DB, logger *zap.Logger) *BusinessRegistry {
	return &BusinessRegistry{
		cfg:      cfg,
		registry: registry,
		db:       db,
		logger:   logger,
	}
}

// RegisterBuiltins 按配置注册所有内置业务插件。
//
// 每个内置插件按以下规则处理：
//  1. 配置未启用 → 跳过并记录日志；
//  2. 注册表中已存在同名插件（Wasm 动态加载）→ 跳过并记录日志；
//  3. 否则注册内置实现。
//
// 任一插件注册失败立即返回错误（由调用方决定终止启动）。
func (r *BusinessRegistry) RegisterBuiltins() error {
	if r.registry == nil {
		return fmt.Errorf("bizplugin: 插件注册表为空，无法注册内置插件")
	}
	logger := r.logger
	if logger == nil {
		logger = zap.NewNop()
	}

	// ── 签到插件 ──
	enabled := r.cfg == nil || r.cfg.Signin // 配置为空时按默认开启处理
	if !enabled {
		logger.Info("bizplugin: 签到插件已由配置关闭，跳过注册")
	} else if _, loaded := r.registry.Get("signin"); loaded {
		logger.Info("bizplugin: 签到插件已由 Wasm 动态加载，跳过内置注册")
	} else {
		if err := r.registry.Register(NewSigninPlugin(r.db, logger)); err != nil {
			return fmt.Errorf("bizplugin: 注册签到插件失败: %w", err)
		}
	}

	// ── 入群欢迎插件 ──
	enabled = r.cfg == nil || r.cfg.Welcome
	if !enabled {
		logger.Info("bizplugin: 入群欢迎插件已由配置关闭，跳过注册")
	} else if _, loaded := r.registry.Get("welcome"); loaded {
		logger.Info("bizplugin: 入群欢迎插件已由 Wasm 动态加载，跳过内置注册")
	} else {
		if err := r.registry.Register(NewWelcomePlugin(logger)); err != nil {
			return fmt.Errorf("bizplugin: 注册入群欢迎插件失败: %w", err)
		}
	}

	return nil
}
