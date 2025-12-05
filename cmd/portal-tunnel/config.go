package main

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
	"gosuda.org/portal/sdk"
)

var defaultProtocols = []string{"http/1.1", "h2"}

// ServiceConfig describes a local service exposed through the tunnel.
type ServiceConfig struct {
	Name      string       `yaml:"name"`
	Target    string       `yaml:"target"`
	Protocols []string     `yaml:"protocols"`
	Metadata  sdk.Metadata `yaml:"metadata,omitempty"`
}

// TunnelConfig represents the YAML configuration schema for portal-tunnel.
type TunnelConfig struct {
	Relays  []string      `yaml:"relays"`
	Service ServiceConfig `yaml:"service"`
}

// LoadConfig reads the YAML file at path, parses it into TunnelConfig, and validates it for single-service use.
func LoadConfig(path string) (*TunnelConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	var cfg TunnelConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	cfg.applyDefaults()

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

	service := cfg.Service
	name := strings.TrimSpace(service.Name)
	if name == "" {
		errs = append(errs, "service: name is required")
	}
	target := strings.TrimSpace(service.Target)
	if target == "" {
		errs = append(errs, "service: target is required")
	}
	for i, proto := range service.Protocols {
		if strings.TrimSpace(proto) == "" {
			errs = append(errs, fmt.Sprintf("service.protocols[%d]: protocol cannot be empty", i))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("invalid config:\n - %s", strings.Join(errs, "\n - "))
	}

	return nil
}

func (cfg *TunnelConfig) applyDefaults() {
	applyServiceDefaults(&cfg.Service)
}

func applyServiceDefaults(svc *ServiceConfig) {
	if len(svc.Protocols) == 0 {
		svc.Protocols = append([]string(nil), defaultProtocols...)
	}
}
