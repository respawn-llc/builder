package session

import (
	"context"
	"errors"
	"fmt"
	"time"
)

var ErrEventLogReconciliationConflict = errors.New("session event-log reconciliation conflicted with newer metadata")
var ErrStoreRecoveryRequired = errors.New("session store recovery is required")

type IrreconcilableRecoveryConflict string

const (
	IrreconcilableRecoveryConflictMetadataState   IrreconcilableRecoveryConflict = "metadata_state"
	IrreconcilableRecoveryConflictCommittedSuffix IrreconcilableRecoveryConflict = "committed_suffix"
)

type IrreconcilableRecoverySuffixIdentity struct {
	StartOffset   int64
	EndOffset     int64
	EventCount    int
	FirstSequence int64
	LastSequence  int64
	SHA256        string
}

type IrreconcilableRecoveryDetail struct {
	SessionID             string
	Operation             string
	RecoveryPath          string
	EventsPath            string
	CurrentMetadataSHA256 string
	PreMetadataSHA256     string
	PostMetadataSHA256    string
	Phase                 string
	Conflict              IrreconcilableRecoveryConflict
	Suffix                *IrreconcilableRecoverySuffixIdentity
	cause                 error
}

func (e *IrreconcilableRecoveryDetail) Error() string {
	return fmt.Sprintf(
		"session %q has irreconcilable %s recovery state while attempting to %s",
		e.SessionID,
		e.Conflict,
		e.Operation,
	)
}

func (e *IrreconcilableRecoveryDetail) Unwrap() []error {
	if e.cause == nil {
		return []error{ErrStoreRecoveryRequired}
	}
	return []error{ErrStoreRecoveryRequired, e.cause}
}

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
