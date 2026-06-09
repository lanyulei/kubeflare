package config

import "time"

type Config struct {
	Service       ServiceConfig       `koanf:"service"`
	HTTP          HTTPConfig          `koanf:"http"`
	Auth          AuthConfig          `koanf:"auth"`
	KAPI          KAPIConfig          `koanf:"kapi"`
	AI            AIConfig            `koanf:"ai"`
	Agent         AgentConfig         `koanf:"agent"`
	Database      DatabaseConfig      `koanf:"database"`
	Redis         RedisConfig         `koanf:"redis"`
	Secrets       SecretsConfig       `koanf:"secrets"`
	Upload        UploadConfig        `koanf:"upload"`
	Observability ObservabilityConfig `koanf:"observability"`
}

type ServiceConfig struct {
	Name        string `koanf:"name"`
	Environment string `koanf:"environment"`
}

type HTTPConfig struct {
	Address           string        `koanf:"address"`
	ReadTimeout       time.Duration `koanf:"read_timeout"`
	ReadHeaderTimeout time.Duration `koanf:"read_header_timeout"`
	WriteTimeout      time.Duration `koanf:"write_timeout"`
	IdleTimeout       time.Duration `koanf:"idle_timeout"`
	ShutdownTimeout   time.Duration `koanf:"shutdown_timeout"`
	DrainTimeout      time.Duration `koanf:"drain_timeout"`
	APIRequestTimeout time.Duration `koanf:"api_request_timeout"`
	MaxHeaderBytes    int           `koanf:"max_header_bytes"`
	TrustedProxies    []string      `koanf:"trusted_proxies"`
	AllowedOrigins    []string      `koanf:"allowed_origins"`
	AllowCredentials  bool          `koanf:"allow_credentials"`
	AllowHeaders      []string      `koanf:"allow_headers"`
	AllowMethods      []string      `koanf:"allow_methods"`
	EnablePprof       bool          `koanf:"enable_pprof"`
	ReadinessTimeout  time.Duration `koanf:"readiness_timeout"`
}

// KAPIConfig hardens the Kubernetes API proxy / WebSocket exec path.
//
// All fields have safe defaults so a deployment that omits the block still
// gets sensible behaviour:
//   - exec into kube-system / kube-public / kube-node-lease is denied
//   - each user is capped at 5 concurrent upgrade sessions
type KAPIConfig struct {
	// BlockedNamespaces is the set of Kubernetes namespaces in which
	// pods/exec, pods/attach, and pods/portforward upgrade requests are
	// refused. Defaults to the cluster's privileged control-plane
	// namespaces.
	BlockedNamespaces []string `koanf:"blocked_namespaces"`
	// MaxConcurrentSessionsPerUser limits how many simultaneous WebSocket /
	// SPDY upgrade sessions a single authenticated subject may hold open.
	// 0 disables the cap (not recommended).
	MaxConcurrentSessionsPerUser int `koanf:"max_concurrent_sessions_per_user"`
}

type AIConfig struct {
	Enabled         bool   `koanf:"enabled"`
	DefaultProvider string `koanf:"default_provider"`
	// SystemPrompt 是对话助手(非 Agent)的系统提示词,留空表示不注入。
	SystemPrompt string                      `koanf:"system_prompt"`
	Providers    map[string]AIProviderConfig `koanf:"providers"`
}

type AIProviderConfig struct {
	Type          string        `koanf:"type"`
	BaseURL       string        `koanf:"base_url"`
	ChatPath      string        `koanf:"chat_path"`
	APIKey        string        `koanf:"api_key"`
	Model         string        `koanf:"model"`
	Timeout       time.Duration `koanf:"timeout"`
	StreamTimeout time.Duration `koanf:"stream_timeout"`
	Stream        bool          `koanf:"stream"`
	Temperature   *float64      `koanf:"temperature"`
	MaxTokens     int           `koanf:"max_tokens"`
	// MaxRetries 是对可重试错误(网络错误/429/5xx)的最大重试次数,0 表示不重试。
	MaxRetries int `koanf:"max_retries"`
	// RetryBackoff 是首次重试的退避基数,后续按指数增长。
	RetryBackoff time.Duration `koanf:"retry_backoff"`
	// IncludeStreamUsage 控制流式请求是否下发 stream_options.include_usage,
	// nil 表示默认开启;个别非标准 provider 可设为 false 关闭。
	IncludeStreamUsage *bool `koanf:"include_stream_usage"`
}

