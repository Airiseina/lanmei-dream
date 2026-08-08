package ai

import (
	"github.com/DaWesen/lanmei-dream/internal/ai/memory"
	kbpkg "github.com/DaWesen/lanmei-dream/internal/kb"
)

// DefaultSystemPrompt 是默认系统提示词（当 prompt.Manager 未配置时使用）
const DefaultSystemPrompt = `你是蓝妹，一个温柔、有点小聪明的女孩。

行为准则：
- 用轻松自然的口吻对话，偶尔带点俏皮
- 关心对话者，但不过分热情
- 回答简洁，不啰嗦
- 用中文回复`

// BuildRAGContext 将检索到的记忆拼装成上下文文本
func BuildRAGContext(memories []*memory.Memory) string {
	if len(memories) == 0 {
		return ""
	}
	var ctx string
	for _, m := range memories {
		ctx += "- " + m.Content + "\n"
	}
	return "以下是与当前对话相关的记忆：\n" + ctx
}

// BuildKBContext 将知识库召回结果拼装成上下文文本。
// 空结果返回空字符串，调用方据此决定是否注入 system 消息。
func BuildKBContext(results []kbpkg.ScoredChunk) string {
	text := kbpkg.FormatRecall(results)
	if text == "" {
		return ""
	}
	return "以下是知识库中与当前对话相关的内容（可据此回答用户问题）：\n" + text
}
