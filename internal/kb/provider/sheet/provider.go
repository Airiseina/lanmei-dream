// Package sheet 实现基于飞书电子表格（Sheets）的 KV 知识库 Provider。
//
// 能力：
//   - vector：向量召回（本地对每行知识内容实时向量化 + 余弦相似度排序，结果缓存）
//   - fuzzy：模糊召回（对索引列/知识列的本地 token 命中评分）
//
// 表格结构（KV）：两列（默认 A=索引/关键词，B=知识内容），首行为表头。
// 通过飞书 Sheets v2 的 values 接口读取整列非空范围，在本地完成召回计算；
// 行数据与向量均带 TTL 内存缓存，避免每次查询都打飞书接口。
//
// 认证：应用身份 tenant_access_token（SDK 自动换取，本地按过期时间缓存）。
// 需在飞书开放平台为企业自建应用开通 sheets:spreadsheet:readonly 权限，
// 并将应用添加为目标电子表格的协作者（可查看）。
package sheet

import (
	"context"
	"fmt"
	"sync"
	"time"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"

	"github.com/DaWesen/lanmei-dream/internal/ai/embedding"
	kbpkg "github.com/DaWesen/lanmei-dream/internal/kb"
	"go.uber.org/zap"
)

// providerName sheet provider 的类型标识。
const providerName = "sheet"

// 默认配置值
const (
	defaultFuzzyThreshold = 0.1  // 模糊召回最低分数
	defaultCacheTTL       = 600  // 行/向量缓存 TTL（秒）
	defaultFetchTimeout   = 15   // 单次全量拉取软超时（秒）
	defaultMaxRows        = 2000 // 拉取行数上限（防超大表格拖慢启动）
	defaultSkipHeaderRows = 1    // 默认跳过首行表头
)

// kvRow 一行 KV 记录（本地缓存单元）。
type kvRow struct {
	id      string // 行序号（1-based），作为 Chunk.ID
	index   string // 索引内容（关键词/问题）
	content string // 知识内容（回答/正文）
}

// Provider 基于飞书电子表格的 KV 知识库 Provider。
type Provider struct {
	client    *lark.Client
	appID     string
	appSecret string
	kb        *kbpkg.KnowledgeBase
	embedder  embedding.Embedder // 可为 nil（vector 模式自动禁用）

	spreadsheetToken string // 表格 token（URL /sheets/<token>）
	sheetID          string // 工作表 ID（URL ?sheet= 参数；空则由 sheet_name 解析）
	sheetName        string // 工作表名（sheet_id 为空时的解析依据，默认 Sheet1）
	indexColumn      string // 索引列字母（默认 A）
	contentColumn    string // 知识列字母（默认 B）
	skipHeaderRows   int    // 跳过表头行数

	maxRows        int
	fuzzyThreshold float64
	cacheTTL       time.Duration
	fetchTimeout   time.Duration

	mu         sync.Mutex // 保护 rows/loaded/fetchedAt/embeddings
	rows       []*kvRow
	loaded     bool
	fetchedAt  time.Time
	embeddings map[string][]float32 // rowID -> 内容向量（惰性计算）

	tokenMu     sync.Mutex // 保护 tokenValue/tokenExpire
	tokenValue  string
	tokenExpire time.Time

	logger *zap.Logger
}

