package workflowexecution

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"core/server/workflow"
)

type TaskPreparationFinalizationKind string

const (
	TaskPreparationHandedOff          TaskPreparationFinalizationKind = "handed_off"
	TaskPreparationFailed             TaskPreparationFinalizationKind = "preparation_failed"
	TaskPreparationInterruptionFailed TaskPreparationFinalizationKind = "interruption_persistence_failed"
	TaskPreparationCanceled           TaskPreparationFinalizationKind = "canceled"
	TaskPreparationControllerShutDown TaskPreparationFinalizationKind = "controller_shutdown"
)

type TaskPreparationFinalization struct {
	Kind  TaskPreparationFinalizationKind
	Cause error
}

type TaskPreparationFinalizer func(TaskPreparationFinalization)

type taskPreparationBatch struct {
	taskID      workflow.TaskID
	starts      []currentNodeQueuedStart
	preparation TaskStartPreparation
	finalizer   TaskPreparationFinalizer
	ctx         context.Context
	cancel      context.CancelCauseFunc
	done        chan struct{}
}

func newTaskPreparationBatch(
	parent context.Context,
	taskID workflow.TaskID,
	starts []currentNodeQueuedStart,
	preparation TaskStartPreparation,
	finalizer TaskPreparationFinalizer,
) (*taskPreparationBatch, error) {
	if parent == nil {
		return nil, errors.New("task preparation parent context is required")
	}
	if err := preparation.validate(); err != nil {
		return nil, err
	}
	if finalizer == nil {
		return nil, errors.New("task preparation finalizer is required")
	}
	if len(starts) == 0 {
		return nil, errors.New("task preparation requires at least one explicit Current Node start")
	}
	ordered := append([]currentNodeQueuedStart(nil), starts...)
	sort.Slice(ordered, func(left, right int) bool {
		leftReference := ordered[left].reference
		rightReference := ordered[right].reference
		if leftReference.NodeID != rightReference.NodeID {
			return leftReference.NodeID < rightReference.NodeID
		}
		leftBranch, leftScoped := leftReference.TransitionBranchKey()
		rightBranch, rightScoped := rightReference.TransitionBranchKey()
		if leftScoped != rightScoped {
			return !leftScoped
		}
		return leftScoped && leftBranch < rightBranch
	})
	seen := make(map[workflow.CurrentNodeReferenceKey]struct{}, len(ordered))
	for index, start := range ordered {
		if start.policy != currentNodeAdmissionExplicitOverride {
			return nil, fmt.Errorf("task preparation Current Node at index %d is not an explicit start", index)
		}
		if start.reference.TaskID != taskID {
			return nil, fmt.Errorf("task preparation Current Node at index %d belongs to another Task", index)
		}
		key, err := start.reference.Key()
		if err != nil {
			return nil, fmt.Errorf("task preparation Current Node at index %d: %w", index, err)
		}
		if _, duplicate := seen[key]; duplicate {
			return nil, fmt.Errorf("task preparation Current Node at index %d is duplicated", index)
		}
		seen[key] = struct{}{}
	}
	ctx, cancel := context.WithCancelCause(parent)
	return &taskPreparationBatch{
		taskID:      taskID,
		starts:      ordered,
		preparation: preparation,
		finalizer:   finalizer,
		ctx:         ctx,
		cancel:      cancel,
		done:        make(chan struct{}),
	}, nil
}

func (c *CurrentNodeController) queueTaskPreparationBatchLocked(batch *taskPreparationBatch) error {
	if batch == nil {
		return errors.New("task preparation batch is required")
	}
	if c.queuedTaskPreparationLocked(batch.taskID) != nil || c.runningTaskPreparationLocked(batch.taskID) != nil {
		return fmt.Errorf("task %q already has a preparation batch", batch.taskID)
	}
	for _, start := range batch.starts {
		key, err := start.reference.Key()
		if err != nil {
			return err
		}
		if c.currentNodeOwnedLocked(key) {
			return fmt.Errorf("current node %v is already owned", start.reference)
		}
	}
	c.preparationQueue = append(c.preparationQueue, batch)
	c.wakeAdmissionWorker()
	return nil
}

