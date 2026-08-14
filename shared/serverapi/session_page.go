package serverapi

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"core/shared/clientui"
	"core/shared/protocol"
	"core/shared/sessioncontract"
)

const MaxSessionPageSize = OffsetPaginationMaxLimit

type SessionPageRequest struct {
	ProjectID string                          `json:"project_id"`
	Category  sessioncontract.SessionCategory `json:"category"`
	Offset    *int                            `json:"offset,omitempty"`
	Limit     *int                            `json:"limit,omitempty"`
}

func (r *SessionPageRequest) UnmarshalJSON(data []byte) error {
	type wire SessionPageRequest
	var decoded wire
	if err := protocol.DecodeStrictJSON(data, &decoded); err != nil {
		return err
	}
	*r = SessionPageRequest(decoded)
	return nil
}

func (r SessionPageRequest) ResolveWindow() (OffsetWindow, error) {
	return ResolveOffsetWindow(r.Offset, r.Limit)
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
	_, err := r.ResolveWindow()
	return err
}

type SessionPageResponse struct {
	ProjectID  string                          `json:"project_id"`
	Category   sessioncontract.SessionCategory `json:"category"`
	Sessions   []clientui.SessionSummary       `json:"sessions"`
	NextOffset *int                            `json:"next_offset,omitempty"`
}

func (r *SessionPageResponse) UnmarshalJSON(data []byte) error {
	type wire SessionPageResponse
	var decoded wire
	if err := protocol.DecodeStrictJSON(data, &decoded); err != nil {
		return err
	}
	*r = SessionPageResponse(decoded)
	return nil
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
	if r.NextOffset != nil && *r.NextOffset <= 0 {
		return errors.New("next_offset must be positive when present")
	}
	return nil
}

func validSessionRecency(value time.Time) bool {
	return !value.IsZero() && value.After(time.Unix(0, 0).UTC())
}
