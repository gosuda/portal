package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/gosuda/portal/v2/utils"
)

const (
	cliConfigDirName  = "portal"
	cliConfigFileName = "config.json"
)

type cliConfig struct {
	ClientID string   `json:"client_id,omitempty"`
	Relays   []string `json:"relays,omitempty"`
}

func loadCLIConfig() (cliConfig, string, error) {
	path, err := cliConfigPath()
	if err != nil {
		return cliConfig{}, "", err
	}

	cfg := cliConfig{}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return cfg, path, nil
		}
		return cfg, path, err
	}
	if len(data) == 0 {
		return cfg, path, nil
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cliConfig{}, path, err
	}
	if len(cfg.Relays) > 0 {
		cfg.Relays, err = utils.NormalizeRelayURLs(cfg.Relays)
		if err != nil {
			return cliConfig{}, path, err
		}
	}
	return cfg, path, nil
}

func saveCLIConfig(path string, cfg cliConfig) error {
	if strings.TrimSpace(path) == "" {
		var err error
		path, err = cliConfigPath()
		if err != nil {
			return err
		}
	}

	if len(cfg.Relays) > 0 {
		normalizedRelays, err := utils.NormalizeRelayURLs(cfg.Relays)
		if err != nil {
			return err
		}
		cfg.Relays = normalizedRelays
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}

func cliConfigPath() (string, error) {
	baseDir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(baseDir, cliConfigDirName, cliConfigFileName), nil
}
