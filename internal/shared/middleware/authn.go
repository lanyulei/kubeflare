package middleware

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	sharedErrors "github.com/lanyulei/kubeflare/internal/shared/errors"
	"github.com/lanyulei/kubeflare/internal/shared/response"
)

type Principal struct {
	Subject string `json:"subject"`
}

type Authenticator interface {
	Authenticate(ctx context.Context, token string) (Principal, error)
}

type TokenStateStore interface {
	CreateSession(ctx context.Context, session TokenSession) error
	GetSession(ctx context.Context, sessionID string) (TokenSession, error)
	RevokeSession(ctx context.Context, sessionID string, expiresAt time.Time) error
	IsSessionRevoked(ctx context.Context, sessionID string) (bool, error)
	RevokeToken(ctx context.Context, tokenID string, expiresAt time.Time) error
	IsTokenRevoked(ctx context.Context, tokenID string) (bool, error)
	StoreRefreshToken(ctx context.Context, token RefreshTokenRecord) error
	GetRefreshToken(ctx context.Context, tokenID string) (RefreshTokenRecord, error)
	ConsumeRefreshToken(ctx context.Context, token RefreshTokenRecord) error
	RotateRefreshToken(ctx context.Context, oldToken RefreshTokenRecord, newToken RefreshTokenRecord) error
	RevokeSubjectSessions(ctx context.Context, subject string, expiresAt time.Time) error
}

type PrincipalResolver interface {
	ResolvePrincipal(ctx context.Context, subject string) (Principal, error)
}

type SignedTokenAuthenticator struct {
	tokenManager *SignedTokenManager
	resolver     PrincipalResolver
}

type SignedTokenManager struct {
	secret     []byte
	accessTTL  time.Duration
	refreshTTL time.Duration
	store      TokenStateStore
}

type TokenPair struct {
	AccessToken           string
	RefreshToken          string
	TokenType             string
	ExpiresIn             int64
	RefreshTokenExpiresIn int64
	SessionID             string
}

type TokenSubject struct {
	Subject   string
	SessionID string
	ExpiresAt time.Time
}

type TokenSession struct {
	ID        string
	Subject   string
	ExpiresAt time.Time
	RevokedAt *time.Time
}

type RefreshTokenRecord struct {
	ID        string
	SessionID string
	Subject   string
	ExpiresAt time.Time
	RevokedAt *time.Time
}

type signedTokenPayload struct {
	Kind      string `json:"kind"`
	Subject   string `json:"sub"`
	ID        string `json:"jti"`
	SessionID string `json:"sid,omitempty"`
	ExpiresAt int64  `json:"exp"`
}

type principalContextKey struct{}

const (
	AccessTokenCookieName  = "kubeflare_access_token"
	RefreshTokenCookieName = "kubeflare_refresh_token"
	CSRFTokenCookieName    = "kubeflare_csrf_token"
	CSRFTokenHeaderName    = "X-Kubeflare-CSRF"
)

func NewSignedTokenManager(secret string, ttl time.Duration) *SignedTokenManager {
	return NewSignedTokenManagerWithOptions(secret, ttl, 7*24*time.Hour, nil)
}

func NewSignedTokenManagerWithOptions(secret string, accessTTL time.Duration, refreshTTL time.Duration, store TokenStateStore) *SignedTokenManager {
	if accessTTL <= 0 {
		accessTTL = 24 * time.Hour
	}
	if refreshTTL <= 0 {
		refreshTTL = 7 * 24 * time.Hour
	}
	return &SignedTokenManager{
		secret:     []byte(strings.TrimSpace(secret)),
		accessTTL:  accessTTL,
		refreshTTL: refreshTTL,
		store:      store,
	}
}

func (m *SignedTokenManager) IssueToken(ctx context.Context, subject string) (string, error) {
	pair, err := m.IssueTokenPair(ctx, subject)
	if err != nil {
		return "", err
	}
	return pair.AccessToken, nil
}

