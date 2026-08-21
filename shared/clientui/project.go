package clientui

import (
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
	Name               *string                         `json:"name,omitempty"`
	FirstPromptPreview *string                         `json:"first_prompt_preview,omitempty"`
	UpdatedAt          time.Time                       `json:"updated_at"`
}
