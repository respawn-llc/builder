package workflowexecution

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"core/server/sessionruntime"
	"core/server/workflow"
	"core/shared/runtimeids"
)

const interruptCleanupTimeout = 300 * time.Second

type currentNodeAdmissionWait struct {
	key  workflow.CurrentNodeReferenceKey
	done <-chan struct{}
}

type currentNodeInterruptCleanupState struct {
	stopHandles    []sessionruntime.ExecutionHandle
	waitHandles    []sessionruntime.ExecutionHandle
	references     []workflow.CurrentNodeReference
	admissionWaits []currentNodeAdmissionWait
	taskFence      *currentNodeInterruptFence
}

func (c *CurrentNodeController) cleanupInterrupt(state currentNodeInterruptCleanupState) error {
	if len(state.stopHandles) == 0 && len(state.admissionWaits) == 0 {
		return nil
	}
	cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), interruptCleanupTimeout)
	defer cleanupCancel()
	c.wakeAdmissionWorker()
	for _, handle := range state.stopHandles {
		handle.RequestStop()
	}
	persistenceErr := c.permit.Run(cleanupCtx, func(ctx context.Context) error {
		detail := workflow.NewCurrentNodeInterruptionDetail(string(workflow.CurrentNodeInterruptionReasonUserInterrupt), nil)
		_, err := interruptCurrentNodeReferences(ctx, c.store.InterruptCurrentNode, state.references, workflow.CurrentNodeInterruptionReasonUserInterrupt, detail)
		return err
	})
	var waitErrs []error
	for _, handle := range state.waitHandles {
		if _, err := handle.Wait(cleanupCtx); err != nil &&
			!errors.Is(err, context.Canceled) &&
			!errors.Is(err, sessionruntime.ErrExecutionNoLongerLive) &&
			!errors.Is(err, ErrTaskExecutionNotQuiescent) {
			waitErrs = append(waitErrs, err)
		}
	}
	for _, wait := range state.admissionWaits {
		if wait.done != nil {
			select {
			case <-wait.done:
			case <-cleanupCtx.Done():
				waitErrs = append(waitErrs, context.Cause(cleanupCtx))
				continue
			}
		}
		c.finishTaskInterruptAdmissionKey(wait.key)
	}
	verifyErr := c.permit.Run(cleanupCtx, func(context.Context) error {
		for _, handle := range state.waitHandles {
			if _, live := c.authority.ExecutionByScope(handle.Scope().ID()); live {
				return errors.New("workflow interruption left an affected exact execution scope")
			}
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		if state.taskFence != nil && c.interrupts.fenceActive(state.taskFence) {
			return errors.New("workflow interruption fence remains active")
		}
		return nil
	})
	return errors.Join(persistenceErr, errors.Join(waitErrs...), verifyErr)
}

