package kb

import (
	"context"
	"time"

	"github.com/DaWesen/lanmei-dream/internal/ai/embedding"
	"github.com/DaWesen/lanmei-dream/internal/ai/tool"
	"github.com/DaWesen/lanmei-dream/internal/config"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// startupSyncTimeout 启动时内容同步的软超时（docs_dir 文件摄入）。
const startupSyncTimeout = 30 * time.Second

// defaultAutoRecallLimit 隐式召回注入条数的默认值。
const defaultAutoRecallLimit = 3

// Service 知识库系统对外门面：
//   - 加载配置并构建各 provider（可插拔）
//   - 提供召回入口（隐式 RAG 与 kb_search 工具共用）
//   - 提供 LLM 工具注册（主动召回）
type Service struct {
	engine       *Engine
	defaultModes []RecallMode
	autoLimit    int
	logger       *zap.Logger
}

// NewService 依据配置构建知识库服务。
//
// 注意：provider 工厂需在调用前通过 RegisterProvider 注册（main 中完成）。
// 单个知识库配置非法/构造失败时跳过并告警，不影响其它库；
// 若没有任何可用知识库，仍返回 Service（召回恒为空），由调用方决定是否启用。
func NewService(ctx context.Context, cfg *config.KnowledgeConfig, orm *gorm.DB, embedder embedding.Embedder, logger *zap.Logger) (*Service, error) {
	if cfg == nil {
		return nil, nil
	}
	if logger == nil {
		logger = zap.NewNop()
	}

	// 多路召回权重：直接采用配置值（viper SetDefault 已提供 1.0/0.8/0.5 默认）。
	// 仅当全部为 0（配置与默认均未提供）时回退内置默认值。
	weights := RecallWeights{
		Vector: cfg.Weights.Vector,
		Fuzzy:  cfg.Weights.Fuzzy,
		Time:   cfg.Weights.Time,
	}
	if weights.Vector == 0 && weights.Fuzzy == 0 && weights.Time == 0 {
		weights = DefaultRecallWeights
	}

	eng := NewEngine(weights, embedder, logger)
	deps := Deps{Orm: orm, Embedder: embedder, Logger: logger}

	for _, bc := range cfg.Bases {
		if !bc.Enabled {
			continue
		}
		if bc.ID == "" {
			logger.Warn("kb: 知识库配置缺少 id，已跳过")
			continue
		}
		if bc.Provider == "" {
			logger.Warn("kb: 知识库配置缺少 provider", zap.String("kb", bc.ID))
			continue
		}
		kbb := &KnowledgeBase{
			ID:          bc.ID,
			Name:        bc.Name,
			Description: bc.Description,
			Provider:    bc.Provider,
			Enabled:     true,
			RecallLimit: bc.RecallLimit,
			Config:      bc.Config,
		}
		p, err := createProvider(ctx, kbb, deps)
		if err != nil {
			logger.Warn("kb: 知识库构造失败", zap.String("kb", bc.ID), zap.Error(err))
			continue
		}
		eng.AddProvider(kbb, p)

		// 启动时同步（本地 provider 的 docs_dir 文件摄入）
		if syncr, ok := p.(Syncer); ok {
			syncCtx, cancel := context.WithTimeout(ctx, startupSyncTimeout)
			if err := syncr.Sync(syncCtx); err != nil {
				logger.Warn("kb: 启动同步失败", zap.String("kb", bc.ID), zap.Error(err))
			}
			cancel()
		}
		logger.Info("kb: 知识库就绪",
			zap.String("kb", bc.ID),
			zap.String("name", bc.Name),
			zap.String("provider", bc.Provider),
		)
	}

	autoLimit := cfg.AutoRecallLimit
	if autoLimit <= 0 {
		autoLimit = defaultAutoRecallLimit
	}

	return &Service{
		engine:       eng,
		defaultModes: parseModes(cfg.DefaultModes),
		autoLimit:    autoLimit,
		logger:       logger,
	}, nil
}

// Recall 执行召回（隐式 RAG 与 kb_search 工具共用入口）。
func (s *Service) Recall(ctx context.Context, req *RecallRequest) ([]ScoredChunk, error) {
	if s == nil || s.engine == nil {
		return nil, nil
	}
	return s.engine.Recall(ctx, req)
}

// DefaultModes 返回配置的默认召回模式（空表示使用 provider 全部能力）。
func (s *Service) DefaultModes() []RecallMode {
	if s == nil {
		return nil
	}
	return s.defaultModes
}

// AutoRecallLimit 返回隐式召回注入条数。
func (s *Service) AutoRecallLimit() int {
	if s == nil {
		return 0
	}
	return s.autoLimit
}

// List 返回全部已加载知识库。
func (s *Service) List() []KnowledgeBase {
	if s == nil || s.engine == nil {
		return nil
	}
	return s.engine.List()
}

// Close 关闭全部 provider。
func (s *Service) Close() {
	if s != nil && s.engine != nil {
		s.engine.Close()
	}
}

// RegisterTools 将主动召回工具注册进 AI 工具注册表。
// 注册后自动参与 eino 工具调用循环与意图分析工具列表。
func (s *Service) RegisterTools(reg *tool.Registry) error {
	if reg == nil {
		return nil
	}
	if err := reg.Register(kbSearchTool(s)); err != nil {
		return err
	}
	// kb_add 仅在存在本地知识库时提供（远程 provider 不支持写入）
	if s.hasLocalBase() {
		if err := reg.Register(kbAddTool(s)); err != nil {
			return err
		}
	}
	return nil
}

// hasLocalBase 判断是否存在 local provider 知识库。
func (s *Service) hasLocalBase() bool {
	for _, kbb := range s.List() {
		if kbb.Provider == "local" {
			return true
		}
	}
	return false
}

// parseModes 将配置的字符串模式列表解析为 RecallMode 列表（过滤非法值与重复项）。
func parseModes(modes []string) []RecallMode {
	out := make([]RecallMode, 0, len(modes))
	seen := make(map[RecallMode]struct{}, len(modes))
	for _, m := range modes {
		mode := RecallMode(m)
		if !mode.Valid() {
			continue
		}
		if _, dup := seen[mode]; dup {
			continue
		}
		seen[mode] = struct{}{}
		out = append(out, mode)
	}
	return out
}
