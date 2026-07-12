package worktree

import (
	"context"
	"encoding/json"
	"errors"

	"core/server/metadata"
	"core/shared/serverapi"
)

type worktreeOperationLedger struct {
	store *metadata.Store
}

func (l worktreeOperationLedger) recover(ctx context.Context, limit int, resume func(metadata.WorktreeOperationRecord) error) error {
	if l.store == nil {
		return errors.New("worktree operation metadata store is required")
	}
	records, err := l.store.ListRecoverableWorktreeOperations(ctx, nil, limit)
	if err != nil {
		return err
	}
	for _, record := range records {
		switch record.LifecycleState {
		case serverapi.WorktreeOperationLifecycleStateQueued:
			claimed, err := l.store.CompareAndSetWorktreeOperationLifecycle(
				ctx,
				record.OperationID,
				record.LifecycleState,
				record.LifecycleVersion,
				serverapi.WorktreeOperationLifecycleStateRunning,
				nil,
				nil,
			)
			if err != nil || !claimed {
				if err != nil {
					return err
				}
				continue
			}
			if resume != nil {
				if err := resume(record); err != nil {
					return err
				}
			}
		case serverapi.WorktreeOperationLifecycleStateRunning:
			failure, err := json.Marshal(serverapi.WorktreeOperationFailure{
				Kind:       serverapi.WorktreeOperationFailureKindExecutionIndeterminate,
				Diagnostic: "worktree operation was running when the server stopped",
			})
			if err != nil {
				return err
			}
			terminalError := json.RawMessage(failure)
			if _, err := l.store.CompareAndSetWorktreeOperationLifecycle(
				ctx,
				record.OperationID,
				record.LifecycleState,
				record.LifecycleVersion,
				serverapi.WorktreeOperationLifecycleStateFailed,
				nil,
				&terminalError,
			); err != nil {
				return err
			}
		}
	}
	return nil
}

func (l worktreeOperationLedger) accept(ctx context.Context, record metadata.WorktreeOperationRecord) (metadata.WorktreeOperationRecord, bool, error) {
	if l.store == nil {
		return metadata.WorktreeOperationRecord{}, false, errors.New("worktree operation metadata store is required")
	}
	inserted, err := l.store.InsertWorktreeOperation(ctx, record)
	if err != nil {
		return metadata.WorktreeOperationRecord{}, false, err
	}
	if inserted {
		return record, true, nil
	}
	stored, err := l.store.GetWorktreeOperation(ctx, record.OperationID)
	if err != nil {
		return metadata.WorktreeOperationRecord{}, false, err
	}
	if !stored.Payload.Equal(record.Payload) {
		return metadata.WorktreeOperationRecord{}, false, &serverapi.WorktreeOperationIDConflictError{
			OperationID: record.OperationID,
			Existing:    stored.Payload,
			Incoming:    record.Payload,
		}
	}
	return stored, false, nil
}
