package serverapi

import (
	"errors"
	"strings"

	"core/shared/runtimeids"
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
	ClientRequestID string `json:"client_request_id"`
	SessionID       string `json:"session_id"`
	Input           string `json:"input,omitempty"`
}

type SessionPersistInputDraftResponse struct{}

type SessionRetargetCompletionMode string

const (
	SessionRetargetCompletionScheduled SessionRetargetCompletionMode = "scheduled"
	SessionRetargetCompletionWait      SessionRetargetCompletionMode = "wait"
)

type SessionRetargetWorkspaceRequest struct {
	WorktreeTransitionHeader
	WorkspaceRoot  string                        `json:"workspace_root"`
	ProjectID      *string                       `json:"project_id,omitempty"`
	CompletionMode SessionRetargetCompletionMode `json:"completion_mode"`
}

type SessionRetargetWorkspaceResponse struct {
	Acknowledgement WorktreeScheduledAcknowledgement `json:"acknowledgement"`
	Outcome         *SessionRetargetOutcome          `json:"outcome,omitempty"`
}

type SessionRetargetOutcomeKind string

const (
	SessionRetargetOutcomeSucceeded SessionRetargetOutcomeKind = "succeeded"
	SessionRetargetOutcomeFailed    SessionRetargetOutcomeKind = "failed"
)

type SessionRetargetSuccess struct {
	Binding                 ProjectBinding `json:"binding"`
	WorkspaceBindingCreated bool           `json:"workspace_binding_created"`
}

type SessionRetargetFailure struct {
	Diagnostic                string           `json:"diagnostic"`
	UnchangedProject          ProjectReference `json:"unchanged_project"`
	UnchangedWorkingDirectory string           `json:"unchanged_working_directory"`
}

type SessionRetargetOutcome struct {
	OperationID WorktreeOperationID        `json:"operation_id"`
	Kind        SessionRetargetOutcomeKind `json:"kind"`
	Success     *SessionRetargetSuccess    `json:"success,omitempty"`
	Failure     *SessionRetargetFailure    `json:"failure,omitempty"`
}

type SessionResolveTransitionRequest struct {
	ClientRequestID string            `json:"client_request_id"`
	SessionID       string            `json:"session_id,omitempty"`
	Transition      SessionTransition `json:"transition"`
}

type SessionResolveTransitionResponse = SessionDirective

func (r SessionPersistInputDraftRequest) Validate() error {
	if strings.TrimSpace(r.ClientRequestID) == "" {
		return ErrClientRequestIDRequired
	}
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
	if err := r.WorktreeTransitionHeader.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(r.WorkspaceRoot) == "" {
		return errors.New("workspace_root is required")
	}
	if r.ProjectID != nil && strings.TrimSpace(*r.ProjectID) == "" {
		return errors.New("project_id must not be blank when provided")
	}
	switch r.CompletionMode {
	case SessionRetargetCompletionScheduled, SessionRetargetCompletionWait:
	default:
		return errors.New("completion_mode must be scheduled or wait")
	}
	return nil
}

func (r SessionRetargetWorkspaceResponse) Validate() error {
	if err := r.Acknowledgement.Validate(); err != nil {
		return err
	}
	if r.Outcome == nil {
		return nil
	}
	if r.Outcome.OperationID != r.Acknowledgement.OperationID {
		return errors.New("retarget outcome operation_id does not match acknowledgement")
	}
	return r.Outcome.Validate()
}

func (r SessionRetargetWorkspaceResponse) ValidateForCompletionMode(mode SessionRetargetCompletionMode) error {
	if err := r.Validate(); err != nil {
		return err
	}
	switch mode {
	case SessionRetargetCompletionScheduled:
		if r.Outcome != nil {
			return errors.New("scheduled retarget response must not contain an outcome")
		}
	case SessionRetargetCompletionWait:
		if r.Outcome == nil {
			return errors.New("wait retarget response requires an outcome")
		}
	default:
		return errors.New("invalid retarget completion mode")
	}
	return nil
}

func (o SessionRetargetOutcome) Validate() error {
	if err := o.OperationID.Validate(); err != nil {
		return err
	}
	switch o.Kind {
	case SessionRetargetOutcomeSucceeded:
		if o.Success == nil || o.Failure != nil {
			return errors.New("succeeded retarget outcome must contain only success")
		}
		return o.Success.Validate()
	case SessionRetargetOutcomeFailed:
		if o.Failure == nil || o.Success != nil {
			return errors.New("failed retarget outcome must contain only failure")
		}
		return o.Failure.Validate()
	default:
		return errors.New("invalid retarget outcome kind")
	}
}

func (s SessionRetargetSuccess) Validate() error {
	if strings.TrimSpace(s.Binding.ProjectID) == "" {
		return errors.New("retarget success project_id is required")
	}
	if strings.TrimSpace(s.Binding.WorkspaceID) == "" {
		return errors.New("retarget success workspace_id is required")
	}
	if strings.TrimSpace(s.Binding.CanonicalRoot) == "" {
		return errors.New("retarget success canonical_root is required")
	}
	return nil
}

func (f SessionRetargetFailure) Validate() error {
	if strings.TrimSpace(f.Diagnostic) == "" {
		return errors.New("retarget failure diagnostic is required")
	}
	if err := f.UnchangedProject.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(f.UnchangedWorkingDirectory) == "" {
		return errors.New("retarget failure unchanged_working_directory is required")
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
