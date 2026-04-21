package config

import (
	"fmt"
	"strings"

	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/env"
	"github.com/knadh/koanf/providers/file"
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
		if err := k.Load(file.Provider(configPath), yaml.Parser()); err != nil {
			return Config{}, fmt.Errorf("load config file %s: %w", configPath, err)
		}
	}

	if err := k.Load(env.Provider("KUBEFLARE_", ".", func(s string) string {
		s = strings.TrimPrefix(s, "KUBEFLARE_")
		s = strings.ToLower(s)
		s = strings.ReplaceAll(s, "__", ".")
		return s
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