// AgentConfig 控制 LLM 驱动的 Agent loop 行为。
type AgentConfig struct {
	// MaxSteps 单次运行的最大 think-act 轮数。
	MaxSteps int `koanf:"max_steps"`
	// MaxTokenBudget 单次运行的累计 token 预算上限,0 表示不限。
	MaxTokenBudget int `koanf:"max_token_budget"`
	// MaxToolErrorsPerStep 连续无有效工具调用的最大步数,超过则强制收尾。
	MaxToolErrorsPerStep int `koanf:"max_tool_errors_per_step"`
	// StepTimeout 单步 LLM 调用的超时。
	StepTimeout time.Duration `koanf:"step_timeout"`
	// ToolChoice 工具选择策略:""/auto/none/required。
	ToolChoice string `koanf:"tool_choice"`
	// LLMRouting 控制是否用 LLM 做 Agent 路由分类(失败回退关键词规则),
	// nil 表示默认开启。
	LLMRouting *bool `koanf:"llm_routing"`
	// StreamThink 控制 Agent think 阶段是否流式输出(token 级 thinking 事件),
	// nil 表示默认开启。
	StreamThink *bool `koanf:"stream_think"`
	// MaxConcurrentRunsPerUser 限制单个用户同时执行的 Agent run 数量,防止单个
	// 用户瞬间发起大量 run 打爆 LLM 配额与集群 apiserver。0 表示不限(不推荐)。
	MaxConcurrentRunsPerUser int `koanf:"max_concurrent_runs_per_user"`
	// MaxConcurrentRuns 限制全实例同时执行的 Agent run 总数。0 表示不限(不推荐)。
	MaxConcurrentRuns int `koanf:"max_concurrent_runs"`
	// Prompts 是 agentType -> system prompt 的内联覆盖(最高优先级)。
	Prompts map[string]string `koanf:"prompts"`
	// PromptFiles 是 agentType -> system prompt 文件路径(次优先级)。
	PromptFiles map[string]string `koanf:"prompt_files"`
	// Tools 控制内置工具的治理(启停 / 元数据覆盖),不改代码即可裁剪暴露给
	// LLM 的工具集。
	Tools AgentToolsConfig `koanf:"tools"`
	// Skills 是关键字触发的被动技能(命中后收窄工具集 + 追加系统提示),不引入
	// 额外 LLM 调用。
	Skills []AgentSkillConfig `koanf:"skills"`
	// Prometheus 配置 Agent 如何经 K8s API Server 代理访问集群内 Prometheus。
	Prometheus AgentPrometheusConfig `koanf:"prometheus"`
}

// AgentSkillConfig 声明一个被动技能。
type AgentSkillConfig struct {
	ID          string `koanf:"id"`
	Name        string `koanf:"name"`
	Description string `koanf:"description"`
	// Enabled 启停该技能。nil 表示默认启用。
	Enabled      *bool    `koanf:"enabled"`
	AgentTypes   []string `koanf:"agent_types"`
	Triggers     []string `koanf:"triggers"`
	SystemPrompt string   `koanf:"system_prompt"`
	AllowedTools []string `koanf:"allowed_tools"`
	Hints        []string `koanf:"hints"`
}

// AgentToolsConfig 聚合工具治理配置。
type AgentToolsConfig struct {
	// Overrides 是工具 ID -> 覆盖补丁。仅覆盖显式提供的字段,未列出的工具与
	// 字段保持内置默认。
	Overrides map[string]AgentToolOverride `koanf:"overrides"`
}

// AgentToolOverride 是单个工具的配置覆盖。指针字段区分"未设置"与"设为零值",
// 仅非 nil 的字段参与覆盖。
type AgentToolOverride struct {
	// Enabled 启停该工具。nil 表示不改动(保持启用)。
	Enabled *bool `koanf:"enabled"`
	// Description 覆盖工具描述(影响 LLM 选择)。nil 表示不改动。
	Description *string `koanf:"description"`
	// TimeoutMS 覆盖单次执行超时(毫秒)。nil 或 <=0 表示不改动。
	TimeoutMS *int `koanf:"timeout_ms"`
}

// AgentPrometheusConfig 定位集群内 Prometheus 服务(经 API Server 代理访问)。
type AgentPrometheusConfig struct {
	Namespace    string        `koanf:"namespace"`
	Service      string        `koanf:"service"`
	Port         string        `koanf:"port"`
	Scheme       string        `koanf:"scheme"`
	QueryTimeout time.Duration `koanf:"query_timeout"`
}

type AuthConfig struct {
	SigningKey            string        `koanf:"signing_key"`
	TokenTTL              time.Duration `koanf:"token_ttl"`
	RefreshTokenTTL       time.Duration `koanf:"refresh_token_ttl"`
	MaxFailedAttempts     int           `koanf:"max_failed_attempts"`
	LockoutDuration       time.Duration `koanf:"lockout_duration"`
	CaptchaFailureTrigger int           `koanf:"captcha_failure_trigger"`
	CaptchaTTL            time.Duration `koanf:"captcha_ttl"`
	CookieSecure          bool          `koanf:"cookie_secure"`
	CookieDomain          string        `koanf:"cookie_domain"`
	OIDC                  OIDCConfig    `koanf:"oidc"`
}

type OIDCConfig struct {
	Enabled      bool     `koanf:"enabled"`
	IssuerURL    string   `koanf:"issuer_url"`
	ClientID     string   `koanf:"client_id"`
	ClientSecret string   `koanf:"client_secret"`
	RedirectURL  string   `koanf:"redirect_url"`
	Scopes       []string `koanf:"scopes"`
}

