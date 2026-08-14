package serverapi

import (
	"errors"
	"fmt"
	"strings"

	"core/shared/config"
	"core/shared/runtimeids"
)

type SessionRuntimeActivateRequest struct {
	ClientRequestID          string              `json:"client_request_id"`
	SessionID                string              `json:"session_id"`
	OwnerID                  string              `json:"owner_id,omitempty"`
	ActiveSettings           config.Settings     `json:"active_settings"`
	EnabledToolIDs           []string            `json:"enabled_tool_ids"`
	QuestionsEnabled         *bool               `json:"questions_enabled"`
	AutoCompactionEnabled    *bool               `json:"auto_compaction_enabled"`
	ThinkingOverrideExplicit bool                `json:"thinking_override_explicit"`
	Source                   config.SourceReport `json:"source"`
}

type SessionRuntimeAttachment struct {
	SessionID  string `json:"session_id"`
	Generation uint64 `json:"generation"`
}

type SessionRuntimeActivateResponse struct {
	Attachment SessionRuntimeAttachment `json:"attachment"`
}

type SessionRuntimeReleaseRequest struct {
	ClientRequestID string                           `json:"client_request_id"`
	Attachment      SessionRuntimeAttachment         `json:"attachment"`
	DropOwner       bool                             `json:"drop_owner,omitempty"`
	ClosePolicy     SessionRuntimeReleaseClosePolicy `json:"close_policy,omitempty"`
	OwnerID         string                           `json:"owner_id,omitempty"`
}

type SessionRuntimeReleaseResponse struct {
	Released bool `json:"released"`
	Active   bool `json:"active,omitempty"`
}

type SessionRuntimeReleaseClosePolicy string

var ErrRuntimeOwnerIDRequired = errors.New("runtime owner id is required")

const (
	SessionRuntimeReleaseClosePolicyCloseIfIdle SessionRuntimeReleaseClosePolicy = "close_if_idle"
	SessionRuntimeReleaseClosePolicyDetachOnly  SessionRuntimeReleaseClosePolicy = "detach_only"
)

func (r SessionRuntimeActivateRequest) Validate() error {
	_, err := PrepareSessionRuntimeActivateRequest(r)
	return err
}

func PrepareSessionRuntimeActivateRequest(r SessionRuntimeActivateRequest) (runtimeids.SessionID, error) {
	if strings.TrimSpace(r.ClientRequestID) == "" {
		return runtimeids.SessionID{}, errors.New("client_request_id is required")
	}
	sessionID, err := parseScopedSessionID(r.SessionID)
	if err != nil {
		return runtimeids.SessionID{}, err
	}
	if strings.TrimSpace(r.OwnerID) == "" {
		return runtimeids.SessionID{}, errors.Join(ErrRuntimeOwnerIDRequired, errors.New("runtime owner_id is required; upgrade the client or connect through the current Kent gateway"))
	}
	if r.QuestionsEnabled == nil {
		return runtimeids.SessionID{}, errors.New("questions_enabled is required")
	}
	if r.AutoCompactionEnabled == nil {
		return runtimeids.SessionID{}, errors.New("auto_compaction_enabled is required")
	}
	return sessionID, nil
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
	_, err := PrepareSessionRuntimeReleaseRequest(r)
	return err
}

func PrepareSessionRuntimeReleaseRequest(r SessionRuntimeReleaseRequest) (runtimeids.SessionID, error) {
	if strings.TrimSpace(r.ClientRequestID) == "" {
		return runtimeids.SessionID{}, errors.New("client_request_id is required")
	}
	sessionID, err := parseScopedSessionID(r.Attachment.SessionID)
	if err != nil {
		return runtimeids.SessionID{}, err
	}
	if err := runtimeids.ResourceGeneration(r.Attachment.Generation).Validate(); err != nil {
		return runtimeids.SessionID{}, err
	}
	if strings.TrimSpace(r.OwnerID) == "" {
		return runtimeids.SessionID{}, errors.Join(ErrRuntimeOwnerIDRequired, errors.New("runtime owner_id is required; upgrade the client or connect through the current Kent gateway"))
	}
	switch r.ClosePolicy {
	case "", SessionRuntimeReleaseClosePolicyCloseIfIdle:
	case SessionRuntimeReleaseClosePolicyDetachOnly:
		if !r.DropOwner {
			return runtimeids.SessionID{}, errors.New("detach_only release requires drop_owner")
		}
	default:
		return runtimeids.SessionID{}, errors.New("invalid session runtime release close_policy")
	}
	return sessionID, nil
}

func (r SessionRuntimeReleaseRequest) EffectiveClosePolicy() SessionRuntimeReleaseClosePolicy {
	return r.ClosePolicy
}