func (m *SignedTokenManager) IssueTokenPair(ctx context.Context, subject string) (TokenPair, error) {
	sessionID := newTokenID()
	now := time.Now().UTC()
	sessionExpiresAt := now.Add(m.refreshTTL)
	if m.store != nil {
		if err := m.store.CreateSession(ctx, TokenSession{
			ID:        sessionID,
			Subject:   subject,
			ExpiresAt: sessionExpiresAt,
		}); err != nil {
			return TokenPair{}, err
		}
	}

	accessToken, accessPayload, err := m.issueToken("access", subject, sessionID, m.accessTTL)
	if err != nil {
		return TokenPair{}, err
	}
	refreshToken, refreshPayload, err := m.issueToken("refresh", subject, sessionID, m.refreshTTL)
	if err != nil {
		return TokenPair{}, err
	}
	if m.store != nil {
		if err := m.store.StoreRefreshToken(ctx, RefreshTokenRecord{
			ID:        refreshPayload.ID,
			SessionID: sessionID,
			Subject:   subject,
			ExpiresAt: time.Unix(refreshPayload.ExpiresAt, 0).UTC(),
		}); err != nil {
			return TokenPair{}, err
		}
	}

	return TokenPair{
		AccessToken:           accessToken,
		RefreshToken:          refreshToken,
		TokenType:             "Bearer",
		ExpiresIn:             accessPayload.ExpiresAt - now.Unix(),
		RefreshTokenExpiresIn: refreshPayload.ExpiresAt - now.Unix(),
		SessionID:             sessionID,
	}, nil
}

func (m *SignedTokenManager) RefreshToken(ctx context.Context, refreshToken string) (TokenPair, error) {
	payload, err := m.verifyToken(ctx, refreshToken, "refresh")
	if err != nil {
		return TokenPair{}, ErrUnauthorized
	}
	now := time.Now().UTC()
	accessToken, accessPayload, err := m.issueToken("access", payload.Subject, payload.SessionID, m.accessTTL)
	if err != nil {
		return TokenPair{}, err
	}
	newRefreshToken, refreshPayload, err := m.issueToken("refresh", payload.Subject, payload.SessionID, m.refreshTTL)
	if err != nil {
		return TokenPair{}, err
	}
	if m.store != nil {
		if err := m.store.RotateRefreshToken(ctx, RefreshTokenRecord{
			ID:        payload.ID,
			SessionID: payload.SessionID,
			Subject:   payload.Subject,
			ExpiresAt: time.Unix(payload.ExpiresAt, 0).UTC(),
		}, RefreshTokenRecord{
			ID:        refreshPayload.ID,
			SessionID: payload.SessionID,
			Subject:   payload.Subject,
			ExpiresAt: time.Unix(refreshPayload.ExpiresAt, 0).UTC(),
		}); err != nil {
			return TokenPair{}, err
		}
	}
	return TokenPair{
		AccessToken:           accessToken,
		RefreshToken:          newRefreshToken,
		TokenType:             "Bearer",
		ExpiresIn:             accessPayload.ExpiresAt - now.Unix(),
		RefreshTokenExpiresIn: refreshPayload.ExpiresAt - now.Unix(),
		SessionID:             payload.SessionID,
	}, nil
}

func (m *SignedTokenManager) RefreshTokenSubject(ctx context.Context, refreshToken string) (TokenSubject, error) {
	payload, err := m.verifyToken(ctx, refreshToken, "refresh")
	if err != nil {
		return TokenSubject{}, ErrUnauthorized
	}
	return TokenSubject{
		Subject:   payload.Subject,
		SessionID: payload.SessionID,
		ExpiresAt: time.Unix(payload.ExpiresAt, 0).UTC(),
	}, nil
}

func (m *SignedTokenManager) RevokeToken(ctx context.Context, token string) error {
	payload, err := m.verifyToken(ctx, token, "")
	if err != nil {
		return ErrUnauthorized
	}
	if m.store == nil {
		return nil
	}
	return m.store.RevokeToken(ctx, payload.ID, time.Unix(payload.ExpiresAt, 0).UTC())
}

func (m *SignedTokenManager) RevokeSession(ctx context.Context, token string) error {
	payload, err := m.verifyToken(ctx, token, "")
	if err != nil {
		return ErrUnauthorized
	}
	if m.store == nil {
		return nil
	}
	expiresAt := time.Now().UTC().Add(m.refreshTTL)
	session, err := m.store.GetSession(ctx, payload.SessionID)
	if err == nil && session.ExpiresAt.After(time.Now().UTC()) {
		expiresAt = session.ExpiresAt
	}
	return m.store.RevokeSession(ctx, payload.SessionID, expiresAt)
}

