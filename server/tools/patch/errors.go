package patch

import (
	"errors"
	"fmt"
	"strings"

	"core/server/tools"
)

type failureKind string

const (
	failureKindMalformedSyntax failureKind = "malformed_syntax"
	failureKindContentMismatch failureKind = "content_mismatch"
	failureKindOutOfBounds     failureKind = "out_of_bounds"
	failureKindNoPermission    failureKind = "no_permission"
	failureKindUserDenied      failureKind = "user_denied"
	failureKindApprovalFailed  failureKind = "approval_failed"
	failureKindTargetMissing   failureKind = "target_missing"
	failureKindTargetExists    failureKind = "target_exists"
	failureKindInternal        failureKind = "internal"
)

type failure struct {
	Kind       failureKind
	Path       string
	Line       int
	NearLine   bool
	Reason     string
	Commentary *string
}

type failurePayload struct {
	Error      string      `json:"error"`
	Kind       failureKind `json:"kind,omitempty"`
	Path       string      `json:"path,omitempty"`
	Line       int         `json:"line,omitempty"`
	NearLine   bool        `json:"near_line,omitempty"`
	Reason     string      `json:"reason,omitempty"`
	Commentary *string     `json:"commentary,omitempty"`
}

func (f *failure) Error() string {
	if f == nil {
		return "Patch failed."
	}
	return f.message()
}

func (f *failure) message() string {
	path := strings.TrimSpace(f.Path)
	reason := strings.TrimSpace(f.Reason)
	lineRef := ""
	if f.Line > 0 {
		if f.NearLine {
			lineRef = fmt.Sprintf(" near line %d", f.Line)
		} else {
			lineRef = fmt.Sprintf(" at line %d", f.Line)
		}
	}
	withReason := func(base string) string {
		if reason == "" {
			return base
		}
		return base + "\nReason: " + reason
	}
	pathSuffix := func() string {
		if path == "" {
			return ""
		}
		return " in " + path
	}

	switch f.Kind {
	case failureKindMalformedSyntax:
		return withReason("Patch failed: malformed patch syntax.")
	case failureKindContentMismatch:
		return withReason("Patch failed: mismatch between file content and model-provided patch" + pathSuffix() + lineRef + ".")
	case failureKindOutOfBounds:
		return withReason("Patch failed: model tried to change lines outside file bounds" + pathSuffix() + lineRef + ".")
	case failureKindNoPermission:
		if path == "" {
			return withReason("Patch failed: no file edit permission.")
		}
		return withReason("Patch failed: no file edit permission for " + path + ".")
	case failureKindUserDenied:
		message := "Patch failed: user denied the edit"
		if path != "" {
			message += " for " + path
		}
		message += "."
		if f.Commentary != nil {
			message += "\nUser said: " + strings.TrimSpace(*f.Commentary)
		}
		return message
	case failureKindApprovalFailed:
		if path == "" {
			return withReason("Patch failed: file edit approval failed.")
		}
		return withReason("Patch failed: file edit approval failed for " + path + ".")
	case failureKindTargetMissing:
		if path == "" {
			return withReason("Patch failed: target file does not exist.")
		}
		return withReason("Patch failed: target file does not exist: " + path + ".")
	case failureKindTargetExists:
		if path == "" {
			return withReason("Patch failed: target file already exists.")
		}
		return withReason("Patch failed: target file already exists: " + path + ".")
	default:
		return withReason("Patch failed.")
	}
}

func malformedFailure(reason string) error {
	return &failure{Kind: failureKindMalformedSyntax, Reason: reason}
}

func contentMismatchFailure(line int, near bool, reason string) error {
	return &failure{Kind: failureKindContentMismatch, Line: line, NearLine: near, Reason: reason}
}

func outOfBoundsFailure(line int, reason string) error {
	return &failure{Kind: failureKindOutOfBounds, Line: line, Reason: reason}
}

func noPermissionFailure(path, reason string) error {
	return &failure{Kind: failureKindNoPermission, Path: path, Reason: reason}
}

func userDeniedFailure(path string, commentary *string) error {
	return &failure{Kind: failureKindUserDenied, Path: path, Commentary: commentary}
}

func approvalFailedFailure(path, reason string) error {
	return &failure{Kind: failureKindApprovalFailed, Path: path, Reason: reason}
}

func fileAccessFailure(outcome tools.FileAccessOutcome) error {
	path := strings.TrimSpace(outcome.Request.RequestedPath)
	switch outcome.Kind {
	case tools.FileAccessDeniedForeignManagedWorktree:
		return tools.ErrForeignManagedWorktreeEdit
	case tools.FileAccessDeniedByPathPolicy:
		if outcome.PathDeny == nil {
			return internalFailure(path, "file access path-deny outcome has no match")
		}
		return noPermissionFailure(path, outcome.PathDeny.Message)
	case tools.FileAccessDeniedOutsideWorkspace:
		return noPermissionFailure(path, "patch target outside workspace")
	case tools.FileAccessDeniedByUser:
		return userDeniedFailure(path, outcome.Commentary)
	case tools.FileAccessApprovalFailed:
		if outcome.Cause == nil {
			return approvalFailedFailure(path, "approval failed")
		}
		return approvalFailedFailure(path, outcome.Cause.Error())
	case tools.FileAccessPolicyFailed:
		if outcome.Cause == nil {
			return internalFailure(path, "file access policy failed")
		}
		return outcome.Cause
	default:
		return internalFailure(path, fmt.Sprintf("unexpected file access outcome %d", outcome.Kind))
	}
}

func targetMissingFailure(path, reason string) error {
	return &failure{Kind: failureKindTargetMissing, Path: path, Reason: reason}
}

func targetExistsFailure(path, reason string) error {
	return &failure{Kind: failureKindTargetExists, Path: path, Reason: reason}
}

func internalFailure(path, reason string) error {
	return &failure{Kind: failureKindInternal, Path: path, Reason: reason}
}

func attachFailurePath(err error, path string) error {
	if err == nil {
		return nil
	}
	var f *failure
	if !errors.As(err, &f) || f == nil {
		return internalFailure(path, err.Error())
	}
	if strings.TrimSpace(f.Path) != "" {
		return err
	}
	copy := *f
	copy.Path = path
	return &copy
}

func attachFailureReasonContext(err error, context string) error {
	if err == nil {
		return nil
	}
	trimmedContext := strings.TrimSpace(context)
	if trimmedContext == "" {
		return err
	}
	var f *failure
	if !errors.As(err, &f) || f == nil {
		return internalFailure("", trimmedContext+": "+err.Error())
	}
	copy := *f
	trimmedReason := strings.TrimSpace(copy.Reason)
	if trimmedReason == "" {
		copy.Reason = trimmedContext
	} else {
		copy.Reason = trimmedContext + ": " + trimmedReason
	}
	return &copy
}

func errorPayload(err error) failurePayload {
	message := "Patch failed."
	if err != nil {
		message = err.Error()
	}
	payload := failurePayload{Error: message}
	var f *failure
	if !errors.As(err, &f) || f == nil {
		return payload
	}
	payload.Kind = f.Kind
	payload.Path = strings.TrimSpace(f.Path)
	payload.Line = f.Line
	payload.NearLine = f.NearLine
	payload.Reason = strings.TrimSpace(f.Reason)
	if f.Commentary != nil {
		commentary := strings.TrimSpace(*f.Commentary)
		payload.Commentary = &commentary
	}
	return payload
}
