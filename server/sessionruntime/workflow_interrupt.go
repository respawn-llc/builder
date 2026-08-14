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
}

var (
	ErrWorkflowQuestionPending = errors.New("workflow task has a pending question")
	ErrWorkflowApprovalPending = errors.New("workflow task has a pending session approval")
)

// WithWorkflowManualMoveSelection selects one Task's exact workflow executions
// and closes Question admission without retaining Authority-wide ownership
// while the controller establishes its interruption fence.
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
	a.mu.Unlock()
	lockExactExecutions(executions)
	defer unlockExactExecutions(executions)
	a.mu.Lock()
	if !a.exactExecutionsLiveLocked(executions) {
		a.mu.Unlock()
		return ErrExecutionNoLongerLive
	}
	locked := make([]*execution, 0, len(executions))
	selection := WorkflowInterruptSelection{}
	for _, execution := range executions {
		switch execution.phase {
		case executionPhaseRunning:
			selection.Interruptible = append(selection.Interruptible, executionHandle{execution: execution})
		case executionPhaseQueued:
			selection.Queued = append(selection.Queued, executionHandle{execution: execution})
		case executionPhaseFinalizing:
		default:
			panic(fmt.Sprintf("workflow execution scope %s has invalid phase", execution.scope.ID()))
		}
		execution.prompts.mu.Lock()
		locked = append(locked, execution)
	}
	a.mu.Unlock()
	hasQuestion := false
	hasApproval := false
	for _, execution := range locked {
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
		for _, execution := range locked {
			execution.prompts.mu.Unlock()
		}
		if hasQuestion {
			return ErrWorkflowQuestionPending
		}
		return ErrWorkflowApprovalPending
	}
	err := operation(selection)
	if err != nil {
		for _, execution := range locked {
			execution.prompts.mu.Unlock()
		}
		return err
	}
	closures := make([]struct {
		store   *executionPromptStore
		closure executionPromptClosure
	}, 0, len(locked))
	for _, execution := range locked {
		closures = append(closures, struct {
			store   *executionPromptStore
			closure executionPromptClosure
		}{store: &execution.prompts, closure: execution.prompts.closeLocked(context.Canceled)})
	}
	for _, execution := range locked {
		execution.prompts.mu.Unlock()
	}
	var publicationErr error
	for _, item := range closures {
		publicationErr = errors.Join(publicationErr, item.store.publishClosure(item.closure))
	}
	for _, item := range closures {
		item.store.releaseClosure(item.closure)
	}
	return publicationErr
}

// WithWorkflowInterruptSelection linearizes Task Interrupt selection against
// phase changes and retirement only for the selected Task's exact executions.
// A queued scope or a scope waiting for a Question never authorizes Interrupt.
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
	executions := make([]*execution, 0)
	for _, execution := range a.byScope {
		ref, workflowScoped := execution.scope.Workflow()
		if !workflowScoped || ref.CurrentNode.TaskID != taskID {
			continue
		}
		if sessionID != nil {
			resource, agent := execution.scope.Resource()
			if !agent || resource.SessionID() != *sessionID {
				continue
			}
		}
		executions = append(executions, execution)
	}
	sort.Slice(executions, func(i, j int) bool {
		return executions[i].scope.ID().String() < executions[j].scope.ID().String()
	})
	a.mu.Unlock()
	lockExactExecutions(executions)
	defer unlockExactExecutions(executions)
	a.mu.Lock()
	if !a.exactExecutionsLiveLocked(executions) {
		a.mu.Unlock()
		return ErrExecutionNoLongerLive
	}

	promptLocked := make([]*execution, 0, len(executions))
	selection := WorkflowInterruptSelection{}
	hasQuestion := false
	hasApproval := false
	for _, execution := range executions {
		execution.prompts.mu.RLock()
		promptLocked = append(promptLocked, execution)
		for _, entry := range execution.prompts.pending {
			if entry == nil {
				panic(fmt.Sprintf("workflow execution scope %s has a nil pending prompt", execution.scope.ID()))
			}
			if !entry.snapshot.Request.Approval {
				hasQuestion = true
			} else {
				hasApproval = true
			}
		}
		handle := executionHandle{execution: execution}
		switch execution.phase {
		case executionPhaseQueued:
			if sessionID == nil {
				selection.Queued = append(selection.Queued, handle)
			}
		case executionPhaseRunning:
			if len(execution.prompts.pending) == 0 {
				selection.Interruptible = append(selection.Interruptible, handle)
			}
		default:
			panic("workflow execution has an invalid interrupt phase")
		}
	})
	if len(selection.Interruptible) == 0 {
		if hasQuestion {
			return ErrWorkflowQuestionPending
		}
		if hasApproval {
			return ErrWorkflowApprovalPending
		}
		return ErrExecutionNoLongerLive
	}
	return operation(selection)
}
