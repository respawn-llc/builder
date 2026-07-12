package worktree

import (
	"errors"
	"testing"

	"core/server/metadata"
	"core/shared/serverapi"
)

func TestWorktreeOperationLedgerReplaysMatchingPayloadAndRejectsConflict(t *testing.T) {
	env := newServiceTestEnv(t)
	ledger := worktreeOperationLedger{store: env.store}
	operationID := serverapi.NewWorktreeOperationID()
	record := metadata.WorktreeOperationRecord{
		OperationID: operationID,
		Payload: serverapi.WorktreeOperationPayload{
			Version:             serverapi.WorktreeOperationPayloadVersion1,
			SessionID:           "session",
			Kind:                serverapi.WorktreeOperationKindLeave,
			BranchCleanupPolicy: serverapi.WorktreeBranchCleanupPolicyRetain,
		},
		ExpectedTarget:   serverapi.WorktreeOperationExpectedTarget{CanonicalRoot: env.cfg.WorkspaceRoot},
		ExecutionMode:    serverapi.WorktreeOperationExecutionModeScheduledTransition,
		LifecycleState:   serverapi.WorktreeOperationLifecycleStateQueued,
		LifecycleVersion: 1,
	}
	_, inserted, err := ledger.accept(env.ctx, record)
	if err != nil || !inserted {
		t.Fatalf("first accept = inserted %t err=%v", inserted, err)
	}
	replayed, inserted, err := ledger.accept(env.ctx, record)
	if err != nil || inserted || replayed.OperationID != operationID {
		t.Fatalf("matching replay = %+v inserted=%t err=%v", replayed, inserted, err)
	}
	conflicting := record
	conflicting.Payload.Kind = serverapi.WorktreeOperationKindDelete
	selector := "feature"
	conflicting.Payload.Selector = &selector
	_, _, err = ledger.accept(env.ctx, conflicting)
	var conflict *serverapi.WorktreeOperationIDConflictError
	if !errors.As(err, &conflict) || conflict.OperationID != operationID {
		t.Fatalf("conflicting replay error = %v", err)
	}
}
