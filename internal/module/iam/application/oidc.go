package application

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
	"gorm.io/gorm"

	"github.com/lanyulei/kubeflare/internal/module/iam/domain"
	sharedErrors "github.com/lanyulei/kubeflare/internal/shared/errors"
)

type OIDCConfig struct {
	IssuerURL    string
	ClientID     string
	ClientSecret string
	RedirectURL  string
	Scopes       []string
}

type OIDCService struct {
	repo        domain.Repository
	tokenIssuer TokenIssuer
	provider    *oidc.Provider
	verifier    *oidc.IDTokenVerifier
	oauthConfig oauth2.Config
	issuerURL   string
	stateStore  domain.SecurityStateStore
}

type oidcClaims struct {
	Subject       string `json:"sub"`
	Email         string `json:"email"`
	EmailVerified bool   `json:"email_verified"`
	Name          string `json:"name"`
	Nonce         string `json:"nonce"`
}

// OIDCLoginChallenge 携带需写入浏览器 Cookie 的一次性绑定值。state 绑定浏览器可
// 防 login-CSRF;nonce 写入授权请求并在 id_token 中回验,防 id_token 重放。
type OIDCLoginChallenge struct {
	AuthURL string
	State   string
	Nonce   string
}

func NewOIDCService(ctx context.Context, cfg OIDCConfig, repo domain.Repository, tokenIssuer TokenIssuer, stateStore domain.SecurityStateStore) (*OIDCService, error) {
	provider, err := oidc.NewProvider(ctx, cfg.IssuerURL)
	if err != nil {
		return nil, err
	}
	scopes := cfg.Scopes
	if len(scopes) == 0 {
		scopes = []string{oidc.ScopeOpenID, "profile", "email"}
	}
	oauthConfig := oauth2.Config{
		ClientID:     cfg.ClientID,
		ClientSecret: cfg.ClientSecret,
		Endpoint:     provider.Endpoint(),
		RedirectURL:  cfg.RedirectURL,
		Scopes:       scopes,
	}
	return &OIDCService{
		repo:        repo,
		tokenIssuer: tokenIssuer,
		provider:    provider,
		verifier:    provider.Verifier(&oidc.Config{ClientID: cfg.ClientID}),
		oauthConfig: oauthConfig,
		issuerURL:   cfg.IssuerURL,
		stateStore:  stateStore,
	}, nil
}

func (s *OIDCService) LoginURL(ctx context.Context) (OIDCLoginChallenge, error) {
	if s.stateStore == nil {
		return OIDCLoginChallenge{}, unauthorized("oidc state store is unavailable", nil)
	}
	state := newOpaqueValue()
	nonce := newOpaqueValue()
	if err := s.stateStore.SaveOIDCState(ctx, state, time.Now().UTC().Add(10*time.Minute)); err != nil {
		return OIDCLoginChallenge{}, err
	}
	return OIDCLoginChallenge{
		AuthURL: s.oauthConfig.AuthCodeURL(state, oidc.Nonce(nonce)),
		State:   state,
		Nonce:   nonce,
	}, nil
}

// Callback 校验回调。expectedState/expectedNonce 来自浏览器 Cookie:state 必须与
// 发起登录的浏览器一致(防 login-CSRF),nonce 必须与 id_token 内声明一致(防重放)。
func (s *OIDCService) Callback(ctx context.Context, state string, code string, expectedState string, expectedNonce string) (LoginResponse, error) {
	state = strings.TrimSpace(state)
	// 先比对浏览器绑定的 state,再查服务端存储,任一不符即拒绝。
	if expectedState == "" || subtle.ConstantTimeCompare([]byte(state), []byte(strings.TrimSpace(expectedState))) != 1 {
		return LoginResponse{}, unauthorized("invalid oidc state", nil)
	}
	stateOK, err := s.hasState(ctx, state)
	if err != nil {
		return LoginResponse{}, err
	}
	if !stateOK {
		return LoginResponse{}, unauthorized("invalid oidc state", nil)
	}
	oauthToken, err := s.oauthConfig.Exchange(ctx, strings.TrimSpace(code))
	if err != nil {
		return LoginResponse{}, unauthorized("oidc token exchange failed", err)
	}
	rawIDToken, ok := oauthToken.Extra("id_token").(string)
	if !ok || strings.TrimSpace(rawIDToken) == "" {
		return LoginResponse{}, unauthorized("oidc id token missing", nil)
	}
	idToken, err := s.verifier.Verify(ctx, rawIDToken)
	if err != nil {
		return LoginResponse{}, unauthorized("oidc id token is invalid", err)
	}
	var claims oidcClaims
	if err := idToken.Claims(&claims); err != nil {
		return LoginResponse{}, err
	}
	// 校验 nonce:必须存在且与浏览器 Cookie 一致,抵御 id_token 重放。
	if strings.TrimSpace(expectedNonce) == "" ||
		subtle.ConstantTimeCompare([]byte(strings.TrimSpace(claims.Nonce)), []byte(strings.TrimSpace(expectedNonce))) != 1 {
		return LoginResponse{}, unauthorized("invalid oidc nonce", nil)
	}
	stateOK, err = s.consumeState(ctx, state)
	if err != nil {
		return LoginResponse{}, err
	}
	if !stateOK {
		return LoginResponse{}, unauthorized("invalid oidc state", nil)
	}

	user, err := s.resolveOrCreateUser(ctx, claims)
	if err != nil {
		return LoginResponse{}, err
	}
	pair, err := s.tokenIssuer.IssueTokenPair(ctx, userSubject(user))
	if err != nil {
		return LoginResponse{}, err
	}
	return LoginResponse{
		AccessToken:           pair.AccessToken,
		RefreshToken:          pair.RefreshToken,
		TokenType:             pair.TokenType,
		ExpiresIn:             pair.ExpiresIn,
		RefreshTokenExpiresIn: pair.RefreshTokenExpiresIn,
		SessionID:             pair.SessionID,
		User:                  user,
	}, nil
}

