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
	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/contrib/instrumentation/github.com/gin-gonic/gin/otelgin"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"gorm.io/gorm"

	agentapplication "github.com/lanyulei/kubeflare/internal/module/agent/application"
	agentdomain "github.com/lanyulei/kubeflare/internal/module/agent/domain"
	agentkubeclient "github.com/lanyulei/kubeflare/internal/module/agent/infrastructure/kubeclient"
	agentkubernetes "github.com/lanyulei/kubeflare/internal/module/agent/infrastructure/kubernetes"
	agentmcp "github.com/lanyulei/kubeflare/internal/module/agent/infrastructure/mcp"
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
	platformcoord "github.com/lanyulei/kubeflare/internal/platform/coordination"
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

// New 根据配置装配完整应用实例，但不主动启动 HTTP 服务。
//
// 装配按依赖顺序拆分为若干阶段:平台基础设施 -> 仓储 -> 认证状态 -> IAM ->
// 核心业务服务 -> Agent -> HTTP handler -> 后台任务 -> 根 handler。每个阶段封装
// 在独立函数中,New 仅负责串联各阶段并把产物组装成 App。
func New(ctx context.Context, cfg config.Config) (*App, error) {
	// 装配日志、追踪、校验器、指标、数据库、缓存、加密器等平台级共享依赖。
	plat, err := newPlatform(ctx, cfg)
	if err != nil {
		return nil, err
	}

	// 装配各业务模块的持久化仓储。
	repos := newRepositories(plat, cfg)

	// 解析会话状态存储,决定 token 撤销/刷新轮换能力的可用性。
	authStateStore, err := resolveAuthStateStore(cfg, plat, repos)
	if err != nil {
		return nil, err
	}

	// 装配 IAM 相关服务与请求认证器。
	iam, err := newIAMServices(ctx, cfg, plat, repos, authStateStore)
	if err != nil {
		return nil, err
	}

	// 装配上传、集群、AI 等核心业务服务。
	core, err := newCoreServices(cfg, plat, repos)
	if err != nil {
		return nil, err
	}

	// 装配 Agent 服务及其 MCP 连接管理器。
	agentService, mcpManager, err := newAgentService(cfg, plat, repos, core)
	if err != nil {
		return nil, err
	}
	// AI 消息反馈需要 Agent 补充运行维度元数据。
	core.ai.SetMessageMetadataEnricher(agentService.EnrichChatMessageFeedback)

	// KAPI handler 负责认证后代理 Kubernetes API。
	kapiHandler := newKAPIHandler(core.cluster, iam.authenticator, cfg.HTTP.APIRequestTimeout, clusterkubernetes.SecurityOptions{
		AllowedOrigins:               cfg.HTTP.AllowedOrigins,
		BlockedNamespaces:            cfg.KAPI.BlockedNamespaces,
		MaxConcurrentSessionsPerUser: cfg.KAPI.MaxConcurrentSessionsPerUser,
		SessionSemaphore:             plat.coordinationClient,
	})

	// API handler 装配 Gin 路由和业务模块路由。
	apiHandler, err := newAPIHandler(cfg, plat.logger, iam.authenticator, iam.service, iam.oidc, core.upload, core.cluster, core.ai, agentService)
	if err != nil {
		return nil, err
	}

	// 启动后台周期任务与 MCP 生命周期,拿到统一的停止函数。
	stopAuthCleanup := startBackgroundTasks(plat.logger, repos, core, agentService, mcpManager)

	// 装配根 handler 与中间件链,并拿到健康检查管理器。
	rootHandler, healthManager := newRootHTTPHandler(cfg, plat, apiHandler, kapiHandler)

	// 根据 HTTP 配置创建标准 http.Server。
	server := httpx.NewServer(cfg.HTTP, rootHandler)

	// 返回已完成装配的应用对象，启动由 Run 负责。
	return &App{
		Config:     cfg,
		Logger:     plat.logger,
		Server:     server,
		Health:     healthManager,
		drainDelay: cfg.HTTP.DrainTimeout,
		shutdowners: []func(context.Context) error{
			func(context.Context) error {
				stopAuthCleanup()
				return nil
			},
			// MCP 优雅关闭:停止 supervise goroutine 并回收全部 stdio 子进程 / HTTP
			// 连接,杜绝僵尸进程。排在 stopAuthCleanup 之后——后者已取消 supervise 的
			// 维护 ctx,Close 再做连接回收兜底。mcpManager 为 nil(未配置)时安全空操作。
			func(ctx context.Context) error { return mcpManager.Close(ctx) },
			plat.traceShutdown,
			func(context.Context) error { return cache.Close(plat.redisClient) },
			func(context.Context) error { return db.Close(plat.gormDB) },
		},
	}, nil
}

// platform 聚合所有业务模块共享的平台级依赖,由 newPlatform 一次性装配。
type platform struct {
	logger             *slog.Logger                    // 结构化日志器,全局共享同一观测配置。
	traceShutdown      func(context.Context) error     // 链路追踪的 flush/关闭函数。
	validator          *validator.Validate             // 请求参数校验器,供各 service 复用。
	metricsRegistry    *metrics.Registry               // 指标注册表,供 HTTP 与模块指标统一注册。
	gormDB             *gorm.DB                        // Postgres 连接;未启用时为 nil。
	redisClient        *redis.Client                   // Redis 客户端;未启用时为 nil。
	coordinationClient *platformcoord.RedisCoordinator // 事件总线、分布式信号与并发控制。
	encryptor          secrets.Encryptor               // AES-GCM 加解密器,处理 enc:v1: 密文。
	authSigningKey     string                          // 登录 token 签名密钥(已去空白)。
}

// newPlatform 装配日志、追踪、校验器、指标、数据库、缓存、协调器与加密器等
// 平台级共享依赖,任一构造失败即作为致命错误返回。
func newPlatform(ctx context.Context, cfg config.Config) (*platform, error) {
	// 创建结构化日志器，后续所有组件共享同一观测配置。
	logger := logpkg.New(cfg.Observability)

	// 初始化链路追踪，并拿到关闭时需要调用的 flush 函数。
	traceShutdown, err := tracepkg.Setup(ctx, cfg.Service.Name, cfg.Observability.Tracing)
	if err != nil {
		return nil, err
	}

	// 创建指标注册表，供 HTTP 和模块指标统一注册。
	metricsRegistry, err := metrics.NewRegistry()
	if err != nil {
		return nil, err
	}

	// 按配置打开 Postgres；未启用时底层会返回 nil 连接。
	gormDB, err := db.OpenPostgres(cfg.Database)
	if err != nil {
		return nil, err
	}

	// 按配置创建 Redis 客户端；未启用时返回 nil。
	redisClient, err := cache.NewRedisClient(cfg.Redis)
	if err != nil {
		return nil, err
	}

	// 清理 secrets.encryption_key 周围空白，避免配置误输入影响校验。
	encryptionKey := strings.TrimSpace(cfg.Secrets.EncryptionKey)
	// 加密密钥是集群凭证、AI 密钥等敏感数据解密的前置条件。
	if encryptionKey == "" {
		return nil, errors.New("secrets.encryption_key is required")
	}
	// 创建 AES-GCM 加解密器，统一处理 enc:v1: 密文。
	encryptor, err := secrets.NewAESGCMEncryptor(encryptionKey)
	if err != nil {
		return nil, err
	}

	return &platform{
		logger:          logger,
		traceShutdown:   traceShutdown,
		validator:       validator.New(),
		metricsRegistry: metricsRegistry,
		gormDB:          gormDB,
		redisClient:     redisClient,
		// Redis 协调器同时承载事件总线、分布式信号和并发控制。
		coordinationClient: platformcoord.NewRedisCoordinator(redisClient, cfg.Service.Name),
		encryptor:          encryptor,
		// 登录签名密钥去除空白后传给 token 管理器。
		authSigningKey: strings.TrimSpace(cfg.Auth.SigningKey),
	}, nil
}

