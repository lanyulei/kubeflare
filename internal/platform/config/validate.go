package config

import (
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

func Validate(cfg Config) error {
	production := strings.EqualFold(strings.TrimSpace(cfg.Service.Environment), "production")
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
	// 生产环境严禁通配 Origin:exec/attach 代理的跨站防护完全依赖 Origin 白名单
	// (升级请求是 GET,CSRF 被有意跳过),通配会让任何站点借管理员 Cookie 跨站
	// 打开 Pod root shell(CSWSH)。本地/开发保留 "*" 以免阻断联调。
	if production {
		for _, origin := range cfg.HTTP.AllowedOrigins {
			if strings.TrimSpace(origin) == "*" {
				return errors.New("http.allowed_origins cannot contain * in production; configure explicit origins")
			}
		}
	}
	if err := validateDatabaseConfig(cfg.Database); err != nil {
		return err
	}
	if production && !cfg.Redis.Enabled {
		return errors.New("redis.enabled is required in production for distributed coordination")
	}
	if err := validateRedisConfig(cfg.Redis); err != nil {
		return err
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
		// 仅校验 scheme 合法(http/https)且可解析;不封禁内网 IP —— on-prem 内网
		// LLM 端点是合法部署形态,封禁会破坏既有环境。
		if parsed, perr := url.Parse(strings.TrimSpace(provider.BaseURL)); perr != nil {
			return errors.New("ai.providers." + name + ".base_url is not a valid URL")
		} else if parsed.Scheme != "http" && parsed.Scheme != "https" {
			return errors.New("ai.providers." + name + ".base_url scheme must be http or https")
		} else if parsed.Host == "" {
			return errors.New("ai.providers." + name + ".base_url must include a host")
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

	// fallback_providers 必须全部引用已配置的 provider,且不应是默认 provider 自身。
	for _, name := range cfg.FallbackProviders {
		trimmed := strings.TrimSpace(name)
		if trimmed == "" {
			return errors.New("ai.fallback_providers must not contain empty entries")
		}
		if _, ok := cfg.Providers[trimmed]; !ok {
			return errors.New("ai.fallback_providers." + trimmed + " must reference an entry in ai.providers")
		}
		if trimmed == defaultProvider {
			return errors.New("ai.fallback_providers must not include the default provider")
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
	if cfg.MaxReflectionSteps < 0 || cfg.MaxReflectionSteps > 5 {
		return errors.New("agent.max_reflection_steps must be between 0 and 5")
	}
	if cfg.MaxReflections < 0 || cfg.MaxReflections > 3 {
		return errors.New("agent.max_reflections must be between 0 and 3")
	}
	if cfg.CaseFewShotLimit < 0 || cfg.CaseFewShotLimit > 8 {
		return errors.New("agent.case_few_shot_limit must be between 0 and 8")
	}
	if cfg.RouteFewShotLimit < 0 || cfg.RouteFewShotLimit > 32 {
		return errors.New("agent.route_few_shot_limit must be between 0 and 32")
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
	for toolID, override := range cfg.Tools.Overrides {
		if override.ObserveMaxChars != nil && (*override.ObserveMaxChars < 256 || *override.ObserveMaxChars > 16000) {
			return fmt.Errorf("agent.tools.overrides[%s].observe_max_chars must be between 256 and 16000", toolID)
		}
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
	if err := validateAgentMCPServers(cfg.McpServers); err != nil {
		return err
	}
	return nil
}

// validateAgentMCPServers 校验 MCP server 声明:名称唯一、transport 合法、按 transport
// 校验必填项(stdio 需 command,http 需合法 url),超时 / 并发不为负。
func validateAgentMCPServers(servers []AgentMCPServerConfig) error {
	seen := make(map[string]struct{}, len(servers))
	for index, server := range servers {
		name := strings.TrimSpace(server.Name)
		if name == "" {
			return fmt.Errorf("agent.mcp_servers[%d].name must not be empty", index)
		}
		if _, dup := seen[name]; dup {
			return fmt.Errorf("agent.mcp_servers[%d].name %q is duplicated", index, name)
		}
		seen[name] = struct{}{}
		// 名称用作工具 ID 段(mcp.<name>.<tool>),禁止点号避免破坏 ID 解析。
		if strings.ContainsRune(name, '.') {
			return fmt.Errorf("agent.mcp_servers[%d].name %q must not contain '.'", index, name)
		}
		switch strings.TrimSpace(server.Transport) {
		case "stdio":
			if len(server.Command) == 0 || strings.TrimSpace(server.Command[0]) == "" {
				return fmt.Errorf("agent.mcp_servers[%d] (%s): command is required for stdio transport", index, name)
			}
		case "http":
			endpoint := strings.TrimSpace(server.URL)
			if endpoint == "" {
				return fmt.Errorf("agent.mcp_servers[%d] (%s): url is required for http transport", index, name)
			}
			if parsed, perr := url.Parse(endpoint); perr != nil {
				return fmt.Errorf("agent.mcp_servers[%d] (%s): url is not a valid URL", index, name)
			} else if parsed.Scheme != "http" && parsed.Scheme != "https" {
				return fmt.Errorf("agent.mcp_servers[%d] (%s): url scheme must be http or https", index, name)
			} else if parsed.Host == "" {
				return fmt.Errorf("agent.mcp_servers[%d] (%s): url must include a host", index, name)
			}
		default:
			return fmt.Errorf("agent.mcp_servers[%d] (%s): transport must be one of stdio, http", index, name)
		}
		if server.ConnectTimeout < 0 || server.ListTimeout < 0 || server.CallTimeout < 0 || server.HealthInterval < 0 {
			return fmt.Errorf("agent.mcp_servers[%d] (%s): timeouts must not be negative", index, name)
		}
		if server.MaxConcurrency < 0 {
			return fmt.Errorf("agent.mcp_servers[%d] (%s): max_concurrency must not be negative", index, name)
		}
	}
	return nil
}

// validateDatabaseConfig 防呆校验连接池参数,避免错误配置静默降级(如 0=无限连接、
// 空闲数大于最大数导致 GORM 内部纠偏、QueryTimeout=0 使所有查询失去超时)。
func validateDatabaseConfig(cfg DatabaseConfig) error {
	if !cfg.Enabled {
		return nil
	}
	if cfg.DSN == "" {
		return errors.New("database.dsn is required when database is enabled")
	}
	if cfg.MaxOpenConns <= 0 {
		return errors.New("database.max_open_conns must be positive when database is enabled")
	}
	if cfg.MaxIdleConns < 0 {
		return errors.New("database.max_idle_conns must not be negative")
	}
	if cfg.MaxIdleConns > cfg.MaxOpenConns {
		return errors.New("database.max_idle_conns must not exceed database.max_open_conns")
	}
	if cfg.QueryTimeout <= 0 {
		return errors.New("database.query_timeout must be positive to bound query execution")
	}
	if cfg.HealthCheckTimeout <= 0 {
		return errors.New("database.health_check_timeout must be positive")
	}
	return nil
}

// validateRedisConfig 确保 Redis 超时为正,避免 0 值导致用已过期 context 发起 ping
// 而启动失败,或连接池参数互相矛盾。
func validateRedisConfig(cfg RedisConfig) error {
	if !cfg.Enabled {
		return nil
	}
	if cfg.Address == "" {
		return errors.New("redis.address is required when redis is enabled")
	}
	if cfg.DialTimeout <= 0 || cfg.ReadTimeout <= 0 || cfg.WriteTimeout <= 0 {
		return errors.New("redis dial_timeout, read_timeout and write_timeout must be positive")
	}
	if cfg.HealthCheckTimeout <= 0 {
		return errors.New("redis.health_check_timeout must be positive")
	}
	if cfg.PoolSize < 0 || cfg.MinIdleConns < 0 || cfg.MaxIdleConns < 0 {
		return errors.New("redis pool sizes must not be negative")
	}
	if cfg.MaxIdleConns > 0 && cfg.MinIdleConns > cfg.MaxIdleConns {
		return errors.New("redis.min_idle_conns must not exceed redis.max_idle_conns")
	}
	return nil
}
