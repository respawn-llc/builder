package clienterrors

import (
	"errors"
	"testing"

	"core/shared/serverapi"
)

func TestWorkflowTaskResumeConflictMessageRendersClientGuidance(t *testing.T) {
	messages := make(map[serverapi.WorkflowTaskResumeConflictState]string)
	for _, state := range []serverapi.WorkflowTaskResumeConflictState{
		serverapi.WorkflowTaskResumeConflictPendingApproval,
		serverapi.WorkflowTaskResumeConflictFinished,
		serverapi.WorkflowTaskResumeConflictMovedCurrentNode,
		serverapi.WorkflowTaskResumeConflictCurrentNodeNotInterrupted,
		serverapi.WorkflowTaskResumeConflictNoResumableCurrentNode,
	} {
		t.Run(string(state), func(t *testing.T) {
			err := &serverapi.WorkflowTaskResumeConflictError{TaskID: "KNT-123", State: state}
			message, ok := WorkflowTaskResumeConflictMessage(errors.Join(serverapi.ErrRuntimeCommandNotAccepted, err))
			if !ok {
				t.Fatal("WorkflowTaskResumeConflictMessage did not recognize the conflict")
			}
			if message == "" || message == err.Error() {
				t.Fatalf("client guidance = %q, want client-owned guidance distinct from transport error", message)
			}
			if previous, exists := messages[state]; exists && previous != message {
				t.Fatalf("state %q produced inconsistent client guidance", state)
			}
			messages[state] = message
		})
	}
	seen := make(map[string]serverapi.WorkflowTaskResumeConflictState, len(messages))
	for state, message := range messages {
		if previous, exists := seen[message]; exists {
			t.Fatalf("states %q and %q produced the same client guidance", previous, state)
		}
		seen[message] = state
	}
}
