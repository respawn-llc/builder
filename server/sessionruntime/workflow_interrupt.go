package sessionruntime

import (
	"errors"
	"strings"

	"core/server/workflow"
	"core/shared/runtimeids"
)

// WorkflowInterruptSelection separates scopes that authorize Interrupt from
// non-authorizing scopes that Workflow Execution must drain once a Task-wide
// Interrupt has been authorized.
type WorkflowInterruptSelection struct {
	Interruptible []ExecutionHandle
	Queued        []ExecutionHandle
	Finalizing    []ExecutionHandle
}

// WithWorkflowInterruptSelection linearizes Task Interrupt selection against
// exact-scope phase changes and retirement. A queued scope or a scope waiting
// for a Question never authorizes Interrupt.
func (a *Authority) WithWorkflowInterruptSelection(
	taskID workflow.TaskID,
	sessionID *runtimeids.SessionID,
	operation func(WorkflowInterruptSelection) error,
) error {
	if a == nil {
		return errors.New("session runtime authority is required")
	}
	if strings.TrimSpace(string(taskID)) == "" {
		return errors.New("workflow task id is required")
	}
	if sessionID != nil && sessionID.IsZero() {
		return errors.New("session id is required")
	}
	if operation == nil {
		return errors.New("workflow interrupt selection operation is required")
	}
	a.mu.Lock()
	defer a.mu.Unlock()

	selection := WorkflowInterruptSelection{}
	a.forEachWorkflowExecutionLocked(func(execution *execution) {
		ref, workflowScoped := execution.scope.Workflow()
		if !workflowScoped || ref.CurrentNode.TaskID != taskID {
			return
		}
		if sessionID != nil {
			resource, agent := execution.scope.Resource()
			if !agent || resource.SessionID() != *sessionID {
				return
			}
		}
		handle := executionHandle{execution: execution}
		switch execution.phase {
		case executionPhaseQueued:
			if sessionID == nil {
				selection.Queued = append(selection.Queued, handle)
			}
		case executionPhaseRunning:
			if !execution.prompts.hasPending() {
				selection.Interruptible = append(selection.Interruptible, handle)
			}
		default:
			panic("workflow execution has an invalid interrupt phase")
		}
	})
	if sessionID == nil {
		for _, execution := range a.byScope {
			if execution.phase != executionPhaseFinalizing {
				continue
			}
			ref, workflowScoped := execution.scope.Workflow()
			if !workflowScoped || ref.CurrentNode.TaskID != taskID {
				continue
			}
			if execution.scope.Kind() != ExecutionScopeScript {
				panic("workflow execution finalizing phase is not a script")
			}
			selection.Finalizing = append(selection.Finalizing, executionHandle{execution: execution})
		}
	}
	if len(selection.Interruptible) == 0 {
		return ErrExecutionNoLongerLive
	}
	return operation(selection)
}
