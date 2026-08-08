package kb

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/DaWesen/lanmei-dream/internal/ai/tool"
	"github.com/cloudwego/eino/schema"
)

// kbSearchTool 返回「知识库检索」工具（主动召回）。
// LLM 在需要核实事实 / 查询已录入知识时调用。
func kbSearchTool(s *Service) *tool.Tool {
	return &tool.Tool{
		Info: &schema.ToolInfo{
			Name: "kb_search",
			Desc: "在知识库中检索信息。当用户询问与已录入知识相关的问题、或需要核实事实时调用。",
			ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
				"query": {
					Type:     schema.String,
					Desc:     "检索的问题或关键词",
					Required: true,
				},
				"knowledge_base_id": {
					Type: schema.String,
					Desc: "限定知识库 ID（缺省检索全部知识库）",
				},
				"limit": {
					Type: schema.Integer,
					Desc: "返回条数，1-5，默认 3",
				},
			}),
		},
		Handler: s.handleKBSearch,
	}
}

// kbAddTool 返回「知识录入」工具（仅本地知识库）。
// 把值得长期记住的事实/规范/偏好录入本地知识库。
func kbAddTool(s *Service) *tool.Tool {
	return &tool.Tool{
		Info: &schema.ToolInfo{
			Name: "kb_add",
			Desc: "把一段值得长期记住的信息录入本地知识库。适用于用户告知的明确事实、规范、偏好。",
			ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
				"content": {
					Type:     schema.String,
					Desc:     "要录入的知识内容",
					Required: true,
				},
				"knowledge_base_id": {
					Type: schema.String,
					Desc: "目标知识库 ID（缺省第一个本地知识库）",
				},
				"title": {
					Type: schema.String,
					Desc: "标题（可选，缺省取内容前 20 字）",
				},
			}),
		},
		Handler: s.handleKBAdd,
	}
}

// kbSearchArgs kb_search 工具参数
type kbSearchArgs struct {
	Query           string `json:"query"`
	KnowledgeBaseID string `json:"knowledge_base_id"`
	Limit           int    `json:"limit"`
}

// handleKBSearch 执行知识库检索并返回格式化结果。
func (s *Service) handleKBSearch(ctx context.Context, argsJSON string) (string, error) {
	var args kbSearchArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("kb_search 参数解析失败: %w", err)
	}
	query := strings.TrimSpace(args.Query)
	if query == "" {
		return "请提供要检索的 query 参数", nil
	}
	limit := args.Limit
	if limit <= 0 {
		limit = 3
	}
	if limit > 5 {
		limit = 5
	}

	var filter *RecallFilter
	if strings.TrimSpace(args.KnowledgeBaseID) != "" {
		filter = &RecallFilter{KnowledgeIDs: []string{strings.TrimSpace(args.KnowledgeBaseID)}}
	}

	results, err := s.Recall(ctx, &RecallRequest{
		Query:  query,
		Modes:  s.DefaultModes(),
		Limit:  limit,
		Filter: filter,
	})
	if err != nil {
		return "", fmt.Errorf("kb_search 失败: %w", err)
	}
	if len(results) == 0 {
		return "知识库中未找到相关内容", nil
	}
	return FormatRecall(results), nil
}

// kbAddArgs kb_add 工具参数
type kbAddArgs struct {
	Content         string `json:"content"`
	KnowledgeBaseID string `json:"knowledge_base_id"`
	Title           string `json:"title"`
}

// handleKBAdd 将内容录入本地知识库（幂等）。
func (s *Service) handleKBAdd(ctx context.Context, argsJSON string) (string, error) {
	var args kbAddArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("kb_add 参数解析失败: %w", err)
	}
	content := strings.TrimSpace(args.Content)
	if content == "" {
		return "请提供要录入的 content 参数", nil
	}

	// 定位目标本地知识库
	target := s.findLocalBase(args.KnowledgeBaseID)
	if target == nil {
		return "当前未配置本地知识库，无法录入", nil
	}

	title := strings.TrimSpace(args.Title)
	if title == "" {
		title = truncateRunes(content, 20)
	}

	chunk := &Chunk{
		ID:                fmt.Sprintf("llm:%d:%x", time.Now().UnixNano(), fnvHash(content)),
		KnowledgeBaseID:   target.ID,
		KnowledgeBaseName: target.Name,
		Provider:          target.Provider,
		Title:             title,
		Content:           content,
		Meta:              map[string]any{"source": "llm"},
		CreatedAt:         time.Now(),
		UpdatedAt:         time.Now(),
	}

	if err := s.storeInto(ctx, target, chunk); err != nil {
		return "", fmt.Errorf("kb_add 失败: %w", err)
	}
	return fmt.Sprintf("已录入知识库「%s」", target.Name), nil
}

// findLocalBase 按 ID 或默认规则（第一个本地库）查找目标知识库。
func (s *Service) findLocalBase(id string) *KnowledgeBase {
	for _, kbb := range s.List() {
		if kbb.Provider != "local" {
			continue
		}
		if id == "" || kbb.ID == id {
			return &kbb
		}
	}
	return nil
}

// storeInto 通过 provider 的 Ingester 能力写入分块。
func (s *Service) storeInto(ctx context.Context, kbb *KnowledgeBase, chunk *Chunk) error {
	p, ok := s.engine.Provider(kbb.ID)
	if !ok {
		return fmt.Errorf("kb: 知识库 %q 不可用", kbb.ID)
	}
	ing, ok := p.(Ingester)
	if !ok {
		return fmt.Errorf("kb: 知识库 %q 不支持内容写入", kbb.ID)
	}
	return ing.Store(ctx, chunk)
}

// fnvHash 计算字符串的 64 位 FNV-1a 哈希（用于生成稳定 source_id）。
func fnvHash(s string) uint64 {
	var h uint64 = 14695981039346656037
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= 1099511628211
	}
	return h
}
