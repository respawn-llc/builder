package serverapi

import (
	"errors"
	"strings"

	"core/shared/runtimeids"
)

// ErrSessionIDRequired is returned when a session id is empty or whitespace-only.
var ErrSessionIDRequired = errors.New("session_id is required")

// ErrSessionIDNotSingle is returned when a session id is not a single,
// container-relative session id (absolute, traversal, or contains separators).
var ErrSessionIDNotSingle = errors.New("session_id must be a single session id")

func validateRequiredSessionID(sessionID string) error {
	if strings.TrimSpace(sessionID) == "" {
		return ErrSessionIDRequired
	}
	return nil
}

func validateScopedSessionID(sessionID string) error {
	trimmed := strings.TrimSpace(sessionID)
	if err := validateRequiredSessionID(trimmed); err != nil {
		return err
	}
	if _, err := runtimeids.ParseSessionID(trimmed); err != nil {
		return ErrSessionIDNotSingle
	}
	return nil
}
