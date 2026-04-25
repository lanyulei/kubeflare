package http

import (
	"github.com/gin-gonic/gin"
)

func RegisterPublicRoutes(group *gin.RouterGroup, handler *Handler) {
	upload := group.Group("/upload")
	upload.GET("/:type/:filename", handler.Get)
}

func RegisterProtectedRoutes(group *gin.RouterGroup, handler *Handler) {
	upload := group.Group("/upload")
	upload.POST("/:type", handler.Upload)
}