func (c *CurrentNodeController) takeTaskPreparationBatch() (*taskPreparationBatch, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed ||
		len(c.preparationQueue) == 0 ||
		c.inFlightAdmissionCountLocked(currentNodeAdmissionExplicitOverride)+len(c.preparationRunning) >= explicitAdmissionConcurrency {
		return nil, false
	}
	batch := c.preparationQueue[0]
	c.preparationQueue = c.preparationQueue[1:]
	c.preparationRunning = append(c.preparationRunning, batch)
	c.preparationWG.Add(1)
	return batch, true
}

func (c *CurrentNodeController) runTaskPreparationBatch(batch *taskPreparationBatch) {
	defer c.preparationWG.Done()
	defer close(batch.done)
	if err := batch.preparation.Prepare(batch.ctx); err != nil {
		if batch.ctx.Err() != nil {
			c.finishCanceledTaskPreparationBatch(batch)
			return
		}
		c.finishFailedTaskPreparationBatch(batch, err)
		return
	}
	c.finishPreparedTaskPreparationBatch(batch)
}

func (c *CurrentNodeController) finishPreparedTaskPreparationBatch(batch *taskPreparationBatch) {
	cleanupCtx, cancel := context.WithTimeout(context.Background(), interruptCleanupTimeout)
	defer cancel()
	var (
		interrupted            []workflow.CurrentNodeReference
		interruptionDiagnostic error
		commitErr              error
		canceled               bool
	)
	persistenceErr := c.runInternalTaskMutation(cleanupCtx, batch.taskID, func(ctx context.Context) error {
		if batch.ctx.Err() != nil {
			c.mu.Lock()
			if c.runningTaskPreparationLocked(batch.taskID) == batch {
				c.removeRunningTaskPreparationLocked(batch)
			}
			c.mu.Unlock()
			canceled = true
			return nil
		}
		if commitErr = batch.preparation.Commit(ctx); commitErr != nil {
			var persistenceErr error
			interrupted, interruptionDiagnostic, persistenceErr =
				c.persistAndRetireFailedTaskPreparationBatch(ctx, batch, commitErr)
			return persistenceErr
		}
		if handoffErr := c.handoffTaskPreparationBatch(batch); handoffErr != nil {
			commitErr = handoffErr
			var persistenceErr error
			interrupted, interruptionDiagnostic, persistenceErr =
				c.persistAndRetireFailedTaskPreparationBatch(ctx, batch, commitErr)
			return persistenceErr
		}
		return nil
	})
	if canceled {
		c.finalizeCanceledTaskPreparationBatch(batch, persistenceErr)
		return
	}
	if commitErr != nil || persistenceErr != nil {
		c.publishFailedTaskPreparationBatch(
			batch,
			errors.Join(commitErr, interruptionDiagnostic),
			persistenceErr,
			interrupted,
		)
		return
	}
	batch.finalizer(TaskPreparationFinalization{Kind: TaskPreparationHandedOff})
	c.wakeAdmissionWorker()
}

func (c *CurrentNodeController) handoffTaskPreparationBatch(batch *taskPreparationBatch) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return errors.New("current node workflow controller is closed")
	}
	if c.runningTaskPreparationLocked(batch.taskID) != batch {
		return errors.New("task preparation batch is no longer the active owner")
	}
	for _, start := range batch.starts {
		key, err := start.reference.Key()
		if err != nil {
			return err
		}
		if _, queued := c.explicitQueued[key]; queued {
			return fmt.Errorf("current node %v is already explicitly queued", start.reference)
		}
		if _, reserved := c.explicitReservations[key]; reserved {
			return fmt.Errorf("current node %v already has an explicit reservation", start.reference)
		}
		if _, queued := c.queued[key]; queued {
			return fmt.Errorf("current node %v is already automatically queued", start.reference)
		}
		if _, reserved := c.automaticReservations[key]; reserved {
			return fmt.Errorf("current node %v already has an automatic reservation", start.reference)
		}
		if _, gated := c.gates[key]; gated {
			return fmt.Errorf("current node %v already has an admission gate", start.reference)
		}
		if _, live := c.liveByNode[key]; live {
			return fmt.Errorf("current node %v already has a live execution scope", start.reference)
		}
	}
	c.removeRunningTaskPreparationLocked(batch)
	for _, start := range batch.starts {
		key, _ := start.reference.Key()
		c.explicitQueue = append(c.explicitQueue, start)
		c.explicitQueued[key] = struct{}{}
	}
	c.wakeAdmissionWorker()
	return nil
}

