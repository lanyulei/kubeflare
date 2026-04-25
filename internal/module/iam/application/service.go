package application

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/dchest/captcha"
	"github.com/go-playground/validator/v10"
	"github.com/pquerna/otp/totp"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"

	"github.com/lanyulei/kubeflare/internal/module/iam/domain"
	"github.com/lanyulei/kubeflare/internal/platform/secrets"
	sharedErrors "github.com/lanyulei/kubeflare/internal/shared/errors"
	"github.com/lanyulei/kubeflare/internal/shared/middleware"
)

var usernamePattern = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)

const (
	USER_STATUS_DISABLED = 0
	USER_STATUS_ACTIVE   = 1
)

type TokenIssuer interface {
	IssueToken(ctx context.Context, subject string, roles []string) (string, error)
	IssueTokenPair(ctx context.Context, subject string, roles []string) (middleware.TokenPair, error)
	RefreshToken(ctx context.Context, refreshToken string) (middleware.TokenPair, error)
	RefreshTokenSubject(ctx context.Context, refreshToken string) (middleware.TokenSubject, error)
	RevokeToken(ctx context.Context, token string) error
	RevokeSession(ctx context.Context, token string) error
	RevokeSubjectSessions(ctx context.Context, token string) error
}

type LoginOptions struct {
	ClientIP string
}

type AuthPolicy struct {
	CaptchaTTL            time.Duration
	CaptchaFailureTrigger int
	MaxFailedAttempts     int
	LockoutDuration       time.Duration
}

type Service struct {
	repo        domain.Repository
	validator   *validator.Validate
	tokenIssuer TokenIssuer
	security    domain.SecurityStateStore
	encryptor   secrets.Encryptor
	policy      AuthPolicy
}

func NewService(repo domain.Repository, validator *validator.Validate, tokenIssuer TokenIssuer) *Service {
	return &Service{
		repo:        repo,
		validator:   validator,
		tokenIssuer: tokenIssuer,
		encryptor:   secrets.NoopEncryptor{},
		policy: AuthPolicy{
			CaptchaTTL:            5 * time.Minute,
			CaptchaFailureTrigger: 3,
			MaxFailedAttempts:     5,
			LockoutDuration:       15 * time.Minute,
		},
	}
}

func (s *Service) SetAuthPolicy(policy AuthPolicy) {
	if policy.CaptchaTTL > 0 {
		s.policy.CaptchaTTL = policy.CaptchaTTL
	}
	if policy.CaptchaFailureTrigger >= 0 {
		s.policy.CaptchaFailureTrigger = policy.CaptchaFailureTrigger
	}
	if policy.MaxFailedAttempts >= 0 {
		s.policy.MaxFailedAttempts = policy.MaxFailedAttempts
	}
	if policy.LockoutDuration > 0 {
		s.policy.LockoutDuration = policy.LockoutDuration
	}
}

func (s *Service) SetSecurityStateStore(store domain.SecurityStateStore) {
	s.security = store
}

func (s *Service) SetSecretEncryptor(encryptor secrets.Encryptor) {
	if encryptor != nil {
		s.encryptor = encryptor
	}
}

func (s *Service) Login(ctx context.Context, req LoginRequest) (LoginResponse, error) {
	return s.LoginWithOptions(ctx, req, LoginOptions{})
}

