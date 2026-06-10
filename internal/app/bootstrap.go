package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/dchest/captcha"
	"github.com/gin-gonic/gin"
	"github.com/go-playground/validator/v10"
	"go.opentelemetry.io/contrib/instrumentation/github.com/gin-gonic/gin/otelgin"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	agentapplication "github.com/lanyulei/kubeflare/internal/module/agent/application"
	agentdomain "github.com/lanyulei/kubeflare/internal/module/agent/domain"
	agentkubeclient "github.com/lanyulei/kubeflare/internal/module/agent/infrastructure/kubeclient"
	agentkubernetes "github.com/lanyulei/kubeflare/internal/module/agent/infrastructure/kubernetes"
	agentpostgres "github.com/lanyulei/kubeflare/internal/module/agent/infrastructure/postgres"
	agentprometheus "github.com/lanyulei/kubeflare/internal/module/agent/infrastructure/prometheus"
	agenthttp "github.com/lanyulei/kubeflare/internal/module/agent/interface/http"
	aiapplication "github.com/lanyulei/kubeflare/internal/module/ai/application"
	aillm "github.com/lanyulei/kubeflare/internal/module/ai/infrastructure/llm"
	aipostgres "github.com/lanyulei/kubeflare/internal/module/ai/infrastructure/postgres"
	aihttp "github.com/lanyulei/kubeflare/internal/module/ai/interface/http"
	clusterapplication "github.com/lanyulei/kubeflare/internal/module/cluster/application"
	clusterkubernetes "github.com/lanyulei/kubeflare/internal/module/cluster/infrastructure/kubernetes"
	clusterpostgres "github.com/lanyulei/kubeflare/internal/module/cluster/infrastructure/postgres"
	clusterhttp "github.com/lanyulei/kubeflare/internal/module/cluster/interface/http"
	iamapplication "github.com/lanyulei/kubeflare/internal/module/iam/application"
	iamdomain "github.com/lanyulei/kubeflare/internal/module/iam/domain"
	iamauthstate "github.com/lanyulei/kubeflare/internal/module/iam/infrastructure/authstate"
	iamcaptcha "github.com/lanyulei/kubeflare/internal/module/iam/infrastructure/captchastore"
	iampostgres "github.com/lanyulei/kubeflare/internal/module/iam/infrastructure/postgres"
	iamredis "github.com/lanyulei/kubeflare/internal/module/iam/infrastructure/redis"
	iamhttp "github.com/lanyulei/kubeflare/internal/module/iam/interface/http"
	uploadapplication "github.com/lanyulei/kubeflare/internal/module/upload/application"
	uploadlocal "github.com/lanyulei/kubeflare/internal/module/upload/infrastructure/local"
	uploadhttp "github.com/lanyulei/kubeflare/internal/module/upload/interface/http"
	"github.com/lanyulei/kubeflare/internal/platform/cache"
	"github.com/lanyulei/kubeflare/internal/platform/config"
	"github.com/lanyulei/kubeflare/internal/platform/db"
	"github.com/lanyulei/kubeflare/internal/platform/httpx"
	platformllm "github.com/lanyulei/kubeflare/internal/platform/llm"
	logpkg "github.com/lanyulei/kubeflare/internal/platform/log"
	"github.com/lanyulei/kubeflare/internal/platform/metrics"
	"github.com/lanyulei/kubeflare/internal/platform/secrets"
	tracepkg "github.com/lanyulei/kubeflare/internal/platform/trace"
	"github.com/lanyulei/kubeflare/internal/shared/health"
	"github.com/lanyulei/kubeflare/internal/shared/middleware"
	"github.com/lanyulei/kubeflare/internal/shared/response"
	"github.com/lanyulei/kubeflare/internal/shared/safego"
)

