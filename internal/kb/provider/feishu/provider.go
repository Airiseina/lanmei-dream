// Package feishu 实现基于飞书知识库（Wiki）的 Provider，提供 vector/fuzzy/time 召回。
//
// 飞书开放平台不提供服务端向量检索，故通过 Wiki API 拉取知识空间节点树
// （SpaceNode.List）与文档纯文本（Document.RawContent）在本地计算召回，
// 文档与向量均带 TTL 内存缓存，避免每次查询都打飞书接口。
//
// 认证使用应用身份（tenant access token，SDK 按接口声明的 token 类型自动换取），
// 需为企业自建应用开通 Wiki 与文档读取权限，并将应用添加为目标知识空间成员。
package feishu

import (
	"context"
	"fmt"
	"sync"
	"time"

	lark "github.com/larksuite/oapi-sdk-go/v3"

	"github.com/DaWesen/lanmei-dream/internal/ai/embedding"
	kbpkg "github.com/DaWesen/lanmei-dream/internal/kb"
	"go.uber.org/zap"
)

// providerName 飞书 provider 的类型标识。
const providerName = "feishu"

// 默认配置值
const (
	defaultFuzzyThreshold = 0.1 // 模糊召回最低分数
	defaultMaxNodes       = 200 // 拉取节点数上限（防止超大知识空间拖慢启动）
	defaultCacheTTL       = 600 // 文档/向量缓存 TTL（秒）
	defaultFetchWorkers   = 8   // 拉取文档内容的最大并发数
	defaultFetchTimeout   = 15  // 单次全量拉取软超时（秒）
	defaultPageSize       = 50  // 飞书分页大小
)

// cachedDoc 一条已拉取的飞书文档（节点 + 纯文本）。
type cachedDoc struct {
	nodeToken string // 节点 token（作为 Chunk.ID）
	title     string
	content   string
	url       string
	createdAt time.Time
	updatedAt time.Time
}

// Provider 基于飞书知识库的 Provider。
type Provider struct {
	client   *lark.Client
	kb       *kbpkg.KnowledgeBase
	embedder embedding.Embedder // 可为 nil（vector 模式自动禁用）

	spaceID        string // 目标知识空间 ID（空则取第一个有权限的空间）
	nodeToken      string // 起始节点 token（空则从空间根节点开始）
	maxNodes       int
	fuzzyThreshold float64
	cacheTTL       time.Duration
	fetchTimeout   time.Duration
	workers        int

	mu         sync.Mutex   // 保护 docs/fetchedAt/embeddings
	docs       []*cachedDoc // 已拉取文档（拉取失败且缓存非空时保留旧值降级）
	loaded     bool         // 是否已完成首次成功拉取（空文档空间也视为已加载）
	fetchedAt  time.Time
	embeddings map[string][]float32 // nodeToken -> 内容向量（惰性计算）

	logger *zap.Logger
}

// New 构建飞书 provider。
func New(_ context.Context, kbb *kbpkg.KnowledgeBase, cfg map[string]any, deps kbpkg.Deps) (kbpkg.Provider, error) {
	appID := kbpkg.ResolveSecret(cfg["app_id"])
	appSecret := kbpkg.ResolveSecret(cfg["app_secret"])
	if appID == "" || appSecret == "" {
		return nil, fmt.Errorf("kb feishu: 缺少 app_id/app_secret 配置（支持 env:VAR_NAME 从环境变量读取）")
	}
	if deps.Logger == nil {
		deps.Logger = zap.NewNop()
	}

	threshold := kbpkg.ConfigFloat(cfg, "fuzzy_threshold", defaultFuzzyThreshold)
	if threshold <= 0 || threshold > 1 {
		threshold = defaultFuzzyThreshold
	}
	maxNodes := kbpkg.ConfigInt(cfg, "max_nodes", defaultMaxNodes)
	if maxNodes <= 0 {
		maxNodes = defaultMaxNodes
	}
	ttl := kbpkg.ConfigInt(cfg, "cache_ttl_seconds", defaultCacheTTL)
	if ttl <= 0 {
		ttl = defaultCacheTTL
	}
	workers := kbpkg.ConfigInt(cfg, "fetch_workers", defaultFetchWorkers)
	if workers <= 0 {
		workers = defaultFetchWorkers
	}

	return &Provider{
		client:         lark.NewClient(appID, appSecret),
		kb:             kbb,
		embedder:       deps.Embedder,
		spaceID:        kbpkg.ConfigString(cfg, "space_id", ""),
		nodeToken:      kbpkg.ConfigString(cfg, "node_token", ""),
		maxNodes:       maxNodes,
		fuzzyThreshold: threshold,
		cacheTTL:       time.Duration(ttl) * time.Second,
		fetchTimeout:   defaultFetchTimeout * time.Second,
		workers:        workers,
		embeddings:     make(map[string][]float32),
		logger:         deps.Logger,
	}, nil
}

// Name 实现 kb.Provider。
func (p *Provider) Name() string { return providerName }

// Capabilities 实现 kb.Provider。
// vector 模式依赖 embedder（deps 注入），未注入时仅声明 fuzzy/time。
func (p *Provider) Capabilities() kbpkg.Capabilities {
	modes := []kbpkg.RecallMode{kbpkg.RecallModeFuzzy, kbpkg.RecallModeTime}
	if p.embedder != nil {
		modes = append(modes, kbpkg.RecallModeVector)
	}
	return kbpkg.Capabilities{Modes: modes}
}

// Close 实现 kb.Provider：飞书客户端无独立连接需释放。
func (p *Provider) Close() error { return nil }

// Sync 实现 kb.Syncer：启动时预热文档缓存（幂等）。
func (p *Provider) Sync(ctx context.Context) error {
	return p.ensureDocs(ctx)
}
