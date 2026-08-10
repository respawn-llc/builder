package workflowexecution

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"core/server/session"
	"core/server/sessionruntime"
	"core/server/workflow"
	"core/server/workflowruntime"
	"core/server/workflowstore"
	"core/shared/runtimeids"
)

const (
	explicitAdmissionConcurrency                                               = 8
	reasonCurrentNodeRuntimeStartFailed workflow.CurrentNodeInterruptionReason = "workflow_runtime_start_failed"
)

type currentNodeAdmissionPolicy uint8

const (
	currentNodeAdmissionExplicitOverride currentNodeAdmissionPolicy = iota
	currentNodeAdmissionAutomaticAgent
	currentNodeAdmissionAutomaticScript
)

func (p currentNodeAdmissionPolicy) isAutomatic() bool {
	return p == currentNodeAdmissionAutomaticAgent || p == currentNodeAdmissionAutomaticScript
}

func (p currentNodeAdmissionPolicy) countsAgentCapacity() bool {
	return p == currentNodeAdmissionAutomaticAgent
}

func (p currentNodeAdmissionPolicy) nodeKind() workflow.NodeKind {
	switch p {
	case currentNodeAdmissionAutomaticAgent:
		return workflow.NodeKindAgent
	case currentNodeAdmissionAutomaticScript:
		return workflow.NodeKindScript
	default:
		panic(fmt.Sprintf("admission policy %d has no executable Node kind", p))
	}
}

type currentNodeQueuedStart struct {
	reference          workflow.CurrentNodeReference
	preparation        TaskStartPreparation
	taskPromptDelivery workflowruntime.TaskPromptDelivery
	assignmentSteer    CurrentNodeAssignmentSteer
	policy             currentNodeAdmissionPolicy
}

type currentNodeAdmissionError struct {
	cause    error
	admitted bool
}

type TaskStartPreparationError struct {
	cause  error
	detail workflow.CurrentNodeInterruptionDetail
}

func NewTaskStartPreparationError(
	cause error,
	detail workflow.CurrentNodeInterruptionDetail,
) *TaskStartPreparationError {
	if cause == nil {
		panic("task start preparation error requires a cause")
	}
	return &TaskStartPreparationError{cause: cause, detail: detail}
}

func (e *TaskStartPreparationError) Error() string {
	return e.cause.Error()
}

func (e *TaskStartPreparationError) Unwrap() error {
	return e.cause
}

func (e *TaskStartPreparationError) InterruptionDetail() workflow.CurrentNodeInterruptionDetail {
	return e.detail
}

type pendingCurrentNodeAssignmentSteer struct {
	ready chan struct{}
	once  sync.Once
	steer CurrentNodeAssignmentSteer
	err   error
}

func newPendingCurrentNodeAssignmentSteer() *pendingCurrentNodeAssignmentSteer {
	return &pendingCurrentNodeAssignmentSteer{ready: make(chan struct{})}
}

func (s *pendingCurrentNodeAssignmentSteer) resolve(steer CurrentNodeAssignmentSteer, err error) {
	s.once.Do(func() {
		s.steer = steer
		s.err = err
		close(s.ready)
	})
}

func (s *pendingCurrentNodeAssignmentSteer) Wait(ctx context.Context) (session.CommitReceipt, error) {
	steer, err := s.resolved(ctx)
	if err != nil {
		return session.CommitReceipt{}, err
	}
	return steer.Wait(ctx)
}

func (s *pendingCurrentNodeAssignmentSteer) resolved(ctx context.Context) (CurrentNodeAssignmentSteer, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-s.ready:
	case <-ctx.Done():
		return nil, context.Cause(ctx)
	}
	if s.err != nil {
		return nil, s.err
	}
	if s.steer == nil {
		return nil, errors.New("resolved current node assignment steer is absent")
	}
	return s.steer, nil
}

