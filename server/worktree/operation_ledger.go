package worktree

import (
	"context"
	"errors"

	"core/server/metadata"
	"core/shared/serverapi"
)

type worktreeOperationLedger struct {
	store *metadata.Store
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
