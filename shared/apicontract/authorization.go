package apicontract

import (
	"strings"

	"core/shared/clientui"
	"core/shared/runtimeids"
)

type attachedProjectConstraintKind uint8

const (
	attachedProjectConstraintAbsent attachedProjectConstraintKind = iota
	attachedProjectConstraintPresent
)

type AttachedProjectConstraint struct {
	kind      attachedProjectConstraintKind
	projectID string
}

func AbsentAttachedProjectConstraint() AttachedProjectConstraint {
	return AttachedProjectConstraint{kind: attachedProjectConstraintAbsent}
}

func PresentAttachedProjectConstraint(projectID string) AttachedProjectConstraint {
	trimmed := strings.TrimSpace(projectID)
	if trimmed == "" {
		panic("attached Project constraint requires a Project ID")
	}
	return AttachedProjectConstraint{
		kind:      attachedProjectConstraintPresent,
		projectID: trimmed,
	}
}

func (c AttachedProjectConstraint) ProjectID() (string, bool) {
	switch c.kind {
	case attachedProjectConstraintAbsent:
		return "", false
	case attachedProjectConstraintPresent:
		return c.projectID, true
	default:
		panic("invalid attached-Project constraint kind")
	}
}

type AuthorizedProjectWorkspaceBinding struct {
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
