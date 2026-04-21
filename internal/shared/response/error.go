package response

import (
	"github.com/gin-gonic/gin"

	sharedErrors "github.com/lanyulei/kubeflare/internal/shared/errors"
)

func Error(c *gin.Context, err error) {
	appErr := sharedErrors.From(err)
	c.AbortWithStatusJSON(appErr.Status, Envelope{
		Code:      appErr.Code,
		Message:   appErr.Message,
		RequestID: c.GetString("request_id"),
	})
}