// Interrupt stops the exact live workflow scope selected by the caller. A
// Task-wide interrupt also drains controller-owned automatic work for that
// Task so a successor cannot start after the interrupt returns.
func (c *CurrentNodeController) Interrupt(ctx context.Context, selector InterruptSelector) error {
	if c == nil {
		return errors.New("current node workflow controller is required")
	}
	if err := selector.Validate(); err != nil {
		return err
	}
	var (
		stopHandles    []sessionruntime.ExecutionHandle
		waitHandles    []sessionruntime.ExecutionHandle
		references     []workflow.CurrentNodeReference
		admissionWaits []currentNodeAdmissionWait
		taskFence      *currentNodeInterruptFence
	)
	if err := c.permit.Run(ctx, func(ctx context.Context) error {
		err := c.authority.WithWorkflowInterruptSelection(selector.TaskID, selector.SessionID, func(selection sessionruntime.WorkflowInterruptSelection) error {
			selected := append([]sessionruntime.WorkflowExecutionSelection(nil), selection.Interruptible...)
			if selector.SessionID == nil {
				selected = append(selected, selection.Queued...)
			}
			c.mu.Lock()
			defer c.mu.Unlock()
			if c.closed {
				return errors.New("current node workflow controller is closed")
			}
			if c.workerErr != nil {
				return fmt.Errorf("workflow execution lifecycle failed: %w", c.workerErr)
			}
			if c.interrupts.taskActive(selector.TaskID) {
				return ErrTaskExecutionNotQuiescent
			}
			for _, selectedExecution := range selected {
				key, err := selectedExecution.Operation.CurrentNode.Key()
				if err != nil {
					return err
				}
				operation := c.operations[key]
				if operation == nil || operation.ref.OperationID != selectedExecution.Operation.OperationID {
					return errors.New("authority interrupt selection does not match admitted workflow operation")
				}
			}

			if selector.SessionID == nil {
				if err := validateTaskControllerWorkLocked(c, selector.TaskID); err != nil {
					return err
				}
				var fenceErr error
				taskFence, fenceErr = c.interrupts.beginTask(selector.TaskID)
				if fenceErr != nil {
					return fenceErr
				}
			}
			for _, selectedExecution := range selected {
				stopHandles = append(stopHandles, selectedExecution.Handle)
				waitHandles = append(waitHandles, selectedExecution.Handle)
				references = append(references, selectedExecution.Operation.CurrentNode)
				if taskFence != nil {
					c.interrupts.addOperation(taskFence, selectedExecution.Operation.OperationID)
				}
			}
			if taskFence == nil {
				return nil
			}
			return drainTaskControllerWorkLocked(c, selector.TaskID, taskFence, &references, &admissionWaits)
		})
		if errors.Is(err, sessionruntime.ErrExecutionNoLongerLive) {
			currentNodes, durableErr := c.store.ListCurrentNodes(ctx, selector.TaskID)
			if durableErr != nil {
				return durableErr
			}
			if interruptSelectorAlreadyInterrupted(currentNodes, selector.SessionID) {
				return nil
			}
			return ErrNoInterruptibleExecution
		}
		if err != nil {
			return err
		}
		return nil
	}); err != nil {
		return err
	}
	return c.cleanupInterrupt(currentNodeInterruptCleanupState{
		stopHandles:    stopHandles,
		waitHandles:    waitHandles,
		references:     references,
		admissionWaits: admissionWaits,
		taskFence:      taskFence,
	})
}

func interruptSelectorAlreadyInterrupted(
	currentNodes []workflow.CurrentNode,
	sessionID *runtimeids.SessionID,
) bool {
	selected := false
	for _, currentNode := range currentNodes {
		if sessionID != nil &&
			(currentNode.SessionID == nil || *currentNode.SessionID != *sessionID) {
			continue
		}
		selected = true
		if currentNode.Scheduling == nil ||
			currentNode.Scheduling.State != workflow.CurrentNodeSchedulingInterrupted {
			return false
		}
	}
	return selected
}

