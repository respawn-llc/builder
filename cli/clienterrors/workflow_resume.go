package clienterrors

import (
	"errors"
	"fmt"
	"strings"

	"core/shared/serverapi"
)

func WorkflowResumeDiagnosticMessages(
	diagnostics []serverapi.RunPromptWorkflowResumeDiagnostic,
) []string {
	messages := make([]string, 0, len(diagnostics))
	for _, diagnostic := range diagnostics {
		reference := diagnostic.TaskID + "/" + diagnostic.NodeID
		if diagnostic.TransitionBranchKey != nil {
			reference += "/" + *diagnostic.TransitionBranchKey
		}
		messages = append(messages, fmt.Sprintf(
			"resume current node %s: %s",
			reference,
			strings.TrimSpace(diagnostic.Cause),
		))
	}
	return messages
}

func WorkflowTaskResumeConflictMessage(err error) (string, bool) {
	var conflict *serverapi.WorkflowTaskResumeConflictError
	if !errors.As(err, &conflict) || conflict == nil {
		return "", false
	}
	const unsupported = "Direct interactive continuation of this retained Workflow Session is not currently supported."
	switch conflict.State {
	case serverapi.WorkflowTaskResumeConflictPendingApproval:
		return fmt.Sprintf(
			"Workflow Task %q is waiting for an Approval; resolve that Approval before continuing the Task. %s",
			conflict.TaskID,
			unsupported,
		), true
	case serverapi.WorkflowTaskResumeConflictFinished:
		return fmt.Sprintf(
			"Workflow Task %q has finished; start a new ordinary Session. %s",
			conflict.TaskID,
			unsupported,
		), true
	case serverapi.WorkflowTaskResumeConflictMovedCurrentNode:
		return fmt.Sprintf(
			"Workflow Task %q has moved to a different Current Node; continue through the Task's current Node. %s",
			conflict.TaskID,
			unsupported,
		), true
	case serverapi.WorkflowTaskResumeConflictCurrentNodeNotInterrupted:
		return fmt.Sprintf(
			"Workflow Task %q's Current Node is no longer interrupted; use the Task's current Node controls or wait for its lifecycle state to change. %s",
			conflict.TaskID,
			unsupported,
		), true
	case serverapi.WorkflowTaskResumeConflictNoResumableCurrentNode:
		return fmt.Sprintf(
			"Workflow Task %q has no interrupted executable Current Node; use the Task's current Node controls or start a new ordinary Session. %s",
			conflict.TaskID,
			unsupported,
		), true
	default:
		return fmt.Sprintf("Workflow Task %q cannot be resumed. %s", conflict.TaskID, unsupported), true
	}
}