// repositories 聚合各业务模块的持久化仓储,由 newRepositories 一次性装配。
type repositories struct {
	user             *iampostgres.UserRepository        // IAM 用户基础数据读写。
	authState        *iampostgres.AuthStateRepository   // token 撤销、刷新轮换等会话状态。
	captcha          *iamcaptcha.Store                  // 验证码存储(Redis 优先,数据库兜底)。
	upload           *uploadlocal.FileRepository        // 上传文件落盘。
	cluster          *clusterpostgres.ClusterRepository // 集群元数据持久化。
	clusterInspector *clusterkubernetes.Inspector       // 探测 Kubernetes 集群状态。
	ai               *aipostgres.ChatRepository         // AI 聊天消息持久化。
	agent            *agentpostgres.AgentRepository     // Agent 运行与配置持久化。
}

// newRepositories 基于平台连接装配各模块仓储,并把自定义验证码存储注入 captcha 库。
func newRepositories(plat *platform, cfg config.Config) *repositories {
	// 验证码存储优先使用 Redis，并可配合数据库兜底清理。
	captchaStore := iamcaptcha.NewStore(plat.redisClient, plat.gormDB, cfg.Auth.CaptchaTTL, cfg.Database.QueryTimeout)
	// 将自定义验证码存储注入 captcha 库。
	captcha.SetCustomStore(captchaStore)

	return &repositories{
		// IAM 用户仓储负责账户基础数据读写。
		user: iampostgres.NewUserRepository(plat.gormDB, cfg.Database.QueryTimeout),
		// 认证状态仓储负责 token 撤销、刷新轮换等状态。
		authState: iampostgres.NewAuthStateRepository(plat.gormDB, cfg.Database.QueryTimeout),
		captcha:   captchaStore,
		// 本地文件仓储负责上传文件落盘。
		upload: uploadlocal.NewFileRepository(cfg.Upload.RootDir),
		// 集群仓储负责集群元数据持久化。
		cluster: clusterpostgres.NewClusterRepository(plat.gormDB, cfg.Database.QueryTimeout),
		// 集群检查器负责探测 Kubernetes 集群状态。
		clusterInspector: clusterkubernetes.NewInspector(cfg.Database.QueryTimeout),
		// AI 会话仓储负责聊天消息持久化。
		ai: aipostgres.NewChatRepository(plat.gormDB, cfg.Database.QueryTimeout),
		// Agent 仓储负责 Agent 运行和配置持久化。
		agent: agentpostgres.NewAgentRepository(plat.gormDB, cfg.Database.QueryTimeout),
	}
}

// resolveAuthStateStore 根据可用的持久化后端选择会话状态存储。Redis 与数据库
// 同时存在时使用故障转移存储;无任何后端时,生产环境拒绝启动,其余环境降级告警。
func resolveAuthStateStore(cfg config.Config, plat *platform, repos *repositories) (middleware.TokenStateStore, error) {
	// authStateStore 是中间件读取会话状态的抽象入口。
	var authStateStore middleware.TokenStateStore
	// Redis 与数据库同时存在时使用故障转移存储，提升可用性。
	if plat.redisClient != nil && plat.gormDB != nil {
		authStateStore = iamauthstate.NewFailoverStore(iamredis.NewAuthStateStore(plat.redisClient), repos.authState)
	} else if plat.gormDB != nil {
		// 只有数据库时直接使用数据库状态仓储。
		authStateStore = repos.authState
	} else if plat.redisClient != nil {
		// 只有 Redis 时使用 Redis 状态存储。
		authStateStore = iamredis.NewAuthStateStore(plat.redisClient)
	}
	// 无状态存储会导致 token 撤销能力不可用。
	if authStateStore == nil {
		// 无持久化存储时,会话撤销/刷新轮换全部静默失效(登出无效、token 无法
		// 吊销)。生产环境视为严重风险,启动即拒绝;其余环境降级为告警。
		if cfg.Service.Environment == "production" {
			return nil, errors.New("auth state store is required in production (configure database or redis) to enable session revocation")
		}
		plat.logger.Warn("auth state store is not configured; session revocation and refresh-token rotation are disabled")
	}
	return authStateStore, nil
}

// iamServices 聚合 IAM 相关服务与请求认证器,供 HTTP handler 使用。
type iamServices struct {
	service       *iamapplication.Service     // 聚合用户、登录、密码和 token 业务。
	oidc          *iamapplication.OIDCService // OIDC 登录服务;未启用时为 nil。
	authenticator middleware.Authenticator    // 从 token 解析用户身份并回查状态。
}

// newIAMServices 装配 token 管理器、认证器与 IAM service,并在配置启用时接入 OIDC。
func newIAMServices(ctx context.Context, cfg config.Config, plat *platform, repos *repositories, authStateStore middleware.TokenStateStore) (*iamServices, error) {
	// tokenManager 统一签发、校验和刷新访问令牌。
	tokenManager := middleware.NewSignedTokenManagerWithOptions(plat.authSigningKey, cfg.Auth.TokenTTL, cfg.Auth.RefreshTokenTTL, authStateStore)
	// authenticator 从 token 解析用户身份，并回查用户状态。
	authenticator := middleware.NewSignedTokenAuthenticator(tokenManager, userPrincipalResolver{repo: repos.user})
	// IAM service 聚合用户、登录、密码和 token 相关业务。
	iamService := iamapplication.NewService(repos.user, plat.validator, tokenManager)
	// 安全状态存储可选注入，支持登录失败锁定等策略。
	securityStateStore, _ := authStateStore.(iamdomain.SecurityStateStore)
	// 将安全状态存储交给 IAM service 使用。
	iamService.SetSecurityStateStore(securityStateStore)
	// 注入敏感字段加密器，用于保存密钥类数据。
	iamService.SetSecretEncryptor(plat.encryptor)
	// 注入认证策略，控制验证码、失败次数和锁定时间。
	iamService.SetAuthPolicy(iamapplication.AuthPolicy{
		CaptchaTTL:            cfg.Auth.CaptchaTTL,
		CaptchaFailureTrigger: cfg.Auth.CaptchaFailureTrigger,
		MaxFailedAttempts:     cfg.Auth.MaxFailedAttempts,
		LockoutDuration:       cfg.Auth.LockoutDuration,
	})

	// OIDC service 仅在配置启用时创建。
	var oidcService *iamapplication.OIDCService
	// OIDC 启用后加载 issuer 元数据并接入 token 管理。
	if cfg.Auth.OIDC.Enabled {
		var err error
		oidcService, err = iamapplication.NewOIDCService(ctx, iamapplication.OIDCConfig{
			IssuerURL:    cfg.Auth.OIDC.IssuerURL,
			ClientID:     cfg.Auth.OIDC.ClientID,
			ClientSecret: cfg.Auth.OIDC.ClientSecret,
			RedirectURL:  cfg.Auth.OIDC.RedirectURL,
			Scopes:       cfg.Auth.OIDC.Scopes,
		}, repos.user, tokenManager, securityStateStore)
		if err != nil {
			return nil, err
		}
	}

	return &iamServices{
		service:       iamService,
		oidc:          oidcService,
		authenticator: authenticator,
	}, nil
}

