package http

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/lanyulei/kubeflare/internal/module/gitops/application"
	"github.com/lanyulei/kubeflare/internal/shared/middleware"
	"github.com/lanyulei/kubeflare/internal/shared/response"
)

type Handler struct {
	service *application.Service
}

func NewHandler(service *application.Service) *Handler {
	return &Handler{service: service}
}

func (h *Handler) Dashboard(c *gin.Context) {
	stats, err := h.service.Dashboard(c.Request.Context())
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, http.StatusOK, stats)
}

func (h *Handler) ListProvider(c *gin.Context) {
	var query application.ListQuery
	if err := c.ShouldBindQuery(&query); err != nil {
		response.Error(c, err)
		return
	}
	items, err := h.service.ListProviders(c.Request.Context(), query)
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OKList(c, items)
}

func (h *Handler) GetProvider(c *gin.Context) {
	item, err := h.service.GetProvider(c.Request.Context(), c.Param("providerID"))
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, http.StatusOK, item)
}

func (h *Handler) CreateProvider(c *gin.Context) {
	var req application.CreateProviderRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, err)
		return
	}
	item, err := h.service.CreateProvider(c.Request.Context(), req, operatorID(c))
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, http.StatusCreated, item)
}

func (h *Handler) UpdateProvider(c *gin.Context) {
	var req application.UpdateProviderRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, err)
		return
	}
	item, err := h.service.UpdateProvider(c.Request.Context(), c.Param("providerID"), req, operatorID(c))
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, http.StatusOK, item)
}

func (h *Handler) DeleteProvider(c *gin.Context) {
	if err := h.service.DeleteProvider(c.Request.Context(), c.Param("providerID"), operatorID(c)); err != nil {
		response.Error(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *Handler) TestProvider(c *gin.Context) {
	result, err := h.service.TestProvider(c.Request.Context(), c.Param("providerID"), operatorID(c))
	if err != nil && result.ProviderID != "" && !result.Reachable {
		response.OK(c, http.StatusOK, result)
		return
	}
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, http.StatusOK, result)
}

func (h *Handler) ListRepository(c *gin.Context) {
	var query application.ListQuery
	if err := c.ShouldBindQuery(&query); err != nil {
		response.Error(c, err)
		return
	}
	items, err := h.service.ListGitRepositories(c.Request.Context(), query)
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OKList(c, items)
}

func (h *Handler) CreateRepository(c *gin.Context) {
	var req application.CreateRepositoryRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, err)
		return
	}
	item, err := h.service.CreateGitRepository(c.Request.Context(), req, operatorID(c))
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, http.StatusCreated, item)
}

func (h *Handler) UpdateRepository(c *gin.Context) {
	var req application.UpdateRepositoryRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, err)
		return
	}
	item, err := h.service.UpdateGitRepository(c.Request.Context(), c.Param("repositoryID"), req, operatorID(c))
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, http.StatusOK, item)
}

func (h *Handler) DeleteRepository(c *gin.Context) {
	if err := h.service.DeleteGitRepository(c.Request.Context(), c.Param("repositoryID"), operatorID(c)); err != nil {
		response.Error(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *Handler) ListApplication(c *gin.Context) {
	var query application.ListQuery
	if err := c.ShouldBindQuery(&query); err != nil {
		response.Error(c, err)
		return
	}
	items, err := h.service.ListApplications(c.Request.Context(), query)
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OKList(c, items)
}

func (h *Handler) GetApplication(c *gin.Context) {
	item, err := h.service.GetApplication(c.Request.Context(), c.Param("applicationID"))
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, http.StatusOK, item)
}

func (h *Handler) CreateApplication(c *gin.Context) {
	var req application.CreateApplicationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, err)
		return
	}
	item, err := h.service.CreateApplication(c.Request.Context(), req, operatorID(c))
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, http.StatusCreated, item)
}

func (h *Handler) UpdateApplication(c *gin.Context) {
	var req application.UpdateApplicationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, err)
		return
	}
	item, err := h.service.UpdateApplication(c.Request.Context(), c.Param("applicationID"), req, operatorID(c))
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, http.StatusOK, item)
}

