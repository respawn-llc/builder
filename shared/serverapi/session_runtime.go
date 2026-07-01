package serverapi

import (
	"errors"
	"strings"

	"core/shared/config"
)

type SessionRuntimeActivateRequest struct {
	ClientRequestID string              `json:"client_request_id"`
	SessionID       string              `json:"session_id"`
	OwnerID         string              `json:"owner_id,omitempty"`
	ActiveSettings  config.Settings     `json:"active_settings"`
	EnabledToolIDs  []string            `json:"enabled_tool_ids"`
	Source          config.SourceReport `json:"source"`
}

type SessionRuntimeActivateResponse struct{}

type SessionRuntimeReleaseRequest struct {
	ClientRequestID string `json:"client_request_id"`
	SessionID       string `json:"session_id"`
	OnlyIfIdle      bool   `json:"only_if_idle,omitempty"`
	DropOwner       bool   `json:"drop_owner,omitempty"`
	OwnerID         string `json:"owner_id,omitempty"`
}

type SessionRuntimeReleaseResponse struct {
	Released bool `json:"released"`
	Active   bool `json:"active,omitempty"`
}

func (r SessionRuntimeActivateRequest) Validate() error {
	if strings.TrimSpace(r.ClientRequestID) == "" {
		return errors.New("client_request_id is required")
	}
	if err := validateScopedSessionID(r.SessionID); err != nil {
		return err
	}
	return nil
}

func (r SessionRuntimeReleaseRequest) Validate() error {
	if strings.TrimSpace(r.ClientRequestID) == "" {
		return errors.New("client_request_id is required")
	}
	if err := validateScopedSessionID(r.SessionID); err != nil {
		return err
	}
	return nil
}
