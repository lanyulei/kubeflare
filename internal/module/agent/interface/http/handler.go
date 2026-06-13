package http

import (
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/lanyulei/kubeflare/internal/module/agent/application"
	"github.com/lanyulei/kubeflare/internal/module/agent/domain"
	"github.com/lanyulei/kubeflare/internal/shared/middleware"
	"github.com/lanyulei/kubeflare/internal/shared/response"
)

type Handler struct {
	service *application.Service
}

func NewHandler(service *application.Service) *Handler {
	return &Handler{service: service}
}

func (h *Handler) ListAgent(c *gin.Context) {
	if _, err := middleware.RequireSubject(c); err != nil {
		response.Error(c, err)
		return
	}
	response.OKList(c, h.service.ListAgents(c.Request.Context()))
}

func (h *Handler) ListTool(c *gin.Context) {
	if _, err := middleware.RequireSubject(c); err != nil {
		response.Error(c, err)
		return
	}
	response.OKList(c, h.service.ListTools(c.Request.Context()))
}

func (h *Handler) ListSkill(c *gin.Context) {
	if _, err := middleware.RequireSubject(c); err != nil {
		response.Error(c, err)
		return
	}
	response.OKList(c, h.service.ListSkills(c.Request.Context()))
}

func (h *Handler) GetRuntimeStatus(c *gin.Context) {
	userID, err := middleware.RequireSubject(c)
	if err != nil {
		response.Error(c, err)
		return
	}

	status, err := h.service.GetRuntimeStatus(c.Request.Context(), userID, clusterIDFromRequest(c, ""))
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, http.StatusOK, status)
}

// Reload 在运行时热重载工具覆盖与技能。空请求体表示回滚到启动配置,因此容忍
// EOF(空 body);仅在 body 非空但非法时报错。
func (h *Handler) Reload(c *gin.Context) {
	userID, err := middleware.RequireSubject(c)
	if err != nil {
		response.Error(c, err)
		return
	}

	var req application.ReloadToolsRequest
	if err := c.ShouldBindJSON(&req); err != nil && !errors.Is(err, io.EOF) {
		response.Error(c, err)
		return
	}

	result, err := h.service.ReloadTools(c.Request.Context(), userID, req)
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, http.StatusOK, result)
}

func (h *Handler) ListRuntimeConfigVersion(c *gin.Context) {
	userID, err := middleware.RequireSubject(c)
	if err != nil {
		response.Error(c, err)
		return
	}

	items, err := h.service.ListRuntimeConfigVersions(c.Request.Context(), userID, runtimeConfigLimit(c))
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OKList(c, items)
}

func (h *Handler) ListRuntimeConfigAudit(c *gin.Context) {
	userID, err := middleware.RequireSubject(c)
	if err != nil {
		response.Error(c, err)
		return
	}

	items, err := h.service.ListRuntimeConfigAudits(c.Request.Context(), userID, c.Query("version_id"), runtimeConfigLimit(c))
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OKList(c, items)
}

func (h *Handler) RollbackRuntimeConfigVersion(c *gin.Context) {
	userID, err := middleware.RequireSubject(c)
	if err != nil {
		response.Error(c, err)
		return
	}

	var req application.RollbackRuntimeConfigRequest
	if err := c.ShouldBindJSON(&req); err != nil && !errors.Is(err, io.EOF) {
		response.Error(c, err)
		return
	}

	result, err := h.service.RollbackRuntimeConfigVersion(c.Request.Context(), userID, c.Param("versionID"), req)
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, http.StatusOK, result)
}

func (h *Handler) Route(c *gin.Context) {
	userID, err := middleware.RequireSubject(c)
	if err != nil {
		response.Error(c, err)
		return
	}

	var req application.RouteAgentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, err)
		return
	}
	req.ClusterID = clusterIDFromRequest(c, req.ClusterID)

	result, err := h.service.Route(c.Request.Context(), userID, req)
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, http.StatusOK, result)
}

func (h *Handler) StreamRun(c *gin.Context) {
	userID, err := middleware.RequireSubject(c)
	if err != nil {
		response.Error(c, err)
		return
	}

	var req application.RunAgentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, err)
		return
	}
	req.ClusterID = clusterIDFromRequest(c, req.ClusterID)

	events, err := h.service.StreamRun(c.Request.Context(), userID, c.Param("agentType"), req)
	if err != nil {
		response.Error(c, err)
		return
	}

	response.StreamSSE(c, events, func(event domain.AgentRunEvent) string {
		return event.Event
	})
}

func (h *Handler) CancelRun(c *gin.Context) {
	userID, err := middleware.RequireSubject(c)
	if err != nil {
		response.Error(c, err)
		return
	}

	run, err := h.service.CancelRunForAdmin(c.Request.Context(), userID, c.Param("runID"))
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, http.StatusOK, run)
}

func (h *Handler) ListRun(c *gin.Context) {
	userID, err := middleware.RequireSubject(c)
	if err != nil {
		response.Error(c, err)
		return
	}

	result, err := h.service.ListRuns(c.Request.Context(), userID, application.AgentRunListRequest{
		Keyword:   c.Query("keyword"),
		AgentType: c.Query("agent_type"),
		ClusterID: c.Query("cluster_id"),
		Status:    c.Query("status"),
		UserID:    c.Query("user_id"),
		Days:      queryInt(c, "days"),
		Limit:     queryInt(c, "limit"),
		Offset:    queryInt(c, "offset"),
	})
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, http.StatusOK, result)
}