func New(ctx context.Context, cfg config.Config) (*App, error) {
	logger := logpkg.New(cfg.Observability)

	traceShutdown, err := tracepkg.Setup(ctx, cfg.Service.Name, cfg.Observability.Tracing)
	if err != nil {
		return nil, err
	}

	validator := validator.New()

	metricsRegistry, err := metrics.NewRegistry()
	if err != nil {
		return nil, err
	}

	gormDB, err := db.OpenPostgres(cfg.Database)
	if err != nil {
		return nil, err
	}

	redisClient, err := cache.NewRedisClient(cfg.Redis)
	if err != nil {
		return nil, err
	}

	encryptionKey := strings.TrimSpace(cfg.Secrets.EncryptionKey)
	if encryptionKey == "" {
		return nil, errors.New("secrets.encryption_key is required")
	}
	encryptor, err := secrets.NewAESGCMEncryptor(encryptionKey)
	if err != nil {
		return nil, err
	}

	authSigningKey := strings.TrimSpace(cfg.Auth.SigningKey)

	userRepo := iampostgres.NewUserRepository(gormDB, cfg.Database.QueryTimeout)
	authStateRepo := iampostgres.NewAuthStateRepository(gormDB, cfg.Database.QueryTimeout)
	captchaStore := iamcaptcha.NewStore(redisClient, gormDB, cfg.Auth.CaptchaTTL, cfg.Database.QueryTimeout)
	captcha.SetCustomStore(captchaStore)
	var authStateStore middleware.TokenStateStore
	if redisClient != nil && gormDB != nil {
		authStateStore = iamauthstate.NewFailoverStore(iamredis.NewAuthStateStore(redisClient), authStateRepo)
	} else if gormDB != nil {
		authStateStore = authStateRepo
	} else if redisClient != nil {
		authStateStore = iamredis.NewAuthStateStore(redisClient)
	}
	if authStateStore == nil {
		// 无持久化存储时,会话撤销/刷新轮换全部静默失效(登出无效、token 无法
		// 吊销)。生产环境视为严重风险,启动即拒绝;其余环境降级为告警。
		if cfg.Service.Environment == "production" {
			return nil, errors.New("auth state store is required in production (configure database or redis) to enable session revocation")
		}
		logger.Warn("auth state store is not configured; session revocation and refresh-token rotation are disabled")
	}
	uploadRepo := uploadlocal.NewFileRepository(cfg.Upload.RootDir)
	clusterRepo := clusterpostgres.NewClusterRepository(gormDB, cfg.Database.QueryTimeout)
	clusterInspector := clusterkubernetes.NewInspector(cfg.Database.QueryTimeout)
	aiRepo := aipostgres.NewChatRepository(gormDB, cfg.Database.QueryTimeout)
	agentRepo := agentpostgres.NewAgentRepository(gormDB, cfg.Database.QueryTimeout)

	tokenManager := middleware.NewSignedTokenManagerWithOptions(authSigningKey, cfg.Auth.TokenTTL, cfg.Auth.RefreshTokenTTL, authStateStore)
	authenticator := middleware.NewSignedTokenAuthenticator(tokenManager, userPrincipalResolver{repo: userRepo})
	iamService := iamapplication.NewService(userRepo, validator, tokenManager)
	securityStateStore, _ := authStateStore.(iamdomain.SecurityStateStore)
	iamService.SetSecurityStateStore(securityStateStore)
	iamService.SetSecretEncryptor(encryptor)
	iamService.SetAuthPolicy(iamapplication.AuthPolicy{
		CaptchaTTL:            cfg.Auth.CaptchaTTL,
		CaptchaFailureTrigger: cfg.Auth.CaptchaFailureTrigger,
		MaxFailedAttempts:     cfg.Auth.MaxFailedAttempts,
		LockoutDuration:       cfg.Auth.LockoutDuration,
	})
	var oidcService *iamapplication.OIDCService
	if cfg.Auth.OIDC.Enabled {
		oidcService, err = iamapplication.NewOIDCService(ctx, iamapplication.OIDCConfig{
			IssuerURL:    cfg.Auth.OIDC.IssuerURL,
			ClientID:     cfg.Auth.OIDC.ClientID,
			ClientSecret: cfg.Auth.OIDC.ClientSecret,
			RedirectURL:  cfg.Auth.OIDC.RedirectURL,
			Scopes:       cfg.Auth.OIDC.Scopes,
		}, userRepo, tokenManager, securityStateStore)
		if err != nil {
			return nil, err
		}
	}
	aiGenerator, err := newAIGenerator(cfg.AI, encryptor)
	if err != nil {
		return nil, err
	}
	uploadService := uploadapplication.NewService(uploadRepo, validator, "/api/v1/upload")
	clusterService := clusterapplication.NewService(clusterRepo, validator, encryptor, clusterInspector)
	aiService := aiapplication.NewService(aiRepo, validator, aiGenerator, strings.TrimSpace(cfg.AI.SystemPrompt), logger)
	agentClientFactory := agentkubeclient.NewFactory(clusterService, 0)
	// 集群 kubeconfig 更新/删除后失效缓存的 clientset,避免 TTL 窗口内沿用旧凭证。
	clusterService.RegisterCacheInvalidator(agentClientFactory.Invalidate)
	agentKubernetesExecutor := agentkubernetes.NewToolExecutor(agentClientFactory)
	agentPrometheusExecutor := agentprometheus.NewToolExecutor(agentClientFactory, agentprometheus.Config{
		Namespace:    cfg.Agent.Prometheus.Namespace,
		Service:      cfg.Agent.Prometheus.Service,
		Port:         cfg.Agent.Prometheus.Port,
		Scheme:       cfg.Agent.Prometheus.Scheme,
		QueryTimeout: cfg.Agent.Prometheus.QueryTimeout,
	})
	agentGenerator := aiGenerator
	if agentGenerator == nil {
		agentGenerator = aiapplication.NewUnavailableAssistantGenerator()
	}
	agentService := agentapplication.NewService(agentapplication.Options{
		Repo:              agentRepo,
		Validator:         validator,
		ChatRepo:          aiRepo,
		AssistantStreamer: aiService,
		ToolExecutors: []agentapplication.SourceToolExecutor{
			agentKubernetesExecutor,
			agentPrometheusExecutor,
		},
		Generator: agentGenerator,
		Loop: agentapplication.LoopConfig{
			MaxSteps:                 cfg.Agent.MaxSteps,
			MaxTokenBudget:           cfg.Agent.MaxTokenBudget,
			MaxToolErrorsPerStep:     cfg.Agent.MaxToolErrorsPerStep,
			StepTimeout:              cfg.Agent.StepTimeout,
			ToolChoice:               cfg.Agent.ToolChoice,
			LLMRouting:               cfg.Agent.LLMRouting,
			StreamThink:              cfg.Agent.StreamThink,
			Planning:                 cfg.Agent.Planning,
			Reflection:               cfg.Agent.Reflection,
			MaxReflectionSteps:       cfg.Agent.MaxReflectionSteps,
			MaxReflections:           cfg.Agent.MaxReflections,
			ObserveCompression:       cfg.Agent.ObserveCompression,
			CaseLibrary:              cfg.Agent.CaseLibrary,
			CaseFewShotLimit:         cfg.Agent.CaseFewShotLimit,
			RouteLearning:            cfg.Agent.RouteLearning,
			RouteFewShotLimit:        cfg.Agent.RouteFewShotLimit,
			MaxConcurrentRunsPerUser: cfg.Agent.MaxConcurrentRunsPerUser,
			MaxConcurrentRuns:        cfg.Agent.MaxConcurrentRuns,
		},
		SystemPrompts: resolveAgentPrompts(cfg.Agent, logger),
		ToolOverrides: resolveAgentToolOverrides(cfg.Agent),
		Skills:        resolveAgentSkills(cfg.Agent),
		Logger:        logger,
	})
	kapiHandler := newKAPIHandler(clusterService, authenticator, cfg.HTTP.APIRequestTimeout, clusterkubernetes.SecurityOptions{
		AllowedOrigins:               cfg.HTTP.AllowedOrigins,
		BlockedNamespaces:            cfg.KAPI.BlockedNamespaces,
		MaxConcurrentSessionsPerUser: cfg.KAPI.MaxConcurrentSessionsPerUser,
	})

	apiHandler, err := newAPIHandler(cfg, logger, authenticator, iamService, oidcService, uploadService, clusterService, aiService, agentService)
	if err != nil {
		return nil, err
	}
	authCleanupCtx, stopAuthCleanup := context.WithCancel(context.Background())
	safego.Go(logger, "auth state cleanup", func() { runAuthStateCleanup(authCleanupCtx, logger, authStateRepo, captchaStore) })
	safego.Go(logger, "ai state recovery", func() { runAIStateRecovery(authCleanupCtx, logger, aiService, agentService) })

	healthManager := health.NewManager(
		cfg.HTTP.ReadinessTimeout,
		health.FuncChecker{
			CheckName: "postgres",
			CheckFunc: func(ctx context.Context) error {
				pingCtx, cancel := db.WithTimeout(ctx, cfg.Database.HealthCheckTimeout)
				defer cancel()
				return db.Ping(pingCtx, gormDB)
			},
		},
		health.FuncChecker{
			CheckName: "redis",
			CheckFunc: func(ctx context.Context) error {
				if redisClient == nil {
					return nil
				}
				pingCtx, cancel := context.WithTimeout(ctx, cfg.Redis.HealthCheckTimeout)
				defer cancel()
				return redisClient.Ping(pingCtx).Err()
			},
		},
	)

	var pprofHandler http.Handler
	if cfg.HTTP.EnablePprof {
		pprofHandler = NewPprofHandler()
	}

	rootHandler := NewRootHandler(RootHandlerOptions{
		LivezHandler:   healthManager.LiveHandler(),
		ReadyzHandler:  healthManager.ReadyHandler(),
		MetricsHandler: metricsRegistry.Handler(),
		PprofHandler:   pprofHandler,
		APIHandler:     apiHandler,
		KAPIHandler:    kapiHandler,
	})

	rootHandler = metrics.InstrumentHTTP(metricsRegistry, rootHandler)
	rootHandler = middleware.SecurityHeadersHTTP(rootHandler)
	rootHandler = middleware.CORSHTTP(toCORSConfig(cfg), rootHandler)
	rootHandler = middleware.AccessLogHTTP(logger, rootHandler)
	rootHandler = middleware.RequestIDHTTP(rootHandler)
	rootHandler = middleware.RecoverHTTP(logger, rootHandler)
	rootHandler = otelhttp.NewHandler(rootHandler, cfg.Service.Name)

	server := httpx.NewServer(cfg.HTTP, rootHandler)

	return &App{
		Config:     cfg,
		Logger:     logger,
		Server:     server,
		Health:     healthManager,
		drainDelay: cfg.HTTP.DrainTimeout,
		shutdowners: []func(context.Context) error{
			func(context.Context) error {
				stopAuthCleanup()
				return nil
			},
			traceShutdown,
			func(context.Context) error { return cache.Close(redisClient) },
			func(context.Context) error { return db.Close(gormDB) },
		},
	}, nil
}

