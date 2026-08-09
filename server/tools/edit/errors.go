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
	if errors.Is(err, tools.ErrForeignManagedWorktreeEditDenied) {
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
