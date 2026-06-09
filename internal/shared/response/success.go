package response

import (
	"net/http"

	"github.com/gin-gonic/gin"

	sharedErrors "github.com/lanyulei/kubeflare/internal/shared/errors"
)

type Envelope struct {
	Code      int    `json:"code"`
	Message   string `json:"message,omitempty"`
	Data      any    `json:"data,omitempty"`
	RequestID string `json:"request_id,omitempty"`
}

func OK(c *gin.Context, status int, data any) {
	c.JSON(status, Envelope{
		Code:      sharedErrors.CodeSuccess,
		Message:   "成功",
		Data:      data,
		RequestID: c.GetString("request_id"),
	})
}

// OKList 返回 200 + {"items": items} 的列表响应。统一各列表接口的包裹形态,
// 避免在多个 handler 重复 gin.H{"items": ...} 字面量。
func OKList(c *gin.Context, items any) {
	OK(c, http.StatusOK, gin.H{"items": items})
}
