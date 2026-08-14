package serverapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"core/shared/config"
	"core/shared/protocol"
	"core/shared/runtimeids"
)

type SessionLaunchMode string

const SessionPromptHistoryMaxEntries = 100

const (
	SessionLaunchModeInteractive SessionLaunchMode = "interactive"
	SessionLaunchModeHeadless    SessionLaunchMode = "headless"
)

type SessionLaunchIntentKind string
type SessionCreateOriginKind string

const (
	SessionLaunchIntentCreateNew    SessionLaunchIntentKind = "create_new"
	SessionLaunchIntentOpenExisting SessionLaunchIntentKind = "open_existing"

	SessionCreateOriginIndependent     SessionCreateOriginKind = "independent"
	SessionCreateOriginPreviousSession SessionCreateOriginKind = "previous_session"
	SessionCreateOriginParentAgent     SessionCreateOriginKind = "parent_agent"
)

type SessionCreateOrigin struct {
	kind      SessionCreateOriginKind
	sessionID *runtimeids.SessionID
}

func IndependentSessionCreateOrigin() SessionCreateOrigin {
	return SessionCreateOrigin{kind: SessionCreateOriginIndependent}
}

func PreviousSessionCreateOrigin(sessionID runtimeids.SessionID) SessionCreateOrigin {
	return sessionCreateOriginWithID(SessionCreateOriginPreviousSession, sessionID)
}

func ParentAgentSessionCreateOrigin(sessionID runtimeids.SessionID) SessionCreateOrigin {
	return sessionCreateOriginWithID(SessionCreateOriginParentAgent, sessionID)
}

func sessionCreateOriginWithID(kind SessionCreateOriginKind, sessionID runtimeids.SessionID) SessionCreateOrigin {
	copied := sessionID
	return SessionCreateOrigin{kind: kind, sessionID: &copied}
}

func (o SessionCreateOrigin) Kind() SessionCreateOriginKind {
	return o.kind
}

func (o SessionCreateOrigin) SessionID() (runtimeids.SessionID, bool) {
	if o.sessionID == nil {
		return runtimeids.SessionID{}, false
	}
	return *o.sessionID, true
}

func (o SessionCreateOrigin) Equal(other SessionCreateOrigin) bool {
	if o.kind != other.kind {
		return false
	}
	left, leftPresent := o.SessionID()
	right, rightPresent := other.SessionID()
	return leftPresent == rightPresent && (!leftPresent || left == right)
}

func (o SessionCreateOrigin) Validate() error {
	switch o.kind {
	case SessionCreateOriginIndependent:
		if o.sessionID != nil {
			return errors.New("independent session create origin cannot contain session_id")
		}
	case SessionCreateOriginPreviousSession, SessionCreateOriginParentAgent:
		if o.sessionID == nil || o.sessionID.IsZero() {
			return fmt.Errorf("%s session create origin requires session_id", o.kind)
		}
	default:
		return errors.New("session create origin kind is invalid")
	}
	return nil
}

func (o SessionCreateOrigin) MarshalJSON() ([]byte, error) {
	if err := o.Validate(); err != nil {
		return nil, err
	}
	type wire struct {
		Kind      SessionCreateOriginKind `json:"kind"`
		SessionID *runtimeids.SessionID   `json:"session_id,omitempty"`
	}
	return json.Marshal(wire{Kind: o.kind, SessionID: o.sessionID})
}

func (o *SessionCreateOrigin) UnmarshalJSON(data []byte) error {
	var wire struct {
		Kind      SessionCreateOriginKind `json:"kind"`
		SessionID *runtimeids.SessionID   `json:"session_id"`
	}
	if err := protocol.DecodeStrictJSON(data, &wire); err != nil {
		return err
	}
	decoded := SessionCreateOrigin{kind: wire.Kind, sessionID: wire.SessionID}
	if err := decoded.Validate(); err != nil {
		return err
	}
	*o = decoded
	return nil
}

type SessionLaunchIntent struct {
	kind      SessionLaunchIntentKind
	origin    *SessionCreateOrigin
	sessionID *runtimeids.SessionID
}

