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
}
