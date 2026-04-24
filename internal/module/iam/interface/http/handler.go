package http

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/dchest/captcha"
	"github.com/gin-gonic/gin"

	"github.com/lanyulei/kubeflare/internal/module/iam/application"
	sharedErrors "github.com/lanyulei/kubeflare/internal/shared/errors"
	"github.com/lanyulei/kubeflare/internal/shared/middleware"
	"github.com/lanyulei/kubeflare/internal/shared/response"
)

type Handler struct {
	service       *application.Service
	oidc          *application.OIDCService
	cookieOptions CookieOptions
}

type CookieOptions struct {
	Secure bool
	Domain string
}

func NewHandler(service *application.Service) *Handler {
	return &Handler{service: service}
}

func (h *Handler) SetOIDCService(service *application.OIDCService) {
	h.oidc = service
}

func (h *Handler) SetCookieOptions(options CookieOptions) {
	h.cookieOptions = options
}

func (h *Handler) Login(c *gin.Context) {
	var req application.LoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, err)
		return
	}

	loginResult, err := h.service.LoginWithOptions(c.Request.Context(), req, application.LoginOptions{
		ClientIP: c.ClientIP(),
	})
	if err != nil {
		response.Error(c, err)
		return
	}

	h.setAuthCookies(c, loginResult)
	response.OK(c, http.StatusOK, loginResult)
}

func (h *Handler) Refresh(c *gin.Context) {
	var req application.RefreshTokenRequest
	if err := c.ShouldBindJSON(&req); err != nil && !errors.Is(err, io.EOF) {
		response.Error(c, err)
		return
	}
	if strings.TrimSpace(req.RefreshToken) == "" {
		req.RefreshToken, _ = c.Cookie(middleware.RefreshTokenCookieName)
		if strings.TrimSpace(req.RefreshToken) != "" && !validCSRF(c) {
			invalidCSRF(c)
			return
		}
	}

	loginResult, err := h.service.Refresh(c.Request.Context(), req)
	if err != nil {
		response.Error(c, err)
		return
	}

	h.setAuthCookies(c, loginResult)
	response.OK(c, http.StatusOK, loginResult)
}

func (h *Handler) Logout(c *gin.Context) {
	var req application.LogoutRequest
	_ = c.ShouldBindJSON(&req)
	if strings.TrimSpace(req.RefreshToken) == "" {
		req.RefreshToken, _ = c.Cookie(middleware.RefreshTokenCookieName)
		if strings.TrimSpace(req.RefreshToken) != "" && !validCSRF(c) {
			invalidCSRF(c)
			return
		}
	}

	token, _ := middleware.BearerToken(c.GetHeader("Authorization"))
	if strings.TrimSpace(token) == "" {
		token, _ = c.Cookie(middleware.AccessTokenCookieName)
		if strings.TrimSpace(token) != "" && !validCSRF(c) {
			invalidCSRF(c)
			return
		}
	}
	if err := h.service.Logout(c.Request.Context(), token, req); err != nil {
		response.Error(c, err)
		return
	}

	h.clearAuthCookies(c)
	response.OK(c, http.StatusOK, gin.H{})
}

func (h *Handler) NewCaptcha(c *gin.Context) {
	response.OK(c, http.StatusOK, h.service.NewCaptchaChallenge())
}

func (h *Handler) CaptchaImage(c *gin.Context) {
	if err := captcha.WriteImage(c.Writer, c.Param("captchaID"), 240, 80); err != nil {
		response.Error(c, err)
	}
}

func (h *Handler) OIDCLogin(c *gin.Context) {
	if h.oidc == nil {
		response.Error(c, &sharedErrors.AppError{
			Code:    sharedErrors.CodeNotFound,
			Message: "oidc is not enabled",
			Status:  http.StatusNotFound,
		})
		return
	}
	loginURL, err := h.oidc.LoginURL(c.Request.Context())
	if err != nil {
		response.Error(c, err)
		return
	}
	c.Redirect(http.StatusFound, loginURL)
}

