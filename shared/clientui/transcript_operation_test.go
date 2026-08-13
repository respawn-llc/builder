package clientui

import (
	"encoding/json"
	"testing"
)

func TestTranscriptWorktreeAndOperationalOutcomesStayTyped(t *testing.T) {
	worktree := TranscriptWorktreeTransitionOutcome{
		OperationID: NewWorktreeTransitionID(),
		Transition:  WorktreeTransitionEnter,
		State:       WorktreeTransitionCompleted,
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

	for _, code := range []OperationalDiagnosticCode{
		OperationalDiagnosticProviderTurnStateInvalid,
		OperationalDiagnosticProviderTurnStateConflict,
	} {
		providerState := TranscriptProviderStateDiagnostic{Code: code}
		if err := providerState.Validate(); err != nil {
			t.Fatalf("validate provider-state diagnostic %q: %v", code, err)
		}
		data, err := json.Marshal(providerState)
		if err != nil {
			t.Fatalf("marshal provider-state diagnostic %q: %v", code, err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(data, &fields); err != nil {
			t.Fatalf("decode provider-state diagnostic %q fields: %v", code, err)
		}
		if _, exists := fields["Detail"]; exists {
			t.Fatalf("provider-state diagnostic %q structurally owns Detail: %s", code, data)
		}
	}
}

func TestTranscriptOperationOutcomesRejectUntypedFailure(t *testing.T) {
	worktree := TranscriptWorktreeTransitionOutcome{
		OperationID: NewWorktreeTransitionID(),
		Transition:  WorktreeTransitionDelete,
		State:       WorktreeTransitionFailed,
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

	dirtyCount := 1
	typed := TranscriptWorktreeTransitionOutcome{
		OperationID: NewWorktreeTransitionID(),
		Transition:  WorktreeTransitionDelete,
		State:       WorktreeTransitionFailed,
		Failure: &TranscriptDiagnostic{
			Code:   TranscriptDiagnosticCode("worktree_transition_failed"),
			Detail: "delete precondition",
		},
		DeletePrecondition: &WorktreeDirtyState{
			Kind:           WorktreeDirtyStateDirty,
			DirtyFileCount: &dirtyCount,
		},
	}
	if err := typed.Validate(); err != nil {
		t.Fatalf("typed delete precondition rejected: %v", err)
	}
	invalidNonDelete := typed
	invalidNonDelete.Transition = WorktreeTransitionLeave
	if err := invalidNonDelete.Validate(); err == nil {
		t.Fatal("non-delete transcript transition accepted delete precondition")
	}
}

func TestTranscriptWorktreeTransitionOutcomeJSONKeepsDeletePrecondition(t *testing.T) {
	count := 2
	outcome := TranscriptWorktreeTransitionOutcome{
		OperationID: NewWorktreeTransitionID(),
		Transition:  WorktreeTransitionDelete,
		State:       WorktreeTransitionFailed,
		Failure: &TranscriptDiagnostic{
			Code:   TranscriptDiagnosticCode("worktree_transition_failed"),
			Detail: "delete precondition",
		},
		DeletePrecondition: &WorktreeDirtyState{
			Kind:           WorktreeDirtyStateDirty,
			DirtyFileCount: &count,
		},
	}
	data, err := json.Marshal(NewTranscriptMessage(1, NewTranscriptEvent(outcome)))
	if err != nil {
		t.Fatalf("marshal transcript outcome: %v", err)
	}
	var decoded TranscriptMessage
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal transcript outcome: %v", err)
	}
	got, ok := decoded.Payload().(TranscriptWorktreeTransitionOutcome)
	if !ok || got.DeletePrecondition == nil ||
		got.DeletePrecondition.Kind != WorktreeDirtyStateDirty ||
		got.DeletePrecondition.DirtyFileCount == nil ||
		*got.DeletePrecondition.DirtyFileCount != count {
		t.Fatalf("decoded transcript outcome = %+v, want typed dirty precondition", decoded.Payload())
	}
}
