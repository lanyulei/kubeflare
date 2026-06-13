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
	agent.GET("/runtime/status", handler.GetRuntimeStatus)
	agent.GET("/runtime/version", handler.ListRuntimeConfigVersion)
	agent.GET("/runtime/audit", handler.ListRuntimeConfigAudit)
	agent.POST("/runtime/version/:versionID/rollback", handler.RollbackRuntimeConfigVersion)
	agent.POST("/route", handler.Route)
	agent.GET("/route-feedback", handler.ListRouteFeedback)
	agent.DELETE("/route-feedback/:feedbackID", handler.DeleteRouteFeedback)
	agent.GET("/run", handler.ListRun)
	agent.GET("/run/:runID", handler.GetRun)
	agent.POST("/run/:runID/cancel", handler.CancelRun)
	agent.GET("/run/:runID/evidence", handler.ListEvidence)
	agent.POST("/run/:runID/feedback", handler.SubmitFeedback)
	// 评估看板独立于 /run/:runID 之下,避免静态段与 :runID 通配在同层冲突
	// (gin 路由树会在注册期 panic)。
	agent.GET("/metrics/run", handler.ListRunMetricsSample)
	agent.GET("/metrics/evaluation", handler.EvaluateRuns)
	agent.GET("/case", handler.ListDiagnosisCase)
	agent.DELETE("/case/run/:runID", handler.DeleteDiagnosisCaseByRunID)
	agent.POST("/:agentType/run/stream", handler.StreamRun)
}
