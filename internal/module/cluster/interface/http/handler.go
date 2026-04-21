package http

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/lanyulei/kubeflare/internal/module/cluster/application"
	"github.com/lanyulei/kubeflare/internal/shared/response"
)

type Handler struct {
	service *application.Service
}

func NewHandler(service *application.Service) *Handler {
	return &Handler{service: service}
}

func (h *Handler) List(c *gin.Context) {
	clusters, err := h.service.List(c.Request.Context())
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, http.StatusOK, gin.H{"items": clusters})
}

func (h *Handler) Get(c *gin.Context) {
	cluster, err := h.service.Get(c.Request.Context(), c.Param("clusterID"))
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, http.StatusOK, cluster)
}

func (h *Handler) Create(c *gin.Context) {
	var req application.CreateClusterRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, err)
		return
	}

	cluster, err := h.service.Create(c.Request.Context(), req)
	if err != nil {
		response.Error(c, err)
		return
	}

	response.OK(c, http.StatusCreated, cluster)
}

func (h *Handler) Update(c *gin.Context) {
	var req application.UpdateClusterRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, err)
		return
	}

	cluster, err := h.service.Update(c.Request.Context(), c.Param("clusterID"), req)
	if err != nil {
		response.Error(c, err)
		return
	}

	response.OK(c, http.StatusOK, cluster)
}

func (h *Handler) Delete(c *gin.Context) {
	if err := h.service.Delete(c.Request.Context(), c.Param("clusterID")); err != nil {
		response.Error(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}