func (h *Handler) OIDCCallback(c *gin.Context) {
	if h.oidc == nil {
		response.Error(c, &sharedErrors.AppError{
			Code:    sharedErrors.CodeNotFound,
			Message: "oidc is not enabled",
			Status:  http.StatusNotFound,
		})
		return
	}
	result, err := h.oidc.Callback(c.Request.Context(), c.Query("state"), c.Query("code"))
	if err != nil {
		response.Error(c, err)
		return
	}
	h.setAuthCookies(c, result)
	response.OK(c, http.StatusOK, result)
}

func (h *Handler) List(c *gin.Context) {
	users, err := h.service.List(c.Request.Context())
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, http.StatusOK, gin.H{"items": users})
}

func (h *Handler) Get(c *gin.Context) {
	user, err := h.service.Get(c.Request.Context(), c.Param("userID"))
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, http.StatusOK, user)
}

func (h *Handler) Create(c *gin.Context) {
	var req application.CreateUserRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, err)
		return
	}

	user, err := h.service.Create(c.Request.Context(), req)
	if err != nil {
		response.Error(c, err)
		return
	}

	response.OK(c, http.StatusCreated, user)
}

func (h *Handler) Update(c *gin.Context) {
	var req application.UpdateUserRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, err)
		return
	}

	user, err := h.service.Update(c.Request.Context(), c.Param("userID"), req)
	if err != nil {
		response.Error(c, err)
		return
	}

	response.OK(c, http.StatusOK, user)
}

func (h *Handler) Delete(c *gin.Context) {
	if err := h.service.Delete(c.Request.Context(), c.Param("userID")); err != nil {
		response.Error(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *Handler) GetCurrent(c *gin.Context) {
	principal, err := currentPrincipal(c)
	if err != nil {
		response.Error(c, err)
		return
	}

	user, err := h.service.GetCurrent(c.Request.Context(), principal.Subject)
	if err != nil {
		response.Error(c, err)
		return
	}

	response.OK(c, http.StatusOK, user)
}

func (h *Handler) UpdateCurrent(c *gin.Context) {
	principal, err := currentPrincipal(c)
	if err != nil {
		response.Error(c, err)
		return
	}

	var req application.UpdateCurrentUserRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, err)
		return
	}

	user, err := h.service.UpdateCurrent(c.Request.Context(), principal.Subject, req)
	if err != nil {
		response.Error(c, err)
		return
	}

	response.OK(c, http.StatusOK, user)
}

func (h *Handler) UpdateCurrentPassword(c *gin.Context) {
	principal, err := currentPrincipal(c)
	if err != nil {
		response.Error(c, err)
		return
	}

	var req application.UpdateCurrentPasswordRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, err)
		return
	}

	if err := h.service.UpdateCurrentPassword(c.Request.Context(), principal.Subject, req); err != nil {
		response.Error(c, err)
		return
	}

	response.OK(c, http.StatusOK, gin.H{})
}

func (h *Handler) EnableMFA(c *gin.Context) {
	principal, err := currentPrincipal(c)
	if err != nil {
		response.Error(c, err)
		return
	}
	result, err := h.service.EnableMFA(c.Request.Context(), principal.Subject)
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, http.StatusOK, result)
}

func (h *Handler) ConfirmMFA(c *gin.Context) {
	principal, err := currentPrincipal(c)
	if err != nil {
		response.Error(c, err)
		return
	}
	var req application.ConfirmMFARequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, err)
		return
	}
	if err := h.service.ConfirmMFA(c.Request.Context(), principal.Subject, req); err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, http.StatusOK, gin.H{})
}

func (h *Handler) DisableMFA(c *gin.Context) {
	principal, err := currentPrincipal(c)
	if err != nil {
		response.Error(c, err)
		return
	}
	var req application.DisableMFARequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, err)
		return
	}
	if err := h.service.DisableMFA(c.Request.Context(), principal.Subject, req); err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, http.StatusOK, gin.H{})
}