// InterruptForManualMove atomically revalidates the mutation, then fences all
// currently running, pending-free workflow scopes for a Task before closing
// their canonical prompt stores. It intentionally rejects queued and
// waiting-Question work before requesting any stop.
func (c *CurrentNodeController) InterruptForManualMove(
	ctx context.Context,
	taskID workflow.TaskID,
	beforeSelection func() error,
) error {
	if c == nil {
		return errors.New("current node workflow controller is required")
	}
	if strings.TrimSpace(string(taskID)) == "" {
		return errors.New("workflow task id is required")
	}
	var (
		stopHandles    []sessionruntime.ExecutionHandle
		waitHandles    []sessionruntime.ExecutionHandle
		references     []workflow.CurrentNodeReference
		admissionWaits []currentNodeAdmissionWait
		taskFence      *currentNodeInterruptFence
	)
	if err := c.permit.Run(ctx, func(ctx context.Context) error {
		if beforeSelection != nil {
			if err := beforeSelection(); err != nil {
				return err
			}
		}
		return c.authority.WithWorkflowManualMoveSelection(taskID, func(selection sessionruntime.WorkflowInterruptSelection) error {
			if len(selection.Queued) != 0 {
				return ErrManualMoveLifecycleConflict
			}
			c.mu.Lock()
			defer c.mu.Unlock()
			if c.closed {
				return errors.New("current node workflow controller is closed")
			}
			if c.workerErr != nil {
				return fmt.Errorf("workflow execution lifecycle failed: %w", c.workerErr)
			}
			if taskHasPreparationLocked(c, taskID) {
				return ErrManualMoveLifecycleConflict
			}
			if c.interrupts.taskActive(taskID) {
				return ErrTaskExecutionNotQuiescent
			}

			for _, selectedExecution := range selection.Interruptible {
				if selectedExecution.Operation.CurrentNode.TaskID != taskID {
					return errors.New("manual move interruption selection is not workflow scoped")
				}
				key, err := selectedExecution.Operation.CurrentNode.Key()
				if err != nil {
					return err
				}
				operation := c.operations[key]
				if operation == nil || operation.ref.OperationID != selectedExecution.Operation.OperationID {
					return errors.New("manual move interruption selection does not match controller ownership")
				}
			}
			for entry := c.automaticQueue.first; entry != nil; entry = entry.globalNext {
				if entry.start.reference.TaskID == taskID {
					if _, err := entry.start.reference.Key(); err != nil {
						return err
					}
				}
			}
			for _, start := range c.explicitQueue {
				if start.reference.TaskID == taskID {
					if _, err := start.reference.Key(); err != nil {
						return err
					}
				}
			}
			for _, operation := range c.operations {
				for _, start := range operation.heldStarts {
					if start.reference.TaskID == taskID {
						if _, err := start.reference.Key(); err != nil {
							return err
						}
					}
				}
			}
			for _, start := range c.explicitReservations {
				if start.reference.TaskID == taskID {
					if err := start.reference.Validate(); err != nil {
						return err
					}
				}
			}
			for _, start := range c.automaticReservations {
				if start.reference.TaskID == taskID {
					if err := start.reference.Validate(); err != nil {
						return err
					}
				}
			}
			if len(selection.Interruptible) != 0 ||
				taskHasControllerQueuedWorkLocked(c, taskID) {
				if err := validateTaskControllerWorkLocked(c, taskID); err != nil {
					return err
				}
				var err error
				taskFence, err = c.interrupts.beginTask(taskID)
				if err != nil {
					return err
				}
			}
			for _, selectedExecution := range selection.Interruptible {
				if taskFence != nil {
					c.interrupts.addOperation(taskFence, selectedExecution.Operation.OperationID)
				}
				stopHandles = append(stopHandles, selectedExecution.Handle)
				waitHandles = append(waitHandles, selectedExecution.Handle)
				references = append(references, selectedExecution.Operation.CurrentNode)
			}
			if taskFence != nil {
				if err := drainTaskControllerWorkLocked(c, taskID, taskFence, &references, &admissionWaits); err != nil {
					return err
				}
			}
			return nil
		})
	}); err != nil {
		if errors.Is(err, sessionruntime.ErrWorkflowQuestionPending) {
			return err
		}
		if errors.Is(err, sessionruntime.ErrWorkflowApprovalPending) {
			return ErrManualMoveLifecycleConflict
		}
		return err
	}
	return c.cleanupInterrupt(currentNodeInterruptCleanupState{
		stopHandles:    stopHandles,
		waitHandles:    waitHandles,
		references:     references,
		admissionWaits: admissionWaits,
		taskFence:      taskFence,
	})
}

func appendAdmissionWait(
	waits []currentNodeAdmissionWait,
	key workflow.CurrentNodeReferenceKey,
	wait <-chan struct{},
) []currentNodeAdmissionWait {
	return append(waits, currentNodeAdmissionWait{key: key, done: wait})
}

