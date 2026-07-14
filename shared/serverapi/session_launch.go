package serverapi

import (
	"errors"
	"strings"

	"core/shared/config"
)

type SessionLaunchMode string

const (
	SessionLaunchModeInteractive SessionLaunchMode = "interactive"
	SessionLaunchModeHeadless    SessionLaunchMode = "headless"
)

type SessionPlanRequest struct {
	ClientRequestID   string             `json:"client_request_id"`
	Mode              SessionLaunchMode  `json:"mode"`
	SelectedSessionID string             `json:"selected_session_id,omitempty"`
	ForceNewSession   bool               `json:"force_new_session,omitempty"`
	ParentSessionID   string             `json:"parent_session_id,omitempty"`
	Overrides         RunPromptOverrides `json:"overrides,omitempty"`
}

type SessionPlan struct {
	SessionID           string               `json:"session_id"`
	ActiveSettings      config.Settings      `json:"active_settings"`
	EnabledToolIDs      []string             `json:"enabled_tool_ids,omitempty"`
	ConfiguredModelName string               `json:"configured_model_name,omitempty"`
	SessionName         string               `json:"session_name,omitempty"`
	PromptHistory       []string             `json:"prompt_history,omitempty"`
	ModelContractLocked bool                 `json:"model_contract_locked,omitempty"`
	WorkspaceRoot       string               `json:"workspace_root,omitempty"`
	Source              config.SourceReport  `json:"source"`
	Recovery            *SessionPlanRecovery `json:"recovery,omitempty"`
}

type SessionPlanRecoveryKind string

const (
	SessionPlanRecoveryKindDeletedManagedWorktree SessionPlanRecoveryKind = "deleted_managed_worktree"
)

type SessionPlanRecovery struct {
	Kind          SessionPlanRecoveryKind `json:"kind"`
	WorkspaceRoot string                  `json:"workspace_root"`
}

type SessionPlanResponse struct {
	Plan     SessionPlan `json:"plan"`
	Warnings []string    `json:"warnings,omitempty"`
}

func (r SessionPlanRequest) Validate() error {
	if strings.TrimSpace(r.ClientRequestID) == "" {
		return errors.New("client_request_id is required")
	}
	mode := strings.TrimSpace(string(r.Mode))
	if mode == "" {
		return errors.New("mode is required")
	}
	if mode != string(SessionLaunchModeInteractive) && mode != string(SessionLaunchModeHeadless) {
		return errors.New("mode must be interactive or headless")
	}
	if strings.TrimSpace(r.SelectedSessionID) == "" && !r.ForceNewSession {
		return errors.New("selected_session_id or force_new_session is required")
	}
	return r.Overrides.ValidateAgentRoleOverride()
}
