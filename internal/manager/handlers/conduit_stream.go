package handlers

import (
	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/sse"
)

// TraceStream 实时推送执行链路 Trace（SSE）。
// 使用 Fiber 官方 SSE middleware：自动设置流式响应头、逐条 Flush、
// 心跳保活（默认 15s 注释行）与断连检测。
// 该路由位于受保护分组（Bearer + CSRF），前端需用 fetch + ReadableStream
// 携带 Authorization / X-CSRF-Token 头读取，断开即取消订阅。
func (h *Handler) TraceStream(c fiber.Ctx) error {
	ch := h.traceCol.Subscribe()
	defer h.traceCol.Unsubscribe(ch)

	return sse.New(sse.Config{
		Handler: func(_ fiber.Ctx, stream *sse.Stream) error {
			for {
				select {
				case <-stream.Context().Done():
					// 客户端断开或流写入失败
					return nil
				case rec := <-ch:
					if err := stream.Event(sse.Event{Name: "trace", Data: rec}); err != nil {
						return err
					}
				}
			}
		},
	})(c)
}
