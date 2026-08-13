package llm

// ─────────────────────────────────────────────────────────────
// LLM Provider 定义与计费
// ─────────────────────────────────────────────────────────────

// Provider 运行时的 LLM Provider 配置。
// APIKey 明文仅存在于内存（数据库侧以 AES-256-GCM 加密存储）。
type Provider struct {
	ID           uint
	Name         string
	BaseURL      string
	APIKey       string
	Model        string
	MaxTokens    int
	Temperature  float64
	InPricePerM  float64 // 每百万输入 token 价格（元）
	OutPricePerM float64 // 每百万输出 token 价格（元）
	Enabled      bool
	Priority     int
}

// CostCents 按定价表计算一次调用的费用（分）。
// 价格为 0 视为不计费（免费 provider）。
func (p *Provider) CostCents(inputTokens, outputTokens int64) int64 {
	if p == nil || (p.InPricePerM <= 0 && p.OutPricePerM <= 0) {
		return 0
	}
	inCost := float64(inputTokens) * p.InPricePerM / 1_000_000
	outCost := float64(outputTokens) * p.OutPricePerM / 1_000_000
	// 四舍五入到分
	return int64((inCost + outCost) * 100 + 0.5)
}
