package edit

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"core/server/tools"
	"core/shared/textutil"
)

type failure struct {
	Message string
}

func (f failure) Error() string {
	message := strings.TrimSpace(f.Message)
	if message == "" {
		return "Edit failed."
	}
	if strings.HasPrefix(message, "Edit failed:") {
		return message
	}
	return "Edit failed: " + message
}

func failf(format string, args ...any) error {
	return failure{Message: fmt.Sprintf(format, args...)}
}

func editErrorResult(c tools.Call, err error) tools.Result {
	if errors.Is(err, tools.ErrForeignManagedWorktreeEdit) {
		return tools.ErrorResult(c, tools.ForeignManagedWorktreeEditDeniedMessage)
	}
	message := "Edit failed."
	if err != nil {
		message = err.Error()
	}
	if !strings.HasPrefix(strings.TrimSpace(message), "Edit failed") {
		message = "Edit failed: " + strings.TrimSpace(message)
	}
	body, _ := json.Marshal(message)
	return tools.Result{
		CallID: c.ID, Name: c.Name, Output: body, IsError: true,
		Summary: textutil.Value(message),
	}
}

func editFileAccessFailure(outcome tools.FileAccessOutcome) error {
	path := strings.TrimSpace(outcome.Request.RequestedPath)
	switch outcome.Kind {
	case tools.FileAccessDeniedForeignManagedWorktree:
		return tools.ErrForeignManagedWorktreeEdit
	case tools.FileAccessDeniedByPathPolicy:
		if outcome.PathDeny == nil {
			return failf("file access path-deny outcome has no match")
		}
		return failf("no file edit permission for %s. %s", path, outcome.PathDeny.Message)
	case tools.FileAccessDeniedOutsideWorkspace:
		return failf("no file edit permission for %s. edit target outside workspace", path)
	case tools.FileAccessDeniedByUser:
		if outcome.Commentary == nil {
			return failf("user denied the edit for %s.", path)
		}
		return failf("user denied the edit for %s.\nUser said: %s", path, strings.TrimSpace(*outcome.Commentary))
	case tools.FileAccessApprovalFailed:
		if outcome.Cause == nil {
			return failf("file edit approval failed for %s.", path)
		}
		return failf("file edit approval failed for %s. %s", path, outcome.Cause)
	case tools.FileAccessPolicyFailed:
		if outcome.Cause == nil {
			return failf("file access policy failed for %s.", path)
		}
		return outcome.Cause
	default:
		return failf("unexpected file access outcome %d for %s.", outcome.Kind, path)
	}
}

func editSuccessResult(c tools.Call, message string) tools.Result {
	trimmed := strings.TrimSpace(message)
	if trimmed == "" {
		trimmed = "ok"
	}
	body, _ := json.Marshal(trimmed)
	return tools.Result{
		CallID: c.ID, Name: c.Name, Output: body, Summary: textutil.Value(trimmed),
	}
}
