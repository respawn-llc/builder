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
	_, err := parseScopedSessionID(sessionID)
	return err
}

func parseScopedSessionID(sessionID string) (runtimeids.SessionID, error) {
	trimmed := strings.TrimSpace(sessionID)
	if err := validateRequiredSessionID(trimmed); err != nil {
		return runtimeids.SessionID{}, err
	}
	parsed, err := runtimeids.ParseSessionID(trimmed)
	if err != nil {
		return runtimeids.SessionID{}, ErrSessionIDNotSingle
	}
	return parsed, nil
}
