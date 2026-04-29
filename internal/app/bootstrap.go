package app

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/dchest/captcha"
	"github.com/gin-gonic/gin"
	"github.com/go-playground/validator/v10"
	"go.opentelemetry.io/contrib/instrumentation/github.com/gin-gonic/gin/otelgin"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

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
	uploadRepo := uploadlocal.NewFileRepository(cfg.Upload.RootDir)
	clusterRepo := clusterpostgres.NewClusterRepository(gormDB, cfg.Database.QueryTimeout)
	clusterInspector := clusterkubernetes.NewInspector(cfg.Database.QueryTimeout)

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
	uploadService := uploadapplication.NewService(uploadRepo, validator, "/api/v1/upload")
	clusterService := clusterapplication.NewService(clusterRepo, validator, encryptor, clusterInspector)

	apiHandler, err := newAPIHandler(cfg, logger, authenticator, iamService, oidcService, uploadService, clusterService)
	if err != nil {
		return nil, err
	}
	authCleanupCtx, stopAuthCleanup := context.WithCancel(context.Background())
	go runAuthStateCleanup(authCleanupCtx, logger, authStateRepo, captchaStore)

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

func newAPIHandler(
	cfg config.Config,
	logger *slog.Logger,
	authenticator middleware.Authenticator,
	iamService *iamapplication.Service,
	oidcService *iamapplication.OIDCService,
	uploadService *uploadapplication.Service,
	clusterService *clusterapplication.Service,
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
	iamhttp.RegisterAdminRoutes(protectedAPI, iamHandler)
	uploadhttp.RegisterProtectedRoutes(protectedAPI, uploadHandler)
	clusterHandler := clusterhttp.NewHandler(clusterService)
	clusterhttp.RegisterRoutes(protectedAPI, clusterHandler)

	var handler http.Handler = engine
	if cfg.HTTP.APIRequestTimeout > 0 {
		handler = middleware.TimeoutHTTP(cfg.HTTP.APIRequestTimeout, handler)
	}
	return handler, nil
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