func newKAPIHandler(clusterService *clusterapplication.Service, authenticator middleware.Authenticator, timeout time.Duration, security clusterkubernetes.SecurityOptions) http.Handler {
	proxy := clusterkubernetes.NewProxyHandlerWithSecurity(clusterService, timeout, security)
	// 集群 kubeconfig 更新/删除后失效代理缓存的 transport,避免继续复用旧端点连接池。
	clusterService.RegisterCacheInvalidator(proxy.Invalidate)
	var handler http.Handler = proxy
	handler = middleware.RequireRolesHTTP("admin")(handler)
	handler = middleware.RequireCSRFHTTP(handler)
	handler = middleware.AuthenticateHTTP(authenticator, handler)
	return handler
}

func newAIGenerator(cfg config.AIConfig, encryptor secrets.Encryptor) (aiapplication.AssistantGenerator, error) {
	if !cfg.Enabled {
		return nil, nil
	}

	providers := make(map[string]platformllm.ProviderConfig, len(cfg.Providers))
	for providerName, providerConfig := range cfg.Providers {
		// api_key 支持密文(enc:v1: 前缀,与集群 kubeconfig 同一套 AES-GCM
		// 加密体系)或明文;Decrypt 对无前缀的明文原样透传,完全向后兼容。
		apiKey, err := encryptor.Decrypt(strings.TrimSpace(providerConfig.APIKey))
		if err != nil {
			return nil, fmt.Errorf("decrypt api_key for ai provider %q: %w", providerName, err)
		}
		providers[providerName] = platformllm.ProviderConfig{
			Type:               providerConfig.Type,
			BaseURL:            providerConfig.BaseURL,
			ChatPath:           providerConfig.ChatPath,
			APIKey:             apiKey,
			Model:              providerConfig.Model,
			Timeout:            providerConfig.Timeout,
			StreamTimeout:      providerConfig.StreamTimeout,
			Stream:             providerConfig.Stream,
			Temperature:        providerConfig.Temperature,
			MaxTokens:          providerConfig.MaxTokens,
			MaxRetries:         providerConfig.MaxRetries,
			RetryBackoff:       providerConfig.RetryBackoff,
			IncludeStreamUsage: providerConfig.IncludeStreamUsage,
		}
	}

	registry, err := platformllm.NewRegistry(cfg.DefaultProvider, providers)
	if err != nil {
		return nil, err
	}
	return aillm.NewAssistantGenerator(registry), nil
}

