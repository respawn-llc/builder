package worktreecontract

import "strings"

type ProjectAvailability string

const (
	ProjectAvailabilityAvailable    ProjectAvailability = "available"
	ProjectAvailabilityMissing      ProjectAvailability = "missing"
	ProjectAvailabilityInaccessible ProjectAvailability = "inaccessible"
	ProjectAvailabilityUnlinked     ProjectAvailability = "unlinked"
)

type SessionExecutionTarget struct {
	WorkspaceID           string
	WorkspaceName         string
	WorkspaceRoot         string
	WorkspaceAvailability ProjectAvailability
	Worktree              *SessionExecutionWorktreeTarget
	CwdRelpath            string
	EffectiveWorkdir      string
}

type SessionExecutionWorktreeTarget struct {
	ID           string
	Name         string
	Root         string
	Availability string
}

func SessionExecutionTargetIsZero(target SessionExecutionTarget) bool {
	return strings.TrimSpace(target.WorkspaceID) == "" &&
		strings.TrimSpace(target.WorkspaceName) == "" &&
		strings.TrimSpace(target.WorkspaceRoot) == "" &&
		strings.TrimSpace(string(target.WorkspaceAvailability)) == "" &&
		target.Worktree == nil &&
		strings.TrimSpace(target.CwdRelpath) == "" &&
		strings.TrimSpace(target.EffectiveWorkdir) == ""
}