type DatabaseConfig struct {
	Enabled            bool          `koanf:"enabled"`
	DSN                string        `koanf:"dsn"`
	MaxOpenConns       int           `koanf:"max_open_conns"`
	MaxIdleConns       int           `koanf:"max_idle_conns"`
	ConnMaxLifetime    time.Duration `koanf:"conn_max_lifetime"`
	ConnMaxIdleTime    time.Duration `koanf:"conn_max_idle_time"`
	QueryTimeout       time.Duration `koanf:"query_timeout"`
	HealthCheckTimeout time.Duration `koanf:"health_check_timeout"`
}

type RedisConfig struct {
	Enabled            bool          `koanf:"enabled"`
	Address            string        `koanf:"address"`
	Username           string        `koanf:"username"`
	Password           string        `koanf:"password"`
	DB                 int           `koanf:"db"`
	DialTimeout        time.Duration `koanf:"dial_timeout"`
	ReadTimeout        time.Duration `koanf:"read_timeout"`
	WriteTimeout       time.Duration `koanf:"write_timeout"`
	PoolTimeout        time.Duration `koanf:"pool_timeout"`
	MinIdleConns       int           `koanf:"min_idle_conns"`
	MaxIdleConns       int           `koanf:"max_idle_conns"`
	PoolSize           int           `koanf:"pool_size"`
	CacheTTL           time.Duration `koanf:"cache_ttl"`
	HealthCheckTimeout time.Duration `koanf:"health_check_timeout"`
}

type SecretsConfig struct {
	EncryptionKey string `koanf:"encryption_key"`
}

type UploadConfig struct {
	RootDir string `koanf:"root_dir"`
}

type ObservabilityConfig struct {
	LogLevel  string        `koanf:"log_level"`
	LogFormat string        `koanf:"log_format"`
	Tracing   TracingConfig `koanf:"tracing"`
}

type TracingConfig struct {
	Enabled bool `koanf:"enabled"`
}

func Default() Config {
	return Config{
		Service: ServiceConfig{
			Name:        "kubeflare",
			Environment: "local",
		},
		HTTP: HTTPConfig{
			Address:           ":8080",
			ReadTimeout:       30 * time.Second,
			ReadHeaderTimeout: 10 * time.Second,
			WriteTimeout:      0,
			IdleTimeout:       90 * time.Second,
			ShutdownTimeout:   20 * time.Second,
			DrainTimeout:      5 * time.Second,
			APIRequestTimeout: 10 * time.Second,
			MaxHeaderBytes:    1 << 20,
			AllowedOrigins:    []string{"*"},
			AllowHeaders: []string{
				"Authorization",
				"Content-Type",
				"X-Request-Id",
				"X-Kubeflare-CSRF",
			},
			AllowMethods:     []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
			ReadinessTimeout: 2 * time.Second,
		},
		KAPI: KAPIConfig{
			BlockedNamespaces: []string{
				"kube-system",
				"kube-public",
				"kube-node-lease",
			},
			MaxConcurrentSessionsPerUser: 5,
		},
		AI: AIConfig{
			Enabled:   false,
			Providers: map[string]AIProviderConfig{},
		},
		Agent: AgentConfig{
			MaxSteps:                 6,
			MaxTokenBudget:           60000,
			MaxToolErrorsPerStep:     3,
			StepTimeout:              60 * time.Second,
			ToolChoice:               "auto",
			MaxConcurrentRunsPerUser: 3,
			MaxConcurrentRuns:        50,
			Prometheus: AgentPrometheusConfig{
				Namespace:    "monitoring",
				Service:      "prometheus-kube-prometheus-prometheus",
				Port:         "9090",
				Scheme:       "http",
				QueryTimeout: 15 * time.Second,
			},
		},
		Auth: AuthConfig{
			TokenTTL:              24 * time.Hour,
			RefreshTokenTTL:       7 * 24 * time.Hour,
			MaxFailedAttempts:     5,
			LockoutDuration:       15 * time.Minute,
			CaptchaFailureTrigger: 3,
			CaptchaTTL:            5 * time.Minute,
			OIDC: OIDCConfig{
				Scopes: []string{"openid", "profile", "email"},
			},
		},
		Database: DatabaseConfig{
			MaxOpenConns:       40,
			MaxIdleConns:       20,
			ConnMaxLifetime:    30 * time.Minute,
			ConnMaxIdleTime:    10 * time.Minute,
			QueryTimeout:       5 * time.Second,
			HealthCheckTimeout: 2 * time.Second,
		},
		Redis: RedisConfig{
			Address:            "127.0.0.1:6379",
			DialTimeout:        2 * time.Second,
			ReadTimeout:        2 * time.Second,
			WriteTimeout:       2 * time.Second,
			PoolTimeout:        4 * time.Second,
			MinIdleConns:       4,
			MaxIdleConns:       16,
			PoolSize:           32,
			CacheTTL:           2 * time.Minute,
			HealthCheckTimeout: 2 * time.Second,
		},
		Upload: UploadConfig{
			RootDir: "data/uploads",
		},
		Observability: ObservabilityConfig{
			LogLevel:  "info",
			LogFormat: "text",
			Tracing: TracingConfig{
				Enabled: false,
			},
		},
	}
}
