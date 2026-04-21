package app

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/go-playground/validator/v10"
	"go.opentelemetry.io/contrib/instrumentation/github.com/gin-gonic/gin/otelgin"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"github.com/lanyulei/kubeflare/internal/module/cluster/application"
	clusterpostgres "github.com/lanyulei/kubeflare/internal/module/cluster/infrastructure/postgres"
	clusterhttp "github.com/lanyulei/kubeflare/internal/module/cluster/interface/http"
	iamapplication "github.com/lanyulei/kubeflare/internal/module/iam/application"
	iampostgres "github.com/lanyulei/kubeflare/internal/module/iam/infrastructure/postgres"
	iamhttp "github.com/lanyulei/kubeflare/internal/module/iam/interface/http"
	kubeproxyapp "github.com/lanyulei/kubeflare/internal/module/kubeproxy/application"
	kubeproxy "github.com/lanyulei/kubeflare/internal/module/kubeproxy/infrastructure/proxy"
	"github.com/lanyulei/kubeflare/internal/platform/cache"
	"github.com/lanyulei/kubeflare/internal/platform/config"
	"github.com/lanyulei/kubeflare/internal/platform/db"
	"github.com/lanyulei/kubeflare/internal/platform/httpx"
	logpkg "github.com/lanyulei/kubeflare/internal/platform/log"
	"github.com/lanyulei/kubeflare/internal/platform/metrics"
	"github.com/lanyulei/kubeflare/internal/platform/secrets"
	tracepkg "github.com/lanyulei/kubeflare/internal/platform/trace"
	"github.com/lanyulei/kubeflare/internal/shared/health"
	"github.com/lanyulei/kubeflare/internal/shared/middleware"
	"github.com/lanyulei/kubeflare/internal/shared/response"
)

func New(ctx context.Context, cfg config.Config) (*App, error) {
	logger := logpkg.New(cfg.Observability)

	traceShutdown, err := tracepkg.Setup(ctx, cfg.Observability.Tracing)
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

	encryptor := secrets.NoopEncryptor{}
	userRepo := iampostgres.NewUserRepository(gormDB)
	clusterRepo := clusterpostgres.NewClusterRepository(gormDB, encryptor)
	clusterRegistry := application.NewCachedRegistry(logger, clusterRepo, redisClient, cfg.Proxy.ClusterCacheTTL)

	authenticator := middleware.NewStaticTokenAuthenticator(buildBootstrapPrincipals(cfg))
	iamService := iamapplication.NewService(userRepo, validator)
	clusterService := application.NewService(clusterRepo, validator, clusterRegistry)

	apiHandler, err := newAPIHandler(cfg, logger, authenticator, iamService, clusterService)
	if err != nil {
		return nil, err
	}

	transportPool := kubeproxy.NewTransportPool(cfg.Proxy)
	proxyHandler := middleware.AuthenticateHTTP(authenticator, kubeproxy.NewHandler(kubeproxy.HandlerOptions{
		DefaultClusterID: cfg.Proxy.DefaultClusterID,
		Registry:         clusterRegistry,
		Authorizer: kubeproxyapp.RoleAuthorizer{
			AllowedRoles: cfg.Proxy.AllowedRoles,
		},
		TransportBuilder: transportPool.For,
		FlushInterval:    cfg.Proxy.FlushInterval,
	}))

	healthManager := health.NewManager(
		cfg.HTTP.ReadinessTimeout,
		health.FuncChecker{
			CheckName: "postgres",
			CheckFunc: func(ctx context.Context) error {
				return db.Ping(ctx, gormDB)
			},
		},
		health.FuncChecker{
			CheckName: "redis",
			CheckFunc: func(ctx context.Context) error {
				if redisClient == nil {
					return nil
				}
				return redisClient.Ping(ctx).Err()
			},
		},
	)

	rootHandler := NewRootHandler(RootHandlerOptions{
		LivezHandler:   healthManager.LiveHandler(),
		ReadyzHandler:  healthManager.ReadyHandler(),
		MetricsHandler: metricsRegistry.Handler(),
		APIHandler:     apiHandler,
		KAPIHandler:    proxyHandler,
		KAPIsHandler:   proxyHandler,
	})

	rootHandler = metrics.InstrumentHTTP(metricsRegistry, rootHandler)
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
			traceShutdown,
			func(context.Context) error { return cache.Close(redisClient) },
			func(context.Context) error { return db.Close(gormDB) },
		},
	}, nil
}

func newAPIHandler(
	cfg config.Config,
	logger *slog.Logger,
	authenticator middleware.Authenticator,
	iamService *iamapplication.Service,
	clusterService *application.Service,
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
	api.Use(middleware.AuthenticateGin(authenticator))
	api.GET("/system/info", func(c *gin.Context) {
		principal, _ := middleware.PrincipalFromContext(c.Request.Context())
		response.OK(c, http.StatusOK, gin.H{
			"service":     cfg.Service.Name,
			"environment": cfg.Service.Environment,
			"subject":     principal.Subject,
			"roles":       principal.Roles,
		})
	})

	iamhttp.RegisterRoutes(api, iamhttp.NewHandler(iamService))
	clusterhttp.RegisterRoutes(api, clusterhttp.NewHandler(clusterService))

	var handler http.Handler = engine
	if cfg.HTTP.APIRequestTimeout > 0 {
		handler = http.TimeoutHandler(handler, cfg.HTTP.APIRequestTimeout, `{"code":"TIMEOUT","message":"request timed out"}`)
	}
	return handler, nil
}

func buildBootstrapPrincipals(cfg config.Config) map[string]middleware.Principal {
	principals := make(map[string]middleware.Principal, len(cfg.Auth.BootstrapTokens))
	for token, principal := range cfg.Auth.BootstrapTokens {
		principals[token] = middleware.Principal{
			Subject: principal.Subject,
			Roles:   principal.Roles,
		}
	}
	return principals
}

func toCORSConfig(cfg config.Config) middleware.CORSConfig {
	return middleware.CORSConfig{
		AllowedOrigins:   cfg.HTTP.AllowedOrigins,
		AllowCredentials: cfg.HTTP.AllowCredentials,
		AllowHeaders:     cfg.HTTP.AllowHeaders,
		AllowMethods:     cfg.HTTP.AllowMethods,
	}
}
