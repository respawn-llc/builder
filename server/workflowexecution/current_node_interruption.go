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
	"core/server/workflowstore"
	"core/shared/runtimeids"
)

const interruptCleanupTimeout = 300 * time.Second

var errCurrentNodeFinalizationPublicationInProgress = errors.New("Current Node finalization publication is in progress")

type currentNodeAdmissionWait struct {
	key  workflow.CurrentNodeReferenceKey
	done <-chan struct{}
}

type currentNodeInterruptCleanupState struct {
	stopHandles    []sessionruntime.ExecutionHandle
	waitHandles    []sessionruntime.ExecutionHandle
	finalizers     []currentNodeInterruptedFinalizer
	references     []workflow.CurrentNodeReference
	expectedExact  []workflowstore.LifecycleExactExecution
	drainedGates   []sessionruntime.WorkflowExecutionLease
	admissionWaits []currentNodeAdmissionWait
	taskFence      *currentNodeInterruptFence
}

type currentNodeInterruptedFinalizer struct {
	handle     sessionruntime.ExecutionHandle
	diagnostic interruptedFinalizerDiagnostic
}

func (c *CurrentNodeController) cleanupInterrupt(state currentNodeInterruptCleanupState) error {
	if len(state.stopHandles) == 0 &&
		len(state.drainedGates) == 0 &&
		len(state.admissionWaits) == 0 {
		return nil
	}
	references, expectedExact, err := normalizeCurrentNodeInterruptPublication(
		state.references,
		state.expectedExact,
	)
	if err != nil {
		return err
	}
	cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), interruptCleanupTimeout)
	defer cleanupCancel()
	for _, gate := range state.drainedGates {
		gate.Cancel()
	}
	c.wakeAdmissionWorker()
	finalizerByScope := make(map[runtimeids.ExecutionScopeID]currentNodeInterruptedFinalizer, len(state.finalizers))
	for _, finalizer := range state.finalizers {
		finalizerByScope[finalizer.handle.Scope().ID()] = finalizer
	}
	for _, handle := range state.stopHandles {
		requested := handle.RequestStop()
		finalizer, finalizing := finalizerByScope[handle.Scope().ID()]
		if !requested || !finalizing {
			continue
		}
		superviseInterruptedFinalizer(
			func() {
				_, _ = finalizer.handle.Wait(context.Background())
			},
			finalizer.diagnostic,
			interruptCleanupTimeout,
		)
	}
	taskID := references[0].TaskID
	persistenceErr := c.lifecycle.Run(cleanupCtx, taskID, func(ctx context.Context) error {
		detail := workflow.NewCurrentNodeInterruptionDetail(string(workflow.CurrentNodeInterruptionReasonUserInterrupt), nil)
		return c.publication.PublishCurrentNodeInterruption(
			ctx,
			references,
			workflowstore.CurrentNodeInterruptionFromReadyOrAdmitted,
			workflowstore.LifecycleFieldPresent,
			workflow.CurrentNodeInterruptionReasonUserInterrupt,
			detail,
			expectedExact,
		)
	})
	if persistenceErr == nil {
		for _, exact := range expectedExact {
			if err := c.authority.ConfirmWorkflowDisposition(exact.ScopeID); err != nil &&
				!errors.Is(err, sessionruntime.ErrExecutionNoLongerLive) {
				persistenceErr = errors.Join(persistenceErr, err)
			}
		}
	}
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
	verifyErr := c.lifecycle.Run(cleanupCtx, taskID, func(context.Context) error {
		c.mu.Lock()
		defer c.mu.Unlock()
		for _, handle := range state.waitHandles {
			if _, _, live := c.runByScopeLocked(handle.Scope().ID()); live {
				return errors.New("workflow interruption left an affected exact execution scope")
			}
		}
		if state.taskFence != nil && c.interrupts.fenceActive(state.taskFence) {
			return errors.New("workflow interruption fence remains active")
		}
		return nil
	})
	return errors.Join(persistenceErr, errors.Join(waitErrs...), verifyErr)
}