func taskHasControllerQueuedWorkLocked(c *CurrentNodeController, taskID workflow.TaskID) bool {
	if taskHasPreparationLocked(c, taskID) {
		return true
	}
	for entry := c.automaticQueue.first; entry != nil; entry = entry.globalNext {
		if entry.start.reference.TaskID == taskID {
			return true
		}
	}
	for _, start := range c.explicitQueue {
		if start.reference.TaskID == taskID {
			return true
		}
	}
	for _, operation := range c.operations {
		for _, start := range operation.heldStarts {
			if start.reference.TaskID == taskID {
				return true
			}
		}
	}
	for _, start := range c.automaticReservations {
		if start.reference.TaskID == taskID {
			return true
		}
	}
	for _, start := range c.explicitReservations {
		if start.reference.TaskID == taskID {
			return true
		}
	}
	for _, start := range c.admissionWorkers {
		if start.reference.TaskID == taskID {
			return true
		}
	}
	return false
}

func taskHasPreparationLocked(c *CurrentNodeController, taskID workflow.TaskID) bool {
	return c.queuedTaskPreparationLocked(taskID) != nil || c.runningTaskPreparationLocked(taskID) != nil
}

func validateTaskControllerWorkLocked(c *CurrentNodeController, taskID workflow.TaskID) error {
	for _, batch := range []*taskPreparationBatch{c.queuedTaskPreparationLocked(taskID), c.runningTaskPreparationLocked(taskID)} {
		if batch == nil {
			continue
		}
		for index, start := range batch.starts {
			if _, err := start.reference.Key(); err != nil {
				return fmt.Errorf("validate task preparation Current Node at index %d for task %s: %w", index, taskID, err)
			}
		}
	}
	for entry := c.automaticQueue.first; entry != nil; entry = entry.globalNext {
		if entry.start.reference.TaskID == taskID {
			if _, err := entry.start.reference.Key(); err != nil {
				return fmt.Errorf("validate automatic queue for task %s: %w", taskID, err)
			}
		}
	}
	for _, start := range c.explicitQueue {
		if start.reference.TaskID == taskID {
			if _, err := start.reference.Key(); err != nil {
				return fmt.Errorf("validate explicit queue for task %s: %w", taskID, err)
			}
		}
	}
	for _, operation := range c.operations {
		for _, start := range operation.heldStarts {
			if start.reference.TaskID == taskID {
				if _, err := start.reference.Key(); err != nil {
					return fmt.Errorf("validate held start for task %s: %w", taskID, err)
				}
			}
		}
	}
	for _, start := range c.explicitReservations {
		if start.reference.TaskID == taskID {
			if err := start.reference.Validate(); err != nil {
				return fmt.Errorf("validate explicit reservation for task %s: %w", taskID, err)
			}
		}
	}
	for _, start := range c.automaticReservations {
		if start.reference.TaskID == taskID {
			if err := start.reference.Validate(); err != nil {
				return fmt.Errorf("validate automatic reservation for task %s: %w", taskID, err)
			}
		}
	}
	for _, start := range c.admissionWorkers {
		if start.reference.TaskID == taskID {
			if err := start.reference.Validate(); err != nil {
				return fmt.Errorf("validate admission worker for task %s: %w", taskID, err)
			}
		}
	}
	return nil
}