// coreServices 聚合上传、集群、AI 等核心业务服务。aiGenerator 单独保留,
// 以便 Agent 复用同一实例(避免重复构造导致连接池/fallback 状态分裂)。
type coreServices struct {
	upload      *uploadapplication.Service       // 文件上传业务和访问路径。
	cluster     *clusterapplication.Service      // 集群 CRUD、凭证加密和连通性检查。
	ai          *aiapplication.Service           // 会话、消息流和系统提示词。
	aiGenerator aiapplication.AssistantGenerator // AI 对话生成器;未启用时为 nil。
}

// newCoreServices 装配上传、集群、AI 服务,并完成跨实例事件总线/缓存
// 失效总线的注入。AI 对话生成器在此构造一次,供 AI service 与后续 Agent 共享。
func newCoreServices(cfg config.Config, plat *platform, repos *repositories) (*coreServices, error) {
	// 创建 AI 对话生成器；AI 未启用时可能返回 nil。
	aiGenerator, err := newAIGenerator(cfg.AI, plat.encryptor)
	if err != nil {
		return nil, err
	}

	// 上传 service 处理文件上传业务和访问路径。
	uploadService := uploadapplication.NewService(repos.upload, plat.validator, "/api/v1/upload")
	// 集群 service 处理集群 CRUD、凭证加密和连通性检查。
	clusterService := clusterapplication.NewService(repos.cluster, plat.validator, plat.encryptor, repos.clusterInspector)
	// 注册跨实例缓存失效总线，让集群变更能通知其他节点。
	clusterService.SetCacheInvalidationBus(plat.coordinationClient)
	// AI service 处理会话、消息流和系统提示词。
	aiService := aiapplication.NewService(repos.ai, plat.validator, aiGenerator, strings.TrimSpace(cfg.AI.SystemPrompt), plat.logger)
	// 注入事件总线，用于跨实例广播 AI 状态变化。
	aiService.SetEventBus(plat.coordinationClient)

	return &coreServices{
		upload:      uploadService,
		cluster:     clusterService,
		ai:          aiService,
		aiGenerator: aiGenerator,
	}, nil
}

// newAgentService 装配 Agent 服务:动态客户端工厂、Kubernetes/Prometheus 工具执行器、
// 可选 embedding 与 MCP 集成,最终聚合为 agent service。返回 mcpManager 供后台任务
// 启动连接生命周期及关闭时回收资源;未配置 MCP 时 mcpManager 为 nil。
func newAgentService(cfg config.Config, plat *platform, repos *repositories, core *coreServices) (*agentapplication.Service, *agentmcp.Manager, error) {
	// Kubernetes client 工厂按集群动态创建客户端。
	agentClientFactory := agentkubeclient.NewFactory(core.cluster, 0)
	// 集群 kubeconfig 更新/删除后失效缓存的 clientset,避免 TTL 窗口内沿用旧凭证。
	core.cluster.RegisterCacheInvalidator(agentClientFactory.Invalidate)
	// Kubernetes 工具执行器让 Agent 能调用集群操作。
	agentKubernetesExecutor := agentkubernetes.NewToolExecutor(agentClientFactory)
	// Prometheus 工具执行器让 Agent 能查询集群监控数据。
	agentPrometheusExecutor := agentprometheus.NewToolExecutor(agentClientFactory, agentprometheus.Config{
		Namespace:    cfg.Agent.Prometheus.Namespace,
		Service:      cfg.Agent.Prometheus.Service,
		Port:         cfg.Agent.Prometheus.Port,
		Scheme:       cfg.Agent.Prometheus.Scheme,
		QueryTimeout: cfg.Agent.Prometheus.QueryTimeout,
	})
	// Agent 默认复用 AI 对话生成器。
	agentGenerator := core.aiGenerator
	// AI 未启用时注入不可用生成器，避免后续空指针。
	if agentGenerator == nil {
		agentGenerator = aiapplication.NewUnavailableAssistantGenerator()
	}
	// 可选 embedding 能力:配置 ai.embedding 后启用语义检索,否则注入 nil client
	// (生成器 Available()=false,语义检索自动降级关键词)。构造失败视为致命配置
	// 错误,与 chat provider 装配一致。
	agentEmbeddingGenerator, err := newAIEmbeddingGenerator(cfg.AI, plat.encryptor)
	if err != nil {
		return nil, nil, err
	}
	// 可选 MCP 集成:配置 agent.mcp_servers 后装配外部工具能力。Manager 持有连接
	// 生命周期,Executor 作为统一 mcp 数据源执行器注入分发器。构造失败(凭证解密)
	// 视为致命配置错误,与其它 provider 装配一致。
	mcpManager, mcpExecutor, err := newAgentMCPManager(cfg.Agent, plat.encryptor, plat.logger, plat.metricsRegistry)
	if err != nil {
		return nil, nil, err
	}
	// Agent 基础工具来源包含 Kubernetes 与 Prometheus。
	agentToolExecutors := []agentapplication.SourceToolExecutor{
		agentKubernetesExecutor,
		agentPrometheusExecutor,
	}
	// MCP 启用时追加外部工具执行器。
	if mcpExecutor != nil {
		agentToolExecutors = append(agentToolExecutors, mcpExecutor)
	}
	// Agent service 聚合运行、工具调用、记忆检索和并发控制。
	agentService := agentapplication.NewService(agentapplication.Options{
		Repo:              repos.agent,
		Validator:         plat.validator,
		ChatRepo:          repos.ai,
		AssistantStreamer: core.ai,
		ToolExecutors:     agentToolExecutors,
		Generator:         agentGenerator,
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
			ReflectionJurors:         cfg.Agent.ReflectionJurors,
			HypothesisLedger:         cfg.Agent.HypothesisLedger,
			Playbook:                 cfg.Agent.Playbook,
			Replanning:               cfg.Agent.Replanning,
			ReplanInterval:           cfg.Agent.ReplanInterval,
			MaxReplans:               cfg.Agent.MaxReplans,
			ObserveCompression:       cfg.Agent.ObserveCompression,
			CaseLibrary:              cfg.Agent.CaseLibrary,
			CaseFewShotLimit:         cfg.Agent.CaseFewShotLimit,
			RouteLearning:            cfg.Agent.RouteLearning,
			RouteFewShotLimit:        cfg.Agent.RouteFewShotLimit,
			RouteCacheSize:           cfg.Agent.RouteCacheSize,
			CaseCacheSize:            cfg.Agent.CaseCacheSize,
			SemanticRetrieval:        cfg.Agent.SemanticRetrieval,
			MaxConcurrentRunsPerUser: cfg.Agent.MaxConcurrentRunsPerUser,
			MaxConcurrentRuns:        cfg.Agent.MaxConcurrentRuns,
		},
		SystemPrompts:            resolveAgentPrompts(cfg.Agent, plat.logger),
		ToolOverrides:            resolveAgentToolOverrides(cfg.Agent),
		Skills:                   resolveAgentSkills(cfg.Agent),
		EmbeddingGenerator:       agentEmbeddingGenerator,
		Semaphore:                plat.coordinationClient,
		EventBus:                 plat.coordinationClient,
		MCPStatusProvider:        newAgentMCPStatusProvider(mcpManager),
		PrometheusHealthProvider: newAgentPrometheusHealthProvider(agentPrometheusExecutor),
		PrometheusStatus: agentapplication.RuntimePrometheusStatus{
			Enabled:        strings.TrimSpace(cfg.Agent.Prometheus.Service) != "",
			Namespace:      cfg.Agent.Prometheus.Namespace,
			Service:        cfg.Agent.Prometheus.Service,
			Port:           cfg.Agent.Prometheus.Port,
			Scheme:         cfg.Agent.Prometheus.Scheme,
			QueryTimeoutMS: cfg.Agent.Prometheus.QueryTimeout.Milliseconds(),
		},
		Logger: plat.logger,
	})

	return agentService, mcpManager, nil
}

