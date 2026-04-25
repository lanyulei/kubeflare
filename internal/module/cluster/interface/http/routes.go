package http

import (
	"github.com/gin-gonic/gin"

	"github.com/lanyulei/kubeflare/internal/shared/middleware"
)

func RegisterRoutes(group *gin.RouterGroup, handler *Handler) {
	cluster := group.Group("/cluster")
	cluster.Use(middleware.RequireRolesGin("admin"))
	cluster.GET("", handler.List)
	cluster.POST("", handler.Create)
	cluster.GET("/:clusterID", handler.Get)
	cluster.PUT("/:clusterID", handler.Update)
	cluster.DELETE("/:clusterID", handler.Delete)
}
