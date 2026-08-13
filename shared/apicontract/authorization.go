package apicontract

import (
	"core/shared/clientui"
	"core/shared/runtimeids"
)

type AuthorizedProjectWorkspaceBinding struct {
	ProjectID     string
	WorkspaceID   string
	CanonicalRoot string
}

type AuthorizedSessionAttachment struct {
	SessionID     runtimeids.SessionID
	ProjectID     string
	WorkspaceID   string
	CanonicalRoot string
}

type ProcessAuthorizationCandidate struct {
	ProcessID      string
	OwnerSessionID string
	Process        clientui.BackgroundProcess
}

type AuthorizedProcessInActiveProject struct {
	ProcessID      string
	OwnerSessionID string
	Process        clientui.BackgroundProcess
}
