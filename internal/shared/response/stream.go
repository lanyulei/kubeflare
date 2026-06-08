package response

import (
	"github.com/gin-gonic/gin"
)

// StreamSSE 以 Server-Sent Events 形式把 events 通道里的事件逐条推给客户端。
//
// nameFn 从事件中提取 SSE 事件名,返回空字符串的事件会被跳过。
// 当客户端断开(请求 context 被取消)时,循环会尽快退出;调用方负责在
// 上游用同一 context 终止事件生产者并完成持久化收尾。
func StreamSSE[T any](c *gin.Context, events <-chan T, nameFn func(T) string) {
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache, no-transform")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	c.Writer.WriteHeaderNow()
	c.Writer.Flush()

	ctx := c.Request.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-events:
			if !ok {
				return
			}
			name := nameFn(event)
			if name == "" {
				continue
			}
			c.SSEvent(name, event)
			c.Writer.Flush()
		}
	}
}
