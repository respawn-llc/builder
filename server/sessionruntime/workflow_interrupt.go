package sessionruntime

import (
	"context"
	"errors"
	"fmt"
	"sort"
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

var (
	ErrWorkflowQuestionPending = errors.New("workflow task has a pending question")
	ErrWorkflowApprovalPending = errors.New("workflow task has a pending session approval")
)

// WithWorkflowManualMoveSelection atomically selects every exact workflow
// execution for a Task and closes Question admission before releasing
// Authority ownership. The callback must establish the controller's
// interruption fence; no prompt store is closed when it returns an error.
func (a *Authority) WithWorkflowManualMoveSelection(
	taskID workflow.TaskID,
	operation func(WorkflowInterruptSelection) error,
) error {
	if a == nil {
		return errors.New("session runtime authority is required")
	}
	if strings.TrimSpace(string(taskID)) == "" {
		return errors.New("workflow task id is required")
	}
	if operation == nil {
		return errors.New("workflow manual move selection operation is required")
	}
	a.mu.Lock()
	unlocked := false
	defer func() {
		if !unlocked {
			a.mu.Unlock()
		}
	}()
	executions := make([]*execution, 0)
	for _, execution := range a.byScope {
		ref, workflowScoped := execution.scope.Workflow()
		if workflowScoped && ref.CurrentNode.TaskID == taskID {
			executions = append(executions, execution)
		}
	}
	sort.Slice(executions, func(i, j int) bool {
		return executions[i].scope.ID().String() < executions[j].scope.ID().String()
	})
	running := make([]*execution, 0, len(executions))
	selection := WorkflowInterruptSelection{}
	for _, execution := range executions {
		switch execution.phase {
		case executionPhaseRunning:
			execution.prompts.mu.Lock()
			running = append(running, execution)
			selection.Interruptible = append(selection.Interruptible, executionHandle{execution: execution})
		case executionPhaseQueued:
			selection.Queued = append(selection.Queued, executionHandle{execution: execution})
		case executionPhaseFinalizing:
			selection.Finalizing = append(selection.Finalizing, executionHandle{execution: execution})
		default:
			panic(fmt.Sprintf("workflow execution scope %s has invalid phase", execution.scope.ID()))
		}
	}
	hasQuestion := false
	hasApproval := false
	for _, execution := range running {
		for _, entry := range execution.prompts.pending {
			if entry == nil {
				panic(fmt.Sprintf("workflow execution scope %s has a nil pending prompt", execution.scope.ID()))
			}
			if entry.snapshot.Request.Approval {
				hasApproval = true
			} else {
				hasQuestion = true
			}
		}
	}
	if hasQuestion || hasApproval {
		for _, locked := range running {
			locked.prompts.mu.Unlock()
		}
		if hasQuestion {
			return ErrWorkflowQuestionPending
		}
		return ErrWorkflowApprovalPending
	}
	err := operation(selection)
	if err != nil {
		for _, locked := range running {
			locked.prompts.mu.Unlock()
		}
		return err
	}
	resolved := make([]struct {
		store     *executionPromptStore
		snapshots []ExecutionPromptSnapshot
	}, 0, len(running))
	for _, execution := range running {
		resolved = append(resolved, struct {
			store     *executionPromptStore
			snapshots []ExecutionPromptSnapshot
		}{store: &execution.prompts, snapshots: execution.prompts.closeLocked(context.Canceled)})
	}
	for _, locked := range running {
		locked.prompts.mu.Unlock()
	}
	a.mu.Unlock()
	unlocked = true
	for _, item := range resolved {
		for _, snapshot := range item.snapshots {
			item.store.publishResolved(snapshot)
		}
	}
	return nil
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