func (s *OIDCService) consumeState(ctx context.Context, state string) (bool, error) {
	state = strings.TrimSpace(state)
	if s.stateStore == nil {
		return false, nil
	}
	return s.stateStore.ConsumeOIDCState(ctx, state)
}

func (s *OIDCService) hasState(ctx context.Context, state string) (bool, error) {
	state = strings.TrimSpace(state)
	if s.stateStore == nil {
		return false, nil
	}
	return s.stateStore.HasOIDCState(ctx, state)
}

func (s *OIDCService) resolveOrCreateUser(ctx context.Context, claims oidcClaims) (domain.User, error) {
	if strings.TrimSpace(claims.Subject) == "" {
		return domain.User{}, unauthorized("oidc subject missing", nil)
	}
	identity, err := s.repo.GetExternalIdentity(ctx, s.issuerURL, claims.Subject)
	if err == nil {
		user, err := s.repo.Get(ctx, identity.UserID)
		if err != nil {
			return domain.User{}, err
		}
		return activeUser(user)
	}
	if !isNotFoundError(err) {
		return domain.User{}, err
	}

	if strings.TrimSpace(claims.Email) == "" {
		return domain.User{}, unauthorized("oidc email is required", nil)
	}
	if !claims.EmailVerified {
		return domain.User{}, unauthorized("oidc email is not verified", nil)
	}

	username := oidcUsername(s.issuerURL, claims.Subject)
	nickname := strings.TrimSpace(claims.Name)
	if nickname == "" {
		nickname = username
	}
	now := time.Now().UTC()
	password, err := hashPassword(newOpaqueValue())
	if err != nil {
		return domain.User{}, err
	}
	user, err := s.repo.CreateWithExternalIdentity(ctx, domain.User{
		Username:  username,
		Nickname:  nickname,
		Password:  password,
		Email:     normalizeEmail(claims.Email),
		Status:    USER_STATUS_ACTIVE,
		CreatedAt: now,
		UpdatedAt: now,
	}, domain.ExternalIdentity{
		Provider:  s.issuerURL,
		Subject:   claims.Subject,
		Email:     normalizeEmail(claims.Email),
		CreatedAt: now,
		UpdatedAt: now,
	})
	if err != nil {
		return s.resolveIdentityAfterCreateConflict(ctx, err, claims)
	}
	return user, nil
}

func (s *OIDCService) resolveIdentityAfterCreateConflict(ctx context.Context, createErr error, claims oidcClaims) (domain.User, error) {
	identity, err := s.repo.GetExternalIdentity(ctx, s.issuerURL, claims.Subject)
	if err == nil {
		user, err := s.repo.Get(ctx, identity.UserID)
		if err != nil {
			return domain.User{}, err
		}
		return activeUser(user)
	}
	if isNotFoundError(err) {
		return domain.User{}, mapRepositoryError(createErr, "user not found")
	}
	return domain.User{}, err
}

func activeUser(user domain.User) (domain.User, error) {
	if user.Status != USER_STATUS_ACTIVE {
		return domain.User{}, &sharedErrors.AppError{
			Code:    sharedErrors.CodeForbidden,
			Message: "user is disabled",
			Status:  403,
		}
	}
	return user, nil
}

func oidcUsername(issuerURL string, subject string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(issuerURL) + "|" + strings.TrimSpace(subject)))
	return "oidc_" + hex.EncodeToString(sum[:])[:24]
}

func isNotFoundError(err error) bool {
	return err == gorm.ErrRecordNotFound || (err != nil && strings.Contains(strings.ToLower(err.Error()), "not found"))
}

func newOpaqueValue() string {
	var buf [24]byte
	_, _ = rand.Read(buf[:])
	return hex.EncodeToString(buf[:])
}

func unauthorized(message string, err error) error {
	return &sharedErrors.AppError{
		Code:    sharedErrors.CodeUnauthorized,
		Message: message,
		Status:  401,
		Err:     err,
	}
}
