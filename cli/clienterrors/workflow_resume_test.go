package clienterrors

import (
	"errors"
	"strings"
	"testing"

	"core/shared/serverapi"
)

func TestWorkflowTaskResumeConflictMessageRendersClientGuidance(t *testing.T) {
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
			if !strings.Contains(message, err.TaskID) || !strings.Contains(message, "retained Workflow Session") {
				t.Fatalf("client guidance = %q, want Task ID and lifecycle guidance", message)
			}
			if got := err.Error(); got != "workflow task resume conflict" {
				t.Fatalf("server error message = %q, want transport-neutral message", got)
			}
		})
	}
}
