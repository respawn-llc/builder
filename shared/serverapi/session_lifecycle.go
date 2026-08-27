package serverapi

import (
	"errors"
	"strings"

	"core/shared/runtimeids"
)

type SessionTransitionAction string

const (
	SessionTransitionActionNone         SessionTransitionAction = "none"
	SessionTransitionActionNewSession   SessionTransitionAction = "new_session"
	SessionTransitionActionResume       SessionTransitionAction = "resume"
	SessionTransitionActionLogout       SessionTransitionAction = "logout"
	SessionTransitionActionForkRollback SessionTransitionAction = "fork_rollback"
	SessionTransitionActionOpenSession  SessionTransitionAction = "open_session"
)

type SessionTransition struct {
	Action                       SessionTransitionAction `json:"action"`
	InitialPrompt                string                  `json:"initial_prompt,omitempty"`
	InitialPromptHistoryRecorded bool                    `json:"initial_prompt_history_recorded,omitempty"`
	InitialInput                 *string                 `json:"initial_input,omitempty"`
	TargetSessionID              string                  `json:"target_session_id,omitempty"`
	ForkRollbackTargetID         string                  `json:"fork_rollback_target_id,omitempty"`
	PreviousSessionID            *runtimeids.SessionID   `json:"previous_session_id,omitempty"`
}

type SessionInitialInputRequest struct {
	SessionID           string `json:"session_id,omitempty"`
	TransitionInput     string `json:"transition_input,omitempty"`
	OverrideStoredDraft bool   `json:"override_stored_draft,omitempty"`
}

type SessionInitialInputResponse struct {
	Input string `json:"input"`
}

type SessionPersistInputDraftRequest struct {
	SessionID string `json:"session_id"`
	Input     string `json:"input,omitempty"`
}

type SessionPersistInputDraftResponse struct{}

type RuntimeStepOrigin struct {
	RunID  string `json:"run_id"`
	StepID string `json:"step_id"`
}

type SessionRetargetWorkspaceRequest struct {
	SessionID     string             `json:"session_id"`
	WorkspaceRoot string             `json:"workspace_root"`
	ProjectID     *string            `json:"project_id,omitempty"`
	Origin        *RuntimeStepOrigin `json:"origin,omitempty"`
}

type SessionRetargetWorkspaceResponse struct {
	Binding                 *ProjectBinding                   `json:"binding,omitempty"`
	WorkspaceBindingCreated bool                              `json:"workspace_binding_created,omitempty"`
	Scheduled               *WorktreeScheduledAcknowledgement `json:"scheduled,omitempty"`
}

type SessionResolveTransitionRequest struct {
	SessionID  string            `json:"session_id,omitempty"`
	Transition SessionTransition `json:"transition"`
}

type SessionResolveTransitionResponse = SessionDirective

func (r SessionPersistInputDraftRequest) Validate() error {
	if err := validateScopedSessionID(r.SessionID); err != nil {
		return err
	}
	return nil
}

func (r SessionInitialInputRequest) Validate() error {
	if strings.TrimSpace(r.SessionID) == "" {
		return nil
	}
	return validateScopedSessionID(r.SessionID)
}

func (r SessionRetargetWorkspaceRequest) Validate() error {
	if err := validateScopedSessionID(r.SessionID); err != nil {
		return err
	}
	if strings.TrimSpace(r.WorkspaceRoot) == "" {
		return errors.New("workspace_root is required")
	}
	if r.ProjectID != nil && strings.TrimSpace(*r.ProjectID) == "" {
		return errors.New("project_id must not be blank when provided")
	}
	if r.Origin != nil {
		return r.Origin.Validate()
	}
	return nil
}

func (origin RuntimeStepOrigin) Validate() error {
	if err := runtimeids.ValidateUUIDv4(origin.RunID, "run_id"); err != nil {
		return err
	}
	return runtimeids.ValidateUUIDv4(origin.StepID, "step_id")
}

func (r SessionRetargetWorkspaceResponse) Validate() error {
	switch {
	case r.Binding != nil && r.Scheduled == nil:
		if strings.TrimSpace(r.Binding.ProjectID) == "" ||
			strings.TrimSpace(r.Binding.WorkspaceID) == "" ||
			strings.TrimSpace(r.Binding.CanonicalRoot) == "" {
			return errors.New("completed retarget response requires a complete binding")
		}
		return nil
	case r.Binding == nil && r.Scheduled != nil:
		if r.WorkspaceBindingCreated {
			return errors.New("scheduled retarget response cannot report workspace creation")
		}
		return r.Scheduled.Validate()
	default:
		return errors.New("retarget response requires exactly one completed binding or scheduled acknowledgement")
	}
}

func (r SessionResolveTransitionRequest) Validate() error {
	if strings.TrimSpace(r.SessionID) != "" {
		if err := validateScopedSessionID(r.SessionID); err != nil {
			return err
		}
	}
	if strings.TrimSpace(string(r.Transition.Action)) == "" {
		return errors.New("transition.action is required")
	}
	return nil
}