// resolveAgentPrompts 解析各 Agent 的 system prompt 覆盖来源:内联 Prompts
// 优先,其次读取 PromptFiles 指定的文件。读文件失败仅告警并跳过(回退到代码
// 内置默认),不阻断启动。
func resolveAgentPrompts(cfg config.AgentConfig, logger *slog.Logger) map[string]string {
	prompts := make(map[string]string)
	for agentType, path := range cfg.PromptFiles {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			logger.Warn("read agent prompt file failed", "agent", agentType, "path", path, "error", err)
			continue
		}
		if content := strings.TrimSpace(string(data)); content != "" {
			prompts[agentType] = content
		}
	}
	for agentType, prompt := range cfg.Prompts {
		if content := strings.TrimSpace(prompt); content != "" {
			prompts[agentType] = content
		}
	}
	return prompts
}

// resolveAgentToolOverrides 把配置中的工具治理覆盖转换为 domain 层补丁(provider
// 无关),供 Service 注入工具注册表。空配置返回 nil,Service 据此不施加任何覆盖。
func resolveAgentToolOverrides(cfg config.AgentConfig) map[string]agentdomain.ToolOverride {
	if len(cfg.Tools.Overrides) == 0 {
		return nil
	}
	overrides := make(map[string]agentdomain.ToolOverride, len(cfg.Tools.Overrides))
	for toolID, override := range cfg.Tools.Overrides {
		toolID = strings.TrimSpace(toolID)
		if toolID == "" {
			continue
		}
		overrides[toolID] = agentdomain.ToolOverride{
			Enabled:         override.Enabled,
			Description:     override.Description,
			TimeoutMS:       override.TimeoutMS,
			ObserveMaxChars: override.ObserveMaxChars,
		}
	}
	return overrides
}