func drainTaskControllerWorkLocked(
	c *CurrentNodeController,
	taskID workflow.TaskID,
	fence *currentNodeInterruptFence,
	references *[]workflow.CurrentNodeReference,
	admissionWaits *[]currentNodeAdmissionWait,
) error {
	explicitQueue := c.explicitQueue[:0]
	for _, start := range c.explicitQueue {
		if start.reference.TaskID != taskID {
			explicitQueue = append(explicitQueue, start)
			continue
		}
		key, err := start.reference.Key()
		if err != nil {
			return fmt.Errorf("drain explicit queue for task %s: %w", taskID, err)
		}
		delete(c.explicitQueued, key)
		c.interrupts.addCurrentNode(fence, key)
		*references = append(*references, start.reference)
		*admissionWaits = appendAdmissionWait(*admissionWaits, key, nil)
	}
	c.explicitQueue = explicitQueue

	for entry := c.automaticQueue.first; entry != nil; {
		next := entry.globalNext
		if entry.start.reference.TaskID != taskID {
			entry = next
			continue
		}
		start := c.automaticQueue.remove(entry)
		key, err := start.reference.Key()
		if err != nil {
			return fmt.Errorf("drain automatic queue for task %s: %w", taskID, err)
		}
		delete(c.queued, key)
		c.interrupts.addCurrentNode(fence, key)
		*references = append(*references, start.reference)
		*admissionWaits = appendAdmissionWait(*admissionWaits, key, nil)
		entry = next
	}

	for _, operation := range c.operations {
		starts := operation.heldStarts
		kept := starts[:0]
		for _, start := range starts {
			if start.reference.TaskID != taskID {
				kept = append(kept, start)
				continue
			}
			key, err := start.reference.Key()
			if err != nil {
				return fmt.Errorf("drain held start for task %s: %w", taskID, err)
			}
			c.interrupts.addCurrentNode(fence, key)
			*references = append(*references, start.reference)
			*admissionWaits = appendAdmissionWait(*admissionWaits, key, nil)
		}
		if len(kept) == 0 {
			operation.heldStarts = nil
		} else {
			operation.heldStarts = kept
		}
	}

	for key, start := range c.explicitReservations {
		if start.reference.TaskID != taskID {
			continue
		}
		if _, preparing := c.admissionWorkers[key]; preparing {
			continue
		}
		delete(c.explicitReservations, key)
		c.interrupts.addCurrentNode(fence, key)
		*references = append(*references, start.reference)
		*admissionWaits = appendAdmissionWait(*admissionWaits, key, start.done)
	}
	for key, start := range c.automaticReservations {
		if start.reference.TaskID != taskID {
			continue
		}
		if _, preparing := c.admissionWorkers[key]; preparing {
			continue
		}
		delete(c.automaticReservations, key)
		c.releaseAgentCapacityLocked(start.agentCapacityLease)
		c.interrupts.addCurrentNode(fence, key)
		*references = append(*references, start.reference)
		*admissionWaits = appendAdmissionWait(*admissionWaits, key, start.done)
	}
	return nil
}

func interruptCurrentNodeReferences(
	ctx context.Context,
	interrupt func(context.Context, workflow.CurrentNodeReference, workflow.CurrentNodeInterruptionReason, workflow.CurrentNodeInterruptionDetail) error,
	references []workflow.CurrentNodeReference,
	reason workflow.CurrentNodeInterruptionReason,
	detail workflow.CurrentNodeInterruptionDetail,
) ([]workflow.CurrentNodeReference, error) {
	seen := make(map[workflow.CurrentNodeReferenceKey]struct{}, len(references))
	interrupted := make([]workflow.CurrentNodeReference, 0, len(references))
	var interruptErrs []error
	for _, reference := range references {
		key, err := reference.Key()
		if err != nil {
			return interrupted, err
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		err = interrupt(ctx, reference, reason, detail)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			interruptErrs = append(interruptErrs, err)
			continue
		}
		interrupted = append(interrupted, reference)
	}
	return interrupted, errors.Join(interruptErrs...)
}

func (c *CurrentNodeController) finishTaskInterruptAdmission(reference workflow.CurrentNodeReference) {
	key, err := reference.Key()
	if err != nil {
		panic(fmt.Sprintf("finish task interrupt admission: %v", err))
	}
	c.finishTaskInterruptAdmissionKey(key)
}

func (c *CurrentNodeController) finishTaskInterruptAdmissionKey(key workflow.CurrentNodeReferenceKey) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.interrupts.finishCurrentNode(key)
}
