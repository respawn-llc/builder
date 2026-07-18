package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"core/shared/llmerrors"
)

var errRuntimeSubmissionInterrupted = errors.New("interrupted")

func formatRuntimeSubmissionError(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, errRuntimeSubmissionInterrupted) || errors.Is(err, context.Canceled) {
		return ""
	}
	if formatted := llmerrors.UserFacingError(err); strings.TrimSpace(formatted) != "" {
		return formatted
	}
	var statusErr *llmerrors.APIStatusError
	if errors.As(err, &statusErr) {
		body := statusErr.Body
		if strings.TrimSpace(body) == "" {
			body = "<empty error body>"
		}
		return fmt.Sprintf("openai status %d\nresponse body:\n%s", statusErr.StatusCode, body)
	}
	return err.Error()
}
