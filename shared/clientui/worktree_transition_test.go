package clientui

import (
	"testing"

	"core/shared/worktreecontract"
)

func TestWorktreeTransitionOutcomeIsProcessLocalTerminalState(t *testing.T) {
	operationID := worktreecontract.NewOperationID()
	if err := operationID.Validate(); err != nil {
		t.Fatalf("generated transition id rejected: %v", err)
	}
	for _, raw := range []string{"", "not-a-uuid", "00000000-0000-0000-0000-000000000000", "11111111-1111-1111-1111-111111111111"} {
		if _, err := worktreecontract.ParseOperationID(raw); err == nil {
			t.Fatalf("ParseOperationID(%q) succeeded", raw)
		}
	}

	completed := WorktreeTransitionOutcome{
		OperationID: operationID,
		Transition:  WorktreeTransitionEnter,
		State:       WorktreeTransitionCompleted,
	}
	if err := completed.Validate(); err != nil {
		t.Fatalf("completed outcome rejected: %v", err)
	}
	failed := WorktreeTransitionOutcome{
		OperationID: operationID,
		Transition:  WorktreeTransitionDelete,
		State:       WorktreeTransitionFailed,
		Failure:     &WorktreeTransitionFailure{Diagnostic: "worktree changed before deletion"},
	}
	if err := failed.Validate(); err != nil {
		t.Fatalf("failed outcome rejected: %v", err)
	}
	if err := (WorktreeTransitionOutcome{
		OperationID: operationID,
		Transition:  WorktreeTransitionLeave,
		State:       WorktreeTransitionFailed,
	}).Validate(); err == nil {
		t.Fatal("failed outcome without failure facts validated")
	}
	if err := (WorktreeTransitionOutcome{
		OperationID: operationID,
		Transition:  WorktreeTransitionLeave,
		State:       WorktreeTransitionCompleted,
		Failure:     failed.Failure,
	}).Validate(); err == nil {
		t.Fatal("completed outcome with failure facts validated")
	}

	dirtyCount := 2
	failedDelete := WorktreeTransitionOutcome{
		OperationID: operationID,
		Transition:  WorktreeTransitionDelete,
		State:       WorktreeTransitionFailed,
		Failure: &WorktreeTransitionFailure{
			Diagnostic: "delete precondition",
			DeletePrecondition: &worktreecontract.DirtyState{
				Kind:           worktreecontract.DirtyStateDirty,
				DirtyFileCount: &dirtyCount,
			},
		},
	}
	if err := failedDelete.Validate(); err != nil {
		t.Fatalf("failed delete with typed precondition rejected: %v", err)
	}
	invalidNonDelete := failedDelete
	invalidNonDelete.Transition = WorktreeTransitionLeave
	if err := invalidNonDelete.Validate(); err == nil {
		t.Fatal("non-delete transition accepted delete precondition")
	}
	invalidClean := failedDelete
	invalidClean.Failure = &WorktreeTransitionFailure{
		Diagnostic:         failedDelete.Failure.Diagnostic,
		DeletePrecondition: &worktreecontract.DirtyState{Kind: worktreecontract.DirtyStateClean},
	}
	if err := invalidClean.Validate(); err == nil {
		t.Fatal("clean delete precondition validated")
	}
}
