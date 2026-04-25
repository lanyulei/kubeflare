package http

import (
	"github.com/gin-gonic/gin"

	"github.com/lanyulei/kubeflare/internal/shared/middleware"
)

func RegisterPublicRoutes(group *gin.RouterGroup, handler *Handler) {
	uploads := group.Group("/uploads")
	uploads.GET("/:type/:filename", handler.Get)
}

func RegisterProtectedRoutes(group *gin.RouterGroup, handler *Handler) {
	uploads := group.Group("/uploads")
	uploads.Use(middleware.RequireRolesGin("admin"))
	uploads.POST("/:type", handler.Upload)
}
