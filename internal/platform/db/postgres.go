package db

import (
	"context"
	"fmt"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	configpkg "github.com/lanyulei/kubeflare/internal/platform/config"
)

func OpenPostgres(cfg configpkg.DatabaseConfig) (*gorm.DB, error) {
	if !cfg.Enabled {
		return nil, nil
	}

	gormDB, err := gorm.Open(postgres.Open(cfg.DSN), &gorm.Config{
		TranslateError: true,
		Logger:         logger.Default.LogMode(logger.Error),
	})
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}

	sqlDB, err := gormDB.DB()
	if err != nil {
		return nil, fmt.Errorf("acquire sql db: %w", err)
	}

	sqlDB.SetMaxOpenConns(cfg.MaxOpenConns)
	sqlDB.SetMaxIdleConns(cfg.MaxIdleConns)
	sqlDB.SetConnMaxLifetime(cfg.ConnMaxLifetime)
	sqlDB.SetConnMaxIdleTime(cfg.ConnMaxIdleTime)

	return gormDB, nil
}

func Ping(ctx context.Context, gormDB *gorm.DB) error {
	if gormDB == nil {
		return nil
	}

	sqlDB, err := gormDB.DB()
	if err != nil {
		return err
	}

	return sqlDB.PingContext(ctx)
}

func Close(gormDB *gorm.DB) error {
	if gormDB == nil {
		return nil
	}

	sqlDB, err := gormDB.DB()
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- sqlDB.Close()
	}()

	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}