// resolveAgentSkills 把配置中的技能声明转换为 domain 层定义(provider 无关),
// 供 Service 注册。空配置返回 nil,Service 据此不注册任何技能。
func resolveAgentSkills(cfg config.AgentConfig) []agentdomain.SkillDefinition {
	if len(cfg.Skills) == 0 {
		return nil
	}
	skills := make([]agentdomain.SkillDefinition, 0, len(cfg.Skills))
	for _, skill := range cfg.Skills {
		id := strings.TrimSpace(skill.ID)
		if id == "" {
			continue
		}
		enabled := true
		if skill.Enabled != nil {
			enabled = *skill.Enabled
		}
		skills = append(skills, agentdomain.SkillDefinition{
			ID:           id,
			Name:         strings.TrimSpace(skill.Name),
			Description:  strings.TrimSpace(skill.Description),
			Enabled:      enabled,
			AgentTypes:   skill.AgentTypes,
			Triggers:     skill.Triggers,
			SystemPrompt: skill.SystemPrompt,
			AllowedTools: skill.AllowedTools,
			Hints:        skill.Hints,
		})
	}
	return skills
}

func newAPIHandler(
	cfg config.Config,
	logger *slog.Logger,
	authenticator middleware.Authenticator,
	iamService *iamapplication.Service,
	oidcService *iamapplication.OIDCService,
	uploadService *uploadapplication.Service,
	clusterService *clusterapplication.Service,
	aiService *aiapplication.Service,
	agentService *agentapplication.Service,
) (http.Handler, error) {
	if cfg.Service.Environment == "production" {
		gin.SetMode(gin.ReleaseMode)
	}

	engine := gin.New()
	if err := engine.SetTrustedProxies(cfg.HTTP.TrustedProxies); err != nil {
		return nil, err
	}

	engine.Use(middleware.RecoverGin(logger))
	engine.Use(middleware.RequestIDGin())
	engine.Use(middleware.AccessLogGin(logger))
	engine.Use(middleware.CORSGin(toCORSConfig(cfg)))
	engine.Use(otelgin.Middleware(cfg.Service.Name))

	api := engine.Group("/api/v1")
	iamHandler := iamhttp.NewHandler(iamService)
	iamHandler.SetOIDCService(oidcService)
	iamHandler.SetCookieOptions(iamhttp.CookieOptions{
		Secure: cfg.Auth.CookieSecure || cfg.Service.Environment == "production",
		Domain: cfg.Auth.CookieDomain,
	})
	iamhttp.RegisterPublicRoutes(api, iamHandler)
	uploadHandler := uploadhttp.NewHandler(uploadService)
	uploadhttp.RegisterPublicRoutes(api, uploadHandler)

	protectedAPI := api.Group("")
	protectedAPI.Use(middleware.AuthenticateGin(authenticator))
	protectedAPI.Use(middleware.RequireCSRFGin())
	protectedAPI.GET("/system/info", func(c *gin.Context) {
		principal, _ := middleware.PrincipalFromContext(c.Request.Context())
		response.OK(c, http.StatusOK, gin.H{
			"service":     cfg.Service.Name,
			"environment": cfg.Service.Environment,
			"subject":     principal.Subject,
		})
	})
	iamhttp.RegisterProtectedRoutes(protectedAPI, iamHandler)
	// 用户增删改查属于管理操作,必须带 admin 角色守卫,避免任何已登录用户越权
	// 枚举/创建/改密/删除其他账户。
	adminAPI := protectedAPI.Group("")
	adminAPI.Use(middleware.RequireRolesGin(middleware.RoleAdmin))
	iamhttp.RegisterAdminRoutes(adminAPI, iamHandler)
	uploadhttp.RegisterProtectedRoutes(protectedAPI, uploadHandler)
	clusterHandler := clusterhttp.NewHandler(clusterService)
	clusterhttp.RegisterRoutes(protectedAPI, clusterHandler)
	aiHandler := aihttp.NewHandler(aiService)
	aihttp.RegisterRoutes(protectedAPI, aiHandler)
	agentHandler := agenthttp.NewHandler(agentService)
	agenthttp.RegisterRoutes(protectedAPI, agentHandler)

	var handler http.Handler = engine
	if cfg.HTTP.APIRequestTimeout > 0 {
		handler = middleware.TimeoutHTTPWithSkipper(cfg.HTTP.APIRequestTimeout, isLongLivedAPIRequest, handler)
	}
	return handler, nil
}

