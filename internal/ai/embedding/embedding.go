// Package embedding 定义文本向量化抽象（Embedder）与实现：
// EinoEmbedder（OpenAI 兼容 embeddings 接口）与 VolcEmbedder（火山方舟多模态接口）。
package embedding

import "context"

// Embedder 抽象文本向量化能力，用于 RAG 检索、知识库召回与话题语义判定。
// 具体实现（OpenAI embedding / 火山方舟 / 本地模型等）由外部注入。
//
// 契约：
//   - 实现必须并发安全：同一实例会被多条消息的处理 goroutine 并发调用；
//   - 必须尊重 ctx 的取消与超时；
//   - Dimension 返回值须与 Embed/EmbedBatch 产出的向量长度一致且保持稳定（调用方据此建库）；
//   - EmbedBatch 的返回顺序与输入文本一一对应；texts 为空时返回 (nil, nil)；
//   - 返回 error 时调用方按降级处理（跳过语义检索/向量记忆），不视为致命错误。
type Embedder interface {
	// Embed 将单段文本转为向量。
	//
	// 返回：文本的向量表示；向量化失败返回错误。
	Embed(ctx context.Context, text string) ([]float32, error)
	// EmbedBatch 批量向量化。
	//
	// 返回：与输入顺序一一对应的向量列表；texts 为空时返回 (nil, nil)。
	EmbedBatch(ctx context.Context, texts []string) ([][]float32, error)
	// Dimension 返回向量维度。
	Dimension() int
}
