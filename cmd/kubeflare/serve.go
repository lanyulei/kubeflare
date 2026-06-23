package main

import (
	"context"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/lanyulei/kubeflare/internal/app"
	configpkg "github.com/lanyulei/kubeflare/internal/platform/config"
)

func init() {
	// 进程启动时，将服务启动命令注册到根命令下。
	rootCmd.AddCommand(newServeCommand())
}

// newServeCommand 构建 `kubeflare serve` 命令及其运行参数。
func newServeCommand() *cobra.Command {
	var configPath string

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start the Kubeflare HTTP service",
		RunE: func(cmd *cobra.Command, _ []string) error {
			// 先读取配置文件，再用命令行参数覆盖支持的配置项。
			cfg, err := configpkg.Load(configPath, cmd.Flags())
			if err != nil {
				return err
			}

			// 收到 SIGINT 或 SIGTERM 时，通知服务优雅退出。
			ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()

			// 使用最终配置创建应用实例，然后启动服务。
			application, err := app.New(ctx, cfg)
			if err != nil {
				return err
			}

			return application.Run(ctx)
		},
	}

	flags := cmd.Flags()
	// 配置来源。
	flags.StringVar(&configPath, "config", "", "path to the configuration file")

	// 核心服务配置。
	flags.String("http.address", configpkg.Default().HTTP.Address, "http listen address")
	flags.String("service.environment", configpkg.Default().Service.Environment, "service environment")

	// 可选基础设施依赖。
	flags.String("database.dsn", "", "postgres dsn")
	flags.Bool("database.enabled", false, "enable postgres")
	flags.String("redis.address", configpkg.Default().Redis.Address, "redis address")
	flags.Bool("redis.enabled", false, "enable redis")

	// 敏感安全配置需显式传入，不使用默认值。
	flags.String("auth.signing_key", "", "token signing key")
	flags.String("secrets.encryption_key", "", "hex-encoded 32-byte key for encrypting cluster secrets")
	return cmd
}
