package config

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/env"
	"github.com/knadh/koanf/providers/posflag"
	"github.com/knadh/koanf/v2"
	"github.com/spf13/pflag"
)

func Load(configPath string, flags *pflag.FlagSet) (Config, error) {
	k := koanf.New(".")
	cfg := Default()

	if err := k.Load(posflag.Provider(flags, ".", k), nil); err != nil {
		return Config{}, fmt.Errorf("load flag defaults: %w", err)
	}

	if configPath != "" {
		configBytes, err := os.ReadFile(configPath)
		if err != nil {
			return Config{}, fmt.Errorf("read config file %s: %w", configPath, err)
		}

		expandedConfig := os.ExpandEnv(string(configBytes))
		if err := k.Load(bytesProvider([]byte(expandedConfig)), yaml.Parser()); err != nil {
			return Config{}, fmt.Errorf("load config file %s: %w", configPath, err)
		}
	}

	if err := k.Load(env.ProviderWithValue("KUBEFLARE_", ".", func(key string, value string) (string, interface{}) {
		path := envKeyToPath(key)
		if path == "" {
			return "", nil
		}
		return path, normalizeEnvValue(path, value)
	}), nil); err != nil {
		return Config{}, fmt.Errorf("load env config: %w", err)
	}

	if err := k.Load(posflag.Provider(flags, ".", k), nil); err != nil {
		return Config{}, fmt.Errorf("load flags: %w", err)
	}

	if err := k.Unmarshal("", &cfg); err != nil {
		return Config{}, fmt.Errorf("unmarshal config: %w", err)
	}

	if err := Validate(cfg); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

func envKeyToPath(key string) string {
	key = strings.TrimPrefix(key, "KUBEFLARE_")
	key = strings.ToLower(key)
	key = strings.ReplaceAll(key, "__", ".")
	return key
}

func normalizeEnvValue(path string, value string) interface{} {
	switch {
	case path == "http.trusted_proxies":
		return splitEnvList(value)
	case path == "http.allowed_origins":
		return splitEnvList(value)
	case path == "http.allow_headers":
		return splitEnvList(value)
	case path == "http.allow_methods":
		return splitEnvList(value)
	case path == "auth.oidc.scopes":
		return splitEnvList(value)
	default:
		return value
	}
}

func splitEnvList(value string) []string {
	if strings.TrimSpace(value) == "" {
		return []string{}
	}

	parts := strings.Split(value, ",")
	items := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			items = append(items, part)
		}
	}
	return items
}

type inlineBytesProvider []byte

func bytesProvider(value []byte) inlineBytesProvider {
	copyValue := make([]byte, len(value))
	copy(copyValue, value)
	return inlineBytesProvider(copyValue)
}

func (p inlineBytesProvider) ReadBytes() ([]byte, error) {
	return p, nil
}

func (p inlineBytesProvider) Read() (map[string]interface{}, error) {
	return nil, errors.New("inline bytes provider does not support Read")
}