func (m *SignedTokenManager) RevokeSubjectSessions(ctx context.Context, token string) error {
	payload, err := m.verifyToken(ctx, token, "")
	if err != nil {
		return ErrUnauthorized
	}
	if m.store == nil {
		return nil
	}
	expiresAt := time.Now().UTC().Add(m.refreshTTL)
	session, err := m.store.GetSession(ctx, payload.SessionID)
	if err == nil && session.ExpiresAt.After(time.Now().UTC()) {
		expiresAt = session.ExpiresAt
	}
	return m.store.RevokeSubjectSessions(ctx, payload.Subject, expiresAt)
}

func NewSignedTokenAuthenticator(tokenManager *SignedTokenManager, resolver PrincipalResolver) *SignedTokenAuthenticator {
	return &SignedTokenAuthenticator{
		tokenManager: tokenManager,
		resolver:     resolver,
	}
}

func (a *SignedTokenAuthenticator) Authenticate(ctx context.Context, token string) (Principal, error) {
	if a.tokenManager == nil || a.resolver == nil {
		return Principal{}, ErrUnauthorized
	}

	payload, err := a.tokenManager.verifyToken(ctx, token, "access")
	if err != nil {
		return Principal{}, ErrUnauthorized
	}

	principal, err := a.resolver.ResolvePrincipal(ctx, payload.Subject)
	if err != nil {
		return Principal{}, ErrUnauthorized
	}
	return principal, nil
}

func (m *SignedTokenManager) issueToken(kind, subject string, sessionID string, ttl time.Duration) (string, signedTokenPayload, error) {
	if len(m.secret) == 0 {
		return "", signedTokenPayload{}, errors.New("signing key is empty")
	}
	if ttl <= 0 {
		ttl = m.accessTTL
	}

	payload := signedTokenPayload{
		Kind:      kind,
		Subject:   subject,
		ID:        newTokenID(),
		SessionID: sessionID,
		ExpiresAt: time.Now().UTC().Add(ttl).Unix(),
	}
	encodedPayload, err := encodeSignedPayload(payload)
	if err != nil {
		return "", signedTokenPayload{}, err
	}

	signature := m.sign(encodedPayload)
	return encodedPayload + "." + signature, payload, nil
}

func (m *SignedTokenManager) verifyToken(ctx context.Context, token string, kind string) (signedTokenPayload, error) {
	if len(m.secret) == 0 {
		return signedTokenPayload{}, ErrUnauthorized
	}

	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return signedTokenPayload{}, ErrUnauthorized
	}

	expectedSignature := m.sign(parts[0])
	if !hmac.Equal([]byte(parts[1]), []byte(expectedSignature)) {
		return signedTokenPayload{}, ErrUnauthorized
	}

	payload, err := decodeSignedPayload(parts[0])
	if err != nil {
		return signedTokenPayload{}, ErrUnauthorized
	}
	if kind != "" && payload.Kind != kind {
		return signedTokenPayload{}, ErrUnauthorized
	}
	if payload.Subject == "" || payload.ID == "" || payload.SessionID == "" || payload.ExpiresAt <= time.Now().Unix() {
		return signedTokenPayload{}, ErrUnauthorized
	}
	if m.store != nil {
		revoked, err := m.store.IsTokenRevoked(ctx, payload.ID)
		if err != nil || revoked {
			return signedTokenPayload{}, ErrUnauthorized
		}
		sessionRevoked, err := m.store.IsSessionRevoked(ctx, payload.SessionID)
		if err != nil || sessionRevoked {
			return signedTokenPayload{}, ErrUnauthorized
		}
	}
	return payload, nil
}

