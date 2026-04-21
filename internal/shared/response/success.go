package response

import "github.com/gin-gonic/gin"

type Envelope struct {
	Code      string `json:"code"`
	Message   string `json:"message,omitempty"`
	Data      any    `json:"data,omitempty"`
	RequestID string `json:"request_id,omitempty"`
}

func OK(c *gin.Context, status int, data any) {
	c.JSON(status, Envelope{
		Code:      "OK",
		Data:      data,
		RequestID: c.GetString("request_id"),
	})
}
