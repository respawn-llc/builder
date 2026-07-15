package runtimefeed

import (
	"testing"

	"core/shared/clientui"
)

func TestTranscriptWorktreeAndOperationalOutcomesStayTyped(t *testing.T) {
	worktree := TranscriptWorktreeTransitionOutcome{
		OperationID: clientui.NewWorktreeTransitionID(),
		Transition:  clientui.WorktreeTransitionEnter,
		State:       clientui.WorktreeTransitionCompleted,
	}
	if err := worktree.Validate(); err != nil {
		t.Fatalf("validate worktree transition outcome: %v", err)
	}

	diagnostic := TranscriptOperationalDiagnostic{
		Code:   OperationalDiagnosticSleepGuardFailed,
		Detail: "operating system rejected sleep prevention",
	}
	if err := diagnostic.Validate(); err != nil {
		t.Fatalf("validate operational diagnostic: %v", err)
	}
}

func TestTranscriptOperationOutcomesRejectUntypedFailure(t *testing.T) {
	worktree := TranscriptWorktreeTransitionOutcome{
		OperationID: clientui.NewWorktreeTransitionID(),
		Transition:  clientui.WorktreeTransitionDelete,
		State:       clientui.WorktreeTransitionFailed,
	}
	if err := worktree.Validate(); err == nil {
		t.Fatal("accepted failed worktree transition without diagnostic")
	}

	diagnostic := TranscriptOperationalDiagnostic{
		Code:   OperationalDiagnosticCode("unknown"),
		Detail: "detail",
	}
	if err := diagnostic.Validate(); err == nil {
		t.Fatal("accepted unknown operational diagnostic code")
	}
}
