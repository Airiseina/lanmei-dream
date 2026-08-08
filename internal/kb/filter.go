package kb

import (
	"strings"
)

// filterChunk 在引擎层对单个分块应用筛选条件。
// 时序/来源/标签/白名单等可下推条件在 LocalProvider 已做过 SQL 下推，
// 此处作为兜底（对飞书等远程 provider 的结果同样生效），保证语义一致。
func filterChunk(c *Chunk, f *RecallFilter) bool {
	if c == nil {
		return false
	}
	if f == nil {
		return true
	}

	// 知识库白名单
	if len(f.KnowledgeIDs) > 0 {
		hit := false
		for _, id := range f.KnowledgeIDs {
			if id == c.KnowledgeBaseID {
				hit = true
				break
			}
		}
		if !hit {
			return false
		}
	}

	// 时序筛选（以更新时间为准，未设置时退回创建时间）
	t := c.UpdatedAt
	if t.IsZero() {
		t = c.CreatedAt
	}
	if f.StartTime != nil && t.Before(*f.StartTime) {
		return false
	}
	if f.EndTime != nil && t.After(*f.EndTime) {
		return false
	}

	// 来源筛选
	if len(f.Sources) > 0 {
		src, _ := c.Meta["source"].(string)
		if !stringInSlice(src, f.Sources) {
			return false
		}
	}

	// 标签筛选（meta.tags 命中任一）
	if len(f.Tags) > 0 {
		tags := metaStrings(c.Meta, "tags")
		if !intersectAny(tags, f.Tags) {
			return false
		}
	}

	return true
}

// metaStrings 从 chunk.Meta 读取字符串数组字段（兼容 []string 与 []any 两种反序列化形态）。
func metaStrings(meta map[string]any, key string) []string {
	raw, ok := meta[key]
	if !ok {
		return nil
	}
	switch v := raw.(type) {
	case []string:
		return v
	case []any:
		out := make([]string, 0, len(v))
		for _, it := range v {
			if s, ok := it.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func stringInSlice(s string, list []string) bool {
	for _, item := range list {
		if item == s {
			return true
		}
	}
	return false
}

// intersectAny 判断 a 与 b 是否存在交集（均为空白归一化比较）。
func intersectAny(a, b []string) bool {
	norm := func(s string) string { return strings.ToLower(strings.TrimSpace(s)) }
	bset := make(map[string]struct{}, len(b))
	for _, s := range b {
		bset[norm(s)] = struct{}{}
	}
	for _, s := range a {
		if _, ok := bset[norm(s)]; ok {
			return true
		}
	}
	return false
}
