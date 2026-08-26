package clientui

import (
	"encoding/json"
	"testing"

	"core/shared/worktreecontract"
)

func TestTranscriptWorktreeAndOperationalOutcomesStayTyped(t *testing.T) {
	worktree := TranscriptWorktreeTransitionOutcome{
		OperationID: worktreecontract.NewOperationID(),
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
}

func TestTranscriptOperationOutcomesRejectUntypedFailure(t *testing.T) {
	worktree := TranscriptWorktreeTransitionOutcome{
		OperationID: worktreecontract.NewOperationID(),
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
		OperationID: worktreecontract.NewOperationID(),
		Transition:  WorktreeTransitionDelete,
		State:       WorktreeTransitionFailed,
		Failure: &TranscriptDiagnostic{
			Code:   TranscriptDiagnosticCode("worktree_transition_failed"),
			Detail: "delete precondition",
		},
		DeletePrecondition: &worktreecontract.DirtyState{
			Kind:           worktreecontract.DirtyStateDirty,
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
		OperationID: worktreecontract.NewOperationID(),
		Transition:  WorktreeTransitionDelete,
		State:       WorktreeTransitionFailed,
		Failure: &TranscriptDiagnostic{
			Code:   TranscriptDiagnosticCode("worktree_transition_failed"),
			Detail: "delete precondition",
		},
		DeletePrecondition: &worktreecontract.DirtyState{
			Kind:           worktreecontract.DirtyStateDirty,
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
		got.DeletePrecondition.Kind != worktreecontract.DirtyStateDirty ||
		got.DeletePrecondition.DirtyFileCount == nil ||
		*got.DeletePrecondition.DirtyFileCount != count {
		t.Fatalf("decoded transcript outcome = %+v, want typed dirty precondition", decoded.Payload())
	}
}

func TestTranscriptWorktreeTransitionOutcomeJSONPreservesExistingShape(t *testing.T) {
	fixtures := []string{
		`{"OperationID":"11111111-1111-4111-8111-111111111111","Transition":"enter","State":"completed","Failure":null,"DeletePrecondition":null}`,
		`{"OperationID":"11111111-1111-4111-8111-111111111111","Transition":"delete","State":"failed","Failure":{"Code":"worktree_transition_failed","Detail":"delete precondition"},"DeletePrecondition":{"kind":"dirty","dirty_file_count":2}}`,
		`{"OperationID":"11111111-1111-4111-8111-111111111111","Transition":"delete","State":"failed","Failure":{"Code":"worktree_transition_failed","Detail":"delete precondition"},"DeletePrecondition":{"kind":"unknown","unknown_cause":"git status failed"}}`,
	}
	for _, fixture := range fixtures {
		var outcome TranscriptWorktreeTransitionOutcome
		if err := json.Unmarshal([]byte(fixture), &outcome); err != nil {
			t.Fatalf("decode existing transcript outcome: %v", err)
		}
		if err := outcome.Validate(); err != nil {
			t.Fatalf("validate existing transcript outcome: %v", err)
		}
		data, err := json.Marshal(outcome)
		if err != nil {
			t.Fatalf("re-encode existing transcript outcome: %v", err)
		}
		if string(data) != fixture {
			t.Fatalf("re-encoded transcript outcome = %s, want %s", data, fixture)
		}
	}
}
