package appfixture

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
)

const LifecycleProcessConfigEnvName = "KENT_LIFECYCLE_PTY_FIXTURE_CONFIG"
const LifecycleServerProcessConfigEnvName = "KENT_LIFECYCLE_SERVER_PTY_FIXTURE_CONFIG"

type LifecycleServerMode string

const (
	LifecycleServerModeLocal  LifecycleServerMode = "local"
	LifecycleServerModeRemote LifecycleServerMode = "remote"
)

type LifecycleOpeningKind string

const (
	LifecycleOpeningKindNew     LifecycleOpeningKind = "new"
	LifecycleOpeningKindResumed LifecycleOpeningKind = "resumed"
)

type LifecycleHookBehavior string

const (
	LifecycleHookBehaviorSuccess     LifecycleHookBehavior = "success"
	LifecycleHookBehaviorNonzero     LifecycleHookBehavior = "nonzero"
	LifecycleHookBehaviorNonzeroOnce LifecycleHookBehavior = "nonzero_once"
	LifecycleHookBehaviorHang        LifecycleHookBehavior = "hang"
)

type LifecycleProcessConfig struct {
	WorkspaceRoot             string                `json:"workspace_root"`
	PersistenceRoot           string                `json:"persistence_root"`
	ServerMode                LifecycleServerMode   `json:"server_mode"`
	OpeningKind               LifecycleOpeningKind  `json:"opening_kind"`
	LocalScriptPath           *string               `json:"local_script_path,omitempty"`
	SessionID                 *string               `json:"session_id,omitempty"`
	InitialPrompt             *string               `json:"initial_prompt,omitempty"`
	TargetFinalAssistantCount uint64                `json:"target_final_assistant_count"`
	HookRecordPath            string                `json:"hook_record_path"`
	HookBehavior              LifecycleHookBehavior `json:"hook_behavior"`
	HookReadyPath             *string               `json:"hook_ready_path,omitempty"`
	HookStatePath             *string               `json:"hook_state_path,omitempty"`
}

type LifecycleServerProcessConfig struct {
	WorkspaceRoot   string                `json:"workspace_root"`
	PersistenceRoot string                `json:"persistence_root"`
	ScriptPath      string                `json:"script_path"`
	ReadyPath       string                `json:"ready_path"`
	HookRecordPath  string                `json:"hook_record_path"`
	HookBehavior    LifecycleHookBehavior `json:"hook_behavior"`
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
	switch config.HookBehavior {
	case LifecycleHookBehaviorSuccess,
		LifecycleHookBehaviorNonzero,
		LifecycleHookBehaviorNonzeroOnce,
		LifecycleHookBehaviorHang:
	default:
		return errors.New("lifecycle server PTY fixture hook_behavior is invalid")
	}
	return nil
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
	switch config.OpeningKind {
	case LifecycleOpeningKindNew:
		if config.SessionID != nil {
			return errors.New("new lifecycle PTY fixture cannot contain session_id")
		}
	case LifecycleOpeningKindResumed:
		if config.SessionID != nil && strings.TrimSpace(*config.SessionID) == "" {
			return errors.New("lifecycle PTY fixture session_id cannot be blank")
		}
		if config.ServerMode == LifecycleServerModeRemote && config.SessionID == nil {
			return errors.New("remote resumed lifecycle PTY fixture requires session_id")
		}
	default:
		return errors.New("lifecycle PTY fixture opening_kind is invalid")
	}
	if config.InitialPrompt != nil && strings.TrimSpace(*config.InitialPrompt) == "" {
		return errors.New("lifecycle PTY fixture initial_prompt cannot be blank")
	}
	if strings.TrimSpace(config.HookRecordPath) == "" {
		return errors.New("lifecycle PTY fixture hook_record_path is required")
	}
	switch config.HookBehavior {
	case LifecycleHookBehaviorSuccess,
		LifecycleHookBehaviorNonzero,
		LifecycleHookBehaviorNonzeroOnce,
		LifecycleHookBehaviorHang:
	default:
		return errors.New("lifecycle PTY fixture hook_behavior is invalid")
	}
	if config.HookReadyPath != nil && strings.TrimSpace(*config.HookReadyPath) == "" {
		return errors.New("lifecycle PTY fixture hook_ready_path cannot be blank")
	}
	if config.HookStatePath != nil && strings.TrimSpace(*config.HookStatePath) == "" {
		return errors.New("lifecycle PTY fixture hook_state_path cannot be blank")
	}
	if config.HookBehavior == LifecycleHookBehaviorHang && config.HookReadyPath == nil {
		return errors.New("hanging lifecycle PTY fixture hook requires hook_ready_path")
	}
	if config.HookBehavior == LifecycleHookBehaviorNonzeroOnce && config.HookStatePath == nil {
		return errors.New("non-zero-once lifecycle PTY fixture hook requires hook_state_path")
	}
	return nil
}

func WriteLifecycleProcessConfig(path string, config LifecycleProcessConfig) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("lifecycle PTY fixture process config path is required")
	}
	if err := config.Validate(); err != nil {
		return err
	}
	encoded, err := json.Marshal(config)
	if err != nil {
		return fmt.Errorf("marshal lifecycle PTY fixture process config: %w", err)
	}
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		return fmt.Errorf("write lifecycle PTY fixture process config: %w", err)
	}
	return nil
}

func ReadLifecycleProcessConfig(path string) (LifecycleProcessConfig, error) {
	if strings.TrimSpace(path) == "" {
		return LifecycleProcessConfig{}, errors.New("lifecycle PTY fixture process config path is required")
	}
	encoded, err := os.ReadFile(path)
	if err != nil {
		return LifecycleProcessConfig{}, fmt.Errorf("read lifecycle PTY fixture process config: %w", err)
	}
	var config LifecycleProcessConfig
	if err := json.Unmarshal(encoded, &config); err != nil {
		return LifecycleProcessConfig{}, fmt.Errorf("decode lifecycle PTY fixture process config: %w", err)
	}
	if err := config.Validate(); err != nil {
		return LifecycleProcessConfig{}, err
	}
	return config, nil
}

func WriteLifecycleServerProcessConfig(path string, config LifecycleServerProcessConfig) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("lifecycle server PTY fixture process config path is required")
	}
	if err := config.Validate(); err != nil {
		return err
	}
	encoded, err := json.Marshal(config)
	if err != nil {
		return fmt.Errorf("marshal lifecycle server PTY fixture process config: %w", err)
	}
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		return fmt.Errorf("write lifecycle server PTY fixture process config: %w", err)
	}
	return nil
}

func ReadLifecycleServerProcessConfig(path string) (LifecycleServerProcessConfig, error) {
	if strings.TrimSpace(path) == "" {
		return LifecycleServerProcessConfig{}, errors.New("lifecycle server PTY fixture process config path is required")
	}
	encoded, err := os.ReadFile(path)
	if err != nil {
		return LifecycleServerProcessConfig{}, fmt.Errorf("read lifecycle server PTY fixture process config: %w", err)
	}
	var config LifecycleServerProcessConfig
	if err := json.Unmarshal(encoded, &config); err != nil {
		return LifecycleServerProcessConfig{}, fmt.Errorf("decode lifecycle server PTY fixture process config: %w", err)
	}
	if err := config.Validate(); err != nil {
		return LifecycleServerProcessConfig{}, err
	}
	return config, nil
}
