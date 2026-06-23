package http

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/lanyulei/kubeflare/internal/module/gitops/application"
	"github.com/lanyulei/kubeflare/internal/shared/middleware"
	"github.com/lanyulei/kubeflare/internal/shared/response"
)

// maxWebhookBodyBytes 限制 webhook 请求体大小,防御异常超大载荷耗尽内存。
const maxWebhookBodyBytes = 1 << 20 // 1 MiB

type Handler struct {
	service       *application.Service
	webhookSecret string // Flux 状态回流 webhook 的 HMAC 验签密钥;为空时拒绝一切 webhook。
}

func NewHandler(service *application.Service) *Handler {
	return &Handler{service: service}
}

// SetWebhookSecret 注入 Flux webhook 验签密钥。空字符串表示未配置,FluxWebhook 将一律 401。
func (h *Handler) SetWebhookSecret(secret string) {
	h.webhookSecret = strings.TrimSpace(secret)
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
	page, err := h.service.ListProviders(c.Request.Context(), query)
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, http.StatusOK, page)
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
	// 连通性失败由 service 以 (result, nil) 返回并落库,这里只需对真正的基础设施
	// 错误(解密/写库失败)走错误响应,其余统一 200 返回探测结果。
	result, err := h.service.TestProvider(c.Request.Context(), c.Param("providerID"), operatorID(c))
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
	page, err := h.service.ListGitRepositories(c.Request.Context(), query)
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, http.StatusOK, page)
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
	page, err := h.service.ListApplications(c.Request.Context(), query)
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, http.StatusOK, page)
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
	page, err := h.service.ListEnvironments(c.Request.Context(), query)
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, http.StatusOK, page)
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
	page, err := h.service.ListReleases(c.Request.Context(), query)
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, http.StatusOK, page)
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
	page, err := h.service.ListSyncRecords(c.Request.Context(), query)
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, http.StatusOK, page)
}

func (h *Handler) ListPolicyReport(c *gin.Context) {
	var query application.ListQuery
	if err := c.ShouldBindQuery(&query); err != nil {
		response.Error(c, err)
		return
	}
	page, err := h.service.ListPolicyReports(c.Request.Context(), query)
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, http.StatusOK, page)
}

func (h *Handler) ListAudit(c *gin.Context) {
	var query application.ListQuery
	if err := c.ShouldBindQuery(&query); err != nil {
		response.Error(c, err)
		return
	}
	page, err := h.service.ListAudits(c.Request.Context(), query)
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, http.StatusOK, page)
}

func operatorID(c *gin.Context) string {
	principal, ok := middleware.PrincipalFromContext(c.Request.Context())
	if !ok {
		return ""
	}
	return principal.Subject
}

// fluxWebhookPayload 是 Flux notification-controller 上报事件的原始结构(仅取所需字段)。
// revision 在不同版本可能位于 metadata.revision,这里一并兼容。
type fluxWebhookPayload struct {
	InvolvedObject struct {
		Kind      string `json:"kind"`
		Namespace string `json:"namespace"`
		Name      string `json:"name"`
	} `json:"involvedObject"`
	Severity string            `json:"severity"`
	Reason   string            `json:"reason"`
	Message  string            `json:"message"`
	Revision string            `json:"revision"`
	Metadata map[string]string `json:"metadata"`
}

// FluxWebhook 接收 Flux notification-controller 的调和事件并回流到发布单状态。
// 该端点为公开路由(CSRF 豁免),改用全局密钥 HMAC-SHA256 验签:
//   - 未配置密钥或验签不通过 → 401(fail-closed,绝不放行未签名请求改状态);
//   - body 读取/解析失败 → 400;
//   - 其余一律 200 ack(业务侧关联不到资源时静默忽略,避免 Flux 无限重投)。
func (h *Handler) FluxWebhook(c *gin.Context) {
	// 未配置密钥时拒绝一切请求,避免裸奔的状态写入入口。
	if h.webhookSecret == "" {
		c.Status(http.StatusUnauthorized)
		return
	}
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, maxWebhookBodyBytes))
	if err != nil {
		c.Status(http.StatusBadRequest)
		return
	}
	if !h.verifyWebhookSignature(c, body) {
		c.Status(http.StatusUnauthorized)
		return
	}

	var payload fluxWebhookPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		c.Status(http.StatusBadRequest)
		return
	}

	revision := strings.TrimSpace(payload.Revision)
	if revision == "" && payload.Metadata != nil {
		revision = strings.TrimSpace(payload.Metadata["revision"])
	}
	event := application.FluxEvent{
		Kind:      strings.TrimSpace(payload.InvolvedObject.Kind),
		Namespace: strings.TrimSpace(payload.InvolvedObject.Namespace),
		Name:      strings.TrimSpace(payload.InvolvedObject.Name),
		Revision:  revision,
		Reason:    strings.TrimSpace(payload.Reason),
		Severity:  strings.TrimSpace(payload.Severity),
		Message:   payload.Message,
	}
	if err := h.service.HandleFluxEvent(c.Request.Context(), event); err != nil {
		response.Error(c, err)
		return
	}
	c.Status(http.StatusOK)
}

// verifyWebhookSignature 用 HMAC-SHA256 校验请求体签名。签名取自 X-Signature 头,
// 兼容 "sha256=<hex>" 与裸 "<hex>" 两种形式,使用常量时间比较防时序侧信道。
func (h *Handler) verifyWebhookSignature(c *gin.Context, body []byte) bool {
	provided := strings.TrimSpace(c.GetHeader("X-Signature"))
	provided = strings.TrimPrefix(provided, "sha256=")
	if provided == "" {
		return false
	}
	mac := hmac.New(sha256.New, []byte(h.webhookSecret))
	mac.Write(body)
	expected := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(provided), []byte(expected))
}
