package http

import (
	"github.com/gin-gonic/gin"

	"github.com/lanyulei/kubeflare/internal/shared/middleware"
)

func RegisterRoutes(group *gin.RouterGroup, handler *Handler) {
	agent := group.Group("/agent")
	// Agent 工具通过集群存储的(管理员级)kubeconfig 读取集群资源,等同于
	// 集群代理路径的能力,因此与集群管理路由一致要求 admin 角色,避免普通
	// 登录用户借 Agent 越权读取任意集群/任意命名空间。
	agent.Use(middleware.RequireRolesGin("admin"))
	agent.GET("", handler.ListAgent)
	agent.GET("/tool", handler.ListTool)
	agent.POST("/tool/reload", handler.Reload)
	agent.GET("/skill", handler.ListSkill)
	agent.GET("/runtime/version", handler.ListRuntimeConfigVersion)
	agent.GET("/runtime/audit", handler.ListRuntimeConfigAudit)
	agent.POST("/runtime/version/:versionID/rollback", handler.RollbackRuntimeConfigVersion)
	agent.POST("/route", handler.Route)
	agent.POST("/:agentType/run/stream", handler.StreamRun)
	agent.POST("/run/:runID/cancel", handler.CancelRun)
	agent.GET("/run/:runID/evidence", handler.ListEvidence)
}
