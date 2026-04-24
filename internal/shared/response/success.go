package response

import (
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
