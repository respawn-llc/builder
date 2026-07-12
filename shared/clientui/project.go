package clientui

import "time"

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
	Sessions   []SessionSummary
}

type SessionSummary struct {
	SessionID          string
	Name               string
	FirstPromptPreview string
	UpdatedAt          time.Time
}