// New 构建飞书电子表格 provider。
func New(_ context.Context, kbb *kbpkg.KnowledgeBase, cfg map[string]any, deps kbpkg.Deps) (kbpkg.Provider, error) {
	appID := kbpkg.ResolveSecret(cfg["app_id"])
	appSecret := kbpkg.ResolveSecret(cfg["app_secret"])
	if appID == "" || appSecret == "" {
		return nil, fmt.Errorf("kb sheet: 缺少 app_id/app_secret 配置（支持 env:VAR_NAME 从环境变量读取）")
	}
	spreadsheetToken := kbpkg.ConfigString(cfg, "spreadsheet_token", "")
	if spreadsheetToken == "" {
		return nil, fmt.Errorf("kb sheet: 缺少 spreadsheet_token（飞书电子表格 URL 中 /sheets/<token> 段）")
	}
	if deps.Logger == nil {
		deps.Logger = zap.NewNop()
	}

	threshold := kbpkg.ConfigFloat(cfg, "fuzzy_threshold", defaultFuzzyThreshold)
	if threshold <= 0 || threshold > 1 {
		threshold = defaultFuzzyThreshold
	}
	maxRows := kbpkg.ConfigInt(cfg, "max_rows", defaultMaxRows)
	if maxRows <= 0 {
		maxRows = defaultMaxRows
	}
	ttl := kbpkg.ConfigInt(cfg, "cache_ttl_seconds", defaultCacheTTL)
	if ttl <= 0 {
		ttl = defaultCacheTTL
	}
	skip := max(kbpkg.ConfigInt(cfg, "skip_header_rows", defaultSkipHeaderRows), 0)

	indexCol := kbpkg.ConfigString(cfg, "index_column", "A")
	contentCol := kbpkg.ConfigString(cfg, "content_column", "B")
	sheetName := kbpkg.ConfigString(cfg, "sheet_name", "Sheet1")

	return &Provider{
		client:           lark.NewClient(appID, appSecret),
		appID:            appID,
		appSecret:        appSecret,
		kb:               kbb,
		embedder:         deps.Embedder,
		spreadsheetToken: spreadsheetToken,
		sheetID:          kbpkg.ConfigString(cfg, "sheet_id", ""),
		sheetName:        sheetName,
		indexColumn:      indexCol,
		contentColumn:    contentCol,
		skipHeaderRows:   skip,
		maxRows:          maxRows,
		fuzzyThreshold:   threshold,
		cacheTTL:         time.Duration(ttl) * time.Second,
		fetchTimeout:     defaultFetchTimeout * time.Second,
		embeddings:       make(map[string][]float32),
		logger:           deps.Logger,
	}, nil
}

// Name 实现 kb.Provider
func (p *Provider) Name() string { return providerName }

// Capabilities 实现 kb.Provider。
// vector 模式依赖 embedder（deps 注入），未注入时仅声明 fuzzy。
func (p *Provider) Capabilities() kbpkg.Capabilities {
	modes := []kbpkg.RecallMode{kbpkg.RecallModeFuzzy}
	if p.embedder != nil {
		modes = append(modes, kbpkg.RecallModeVector)
	}
	return kbpkg.Capabilities{Modes: modes}
}

// Close 实现 kb.Provider：无独立连接需释放。
func (p *Provider) Close() error { return nil }

// Sync 实现 kb.Syncer：启动时预热行缓存（幂等）。
func (p *Provider) Sync(ctx context.Context) error {
	return p.ensureRows(ctx)
}

// token 返回有效的 tenant_access_token（带过期时间本地缓存）。
func (p *Provider) token(ctx context.Context) (string, error) {
	p.tokenMu.Lock()
	defer p.tokenMu.Unlock()
	if p.tokenValue != "" && time.Now().Before(p.tokenExpire) {
		return p.tokenValue, nil
	}
	// 预取余量 60s，避免 token 恰好过期导致请求失败
	resp, err := p.client.GetTenantAccessTokenBySelfBuiltApp(ctx,
		&larkcore.SelfBuiltTenantAccessTokenReq{AppID: p.appID, AppSecret: p.appSecret})
	if err != nil {
		return "", fmt.Errorf("kb sheet: 获取 tenant_access_token: %w", err)
	}
	if !resp.Success() || resp.TenantAccessToken == "" {
		return "", fmt.Errorf("kb sheet: 获取 tenant_access_token 失败 code=%d msg=%s", resp.Code, resp.Msg)
	}
	p.tokenValue = resp.TenantAccessToken
	ttl := resp.Expire
	if ttl <= 0 {
		ttl = 7200 // 飞书默认 2 小时
	}
	p.tokenExpire = time.Now().Add(time.Duration(ttl)*time.Second - 60*time.Second)
	return p.tokenValue, nil
}
