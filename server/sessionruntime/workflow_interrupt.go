package sessionruntime

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"core/server/workflow"
	"core/shared/runtimeids"
)

// WorkflowInterruptSelection separates scopes that authorize Interrupt from
// non-authorizing scopes that Workflow Execution must drain once a Task-wide
// Interrupt has been authorized.
type WorkflowInterruptSelection struct {
	Interruptible []WorkflowExecutionSelection
	Queued        []WorkflowExecutionSelection
}

type WorkflowExecutionSelection struct {
	Handle      ExecutionHandle
	CurrentNode workflow.CurrentNodeReference
}

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
	executions := a.workflowTaskExecutionsLocked(taskID)
	a.mu.Unlock()
	lockExactExecutions(executions)
	lockedExecutions := executions
	defer unlockExactExecutions(lockedExecutions)
	a.mu.Lock()
	executions = a.liveExactExecutionsLocked(executions)
	locked := make([]*execution, 0, len(executions))
	selection := WorkflowInterruptSelection{}
	for _, execution := range executions {
		activity, activityErr := execution.workflowActivity()
		if activityErr != nil {
			a.mu.Unlock()
			panic(activityErr)
		}
		switch activity {
		case workflowExecutionRunning:
			ref, _ := execution.scope.Workflow()
			selection.Interruptible = append(selection.Interruptible, WorkflowExecutionSelection{
				Handle: executionHandle{execution: execution}, CurrentNode: ref.CurrentNode,
			})
		case workflowExecutionQueued:
			ref, _ := execution.scope.Workflow()
			selection.Queued = append(selection.Queued, WorkflowExecutionSelection{
				Handle: executionHandle{execution: execution}, CurrentNode: ref.CurrentNode,
			})
		case workflowExecutionNotRunning:
		}
		execution.prompts.mu.Lock()
		locked = append(locked, execution)
	}
	a.mu.Unlock()
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
	for _, item := range closures {
		publicationErr = errors.Join(publicationErr, item.store.closeApprovals(item.closure, true))
	}
	return publicationErr
}

// WithWorkflowInterruptSelection linearizes Task Interrupt selection against
// phase changes and retirement only for the selected Task's exact executions.
// A queued scope never authorizes Interrupt. Pending prompts remain part of
// their running exact execution and are closed by ordinary interruption.
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
	executions := a.workflowTaskExecutionsLocked(taskID)
	if sessionID != nil {
		filtered := executions[:0]
		for _, execution := range executions {
			resource, agent := execution.scope.Resource()
			if agent && resource.SessionID() == *sessionID {
				filtered = append(filtered, execution)
			}
		}
		executions = filtered
	}
	a.mu.Unlock()
	lockExactExecutions(executions)
	lockedExecutions := executions
	defer unlockExactExecutions(lockedExecutions)
	a.mu.Lock()
	executions = a.liveExactExecutionsLocked(executions)
	if len(executions) == 0 {
		a.mu.Unlock()
		return ErrExecutionNoLongerLive
	}

	promptLocked := make([]*execution, 0, len(executions))
	selection := WorkflowInterruptSelection{}
	for _, execution := range executions {
		execution.prompts.mu.Lock()
		promptLocked = append(promptLocked, execution)
		for _, entry := range execution.prompts.pending {
			if entry == nil {
				panic(fmt.Sprintf("workflow execution scope %s has a nil pending prompt", execution.scope.ID()))
			}
		}
		ref, _ := execution.scope.Workflow()
		selected := WorkflowExecutionSelection{
			Handle:      executionHandle{execution: execution},
			CurrentNode: ref.CurrentNode,
		}
		activity, activityErr := execution.workflowActivity()
		if activityErr != nil {
			a.mu.Unlock()
			panic(activityErr)
		}
		switch activity {
		case workflowExecutionQueued:
			if sessionID == nil {
				selection.Queued = append(selection.Queued, selected)
			}
		case workflowExecutionRunning:
			selection.Interruptible = append(selection.Interruptible, selected)
		case workflowExecutionNotRunning:
		}
	}
	a.mu.Unlock()
	if len(selection.Interruptible) == 0 {
		for index := len(promptLocked) - 1; index >= 0; index-- {
			promptLocked[index].prompts.mu.Unlock()
		}
		return ErrExecutionNoLongerLive
	}
	if err := operation(selection); err != nil {
		for index := len(promptLocked) - 1; index >= 0; index-- {
			promptLocked[index].prompts.mu.Unlock()
		}
		return err
	}
	closures := make([]struct {
		store   *executionPromptStore
		closure executionPromptClosure
	}, 0, len(promptLocked))
	for _, execution := range promptLocked {
		closures = append(closures, struct {
			store   *executionPromptStore
			closure executionPromptClosure
		}{store: &execution.prompts, closure: execution.prompts.closeLocked(context.Canceled)})
	}
	for index := len(promptLocked) - 1; index >= 0; index-- {
		promptLocked[index].prompts.mu.Unlock()
	}
	var publicationErr error
	for _, item := range closures {
		publicationErr = errors.Join(publicationErr, item.store.publishClosure(item.closure))
	}
	for _, item := range closures {
		item.store.releaseClosure(item.closure)
	}
	for _, item := range closures {
		publicationErr = errors.Join(publicationErr, item.store.closeApprovals(item.closure, true))
	}
	return publicationErr
}

func (a *Authority) liveExactExecutionsLocked(executions []*execution) []*execution {
	return slices.DeleteFunc(slices.Clone(executions), func(execution *execution) bool { return a.byScope[execution.scope.ID()] != execution })
}
