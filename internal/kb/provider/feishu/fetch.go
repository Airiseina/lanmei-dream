package feishu

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"time"

	larkdocx "github.com/larksuite/oapi-sdk-go/v3/service/docx/v1"
	larkwiki "github.com/larksuite/oapi-sdk-go/v3/service/wiki/v2"
	"go.uber.org/zap"
)

// ensureDocs 返回文档缓存，必要时从飞书重新拉取。
//
// 缓存逻辑：
//   - 缓存有效期内直接返回；
//   - 拉取失败但已有缓存时降级使用旧数据（记录告警），保证召回不被单次网络故障击穿；
//   - 拉取失败且无缓存时返回错误，由上层按空结果处理。
func (p *Provider) ensureDocs(ctx context.Context) error {
	p.mu.Lock()
	fresh := p.loaded && time.Since(p.fetchedAt) < p.cacheTTL
	p.mu.Unlock()
	if fresh {
		return nil
	}

	cctx, cancel := context.WithTimeout(ctx, p.fetchTimeout)
	defer cancel()

	docs, err := p.fetchDocs(cctx)
	if err != nil {
		p.mu.Lock()
		haveCache := p.loaded
		p.mu.Unlock()
		if haveCache {
			p.logger.Warn("kb feishu: 文档拉取失败，降级使用缓存", zap.Error(err))
			return nil
		}
		return err
	}

	p.mu.Lock()
	p.docs = docs
	p.loaded = true
	p.fetchedAt = time.Now()
	// 内容更新后失效全部向量缓存，避免脏向量
	p.embeddings = make(map[string][]float32)
	p.mu.Unlock()
	p.logger.Info("kb feishu: 知识文档已缓存",
		zap.String("kb", p.kb.ID), zap.Int("docs", len(docs)))
	return nil
}

// snapshotDocs 返回当前缓存的文档快照（nil 安全）。
func (p *Provider) snapshotDocs() []*cachedDoc {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.docs) == 0 {
		return nil
	}
	out := make([]*cachedDoc, len(p.docs))
	copy(out, p.docs)
	return out
}

// fetchDocs 从飞书拉取知识空间文档（节点树遍历 + 内容并发拉取）。
func (p *Provider) fetchDocs(ctx context.Context) ([]*cachedDoc, error) {
	spaceID := p.spaceID
	if spaceID == "" {
		sid, err := p.findFirstSpace(ctx)
		if err != nil {
			return nil, err
		}
		spaceID = sid
	}

	// 1. 遍历节点树（深度优先，受 maxNodes 限制）
	nodes := make([]*larkwiki.Node, 0, 64)
	seen := make(map[string]struct{})
	if err := p.collectNodes(ctx, spaceID, p.nodeToken, &nodes, seen); err != nil {
		return nil, err
	}

	// 2. 仅保留新版文档（docx）节点，截断到 maxNodes
	var targets []*larkwiki.Node
	for _, n := range nodes {
		if n == nil || n.ObjToken == nil || n.ObjType == nil {
			continue
		}
		if *n.ObjType != larkwiki.ObjTypeObjTypeDocx {
			continue
		}
		targets = append(targets, n)
		if len(targets) >= p.maxNodes {
			break
		}
	}
	if len(targets) == 0 {
		p.logger.Warn("kb feishu: 知识空间无可用 docx 文档", zap.String("kb", p.kb.ID))
		return nil, nil
	}

	// 3. 并发拉取文档纯文本（worker 池，尊重 ctx 取消）
	docs := make([]*cachedDoc, len(targets))
	var wg sync.WaitGroup
	sem := make(chan struct{}, p.workers)
	for i, n := range targets {
		wg.Add(1)
		go func(i int, n *larkwiki.Node) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				return
			}
			docs[i] = p.fetchOneDoc(ctx, n)
		}(i, n)
	}
	wg.Wait()

	// 4. 过滤未拉取成功的条目（ctx 取消或接口失败）
	out := make([]*cachedDoc, 0, len(docs))
	for _, d := range docs {
		if d != nil {
			out = append(out, d)
		}
	}
	return out, nil
}

// findFirstSpace 返回第一个有权限的知识空间 ID。
func (p *Provider) findFirstSpace(ctx context.Context) (string, error) {
	resp, err := p.client.Wiki.V2.Space.List(ctx, larkwiki.NewListSpaceReqBuilder().Limit(20).Build())
	if err != nil {
		return "", fmt.Errorf("kb feishu: 拉取知识空间列表: %w", err)
	}
	if resp == nil || resp.Data == nil || len(resp.Data.Items) == 0 {
		return "", fmt.Errorf("kb feishu: 未找到有权限的知识空间（请将应用添加为目标知识空间成员）")
	}
	if resp.Data.Items[0].SpaceId == nil || *resp.Data.Items[0].SpaceId == "" {
		return "", fmt.Errorf("kb feishu: 知识空间缺少 space_id")
	}
	return *resp.Data.Items[0].SpaceId, nil
}