func AuthenticateHTTP(authenticator Authenticator, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := tokenFromHTTPRequest(r)
		if !ok {
			requestID, _ := RequestIDFromContext(r.Context())
			writeAuthError(w, requestID, ErrUnauthorized)
			return
		}

		principal, err := authenticator.Authenticate(r.Context(), token)
		if err != nil {
			requestID, _ := RequestIDFromContext(r.Context())
			writeAuthError(w, requestID, err)
			return
		}

		ctx := context.WithValue(r.Context(), principalContextKey{}, principal)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func PrincipalFromContext(ctx context.Context) (Principal, bool) {
	principal, ok := ctx.Value(principalContextKey{}).(Principal)
	return principal, ok
}

func AuthenticateGin(authenticator Authenticator) gin.HandlerFunc {
	return func(c *gin.Context) {
		token, ok := tokenFromGinContext(c)
		if !ok {
			response.Error(c, &sharedErrors.AppError{
				Code:    sharedErrors.CodeUnauthorized,
				Message: ErrUnauthorized.Error(),
				Status:  http.StatusUnauthorized,
				Err:     ErrUnauthorized,
			})
			return
		}

		principal, err := authenticator.Authenticate(c.Request.Context(), token)
		if err != nil {
			response.Error(c, &sharedErrors.AppError{
				Code:    sharedErrors.CodeUnauthorized,
				Message: err.Error(),
				Status:  http.StatusUnauthorized,
				Err:     err,
			})
			return
		}

		ctx := context.WithValue(c.Request.Context(), principalContextKey{}, principal)
		c.Request = c.Request.WithContext(ctx)
		c.Set("principal", principal)
		c.Next()
	}
}

func bearerToken(header string) (string, bool) {
	if !strings.HasPrefix(header, "Bearer ") {
		return "", false
	}

	token := strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
	return token, token != ""
}

func BearerToken(header string) (string, bool) {
	return bearerToken(header)
}

func tokenFromHTTPRequest(r *http.Request) (string, bool) {
	if token, ok := bearerToken(r.Header.Get("Authorization")); ok {
		return token, true
	}
	cookie, err := r.Cookie(AccessTokenCookieName)
	if err != nil {
		return "", false
	}
	token := strings.TrimSpace(cookie.Value)
	return token, token != ""
}

func tokenFromGinContext(c *gin.Context) (string, bool) {
	if token, ok := bearerToken(c.GetHeader("Authorization")); ok {
		return token, true
	}
	token, err := c.Cookie(AccessTokenCookieName)
	if err != nil {
		return "", false
	}
	token = strings.TrimSpace(token)
	return token, token != ""
}

func RequireCSRFGin() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Method == http.MethodGet || c.Request.Method == http.MethodHead || c.Request.Method == http.MethodOptions {
			c.Next()
			return
		}
		if _, ok := bearerToken(c.GetHeader("Authorization")); ok {
			c.Next()
			return
		}
		if _, err := c.Cookie(AccessTokenCookieName); err != nil {
			c.Next()
			return
		}
		headerToken := strings.TrimSpace(c.GetHeader(CSRFTokenHeaderName))
		cookieToken, err := c.Cookie(CSRFTokenCookieName)
		if err != nil || headerToken == "" || !hmac.Equal([]byte(headerToken), []byte(strings.TrimSpace(cookieToken))) {
			response.Error(c, &sharedErrors.AppError{
				Code:    sharedErrors.CodeForbidden,
				Message: "invalid csrf token",
				Status:  http.StatusForbidden,
				Err:     errors.New("invalid csrf token"),
			})
			return
		}
		c.Next()
	}
}

func RequireCSRFHTTP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}
		if _, ok := bearerToken(r.Header.Get("Authorization")); ok {
			next.ServeHTTP(w, r)
			return
		}
		if _, err := r.Cookie(AccessTokenCookieName); err != nil {
			next.ServeHTTP(w, r)
			return
		}
		headerToken := strings.TrimSpace(r.Header.Get(CSRFTokenHeaderName))
		cookie, err := r.Cookie(CSRFTokenCookieName)
		if err != nil || headerToken == "" || !hmac.Equal([]byte(headerToken), []byte(strings.TrimSpace(cookie.Value))) {
			requestID, _ := RequestIDFromContext(r.Context())
			response.HTTPError(w, requestID, &sharedErrors.AppError{
				Code:    sharedErrors.CodeForbidden,
				Message: "invalid csrf token",
				Status:  http.StatusForbidden,
				Err:     errors.New("invalid csrf token"),
			})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeAuthError(w http.ResponseWriter, requestID string, err error) {
	response.HTTPError(w, requestID, &sharedErrors.AppError{
		Code:    sharedErrors.CodeUnauthorized,
		Message: err.Error(),
		Status:  http.StatusUnauthorized,
		Err:     err,
	})
}

func (m *SignedTokenManager) sign(payload string) string {
	mac := hmac.New(sha256.New, m.secret)
	_, _ = mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func encodeSignedPayload(payload signedTokenPayload) (string, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}

func decodeSignedPayload(value string) (signedTokenPayload, error) {
	var payload signedTokenPayload

	data, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return signedTokenPayload{}, err
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return signedTokenPayload{}, err
	}
	return payload, nil
}

func newTokenID() string {
	var buf [16]byte
	_, _ = rand.Read(buf[:])
	return hex.EncodeToString(buf[:])
}