func currentPrincipal(c *gin.Context) (middleware.Principal, error) {
	principal, ok := middleware.PrincipalFromContext(c.Request.Context())
	if ok {
		return principal, nil
	}

	return middleware.Principal{}, &sharedErrors.AppError{
		Code:    sharedErrors.CodeUnauthorized,
		Message: middleware.ErrUnauthorized.Error(),
		Status:  http.StatusUnauthorized,
		Err:     middleware.ErrUnauthorized,
	}
}

func validCSRF(c *gin.Context) bool {
	headerToken := strings.TrimSpace(c.GetHeader(middleware.CSRFTokenHeaderName))
	cookieToken, err := c.Cookie(middleware.CSRFTokenCookieName)
	cookieToken = strings.TrimSpace(cookieToken)
	return err == nil && headerToken != "" && len(headerToken) == len(cookieToken) && subtle.ConstantTimeCompare([]byte(headerToken), []byte(cookieToken)) == 1
}

func invalidCSRF(c *gin.Context) {
	response.Error(c, &sharedErrors.AppError{
		Code:    sharedErrors.CodeForbidden,
		Message: "invalid csrf token",
		Status:  http.StatusForbidden,
		Err:     errors.New("invalid csrf token"),
	})
}

func newCSRFToken() string {
	var buf [16]byte
	_, _ = rand.Read(buf[:])
	return hex.EncodeToString(buf[:])
}

func (h *Handler) setAuthCookies(c *gin.Context, result application.LoginResponse) {
	maxAge := int(result.ExpiresIn)
	refreshMaxAge := int(result.RefreshTokenExpiresIn)
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     middleware.AccessTokenCookieName,
		Value:    result.AccessToken,
		Path:     "/",
		Domain:   h.cookieOptions.Domain,
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   h.cookieOptions.Secure,
		SameSite: http.SameSiteLaxMode,
	})
	if result.RefreshToken != "" {
		http.SetCookie(c.Writer, &http.Cookie{
			Name:     middleware.RefreshTokenCookieName,
			Value:    result.RefreshToken,
			Path:     "/api/v1/auth",
			Domain:   h.cookieOptions.Domain,
			MaxAge:   refreshMaxAge,
			HttpOnly: true,
			Secure:   h.cookieOptions.Secure,
			SameSite: http.SameSiteLaxMode,
		})
	}
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     middleware.CSRFTokenCookieName,
		Value:    newCSRFToken(),
		Path:     "/",
		Domain:   h.cookieOptions.Domain,
		MaxAge:   refreshMaxAge,
		HttpOnly: false,
		Secure:   h.cookieOptions.Secure,
		SameSite: http.SameSiteLaxMode,
	})
}

func (h *Handler) clearAuthCookies(c *gin.Context) {
	expiresAt := time.Now().Add(-time.Hour)
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     middleware.AccessTokenCookieName,
		Value:    "",
		Path:     "/",
		Domain:   h.cookieOptions.Domain,
		MaxAge:   -1,
		Expires:  expiresAt,
		HttpOnly: true,
		Secure:   h.cookieOptions.Secure,
		SameSite: http.SameSiteLaxMode,
	})
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     middleware.RefreshTokenCookieName,
		Value:    "",
		Path:     "/api/v1/auth",
		Domain:   h.cookieOptions.Domain,
		MaxAge:   -1,
		Expires:  expiresAt,
		HttpOnly: true,
		Secure:   h.cookieOptions.Secure,
		SameSite: http.SameSiteLaxMode,
	})
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     middleware.CSRFTokenCookieName,
		Value:    "",
		Path:     "/",
		Domain:   h.cookieOptions.Domain,
		MaxAge:   -1,
		Expires:  expiresAt,
		HttpOnly: false,
		Secure:   h.cookieOptions.Secure,
		SameSite: http.SameSiteLaxMode,
	})
}