// collectNodes 深度优先收集节点树，直到全部遍历完或达到 maxNodes。
func (p *Provider) collectNodes(ctx context.Context, spaceID, parentToken string, out *[]*larkwiki.Node, seen map[string]struct{}) error {
	level, err := p.listLevel(ctx, spaceID, parentToken)
	if err != nil {
		return err
	}
	for _, n := range level {
		if n == nil || n.NodeToken == nil {
			continue
		}
		token := *n.NodeToken
		if _, dup := seen[token]; dup {
			continue // 防环
		}
		seen[token] = struct{}{}
		*out = append(*out, n)
		if len(*out) >= p.maxNodes {
			return nil
		}
		// 有子节点则递归
		if n.HasChild != nil && *n.HasChild {
			if err := p.collectNodes(ctx, spaceID, token, out, seen); err != nil {
				return err
			}
			if len(*out) >= p.maxNodes {
				return nil
			}
		}
	}
	return nil
}

// listLevel 拉取指定父节点下的全部同层节点（处理分页）。
func (p *Provider) listLevel(ctx context.Context, spaceID, parentToken string) ([]*larkwiki.Node, error) {
	var all []*larkwiki.Node
	pageToken := ""
	for {
		builder := larkwiki.NewListSpaceNodeReqBuilder().SpaceId(spaceID).PageSize(defaultPageSize)
		if parentToken != "" {
			builder.ParentNodeToken(parentToken)
		}
		if pageToken != "" {
			builder.PageToken(pageToken)
		}
		resp, err := p.client.Wiki.V2.SpaceNode.List(ctx, builder.Build())
		if err != nil {
			return nil, fmt.Errorf("kb feishu: 拉取节点列表: %w", err)
		}
		if resp == nil || resp.Data == nil {
			return nil, fmt.Errorf("kb feishu: 节点列表响应为空")
		}
		if !resp.Success() {
			return nil, fmt.Errorf("kb feishu: 节点列表失败 code=%d msg=%s", resp.Code, resp.Msg)
		}
		all = append(all, resp.Data.Items...)
		if resp.Data.HasMore == nil || !*resp.Data.HasMore || resp.Data.PageToken == nil || *resp.Data.PageToken == "" {
			break
		}
		pageToken = *resp.Data.PageToken
	}
	return all, nil
}

// fetchOneDoc 拉取单个文档：失败返回 nil（不中断整体拉取）。
func (p *Provider) fetchOneDoc(ctx context.Context, n *larkwiki.Node) *cachedDoc {
	content := ""
	if contentResp, err := p.fetchContent(ctx, *n.ObjToken); err != nil {
		p.logger.Warn("kb feishu: 拉取文档内容失败", zap.String("node", *n.NodeToken), zap.Error(err))
	} else {
		content = contentResp
	}
	if content == "" {
		return nil // 内容为空视为不可用，跳过
	}

	title := ""
	if n.Title != nil {
		title = *n.Title
	}
	url := ""
	if n.Url != nil {
		url = *n.Url
	}
	return &cachedDoc{
		nodeToken: *n.NodeToken,
		title:     title,
		content:   content,
		url:       url,
		createdAt: parseLarkTime(n.ObjCreateTime),
		updatedAt: parseLarkTime(n.ObjEditTime),
	}
}

// fetchContent 获取文档纯文本。
func (p *Provider) fetchContent(ctx context.Context, documentID string) (string, error) {
	req := larkdocx.NewRawContentDocumentReqBuilder().DocumentId(documentID).Build()
	resp, err := p.client.Docx.V1.Document.RawContent(ctx, req)
	if err != nil {
		return "", err
	}
	if resp == nil || resp.Data == nil || resp.Data.Content == nil {
		return "", fmt.Errorf("文档内容为空 document_id=%s", documentID)
	}
	return *resp.Data.Content, nil
}

// parseLarkTime 解析飞书时间字段（毫秒时间戳或 RFC3339，兼容两种格式）。
func parseLarkTime(s *string) time.Time {
	if s == nil || *s == "" {
		return time.Time{}
	}
	if ms, err := strconv.ParseInt(*s, 10, 64); err == nil {
		return time.UnixMilli(ms)
	}
	if t, err := time.Parse(time.RFC3339, *s); err == nil {
		return t
	}
	return time.Time{}
}