func (h *Handler) DeleteApplication(c *gin.Context) {
	if err := h.service.DeleteApplication(c.Request.Context(), c.Param("applicationID"), operatorID(c)); err != nil {
		response.Error(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *Handler) ListEnvironment(c *gin.Context) {
	var query application.ListQuery
	if err := c.ShouldBindQuery(&query); err != nil {
		response.Error(c, err)
		return
	}
	items, err := h.service.ListEnvironments(c.Request.Context(), query)
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OKList(c, items)
}

func (h *Handler) CreateEnvironment(c *gin.Context) {
	var req application.CreateEnvironmentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, err)
		return
	}
	item, err := h.service.CreateEnvironment(c.Request.Context(), req, operatorID(c))
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, http.StatusCreated, item)
}

func (h *Handler) UpdateEnvironment(c *gin.Context) {
	var req application.UpdateEnvironmentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, err)
		return
	}
	item, err := h.service.UpdateEnvironment(c.Request.Context(), c.Param("environmentID"), req, operatorID(c))
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, http.StatusOK, item)
}

func (h *Handler) DeleteEnvironment(c *gin.Context) {
	if err := h.service.DeleteEnvironment(c.Request.Context(), c.Param("environmentID"), operatorID(c)); err != nil {
		response.Error(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *Handler) ListRelease(c *gin.Context) {
	var query application.ListQuery
	if err := c.ShouldBindQuery(&query); err != nil {
		response.Error(c, err)
		return
	}
	items, err := h.service.ListReleases(c.Request.Context(), query)
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OKList(c, items)
}

func (h *Handler) GetRelease(c *gin.Context) {
	item, err := h.service.GetRelease(c.Request.Context(), c.Param("releaseID"))
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, http.StatusOK, item)
}

func (h *Handler) CreateRelease(c *gin.Context) {
	var req application.CreateReleaseRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, err)
		return
	}
	item, err := h.service.CreateRelease(c.Request.Context(), req, operatorID(c))
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, http.StatusCreated, item)
}

func (h *Handler) SubmitRelease(c *gin.Context) {
	item, err := h.service.SubmitRelease(c.Request.Context(), c.Param("releaseID"), operatorID(c))
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, http.StatusOK, item)
}

func (h *Handler) ApproveRelease(c *gin.Context) {
	var req application.ReleaseActionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, err)
		return
	}
	_, item, err := h.service.ApproveRelease(c.Request.Context(), c.Param("releaseID"), req, operatorID(c))
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, http.StatusOK, item)
}

func (h *Handler) RejectRelease(c *gin.Context) {
	var req application.ReleaseActionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, err)
		return
	}
	_, item, err := h.service.RejectRelease(c.Request.Context(), c.Param("releaseID"), req, operatorID(c))
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, http.StatusOK, item)
}

func (h *Handler) RollbackRelease(c *gin.Context) {
	var req application.RollbackReleaseRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, err)
		return
	}
	item, err := h.service.RollbackRelease(c.Request.Context(), c.Param("releaseID"), req, operatorID(c))
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, http.StatusOK, item)
}

func (h *Handler) ListSync(c *gin.Context) {
	var query application.ListQuery
	if err := c.ShouldBindQuery(&query); err != nil {
		response.Error(c, err)
		return
	}
	items, err := h.service.ListSyncRecords(c.Request.Context(), query)
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OKList(c, items)
}

func (h *Handler) ListPolicyReport(c *gin.Context) {
	var query application.ListQuery
	if err := c.ShouldBindQuery(&query); err != nil {
		response.Error(c, err)
		return
	}
	items, err := h.service.ListPolicyReports(c.Request.Context(), query)
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OKList(c, items)
}

func (h *Handler) ListAudit(c *gin.Context) {
	var query application.ListQuery
	if err := c.ShouldBindQuery(&query); err != nil {
		response.Error(c, err)
		return
	}
	items, err := h.service.ListAudits(c.Request.Context(), query)
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OKList(c, items)
}

func operatorID(c *gin.Context) string {
	principal, ok := middleware.PrincipalFromContext(c.Request.Context())
	if !ok {
		return ""
	}
	return principal.Subject
}