func (h *Handler) GetRun(c *gin.Context) {
	userID, err := middleware.RequireSubject(c)
	if err != nil {
		response.Error(c, err)
		return
	}

	detail, err := h.service.GetRunDetail(c.Request.Context(), userID, c.Param("runID"))
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, http.StatusOK, detail)
}

func (h *Handler) ListEvidence(c *gin.Context) {
	userID, err := middleware.RequireSubject(c)
	if err != nil {
		response.Error(c, err)
		return
	}

	items, err := h.service.ListEvidence(c.Request.Context(), userID, c.Param("runID"))
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OKList(c, items)
}

// SubmitFeedback 记录用户对一次诊断结论的质量反馈(有用/没用 + 可选备注),
// 每个 run 只允许提交一次。用于把度量闭环延伸到"准不准"。
func (h *Handler) SubmitFeedback(c *gin.Context) {
	userID, err := middleware.RequireSubject(c)
	if err != nil {
		response.Error(c, err)
		return
	}

	var req application.SubmitRunFeedbackRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, err)
		return
	}

	feedback, err := h.service.SubmitRunFeedback(c.Request.Context(), userID, c.Param("runID"), req)
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, http.StatusOK, feedback)
}

func (h *Handler) ListRunMetricsSample(c *gin.Context) {
	userID, err := middleware.RequireSubject(c)
	if err != nil {
		response.Error(c, err)
		return
	}

	result, err := h.service.ListRunMetricsSamples(c.Request.Context(), userID, application.AgentRunMetricsSampleRequest{
		Days:      queryInt(c, "days"),
		Feature:   c.Query("feature"),
		Enabled:   queryBoolPtr(c, "enabled"),
		AgentType: c.Query("agent_type"),
		ClusterID: c.Query("cluster_id"),
		Limit:     queryInt(c, "limit"),
		Offset:    queryInt(c, "offset"),
	})
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, http.StatusOK, result)
}

// EvaluateRuns 返回 run 度量的离线评估看板(metrics × feedback 按特性 on/off
// 对照),供管理员评估各智能特性的真实增益。窗口天数由 days query 指定(缺省/
// 越界由服务层钳制)。
func (h *Handler) EvaluateRuns(c *gin.Context) {
	userID, err := middleware.RequireSubject(c)
	if err != nil {
		response.Error(c, err)
		return
	}

	evaluation, err := h.service.EvaluateRuns(c.Request.Context(), userID, application.RunMetricsEvaluationRequest{
		Days:      queryInt(c, "days"),
		AgentType: c.Query("agent_type"),
		ClusterID: c.Query("cluster_id"),
	})
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, http.StatusOK, evaluation)
}

func (h *Handler) ListDiagnosisCase(c *gin.Context) {
	userID, err := middleware.RequireSubject(c)
	if err != nil {
		response.Error(c, err)
		return
	}

	result, err := h.service.ListDiagnosisCases(c.Request.Context(), userID, application.AgentDiagnosisCaseListRequest{
		Keyword:   c.Query("keyword"),
		AgentType: c.Query("agent_type"),
		ClusterID: c.Query("cluster_id"),
		Limit:     queryInt(c, "limit"),
		Offset:    queryInt(c, "offset"),
	})
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, http.StatusOK, result)
}

func (h *Handler) DeleteDiagnosisCaseByRunID(c *gin.Context) {
	userID, err := middleware.RequireSubject(c)
	if err != nil {
		response.Error(c, err)
		return
	}

	result, err := h.service.DeleteDiagnosisCaseByRunID(c.Request.Context(), userID, c.Param("runID"))
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, http.StatusOK, result)
}

func (h *Handler) ListRouteFeedback(c *gin.Context) {
	userID, err := middleware.RequireSubject(c)
	if err != nil {
		response.Error(c, err)
		return
	}

	result, err := h.service.ListRouteFeedback(c.Request.Context(), userID, application.AgentRouteFeedbackListRequest{
		Keyword:           c.Query("keyword"),
		SelectedAgentType: c.Query("selected_agent_type"),
		Matched:           queryBoolPtr(c, "matched"),
		Limit:             queryInt(c, "limit"),
		Offset:            queryInt(c, "offset"),
	})
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, http.StatusOK, result)
}

func (h *Handler) DeleteRouteFeedback(c *gin.Context) {
	userID, err := middleware.RequireSubject(c)
	if err != nil {
		response.Error(c, err)
		return
	}

	result, err := h.service.DeleteRouteFeedback(c.Request.Context(), userID, c.Param("feedbackID"))
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, http.StatusOK, result)
}

func clusterIDFromRequest(c *gin.Context, value string) string {
	value = strings.TrimSpace(value)
	if value != "" {
		return value
	}
	return strings.TrimSpace(c.GetHeader("X-Cluster-ID"))
}

func runtimeConfigLimit(c *gin.Context) int {
	return queryInt(c, "limit")
}

func queryInt(c *gin.Context, key string) int {
	value, _ := strconv.Atoi(strings.TrimSpace(c.Query(key)))
	return value
}

func queryBoolPtr(c *gin.Context, key string) *bool {
	raw := strings.TrimSpace(c.Query(key))
	if raw == "" {
		return nil
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return nil
	}
	return &value
}