func isLongLivedAPIRequest(r *http.Request) bool {
	if r == nil || r.URL == nil {
		return false
	}
	path := r.URL.Path
	if r.Method != http.MethodPost {
		return false
	}
	if strings.HasPrefix(path, "/api/v1/agent/") && strings.HasSuffix(path, "/run/stream") {
		return true
	}
	return strings.HasPrefix(path, "/api/v1/ai/session/") &&
		strings.HasSuffix(path, "/message/stream")
}

func runAuthStateCleanup(ctx context.Context, logger *slog.Logger, authStateRepo *iampostgres.AuthStateRepository, captchaStore *iamcaptcha.Store) {
	cleanup := func() {
		cleanupCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		now := time.Now().UTC()
		if err := authStateRepo.CleanupExpired(cleanupCtx, now); err != nil {
			logger.Warn("auth state cleanup failed", "error", err)
		}
		if err := captchaStore.CleanupExpired(cleanupCtx, now); err != nil {
			logger.Warn("captcha cleanup failed", "error", err)
		}
	}

	cleanup()
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cleanup()
		}
	}
}

// runAIStateRecovery 周期性回收 AI 对话与 Agent 运行中残留的"僵尸"状态
// (进程在生成过程中重启/崩溃后留下的 pending/streaming 消息与 running 运行),
// 将其标记为 failed,避免前端看到永远"生成中"的记录。启动时立即执行一次。
func runAIStateRecovery(ctx context.Context, logger *slog.Logger, aiService *aiapplication.Service, agentService *agentapplication.Service) {
	recover := func() {
		recoverCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		if aiService != nil {
			if affected, err := aiService.RecoverStaleMessages(recoverCtx, aiapplication.DEFAULT_STALE_AFTER); err != nil {
				logger.Warn("ai stale message recovery failed", "error", err)
			} else if affected > 0 {
				logger.Info("ai stale messages recovered", "count", affected)
			}
		}
		if agentService != nil {
			if affected, err := agentService.RecoverStaleRuns(recoverCtx, agentapplication.DEFAULT_STALE_AFTER); err != nil {
				logger.Warn("agent stale run recovery failed", "error", err)
			} else if affected > 0 {
				logger.Info("agent stale runs recovered", "count", affected)
			}
		}
	}

	recover()
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			recover()
		}
	}
}

