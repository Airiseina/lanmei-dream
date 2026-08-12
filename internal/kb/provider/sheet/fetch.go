package sheet

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	larksheets "github.com/larksuite/oapi-sdk-go/v3/service/sheets/v3"
	"go.uber.org/zap"
)

// valuesAPIBase 飞书电子表格 v2 values 读取接口前缀。
const valuesAPIBase = "https://open.feishu.cn/open-apis/sheets/v2/spreadsheets/"

// ensureRows 返回 KV 行缓存，必要时从飞书重新拉取。
//
// 缓存逻辑：
//   - 缓存有效期内直接返回；
//   - 拉取失败但已有缓存时降级使用旧数据（记录告警），保证召回不被单次网络故障击穿；
//   - 拉取失败且无缓存时返回错误，由上层按空结果处理。
func (p *Provider) ensureRows(ctx context.Context) error {
	p.mu.Lock()
	fresh := p.loaded && time.Since(p.fetchedAt) < p.cacheTTL
	p.mu.Unlock()
	if fresh {
		return nil
	}

	cctx, cancel := context.WithTimeout(ctx, p.fetchTimeout)
	defer cancel()

	rows, err := p.fetchRows(cctx)
	if err != nil {
		p.mu.Lock()
		haveCache := p.loaded
		p.mu.Unlock()
		if haveCache {
			p.logger.Warn("kb sheet: 表格拉取失败，降级使用缓存", zap.Error(err))
			return nil
		}
		return err
	}

	p.mu.Lock()
	p.rows = rows
	p.loaded = true
	p.fetchedAt = time.Now()
	// 内容更新后失效全部向量缓存，避免脏向量
	p.embeddings = make(map[string][]float32)
	p.mu.Unlock()
	p.logger.Info("kb sheet: KV 记录已缓存",
		zap.String("kb", p.kb.ID), zap.Int("rows", len(rows)))
	return nil
}

// snapshotRows 返回当前缓存的行快照（nil 安全）。
func (p *Provider) snapshotRows() []*kvRow {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.rows) == 0 {
		return nil
	}
	out := make([]*kvRow, len(p.rows))
	copy(out, p.rows)
	return out
}

// fetchRows 从飞书拉取 KV 行数据：
//  1. 解析工作表 ID（配置 sheet_id 优先，否则按 sheet_name 从工作表列表匹配）；
//  2. 调用 values 接口读取「索引列:知识列」整列非空范围；
//  3. 跳过表头行，过滤空行，截断到 maxRows。
func (p *Provider) fetchRows(ctx context.Context) ([]*kvRow, error) {
	sheetID, err := p.resolveSheetID(ctx)
	if err != nil {
		return nil, err
	}

	values, err := p.fetchValues(ctx, sheetID)
	if err != nil {
		return nil, err
	}

	return parseValues(values, p.skipHeaderRows, p.maxRows), nil
}

// parseValues 将 values 接口返回的单元格二维数组解析为 KV 行：
// 跳过表头行，过滤空行，截断到 maxRows 上限（返回 nil 表示无有效记录）。
func parseValues(values [][]any, skipHeaderRows, maxRows int) []*kvRow {
	if maxRows <= 0 {
		maxRows = defaultMaxRows
	}
	rows := make([]*kvRow, 0, len(values))
	skip := max(skipHeaderRows, 0)
	for i, row := range values {
		if skip > 0 {
			skip--
			continue
		}
		if len(row) == 0 {
			continue
		}
		index := cellString(row, 0)
		content := cellString(row, 1)
		if index == "" && content == "" {
			continue
		}
		rows = append(rows, &kvRow{id: fmt.Sprintf("%d", i+1), index: index, content: content})
		if len(rows) >= maxRows {
			break
		}
	}
	if len(rows) == 0 {
		return nil
	}
	return rows
}

// resolveSheetID 解析目标工作表 ID。
// 配置了 sheet_id 直接使用；否则用 sheet_name 匹配工作表列表，
// 匹配失败时回退到第一个非隐藏工作表（并告警提示）。
func (p *Provider) resolveSheetID(ctx context.Context) (string, error) {
	if p.sheetID != "" {
		return p.sheetID, nil
	}
	req := larksheets.NewQuerySpreadsheetSheetReqBuilder().SpreadsheetToken(p.spreadsheetToken).Build()
	resp, err := p.client.Sheets.V3.SpreadsheetSheet.Query(ctx, req)
	if err != nil {
		return "", fmt.Errorf("kb sheet: 查询工作表列表: %w", err)
	}
	if resp == nil || resp.Data == nil || len(resp.Data.Sheets) == 0 {
		return "", fmt.Errorf("kb sheet: 表格无工作表（spreadsheet_token=%s）", p.spreadsheetToken)
	}
	for _, s := range resp.Data.Sheets {
		if s == nil || s.Title == nil || s.SheetId == nil {
			continue
		}
		if *s.Title == p.sheetName {
			return *s.SheetId, nil
		}
	}
	// 名字不匹配：回退第一个非隐藏工作表
	for _, s := range resp.Data.Sheets {
		if s == nil || s.SheetId == nil {
			continue
		}
		if s.Hidden == nil || !*s.Hidden {
			p.logger.Warn("kb sheet: 未找到工作表「"+p.sheetName+"」，回退到第一个非隐藏工作表",
				zap.String("kb", p.kb.ID))
			return *s.SheetId, nil
		}
	}
	return "", fmt.Errorf("kb sheet: 未找到工作表「%s」且无可回退的非隐藏工作表", p.sheetName)
}

// valueRangeResp values 接口响应体（仅解析所需字段）。
type valueRangeResp struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data struct {
		ValueRange struct {
			Values [][]any `json:"values"`
		} `json:"valueRange"`
	} `json:"data"`
}

// fetchValues 调用 v2 values 接口读取「索引列:知识列」整列范围。
// range 形如 "<sheetID>!A:B"，读取该区域内所有非空单元格。
func (p *Provider) fetchValues(ctx context.Context, sheetID string) ([][]any, error) {
	token, err := p.token(ctx)
	if err != nil {
		return nil, err
	}
	// range 形如 "<sheetID>!A:B"：! 与 : 是 API 路径的固定分隔符，不可转义
	rng := sheetID + "!" + p.indexColumn + ":" + p.contentColumn
	apiURL := valuesAPIBase + url.PathEscape(p.spreadsheetToken) + "/values/" + rng

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, fmt.Errorf("kb sheet: 构造读取请求: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+token)

	httpResp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("kb sheet: 读取表格数据: %w", err)
	}
	defer httpResp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(httpResp.Body, 8<<20)) // 上限 8MB，防异常响应撑爆内存
	if err != nil {
		return nil, fmt.Errorf("kb sheet: 读取响应体: %w", err)
	}
	if httpResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("kb sheet: 读取表格数据 HTTP %d: %s", httpResp.StatusCode, truncate(string(body), 200))
	}
	var resp valueRangeResp
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("kb sheet: 解析响应: %w", err)
	}
	if resp.Code != 0 {
		return nil, fmt.Errorf("kb sheet: 读取表格数据失败 code=%d msg=%s", resp.Code, resp.Msg)
	}
	return resp.Data.ValueRange.Values, nil
}

// cellString 将单元格值规范化为字符串（字符串/数字/布尔统一文本化）。
func cellString(row []any, idx int) string {
	if idx < 0 || idx >= len(row) || row[idx] == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprintf("%v", row[idx]))
}

// truncate 截断字节串到 n 字节用于错误日志（按字节截断，仅展示用）。
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
