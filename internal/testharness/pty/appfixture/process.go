package appfixture

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
)

const ProcessConfigEnvName = "KENT_PTY_FIXTURE_CONFIG"

type ProcessConfig struct {
	WorkspaceRoot   string `json:"workspace_root"`
	PersistenceRoot string `json:"persistence_root"`
	ScriptPath      string `json:"script_path"`
	ObservationPath string `json:"observation_path"`
}

func (config ProcessConfig) Validate() error {
	var missing []string
	if strings.TrimSpace(config.WorkspaceRoot) == "" {
		missing = append(missing, "workspace_root")
	}
	if strings.TrimSpace(config.PersistenceRoot) == "" {
		missing = append(missing, "persistence_root")
	}
	if strings.TrimSpace(config.ScriptPath) == "" {
		missing = append(missing, "script_path")
	}
	if strings.TrimSpace(config.ObservationPath) == "" {
		missing = append(missing, "observation_path")
	}
	if len(missing) > 0 {
		return fmt.Errorf("PTY fixture process config missing fields: %s", strings.Join(missing, ", "))
	}
	return nil
}

func WriteProcessConfig(path string, config ProcessConfig) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("PTY fixture process config path is required")
	}
	if err := config.Validate(); err != nil {
		return err
	}
	encoded, err := json.Marshal(config)
	if err != nil {
		return fmt.Errorf("marshal PTY fixture process config: %w", err)
	}
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		return fmt.Errorf("write PTY fixture process config: %w", err)
	}
	return nil
}

func ReadProcessConfig(path string) (ProcessConfig, error) {
	if strings.TrimSpace(path) == "" {
		return ProcessConfig{}, errors.New("PTY fixture process config path is required")
	}
	encoded, err := os.ReadFile(path)
	if err != nil {
		return ProcessConfig{}, fmt.Errorf("read PTY fixture process config: %w", err)
	}
	var config ProcessConfig
	if err := json.Unmarshal(encoded, &config); err != nil {
		return ProcessConfig{}, fmt.Errorf("decode PTY fixture process config: %w", err)
	}
	if err := config.Validate(); err != nil {
		return ProcessConfig{}, err
	}
	return config, nil
}
