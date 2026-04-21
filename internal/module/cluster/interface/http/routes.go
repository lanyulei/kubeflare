package http

import (
	"github.com/gin-gonic/gin"

	"github.com/lanyulei/kubeflare/internal/shared/middleware"
)

func RegisterRoutes(group *gin.RouterGroup, handler *Handler) {
	clusters := group.Group("/clusters")
	clusters.Use(middleware.RequireRolesGin("admin"))
	clusters.GET("", handler.List)
	clusters.POST("", handler.Create)
	clusters.GET("/:clusterID", handler.Get)
	clusters.PUT("/:clusterID", handler.Update)
	clusters.DELETE("/:clusterID", handler.Delete)
}
