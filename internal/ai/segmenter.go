package ai

import "strings"

// segmentDelimiter 是流式回复的段落边界（双换行，即一个空行）。
const segmentDelimiter = "\n\n"

// codeFence markdown 代码块围栏（三个反引号）。
// LLM 输出代码时必须用 ``` 包裹（见 system prompt 规则），
// 分割器据此识别代码块：块内的 \n\n 是代码内容，不作为段落边界。
const codeFence = "```"

// inCodeBlock 判断文本中代码围栏是否处于打开状态。
// 出现奇数个 ``` 视为已进入代码块（开围栏未闭合），偶数个视为在代码块外。
func inCodeBlock(s string) bool {
	return strings.Count(s, codeFence)%2 == 1
}

// StreamSegmenter 流式段落分割器。
//
// 持续接收 LLM 流式产出的文本 chunk，在缓冲区中检测段落边界（\n\n），
// 每当边界出现时产出完整段落。最后一段保留在缓冲区中等待后续 chunk
// 或 Flush 调用。
//
// 设计要点：
//   - 跨 chunk 边界安全：\n\n 的两个换行可能分散在相邻 chunk 中，
//     分割后保留最后一段在缓冲区可自然处理此情况。
//   - 代码块感知：markdown 代码块（``` 包裹）内的 \n\n 是代码内容，
//     不作为段落边界；整块代码在开围栏后保持完整，直到结尾 ``` 闭合。
//     未闭合的代码块（LLM 漏写结尾）整体留在缓冲区，Flush 时整块输出。
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
//  2. 自前向后扫描 buffer 中的 \n\n 边界：
//     - 边界之前 ``` 出现次数为奇数（代码块内）→ 该 \n\n 保留为代码内容，不分割
//     - 边界之前 ``` 出现次数为偶数（代码块外）→ 切出完整段落
//  3. 剩余文本保留在 buffer（可能后续还有边界）
//
// 返回的段落已经过去除首尾空白处理，空段落被过滤。
func (s *StreamSegmenter) Feed(chunk string) []string {
	if chunk == "" {
		return nil
	}

	s.buffer.WriteString(chunk)
	s.fullText.WriteString(chunk)

	buf := s.buffer.String()
	var segments []string
	for scan := 0; ; {
		idx := strings.Index(buf[scan:], segmentDelimiter)
		if idx < 0 {
			break
		}
		abs := scan + idx
		if inCodeBlock(buf[:abs]) {
			// 代码块内：该 \n\n 是代码内容，不作为段落边界，继续向后找
			scan = abs + len(segmentDelimiter)
			continue
		}
		// 代码块外：切出完整段落（保留代码块内部的 \n\n）
		seg := strings.TrimSpace(buf[:abs])
		if seg != "" {
			segments = append(segments, seg)
		}
		buf = buf[abs+len(segmentDelimiter):]
		scan = 0
	}
	s.buffer.Reset()
	s.buffer.WriteString(buf)

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
