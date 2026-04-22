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
	if cfg.HTTP.AllowCredentials {
		for _, origin := range cfg.HTTP.AllowedOrigins {
			if origin == "*" {
				return errors.New("http.allowed_origins cannot contain * when http.allow_credentials is true")
			}
		}
	}
	if cfg.Database.Enabled && cfg.Database.DSN == "" {
		return errors.New("database.dsn is required when database is enabled")
	}
	if cfg.Redis.Enabled && cfg.Redis.Address == "" {
		return errors.New("redis.address is required when redis is enabled")
	}
	if cfg.Auth.BootstrapToken != "" && cfg.Auth.BootstrapSubject == "" {
		return errors.New("auth.bootstrap_subject is required when auth.bootstrap_token is set")
	}
	if cfg.Auth.BootstrapToken != "" && len(cfg.Auth.BootstrapRoles) == 0 {
		return errors.New("auth.bootstrap_roles must not be empty when auth.bootstrap_token is set")
	}
	if len(cfg.Proxy.AllowedRoles) == 0 {
		return fmt.Errorf("proxy.allowed_roles must not be empty")
	}
	if (cfg.Database.Enabled || cfg.Redis.Enabled) && cfg.Proxy.EncryptionKey == "" {
		return errors.New("proxy.encryption_key is required when database or redis is enabled")
	}
	return nil
}
