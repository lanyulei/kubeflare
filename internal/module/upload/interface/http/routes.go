package http

import (
	"github.com/gin-gonic/gin"

	"github.com/lanyulei/kubeflare/internal/shared/middleware"
)

func RegisterPublicRoutes(group *gin.RouterGroup, handler *Handler) {
	upload := group.Group("/upload")
	upload.GET("/:type/:filename", handler.Get)
}

func RegisterProtectedRoutes(group *gin.RouterGroup, handler *Handler) {
	upload := group.Group("/upload")
	upload.Use(middleware.RequireRolesGin("admin"))
	upload.POST("/:type", handler.Upload)
}