func normalizeCurrentNodeInterruptPublication(
	references []workflow.CurrentNodeReference,
	expectedExact []workflowstore.LifecycleExactExecution,
) ([]workflow.CurrentNodeReference, []workflowstore.LifecycleExactExecution, error) {
	if len(references) == 0 {
		return nil, nil, errors.New("workflow interruption requires a Current Node reference")
	}
	uniqueReferences := make([]workflow.CurrentNodeReference, 0, len(references))
	referenceByKey := make(map[workflow.CurrentNodeReferenceKey]workflow.CurrentNodeReference, len(references))
	for _, reference := range references {
		key, err := reference.Key()
		if err != nil {
			return nil, nil, err
		}
		if _, exists := referenceByKey[key]; exists {
			continue
		}
		referenceByKey[key] = reference
		uniqueReferences = append(uniqueReferences, reference)
	}

	uniqueExact := make([]workflowstore.LifecycleExactExecution, 0, len(expectedExact))
	exactByKey := make(map[workflow.CurrentNodeReferenceKey]workflowstore.LifecycleExactExecution, len(expectedExact))
	for _, exact := range expectedExact {
		key, err := exact.CurrentNode.Key()
		if err != nil {
			return nil, nil, err
		}
		if _, exists := referenceByKey[key]; !exists {
			return nil, nil, errors.New("workflow interruption Exact predecessor has no Current Node publication")
		}
		if current, exists := exactByKey[key]; exists {
			if current.ScopeID != exact.ScopeID {
				return nil, nil, errors.New("workflow interruption has contradictory Exact predecessors")
			}
			continue
		}
		exactByKey[key] = exact
		uniqueExact = append(uniqueExact, exact)
	}
	return uniqueReferences, uniqueExact, nil
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
		stopHandles     []sessionruntime.ExecutionHandle
		waitHandles     []sessionruntime.ExecutionHandle
		references      []workflow.CurrentNodeReference
		drainedGates    []sessionruntime.WorkflowExecutionLease
		admissionWaits  []currentNodeAdmissionWait
		taskFence       *currentNodeInterruptFence
		expectedExact   []workflowstore.LifecycleExactExecution
		publicationWait <-chan struct{}
		finalizers      []currentNodeInterruptedFinalizer
	)
	err := c.authority.WithWorkflowInterruptSelection(selector.TaskID, selector.SessionID, func(selection sessionruntime.WorkflowInterruptSelection) error {
		interruptibleScopes := make(map[runtimeids.ExecutionScopeID]struct{}, len(selection.Interruptible))
		for _, handle := range selection.Interruptible {
			interruptibleScopes[handle.Scope().ID()] = struct{}{}
		}
		selected := append([]sessionruntime.ExecutionHandle(nil), selection.Interruptible...)
		if selector.SessionID == nil {
			selected = append(selected, selection.Queued...)
		}
		owned := append([]sessionruntime.ExecutionHandle(nil), selected...)
		finalizingScopes := make(map[runtimeids.ExecutionScopeID]struct{}, len(selection.Finalizing))
		for _, handle := range selection.Finalizing {
			finalizingScopes[handle.Scope().ID()] = struct{}{}
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
		for _, handle := range owned {
			if run, _, exists := c.runByScopeLocked(handle.Scope().ID()); exists &&
				run.finalizationPublishing {
				publicationWait = run.finalizationPublicationDone
				return errCurrentNodeFinalizationPublicationInProgress
			}
		}
		activeOwned := owned[:0]
		activeSelected := selected[:0]
		for _, handle := range selected {
			run, _, live := c.runByScopeLocked(handle.Scope().ID())
			_, completed := c.completed[handle.Scope().ID()]
			if !completed && (!live || run.disposition != currentNodeRunDispositionStopped) {
				activeSelected = append(activeSelected, handle)
			}
		}
		selected = activeSelected
		for _, handle := range owned {
			if _, completed := c.completed[handle.Scope().ID()]; completed {
				continue
			}
			activeOwned = append(activeOwned, handle)
			scopeID := handle.Scope().ID()
			scopeRef, workflowScoped := handle.Scope().Workflow()
			if !workflowScoped {
				return errors.New("authority interrupt selection is not workflow scoped")
			}
			if live, _, exists := c.runByScopeLocked(scopeID); exists {
				if !scopeRef.CurrentNode.Equal(live.reference) {
					return errors.New("authority interrupt selection does not match live workflow execution ownership")
				}
				live.stopOnce(currentNodeRunStopInterrupted, context.Canceled)
				continue
			}
			key, keyErr := scopeRef.CurrentNode.Key()
			if keyErr != nil {
				return keyErr
			}
			_, gated := c.gates[key]
			run, exists := c.runs.get(key)
			if !gated || !exists || run.executionLease == nil || run.executionLease.ScopeID() != scopeID {
				return errors.New("authority interrupt selection does not match workflow execution ownership")
			}
		}
		owned = activeOwned
		if len(selected) == 0 {
			return sessionruntime.ErrExecutionNoLongerLive
		}

		if selector.SessionID == nil {
			if err := validateTaskControllerWorkLocked(c, selector.TaskID); err != nil {
				return err
			}
		}
		var fenceErr error
		taskFence, fenceErr = c.interrupts.beginTask(selector.TaskID)
		if fenceErr != nil {
			return fenceErr
		}
		for _, handle := range selected {
			scopeID := handle.Scope().ID()
			scopeRef, _ := handle.Scope().Workflow()
			c.stopping[scopeID] = struct{}{}
			stopHandles = append(stopHandles, handle)
			waitHandles = append(waitHandles, handle)
			references = append(references, scopeRef.CurrentNode)
			if _, exact := interruptibleScopes[scopeID]; exact {
				expectedExact = append(expectedExact, workflowstore.LifecycleExactExecution{
					CurrentNode: scopeRef.CurrentNode,
					ScopeID:     scopeID,
				})
			}
			if taskFence != nil {
				c.interrupts.addScope(taskFence, scopeID)
			}
			if _, finalizing := finalizingScopes[scopeID]; finalizing {
				run, _, exists := c.runByScopeLocked(scopeID)
				if !exists {
					return errors.New("finalizing Exact Scope has no Current Node Run")
				}
				finalizers = append(finalizers, currentNodeInterruptedFinalizer{
					handle: handle,
					diagnostic: interruptedFinalizerDiagnostic{
						TaskID:         scopeRef.CurrentNode.TaskID,
						CurrentNode:    scopeRef.CurrentNode,
						ScopeID:        scopeID,
						RunPhase:       run.phase,
						FinalizerPhase: workflowFinalizerPhaseResult,
						Canceled:       true,
					},
				})
			}
		}
		if selector.SessionID != nil {
			return nil
		}

		return drainTaskControllerWorkLocked(c, selector.TaskID, taskFence, &references, &admissionWaits, &drainedGates)
	})
	if errors.Is(err, errCurrentNodeFinalizationPublicationInProgress) {
		if publicationWait == nil {
			panic("Current Node finalization publication wait is missing")
		}
		select {
		case <-publicationWait:
			return c.Interrupt(ctx, selector)
		case <-ctx.Done():
			return context.Cause(ctx)
		}
	}
	if errors.Is(err, sessionruntime.ErrExecutionNoLongerLive) {
		return ErrNoInterruptibleExecution
	}
	if err != nil {
		return err
	}
	return c.cleanupInterrupt(currentNodeInterruptCleanupState{
		stopHandles:    stopHandles,
		waitHandles:    waitHandles,
		finalizers:     finalizers,
		references:     references,
		drainedGates:   drainedGates,
		admissionWaits: admissionWaits,
		taskFence:      taskFence,
		expectedExact:  expectedExact,
	})
}

