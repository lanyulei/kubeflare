package app

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/lanyulei/kubeflare/internal/platform/config"
	"github.com/lanyulei/kubeflare/internal/shared/health"
)

type App struct {
	Config       config.Config
	Logger       *slog.Logger
	Server       *http.Server
	Health       *health.Manager
	drainDelay   time.Duration
	shutdowners  []func(context.Context) error
	shutdownOnce sync.Once
}

func (a *App) Run(ctx context.Context) error {
	errCh := make(chan error, 1)

	go func() {
		a.Logger.Info("http server listening", slog.String("address", a.Server.Addr))
		if err := a.Server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), a.Config.HTTP.ShutdownTimeout)
		defer cancel()
		return a.Shutdown(shutdownCtx)
	case err := <-errCh:
		return err
	}
}

func (a *App) Shutdown(ctx context.Context) error {
	var shutdownErr error

	a.shutdownOnce.Do(func() {
		a.Logger.Info("starting graceful shutdown")
		a.Health.SetDraining(true)

		if a.drainDelay > 0 {
			timer := time.NewTimer(a.drainDelay)
			select {
			case <-ctx.Done():
				timer.Stop()
			case <-timer.C:
			}
		}

		if err := a.Server.Shutdown(ctx); err != nil && shutdownErr == nil {
			shutdownErr = err
		}

		// 资源清理(trace flush / redis / db)使用独立超时,避免被前面 drain +
		// Server.Shutdown 已耗尽的 ctx 连带取消,导致连接未正常关闭、trace 丢失。
		cleanupTimeout := a.Config.HTTP.ShutdownTimeout
		if cleanupTimeout <= 0 {
			cleanupTimeout = 10 * time.Second
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
		defer cancel()
		for _, shutdowner := range a.shutdowners {
			if err := shutdowner(cleanupCtx); err != nil && shutdownErr == nil {
				shutdownErr = err
			}
		}
	})

	return shutdownErr
}
