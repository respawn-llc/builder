package appfixture

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
)

const ConfiguredClientProcessConfigEnvName = "KENT_PTY_CONFIGURED_CLIENT_CONFIG"

type ConfiguredClientProcessConfig struct {
	WorkspaceRoot            string `json:"workspace_root"`
	PersistenceRoot          string `json:"persistence_root"`
	ConfiguredServerEndpoint string `json:"configured_server_endpoint"`
}

func (config ConfiguredClientProcessConfig) Validate() error {
	if strings.TrimSpace(config.WorkspaceRoot) == "" {
		return errors.New("configured client process workspace_root is required")
	}
	if strings.TrimSpace(config.PersistenceRoot) == "" {
		return errors.New("configured client process persistence_root is required")
	}
	endpoint, err := url.Parse(strings.TrimSpace(config.ConfiguredServerEndpoint))
	if err != nil || endpoint.Scheme != "ws" || endpoint.Host == "" || endpoint.Path != "/rpc" {
		return errors.New("configured client process configured_server_endpoint must be a ws /rpc endpoint")
	}
	return nil
}

func WriteConfiguredClientProcessConfig(path string, config ConfiguredClientProcessConfig) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("configured client process config path is required")
	}
	if err := config.Validate(); err != nil {
		return err
	}
	encoded, err := json.Marshal(config)
	if err != nil {
		return fmt.Errorf("marshal configured client process config: %w", err)
	}
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		return fmt.Errorf("write configured client process config: %w", err)
	}
	return nil
}

func ReadConfiguredClientProcessConfig(path string) (ConfiguredClientProcessConfig, error) {
	if strings.TrimSpace(path) == "" {
		return ConfiguredClientProcessConfig{}, errors.New("configured client process config path is required")
	}
	encoded, err := os.ReadFile(path)
	if err != nil {
		return ConfiguredClientProcessConfig{}, fmt.Errorf("read configured client process config: %w", err)
	}
	var config ConfiguredClientProcessConfig
	if err := json.Unmarshal(encoded, &config); err != nil {
		return ConfiguredClientProcessConfig{}, fmt.Errorf("decode configured client process config: %w", err)
	}
	if err := config.Validate(); err != nil {
		return ConfiguredClientProcessConfig{}, err
	}
	return config, nil
}
