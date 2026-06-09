package config

import (
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
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
	key, err := hex.DecodeString(cfg.Secrets.EncryptionKey)
	if err != nil || len(key) != 32 {
		return errors.New("secrets.encryption_key is required and must be a 32-byte hex string")
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
	if err := validateAIConfig(cfg.AI); err != nil {
		return err
	}
	if err := validateAgentConfig(cfg.Agent); err != nil {
		return err
	}
	if cfg.Upload.RootDir == "" {
		return errors.New("upload.root_dir is required")
	}
	return nil
}

func validateAIConfig(cfg AIConfig) error {
	if !cfg.Enabled {
		return nil
	}
	if strings.TrimSpace(cfg.DefaultProvider) == "" {
		return errors.New("ai.default_provider is required when ai is enabled")
	}
	if len(cfg.Providers) == 0 {
		return errors.New("ai.providers is required when ai is enabled")
	}

	defaultProvider := strings.TrimSpace(cfg.DefaultProvider)
	if _, ok := cfg.Providers[defaultProvider]; !ok {
		return errors.New("ai.default_provider must reference an entry in ai.providers")
	}

	for name, provider := range cfg.Providers {
		if strings.TrimSpace(name) == "" {
			return errors.New("ai.providers cannot contain an empty provider name")
		}
		providerType := strings.TrimSpace(provider.Type)
		if providerType == "" {
			return errors.New("ai.providers." + name + ".type is required")
		}
		if providerType != "openai_compatible" {
			return errors.New("ai.providers." + name + ".type must be openai_compatible")
		}
		if strings.TrimSpace(provider.BaseURL) == "" {
			return errors.New("ai.providers." + name + ".base_url is required")
		}
		if strings.TrimSpace(provider.APIKey) == "" {
			return errors.New("ai.providers." + name + ".api_key is required")
		}
		if strings.TrimSpace(provider.Model) == "" {
			return errors.New("ai.providers." + name + ".model is required")
		}
		if provider.Timeout < 0 {
			return errors.New("ai.providers." + name + ".timeout must not be negative")
		}
		if provider.StreamTimeout < 0 {
			return errors.New("ai.providers." + name + ".stream_timeout must not be negative")
		}
		if provider.Temperature != nil && (*provider.Temperature < 0 || *provider.Temperature > 2) {
			return errors.New("ai.providers." + name + ".temperature must be between 0 and 2")
		}
		if provider.MaxTokens < 0 {
			return errors.New("ai.providers." + name + ".max_tokens must not be negative")
		}
		if provider.MaxRetries < 0 || provider.MaxRetries > 10 {
			return errors.New("ai.providers." + name + ".max_retries must be between 0 and 10")
		}
		if provider.RetryBackoff < 0 {
			return errors.New("ai.providers." + name + ".retry_backoff must not be negative")
		}
	}

	return nil
}

func validateAgentConfig(cfg AgentConfig) error {
	if cfg.MaxSteps < 0 || cfg.MaxSteps > 20 {
		return errors.New("agent.max_steps must be between 0 and 20")
	}
	if cfg.MaxTokenBudget < 0 {
		return errors.New("agent.max_token_budget must not be negative")
	}
	if cfg.MaxToolErrorsPerStep < 0 || cfg.MaxToolErrorsPerStep > 10 {
		return errors.New("agent.max_tool_errors_per_step must be between 0 and 10")
	}
	if cfg.StepTimeout < 0 {
		return errors.New("agent.step_timeout must not be negative")
	}
	if cfg.MaxConcurrentRunsPerUser < 0 {
		return errors.New("agent.max_concurrent_runs_per_user must not be negative")
	}
	if cfg.MaxConcurrentRuns < 0 {
		return errors.New("agent.max_concurrent_runs must not be negative")
	}
	switch strings.TrimSpace(cfg.ToolChoice) {
	case "", "auto", "none", "required":
	default:
		return errors.New("agent.tool_choice must be one of auto, none, required")
	}
	switch strings.TrimSpace(cfg.Prometheus.Scheme) {
	case "", "http", "https":
	default:
		return errors.New("agent.prometheus.scheme must be http or https")
	}
	if cfg.Prometheus.QueryTimeout < 0 {
		return errors.New("agent.prometheus.query_timeout must not be negative")
	}
	seenSkill := make(map[string]struct{}, len(cfg.Skills))
	for index, skill := range cfg.Skills {
		id := strings.TrimSpace(skill.ID)
		if id == "" {
			return fmt.Errorf("agent.skills[%d].id must not be empty", index)
		}
		if _, dup := seenSkill[id]; dup {
			return fmt.Errorf("agent.skills[%d].id %q is duplicated", index, id)
		}
		seenSkill[id] = struct{}{}
		// 触发词与系统提示同时为空的技能既不会被触发、也无提示效果,视为配置错误。
		if len(skill.Triggers) == 0 && strings.TrimSpace(skill.SystemPrompt) == "" {
			return fmt.Errorf("agent.skills[%d] (%s) must declare triggers or system_prompt", index, id)
		}
	}
	return nil
}