func (s *Service) LoginWithOptions(ctx context.Context, req LoginRequest, opts LoginOptions) (LoginResponse, error) {
	if err := s.validator.Struct(req); err != nil {
		return LoginResponse{}, err
	}
	if s.tokenIssuer == nil {
		return LoginResponse{}, &sharedErrors.AppError{
			Code:    sharedErrors.CodeUnauthorized,
			Message: "login is unavailable",
			Status:  401,
		}
	}

	username, err := normalizeUsername(req.Username)
	if err != nil {
		return LoginResponse{}, err
	}

	captchaRequired, err := s.requiresCaptcha(ctx, username, opts.ClientIP)
	if err != nil {
		return LoginResponse{}, authStateUnavailable(err)
	}
	if captchaRequired && !captcha.VerifyString(req.CaptchaID, req.CaptchaCode) {
		return LoginResponse{RequiresCaptcha: true}, &sharedErrors.AppError{
			Code:    sharedErrors.CodeUnauthorized,
			Message: "captcha is required",
			Status:  401,
		}
	}
	lockedUntil, locked, err := s.loginLock(ctx, username, opts.ClientIP)
	if err != nil {
		return LoginResponse{}, authStateUnavailable(err)
	}
	if locked {
		return LoginResponse{}, &sharedErrors.AppError{
			Code:    sharedErrors.CodeForbidden,
			Message: "account is temporarily locked until " + lockedUntil.Format(time.RFC3339),
			Status:  403,
		}
	}

	user, err := s.repo.GetByUsername(ctx, username)
	if err != nil {
		if recordErr := s.recordLoginFailure(ctx, username, opts.ClientIP); recordErr != nil {
			return LoginResponse{}, authStateUnavailable(recordErr)
		}
		return LoginResponse{}, &sharedErrors.AppError{
			Code:    sharedErrors.CodeUnauthorized,
			Message: "invalid username or password",
			Status:  401,
			Err:     err,
		}
	}
	if user.Status != USER_STATUS_ACTIVE {
		return LoginResponse{}, &sharedErrors.AppError{
			Code:    sharedErrors.CodeForbidden,
			Message: "user is disabled",
			Status:  403,
		}
	}
	if strings.TrimSpace(user.Password) == "" {
		return LoginResponse{}, &sharedErrors.AppError{
			Code:    sharedErrors.CodeForbidden,
			Message: "password is not configured",
			Status:  403,
		}
	}
	if err := bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(req.Password)); err != nil {
		if recordErr := s.recordLoginFailure(ctx, username, opts.ClientIP); recordErr != nil {
			return LoginResponse{}, authStateUnavailable(recordErr)
		}
		return LoginResponse{}, &sharedErrors.AppError{
			Code:    sharedErrors.CodeUnauthorized,
			Message: "invalid username or password",
			Status:  401,
			Err:     err,
		}
	}
	if user.MFAEnabled {
		if strings.TrimSpace(user.MFASecret) == "" {
			return LoginResponse{}, &sharedErrors.AppError{
				Code:    sharedErrors.CodeForbidden,
				Message: "mfa is not configured",
				Status:  403,
			}
		}
		if strings.TrimSpace(req.OTPCode) == "" {
			return LoginResponse{RequiresMFA: true}, &sharedErrors.AppError{
				Code:    sharedErrors.CodeUnauthorized,
				Message: "mfa code is required",
				Status:  401,
			}
		}
		mfaSecret, err := s.decryptSecret(user.MFASecret)
		if err != nil {
			return LoginResponse{}, err
		}
		if !totp.Validate(req.OTPCode, mfaSecret) {
			if recordErr := s.recordLoginFailure(ctx, username, opts.ClientIP); recordErr != nil {
				return LoginResponse{}, authStateUnavailable(recordErr)
			}
			return LoginResponse{}, &sharedErrors.AppError{
				Code:    sharedErrors.CodeUnauthorized,
				Message: "invalid mfa code",
				Status:  401,
			}
		}
	}

	pair, err := s.tokenIssuer.IssueTokenPair(ctx, userSubject(user), user.Roles)
	if err != nil {
		return LoginResponse{}, err
	}
	s.clearLoginFailures(ctx, username, opts.ClientIP)

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

