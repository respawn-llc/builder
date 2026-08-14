package serverapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"core/shared/clientui"
	"core/shared/sessioncontract"
)

const MaxSessionPageSize = 100

type SessionPageContinuation struct {
	value string
}

func ParseSessionPageContinuation(raw string) (SessionPageContinuation, error) {
	if raw == "" {
		return SessionPageContinuation{}, errors.New("session page continuation is required")
	}
	if strings.TrimSpace(raw) != raw {
		return SessionPageContinuation{}, errors.New("session page continuation must not have leading or trailing whitespace")
	}
	return SessionPageContinuation{value: raw}, nil
}

func (c SessionPageContinuation) String() string {
	return c.value
}

func (c SessionPageContinuation) Validate() error {
	_, err := ParseSessionPageContinuation(c.value)
	return err
}

func (c SessionPageContinuation) MarshalJSON() ([]byte, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(c.value)
}

func (c *SessionPageContinuation) UnmarshalJSON(data []byte) error {
	var raw string
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	parsed, err := ParseSessionPageContinuation(raw)
	if err != nil {
		return err
	}
	*c = parsed
	return nil
}

type SessionPagePositionKind string

const (
	SessionPagePositionNewest SessionPagePositionKind = "newest"
	SessionPagePositionOlder  SessionPagePositionKind = "older"
	SessionPagePositionNewer  SessionPagePositionKind = "newer"
)

type SessionPagePosition struct {
	kind         SessionPagePositionKind
	continuation *SessionPageContinuation
}

func NewestSessionPagePosition() SessionPagePosition {
	return SessionPagePosition{kind: SessionPagePositionNewest}
}

func OlderSessionPagePosition(continuation SessionPageContinuation) SessionPagePosition {
	return SessionPagePosition{kind: SessionPagePositionOlder, continuation: &continuation}
}

func NewerSessionPagePosition(continuation SessionPageContinuation) SessionPagePosition {
	return SessionPagePosition{kind: SessionPagePositionNewer, continuation: &continuation}
}

func (p SessionPagePosition) Kind() SessionPagePositionKind {
	return p.kind
}

func (p SessionPagePosition) Continuation() (SessionPageContinuation, bool) {
	if p.continuation == nil {
		return SessionPageContinuation{}, false
	}
	return *p.continuation, true
}

func (p SessionPagePosition) Validate() error {
	switch p.kind {
	case SessionPagePositionNewest:
		if p.continuation != nil {
			return errors.New("newest session page position cannot contain a continuation")
		}
	case SessionPagePositionOlder, SessionPagePositionNewer:
		if p.continuation == nil {
			return fmt.Errorf("%s session page position requires a continuation", p.kind)
		}
		if err := p.continuation.Validate(); err != nil {
			return err
		}
	default:
		return errors.New("session page position kind is invalid")
	}
	return nil
}

func (p SessionPagePosition) MarshalJSON() ([]byte, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	type wire struct {
		Kind  SessionPagePositionKind  `json:"kind"`
		Token *SessionPageContinuation `json:"token,omitempty"`
	}
	return json.Marshal(wire{Kind: p.kind, Token: p.continuation})
}

func (p *SessionPagePosition) UnmarshalJSON(data []byte) error {
	var wire struct {
		Kind  SessionPagePositionKind  `json:"kind"`
		Token *SessionPageContinuation `json:"token"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return err
	}
	decoded := SessionPagePosition{kind: wire.Kind, continuation: wire.Token}
	if err := decoded.Validate(); err != nil {
		return err
	}
	*p = decoded
	return nil
}

type SessionPageRequest struct {
	ProjectID string                          `json:"project_id"`
	Category  sessioncontract.SessionCategory `json:"category"`
	PageSize  int                             `json:"page_size"`
	Position  SessionPagePosition             `json:"position"`
}

func (r SessionPageRequest) Validate() error {
	if strings.TrimSpace(r.ProjectID) == "" {
		return errors.New("project_id is required")
	}
	if strings.TrimSpace(r.ProjectID) != r.ProjectID {
		return errors.New("project_id must not have leading or trailing whitespace")
	}
	if _, err := sessioncontract.ParseSessionCategory(string(r.Category)); err != nil {
		return err
	}
	if r.PageSize < 1 || r.PageSize > MaxSessionPageSize {
		return fmt.Errorf("page_size must be between 1 and %d", MaxSessionPageSize)
	}
	return r.Position.Validate()
}

type SessionPageResponse struct {
	ProjectID string                          `json:"project_id"`
	Category  sessioncontract.SessionCategory `json:"category"`
	Sessions  []clientui.SessionSummary       `json:"sessions"`
	Older     *SessionPageContinuation        `json:"older,omitempty"`
	Newer     *SessionPageContinuation        `json:"newer,omitempty"`
}

func (r SessionPageResponse) Validate() error {
	if strings.TrimSpace(r.ProjectID) == "" || strings.TrimSpace(r.ProjectID) != r.ProjectID {
		return errors.New("project_id is invalid")
	}
	if _, err := sessioncontract.ParseSessionCategory(string(r.Category)); err != nil {
		return err
	}
	if len(r.Sessions) > MaxSessionPageSize {
		return fmt.Errorf("session page exceeds maximum size %d", MaxSessionPageSize)
	}
	for index, summary := range r.Sessions {
		if summary.SessionID.IsZero() {
			return fmt.Errorf("sessions[%d].session_id is required", index)
		}
		if summary.Category != r.Category {
			return fmt.Errorf("sessions[%d].category does not match page category", index)
		}
		if !validSessionRecency(summary.UpdatedAt) {
			return fmt.Errorf("sessions[%d].updated_at is invalid", index)
		}
	}
	if r.Older != nil {
		if err := r.Older.Validate(); err != nil {
			return err
		}
	}
	if r.Newer != nil {
		if err := r.Newer.Validate(); err != nil {
			return err
		}
	}
	return nil
}

func validSessionRecency(value time.Time) bool {
	return !value.IsZero() && value.After(time.Unix(0, 0).UTC())
}
