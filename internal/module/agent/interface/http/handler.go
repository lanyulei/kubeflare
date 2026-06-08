package http

import (
	"net/http"
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
	if _, err := currentUserID(c); err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, http.StatusOK, gin.H{"items": h.service.ListAgents(c.Request.Context())})
}

func (h *Handler) ListTool(c *gin.Context) {
	if _, err := currentUserID(c); err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, http.StatusOK, gin.H{"items": h.service.ListTools(c.Request.Context())})
}

func (h *Handler) Route(c *gin.Context) {
	userID, err := currentUserID(c)
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
	userID, err := currentUserID(c)
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
	userID, err := currentUserID(c)
	if err != nil {
		response.Error(c, err)
		return
	}

	run, err := h.service.CancelRun(c.Request.Context(), userID, c.Param("runID"))
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, http.StatusOK, run)
}

func (h *Handler) ListEvidence(c *gin.Context) {
	userID, err := currentUserID(c)
	if err != nil {
		response.Error(c, err)
		return
	}

	items, err := h.service.ListEvidence(c.Request.Context(), userID, c.Param("runID"))
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, http.StatusOK, gin.H{"items": items})
}

func currentUserID(c *gin.Context) (string, error) {
	return middleware.RequireSubject(c)
}

func clusterIDFromRequest(c *gin.Context, value string) string {
	value = strings.TrimSpace(value)
	if value != "" {
		return value
	}
	return strings.TrimSpace(c.GetHeader("X-Cluster-ID"))
}
