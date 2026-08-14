package apicontract

import (
	"strings"
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
