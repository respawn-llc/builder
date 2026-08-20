package serverapi

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"core/shared/clientui"
	"core/shared/sessioncontract"
)

const MaxSessionPageSize = OffsetPaginationMaxLimit

type SessionPageRequest struct {
	ProjectID string
	Category  sessioncontract.SessionCategory
	Offset    *int
	Limit     *int
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
	ProjectID  string
	Category   sessioncontract.SessionCategory
	Sessions   []clientui.SessionSummary
	NextOffset *int
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
