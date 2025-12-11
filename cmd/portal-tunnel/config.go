package main

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
	"gosuda.org/portal/sdk"
)

var defaultProtocols = []string{"http/1.1", "h2"}

type AppConfig struct {
	Name      string       `yaml:"name"`
	Target    string       `yaml:"target"`
	Protocols []string     `yaml:"protocols"`
	Metadata  sdk.Metadata `yaml:"metadata,omitempty"`
}

type TunnelConfig struct {
	Relays []string  `yaml:"relays"`
	App    AppConfig `yaml:"app"`
}

func LoadConfig(path string) (*TunnelConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	var cfg TunnelConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if len(cfg.App.Protocols) == 0 {
		cfg.App.Protocols = append([]string(nil), defaultProtocols...)
	}

	if err := cfg.validate(); err != nil {
		return nil, err
	}

	return &cfg, nil
}

func (cfg *TunnelConfig) validate() error {
	var errs []string

	if len(cfg.Relays) == 0 {
		errs = append(errs, "at least one relay must be defined")
	}
	for i, url := range cfg.Relays {
		if strings.TrimSpace(url) == "" {
			errs = append(errs, fmt.Sprintf("relays[%d]: url cannot be empty", i))
		}
	}

	app := cfg.App
	name := strings.TrimSpace(app.Name)
	if name == "" {
		errs = append(errs, "app: name is required")
	}
	target := strings.TrimSpace(app.Target)
	if target == "" {
		errs = append(errs, "app: target is required")
	}
	for i, proto := range app.Protocols {
		if strings.TrimSpace(proto) == "" {
			errs = append(errs, fmt.Sprintf("app.protocols[%d]: protocol cannot be empty", i))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("invalid config:\n - %s", strings.Join(errs, "\n - "))
	}

	return nil
}
