package session

import (
	"context"
	"errors"
	"fmt"
	"time"
)

var ErrEventLogReconciliationConflict = errors.New("session event-log reconciliation conflicted with newer metadata")
var ErrStoreRecoveryRequired = errors.New("session store recovery is required")

func storeRecoveryError(sessionID, operation string, err error) error {
	return fmt.Errorf("%w: session %q failed to %s: %w", ErrStoreRecoveryRequired, sessionID, operation, err)
}

type EventLogReconciliationConflictError struct {
	SessionID            string
	ObservedLastSequence int64
	CurrentLastSequence  int64
}

func (e EventLogReconciliationConflictError) Error() string {
	return fmt.Sprintf(
		"session %q event-log reconciliation observed sequence %d but metadata advanced to %d",
		e.SessionID,
		e.ObservedLastSequence,
		e.CurrentLastSequence,
	)
}

func (e EventLogReconciliationConflictError) Unwrap() error {
	return ErrEventLogReconciliationConflict
}

type PersistedStoreSnapshot struct {
	SessionDir string
	Meta       Meta
}

type PersistenceObserver interface {
	ObservePersistedStore(ctx context.Context, snapshot PersistedStoreSnapshot) error
}

type PersistedEventLogReconciliation struct {
	SessionID               string
	ObservedLastSequence    int64
	LastSequence            int64
	ConversationEstablished bool
	UpdatedAt               time.Time
	UsageState              UsageStateReconciliation
}

type EventLogReconciliationObserver interface {
	ObserveEventLogReconciliation(ctx context.Context, reconciliation PersistedEventLogReconciliation) error
}

type UsageStateReconciliation uint8

const (
	UsageStateReconciliationPreserve UsageStateReconciliation = iota
	UsageStateReconciliationInvalidate
)

func (r UsageStateReconciliation) InvalidatesUsageState() (bool, error) {
	switch r {
	case UsageStateReconciliationPreserve:
		return false, nil
	case UsageStateReconciliationInvalidate:
		return true, nil
	default:
		return false, fmt.Errorf("unknown usage-state reconciliation %d", r)
	}
}
