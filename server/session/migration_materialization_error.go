package session

import (
	"errors"
	"fmt"
)

// EventLogMaterializationStage identifies the operation that failed without
// requiring callers to classify rendered error text.
type EventLogMaterializationStage uint8

const (
	EventLogMaterializationStagePreparation EventLogMaterializationStage = iota + 1
	EventLogMaterializationStageReconciliation
)

// EventLogMaterializationError reports whether an attempt crossed the
// irreversible event-log rename point. PendingRepair is true exactly when a
// current-format log is installed but its authoritative metadata observation
// has not durably succeeded.
type EventLogMaterializationError struct {
	Stage         EventLogMaterializationStage
	Committed     bool
	PendingRepair bool
	Err           error
}

type eventLogContractError struct {
	Err error
}

func (e eventLogContractError) Error() string {
	return fmt.Sprintf("session event-log contract failure: %v", e.Err)
}

func (e eventLogContractError) Unwrap() error {
	return e.Err
}

func (e *EventLogMaterializationError) Error() string {
	if e == nil {
		return "<nil>"
	}
	return fmt.Sprintf(
		"session event-log materialization failed at stage %d (committed=%t pending_repair=%t): %v",
		e.Stage,
		e.Committed,
		e.PendingRepair,
		e.Err,
	)
}

func (e *EventLogMaterializationError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func wrapEventLogMaterializationError(
	stage EventLogMaterializationStage,
	committed bool,
	pendingRepair bool,
	err error,
) error {
	if err == nil {
		return nil
	}
	var existing *EventLogMaterializationError
	if errors.As(err, &existing) {
		return err
	}
	return &EventLogMaterializationError{
		Stage:         stage,
		Committed:     committed,
		PendingRepair: pendingRepair,
		Err:           err,
	}
}

func wrapEventLogPreparationError(committed bool, err error) error {
	return wrapEventLogMaterializationError(
		EventLogMaterializationStagePreparation,
		committed,
		committed,
		err,
	)
}
