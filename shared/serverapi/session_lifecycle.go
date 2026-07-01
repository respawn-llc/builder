package serverapi

import (
	"errors"
	"strings"
)

// ErrClientRequestIDRequired is returned when a lifecycle request omits its
// client_request_id.
var ErrClientRequestIDRequired = errors.New("client_request_id is required")

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
	InitialInput                 string                  `json:"initial_input,omitempty"`
	TargetSessionID              string                  `json:"target_session_id,omitempty"`
	ForkRollbackTargetID         string                  `json:"fork_rollback_target_id,omitempty"`
	ParentSessionID              string                  `json:"parent_session_id,omitempty"`
}

type SessionInitialInputRequest struct {
	SessionID       string `json:"session_id,omitempty"`
	TransitionInput string `json:"transition_input,omitempty"`
}

type SessionInitialInputResponse struct {
	Input string `json:"input"`
}

type SessionPersistInputDraftRequest struct {
	ClientRequestID string `json:"client_request_id"`
	SessionID       string `json:"session_id"`
	Input           string `json:"input,omitempty"`
}

type SessionPersistInputDraftResponse struct{}

type SessionRetargetWorkspaceRequest struct {
	ClientRequestID string `json:"client_request_id"`
	SessionID       string `json:"session_id"`
	WorkspaceRoot   string `json:"workspace_root"`
}

type SessionRetargetWorkspaceResponse struct {
	Binding ProjectBinding `json:"binding"`
}

type SessionResolveTransitionRequest struct {
	ClientRequestID string            `json:"client_request_id"`
	SessionID       string            `json:"session_id,omitempty"`
	Transition      SessionTransition `json:"transition"`
}

type SessionResolveTransitionResponse struct {
	NextSessionID                string `json:"next_session_id,omitempty"`
	InitialPrompt                string `json:"initial_prompt,omitempty"`
	InitialPromptHistoryRecorded bool   `json:"initial_prompt_history_recorded,omitempty"`
	InitialInput                 string `json:"initial_input,omitempty"`
	ParentSessionID              string `json:"parent_session_id,omitempty"`
	ForceNewSession              bool   `json:"force_new_session,omitempty"`
	ShouldContinue               bool   `json:"should_continue,omitempty"`
	RequiresReauth               bool   `json:"requires_reauth,omitempty"`
}

func (r SessionPersistInputDraftRequest) Validate() error {
	if strings.TrimSpace(r.ClientRequestID) == "" {
		return ErrClientRequestIDRequired
	}
	return validateScopedSessionID(r.SessionID)
}

func (r SessionInitialInputRequest) Validate() error {
	if strings.TrimSpace(r.SessionID) == "" {
		return nil
	}
	return validateScopedSessionID(r.SessionID)
}

func (r SessionRetargetWorkspaceRequest) Validate() error {
	if strings.TrimSpace(r.ClientRequestID) == "" {
		return ErrClientRequestIDRequired
	}
	if err := validateScopedSessionID(r.SessionID); err != nil {
		return err
	}
	if strings.TrimSpace(r.WorkspaceRoot) == "" {
		return errors.New("workspace_root is required")
	}
	return nil
}

func (r SessionResolveTransitionRequest) Validate() error {
	if strings.TrimSpace(r.ClientRequestID) == "" {
		return ErrClientRequestIDRequired
	}
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
