package cache

import (
	"context"
	"fmt"

	"github.com/redis/go-redis/v9"

	configpkg "github.com/lanyulei/kubeflare/internal/platform/config"
)

func NewRedisClient(cfg configpkg.RedisConfig) (*redis.Client, error) {
	if !cfg.Enabled {
		return nil, nil
	}

	client := redis.NewClient(&redis.Options{
		Addr:         cfg.Address,
		Username:     cfg.Username,
		Password:     cfg.Password,
		DB:           cfg.DB,
		DialTimeout:  cfg.DialTimeout,
		ReadTimeout:  cfg.ReadTimeout,
		WriteTimeout: cfg.WriteTimeout,
		PoolTimeout:  cfg.PoolTimeout,
		MinIdleConns: cfg.MinIdleConns,
		MaxIdleConns: cfg.MaxIdleConns,
		PoolSize:     cfg.PoolSize,
	})

	pingCtx, cancel := context.WithTimeout(context.Background(), cfg.HealthCheckTimeout)
	defer cancel()

	if err := client.Ping(pingCtx).Err(); err != nil {
		return nil, fmt.Errorf("ping redis: %w", err)
	}

	return client, nil
}

func Close(client *redis.Client) error {
	if client == nil {
		return nil
	}
	return client.Close()
}
