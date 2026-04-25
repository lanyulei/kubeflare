package application

type LoginRequest struct {
	Username    string `json:"username" validate:"required,min=3,max=64"`
	Password    string `json:"password" validate:"required,min=6,max=72"`
	CaptchaID   string `json:"captcha_id" validate:"omitempty,max=128"`
	CaptchaCode string `json:"captcha_code" validate:"omitempty,max=32"`
	OTPCode     string `json:"otp_code" validate:"omitempty,len=6,numeric"`
}

type LoginResponse struct {
	AccessToken           string `json:"access_token"`
	RefreshToken          string `json:"refresh_token,omitempty"`
	TokenType             string `json:"token_type"`
	ExpiresIn             int64  `json:"expires_in,omitempty"`
	RefreshTokenExpiresIn int64  `json:"refresh_token_expires_in,omitempty"`
	RequiresCaptcha       bool   `json:"requires_captcha,omitempty"`
	RequiresMFA           bool   `json:"requires_mfa,omitempty"`
	SessionID             string `json:"session_id,omitempty"`
	User                  any    `json:"user,omitempty"`
}

type RefreshTokenRequest struct {
	RefreshToken string `json:"refresh_token" validate:"required"`
}

type LogoutRequest struct {
	RefreshToken string `json:"refresh_token" validate:"omitempty"`
	AllSessions  bool   `json:"all_sessions"`
}

type CaptchaChallengeResponse struct {
	ID        string `json:"id"`
	ImageURL  string `json:"image_url"`
	ExpiresIn int64  `json:"expires_in"`
}

type EnableMFAResponse struct {
	Secret     string `json:"secret"`
	OTPAuthURL string `json:"otp_auth_url"`
}

type ConfirmMFARequest struct {
	OTPCode string `json:"otp_code" validate:"required,len=6,numeric"`
}

type DisableMFARequest struct {
	Password string `json:"password" validate:"required,min=6,max=72"`
	OTPCode  string `json:"otp_code" validate:"required,len=6,numeric"`
}

type CreateUserRequest struct {
	Username string `json:"username" validate:"required,min=3,max=64"`
	Nickname string `json:"nickname" validate:"required,min=1,max=64"`
	Password string `json:"password" validate:"required,min=6,max=72"`
	Email    string `json:"email" validate:"omitempty,email,max=255"`
	Phone    string `json:"phone" validate:"omitempty,max=32"`
	Avatar   string `json:"avatar" validate:"omitempty,max=512"`
	Remarks  string `json:"remarks" validate:"omitempty,max=512"`
	Status   *int   `json:"status" validate:"omitempty,oneof=0 1"`
}

type UpdateUserRequest struct {
	Username string `json:"username" validate:"required,min=3,max=64"`
	Nickname string `json:"nickname" validate:"required,min=1,max=64"`
	Password string `json:"password" validate:"omitempty,min=6,max=72"`
	Email    string `json:"email" validate:"omitempty,email,max=255"`
	Phone    string `json:"phone" validate:"omitempty,max=32"`
	Avatar   string `json:"avatar" validate:"omitempty,max=512"`
	Remarks  string `json:"remarks" validate:"omitempty,max=512"`
	Status   *int   `json:"status" validate:"omitempty,oneof=0 1"`
}

type UpdateCurrentUserRequest struct {
	Nickname string `json:"nickname" validate:"required,min=1,max=64"`
	Email    string `json:"email" validate:"omitempty,email,max=255"`
	Phone    string `json:"phone" validate:"omitempty,max=32"`
	Avatar   string `json:"avatar" validate:"omitempty,max=512"`
	Remarks  string `json:"remarks" validate:"omitempty,max=512"`
}

type UpdateCurrentPasswordRequest struct {
	OldPassword string `json:"old_password" validate:"required,min=6,max=72"`
	NewPassword string `json:"new_password" validate:"required,min=6,max=72"`
}
