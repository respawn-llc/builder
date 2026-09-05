package metadata

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"core/server/session"
)

type SessionInUseError struct {
	SessionID string
}

func (e *SessionInUseError) Error() string {
	return fmt.Sprintf("Session %q is in use", e.SessionID)
}

func (s *Store) DeleteSession(ctx context.Context, sessionID string) error {
	if s == nil || s.db == nil || s.queries == nil {
		return errors.New("metadata store is required")
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return errors.New("Session ID is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin Session deletion: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	queries := s.queries.WithTx(tx)

	state, err := queries.GetSessionDeletionState(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("read Session deletion state: %w", err)
	}
	if state.SessionExists == 0 {
		return session.ErrSessionNotFound
	}
	if state.SessionInUse != 0 {
		return &SessionInUseError{SessionID: sessionID}
	}
	deleted, err := queries.DeleteSessionRecordByID(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("delete Session record: %w", err)
	}
	if deleted != 1 {
		return fmt.Errorf(
			"Session deletion invariant violated: deleted %d records for %q",
			deleted,
			sessionID,
		)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit Session deletion: %w", err)
	}
	return nil
}
