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

func TestWorktreeOperationLedgerRecoveryClaimsQueuedAndFailsRunning(t *testing.T) {
	env := newServiceTestEnv(t)
	ledger := worktreeOperationLedger{store: env.store}
	queuedID := serverapi.NewWorktreeOperationID()
	runningID := serverapi.NewWorktreeOperationID()
	for _, record := range []metadata.WorktreeOperationRecord{
		worktreeOperationLedgerRecord(env.cfg.WorkspaceRoot, queuedID, serverapi.WorktreeOperationLifecycleStateQueued),
		worktreeOperationLedgerRecord(env.cfg.WorkspaceRoot, runningID, serverapi.WorktreeOperationLifecycleStateRunning),
	} {
		if _, err := env.store.InsertWorktreeOperation(env.ctx, record); err != nil {
			t.Fatalf("InsertWorktreeOperation: %v", err)
		}
	}
	resumed := make([]serverapi.WorktreeOperationID, 0, 1)
	if err := ledger.recover(env.ctx, 10, func(record metadata.WorktreeOperationRecord) error {
		resumed = append(resumed, record.OperationID)
		return nil
	}); err != nil {
		t.Fatalf("recover: %v", err)
	}
	if len(resumed) != 1 || resumed[0] != queuedID {
		t.Fatalf("resumed = %+v", resumed)
	}
	queued, err := env.store.GetWorktreeOperation(env.ctx, queuedID)
	if err != nil || queued.LifecycleState != serverapi.WorktreeOperationLifecycleStateRunning || queued.LifecycleVersion != 2 {
		t.Fatalf("queued recovery = %+v err=%v", queued, err)
	}
	running, err := env.store.GetWorktreeOperation(env.ctx, runningID)
	if err != nil || running.LifecycleState != serverapi.WorktreeOperationLifecycleStateFailed || running.TerminalError == nil {
		t.Fatalf("running recovery = %+v err=%v", running, err)
	}
}

func worktreeOperationLedgerRecord(
	root string,
	operationID serverapi.WorktreeOperationID,
	state serverapi.WorktreeOperationLifecycleState,
) metadata.WorktreeOperationRecord {
	return metadata.WorktreeOperationRecord{
		OperationID: operationID,
		Payload: serverapi.WorktreeOperationPayload{
			Version:             serverapi.WorktreeOperationPayloadVersion1,
			SessionID:           "session",
			Kind:                serverapi.WorktreeOperationKindLeave,
			BranchCleanupPolicy: serverapi.WorktreeBranchCleanupPolicyRetain,
		},
		ExpectedTarget:   serverapi.WorktreeOperationExpectedTarget{CanonicalRoot: root},
		ExecutionMode:    serverapi.WorktreeOperationExecutionModeScheduledTransition,
		LifecycleState:   state,
		LifecycleVersion: 1,
	}
}
