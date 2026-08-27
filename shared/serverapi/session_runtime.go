package serverapi

import (
	"errors"
	"fmt"
	"strings"

	"core/shared/config"
	"core/shared/runtimeids"
)

type SessionRuntimeActivateRequest struct {
	SessionID                string                        `json:"session_id"`
	OwnerID                  string                        `json:"owner_id,omitempty"`
	ActiveSettings           config.Settings               `json:"active_settings"`
	EnabledToolIDs           []string                      `json:"enabled_tool_ids"`
	QuestionsEnabled         *bool                         `json:"questions_enabled"`
	AutoCompactionEnabled    *bool                         `json:"auto_compaction_enabled"`
	ThinkingOverrideExplicit bool                          `json:"thinking_override_explicit"`
	AgentSelection           *SessionRuntimeAgentSelection `json:"agent_selection,omitempty"`
	Source                   config.SourceReport           `json:"source"`
}

type SessionRuntimeAgentSelection struct {
	Agent    string                     `json:"agent"`
	Baseline SessionRuntimeChatSettings `json:"baseline"`
}

type SessionRuntimeChatSettings struct {
	Supervisor     string `json:"supervisor"`
	Thinking       string `json:"thinking"`
	Fast           bool   `json:"fast"`
	Questions      bool   `json:"questions"`
	AutoCompaction bool   `json:"auto_compaction"`
}

type SessionRuntimeAttachment struct {
	SessionID  string `json:"session_id"`
	Generation uint64 `json:"generation"`
}

type SessionRuntimeActivateResponse struct {
	Attachment SessionRuntimeAttachment `json:"attachment"`
}

type SessionRuntimeReleaseRequest struct {
	Attachment  SessionRuntimeAttachment         `json:"attachment"`
	DropOwner   bool                             `json:"drop_owner,omitempty"`
	ClosePolicy SessionRuntimeReleaseClosePolicy `json:"close_policy,omitempty"`
	OwnerID     string                           `json:"owner_id,omitempty"`
}

type SessionRuntimeReleaseResponse struct {
	Released bool `json:"released"`
	Active   bool `json:"active,omitempty"`
}

type SessionRuntimeReleaseClosePolicy string

const (
	SessionRuntimeReleaseClosePolicyCloseIfIdle SessionRuntimeReleaseClosePolicy = "close_if_idle"
	SessionRuntimeReleaseClosePolicyDetachOnly  SessionRuntimeReleaseClosePolicy = "detach_only"
)

func (r SessionRuntimeActivateRequest) Validate() error {
	if err := validateScopedSessionID(r.SessionID); err != nil {
		return err
	}
	if r.QuestionsEnabled == nil {
		return errors.New("questions_enabled is required")
	}
	if r.AutoCompactionEnabled == nil {
		return errors.New("auto_compaction_enabled is required")
	}
	if r.AgentSelection != nil {
		if strings.TrimSpace(r.AgentSelection.Agent) == "" {
			return errors.New("agent_selection.agent is required")
		}
		if strings.TrimSpace(r.AgentSelection.Baseline.Supervisor) == "" {
			return errors.New("agent_selection.baseline.supervisor is required")
		}
		if strings.TrimSpace(r.AgentSelection.Baseline.Thinking) == "" {
			return errors.New("agent_selection.baseline.thinking is required")
		}
	}
	return nil
}

func (a SessionRuntimeAttachment) Validate() error {
	if err := validateScopedSessionID(a.SessionID); err != nil {
		return err
	}
	return runtimeids.ResourceGeneration(a.Generation).Validate()
}

func (r SessionRuntimeActivateResponse) ValidateForSession(sessionID string) error {
	if err := r.Attachment.Validate(); err != nil {
		return fmt.Errorf("validate session runtime activation response: %w", err)
	}
	expected := strings.TrimSpace(sessionID)
	if r.Attachment.SessionID != expected {
		return fmt.Errorf(
			"session runtime activation returned attachment for session %q, want %q",
			r.Attachment.SessionID,
			expected,
		)
	}
	return nil
}

func (r SessionRuntimeReleaseRequest) Validate() error {
	if err := r.Attachment.Validate(); err != nil {
		return err
	}
	switch r.ClosePolicy {
	case "", SessionRuntimeReleaseClosePolicyCloseIfIdle:
	case SessionRuntimeReleaseClosePolicyDetachOnly:
		if !r.DropOwner {
			return errors.New("detach_only release requires drop_owner")
		}
	default:
		return errors.New("invalid session runtime release close_policy")
	}
	return nil
}

func (r SessionRuntimeReleaseRequest) EffectiveClosePolicy() SessionRuntimeReleaseClosePolicy {
	return r.ClosePolicy
}
