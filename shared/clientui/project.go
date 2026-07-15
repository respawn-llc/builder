package clientui

import (
	"errors"
	"time"

	"core/shared/runtimeids"
	"core/shared/sessioncontract"
)

type ProjectAvailability string

const (
	ProjectAvailabilityAvailable    ProjectAvailability = "available"
	ProjectAvailabilityMissing      ProjectAvailability = "missing"
	ProjectAvailabilityInaccessible ProjectAvailability = "inaccessible"
	ProjectAvailabilityUnlinked     ProjectAvailability = "unlinked"
)

type ProjectSummary struct {
	ProjectID    string
	ProjectKey   string
	DisplayName  string
	RootPath     string
	Availability ProjectAvailability
	SessionCount int
	UpdatedAt    time.Time
}

type ProjectWorkspaceSummary struct {
	WorkspaceID  string
	DisplayName  string
	RootPath     string
	Availability ProjectAvailability
	IsPrimary    bool
	SessionCount int
	UpdatedAt    time.Time
}

type ProjectOverview struct {
	Project    ProjectSummary
	Workspaces []ProjectWorkspaceSummary
}

type SessionSummary struct {
	SessionID          runtimeids.SessionID            `json:"session_id"`
	Category           sessioncontract.SessionCategory `json:"category"`
	Name               string                          `json:"name,omitempty"`
	FirstPromptPreview string                          `json:"first_prompt_preview,omitempty"`
	UpdatedAt          time.Time                       `json:"updated_at"`
}

func (s SessionSummary) Validate() error {
	if s.SessionID.IsZero() {
		return errors.New("session summary session_id is required")
	}
	if _, err := sessioncontract.ParseSessionCategory(string(s.Category)); err != nil {
		return err
	}
	if s.UpdatedAt.IsZero() || s.UpdatedAt.Unix() <= 0 {
		return errors.New("session summary updated_at must be after the Unix epoch")
	}
	return nil
}
