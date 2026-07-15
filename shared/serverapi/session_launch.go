package serverapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"core/shared/config"
	"core/shared/runtimeids"
)

type SessionLaunchMode string

const (
	SessionLaunchModeInteractive SessionLaunchMode = "interactive"
	SessionLaunchModeHeadless    SessionLaunchMode = "headless"
)

type SessionLaunchIntentKind string

const (
	SessionLaunchIntentCreateNew    SessionLaunchIntentKind = "create_new"
	SessionLaunchIntentOpenExisting SessionLaunchIntentKind = "open_existing"
)

type SessionLaunchIntent struct {
	kind      SessionLaunchIntentKind
	parentID  *runtimeids.SessionID
	sessionID *runtimeids.SessionID
}

func CreateNewSessionLaunchIntent(parentID *runtimeids.SessionID) SessionLaunchIntent {
	intent := SessionLaunchIntent{kind: SessionLaunchIntentCreateNew}
	if parentID != nil {
		copied := *parentID
		intent.parentID = &copied
	}
	return intent
}

func OpenExistingSessionLaunchIntent(sessionID runtimeids.SessionID) SessionLaunchIntent {
	copied := sessionID
	return SessionLaunchIntent{
		kind:      SessionLaunchIntentOpenExisting,
		sessionID: &copied,
	}
}

func (i SessionLaunchIntent) Kind() SessionLaunchIntentKind {
	return i.kind
}

func (i SessionLaunchIntent) ParentID() (runtimeids.SessionID, bool) {
	if i.parentID == nil {
		return runtimeids.SessionID{}, false
	}
	return *i.parentID, true
}

func (i SessionLaunchIntent) SessionID() (runtimeids.SessionID, bool) {
	if i.sessionID == nil {
		return runtimeids.SessionID{}, false
	}
	return *i.sessionID, true
}

func (i SessionLaunchIntent) Equal(other SessionLaunchIntent) bool {
	if i.kind != other.kind {
		return false
	}
	leftParent, leftHasParent := i.ParentID()
	rightParent, rightHasParent := other.ParentID()
	if leftHasParent != rightHasParent || (leftHasParent && leftParent != rightParent) {
		return false
	}
	leftSession, leftHasSession := i.SessionID()
	rightSession, rightHasSession := other.SessionID()
	return leftHasSession == rightHasSession && (!leftHasSession || leftSession == rightSession)
}

func (i SessionLaunchIntent) Validate() error {
	switch i.kind {
	case SessionLaunchIntentCreateNew:
		if i.sessionID != nil {
			return errors.New("create_new session launch intent cannot contain session_id")
		}
		if i.parentID != nil && i.parentID.IsZero() {
			return errors.New("create_new session launch intent parent_id is invalid")
		}
	case SessionLaunchIntentOpenExisting:
		if i.parentID != nil {
			return errors.New("open_existing session launch intent cannot contain parent_id")
		}
		if i.sessionID == nil || i.sessionID.IsZero() {
			return errors.New("open_existing session launch intent requires session_id")
		}
	default:
		return errors.New("session launch intent kind is invalid")
	}
	return nil
}

func (i SessionLaunchIntent) MarshalJSON() ([]byte, error) {
	if err := i.Validate(); err != nil {
		return nil, err
	}
	type wire struct {
		Kind      SessionLaunchIntentKind `json:"kind"`
		ParentID  *runtimeids.SessionID   `json:"parent_id,omitempty"`
		SessionID *runtimeids.SessionID   `json:"session_id,omitempty"`
	}
	return json.Marshal(wire{
		Kind:      i.kind,
		ParentID:  i.parentID,
		SessionID: i.sessionID,
	})
}

func (i *SessionLaunchIntent) UnmarshalJSON(data []byte) error {
	var wire struct {
		Kind      SessionLaunchIntentKind `json:"kind"`
		ParentID  *runtimeids.SessionID   `json:"parent_id"`
		SessionID *runtimeids.SessionID   `json:"session_id"`
	}
	if err := decodeStrictJSON(data, &wire); err != nil {
		return err
	}
	decoded := SessionLaunchIntent{
		kind:      wire.Kind,
		parentID:  wire.ParentID,
		sessionID: wire.SessionID,
	}
	if err := decoded.Validate(); err != nil {
		return err
	}
	*i = decoded
	return nil
}

type SessionPlanRequest struct {
	ClientRequestID string              `json:"client_request_id"`
	Mode            SessionLaunchMode   `json:"mode"`
	Intent          SessionLaunchIntent `json:"intent"`
	Overrides       RunPromptOverrides  `json:"overrides,omitempty"`
}

func (r *SessionPlanRequest) UnmarshalJSON(data []byte) error {
	type wire struct {
		ClientRequestID string              `json:"client_request_id"`
		Mode            SessionLaunchMode   `json:"mode"`
		Intent          SessionLaunchIntent `json:"intent"`
		Overrides       RunPromptOverrides  `json:"overrides"`
	}
	var decoded wire
	if err := decodeStrictJSON(data, &decoded); err != nil {
		return err
	}
	request := SessionPlanRequest(decoded)
	if err := request.Validate(); err != nil {
		return err
	}
	*r = request
	return nil
}

type SessionPlan struct {
	SessionID           string              `json:"session_id"`
	ActiveSettings      config.Settings     `json:"active_settings"`
	EnabledToolIDs      []string            `json:"enabled_tool_ids,omitempty"`
	ConfiguredModelName string              `json:"configured_model_name,omitempty"`
	SessionName         string              `json:"session_name,omitempty"`
	PromptHistory       []string            `json:"prompt_history,omitempty"`
	ModelContractLocked bool                `json:"model_contract_locked,omitempty"`
	WorkspaceRoot       string              `json:"workspace_root,omitempty"`
	Source              config.SourceReport `json:"source"`
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
	if err := r.Intent.Validate(); err != nil {
		return fmt.Errorf("intent: %w", err)
	}
	return r.Overrides.ValidateAgentRoleOverride()
}

func decodeStrictJSON(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("unexpected trailing JSON value")
		}
		return err
	}
	return nil
}
