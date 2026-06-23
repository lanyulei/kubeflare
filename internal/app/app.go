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
	Config       config.Config                 // Config 保存启动时解析完成的全局配置。
	Logger       *slog.Logger                  // Logger 负责记录应用生命周期日志。
	Server       *http.Server                  // Server 是对外提供 HTTP 服务的实例。
	Health       *health.Manager               // Health 管理 livez/readyz 健康检查状态。
	drainDelay   time.Duration                 // drainDelay 控制关闭前等待流量摘除的时间。
	shutdowners  []func(context.Context) error // shutdowners 按顺序释放外部资源。
	shutdownOnce sync.Once                     // shutdownOnce 保证关闭流程只执行一次。
}

// Run 启动 HTTP 服务，并等待上下文取消或服务异常退出。
func (a *App) Run(ctx context.Context) error {
	// errCh 接收 ListenAndServe 返回的首个非正常错误。
	errCh := make(chan error, 1)

	// HTTP 服务在后台运行，主 goroutine 负责等待退出信号。
	go func() {
		// 记录监听地址，便于启动后定位服务入口。
		a.Logger.Info("http server listening", slog.String("address", a.Server.Addr))
		// http.ErrServerClosed 是优雅关闭的正常返回，不作为错误上报。
		if err := a.Server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			// 将真实启动/运行错误交给主流程处理。
			errCh <- err
		}
		// 关闭通道表示 HTTP 服务 goroutine 已结束。
		close(errCh)
	}()

	// 同时等待外部取消和 HTTP 服务自身退出。
	select {
	case <-ctx.Done():
		// 外部取消时，为优雅关闭创建独立超时。
		shutdownCtx, cancel := context.WithTimeout(context.Background(), a.Config.HTTP.ShutdownTimeout)
		// 释放 shutdownCtx 定时器资源。
		defer cancel()
		// 执行统一关闭流程。
		return a.Shutdown(shutdownCtx)
	case err := <-errCh:
		// HTTP 服务异常退出时直接返回错误；正常关闭时 err 为 nil。
		return err
	}
}

// Shutdown 优雅停止 HTTP 服务，并释放应用持有的外部资源。
func (a *App) Shutdown(ctx context.Context) error {
	// shutdownErr 保存关闭流程中遇到的第一个错误。
	var shutdownErr error

	// 关闭逻辑只允许执行一次，避免重复释放资源。
	a.shutdownOnce.Do(func() {
		// 标记关闭开始，方便日志追踪。
		a.Logger.Info("starting graceful shutdown")
		// readyz 进入 draining 状态，让负载均衡停止导入新流量。
		a.Health.SetDraining(true)

		// 配置了 drainDelay 时，先等待外部流量完成摘除。
		if a.drainDelay > 0 {
			// 使用 timer 便于在 ctx 取消时主动停止。
			timer := time.NewTimer(a.drainDelay)
			// 等待 drain 时间结束或关闭上下文提前取消。
			select {
			case <-ctx.Done():
				// 上下文已取消时停止 timer，避免资源泄漏。
				timer.Stop()
			case <-timer.C:
				// drain 时间到，继续执行服务关闭。
			}
		}

		// 先停止 HTTP Server，等待正在处理的请求完成。
		if err := a.Server.Shutdown(ctx); err != nil && shutdownErr == nil {
			// 只记录第一个关闭错误，保留最早失败原因。
			shutdownErr = err
		}

		// 资源清理(trace flush / redis / db)使用独立超时,避免被前面 drain +
		// Server.Shutdown 已耗尽的 ctx 连带取消,导致连接未正常关闭、trace 丢失。
		// cleanupTimeout 默认复用 HTTP 关闭超时。
		cleanupTimeout := a.Config.HTTP.ShutdownTimeout
		// 未配置关闭超时时，给资源清理一个兜底时间。
		if cleanupTimeout <= 0 {
			cleanupTimeout = 10 * time.Second
		}
		// 使用后台上下文创建清理超时，避免继承已取消的请求上下文。
		cleanupCtx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
		// 关闭清理上下文的 timer。
		defer cancel()
		// 按注册顺序依次释放资源。
		for _, shutdowner := range a.shutdowners {
			// 执行单个资源释放函数，并保留第一个错误。
			if err := shutdowner(cleanupCtx); err != nil && shutdownErr == nil {
				shutdownErr = err
			}
		}
	})

	// 返回关闭流程中的第一个错误；无错误时返回 nil。
	return shutdownErr
}
