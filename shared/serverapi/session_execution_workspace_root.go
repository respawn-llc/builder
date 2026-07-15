package serverapi

import (
	"encoding/json"
	"errors"
	"strings"

	"core/shared/runtimeids"
)

type SessionExecutionWorkspaceRootRequest struct {
	SessionID runtimeids.SessionID `json:"session_id"`
}

type SessionExecutionWorkspaceRootResponse struct {
	WorkspaceRoot string `json:"workspace_root"`
}

func (r SessionExecutionWorkspaceRootRequest) Validate() error {
	if r.SessionID.IsZero() {
		return errors.New("session_id is required")
	}
	return nil
}

func (r SessionExecutionWorkspaceRootRequest) MarshalJSON() ([]byte, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	type wire SessionExecutionWorkspaceRootRequest
	return json.Marshal(wire(r))
}

func (r *SessionExecutionWorkspaceRootRequest) UnmarshalJSON(data []byte) error {
	var wire struct {
		SessionID runtimeids.SessionID `json:"session_id"`
	}
	if err := decodeStrictJSON(data, &wire); err != nil {
		return err
	}
	value := SessionExecutionWorkspaceRootRequest{SessionID: wire.SessionID}
	if err := value.Validate(); err != nil {
		return err
	}
	*r = value
	return nil
}

func (r SessionExecutionWorkspaceRootResponse) Validate() error {
	normalized := strings.TrimSpace(r.WorkspaceRoot)
	if normalized == "" {
		return errors.New("session execution workspace_root is required")
	}
	if normalized != r.WorkspaceRoot {
		return errors.New("session execution workspace_root must not have leading or trailing whitespace")
	}
	return nil
}

func (r SessionExecutionWorkspaceRootResponse) MarshalJSON() ([]byte, error) {
	value := SessionExecutionWorkspaceRootResponse{
		WorkspaceRoot: strings.TrimSpace(r.WorkspaceRoot),
	}
	if err := value.Validate(); err != nil {
		return nil, err
	}
	type wire SessionExecutionWorkspaceRootResponse
	return json.Marshal(wire(value))
}

func (r *SessionExecutionWorkspaceRootResponse) UnmarshalJSON(data []byte) error {
	var wire struct {
		WorkspaceRoot *string `json:"workspace_root"`
	}
	if err := decodeStrictJSON(data, &wire); err != nil {
		return err
	}
	if wire.WorkspaceRoot == nil {
		return errors.New("session execution workspace_root is required")
	}
	value := SessionExecutionWorkspaceRootResponse{
		WorkspaceRoot: strings.TrimSpace(*wire.WorkspaceRoot),
	}
	if err := value.Validate(); err != nil {
		return err
	}
	*r = value
	return nil
}