func CreateNewSessionLaunchIntent(origin SessionCreateOrigin) SessionLaunchIntent {
	copied := origin
	return SessionLaunchIntent{kind: SessionLaunchIntentCreateNew, origin: &copied}
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

func (i SessionLaunchIntent) CreateOrigin() (SessionCreateOrigin, bool) {
	if i.origin == nil {
		return SessionCreateOrigin{}, false
	}
	return *i.origin, true
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
	leftOrigin, leftHasOrigin := i.CreateOrigin()
	rightOrigin, rightHasOrigin := other.CreateOrigin()
	if leftHasOrigin != rightHasOrigin || (leftHasOrigin && !leftOrigin.Equal(rightOrigin)) {
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
		if i.origin == nil {
			return errors.New("create_new session launch intent requires origin")
		}
		if err := i.origin.Validate(); err != nil {
			return fmt.Errorf("create_new session launch intent origin: %w", err)
		}
	case SessionLaunchIntentOpenExisting:
		if i.origin != nil {
			return errors.New("open_existing session launch intent cannot contain origin")
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
		Origin    *SessionCreateOrigin    `json:"origin,omitempty"`
		SessionID *runtimeids.SessionID   `json:"session_id,omitempty"`
	}
	return json.Marshal(wire{
		Kind:      i.kind,
		Origin:    i.origin,
		SessionID: i.sessionID,
	})
}

func (i *SessionLaunchIntent) UnmarshalJSON(data []byte) error {
	var wire struct {
		Kind      SessionLaunchIntentKind `json:"kind"`
		Origin    *SessionCreateOrigin    `json:"origin"`
		SessionID *runtimeids.SessionID   `json:"session_id"`
	}
	if err := protocol.DecodeStrictJSON(data, &wire); err != nil {
		return err
	}
	decoded := SessionLaunchIntent{
		kind:      wire.Kind,
		origin:    wire.Origin,
		sessionID: wire.SessionID,
	}
	if err := decoded.Validate(); err != nil {
		return err
	}
	*i = decoded
	return nil
}

type SessionPlanRequest struct {
	Mode            SessionLaunchMode   `json:"mode"`
	Intent          SessionLaunchIntent `json:"intent"`
	CallerSessionID *string             `json:"caller_session_id,omitempty"`
	Overrides       RunPromptOverrides  `json:"overrides,omitempty"`
}

func (r *SessionPlanRequest) UnmarshalJSON(data []byte) error {
	type wire struct {
		Mode            SessionLaunchMode   `json:"mode"`
		Intent          SessionLaunchIntent `json:"intent"`
		CallerSessionID *string             `json:"caller_session_id"`
		Overrides       RunPromptOverrides  `json:"overrides"`
	}
	var decoded wire
	if err := protocol.DecodeStrictJSON(data, &decoded); err != nil {
		return err
	}
	request := SessionPlanRequest{
		Mode:            decoded.Mode,
		Intent:          decoded.Intent,
		CallerSessionID: decoded.CallerSessionID,
		Overrides:       decoded.Overrides,
	}
	if err := request.Validate(); err != nil {
		return err
	}
	*r = request
	return nil
}

type SessionPlan struct {
	SessionID                string              `json:"session_id"`
	ActiveSettings           config.Settings     `json:"active_settings"`
	EnabledToolIDs           []string            `json:"enabled_tool_ids,omitempty"`
	ConfiguredModelName      string              `json:"configured_model_name,omitempty"`
	SessionName              *string             `json:"session_name"`
	PromptHistory            []string            `json:"prompt_history,omitempty"`
	ModelContractLocked      bool                `json:"model_contract_locked,omitempty"`
	QuestionsEnabled         bool                `json:"questions_enabled"`
	AutoCompactionEnabled    bool                `json:"auto_compaction_enabled"`
	ThinkingOverrideExplicit bool                `json:"thinking_override_explicit"`
	Source                   config.SourceReport `json:"source"`
}

func (p SessionPlan) Validate() error {
	if p.SessionName != nil && strings.TrimSpace(*p.SessionName) == "" {
		return errors.New("session plan name cannot be empty or blank")
	}
	return nil
}

type SessionPlanResponse struct {
	Plan     SessionPlan `json:"plan"`
	Warnings []string    `json:"warnings,omitempty"`
}

func (r SessionPlanRequest) Validate() error {
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
	if err := ValidateOptionalIdentifier("caller_session_id", r.CallerSessionID); err != nil {
		return err
	}
	return r.Overrides.ValidateAgentRoleOverride()
}