// startBackgroundTasks 创建后台任务共享的取消上下文,启动各类周期任务与 MCP
// 连接生命周期,并返回统一的停止函数(在关闭流程中调用以取消所有后台任务)。
func startBackgroundTasks(logger *slog.Logger, repos *repositories, core *coreServices, agentService *agentapplication.Service, mcpManager *agentmcp.Manager) func() {
	// 后台任务共享 authCleanupCtx，关闭时统一取消。
	authCleanupCtx, stopAuthCleanup := context.WithCancel(context.Background())
	// 启动 Agent 运行配置监听。
	agentService.StartRuntimeConfigWatcher(authCleanupCtx)
	// 启动集群缓存失效事件监听。
	core.cluster.StartCacheInvalidationWatcher(authCleanupCtx)
	// 周期清理过期认证状态和验证码。
	safego.Go(logger, "auth state cleanup", func() { runAuthStateCleanup(authCleanupCtx, logger, repos.authState, repos.captcha) })
	// 周期恢复重启后遗留的 AI/Agent 运行中状态。
	safego.Go(logger, "ai state recovery", func() { runAIStateRecovery(authCleanupCtx, logger, core.ai, agentService) })

	// 启动 MCP 连接生命周期(异步,不阻塞启动)并注入工具来源。server 就绪后经
	// onReady 触发工具注册表增量重载,把其工具补入对外视图。SetToolProviders 先
	// 行一次聚合加载(此刻多数 server 尚未就绪,加载到的多为降级空集,就绪后由
	// onReady 补入)。仅在配置了 MCP server 时装配,零配置时零行为变化。
	//
	// 刻意不把 MCP server 接入 /readyz:MCP 是增强能力而非核心依赖,外部 server 未
	// 就绪绝不能让整个服务被判不可用而摘除流量。其连接状态经 kubeflare_mcp_server_state
	// 指标可观测,运维据此告警,而不阻断主服务就绪。
	if mcpManager != nil {
		// 异步启动 MCP server，并在就绪后刷新工具注册表。
		mcpManager.Start(authCleanupCtx, func(string) {
			agentService.ReloadToolProviders(authCleanupCtx)
		})
		// 将 MCP provider 注入 Agent 工具来源。
		agentService.SetToolProviders(authCleanupCtx, agentmcp.NewProvider(mcpManager))
	}

	return stopAuthCleanup
}

// newRootHTTPHandler 装配健康检查管理器、可选 pprof,聚合根 handler 并按由内到外
// 的顺序包裹标准 HTTP 中间件。返回最终 handler 与健康检查管理器(供 App 持有)。
func newRootHTTPHandler(cfg config.Config, plat *platform, apiHandler, kapiHandler http.Handler) (http.Handler, *health.Manager) {
	// 健康检查管理器统一处理 livez/readyz。
	healthManager := health.NewManager(
		cfg.HTTP.ReadinessTimeout,
		health.FuncChecker{
			// postgres 检查验证数据库连接是否可用。
			CheckName: "postgres",
			CheckFunc: func(ctx context.Context) error {
				// 单次数据库健康检查使用独立超时。
				pingCtx, cancel := db.WithTimeout(ctx, cfg.Database.HealthCheckTimeout)
				defer cancel()
				// 执行数据库 ping。
				return db.Ping(pingCtx, plat.gormDB)
			},
		},
		health.FuncChecker{
			// redis 检查验证缓存连接是否可用。
			CheckName: "redis",
			CheckFunc: func(ctx context.Context) error {
				// Redis 未启用时视为健康。
				if plat.redisClient == nil {
					return nil
				}
				// 单次 Redis 健康检查使用独立超时。
				pingCtx, cancel := context.WithTimeout(ctx, cfg.Redis.HealthCheckTimeout)
				defer cancel()
				// 执行 Redis ping。
				return plat.redisClient.Ping(pingCtx).Err()
			},
		},
	)

	// pprofHandler 仅在配置开启时创建。
	var pprofHandler http.Handler
	// EnablePprof 为 true 时暴露调试端点。
	if cfg.HTTP.EnablePprof {
		pprofHandler = NewPprofHandler()
	}

	// 根 handler 汇总健康检查、指标、pprof、业务 API 和 KAPI。
	rootHandler := NewRootHandler(RootHandlerOptions{
		LivezHandler:   healthManager.LiveHandler(),
		ReadyzHandler:  healthManager.ReadyHandler(),
		MetricsHandler: plat.metricsRegistry.Handler(),
		PprofHandler:   pprofHandler,
		APIHandler:     apiHandler,
		KAPIHandler:    kapiHandler,
	})

	// 注入 HTTP 指标采集。
	rootHandler = metrics.InstrumentHTTP(plat.metricsRegistry, rootHandler)
	// 注入安全响应头。
	rootHandler = middleware.SecurityHeadersHTTP(rootHandler)
	// 注入跨域处理。
	rootHandler = middleware.CORSHTTP(toCORSConfig(cfg), rootHandler)
	// 注入访问日志。
	rootHandler = middleware.AccessLogHTTP(plat.logger, rootHandler)
	// 注入请求 ID。
	rootHandler = middleware.RequestIDHTTP(rootHandler)
	// 注入 panic 恢复。
	rootHandler = middleware.RecoverHTTP(plat.logger, rootHandler)
	// 注入 OpenTelemetry HTTP 追踪。
	rootHandler = otelhttp.NewHandler(rootHandler, cfg.Service.Name)

	return rootHandler, healthManager
}