func resolvedCurrentNodeAssignmentSteer(ctx context.Context, steer CurrentNodeAssignmentSteer) (CurrentNodeAssignmentSteer, error) {
	if steer == nil {
		return nil, nil
	}
	if pending, ok := steer.(*pendingCurrentNodeAssignmentSteer); ok {
		var err error
		steer, err = pending.resolved(ctx)
		if err != nil {
			return nil, err
		}
	}
	receipt, err := steer.Wait(ctx)
	if err != nil {
		return nil, err
	}
	if !receipt.Committed {
		return nil, errors.New("current node assignment was not committed")
	}
	return steer, nil
}

func (e currentNodeAdmissionError) Error() string {
	return e.cause.Error()
}

func (e currentNodeAdmissionError) Unwrap() error {
	return e.cause
}

func (c *CurrentNodeController) admit(runID currentNodeRunID) (err error) {
	if c == nil {
		return errors.New("current node workflow controller is required")
	}
	c.mu.Lock()
	run := c.runs[runID]
	if run == nil || !run.launching() {
		c.mu.Unlock()
		return sessionruntime.ErrExecutionNoLongerLive
	}
	reference := run.reference
	ctx := run.launchContext
	preparation := run.preparation
	delivery := run.taskPromptDelivery
	assignmentSteer := run.assignmentSteer
	policy := run.policy
	c.mu.Unlock()
	if preparation != nil {
		if err := preparation(ctx); err != nil {
			return err
		}
		if delivery == workflowruntime.TaskPromptDeliveryAssignment {
			assignment, err := c.steerAssignment(ctx, reference)
			if err != nil {
				return err
			}
			assignmentSteer = assignment
		}
	}
	assignmentSteer, err = resolvedCurrentNodeAssignmentSteer(ctx, assignmentSteer)
	if err != nil {
		return err
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return errors.New("current node workflow controller is closed")
	}
	c.mu.Unlock()

	var lease sessionruntime.WorkflowExecutionLease
retryAdmission:
	if err := c.taskMutations.Run(ctx, reference.TaskID, func(ctx context.Context) error {
		c.mu.Lock()
		if err := c.ensureTaskAvailableLocked(reference.TaskID); err != nil {
			c.mu.Unlock()
			return err
		}
		run := c.runs[runID]
		if run == nil || !run.launching() || c.currentRuns[run.key] != runID {
			c.mu.Unlock()
			return sessionruntime.ErrExecutionNoLongerLive
		}
		c.mu.Unlock()
		scope, err := c.store.TaskExecutionScope(ctx, reference.TaskID)
		if err != nil {
			return err
		}
		next, err := c.authority.NewWorkflowExecutionLease(sessionruntime.WorkflowExecutionRef{
			ProjectID: scope.ProjectID, WorkflowID: scope.WorkflowID, CurrentNode: reference,
		})
		if err != nil {
			return err
		}
		c.mu.Lock()
		if c.closed {
			c.mu.Unlock()
			next.Cancel()
			return errors.New("current node workflow controller is closed")
		}
		run = c.runs[runID]
		if run == nil || !run.launching() || run.stopping() || c.currentRuns[run.key] != runID {
			c.mu.Unlock()
			next.Cancel()
			return sessionruntime.ErrExecutionNoLongerLive
		}
		run.lease = &next
		c.mu.Unlock()

		if err := c.store.AdmitCurrentNode(ctx, reference); err != nil {
			c.cancelLaunchingLease(runID, next.ScopeID())
			return err
		}
		c.mu.Lock()
		run = c.runs[runID]
		if run == nil || !run.launching() || run.lease == nil || run.lease.ScopeID() != next.ScopeID() {
			c.mu.Unlock()
			next.Cancel()
			return sessionruntime.ErrExecutionNoLongerLive
		}
		run.expectedScheduling = workflow.CurrentNodeSchedulingAdmitted
		c.mu.Unlock()
		lease = next
		return nil
	}); err != nil {
		if !errors.Is(err, ErrTaskExecutionNotQuiescent) {
			return currentNodeAdmissionError{cause: err}
		}
		c.mu.Lock()
		fence := c.taskInterruptFenceLocked(reference.TaskID)
		selected := run.interruptFence != nil
		c.mu.Unlock()
		if fence == nil || selected {
			return currentNodeAdmissionError{cause: err}
		}
		select {
		case <-fence.done:
		case <-ctx.Done():
			return currentNodeAdmissionError{cause: context.Cause(ctx)}
		}
		goto retryAdmission
	}
	if err := c.runner.StartCurrentNode(ctx, reference, delivery, assignmentSteer, lease, c); err != nil {
		return currentNodeAdmissionError{
			cause:    c.discardAdmission(runID, lease, err),
			admitted: true,
		}
	}
	if err := c.taskMutations.Run(ctx, reference.TaskID, func(context.Context) error {
		handle, ok := c.authority.ExecutionByScope(lease.ScopeID())
		if !ok {
			return errors.New("current node runner returned without its exact live scope")
		}
		scopeRef, ok := handle.Scope().Workflow()
		if !ok || !scopeRef.CurrentNode.Equal(reference) {
			return errors.New("current node runner started a mismatched workflow scope")
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		run := c.runs[runID]
		if run == nil ||
			!run.launching() ||
			run.lease == nil ||
			run.lease.ScopeID() != lease.ScopeID() ||
			c.currentRuns[run.key] != runID {
			return errors.New("current node Run was replaced before exact scope registration")
		}
		if err := c.ensureTaskAvailableLocked(reference.TaskID); err != nil {
			return err
		}
		if run.stopping() {
			return sessionruntime.ErrExecutionNoLongerLive
		}
		scopeID := lease.ScopeID()
		run.exactScopeID = &scopeID
		c.exactRuns[scopeID] = runID
		run.transition(currentNodeRunExact)
		lease.Release()
		return nil
	}); err != nil {
		return currentNodeAdmissionError{
			cause:    c.discardAdmission(runID, lease, err),
			admitted: true,
		}
	}
	if !policy.isAutomatic() {
		c.wakeAdmissionWorker()
	}
	return nil
}

func (c *CurrentNodeController) cancelLaunchingLease(runID currentNodeRunID, scopeID runtimeids.ExecutionScopeID) {
	c.mu.Lock()
	run := c.runs[runID]
	if run != nil && run.launching() && run.lease != nil && run.lease.ScopeID() == scopeID {
		run.lease = nil
	}
	c.mu.Unlock()
}

func (c *CurrentNodeController) discardAdmission(runID currentNodeRunID, lease sessionruntime.WorkflowExecutionLease, cause error) error {
	c.mu.Lock()
	run := c.runs[runID]
	if run != nil {
		c.removeRunLocked(runID)
	}
	c.mu.Unlock()
	lease.Cancel()
	return cause
}

func (c *CurrentNodeController) enqueueAutomaticIntents(intents []CurrentNodeAutomaticIntent) {
	c.enqueueStarts(automaticQueuedStarts(intents))
}

func (c *CurrentNodeController) steerStartsAssignments(ctx context.Context, starts []currentNodeQueuedStart) ([]currentNodeQueuedStart, error) {
	steered := append([]currentNodeQueuedStart(nil), starts...)
	for index := range steered {
		assignment, err := c.steerAssignment(ctx, steered[index].reference)
		if err != nil {
			return steered[:index], err
		}
		steered[index].assignmentSteer = assignment
	}
	return steered, nil
}

func (c *CurrentNodeController) steerAndWaitStarts(
	ctx context.Context,
	starts []currentNodeQueuedStart,
	recovery currentNodeStartFailureRecovery,
) ([]currentNodeQueuedStart, error) {
	steered, steerErr := c.steerStartsAssignments(ctx, starts)
	outcome := waitCurrentNodeAssignmentSteers(ctx, steered)
	cause := errors.Join(steerErr, outcome.err)
	if cause != nil {
		if len(outcome.pending) != 0 {
			c.continueCurrentNodeAssignmentStarts(steered, steerErr)
			return nil, cause
		}
		recoveryStarts := outcome.committed
		if recovery == recoverAllCurrentNodeStarts {
			recoveryStarts = starts
		}
		return nil, errors.Join(cause, c.recoverCurrentNodeStartFailures(ctx, recoveryStarts, false, cause))
	}
	return steered, nil
}

type currentNodeStartFailureRecovery uint8

const (
	recoverCommittedCurrentNodeStarts currentNodeStartFailureRecovery = iota
	recoverAllCurrentNodeStarts
)

func pendingCurrentNodeAssignmentStarts(starts []currentNodeQueuedStart) ([]currentNodeQueuedStart, []*pendingCurrentNodeAssignmentSteer) {
	pendingStarts := append([]currentNodeQueuedStart(nil), starts...)
	pending := make([]*pendingCurrentNodeAssignmentSteer, len(pendingStarts))
	for index := range pendingStarts {
		pending[index] = newPendingCurrentNodeAssignmentSteer()
		pendingStarts[index].assignmentSteer = pending[index]
	}
	return pendingStarts, pending
}

func (c *CurrentNodeController) resolvePendingCurrentNodeAssignmentSteers(
	ctx context.Context,
	starts []currentNodeQueuedStart,
	pending []*pendingCurrentNodeAssignmentSteer,
) error {
	if len(pending) == 0 {
		return nil
	}
	if len(pending) != len(starts) {
		return fmt.Errorf(
			"pending current node assignment count %d does not match start count %d",
			len(pending),
			len(starts),
		)
	}
	for index, start := range starts {
		assignment, err := c.steerAssignment(ctx, start.reference)
		pending[index].resolve(assignment, err)
		if err != nil {
			for _, unresolved := range pending[index+1:] {
				unresolved.resolve(nil, err)
			}
			return err
		}
	}
	return nil
}

func (c *CurrentNodeController) steerAssignment(
	ctx context.Context,
	reference workflow.CurrentNodeReference,
) (CurrentNodeAssignmentSteer, error) {
	assignment, err := c.steerer.SteerCurrentNodeAssignment(ctx, reference)
	if err != nil {
		return nil, fmt.Errorf("steer current node assignment %v: %w", reference, err)
	}
	if assignment == nil {
		return nil, fmt.Errorf("steer current node assignment %v returned no completion", reference)
	}
	return assignment, nil
}

type currentNodeAssignmentWaitOutcome struct {
	committed []currentNodeQueuedStart
	pending   []currentNodeQueuedStart
	err       error
}

func waitCurrentNodeAssignmentSteers(
	ctx context.Context,
	starts []currentNodeQueuedStart,
) currentNodeAssignmentWaitOutcome {
	outcome := currentNodeAssignmentWaitOutcome{
		committed: make([]currentNodeQueuedStart, 0, len(starts)),
		pending:   make([]currentNodeQueuedStart, 0, len(starts)),
	}
	for _, start := range starts {
		if start.assignmentSteer == nil {
			outcome.err = errors.Join(outcome.err, fmt.Errorf(
				"current node assignment %v has no steer completion",
				start.reference,
			))
			continue
		}
		receipt, err := start.assignmentSteer.Wait(ctx)
		if receipt.Committed {
			outcome.committed = append(outcome.committed, start)
		}
		if err != nil {
			if cause := context.Cause(ctx); !receipt.Committed && cause != nil && errors.Is(err, cause) {
				outcome.pending = append(outcome.pending, start)
			}
			outcome.err = errors.Join(outcome.err, fmt.Errorf(
				"wait for current node assignment %v: %w",
				start.reference,
				err,
			))
			continue
		}
		if !receipt.Committed {
			outcome.err = errors.Join(outcome.err, fmt.Errorf(
				"current node assignment %v was not committed",
				start.reference,
			))
		}
	}
	return outcome
}

func (c *CurrentNodeController) continueCurrentNodeAssignmentStarts(
	starts []currentNodeQueuedStart,
	priorErr error,
) {
	if len(starts) == 0 {
		return
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.workerWG.Add(1)
	c.mu.Unlock()
	go func() {
		defer c.workerWG.Done()
		outcome := waitCurrentNodeAssignmentSteers(c.workerContext, starts)
		if context.Cause(c.workerContext) != nil {
			return
		}
		cause := errors.Join(priorErr, outcome.err)
		if cause != nil {
			c.handleCurrentNodeStartFailures(outcome.committed, false, cause)
			return
		}
		c.enqueueStarts(starts)
	}()
}

func (c *CurrentNodeController) enqueueStarts(starts []currentNodeQueuedStart) {
	if len(starts) == 0 || c == nil {
		return
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	for _, start := range starts {
		var err error
		if start.policy.isAutomatic() {
			err = c.queueAutomaticStartLocked(start)
		} else {
			err = c.queueExplicitStartLocked(start)
		}
		if err != nil {
			panic(fmt.Sprintf("queue current node Run: %v", err))
		}
	}
	c.mu.Unlock()
	c.wakeAdmissionWorker()
}

func (c *CurrentNodeController) queueExplicitStartLocked(start currentNodeQueuedStart) error {
	if start.policy != currentNodeAdmissionExplicitOverride {
		return errors.New("explicit current node start cannot be automatic")
	}
	run, created, err := c.allocateRunLocked(start)
	if err != nil {
		return err
	}
	if !created {
		if run.phase == currentNodeRunHeld {
			run.preparation = start.preparation
			run.taskPromptDelivery = start.taskPromptDelivery
			run.assignmentSteer = start.assignmentSteer
			run.transition(currentNodeRunQueued)
			c.explicitQueue = append(c.explicitQueue, run.id)
		}
		return nil
	}
	if err := c.activateRunLocked(run, currentNodeRunQueued); err != nil {
		c.removeRunLocked(run.id)
		return err
	}
	c.explicitQueue = append(c.explicitQueue, run.id)
	c.wakeAdmissionWorker()
	return nil
}

func (c *CurrentNodeController) queueAutomaticStartLocked(start currentNodeQueuedStart) error {
	if !start.policy.isAutomatic() {
		return errors.New("automatic current node start requires an automatic admission policy")
	}
	run, created, err := c.allocateRunLocked(start)
	if err != nil {
		return err
	}
	if !created {
		if run.phase == currentNodeRunHeld {
			run.preparation = start.preparation
			run.taskPromptDelivery = start.taskPromptDelivery
			run.assignmentSteer = start.assignmentSteer
			run.transition(currentNodeRunQueued)
			c.automaticQueue.append(run)
		}
		return nil
	}
	if err := c.activateRunLocked(run, currentNodeRunQueued); err != nil {
		c.removeRunLocked(run.id)
		return err
	}
	c.automaticQueue.append(run)
	c.wakeAdmissionWorker()
	return nil
}

func (c *CurrentNodeController) currentNodeOwnedLocked(key workflow.CurrentNodeReferenceKey) bool {
	_, exists := c.currentRunLocked(key)
	return exists
}

func (c *CurrentNodeController) wakeAdmissionWorker() {
	select {
	case c.workerWake <- struct{}{}:
	default:
	}
}

func (c *CurrentNodeController) runAdmissions() {
	defer c.workerWG.Done()
	for {
		select {
		case <-c.workerContext.Done():
			return
		case <-c.workerWake:
		}
		for {
			runID, ok := c.takeExplicitStart()
			if !ok {
				runID, ok = c.takeAutomaticIntent()
				if !ok {
					break
				}
			}
			go c.runAdmission(runID)
		}
	}
}

func (c *CurrentNodeController) runAdmission(runID currentNodeRunID) {
	c.mu.Lock()
	run := c.runs[runID]
	if run == nil {
		c.mu.Unlock()
		return
	}
	reference := run.reference
	done := run.admissionDone
	c.mu.Unlock()
	defer close(done)
	defer c.finishTaskInterruptAdmission(runID)
	defer c.finishAdmissionWorker(runID)
	if err := c.admit(runID); err != nil {
		c.mu.Lock()
		current := c.runs[runID]
		interrupted := current != nil && current.stopping()
		c.mu.Unlock()
		if errors.Is(err, context.Canceled) ||
			errors.Is(err, sessionruntime.ErrExecutionNoLongerLive) ||
			errors.Is(err, ErrTaskExecutionNotQuiescent) ||
			interrupted {
			return
		}
		var failure currentNodeAdmissionError
		if !errors.As(err, &failure) {
			failure = currentNodeAdmissionError{cause: err}
		}
		c.handleAdmissionFailure(runID, reference, failure.admitted, failure.cause)
	}
}

func (c *CurrentNodeController) takeExplicitStart() (currentNodeRunID, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.inFlightAdmissionCountLocked(currentNodeAdmissionExplicitOverride) >= explicitAdmissionConcurrency || len(c.explicitQueue) == 0 {
		return currentNodeRunID{}, false
	}
	index := -1
	for candidateIndex, candidateID := range c.explicitQueue {
		run := c.runs[candidateID]
		if run != nil && !c.runPredecessorActiveLocked(run) {
			index = candidateIndex
			break
		}
	}
	if index < 0 {
		return currentNodeRunID{}, false
	}
	runID := c.explicitQueue[index]
	c.explicitQueue = append(c.explicitQueue[:index], c.explicitQueue[index+1:]...)
	run := c.runs[runID]
	if run == nil || run.phase != currentNodeRunQueued {
		panic("explicit queue points to absent or non-queued Run")
	}
	run.launchContext, run.launchCancel = context.WithCancel(c.workerContext)
	run.admissionDone = make(chan struct{})
	run.transition(currentNodeRunLaunching)
	return runID, true
}

func (c *CurrentNodeController) takeAutomaticIntent() (currentNodeRunID, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.automaticQueue.len() == 0 {
		return currentNodeRunID{}, false
	}
	agentAvailable := c.agentCapacityActive < c.agentConcurrency
	entry, ok := c.automaticQueue.selectEntry(c.lastAutomaticTask, agentAvailable, func(runID currentNodeRunID) bool {
		run := c.runs[runID]
		return run != nil && !c.runPredecessorActiveLocked(run)
	})
	if !ok {
		return currentNodeRunID{}, false
	}
	runID := c.automaticQueue.remove(entry)
	run := c.runs[runID]
	if run == nil || run.phase != currentNodeRunQueued {
		panic("automatic queue points to absent or non-queued Run")
	}
	run.taskPromptDelivery = workflowruntime.TaskPromptDeliveryResume
	run.launchContext, run.launchCancel = context.WithCancel(c.workerContext)
	run.admissionDone = make(chan struct{})
	run.transition(currentNodeRunLaunching)
	if run.policy.countsAgentCapacity() {
		run.agentCapacity = true
		c.agentCapacityActive++
	}
	taskID := run.reference.TaskID
	c.lastAutomaticTask = &taskID
	return runID, true
}

func (c *CurrentNodeController) inFlightAdmissionCountLocked(policy currentNodeAdmissionPolicy) int {
	count := 0
	for _, run := range c.runs {
		if run.policy == policy && run.phase == currentNodeRunLaunching {
			count++
		}
	}
	return count
}

func (c *CurrentNodeController) finishAdmissionWorker(runID currentNodeRunID) {
	c.mu.Lock()
	run := c.runs[runID]
	if run != nil && run.phase == currentNodeRunLaunching && run.callbackErr == nil {
		c.removeRunLocked(runID)
	}
	closed := c.closed
	c.mu.Unlock()
	if !closed {
		c.wakeAdmissionWorker()
	}
}

func (c *CurrentNodeController) handleAdmissionFailure(
	runID currentNodeRunID,
	reference workflow.CurrentNodeReference,
	admitted bool,
	cause error,
) {
	if err := c.handleCurrentNodeStartFailures([]currentNodeQueuedStart{{reference: reference}}, admitted, cause); err != nil {
		c.mu.Lock()
		if run := c.runs[runID]; run != nil {
			c.recordLifecycleFatalLocked(run, errors.Join(cause, err))
			run.transition(currentNodeRunRetiring)
		}
		c.mu.Unlock()
	}
}

func (c *CurrentNodeController) handleCurrentNodeStartFailures(
	starts []currentNodeQueuedStart,
	admitted bool,
	cause error,
) error {
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return nil
	}
	cleanupCtx, cancel := context.WithTimeout(context.Background(), interruptCleanupTimeout)
	defer cancel()
	err := c.recoverCurrentNodeStartFailures(cleanupCtx, starts, admitted, cause)
	return err
}

func (c *CurrentNodeController) recoverCurrentNodeStartFailures(
	ctx context.Context,
	starts []currentNodeQueuedStart,
	admitted bool,
	cause error,
) error {
	var errs []error
	for _, start := range starts {
		start := start
		errs = append(errs, c.taskMutations.Run(ctx, start.reference.TaskID, func(ctx context.Context) error {
			return c.interruptCurrentNodeStartFailures(ctx, []currentNodeQueuedStart{start}, admitted, cause)
		}))
	}
	return errors.Join(errs...)
}

func (c *CurrentNodeController) interruptCurrentNodeStartFailures(
	ctx context.Context,
	starts []currentNodeQueuedStart,
	admitted bool,
	cause error,
) error {
	references := make([]workflow.CurrentNodeReference, 0, len(starts))
	for _, start := range starts {
		references = append(references, start.reference)
	}
	interrupt := c.store.InterruptCurrentNode
	if admitted {
		interrupt = c.store.InterruptAdmittedCurrentNode
	}
	detail := workflow.CurrentNodeInterruptionDetail{
		Code:   string(reasonCurrentNodeRuntimeStartFailed),
		Fields: map[string]string{"error": cause.Error()},
	}
	var preparationErr *TaskStartPreparationError
	if errors.As(cause, &preparationErr) {
		detail = preparationErr.InterruptionDetail()
	}
	interrupted, err := interruptCurrentNodeReferences(
		ctx,
		interrupt,
		references,
		reasonCurrentNodeRuntimeStartFailed,
		detail,
	)
	for _, reference := range interrupted {
		c.publishPendingInterruptedCurrentNode(ctx, reference, reasonCurrentNodeRuntimeStartFailed)
	}
	return err
}

func currentNodeAutomaticIntents(source []workflowstore.CurrentNodeAutomaticIntent) ([]CurrentNodeAutomaticIntent, error) {
	intents := make([]CurrentNodeAutomaticIntent, 0, len(source))
	seen := make(map[workflow.CurrentNodeReferenceKey]struct{}, len(source))
	for index, intent := range source {
		key, err := intent.CurrentNode.Key()
		if err != nil {
			return nil, fmt.Errorf("automatic successor current node at index %d: %w", index, err)
		}
		if _, exists := seen[key]; exists {
			return nil, fmt.Errorf("automatic successor current node at index %d is duplicated", index)
		}
		if intent.NodeKind != workflow.NodeKindAgent && intent.NodeKind != workflow.NodeKindScript {
			return nil, fmt.Errorf("automatic successor current node at index %d has non-executable kind %q", index, intent.NodeKind)
		}
		seen[key] = struct{}{}
		intents = append(intents, intent)
	}
	return intents, nil
}

func automaticQueuedStarts(intents []CurrentNodeAutomaticIntent) []currentNodeQueuedStart {
	starts := make([]currentNodeQueuedStart, 0, len(intents))
	for _, intent := range intents {
		policy := currentNodeAdmissionAutomaticAgent
		if intent.NodeKind == workflow.NodeKindScript {
			policy = currentNodeAdmissionAutomaticScript
		}
		starts = append(starts, currentNodeQueuedStart{
			reference: intent.CurrentNode,
			policy:    policy,
		})
	}
	return starts
}

func currentNodeExplicitStarts(nodes []workflow.CurrentNode) ([]currentNodeQueuedStart, error) {
	starts := make([]currentNodeQueuedStart, 0, len(nodes))
	seen := make(map[workflow.CurrentNodeReferenceKey]struct{}, len(nodes))
	for index, currentNode := range nodes {
		if currentNode.Scheduling == nil {
			continue
		}
		key, err := currentNode.Reference.Key()
		if err != nil {
			return nil, fmt.Errorf("explicit current node start at index %d: %w", index, err)
		}
		if _, duplicate := seen[key]; duplicate {
			return nil, fmt.Errorf("explicit current node start at index %d is duplicated", index)
		}
		seen[key] = struct{}{}
		starts = append(starts, currentNodeQueuedStart{
			reference:          currentNode.Reference,
			taskPromptDelivery: workflowruntime.TaskPromptDeliveryResume,
		})
	}
	return starts, nil
}