type userPrincipalResolver struct {
	repo iamdomain.Repository
}

func (r userPrincipalResolver) ResolvePrincipal(ctx context.Context, subject string) (middleware.Principal, error) {
	user, err := resolvePrincipalUser(ctx, r.repo, subject)
	if err != nil {
		return middleware.Principal{}, err
	}
	if user.Status != iamapplication.USER_STATUS_ACTIVE {
		return middleware.Principal{}, middleware.ErrUnauthorized
	}

	return middleware.Principal{
		Subject: subject,
		Roles:   rolesForUser(user),
	}, nil
}

func rolesForUser(user iamdomain.User) []string {
	if strings.EqualFold(strings.TrimSpace(user.Username), "admin") {
		return []string{middleware.RoleAdmin}
	}
	return nil
}

func resolvePrincipalUser(ctx context.Context, repo iamdomain.Repository, subject string) (iamdomain.User, error) {
	if repo == nil {
		return iamdomain.User{}, middleware.ErrUnauthorized
	}

	trimmedSubject := strings.TrimSpace(subject)
	if trimmedSubject != "" {
		user, err := repo.GetByLegacyID(ctx, trimmedSubject)
		if err == nil {
			return user, nil
		}
	}
	userID, err := strconv.ParseInt(trimmedSubject, 10, 64)
	if err == nil && userID > 0 {
		return repo.Get(ctx, userID)
	}
	if trimmedSubject == "" {
		return iamdomain.User{}, middleware.ErrUnauthorized
	}
	return repo.GetByLegacyID(ctx, trimmedSubject)
}

func toCORSConfig(cfg config.Config) middleware.CORSConfig {
	return middleware.CORSConfig{
		AllowedOrigins:   cfg.HTTP.AllowedOrigins,
		AllowCredentials: cfg.HTTP.AllowCredentials,
		AllowHeaders:     cfg.HTTP.AllowHeaders,
		AllowMethods:     cfg.HTTP.AllowMethods,
	}
}