// newKAPIHandler 创建受认证、CSRF 和角色保护的 Kubernetes API 代理。
func newKAPIHandler(clusterService *clusterapplication.Service, authenticator middleware.Authenticator, timeout time.Duration, security clusterkubernetes.SecurityOptions) http.Handler {
	// 创建带安全策略的 Kubernetes 代理 handler。
	proxy := clusterkubernetes.NewProxyHandlerWithSecurity(clusterService, timeout, security)
	// 集群 kubeconfig 更新/删除后失效代理缓存的 transport,避免继续复用旧端点连接池。
	clusterService.RegisterCacheInvalidator(proxy.Invalidate)
	// handler 先指向底层代理，再逐层包裹中间件。
	var handler http.Handler = proxy
	// KAPI 只允许 admin 角色访问。
	handler = middleware.RequireRolesHTTP("admin")(handler)
	// 写操作必须带 CSRF 校验。
	handler = middleware.RequireCSRFHTTP(handler)
	// 最外层负责认证并注入用户身份。
	handler = middleware.AuthenticateHTTP(authenticator, handler)
	// 返回完整受保护的 KAPI handler。
	return handler
}

// newAIGenerator 按 AI 配置创建聊天生成器；未启用时返回 nil。
func newAIGenerator(cfg config.AIConfig, encryptor secrets.Encryptor) (aiapplication.AssistantGenerator, error) {
	// AI 未启用时不装配生成器。
	if !cfg.Enabled {
		return nil, nil
	}

	// providers 保存解密后的 LLM provider 配置。
	providers := make(map[string]platformllm.ProviderConfig, len(cfg.Providers))
	// 遍历配置中的每个 provider。
	for providerName, providerConfig := range cfg.Providers {
		// api_key 支持密文(enc:v1: 前缀,与集群 kubeconfig 同一套 AES-GCM
		// 加密体系)或明文;Decrypt 对无前缀的明文原样透传,完全向后兼容。
		// 解密 provider API Key。
		apiKey, err := encryptor.Decrypt(strings.TrimSpace(providerConfig.APIKey))
		if err != nil {
			return nil, fmt.Errorf("decrypt api_key for ai provider %q: %w", providerName, err)
		}
		// 将应用配置转换为平台 LLM 配置。
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
			Seed:               providerConfig.Seed,
			MaxTokens:          providerConfig.MaxTokens,
			MaxRetries:         providerConfig.MaxRetries,
			RetryBackoff:       providerConfig.RetryBackoff,
			IncludeStreamUsage: providerConfig.IncludeStreamUsage,
		}
	}

	// registry 根据默认 provider 和 provider 列表选择实际客户端。
	registry, err := platformllm.NewRegistry(cfg.DefaultProvider, providers)
	if err != nil {
		return nil, err
	}
	// 配置了 fallback_providers 时装配 fallback 链(主+备),消除 LLM 单点;
	// 空列表时退化为纯默认 client,行为与改造前逐字节一致(零回归)。
	// 返回支持 fallback 的聊天生成器。
	return aillm.NewAssistantGeneratorWithFallback(registry, cfg.FallbackProviders)
}

// newAIEmbeddingGenerator 构造可选的 embedding 生成器:未配置 ai.embedding 时
// 返回一个底层 client 为 nil 的生成器(Available()=false,语义检索降级关键词);
// 配置无效(缺 base_url/api_key/model)时返回错误(与 chat provider 装配一致)。
// api_key 走与 chat 同一套 enc:v1: 解密体系。
func newAIEmbeddingGenerator(cfg config.AIConfig, encryptor secrets.Encryptor) (aiapplication.EmbeddingGenerator, error) {
	// AI 或 embedding 未启用时返回不可用生成器。
	if !cfg.Enabled || cfg.Embedding == nil {
		return aillm.NewEmbeddingGenerator(nil), nil
	}

	// 解密 embedding provider 的 API Key。
	apiKey, err := encryptor.Decrypt(strings.TrimSpace(cfg.Embedding.APIKey))
	if err != nil {
		return nil, fmt.Errorf("decrypt api_key for ai embedding provider: %w", err)
	}
	// 创建 embedding 客户端。
	client, err := platformllm.NewEmbeddingsClient("embedding", platformllm.EmbeddingsConfig{
		Type:         cfg.Embedding.Type,
		BaseURL:      cfg.Embedding.BaseURL,
		Path:         cfg.Embedding.Path,
		APIKey:       apiKey,
		Model:        cfg.Embedding.Model,
		Timeout:      cfg.Embedding.Timeout,
		MaxRetries:   cfg.Embedding.MaxRetries,
		RetryBackoff: cfg.Embedding.RetryBackoff,
	})
	if err != nil {
		return nil, err
	}
	// 将底层客户端包装为应用层 embedding 生成器。
	return aillm.NewEmbeddingGenerator(client), nil
}