func (s *Service) Refresh(ctx context.Context, req RefreshTokenRequest) (LoginResponse, error) {
	if err := s.validator.Struct(req); err != nil {
		return LoginResponse{}, err
	}
	if s.tokenIssuer == nil {
		return LoginResponse{}, &sharedErrors.AppError{
			Code:    sharedErrors.CodeUnauthorized,
			Message: "refresh is unavailable",
			Status:  401,
		}
	}
	refreshToken := strings.TrimSpace(req.RefreshToken)
	tokenSubject, err := s.tokenIssuer.RefreshTokenSubject(ctx, refreshToken)
	if err != nil {
		return LoginResponse{}, &sharedErrors.AppError{
			Code:    sharedErrors.CodeUnauthorized,
			Message: "invalid refresh token",
			Status:  401,
			Err:     err,
		}
	}
	user, err := s.resolveUser(ctx, tokenSubject.Subject)
	if err != nil {
		return LoginResponse{}, mapRepositoryError(err, "user not found")
	}
	if user.Status != USER_STATUS_ACTIVE {
		return LoginResponse{}, &sharedErrors.AppError{
			Code:    sharedErrors.CodeForbidden,
			Message: "user is disabled",
			Status:  403,
		}
	}
	pair, err := s.tokenIssuer.RefreshToken(ctx, refreshToken)
	if err != nil {
		return LoginResponse{}, &sharedErrors.AppError{
			Code:    sharedErrors.CodeUnauthorized,
			Message: "invalid refresh token",
			Status:  401,
			Err:     err,
		}
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

func (s *Service) Logout(ctx context.Context, accessToken string, req LogoutRequest) error {
	if s.tokenIssuer == nil {
		return nil
	}
	if req.AllSessions {
		token := strings.TrimSpace(accessToken)
		if token == "" {
			token = strings.TrimSpace(req.RefreshToken)
		}
		if token == "" {
			return &sharedErrors.AppError{
				Code:    sharedErrors.CodeUnauthorized,
				Message: "logout token is required",
				Status:  401,
			}
		}
		return s.tokenIssuer.RevokeSubjectSessions(ctx, token)
	}
	if strings.TrimSpace(accessToken) != "" {
		if err := s.tokenIssuer.RevokeSession(ctx, accessToken); err != nil {
			return err
		}
	}
	if strings.TrimSpace(req.RefreshToken) != "" {
		return s.tokenIssuer.RevokeToken(ctx, req.RefreshToken)
	}
	return nil
}

func (s *Service) NewCaptchaChallenge() CaptchaChallengeResponse {
	id := captcha.NewLen(6)
	return CaptchaChallengeResponse{
		ID:        id,
		ImageURL:  "/api/v1/auth/captcha/" + id + ".png",
		ExpiresIn: int64(s.policy.CaptchaTTL.Seconds()),
	}
}

func (s *Service) List(ctx context.Context) ([]domain.User, error) {
	return s.repo.List(ctx)
}

func (s *Service) Get(ctx context.Context, id string) (domain.User, error) {
	user, err := s.resolveUser(ctx, id)
	if err != nil {
		return domain.User{}, mapRepositoryError(err, "user not found")
	}
	return user, nil
}

func (s *Service) Create(ctx context.Context, req CreateUserRequest) (domain.User, error) {
	if err := s.validator.Struct(req); err != nil {
		return domain.User{}, err
	}

	username, err := normalizeUsername(req.Username)
	if err != nil {
		return domain.User{}, err
	}
	nickname, err := normalizeNickname(req.Nickname)
	if err != nil {
		return domain.User{}, err
	}

	password, err := hashPassword(req.Password)
	if err != nil {
		return domain.User{}, err
	}

	now := time.Now().UTC()
	return s.repo.Create(ctx, domain.User{
		Username:  username,
		Nickname:  nickname,
		Password:  password,
		Email:     normalizeEmail(req.Email),
		Phone:     strings.TrimSpace(req.Phone),
		Avatar:    strings.TrimSpace(req.Avatar),
		Remarks:   strings.TrimSpace(req.Remarks),
		IsAdmin:   normalizeIsAdmin(req.IsAdmin, false),
		Status:    normalizeStatus(req.Status, USER_STATUS_ACTIVE),
		Roles:     []string{"user"},
		CreatedAt: now,
		UpdatedAt: now,
	})
}

func (s *Service) Update(ctx context.Context, id string, req UpdateUserRequest) (domain.User, error) {
	if err := s.validator.Struct(req); err != nil {
		return domain.User{}, err
	}

	existing, err := s.resolveUser(ctx, id)
	if err != nil {
		return domain.User{}, mapRepositoryError(err, "user not found")
	}

	username, err := normalizeUsername(req.Username)
	if err != nil {
		return domain.User{}, err
	}
	nickname, err := normalizeNickname(req.Nickname)
	if err != nil {
		return domain.User{}, err
	}

	existing.Username = username
	existing.Nickname = nickname
	existing.Email = normalizeEmail(req.Email)
	existing.Phone = strings.TrimSpace(req.Phone)
	existing.Avatar = strings.TrimSpace(req.Avatar)
	existing.Remarks = strings.TrimSpace(req.Remarks)
	existing.IsAdmin = normalizeIsAdmin(req.IsAdmin, existing.IsAdmin)
	existing.Status = normalizeStatus(req.Status, existing.Status)
	existing.UpdatedAt = time.Now().UTC()

	if strings.TrimSpace(req.Password) != "" {
		existing.Password, err = hashPassword(req.Password)
		if err != nil {
			return domain.User{}, err
		}
	}

	updated, err := s.repo.Update(ctx, existing)
	if err != nil {
		return domain.User{}, mapRepositoryError(err, "user not found")
	}
	return updated, nil
}

func (s *Service) Delete(ctx context.Context, id string) error {
	existing, err := s.resolveUser(ctx, id)
	if err != nil {
		return mapRepositoryError(err, "user not found")
	}

	if err := s.repo.Delete(ctx, existing.ID); err != nil {
		return mapRepositoryError(err, "user not found")
	}
	return nil
}

func (s *Service) GetCurrent(ctx context.Context, subject string) (domain.User, error) {
	user, err := s.resolveUser(ctx, subject)
	if err != nil {
		return domain.User{}, mapRepositoryError(err, "user not found")
	}
	return user, nil
}

func (s *Service) UpdateCurrent(ctx context.Context, subject string, req UpdateCurrentUserRequest) (domain.User, error) {
	if err := s.validator.Struct(req); err != nil {
		return domain.User{}, err
	}

	existing, err := s.resolveUser(ctx, subject)
	if err != nil {
		return domain.User{}, mapRepositoryError(err, "user not found")
	}
	if existing.Status != USER_STATUS_ACTIVE {
		return domain.User{}, &sharedErrors.AppError{
			Code:    sharedErrors.CodeForbidden,
			Message: "user is disabled",
			Status:  403,
		}
	}

	nickname, err := normalizeNickname(req.Nickname)
	if err != nil {
		return domain.User{}, err
	}
	existing.Nickname = nickname
	existing.Email = normalizeEmail(req.Email)
	existing.Phone = strings.TrimSpace(req.Phone)
	existing.Avatar = strings.TrimSpace(req.Avatar)
	existing.Remarks = strings.TrimSpace(req.Remarks)
	existing.UpdatedAt = time.Now().UTC()

	updated, err := s.repo.Update(ctx, existing)
	if err != nil {
		return domain.User{}, mapRepositoryError(err, "user not found")
	}
	return updated, nil
}

func (s *Service) UpdateCurrentPassword(ctx context.Context, subject string, req UpdateCurrentPasswordRequest) error {
	if err := s.validator.Struct(req); err != nil {
		return err
	}
	if req.OldPassword == req.NewPassword {
		return &sharedErrors.AppError{
			Code:    sharedErrors.CodeBadRequest,
			Message: "new password must be different from old password",
			Status:  400,
		}
	}

	existing, err := s.resolveUser(ctx, subject)
	if err != nil {
		return mapRepositoryError(err, "user not found")
	}
	if existing.Status != USER_STATUS_ACTIVE {
		return &sharedErrors.AppError{
			Code:    sharedErrors.CodeForbidden,
			Message: "user is disabled",
			Status:  403,
		}
	}
	if strings.TrimSpace(existing.Password) == "" {
		return &sharedErrors.AppError{
			Code:    sharedErrors.CodeForbidden,
			Message: "password is not configured",
			Status:  403,
		}
	}
	if err := bcrypt.CompareHashAndPassword([]byte(existing.Password), []byte(req.OldPassword)); err != nil {
		return &sharedErrors.AppError{
			Code:    sharedErrors.CodeUnauthorized,
			Message: "old password is incorrect",
			Status:  401,
			Err:     err,
		}
	}

	existing.Password, err = hashPassword(req.NewPassword)
	if err != nil {
		return err
	}
	existing.UpdatedAt = time.Now().UTC()

	_, err = s.repo.Update(ctx, existing)
	if err != nil {
		return mapRepositoryError(err, "user not found")
	}
	return nil
}

func (s *Service) EnableMFA(ctx context.Context, subject string) (EnableMFAResponse, error) {
	user, err := s.resolveUser(ctx, subject)
	if err != nil {
		return EnableMFAResponse{}, mapRepositoryError(err, "user not found")
	}
	key, err := totp.Generate(totp.GenerateOpts{
		Issuer:      "kubeflare",
		AccountName: user.Username,
	})
	if err != nil {
		return EnableMFAResponse{}, err
	}
	encryptedSecret, err := s.encryptSecret(key.Secret())
	if err != nil {
		return EnableMFAResponse{}, err
	}
	user.MFASecret = encryptedSecret
	user.MFAEnabled = false
	user.UpdatedAt = time.Now().UTC()
	if _, err := s.repo.Update(ctx, user); err != nil {
		return EnableMFAResponse{}, mapRepositoryError(err, "user not found")
	}
	return EnableMFAResponse{
		Secret:     key.Secret(),
		OTPAuthURL: key.URL(),
	}, nil
}

func (s *Service) ConfirmMFA(ctx context.Context, subject string, req ConfirmMFARequest) error {
	if err := s.validator.Struct(req); err != nil {
		return err
	}
	user, err := s.resolveUser(ctx, subject)
	if err != nil {
		return mapRepositoryError(err, "user not found")
	}
	mfaSecret, err := s.decryptSecret(user.MFASecret)
	if err != nil {
		return err
	}
	if strings.TrimSpace(mfaSecret) == "" || !totp.Validate(req.OTPCode, mfaSecret) {
		return &sharedErrors.AppError{
			Code:    sharedErrors.CodeUnauthorized,
			Message: "invalid mfa code",
			Status:  401,
		}
	}
	user.MFAEnabled = true
	user.UpdatedAt = time.Now().UTC()
	_, err = s.repo.Update(ctx, user)
	return mapRepositoryError(err, "user not found")
}

func (s *Service) DisableMFA(ctx context.Context, subject string, req DisableMFARequest) error {
	if err := s.validator.Struct(req); err != nil {
		return err
	}
	user, err := s.resolveUser(ctx, subject)
	if err != nil {
		return mapRepositoryError(err, "user not found")
	}
	if err := bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(req.Password)); err != nil {
		return &sharedErrors.AppError{
			Code:    sharedErrors.CodeUnauthorized,
			Message: "invalid password",
			Status:  401,
			Err:     err,
		}
	}
	mfaSecret, err := s.decryptSecret(user.MFASecret)
	if err != nil {
		return err
	}
	if strings.TrimSpace(mfaSecret) == "" || !totp.Validate(req.OTPCode, mfaSecret) {
		return &sharedErrors.AppError{
			Code:    sharedErrors.CodeUnauthorized,
			Message: "invalid mfa code",
			Status:  401,
		}
	}
	user.MFAEnabled = false
	user.MFASecret = ""
	user.UpdatedAt = time.Now().UTC()
	_, err = s.repo.Update(ctx, user)
	return mapRepositoryError(err, "user not found")
}

func mapRepositoryError(err error, notFoundMessage string) error {
	if err == nil {
		return nil
	}

	if err == gorm.ErrRecordNotFound || strings.Contains(strings.ToLower(err.Error()), "not found") {
		return &sharedErrors.AppError{
			Code:    sharedErrors.CodeUserNotFound,
			Message: notFoundMessage,
			Status:  404,
			Err:     err,
		}
	}
	if err == gorm.ErrDuplicatedKey {
		return &sharedErrors.AppError{
			Code:    sharedErrors.CodeUserAlreadyExists,
			Message: "username or email already exists",
			Status:  409,
			Err:     err,
		}
	}

	return err
}

func parseUserID(value string) (int64, error) {
	userID, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil || userID <= 0 {
		return 0, &sharedErrors.AppError{
			Code:    sharedErrors.CodeBadRequest,
			Message: "invalid user id",
			Status:  400,
			Err:     err,
		}
	}
	return userID, nil
}

func (s *Service) resolveUser(ctx context.Context, reference string) (domain.User, error) {
	trimmedReference := strings.TrimSpace(reference)
	if trimmedReference != "" {
		user, err := s.repo.GetByLegacyID(ctx, trimmedReference)
		if err == nil {
			return user, nil
		}
		if err != gorm.ErrRecordNotFound && !strings.Contains(strings.ToLower(err.Error()), "not found") {
			return domain.User{}, err
		}
	}
	userID, err := strconv.ParseInt(trimmedReference, 10, 64)
	if err == nil && userID > 0 {
		return s.repo.Get(ctx, userID)
	}
	if trimmedReference == "" {
		return domain.User{}, &sharedErrors.AppError{
			Code:    sharedErrors.CodeBadRequest,
			Message: "invalid user id",
			Status:  400,
			Err:     err,
		}
	}
	return s.repo.GetByLegacyID(ctx, trimmedReference)
}

func normalizeUsername(value string) (string, error) {
	username := strings.ToLower(strings.TrimSpace(value))
	if !usernamePattern.MatchString(username) {
		return "", &sharedErrors.AppError{
			Code:    sharedErrors.CodeBadRequest,
			Message: "username contains invalid characters",
			Status:  400,
		}
	}
	return username, nil
}

func normalizeEmail(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func normalizeNickname(value string) (string, error) {
	nickname := strings.TrimSpace(value)
	if nickname == "" {
		return "", &sharedErrors.AppError{
			Code:    sharedErrors.CodeBadRequest,
			Message: "nickname is required",
			Status:  400,
		}
	}
	return nickname, nil
}

func normalizeStatus(value *int, defaultValue int) int {
	if value == nil {
		return defaultValue
	}
	return *value
}

func normalizeIsAdmin(value *bool, defaultValue bool) bool {
	if value == nil {
		return defaultValue
	}
	return *value
}

func hashPassword(value string) (string, error) {
	hashed, err := bcrypt.GenerateFromPassword([]byte(value), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(hashed), nil
}

func userSubject(user domain.User) string {
	if user.LegacyID != nil && strings.TrimSpace(*user.LegacyID) != "" {
		return strings.TrimSpace(*user.LegacyID)
	}
	return strconv.FormatInt(user.ID, 10)
}

func loginKey(username string, clientIP string) string {
	return username + "|" + strings.TrimSpace(clientIP)
}

func (s *Service) requiresCaptcha(ctx context.Context, username string, clientIP string) (bool, error) {
	if s.policy.CaptchaFailureTrigger <= 0 {
		return false, nil
	}
	failure, err := s.getLoginFailure(ctx, username, clientIP)
	if err != nil {
		return false, err
	}
	return failure.Count >= s.policy.CaptchaFailureTrigger, nil
}

func (s *Service) loginLock(ctx context.Context, username string, clientIP string) (time.Time, bool, error) {
	failure, err := s.getLoginFailure(ctx, username, clientIP)
	if err != nil {
		return time.Time{}, false, err
	}
	if failure.LockedUntil.After(time.Now().UTC()) {
		return failure.LockedUntil, true, nil
	}
	return time.Time{}, false, nil
}

func (s *Service) recordLoginFailure(ctx context.Context, username string, clientIP string) error {
	key := loginKey(username, clientIP)
	if s.security != nil {
		_, err := s.security.IncrementLoginFailure(ctx, key, time.Now().UTC().Add(s.policy.LockoutDuration), s.policy.MaxFailedAttempts, s.policy.LockoutDuration)
		return err
	}
	return nil
}

func (s *Service) clearLoginFailures(ctx context.Context, username string, clientIP string) {
	if s.security != nil {
		_ = s.security.ClearLoginFailure(ctx, loginKey(username, clientIP))
	}
}

func (s *Service) getLoginFailure(ctx context.Context, username string, clientIP string) (domain.LoginFailure, error) {
	if s.security == nil {
		return domain.LoginFailure{}, nil
	}
	return s.security.GetLoginFailure(ctx, loginKey(username, clientIP))
}

func (s *Service) encryptSecret(value string) (string, error) {
	if s.encryptor == nil {
		return value, nil
	}
	return s.encryptor.Encrypt(value)
}

func (s *Service) decryptSecret(value string) (string, error) {
	if s.encryptor == nil {
		return value, nil
	}
	return s.encryptor.Decrypt(value)
}

func authStateUnavailable(err error) error {
	return &sharedErrors.AppError{
		Code:    sharedErrors.CodeInternal,
		Message: "authentication state is unavailable",
		Status:  503,
		Err:     err,
	}
}
