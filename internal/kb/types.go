// Package kb 提供知识库系统：多模式召回（向量/模糊/时间）+ Provider 抽象 + LLM 工具。
//
// 设计目标：
//   - 与 eino/conduit 架构优雅结合：召回结果作为 system 消息注入 ChatService，
//     主动召回通过 tool.Registry 注册的 kb_search/kb_add 工具参与 eino 工具调用循环；
//   - Provider 抽象层抹平本地数据库 / 飞书 / 未来其它知识库产品的差异，
//     新增召回模式（如 graph）只需扩展 RecallMode 常量并让 Provider 声明能力；
//   - 多路召回合并采用与 ai/memory 一致的 rank 加权算法，跨 provider 分数可比。
package kb

import "time"

// RecallMode 召回模式标识。
// 未来新增模式（如 graph）只需追加常量、让 Provider 在 Capabilities 中声明支持，
// 引擎会自动把该模式分发给支持它的 Provider，未支持的 Provider 跳过并告警。
type RecallMode string

const (
	RecallModeVector RecallMode = "vector" // 向量召回（语义相似）
	RecallModeFuzzy  RecallMode = "fuzzy"  // 模糊召回（倒排索引/全文匹配）
	RecallModeTime   RecallMode = "time"   // 时间召回（最近更新）
)

// Valid 判断模式是否合法
func (m RecallMode) Valid() bool {
	switch m {
	case RecallModeVector, RecallModeFuzzy, RecallModeTime:
		return true
	}
	return false
}

// Chunk 一条知识分块，是 provider 无关的统一表示。
type Chunk struct {
	ID                string         // provider 内唯一标识（本地=自增ID字符串，飞书=node_id）
	KnowledgeBaseID   string         // 所属知识库 ID（配置 bases[].id）
	KnowledgeBaseName string         // 知识库名称（展示用）
	Provider          string         // provider 类型标识（local/feishu）
	Title             string         // 标题
	Content           string         // 正文内容
	URL               string         // 原文链接（可选）
	Meta              map[string]any // 元数据：source/tags 等（筛选依据）
	CreatedAt         time.Time      // 创建时间（时序筛选）
	UpdatedAt         time.Time      // 更新时间（时序筛选）
}

// ScoredChunk 带分数的召回结果。
// Score 为 provider 给出的原始相关度（0~1，仅供阈值过滤参考）；
// 跨 provider 的最终排序由引擎按 rank 加权计算，不直接比较此值。
type ScoredChunk struct {
	Chunk *Chunk
	Score float64
}

// RecallResult 单个 Provider 的召回结果：按模式分组的排序列表。
type RecallResult struct {
	ByMode map[RecallMode][]ScoredChunk // 每个模式内的列表已按相关度降序
}

// RecallFilter 召回筛选条件。
// 引擎层对合并结果统一应用；LocalProvider 支持 SQL 下推（时序/来源/标签/KB 白名单）。
type RecallFilter struct {
	KnowledgeIDs []string   // 限定知识库 ID（nil=全部已启用）
	StartTime    *time.Time // 时序筛选：更新时间下限
	EndTime      *time.Time // 时序筛选：更新时间上限
	Sources      []string   // 来源筛选（meta.source 命中任一）
	Tags         []string   // 标签筛选（meta.tags 含任一）
	MinScore     float64    // 最低合并分数（仅引擎层生效）
}

// RecallRequest 一次召回查询。
type RecallRequest struct {
	Query       string        // 查询文本（必填）
	QueryVector []float32     // 查询向量（可选；缺省且需要 vector 模式时由引擎用 embedder 补齐）
	Modes       []RecallMode  // 期望模式（nil/空=Provider 全部能力）
	Limit       int           // 每个 provider 每模式的召回上限（>0 生效）
	Filter      *RecallFilter // 筛选条件（nil=不筛选）
}

// RecallWeights 各召回模式的合并权重。
// 多路命中同一分块时分数累加，权重越大该模式证据越强。
type RecallWeights struct {
	Vector float64
	Fuzzy  float64
	Time   float64
}

// DefaultRecallWeights 默认权重：向量最高，模糊次之，时间最低（与 ai/memory 语义一致）。
var DefaultRecallWeights = RecallWeights{Vector: 1.0, Fuzzy: 0.8, Time: 0.5}

// Weight 返回指定模式的权重。
func (w RecallWeights) Weight(m RecallMode) float64 {
	switch m {
	case RecallModeVector:
		return w.Vector
	case RecallModeFuzzy:
		return w.Fuzzy
	case RecallModeTime:
		return w.Time
	}
	return 0
}

// KnowledgeBase 知识库元信息，加载自配置。
type KnowledgeBase struct {
	ID          string
	Name        string
	Description string
	Provider    string // provider 类型标识（local/feishu）
	Enabled     bool
	RecallLimit int            // 单模式召回上限（默认 5）
	Config      map[string]any // provider 私有配置
}

// RecallLimitOrDefault 返回有效的召回上限（<=0 时用默认值）。
func (k *KnowledgeBase) RecallLimitOrDefault() int {
	if k == nil || k.RecallLimit <= 0 {
		return 5
	}
	return k.RecallLimit
}
