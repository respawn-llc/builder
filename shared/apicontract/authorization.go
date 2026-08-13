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

type AuthorizedSessionInActiveProject struct {
	SessionID       runtimeids.SessionID
	ActiveProjectID string
	OwningProjectID string
	ExecutionTarget clientui.SessionExecutionTarget
}

type optionalAuthorizedSessionKind uint8

const (
	optionalAuthorizedSessionAbsent optionalAuthorizedSessionKind = iota
	optionalAuthorizedSessionPresent
)

type OptionalAuthorizedSessionInActiveProject struct {
	kind          optionalAuthorizedSessionKind
	authorization AuthorizedSessionInActiveProject
}

func AbsentAuthorizedSessionInActiveProject() OptionalAuthorizedSessionInActiveProject {
	return OptionalAuthorizedSessionInActiveProject{kind: optionalAuthorizedSessionAbsent}
}

func PresentAuthorizedSessionInActiveProject(
	authorization AuthorizedSessionInActiveProject,
) OptionalAuthorizedSessionInActiveProject {
	return OptionalAuthorizedSessionInActiveProject{
		kind:          optionalAuthorizedSessionPresent,
		authorization: authorization,
	}
}

func (o OptionalAuthorizedSessionInActiveProject) Authorization() (AuthorizedSessionInActiveProject, bool) {
	switch o.kind {
	case optionalAuthorizedSessionAbsent:
		return AuthorizedSessionInActiveProject{}, false
	case optionalAuthorizedSessionPresent:
		return o.authorization, true
	default:
		panic("invalid optional active-Project Session authorization kind")
	}
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
