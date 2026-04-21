package config

import (
	"errors"
	"fmt"
)

func Validate(cfg Config) error {
	if cfg.Service.Name == "" {
		return errors.New("service.name is required")
	}
	if cfg.HTTP.Address == "" {
		return errors.New("http.address is required")
	}
	if cfg.HTTP.MaxHeaderBytes <= 0 {
		return errors.New("http.max_header_bytes must be positive")
	}
	if cfg.Database.Enabled && cfg.Database.DSN == "" {
		return errors.New("database.dsn is required when database is enabled")
	}
	if cfg.Redis.Enabled && cfg.Redis.Address == "" {
		return errors.New("redis.address is required when redis is enabled")
	}
	if len(cfg.Proxy.AllowedRoles) == 0 {
		return fmt.Errorf("proxy.allowed_roles must not be empty")
	}
	return nil
}
