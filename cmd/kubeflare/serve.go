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
	rootCmd.AddCommand(newServeCommand())
}

func newServeCommand() *cobra.Command {
	var configPath string

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start the Kubeflare HTTP service",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := configpkg.Load(configPath, cmd.Flags())
			if err != nil {
				return err
			}

			ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()

			application, err := app.New(ctx, cfg)
			if err != nil {
				return err
			}

			return application.Run(ctx)
		},
	}

	flags := cmd.Flags()
	flags.StringVar(&configPath, "config", "", "path to the configuration file")
	flags.String("http.address", configpkg.Default().HTTP.Address, "http listen address")
	flags.String("service.environment", configpkg.Default().Service.Environment, "service environment")
	flags.String("database.dsn", "", "postgres dsn")
	flags.Bool("database.enabled", false, "enable postgres")
	flags.String("redis.address", configpkg.Default().Redis.Address, "redis address")
	flags.Bool("redis.enabled", false, "enable redis")
	flags.String("auth.signing_key", "", "token signing key")
	flags.String("secrets.encryption_key", "", "hex-encoded 32-byte key for encrypting cluster secrets")
	return cmd
}
