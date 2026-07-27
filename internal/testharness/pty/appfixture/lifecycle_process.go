package appfixture

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"core/shared/lifecyclecontract"
)

const LifecycleProcessConfigEnvName = "KENT_LIFECYCLE_PTY_FIXTURE_CONFIG"
const LifecycleServerProcessConfigEnvName = "KENT_LIFECYCLE_SERVER_PTY_FIXTURE_CONFIG"

type LifecycleServerMode string

const (
	LifecycleServerModeLocal  LifecycleServerMode = "local"
	LifecycleServerModeRemote LifecycleServerMode = "remote"
)

type LifecycleHookBehavior string

const (
	LifecycleHookBehaviorSuccess     LifecycleHookBehavior = "success"
	LifecycleHookBehaviorNonzeroOnce LifecycleHookBehavior = "nonzero_once"
	LifecycleHookBehaviorNonzero     LifecycleHookBehavior = "nonzero"
)

type LifecycleProcessConfig struct {
	WorkspaceRoot             string                           `json:"workspace_root"`
	PersistenceRoot           string                           `json:"persistence_root"`
	ServerMode                LifecycleServerMode              `json:"server_mode"`
	LocalScriptPath           *string                          `json:"local_script_path,omitempty"`
	InitialPrompt             string                           `json:"initial_prompt"`
	TargetFinalAssistantCount uint64                           `json:"target_final_assistant_count"`
	HookRecordPath            string                           `json:"hook_record_path"`
	HookBehavior              LifecycleHookBehavior            `json:"hook_behavior"`
	HookStatePath             *string                          `json:"hook_state_path,omitempty"`
	HookObservationBarrier    *LifecycleHookObservationBarrier `json:"hook_observation_barrier,omitempty"`
}

type LifecycleHookObservationBarrier struct {
	RequiredCategories []lifecyclecontract.Category `json:"required_categories"`
}

type LifecycleServerProcessConfig struct {
	WorkspaceRoot   string `json:"workspace_root"`
	PersistenceRoot string `json:"persistence_root"`
	ScriptPath      string `json:"script_path"`
	ReadyPath       string `json:"ready_path"`
	HookRecordPath  string `json:"hook_record_path"`
}

type LifecycleServerProcessReady struct {
	PID int `json:"pid"`
}

func (config LifecycleProcessConfig) Validate() error {
	if strings.TrimSpace(config.WorkspaceRoot) == "" {
		return errors.New("lifecycle PTY fixture workspace_root is required")
	}
	if strings.TrimSpace(config.PersistenceRoot) == "" {
		return errors.New("lifecycle PTY fixture persistence_root is required")
	}
	switch config.ServerMode {
	case LifecycleServerModeLocal:
		if config.LocalScriptPath == nil || strings.TrimSpace(*config.LocalScriptPath) == "" {
			return errors.New("local lifecycle PTY fixture requires local_script_path")
		}
	case LifecycleServerModeRemote:
		if config.LocalScriptPath != nil {
			return errors.New("remote lifecycle PTY fixture cannot contain local_script_path")
		}
	default:
		return errors.New("lifecycle PTY fixture server_mode is invalid")
	}
	if strings.TrimSpace(config.InitialPrompt) == "" {
		return errors.New("lifecycle PTY fixture initial_prompt is required")
	}
	if config.TargetFinalAssistantCount == 0 {
		return errors.New("lifecycle PTY fixture target_final_assistant_count is required")
	}
	if strings.TrimSpace(config.HookRecordPath) == "" {
		return errors.New("lifecycle PTY fixture hook_record_path is required")
	}
	if config.HookObservationBarrier != nil {
		if err := config.HookObservationBarrier.Validate(); err != nil {
			return err
		}
	}
	switch config.HookBehavior {
	case LifecycleHookBehaviorSuccess:
		if config.HookStatePath != nil {
			return errors.New("successful lifecycle hook cannot contain hook_state_path")
		}
	case LifecycleHookBehaviorNonzeroOnce:
		if config.HookStatePath == nil || strings.TrimSpace(*config.HookStatePath) == "" {
			return errors.New("non-zero-once lifecycle hook requires hook_state_path")
		}
	case LifecycleHookBehaviorNonzero:
		if config.HookStatePath != nil {
			return errors.New("non-zero lifecycle hook cannot contain hook_state_path")
		}
	default:
		return errors.New("lifecycle PTY fixture hook_behavior is invalid")
	}
	return nil
}

