package ai

import "strings"

// segmentDelimiter 是流式回复的段落边界（双换行，即一个空行）。
// 与 splitResponse 共享同一边界语义。
const segmentDelimiter = "\n\n"

// StreamSegmenter 流式段落分割器。
//
// 持续接收 LLM 流式产出的文本 chunk，在缓冲区中检测段落边界（\n\n），
// 每当边界出现时产出完整段落。最后一段保留在缓冲区中等待后续 chunk
// 或 Flush 调用。
//
// 设计要点：
//   - 跨 chunk 边界安全：\n\n 的两个换行可能分散在相邻 chunk 中，
//     分割后保留最后一段在缓冲区可自然处理此情况。
//   - 全文追踪：fullText 记录所有接收到的文本原文，供调用方存储对话记录。
//   - 非线程安全：设计为单 goroutine 内使用（流式消费 goroutine）。
type StreamSegmenter struct {
	buffer   strings.Builder // 未决文本（可能后续还有 \n\n 边界）
	fullText strings.Builder // 累计全文（供存储）
}

// NewStreamSegmenter 创建流式段落分割器。
func NewStreamSegmenter() *StreamSegmenter {
	return &StreamSegmenter{}
}

// Feed 处理一个文本 chunk，返回本次产出的完整段落列表。
//
// 算法：
//  1. 将 chunk 追加到 buffer 和 fullText
//  2. 按 \n\n 分割 buffer
//  3. 除最后一段外的所有段都是完整段落（边界已闭合），返回它们
//  4. 最后一段保留在 buffer（可能后续还有 \n\n）
//
// 返回的段落已经过去除首尾空白处理，空段落被过滤。
func (s *StreamSegmenter) Feed(chunk string) []string {
	if chunk == "" {
		return nil
	}

	s.buffer.WriteString(chunk)
	s.fullText.WriteString(chunk)

	// 按段落边界分割
	parts := strings.Split(s.buffer.String(), segmentDelimiter)
	if len(parts) <= 1 {
		// 无边界，全部保留在 buffer
		return nil
	}

	// 除最后一段外都是完整段落
	complete := parts[:len(parts)-1]
	// 最后一段保留在 buffer（可能后续还有边界）
	s.buffer.Reset()
	s.buffer.WriteString(parts[len(parts)-1])

	// 过滤空段落并去除首尾空白
	segments := make([]string, 0, len(complete))
	for _, p := range complete {
		trimmed := strings.TrimSpace(p)
		if trimmed != "" {
			segments = append(segments, trimmed)
		}
	}

	return segments
}

// Flush 返回缓冲区中剩余的文本作为末段。
// 在流结束后调用，确保最后一段不被遗漏。
// 返回空字符串表示缓冲区为空或仅含空白。
func (s *StreamSegmenter) Flush() string {
	seg := strings.TrimSpace(s.buffer.String())
	s.buffer.Reset()
	return seg
}

// FullText 返回累计接收的全部文本原文。
// 用于在流结束后获取完整回复内容，存储到对话记录。
func (s *StreamSegmenter) FullText() string {
	return s.fullText.String()
}
