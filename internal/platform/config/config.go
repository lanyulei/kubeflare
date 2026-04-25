package config

import "time"

type Config struct {
	Service       ServiceConfig       `koanf:"service"`
	HTTP          HTTPConfig          `koanf:"http"`
	Auth          AuthConfig          `koanf:"auth"`
	Database      DatabaseConfig      `koanf:"database"`
	Redis         RedisConfig         `koanf:"redis"`
	Proxy         ProxyConfig         `koanf:"proxy"`
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

type ProxyConfig struct {
	DefaultClusterID      string        `koanf:"default_cluster_id"`
	ClusterCacheTTL       time.Duration `koanf:"cluster_cache_ttl"`
	EncryptionKey         string        `koanf:"encryption_key"`
	DialTimeout           time.Duration `koanf:"dial_timeout"`
	TLSHandshakeTimeout   time.Duration `koanf:"tls_handshake_timeout"`
	ResponseHeaderTimeout time.Duration `koanf:"response_header_timeout"`
	IdleConnTimeout       time.Duration `koanf:"idle_conn_timeout"`
	MaxIdleConns          int           `koanf:"max_idle_conns"`
	MaxIdleConnsPerHost   int           `koanf:"max_idle_conns_per_host"`
	MaxConnsPerHost       int           `koanf:"max_conns_per_host"`
	FlushInterval         time.Duration `koanf:"flush_interval"`
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
				"X-Kubeflare-Cluster",
				"X-Kubeflare-CSRF",
			},
			AllowMethods:     []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
			ReadinessTimeout: 2 * time.Second,
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
		Proxy: ProxyConfig{
			ClusterCacheTTL:       30 * time.Second,
			EncryptionKey:         "",
			DialTimeout:           5 * time.Second,
			TLSHandshakeTimeout:   5 * time.Second,
			ResponseHeaderTimeout: 30 * time.Second,
			IdleConnTimeout:       90 * time.Second,
			MaxIdleConns:          256,
			MaxIdleConnsPerHost:   64,
			MaxConnsPerHost:       256,
			FlushInterval:         200 * time.Millisecond,
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