func (barrier LifecycleHookObservationBarrier) Validate() error {
	if len(barrier.RequiredCategories) == 0 {
		return errors.New("lifecycle hook observation barrier requires categories")
	}
	seen := make(map[lifecyclecontract.Category]struct{}, len(barrier.RequiredCategories))
	for _, category := range barrier.RequiredCategories {
		switch category {
		case lifecyclecontract.CategorySessionStart,
			lifecyclecontract.CategoryTaskComplete,
			lifecyclecontract.CategoryTaskError,
			lifecyclecontract.CategoryInputRequired,
			lifecyclecontract.CategoryResourceLimit:
		default:
			return fmt.Errorf("lifecycle hook observation barrier category %q is invalid", category)
		}
		if _, exists := seen[category]; exists {
			return fmt.Errorf("lifecycle hook observation barrier category %q is duplicated", category)
		}
		seen[category] = struct{}{}
	}
	return nil
}

func (config LifecycleServerProcessConfig) Validate() error {
	if strings.TrimSpace(config.WorkspaceRoot) == "" {
		return errors.New("lifecycle server PTY fixture workspace_root is required")
	}
	if strings.TrimSpace(config.PersistenceRoot) == "" {
		return errors.New("lifecycle server PTY fixture persistence_root is required")
	}
	if strings.TrimSpace(config.ScriptPath) == "" {
		return errors.New("lifecycle server PTY fixture script_path is required")
	}
	if strings.TrimSpace(config.ReadyPath) == "" {
		return errors.New("lifecycle server PTY fixture ready_path is required")
	}
	if strings.TrimSpace(config.HookRecordPath) == "" {
		return errors.New("lifecycle server PTY fixture hook_record_path is required")
	}
	return nil
}

func WriteLifecycleProcessConfig(path string, config LifecycleProcessConfig) error {
	return writeLifecycleProcessConfig(path, config, config.Validate)
}

func ReadLifecycleProcessConfig(path string) (LifecycleProcessConfig, error) {
	var config LifecycleProcessConfig
	if err := readLifecycleProcessConfig(path, &config, func() error { return config.Validate() }); err != nil {
		return LifecycleProcessConfig{}, err
	}
	return config, nil
}

func WriteLifecycleServerProcessConfig(path string, config LifecycleServerProcessConfig) error {
	return writeLifecycleProcessConfig(path, config, config.Validate)
}

func ReadLifecycleServerProcessConfig(path string) (LifecycleServerProcessConfig, error) {
	var config LifecycleServerProcessConfig
	if err := readLifecycleProcessConfig(path, &config, func() error { return config.Validate() }); err != nil {
		return LifecycleServerProcessConfig{}, err
	}
	return config, nil
}

func WriteLifecycleServerProcessReady(path string, ready LifecycleServerProcessReady) error {
	return writeLifecycleProcessConfig(path, ready, ready.Validate)
}

func ReadLifecycleServerProcessReady(path string) (LifecycleServerProcessReady, error) {
	var ready LifecycleServerProcessReady
	if err := readLifecycleProcessConfig(path, &ready, func() error { return ready.Validate() }); err != nil {
		return LifecycleServerProcessReady{}, err
	}
	return ready, nil
}

func (ready LifecycleServerProcessReady) Validate() error {
	if ready.PID <= 0 {
		return errors.New("lifecycle server PTY fixture readiness PID must be positive")
	}
	return nil
}

func writeLifecycleProcessConfig[T any](path string, config T, validate func() error) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("lifecycle PTY fixture process config path is required")
	}
	if err := validate(); err != nil {
		return err
	}
	encoded, err := json.Marshal(config)
	if err != nil {
		return fmt.Errorf("marshal lifecycle PTY fixture process config: %w", err)
	}
	if err := writeLifecycleFixtureDocument(path, encoded); err != nil {
		return err
	}
	return nil
}

func writeLifecycleFixtureDocument(path string, encoded []byte) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create lifecycle PTY fixture document: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()

	if _, err := temporary.Write(encoded); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write lifecycle PTY fixture document: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close lifecycle PTY fixture document: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("publish lifecycle PTY fixture document: %w", err)
	}
	return nil
}

func readLifecycleProcessConfig[T any](path string, config *T, validate func() error) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("lifecycle PTY fixture process config path is required")
	}
	encoded, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read lifecycle PTY fixture process config: %w", err)
	}
	if err := json.Unmarshal(encoded, config); err != nil {
		return fmt.Errorf("decode lifecycle PTY fixture process config: %w", err)
	}
	return validate()
}
