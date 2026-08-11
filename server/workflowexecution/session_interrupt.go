package workflowexecution

import (
	"context"
	"errors"

	"core/server/runtimeops"
	"core/server/sessionruntime"
)

func (c *CurrentNodeController) InterruptWorkflowSession(
	ctx context.Context,
	request sessionruntime.WorkflowSessionInterruptRequest,
	onCommitted func(sessionruntime.WorkflowCommittedInterruptCleanup) error,
) (sessionruntime.WorkflowSessionInterruptOutcome, error) {
	if c == nil {
		return sessionruntime.WorkflowSessionInterruptUnhandled, errors.New("current node workflow controller is required")
	}
	taskID, err := c.store.TaskIDForSession(ctx, request.SessionID)
	if err != nil {
		return sessionruntime.WorkflowSessionInterruptUnhandled, err
	}
	if taskID == nil {
		return sessionruntime.WorkflowSessionInterruptUnhandled, nil
	}
	binding, found, err := c.store.ResolveDirectSessionCurrentNodeBinding(ctx, request.SessionID)
	if err != nil {
		return sessionruntime.WorkflowSessionInterruptUnhandled, err
	}
	if !found {
		return sessionruntime.WorkflowSessionInterruptNotRetained, nil
	}
	key, err := binding.CurrentNode.Key()
	if err != nil {
		return sessionruntime.WorkflowSessionInterruptUnhandled, err
	}
	c.mu.Lock()
	run, exists := c.currentRunLocked(key)
	interruptible := exists && (run.launching() || run.exact()) && !run.stopping()
	var expectedRunID currentNodeRunID
	if interruptible {
		expectedRunID = run.id
	}
	creator := false
	if interruptible && request.TargetOperationRef != nil && run.operation != nil {
		if creatorRef, ok := run.operation.RuntimeOperationRef(); ok {
			creator = runtimeops.SameOperationRef(creatorRef, *request.TargetOperationRef)
		}
	}
	c.mu.Unlock()
	if !interruptible {
		return sessionruntime.WorkflowSessionInterruptNoLongerLive, nil
	}
	if request.TargetOperationRef != nil {
		if !creator {
			return sessionruntime.WorkflowSessionInterruptOperationLocal, nil
		}
		switch request.Target {
		case runtimeops.CancellationTargetNonActive:
		case runtimeops.CancellationTargetActiveInterruptible:
		case runtimeops.CancellationTargetAbsentOrTerminal:
			return sessionruntime.WorkflowSessionInterruptNoLongerLive, nil
		case runtimeops.CancellationTargetQueuedMessage:
			panic("queued-message cancellation reached Workflow Session interrupt routing")
		default:
			panic("unknown Runtime Operation cancellation target")
		}
	}
	revalidateOwnership := func(ctx context.Context) error {
		currentTaskID, err := c.store.TaskIDForSession(ctx, request.SessionID)
		if err != nil {
			return err
		}
		if currentTaskID == nil || *currentTaskID != *taskID {
			return sessionruntime.ErrExecutionNoLongerLive
		}
		currentBinding, found, err := c.store.ResolveDirectSessionCurrentNodeBinding(ctx, request.SessionID)
		if err != nil {
			return err
		}
		if !found ||
			currentBinding.TaskID != *taskID ||
			!currentBinding.CurrentNode.Equal(binding.CurrentNode) {
			return sessionruntime.ErrExecutionNoLongerLive
		}
		return nil
	}
	cleanupOwned := false
	selector := InterruptSelector{
		TaskID:              *taskID,
		SessionID:           &request.SessionID,
		CurrentNode:         &binding.CurrentNode,
		expectedRunID:       &expectedRunID,
		revalidateOwnership: revalidateOwnership,
	}
	if request.TargetOperationRef != nil {
		targetOperation := *request.TargetOperationRef
		selector.expectedOperation = &targetOperation
	}
	err = c.interrupt(
		ctx,
		selector,
		func(cleanup func(func(context.Context) error, func(context.Context) error) error) error {
			if onCommitted == nil {
				return errors.New("workflow Session Interrupt committed without a cleanup owner")
			}
			cleanupOwned = true
			return onCommitted(sessionruntime.WorkflowCommittedInterruptCleanup(cleanup))
		},
	)
	if errors.Is(err, ErrNoInterruptibleExecution) ||
		errors.Is(err, sessionruntime.ErrExecutionNoLongerLive) {
		return sessionruntime.WorkflowSessionInterruptNoLongerLive, nil
	}
	if err != nil {
		return sessionruntime.WorkflowSessionInterruptUnhandled, err
	}
	if !cleanupOwned {
		return sessionruntime.WorkflowSessionInterruptUnhandled, errors.New("workflow Session Interrupt returned without transferring committed cleanup ownership")
	}
	return sessionruntime.WorkflowSessionInterruptCommitted, nil
}

var _ sessionruntime.WorkflowSessionInterruptor = (*CurrentNodeController)(nil)