// newAgentMCPManager 按 agent.mcp_servers 配置构造 MCP 连接管理器与统一执行器。
// 未配置任何 server 时返回 (nil, nil, nil),调用方据此跳过 MCP 装配(零行为变化)。
// 子进程 env 与 http headers 中的凭证走与其它 provider 同一套 enc:v1: 解密体系
// (明文原样透传);解密失败视为致命配置错误。
func newAgentMCPManager(
	cfg config.AgentConfig,
	encryptor secrets.Encryptor,
	logger *slog.Logger,
	metricsRegistry *metrics.Registry,
) (*agentmcp.Manager, agentapplication.SourceToolExecutor, error) {
	// 未配置 MCP server 时跳过 MCP 装配。
	if len(cfg.McpServers) == 0 {
		return nil, nil, nil
	}

	// servers 保存转换后的 MCP server 配置。
	servers := make([]agentmcp.ServerConfig, 0, len(cfg.McpServers))
	// 遍历原始 MCP server 配置。
	for _, raw := range cfg.McpServers {
		// 解密 stdio 子进程环境变量中的敏感值。
		env, err := decryptStringMap(encryptor, raw.Env)
		if err != nil {
			return nil, nil, fmt.Errorf("decrypt env for mcp server %q: %w", raw.Name, err)
		}
		// 解密 HTTP header 中的敏感值。
		headers, err := decryptStringMap(encryptor, raw.Headers)
		if err != nil {
			return nil, nil, fmt.Errorf("decrypt headers for mcp server %q: %w", raw.Name, err)
		}
		// trusted 记录该 server 允许自动信任的工具名称。
		trusted := make(map[string]struct{}, len(raw.Trust.AllowTools))
		// 过滤空白工具名。
		for _, tool := range raw.Trust.AllowTools {
			if name := strings.TrimSpace(tool); name != "" {
				trusted[name] = struct{}{}
			}
		}
		// 追加规范化后的 server 配置。
		servers = append(servers, agentmcp.ServerConfig{
			Name:           raw.Name,
			Transport:      raw.Transport,
			Command:        raw.Command,
			Env:            env,
			URL:            raw.URL,
			Headers:        headers,
			AgentTypes:     raw.AgentTypes,
			TrustedTools:   trusted,
			ConnectTimeout: raw.ConnectTimeout,
			ListTimeout:    raw.ListTimeout,
			CallTimeout:    raw.CallTimeout,
			HealthInterval: raw.HealthInterval,
			MaxConcurrency: raw.MaxConcurrency,
		})
	}

	// MCP manager 负责连接生命周期、健康状态和指标。
	manager := agentmcp.NewManager(agentmcp.ManagerOptions{
		Servers: servers,
		Logger:  logger,
		Metrics: agentmcp.NewMetrics(metricsRegistry),
	})
	// 若所有配置都被 manager 过滤掉，则跳过 MCP。
	if !manager.HasServers() {
		// 所有 server 配置均无效(已各自记日志):不装配 MCP,主服务正常启动。
		return nil, nil, nil
	}
	// 返回 manager 和基于 manager 的统一工具执行器。
	return manager, agentmcp.NewExecutor(manager), nil
}

// decryptStringMap 对 map 的每个值执行 Decrypt(明文原样透传),用于 MCP 子进程 env
// 与 http headers 中的凭证解密。返回新 map,不修改入参;空入参返回 nil。
func decryptStringMap(encryptor secrets.Encryptor, in map[string]string) (map[string]string, error) {
	// 空 map 不需要解密，保持 nil 语义。
	if len(in) == 0 {
		return nil, nil
	}
	// out 保存解密后的新 map，避免修改入参。
	out := make(map[string]string, len(in))
	// 逐项解密每个配置值。
	for key, value := range in {
		// Decrypt 会对明文原样返回，对 enc:v1: 密文执行解密。
		decrypted, err := encryptor.Decrypt(strings.TrimSpace(value))
		if err != nil {
			return nil, err
		}
		// 保留原 key，写入解密后的 value。
		out[key] = decrypted
	}
	// 返回解密后的配置 map。
	return out, nil
}

// resolveAgentPrompts 解析各 Agent 的 system prompt 覆盖来源:内联 Prompts
// 优先,其次读取 PromptFiles 指定的文件。读文件失败仅告警并跳过(回退到代码
// 内置默认),不阻断启动。
func resolveAgentPrompts(cfg config.AgentConfig, logger *slog.Logger) map[string]string {
	// prompts 保存最终生效的 agentType -> prompt。
	prompts := make(map[string]string)
	// 先读取文件配置，作为低优先级来源。
	for agentType, path := range cfg.PromptFiles {
		// 清理文件路径空白。
		path = strings.TrimSpace(path)
		// 空路径直接跳过。
		if path == "" {
			continue
		}
		// 读取 prompt 文件内容。
		data, err := os.ReadFile(path)
		if err != nil {
			logger.Warn("read agent prompt file failed", "agent", agentType, "path", path, "error", err)
			continue
		}
		// 文件内容非空时写入 prompts。
		if content := strings.TrimSpace(string(data)); content != "" {
			prompts[agentType] = content
		}
	}
	// 内联 Prompts 优先级更高，会覆盖文件来源。
	for agentType, prompt := range cfg.Prompts {
		// 空 prompt 不覆盖已有值。
		if content := strings.TrimSpace(prompt); content != "" {
			prompts[agentType] = content
		}
	}
	// 返回最终 prompt 覆盖表。
	return prompts
}

// resolveAgentToolOverrides 把配置中的工具治理覆盖转换为 domain 层补丁(provider
// 无关),供 Service 注入工具注册表。空配置返回 nil,Service 据此不施加任何覆盖。
func resolveAgentToolOverrides(cfg config.AgentConfig) map[string]agentdomain.ToolOverride {
	// 空配置直接返回 nil，表示不覆盖工具定义。
	if len(cfg.Tools.Overrides) == 0 {
		return nil
	}
	// overrides 保存规范化后的工具覆盖配置。
	overrides := make(map[string]agentdomain.ToolOverride, len(cfg.Tools.Overrides))
	// 遍历配置中的每个工具覆盖项。
	for toolID, override := range cfg.Tools.Overrides {
		// 清理工具 ID 空白。
		toolID = strings.TrimSpace(toolID)
		// 空工具 ID 无法匹配，直接跳过。
		if toolID == "" {
			continue
		}
		// 写入 domain 层无 provider 依赖的覆盖定义。
		overrides[toolID] = agentdomain.ToolOverride{
			Enabled:         override.Enabled,
			Description:     override.Description,
			TimeoutMS:       override.TimeoutMS,
			ObserveMaxChars: override.ObserveMaxChars,
		}
	}
	// 返回可注入 service 的覆盖表。
	return overrides
}