func (c *CurrentNodeController) finishFailedTaskPreparationBatch(batch *taskPreparationBatch, cause error) {
	cleanupCtx, cancel := context.WithTimeout(context.Background(), interruptCleanupTimeout)
	defer cancel()
	var (
		interrupted            []workflow.CurrentNodeReference
		interruptionDiagnostic error
		canceled               bool
	)
	persistenceErr := c.runInternalTaskMutation(cleanupCtx, batch.taskID, func(ctx context.Context) error {
		if batch.ctx.Err() != nil {
			c.mu.Lock()
			if c.runningTaskPreparationLocked(batch.taskID) == batch {
				c.removeRunningTaskPreparationLocked(batch)
			}
			c.mu.Unlock()
			canceled = true
			return nil
		}
		var persistenceErr error
		interrupted, interruptionDiagnostic, persistenceErr =
			c.persistAndRetireFailedTaskPreparationBatch(ctx, batch, cause)
		return persistenceErr
	})
	if canceled {
		c.finalizeCanceledTaskPreparationBatch(batch, persistenceErr)
		return
	}
	c.publishFailedTaskPreparationBatch(
		batch,
		errors.Join(cause, interruptionDiagnostic),
		persistenceErr,
		interrupted,
	)
}

func (c *CurrentNodeController) persistAndRetireFailedTaskPreparationBatch(
	ctx context.Context,
	batch *taskPreparationBatch,
	cause error,
) ([]workflow.CurrentNodeReference, error, error) {
	c.mu.Lock()
	active := c.runningTaskPreparationLocked(batch.taskID) == batch
	c.mu.Unlock()
	if !active {
		return nil, nil, errors.New("failed task preparation batch is no longer the active owner")
	}
	detail := workflow.NewCurrentNodeInterruptionDetail(string(reasonCurrentNodeRuntimeStartFailed), cause)
	var preparationErr *TaskStartPreparationError
	canonicalDetail := detail
	if errors.As(cause, &preparationErr) {
		canonicalDetail = preparationErr.InterruptionDetail()
	}
	interrupted := make([]workflow.CurrentNodeReference, 0, len(batch.starts))
	var interruptionDiagnostic error
	var persistenceErr error
	for _, start := range batch.starts[1:] {
		committed, diagnostic := classifyCurrentNodeInterruption(c.store.InterruptCurrentNode(
			ctx,
			start.reference,
			reasonCurrentNodeRuntimeStartFailed,
			detail,
		))
		if diagnostic != nil {
			diagnostic = fmt.Errorf("interrupt task preparation sibling %v: %w", start.reference, diagnostic)
		}
		if !committed {
			persistenceErr = diagnostic
			break
		}
		interrupted = append(interrupted, start.reference)
		interruptionDiagnostic = errors.Join(interruptionDiagnostic, diagnostic)
	}
	if persistenceErr == nil {
		canonical := batch.starts[0].reference
		committed, diagnostic := classifyCurrentNodeInterruption(c.store.InterruptCurrentNode(
			ctx,
			canonical,
			reasonCurrentNodeRuntimeStartFailed,
			canonicalDetail,
		))
		if diagnostic != nil {
			diagnostic = fmt.Errorf("interrupt canonical task preparation Current Node %v: %w", canonical, diagnostic)
		}
		if !committed {
			persistenceErr = diagnostic
		} else {
			interrupted = append([]workflow.CurrentNodeReference{canonical}, interrupted...)
			interruptionDiagnostic = errors.Join(interruptionDiagnostic, diagnostic)
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.runningTaskPreparationLocked(batch.taskID) != batch {
		return interrupted, interruptionDiagnostic, errors.Join(persistenceErr, errors.New("failed task preparation ownership changed before retirement"))
	}
	c.removeRunningTaskPreparationLocked(batch)
	return interrupted, interruptionDiagnostic, persistenceErr
}

func (c *CurrentNodeController) publishFailedTaskPreparationBatch(
	batch *taskPreparationBatch,
	cause error,
	persistenceErr error,
	interrupted []workflow.CurrentNodeReference,
) {
	finalization := TaskPreparationFinalization{Kind: TaskPreparationFailed, Cause: cause}
	if persistenceErr != nil {
		finalization = TaskPreparationFinalization{
			Kind:  TaskPreparationInterruptionFailed,
			Cause: errors.Join(cause, persistenceErr),
		}
		c.mu.Lock()
		c.workerErr = errors.Join(c.workerErr, persistenceErr)
		c.mu.Unlock()
	}
	for _, reference := range interrupted {
		c.publishPendingInterruptedCurrentNode(context.Background(), reference, reasonCurrentNodeRuntimeStartFailed)
	}
	batch.finalizer(finalization)
	c.wakeAdmissionWorker()
}

func (c *CurrentNodeController) finishCanceledTaskPreparationBatch(batch *taskPreparationBatch) {
	cleanupCtx, cancel := context.WithTimeout(context.Background(), interruptCleanupTimeout)
	defer cancel()
	retireErr := c.runInternalTaskMutation(cleanupCtx, batch.taskID, func(context.Context) error {
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.runningTaskPreparationLocked(batch.taskID) != batch {
			return nil
		}
		c.removeRunningTaskPreparationLocked(batch)
		return nil
	})
	c.finalizeCanceledTaskPreparationBatch(batch, retireErr)
}

func (c *CurrentNodeController) finalizeCanceledTaskPreparationBatch(
	batch *taskPreparationBatch,
	retireErr error,
) {
	kind := TaskPreparationCanceled
	c.mu.Lock()
	if c.closed {
		kind = TaskPreparationControllerShutDown
	}
	c.mu.Unlock()
	cause := context.Cause(batch.ctx)
	if cause == nil {
		cause = errors.New("task preparation was canceled")
	}
	batch.finalizer(TaskPreparationFinalization{Kind: kind, Cause: errors.Join(cause, retireErr)})
	c.wakeAdmissionWorker()
}

func taskPreparationReferences(batch *taskPreparationBatch) []workflow.CurrentNodeReference {
	if batch == nil {
		return nil
	}
	references := make([]workflow.CurrentNodeReference, 0, len(batch.starts))
	for _, start := range batch.starts {
		references = append(references, start.reference)
	}
	return references
}

func (c *CurrentNodeController) queuedTaskPreparationLocked(taskID workflow.TaskID) *taskPreparationBatch {
	for _, batch := range c.preparationQueue {
		if batch.taskID == taskID {
			return batch
		}
	}
	return nil
}

func (c *CurrentNodeController) runningTaskPreparationLocked(taskID workflow.TaskID) *taskPreparationBatch {
	for _, batch := range c.preparationRunning {
		if batch.taskID == taskID {
			return batch
		}
	}
	return nil
}

func (c *CurrentNodeController) removeRunningTaskPreparationLocked(target *taskPreparationBatch) {
	for index, batch := range c.preparationRunning {
		if batch != target {
			continue
		}
		c.preparationRunning = append(c.preparationRunning[:index], c.preparationRunning[index+1:]...)
		return
	}
	panic("running task preparation owner is absent")
}

func closeQueuedTaskPreparationBatch(batch *taskPreparationBatch, cause error) {
	if batch == nil {
		return
	}
	batch.cancel(cause)
	close(batch.done)
}

func preparationShutdownCause() error {
	return errors.New("current node workflow controller shut down during Task preparation")
}

func preparationCancellationCause() error {
	return errors.New("Task preparation canceled by lifecycle mutation")
}
