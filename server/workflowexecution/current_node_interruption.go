package workflowexecution

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"core/server/sessionruntime"
	"core/server/workflow"
	"core/server/workflowstore"
)

const interruptCleanupTimeout = 300 * time.Second

type currentNodeAdmissionWait struct {
	runID currentNodeRunID
	done  <-chan struct{}
}

type currentNodeInterruptCleanupState struct {
	taskID          workflow.TaskID
	stopHandles     []sessionruntime.ExecutionHandle
	waitHandles     []sessionruntime.ExecutionHandle
	references      []workflow.CurrentNodeReference
	selectedRuns    []currentNodeRunID
	launchRuns      []currentNodeRunID
	admissionWaits  []currentNodeAdmissionWait
	taskFence       *currentNodeInterruptFence
	persisted       bool
	fatalDiagnostic LifecycleFatalDiagnostic
}

func (c *CurrentNodeController) cleanupInterrupt(state currentNodeInterruptCleanupState) error {
	if len(state.stopHandles) == 0 &&
		len(state.launchRuns) == 0 &&
		len(state.admissionWaits) == 0 {
		return nil
	}
	cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), interruptCleanupTimeout)
	defer cleanupCancel()
	c.mu.Lock()
	exactHandles := make([]sessionruntime.ExecutionHandle, 0, len(state.stopHandles))
	for _, handle := range state.stopHandles {
		if _, exact := c.runByScopeLocked(handle.Scope().ID()); exact {
			exactHandles = append(exactHandles, handle)
		}
	}
	c.mu.Unlock()
	var persistenceErr error
	if !state.persisted {
		persistenceErr = c.taskMutations.Run(cleanupCtx, state.taskID, func(ctx context.Context) error {
			return c.persistUserInterrupt(ctx, state.references)
		})
	}
	if persistenceErr != nil {
		c.mu.Lock()
		if state.taskFence != nil {
			c.rollbackTaskInterruptLocked(state.taskFence)
		} else {
			c.rollbackSelectedInterruptLocked(state.selectedRuns)
		}
		c.mu.Unlock()
		return persistenceErr
	}
	for _, handle := range exactHandles {
		handle.RequestStop()
	}
	c.mu.Lock()
	for _, runID := range state.launchRuns {
		run := c.runs[runID]
		if run == nil || !run.launching() || c.currentRuns[run.key] != runID {
			continue
		}
		if run.launchCancel != nil {
			run.launchCancel()
		}
		if run.lease != nil {
			run.lease.Cancel()
		}
	}
	c.mu.Unlock()
	c.wakeAdmissionWorker()
	for _, handle := range state.stopHandles {
		handle.RequestStop()
	}
	var waitErrs []error
	for _, handle := range state.waitHandles {
		if err := handle.Stop(cleanupCtx); err != nil {
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
		c.finishTaskInterruptAdmissionRun(wait.runID)
	}
	verifyErr := c.taskMutations.Run(cleanupCtx, state.taskID, func(context.Context) error {
		c.mu.Lock()
		defer c.mu.Unlock()
		for _, handle := range state.waitHandles {
			if _, live := c.runByScopeLocked(handle.Scope().ID()); live {
				return errors.New("workflow interruption left an affected exact execution scope")
			}
		}
		if state.taskFence != nil && c.interruptFenceActiveLocked(state.taskFence) {
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
	if selector.SessionID == nil {
		c.mu.Lock()
		existingFence := c.taskInterruptFenceLocked(selector.TaskID)
		var existingCompletion <-chan struct{}
		if existingFence != nil {
			existingCompletion = existingFence.done
		}
		c.mu.Unlock()
		if existingCompletion != nil {
			select {
			case <-existingCompletion:
				return nil
			case <-ctx.Done():
				return context.Cause(ctx)
			}
		}
	}
	var (
		stopHandles    []sessionruntime.ExecutionHandle
		waitHandles    []sessionruntime.ExecutionHandle
		references     []workflow.CurrentNodeReference
		selectedRuns   []currentNodeRunID
		launchRuns     []currentNodeRunID
		admissionWaits []currentNodeAdmissionWait
		taskFence      *currentNodeInterruptFence
		persisted      bool
	)
	if err := c.taskMutations.Run(ctx, selector.TaskID, func(ctx context.Context) error {
		err := c.authority.WithWorkflowInterruptSelection(selector.TaskID, selector.SessionID, func(selection sessionruntime.WorkflowInterruptSelection) error {
			selected := append([]sessionruntime.ExecutionHandle(nil), selection.Interruptible...)
			if selector.SessionID == nil {
				selected = append(selected, selection.Queued...)
			}
			owned := append([]sessionruntime.ExecutionHandle(nil), selected...)
			if selector.SessionID == nil {
				owned = append(owned, selection.Finalizing...)
			}
			c.mu.Lock()
			defer c.mu.Unlock()
			if c.closed {
				return errors.New("current node workflow controller is closed")
			}
			if c.taskInterruptActiveLocked(selector.TaskID) {
				return ErrTaskExecutionNotQuiescent
			}
			selectedExact := false
			for _, handle := range owned {
				scopeID := handle.Scope().ID()
				scopeRef, workflowScoped := handle.Scope().Workflow()
				if !workflowScoped {
					return errors.New("authority interrupt selection is not workflow scoped")
				}
				if live, exists := c.runByScopeLocked(scopeID); exists {
					selectedExact = true
					if !scopeRef.CurrentNode.Equal(live.reference) {
						return errors.New("authority interrupt selection does not match live workflow execution ownership")
					}
					continue
				}
				run, launching := c.launchingRunByScopeLocked(scopeID)
				if !launching || !run.reference.Equal(scopeRef.CurrentNode) {
					return errors.New("authority interrupt selection does not match workflow execution ownership")
				}
			}

			if selector.SessionID == nil {
				if err := validateTaskControllerWorkLocked(c, selector.TaskID); err != nil {
					return err
				}
				var fenceErr error
				taskFence, fenceErr = c.beginTaskInterruptLocked(selector.TaskID)
				if fenceErr != nil {
					return fenceErr
				}
			}
			for _, handle := range selected {
				scopeID := handle.Scope().ID()
				scopeRef, _ := handle.Scope().Workflow()
				run, exists := c.runByScopeLocked(scopeID)
				if !exists {
					run, exists = c.launchingRunByScopeLocked(scopeID)
				}
				if !exists {
					return errors.New("selected workflow execution has no Run generation")
				}
				selectedRuns = append(selectedRuns, run.id)
				if taskFence != nil {
					c.fenceRunLocked(run, taskFence)
				} else {
					run.stop = currentNodeRunStopInterrupting
					if run.completion.committed() {
						for _, successorID := range run.successors {
							successor := c.runs[successorID]
							if successor == nil {
								continue
							}
							successor.stop = currentNodeRunStopInterrupting
							references = append(references, successor.reference)
							admissionWaits = appendAdmissionWait(admissionWaits, successorID, successor.admissionDone)
						}
					}
				}
				stopHandles = append(stopHandles, handle)
				waitHandles = append(waitHandles, handle)
				if !run.completion.committed() || run.completionSourceRetained {
					references = append(references, scopeRef.CurrentNode)
				}
			}
			if taskFence == nil {
				return nil
			}
			for _, handle := range selection.Finalizing {
				scopeRef, _ := handle.Scope().Workflow()
				run, exists := c.runByScopeLocked(handle.Scope().ID())
				if !exists {
					return errors.New("finalizing workflow execution has no Run generation")
				}
				selectedRuns = append(selectedRuns, run.id)
				c.fenceRunLocked(run, taskFence)
				waitHandles = append(waitHandles, handle)
				if !run.completion.committed() || run.completionSourceRetained {
					references = append(references, scopeRef.CurrentNode)
				}
			}

			if err := drainTaskControllerWorkLocked(c, selector.TaskID, taskFence, &references, &admissionWaits, &launchRuns); err != nil {
				return err
			}
			if !selectedExact && len(selection.Finalizing) == 0 && len(launchRuns) != 0 {
				return nil
			}
			return nil
		})
		if errors.Is(err, sessionruntime.ErrExecutionNoLongerLive) {
			if selector.SessionID != nil {
				return ErrNoInterruptibleExecution
			}
			c.mu.Lock()
			defer c.mu.Unlock()
			if c.closed {
				return errors.New("current node workflow controller is closed")
			}
			if !taskHasInterruptibleLaunchingRunLocked(c, selector.TaskID) {
				return ErrNoInterruptibleExecution
			}
			if err := validateTaskControllerWorkLocked(c, selector.TaskID); err != nil {
				return err
			}
			taskFence, err = c.beginTaskInterruptLocked(selector.TaskID)
			if err != nil {
				return err
			}
			if err := drainTaskControllerWorkLocked(c, selector.TaskID, taskFence, &references, &admissionWaits, &launchRuns); err != nil {
				c.rollbackTaskInterruptLocked(taskFence)
				return err
			}
			return nil
		}
		if err != nil {
			return err
		}
		if err := c.persistUserInterrupt(ctx, references); err != nil {
			c.mu.Lock()
			if taskFence != nil {
				c.rollbackTaskInterruptLocked(taskFence)
			} else {
				c.rollbackSelectedInterruptLocked(selectedRuns)
			}
			c.mu.Unlock()
			return err
		}
		persisted = true
		return nil
	}); err != nil {
		return err
	}
	state := currentNodeInterruptCleanupState{
		taskID:         selector.TaskID,
		stopHandles:    stopHandles,
		waitHandles:    waitHandles,
		references:     references,
		selectedRuns:   selectedRuns,
		launchRuns:     launchRuns,
		admissionWaits: admissionWaits,
		taskFence:      taskFence,
		persisted:      persisted,
	}
	if persisted {
		c.mu.Lock()
		state.fatalDiagnostic = c.interruptCleanupDiagnosticLocked(state)
		c.mu.Unlock()
	}
	err := c.cleanupInterrupt(state)
	if err != nil && persisted {
		state.fatalDiagnostic.CleanupFailure = err
		result := c.lifecycleFatalReporter.ReportFatal(state.fatalDiagnostic)
		c.mu.Lock()
		if run := c.runs[currentNodeRunID{sequence: state.fatalDiagnostic.RunID}]; run != nil {
			run.recordCallbackError(fmt.Errorf(
				"%s shutdown_accepted=%t: %w",
				state.fatalDiagnostic.Error(),
				result.ShutdownAccepted,
				err,
			))
		}
		c.mu.Unlock()
	}
	return err
}

func (c *CurrentNodeController) interruptCleanupDiagnosticLocked(
	state currentNodeInterruptCleanupState,
) LifecycleFatalDiagnostic {
	runIDs := append([]currentNodeRunID(nil), state.selectedRuns...)
	runIDs = append(runIDs, state.launchRuns...)
	for _, wait := range state.admissionWaits {
		runIDs = append(runIDs, wait.runID)
	}
	for _, runID := range runIDs {
		if run := c.runs[runID]; run != nil {
			return lifecycleFatalDiagnosticForRun(
				run,
				LifecycleFatalOperationInterruptCleanup,
				errors.New("durable user interruption committed before bounded stop cleanup"),
				nil,
			)
		}
	}
	panic(fmt.Sprintf(
		"committed workflow interruption has no cleanup Run: task_id=%s references=%v selected_runs=%v launch_runs=%v",
		state.taskID,
		state.references,
		state.selectedRuns,
		state.launchRuns,
	))
}

func (c *CurrentNodeController) rollbackSelectedInterruptLocked(runIDs []currentNodeRunID) {
	for _, runID := range runIDs {
		run := c.runs[runID]
		if run == nil {
			continue
		}
		run.stop = currentNodeRunStopNone
		for _, successorID := range run.successors {
			if successor := c.runs[successorID]; successor != nil {
				successor.stop = currentNodeRunStopNone
			}
		}
	}
}

func (c *CurrentNodeController) persistUserInterrupt(
	ctx context.Context,
	references []workflow.CurrentNodeReference,
) error {
	if len(references) == 0 {
		return nil
	}
	detail := workflow.NewCurrentNodeInterruptionDetail(string(workflow.CurrentNodeInterruptionReasonUserInterrupt), nil)
	c.mu.Lock()
	targets, err := c.currentSchedulingTargetsLocked(references)
	c.mu.Unlock()
	if err != nil {
		return err
	}
	result, err := c.store.InterruptCurrentNodeSchedulingSet(
		ctx,
		references[0].TaskID,
		targets,
		workflow.CurrentNodeInterruptionReasonUserInterrupt,
		detail,
	)
	if result.NotificationError != nil {
		slog.Warn(
			"publish workflow Current-Node interruption event failed",
			"task_id", references[0].TaskID,
			"error", result.NotificationError,
		)
	}
	return err
}

func (c *CurrentNodeController) currentSchedulingTargetsLocked(
	references []workflow.CurrentNodeReference,
) ([]workflowstore.CurrentNodeSchedulingTarget, error) {
	if len(references) == 0 {
		return nil, errors.New("workflow interruption requires Current-Node references")
	}
	targets := make([]workflowstore.CurrentNodeSchedulingTarget, 0, len(references))
	seen := make(map[workflow.CurrentNodeReferenceKey]struct{}, len(references))
	for _, reference := range references {
		key, err := reference.Key()
		if err != nil {
			return nil, err
		}
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		run, exists := c.currentRunLocked(key)
		if !exists || !run.reference.Equal(reference) {
			return nil, fmt.Errorf("Current Node %v has no current Run generation", reference)
		}
		switch run.expectedScheduling {
		case workflow.CurrentNodeSchedulingReady, workflow.CurrentNodeSchedulingAdmitted:
		default:
			return nil, fmt.Errorf(
				"Current Node %v Run %d has invalid expected scheduling %q",
				reference,
				run.id.sequence,
				run.expectedScheduling,
			)
		}
		targets = append(targets, workflowstore.CurrentNodeSchedulingTarget{
			Reference: reference,
			Expected:  run.expectedScheduling,
		})
	}
	return targets, nil
}

func (c *CurrentNodeController) rollbackTaskInterruptLocked(fence *currentNodeInterruptFence) {
	if fence == nil {
		return
	}
	for _, run := range c.runs {
		if run.interruptFence != fence {
			continue
		}
		run.interruptFence = nil
		run.stop = currentNodeRunStopNone
		if run.phase == currentNodeRunQueued {
			c.queueRunLocked(run.id, run.assignmentSteer)
		}
	}
	c.finishInterruptFenceLocked(fence)
}

func taskHasInterruptibleLaunchingRunLocked(c *CurrentNodeController, taskID workflow.TaskID) bool {
	for _, run := range c.runs {
		if run.reference.TaskID == taskID && run.launching() && !run.stopping() && run.callbackErr == nil {
			return true
		}
	}
	return false
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
		launchRuns     []currentNodeRunID
		admissionWaits []currentNodeAdmissionWait
		taskFence      *currentNodeInterruptFence
	)
	if err := c.taskMutations.Run(ctx, taskID, func(ctx context.Context) error {
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
			if c.taskInterruptActiveLocked(taskID) {
				return ErrTaskExecutionNotQuiescent
			}

			for _, handle := range selection.Interruptible {
				scopeID := handle.Scope().ID()
				scopeRef, workflowScoped := handle.Scope().Workflow()
				if !workflowScoped || scopeRef.CurrentNode.TaskID != taskID {
					return errors.New("manual move interruption selection is not workflow scoped")
				}
				live, exists := c.runByScopeLocked(scopeID)
				if !exists || !live.reference.Equal(scopeRef.CurrentNode) {
					return errors.New("manual move interruption selection does not match controller ownership")
				}
			}
			for entry := c.automaticQueue.first; entry != nil; entry = entry.globalNext {
				run := c.runs[entry.runID]
				if run != nil && run.reference.TaskID == taskID {
					if _, err := run.reference.Key(); err != nil {
						return err
					}
				}
			}
			for _, id := range c.explicitQueue {
				run := c.runs[id]
				if run != nil && run.reference.TaskID == taskID {
					if _, err := run.reference.Key(); err != nil {
						return err
					}
				}
			}
			for _, run := range c.runs {
				if run.reference.TaskID == taskID {
					if err := run.reference.Validate(); err != nil {
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
				taskFence, err = c.beginTaskInterruptLocked(taskID)
				if err != nil {
					return err
				}
			}
			for _, handle := range selection.Interruptible {
				scopeID := handle.Scope().ID()
				scopeRef, _ := handle.Scope().Workflow()
				if taskFence != nil {
					run, exists := c.runByScopeLocked(scopeID)
					if !exists {
						return errors.New("manual move workflow execution has no Run generation")
					}
					c.fenceRunLocked(run, taskFence)
				}
				stopHandles = append(stopHandles, handle)
				waitHandles = append(waitHandles, handle)
				references = append(references, scopeRef.CurrentNode)
			}
			if taskFence != nil {
				if err := drainTaskControllerWorkLocked(c, taskID, taskFence, &references, &admissionWaits, &launchRuns); err != nil {
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
		taskID:         taskID,
		stopHandles:    stopHandles,
		waitHandles:    waitHandles,
		references:     references,
		launchRuns:     launchRuns,
		admissionWaits: admissionWaits,
		taskFence:      taskFence,
	})
}

func appendAdmissionWait(
	waits []currentNodeAdmissionWait,
	runID currentNodeRunID,
	wait <-chan struct{},
) []currentNodeAdmissionWait {
	return append(waits, currentNodeAdmissionWait{runID: runID, done: wait})
}

func taskHasControllerQueuedWorkLocked(c *CurrentNodeController, taskID workflow.TaskID) bool {
	for _, run := range c.runs {
		if run.reference.TaskID == taskID &&
			(run.phase == currentNodeRunQueued ||
				run.phase == currentNodeRunHeld ||
				run.phase == currentNodeRunLaunching) {
			return true
		}
	}
	return false
}

func validateTaskControllerWorkLocked(c *CurrentNodeController, taskID workflow.TaskID) error {
	for _, run := range c.runs {
		if run.reference.TaskID == taskID {
			if err := run.reference.Validate(); err != nil {
				return fmt.Errorf("validate Run generation for task %s: %w", taskID, err)
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
	launchRuns *[]currentNodeRunID,
) error {
	explicitQueue := c.explicitQueue[:0]
	for _, runID := range c.explicitQueue {
		run := c.runs[runID]
		if run == nil {
			continue
		}
		if run.reference.TaskID != taskID {
			explicitQueue = append(explicitQueue, runID)
			continue
		}
		c.fenceRunLocked(run, fence)
		*references = append(*references, run.reference)
		*admissionWaits = appendAdmissionWait(*admissionWaits, runID, nil)
	}
	c.explicitQueue = explicitQueue

	for entry := c.automaticQueue.first; entry != nil; {
		next := entry.globalNext
		run := c.runs[entry.runID]
		if run == nil {
			c.automaticQueue.remove(entry)
			entry = next
			continue
		}
		if run.reference.TaskID != taskID {
			entry = next
			continue
		}
		runID := c.automaticQueue.remove(entry)
		c.fenceRunLocked(run, fence)
		*references = append(*references, run.reference)
		*admissionWaits = appendAdmissionWait(*admissionWaits, runID, nil)
		entry = next
	}

	for runID, run := range c.runs {
		if run.reference.TaskID == taskID && run.phase == currentNodeRunHeld {
			c.fenceRunLocked(run, fence)
			*references = append(*references, run.reference)
			*admissionWaits = appendAdmissionWait(*admissionWaits, runID, nil)
		}
	}

	for runID, run := range c.runs {
		if run.reference.TaskID != taskID || !run.launching() {
			continue
		}
		c.fenceRunLocked(run, fence)
		*launchRuns = append(*launchRuns, runID)
		*references = append(*references, run.reference)
		*admissionWaits = appendAdmissionWait(*admissionWaits, runID, run.admissionDone)
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

func (c *CurrentNodeController) finishTaskInterruptAdmission(runID currentNodeRunID) {
	c.finishTaskInterruptAdmissionRun(runID)
}

func (c *CurrentNodeController) finishTaskInterruptAdmissionRun(runID currentNodeRunID) {
	c.mu.Lock()
	defer c.mu.Unlock()
	run := c.runs[runID]
	if run == nil || run.interruptFence == nil {
		return
	}
	c.removeRunLocked(runID)
}
