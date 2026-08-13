package apicontract

import "core/shared/clientui"

type AuthorizedProjectWorkspaceBinding struct {
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