// resolveAgentSkills 把配置中的技能声明转换为 domain 层定义(provider 无关),
// 供 Service 注册。空配置返回 nil,Service 据此不注册任何技能。
func resolveAgentSkills(cfg config.AgentConfig) []agentdomain.SkillDefinition {
	// 空配置直接返回 nil，表示不注册技能。
	if len(cfg.Skills) == 0 {
		return nil
	}
	// skills 保存规范化后的技能定义。
	skills := make([]agentdomain.SkillDefinition, 0, len(cfg.Skills))
	// 遍历配置中的每个技能。
	for _, skill := range cfg.Skills {
		// 技能 ID 是注册和匹配的关键字段。
		id := strings.TrimSpace(skill.ID)
		// 空 ID 无法注册，直接跳过。
		if id == "" {
			continue
		}
		// 默认启用技能。
		enabled := true
		// 配置显式给出 Enabled 时采用配置值。
		if skill.Enabled != nil {
			enabled = *skill.Enabled
		}
		// 追加 domain 层技能定义。
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
	// 返回最终技能列表。
	return skills
}

// newAgentMCPStatusProvider 创建运行时 MCP 状态读取函数。
func newAgentMCPStatusProvider(manager *agentmcp.Manager) func() []agentapplication.RuntimeMCPServerStatus {
	// 未启用 MCP 时不提供状态函数。
	if manager == nil {
		return nil
	}
	// 返回闭包，调用时读取 manager 最新状态。
	return func() []agentapplication.RuntimeMCPServerStatus {
		// 从 manager 获取原始 server 状态。
		statuses := manager.Statuses()
		// items 保存转换后的运行时状态。
		items := make([]agentapplication.RuntimeMCPServerStatus, 0, len(statuses))
		// 逐个转换状态字段。
		for _, status := range statuses {
			// 写入对外暴露的状态结构。
			items = append(items, agentapplication.RuntimeMCPServerStatus{
				Name:             status.Name,
				Transport:        status.Transport,
				State:            status.State,
				Ready:            status.Ready,
				ToolCount:        status.ToolCount,
				TrustedToolCount: status.TrustedToolCount,
				MaxConcurrency:   status.MaxConcurrency,
				HealthIntervalMS: status.HealthInterval.Milliseconds(),
				CallTimeoutMS:    status.CallTimeout.Milliseconds(),
			})
		}
		// 返回所有 MCP server 状态。
		return items
	}
}

// newAgentPrometheusHealthProvider 创建 Agent 运行时 Prometheus 健康检查函数。
func newAgentPrometheusHealthProvider(executor *agentprometheus.ToolExecutor) func(context.Context, string) agentapplication.RuntimePrometheusHealth {
	// 未装配 Prometheus executor 时不提供健康检查函数。
	if executor == nil {
		return nil
	}
	// 返回闭包，按集群 ID 查询 Prometheus 健康状态。
	return func(ctx context.Context, clusterID string) agentapplication.RuntimePrometheusHealth {
		// 调用 executor 获取原始健康状态。
		status := executor.Health(ctx, clusterID)
		// 转换为应用层运行时状态结构。
		return agentapplication.RuntimePrometheusHealth{
			Healthy:       status.Healthy,
			LastError:     status.LastError,
			LatencyMS:     status.Latency.Milliseconds(),
			LastCheckedAt: status.LastCheckedAt,
		}
	}
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
	// 生产环境关闭 Gin 调试输出。
	if cfg.Service.Environment == "production" {
		gin.SetMode(gin.ReleaseMode)
	}

	// 创建无默认中间件的 Gin engine，便于精确控制中间件顺序。
	engine := gin.New()
	// 配置可信代理，避免错误信任客户端传入的转发头。
	if err := engine.SetTrustedProxies(cfg.HTTP.TrustedProxies); err != nil {
		return nil, err
	}

	// panic 恢复中间件放在最前面，兜底保护所有后续处理。
	engine.Use(middleware.RecoverGin(logger))
	// 为每个请求生成或透传 Request ID。
	engine.Use(middleware.RequestIDGin())
	// 记录 API 访问日志。
	engine.Use(middleware.AccessLogGin(logger))
	// 按配置处理跨域请求。
	engine.Use(middleware.CORSGin(toCORSConfig(cfg)))
	// 注入 Gin 层 OpenTelemetry 追踪。
	engine.Use(otelgin.Middleware(cfg.Service.Name))

	// 所有业务 API 统一挂在 /api/v1。
	api := engine.Group("/api/v1")
	// 创建 IAM handler。
	iamHandler := iamhttp.NewHandler(iamService)
	// 可选接入 OIDC service。
	iamHandler.SetOIDCService(oidcService)
	// 设置认证 cookie 策略。
	iamHandler.SetCookieOptions(iamhttp.CookieOptions{
		Secure: cfg.Auth.CookieSecure || cfg.Service.Environment == "production",
		Domain: cfg.Auth.CookieDomain,
	})
	// 注册无需登录的 IAM 路由。
	iamhttp.RegisterPublicRoutes(api, iamHandler)
	// 创建上传 handler。
	uploadHandler := uploadhttp.NewHandler(uploadService)
	// 注册公开上传相关路由。
	uploadhttp.RegisterPublicRoutes(api, uploadHandler)

	// protectedAPI 承载所有需要登录的 API。
	protectedAPI := api.Group("")
	// 登录认证必须先执行，后续中间件才能读取用户身份。
	protectedAPI.Use(middleware.AuthenticateGin(authenticator))
	// 受保护接口统一要求 CSRF 校验。
	protectedAPI.Use(middleware.RequireCSRFGin())
	// 系统信息接口返回当前服务和登录主体信息。
	protectedAPI.GET("/system/info", func(c *gin.Context) {
		// 从请求上下文读取认证主体。
		principal, _ := middleware.PrincipalFromContext(c.Request.Context())
		// 返回服务名、环境和当前用户 subject。
		response.OK(c, http.StatusOK, gin.H{
			"service":     cfg.Service.Name,
			"environment": cfg.Service.Environment,
			"subject":     principal.Subject,
		})
	})
	// 注册登录后可访问的 IAM 路由。
	iamhttp.RegisterProtectedRoutes(protectedAPI, iamHandler)
	// 用户增删改查属于管理操作,必须带 admin 角色守卫,避免任何已登录用户越权
	// 枚举/创建/改密/删除其他账户。
	// adminAPI 在登录和 CSRF 基础上追加 admin 角色要求。
	adminAPI := protectedAPI.Group("")
	// 管理接口只允许 admin 角色访问。
	adminAPI.Use(middleware.RequireRolesGin(middleware.RoleAdmin))
	// 注册 IAM 管理路由。
	iamhttp.RegisterAdminRoutes(adminAPI, iamHandler)
	// 注册受保护的上传路由。
	uploadhttp.RegisterProtectedRoutes(protectedAPI, uploadHandler)
	// 创建集群 handler。
	clusterHandler := clusterhttp.NewHandler(clusterService)
	// 注册集群管理路由。
	clusterhttp.RegisterRoutes(protectedAPI, clusterHandler)
	// 创建 AI handler。
	aiHandler := aihttp.NewHandler(aiService)
	// 注册 AI 会话路由。
	aihttp.RegisterRoutes(protectedAPI, aiHandler)
	// 创建 Agent handler。
	agentHandler := agenthttp.NewHandler(agentService)
	// 注册 Agent 路由。
	agenthttp.RegisterRoutes(protectedAPI, agentHandler)

	// handler 从 Gin engine 开始，按需包裹标准 HTTP 中间件。
	var handler http.Handler = engine
	// 配置了 API 超时时，为普通请求增加超时保护。
	if cfg.HTTP.APIRequestTimeout > 0 {
		// 流式接口由 skipper 跳过，避免长连接被普通超时截断。
		handler = middleware.TimeoutHTTPWithSkipper(cfg.HTTP.APIRequestTimeout, isLongLivedAPIRequest, handler)
	}
	// 返回完整 API handler。
	return handler, nil
}

// isLongLivedAPIRequest 判断请求是否为需要跳过普通超时的流式接口。
func isLongLivedAPIRequest(r *http.Request) bool {
	// 空请求或空 URL 不是有效长连接请求。
	if r == nil || r.URL == nil {
		return false
	}
	// path 保存请求路径，后续做前后缀判断。
	path := r.URL.Path
	// 当前只有 POST 流式接口需要跳过超时。
	if r.Method != http.MethodPost {
		return false
	}
	// Agent 运行流式接口需要允许长时间输出。
	if strings.HasPrefix(path, "/api/v1/agent/") && strings.HasSuffix(path, "/run/stream") {
		return true
	}
	// AI 消息流式接口同样需要跳过普通请求超时。
	return strings.HasPrefix(path, "/api/v1/ai/session/") &&
		strings.HasSuffix(path, "/message/stream")
}

// runAuthStateCleanup 周期清理过期认证状态和验证码。
func runAuthStateCleanup(ctx context.Context, logger *slog.Logger, authStateRepo *iampostgres.AuthStateRepository, captchaStore *iamcaptcha.Store) {
	// cleanup 执行一次清理任务。
	cleanup := func() {
		// 单次清理最多运行 30 秒。
		cleanupCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		// 释放清理上下文 timer。
		defer cancel()
		// 使用 UTC 当前时间作为过期判断基准。
		now := time.Now().UTC()
		// 清理过期认证状态。
		if err := authStateRepo.CleanupExpired(cleanupCtx, now); err != nil {
			logger.Warn("auth state cleanup failed", "error", err)
		}
		// 清理过期验证码。
		if err := captchaStore.CleanupExpired(cleanupCtx, now); err != nil {
			logger.Warn("captcha cleanup failed", "error", err)
		}
	}

	// 启动后立即清理一次，避免等待首个周期。
	cleanup()
	// 后续每小时清理一次。
	ticker := time.NewTicker(time.Hour)
	// 函数退出时停止 ticker。
	defer ticker.Stop()
	// 持续运行直到上下文取消。
	for {
		select {
		case <-ctx.Done():
			// 应用关闭时退出后台任务。
			return
		case <-ticker.C:
			// 到达周期后执行清理。
			cleanup()
		}
	}
}

// runAIStateRecovery 周期性回收 AI 对话与 Agent 运行中残留的"僵尸"状态
// (进程在生成过程中重启/崩溃后留下的 pending/streaming 消息与 running 运行),
// 将其标记为 failed,避免前端看到永远"生成中"的记录。启动时立即执行一次。
func runAIStateRecovery(ctx context.Context, logger *slog.Logger, aiService *aiapplication.Service, agentService *agentapplication.Service) {
	// recover 执行一次僵尸状态恢复。
	recover := func() {
		// 单次恢复最多运行 30 秒。
		recoverCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		// 释放恢复上下文 timer。
		defer cancel()
		// AI service 存在时恢复过期消息。
		if aiService != nil {
			// 将长时间未结束的消息标记为失败。
			if affected, err := aiService.RecoverStaleMessages(recoverCtx, aiapplication.DEFAULT_STALE_AFTER); err != nil {
				logger.Warn("ai stale message recovery failed", "error", err)
			} else if affected > 0 {
				logger.Info("ai stale messages recovered", "count", affected)
			}
		}
		// Agent service 存在时恢复过期运行。
		if agentService != nil {
			// 将长时间未结束的 Agent run 标记为失败。
			if affected, err := agentService.RecoverStaleRuns(recoverCtx, agentapplication.DEFAULT_STALE_AFTER); err != nil {
				logger.Warn("agent stale run recovery failed", "error", err)
			} else if affected > 0 {
				logger.Info("agent stale runs recovered", "count", affected)
			}
		}
	}

	// 启动后立即恢复一次。
	recover()
	// 后续每 10 分钟恢复一次。
	ticker := time.NewTicker(10 * time.Minute)
	// 函数退出时停止 ticker。
	defer ticker.Stop()
	// 持续运行直到上下文取消。
	for {
		select {
		case <-ctx.Done():
			// 应用关闭时退出后台任务。
			return
		case <-ticker.C:
			// 到达周期后执行恢复。
			recover()
		}
	}
}

// userPrincipalResolver 根据 token subject 解析当前用户身份。
type userPrincipalResolver struct {
	repo iamdomain.Repository // repo 用于回查用户信息和状态。
}

// ResolvePrincipal 将 token subject 转换为中间件 Principal。
func (r userPrincipalResolver) ResolvePrincipal(ctx context.Context, subject string) (middleware.Principal, error) {
	// 通过兼容逻辑查找用户。
	user, err := resolvePrincipalUser(ctx, r.repo, subject)
	if err != nil {
		return middleware.Principal{}, err
	}
	// 非启用状态用户不允许通过认证。
	if user.Status != iamapplication.USER_STATUS_ACTIVE {
		return middleware.Principal{}, middleware.ErrUnauthorized
	}

	// 返回认证主体和该用户拥有的角色。
	return middleware.Principal{
		Subject: subject,
		Roles:   rolesForUser(user),
	}, nil
}

// rolesForUser 根据用户信息计算角色列表。
func rolesForUser(user iamdomain.User) []string {
	// 当前约定用户名为 admin 的账户拥有管理员角色。
	if strings.EqualFold(strings.TrimSpace(user.Username), "admin") {
		return []string{middleware.RoleAdmin}
	}
	// 其他用户默认无额外角色。
	return nil
}

// resolvePrincipalUser 兼容旧 subject 和数字 ID 两种用户标识。
func resolvePrincipalUser(ctx context.Context, repo iamdomain.Repository, subject string) (iamdomain.User, error) {
	// 没有仓储时无法解析身份。
	if repo == nil {
		return iamdomain.User{}, middleware.ErrUnauthorized
	}

	// 清理 subject 空白。
	trimmedSubject := strings.TrimSpace(subject)
	// 先尝试按 legacy ID 查询，兼容旧 token。
	if trimmedSubject != "" {
		user, err := repo.GetByLegacyID(ctx, trimmedSubject)
		if err == nil {
			return user, nil
		}
	}
	// 再尝试把 subject 解析为数字用户 ID。
	userID, err := strconv.ParseInt(trimmedSubject, 10, 64)
	// 正数 ID 直接按主键查询用户。
	if err == nil && userID > 0 {
		return repo.Get(ctx, userID)
	}
	// 空 subject 直接拒绝。
	if trimmedSubject == "" {
		return iamdomain.User{}, middleware.ErrUnauthorized
	}
	// 最后再次按 legacy ID 返回具体错误，保留仓储错误语义。
	return repo.GetByLegacyID(ctx, trimmedSubject)
}

// toCORSConfig 将全局配置转换为中间件需要的 CORS 配置。
func toCORSConfig(cfg config.Config) middleware.CORSConfig {
	// 只拷贝 CORS 相关字段，避免中间件依赖完整配置。
	return middleware.CORSConfig{
		AllowedOrigins:   cfg.HTTP.AllowedOrigins,
		AllowCredentials: cfg.HTTP.AllowCredentials,
		AllowHeaders:     cfg.HTTP.AllowHeaders,
		AllowMethods:     cfg.HTTP.AllowMethods,
	}
}