// InterruptForManualMove atomically revalidates the mutation, then fences all
// currently running, pending-free workflow scopes for a Task before closing
// their canonical prompt stores. It intentionally rejects queued, finalizing,
// and waiting-Question work before requesting any stop.
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
		drainedGates   []sessionruntime.WorkflowExecutionLease
		admissionWaits []currentNodeAdmissionWait
		taskFence      *currentNodeInterruptFence
		expectedExact  []workflowstore.LifecycleExactExecution
	)
	if err := c.lifecycle.Run(ctx, taskID, func(ctx context.Context) error {
		if beforeSelection != nil {
			if err := beforeSelection(); err != nil {
				return err
			}
		}
		return c.authority.WithWorkflowManualMoveSelection(taskID, func(selection sessionruntime.WorkflowInterruptSelection) error {
			if len(selection.Queued) != 0 || len(selection.Finalizing) != 0 {
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
			if c.interrupts.taskActive(taskID) {
				return ErrTaskExecutionNotQuiescent
			}

			for _, handle := range selection.Interruptible {
				scopeID := handle.Scope().ID()
				scopeRef, workflowScoped := handle.Scope().Workflow()
				if !workflowScoped || scopeRef.CurrentNode.TaskID != taskID {
					return errors.New("manual move interruption selection is not workflow scoped")
				}
				live, _, exists := c.runByScopeLocked(scopeID)
				if !exists || !live.reference.Equal(scopeRef.CurrentNode) {
					return errors.New("manual move interruption selection does not match controller ownership")
				}
				live.stopOnce(currentNodeRunStopInterrupted, context.Canceled)
			}
			if err := validateTaskControllerWorkLocked(c, taskID); err != nil {
				return err
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
			for _, handle := range selection.Interruptible {
				scopeID := handle.Scope().ID()
				scopeRef, _ := handle.Scope().Workflow()
				c.stopping[scopeID] = struct{}{}
				if taskFence != nil {
					c.interrupts.addScope(taskFence, scopeID)
				}
				stopHandles = append(stopHandles, handle)
				waitHandles = append(waitHandles, handle)
				references = append(references, scopeRef.CurrentNode)
				expectedExact = append(expectedExact, workflowstore.LifecycleExactExecution{
					CurrentNode: scopeRef.CurrentNode,
					ScopeID:     scopeID,
				})
			}
			if taskFence != nil {
				if err := drainTaskControllerWorkLocked(c, taskID, taskFence, &references, &admissionWaits, &drainedGates); err != nil {
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
		drainedGates:   drainedGates,
		admissionWaits: admissionWaits,
		taskFence:      taskFence,
		expectedExact:  expectedExact,
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
	for _, run := range c.runs.byCurrentNode {
		if run.reference.TaskID == taskID && run.phase != currentNodeRunRunning {
			return true
		}
	}
	return false
}

func validateTaskControllerWorkLocked(c *CurrentNodeController, taskID workflow.TaskID) error {
	for _, run := range c.runs.byCurrentNode {
		if run.reference.TaskID == taskID {
			if err := run.reference.Validate(); err != nil {
				return fmt.Errorf("validate current node Run for task %s: %w", taskID, err)
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
	drainedGates *[]sessionruntime.WorkflowExecutionLease,
) error {
	explicitQueue := c.explicitQueue[:0]
	for _, key := range c.explicitQueue {
		run, exists := c.runs.get(key)
		if !exists {
			return errors.New("explicit queue lost its current node Run")
		}
		if run.reference.TaskID != taskID {
			explicitQueue = append(explicitQueue, key)
			continue
		}
		delete(c.explicitQueued, key)
		c.interrupts.addCurrentNode(fence, key)
		*references = append(*references, run.reference)
		*admissionWaits = appendAdmissionWait(*admissionWaits, key, nil)
		run.stopOnce(currentNodeRunStopInterrupted, context.Canceled)
		c.runs.delete(key)
	}
	c.explicitQueue = explicitQueue

	for entry := c.automaticQueue.first; entry != nil; {
		next := entry.globalNext
		run, exists := c.runs.get(entry.key)
		if !exists {
			return errors.New("automatic queue lost its current node Run")
		}
		if run.reference.TaskID != taskID {
			entry = next
			continue
		}
		key := c.automaticQueue.remove(entry, run)
		delete(c.queued, key)
		c.interrupts.addCurrentNode(fence, key)
		*references = append(*references, run.reference)
		*admissionWaits = appendAdmissionWait(*admissionWaits, key, nil)
		run.stopOnce(currentNodeRunStopInterrupted, context.Canceled)
		c.runs.delete(key)
		entry = next
	}

	for sourceScope, keys := range c.heldStarts {
		kept := keys[:0]
		for _, key := range keys {
			run, exists := c.runs.get(key)
			if !exists {
				return errors.New("held-start index lost its current node Run")
			}
			if run.reference.TaskID != taskID {
				kept = append(kept, key)
				continue
			}
			c.interrupts.addCurrentNode(fence, key)
			*references = append(*references, run.reference)
			*admissionWaits = appendAdmissionWait(*admissionWaits, key, nil)
			run.stopOnce(currentNodeRunStopInterrupted, context.Canceled)
			c.runs.delete(key)
		}
		if len(kept) == 0 {
			delete(c.heldStarts, sourceScope)
		} else {
			c.heldStarts[sourceScope] = kept
		}
	}

	for key := range c.explicitReservations {
		run, exists := c.runs.get(key)
		if !exists {
			return errors.New("explicit reservation lost its current node Run")
		}
		if run.reference.TaskID != taskID {
			continue
		}
		delete(c.explicitReservations, key)
		c.interrupts.addCurrentNode(fence, key)
		*references = append(*references, run.reference)
		*admissionWaits = appendAdmissionWait(*admissionWaits, key, run.admissionDone)
		run.stopOnce(currentNodeRunStopInterrupted, context.Canceled)
		if _, preparing := c.admissionWorkers[key]; !preparing {
			c.runs.delete(key)
		}
	}
	for key := range c.automaticReservations {
		run, exists := c.runs.get(key)
		if !exists {
			return errors.New("automatic reservation lost its current node Run")
		}
		if run.reference.TaskID != taskID {
			continue
		}
		delete(c.automaticReservations, key)
		c.releaseAgentCapacityLocked(run.agentCapacityLease)
		c.interrupts.addCurrentNode(fence, key)
		*references = append(*references, run.reference)
		*admissionWaits = appendAdmissionWait(*admissionWaits, key, run.admissionDone)
		run.stopOnce(currentNodeRunStopInterrupted, context.Canceled)
		if _, preparing := c.admissionWorkers[key]; !preparing {
			c.runs.delete(key)
		}
	}
	for key := range c.gates {
		run, exists := c.runs.get(key)
		if !exists || run.executionLease == nil {
			return errors.New("admission gate lost its current node Run")
		}
		if run.reference.TaskID != taskID {
			continue
		}
		c.stopping[run.executionLease.ScopeID()] = struct{}{}
		c.interrupts.addCurrentNode(fence, key)
		*drainedGates = append(*drainedGates, *run.executionLease)
		*references = append(*references, run.reference)
		*admissionWaits = appendAdmissionWait(*admissionWaits, key, run.admissionDone)
		run.stopOnce(currentNodeRunStopInterrupted, context.Canceled)
	}
	for key, run := range c.runs.byCurrentNode {
		if run.reference.TaskID != taskID ||
			run.disposition != currentNodeRunDispositionQueued {
			continue
		}
		c.interrupts.addCurrentNode(fence, key)
		*references = append(*references, run.reference)
		*admissionWaits = appendAdmissionWait(*admissionWaits, key, run.admissionDone)
		run.stopOnce(currentNodeRunStopInterrupted, context.Canceled)
		c.runs.delete(key)
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
