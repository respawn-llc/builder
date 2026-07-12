package metadata

import (
	"context"
	"testing"

	"core/shared/serverapi"
)

func TestInsertWorktreeOperationIsInsertOnce(t *testing.T) {
	store, cfg, _ := newMetadataTestStore(t)
	operationID := serverapi.NewWorktreeOperationID()
	record := WorktreeOperationRecord{
		OperationID: operationID,
		Payload: serverapi.WorktreeOperationPayload{
			Version:             serverapi.WorktreeOperationPayloadVersion1,
			SessionID:           "session",
			Kind:                serverapi.WorktreeOperationKindLeave,
			BranchCleanupPolicy: serverapi.WorktreeBranchCleanupPolicyRetain,
		},
		ExpectedTarget: serverapi.WorktreeOperationExpectedTarget{
			CanonicalRoot: cfg.WorkspaceRoot,
		},
		ExecutionMode:    serverapi.WorktreeOperationExecutionModeScheduledTransition,
		LifecycleState:   serverapi.WorktreeOperationLifecycleStateQueued,
		LifecycleVersion: 1,
	}
	inserted, err := store.InsertWorktreeOperation(context.Background(), record)
	if err != nil || !inserted {
		t.Fatalf("first insert = %t, %v", inserted, err)
	}
	inserted, err = store.InsertWorktreeOperation(context.Background(), record)
	if err != nil || inserted {
		t.Fatalf("duplicate insert = %t, %v", inserted, err)
	}
	stored, err := store.GetWorktreeOperation(context.Background(), operationID)
	if err != nil {
		t.Fatalf("GetWorktreeOperation: %v", err)
	}
	if stored.OperationID != operationID || !stored.Payload.Equal(record.Payload) || stored.LifecycleVersion != 1 {
		t.Fatalf("stored record = %+v", stored)
	}
	updated, err := store.CompareAndSetWorktreeOperationLifecycle(
		context.Background(),
		operationID,
		serverapi.WorktreeOperationLifecycleStateQueued,
		1,
		serverapi.WorktreeOperationLifecycleStateRunning,
		nil,
		nil,
	)
	if err != nil || !updated {
		t.Fatalf("CompareAndSetWorktreeOperationLifecycle = %t, %v", updated, err)
	}
	updated, err = store.CompareAndSetWorktreeOperationLifecycle(
		context.Background(),
		operationID,
		serverapi.WorktreeOperationLifecycleStateQueued,
		1,
		serverapi.WorktreeOperationLifecycleStateRunning,
		nil,
		nil,
	)
	if err != nil || updated {
		t.Fatalf("stale CompareAndSetWorktreeOperationLifecycle = %t, %v", updated, err)
	}
	stored, err = store.GetWorktreeOperation(context.Background(), operationID)
	if err != nil || stored.LifecycleState != serverapi.WorktreeOperationLifecycleStateRunning || stored.LifecycleVersion != 2 {
		t.Fatalf("updated stored record = %+v err=%v", stored, err)
	}
	recoverable, err := store.ListRecoverableWorktreeOperations(context.Background(), nil, 1)
	if err != nil || len(recoverable) != 1 || recoverable[0].OperationID != operationID {
		t.Fatalf("ListRecoverableWorktreeOperations = %+v err=%v", recoverable, err)
	}
}
