package config

import (
	"errors"
)

func Validate(cfg Config) error {
	if cfg.Service.Name == "" {
		return errors.New("service.name is required")
	}
	if cfg.HTTP.Address == "" {
		return errors.New("http.address is required")
	}
	if cfg.HTTP.MaxHeaderBytes <= 0 {
		return errors.New("http.max_header_bytes must be positive")
	}
	if cfg.HTTP.AllowCredentials {
		for _, origin := range cfg.HTTP.AllowedOrigins {
			if origin == "*" {
				return errors.New("http.allowed_origins cannot contain * when http.allow_credentials is true")
			}
		}
	}
	if cfg.Database.Enabled && cfg.Database.DSN == "" {
		return errors.New("database.dsn is required when database is enabled")
	}
	if cfg.Redis.Enabled && cfg.Redis.Address == "" {
		return errors.New("redis.address is required when redis is enabled")
	}
	if cfg.Auth.TokenTTL < 0 {
		return errors.New("auth.token_ttl must not be negative")
	}
	if cfg.Auth.RefreshTokenTTL < 0 {
		return errors.New("auth.refresh_token_ttl must not be negative")
	}
	if cfg.Auth.MaxFailedAttempts < 0 {
		return errors.New("auth.max_failed_attempts must not be negative")
	}
	if cfg.Auth.LockoutDuration < 0 {
		return errors.New("auth.lockout_duration must not be negative")
	}
	if cfg.Auth.CaptchaFailureTrigger < 0 {
		return errors.New("auth.captcha_failure_trigger must not be negative")
	}
	if cfg.Auth.CaptchaTTL < 0 {
		return errors.New("auth.captcha_ttl must not be negative")
	}
	if cfg.Auth.SigningKey == "" {
		return errors.New("auth.signing_key is required")
	}
	if cfg.Auth.OIDC.Enabled {
		if cfg.Auth.OIDC.IssuerURL == "" || cfg.Auth.OIDC.ClientID == "" || cfg.Auth.OIDC.ClientSecret == "" || cfg.Auth.OIDC.RedirectURL == "" {
			return errors.New("auth.oidc issuer_url, client_id, client_secret, and redirect_url are required when oidc is enabled")
		}
	}
	if cfg.Upload.RootDir == "" {
		return errors.New("upload.root_dir is required")
	}
	return nil
}
