package workflowexecution

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"

	"core/server/session"
	"core/server/sessionruntime"
	"core/server/workflow"
	"core/server/workflowruntime"
	"core/shared/runtimeids"
)

const (
	explicitAdmissionConcurrency                                               = 8
	reasonCurrentNodeRuntimeStartFailed workflow.CurrentNodeInterruptionReason = "workflow_runtime_start_failed"
)

type CurrentNodeAutomaticInterruptionPersistencePanic struct {
	Operation           string
	Reference           workflow.CurrentNodeReference
	ExpectedScheduling  workflow.CurrentNodeSchedulingState
	OriginalFailure     error
	InterruptionFailure error
}

func (p *CurrentNodeAutomaticInterruptionPersistencePanic) Error() string {
	if p == nil {
		return "automatic Current Node interruption persistence failed"
	}
	return fmt.Sprintf(
		"automatic Current Node interruption persistence failed operation=%q task_id=%q current_node=%v expected_scheduling=%q original_failure=%v interruption_failure=%v",
		p.Operation,
		p.Reference.TaskID,
		p.Reference,
		p.ExpectedScheduling,
		p.OriginalFailure,
		p.InterruptionFailure,
	)
}

func (*CurrentNodeAutomaticInterruptionPersistencePanic) ProcessFatalPanic() {}

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

type currentNodeAgentCapacityOwner uint8

const (
	currentNodeAgentCapacityReservation currentNodeAgentCapacityOwner = iota
	currentNodeAgentCapacityLive
	currentNodeAgentCapacityReleased
)

type currentNodeAgentCapacityLease struct {
	owner currentNodeAgentCapacityOwner
}

type currentNodeQueuedStart struct {
	reference          workflow.CurrentNodeReference
	nodeKind           workflow.NodeKind
	taskPromptDelivery workflowruntime.TaskPromptDelivery
	assignmentSteer    CurrentNodeAssignmentSteer
	policy             currentNodeAdmissionPolicy
	completion         *currentNodeAdmissionCompletion
	agentCapacityLease *currentNodeAgentCapacityLease
}

type currentNodeAdmissionCompletion struct {
	once   sync.Once
	done   chan struct{}
	handle sessionruntime.ExecutionHandle
	err    error
}

func newCurrentNodeAdmissionCompletion() *currentNodeAdmissionCompletion {
	return &currentNodeAdmissionCompletion{done: make(chan struct{})}
}

func (c *currentNodeAdmissionCompletion) resolve(handle sessionruntime.ExecutionHandle, err error) {
	if c == nil {
		return
	}
	c.once.Do(func() {
		c.handle = handle
		c.err = err
		close(c.done)
	})
}

func (c *currentNodeAdmissionCompletion) wait(
	ctx context.Context,
) (sessionruntime.ExecutionHandle, error) {
	if c == nil {
		return nil, errors.New("current node admission completion is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-c.done:
	case <-ctx.Done():
		return nil, context.Cause(ctx)
	}
	if c.err != nil {
		return nil, c.err
	}
	if c.handle == nil {
		return nil, errors.New("current node admission completed without an execution")
	}
	return c.handle, nil
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

func prepareCurrentNodeAssignmentSteer(
	ctx context.Context,
	steer CurrentNodeAssignmentSteer,
) (CurrentNodeAssignmentSteer, error) {
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
	preparation, ok := steer.(CurrentNodeAssignmentPreparation)
	if !ok {
		return steer, nil
	}
	if err := preparation.Prepare(ctx); err != nil {
		return nil, err
	}
	return steer, nil
}

func (e currentNodeAdmissionError) Error() string {
	return e.cause.Error()
}

func (e currentNodeAdmissionError) Unwrap() error {
	return e.cause
}

func (c *CurrentNodeController) admit(
	ctx context.Context,
	start currentNodeQueuedStart,
) (sessionruntime.ExecutionHandle, error) {
	if c == nil {
		return nil, errors.New("current node workflow controller is required")
	}
	reference := start.reference
	if err := reference.Validate(); err != nil {
		return nil, err
	}
	key, err := reference.Key()
	if err != nil {
		return nil, err
	}
	operationID := runtimeids.NewCurrentNodeOperationID()
	defer c.releaseReservation(key, start.policy, start.agentCapacityLease)
	scriptPublication, err := c.runner.PrepareScriptPublication(ctx, reference, operationID, c)
	if err != nil {
		return nil, err
	}
	if scriptPublication != nil {
		start.nodeKind = workflow.NodeKindScript
		return c.admitPreparedScript(ctx, start, key, operationID, scriptPublication)
	}
	if start.nodeKind == workflow.NodeKindScript {
		return nil, errors.New("Script publication preparation returned no publication")
	}
	if start.nodeKind == "" {
		start.nodeKind = workflow.NodeKindAgent
	}
	if start.taskPromptDelivery == workflowruntime.TaskPromptDeliveryAssignment &&
		start.assignmentSteer == nil {
		assignment, err := c.steerAssignment(ctx, reference)
		if err != nil {
			return nil, err
		}
		start.assignmentSteer = assignment
	}
	start.assignmentSteer, err = prepareCurrentNodeAssignmentSteer(ctx, start.assignmentSteer)
	if err != nil {
		return nil, err
	}
	assignmentSteer, err := resolvedCurrentNodeAssignmentSteer(ctx, start.assignmentSteer)
	if err != nil {
		return nil, err
	}
	publication, err := c.runner.PrepareAgentPublication(
		ctx,
		reference,
		operationID,
		start.taskPromptDelivery,
		assignmentSteer,
		c,
	)
	if err != nil {
		return nil, err
	}
	if publication == nil {
		return nil, errors.New("Agent publication preparation returned no publication")
	}
	return c.admitPreparedAgent(ctx, start, key, operationID, publication)
}

func (c *CurrentNodeController) admitPreparedScript(
	ctx context.Context,
	start currentNodeQueuedStart,
	key workflow.CurrentNodeReferenceKey,
	operationID runtimeids.CurrentNodeOperationID,
	publication CurrentNodeScriptPublication,
) (sessionruntime.ExecutionHandle, error) {
	defer publication.Cancel()
	var handle sessionruntime.ExecutionHandle
	var launch func()
	err := c.runTaskMutation(ctx, start.reference.TaskID, func(ctx context.Context) error {
		c.mu.Lock()
		if err := c.ensureTaskAvailableLocked(start.reference.TaskID); err != nil {
			c.mu.Unlock()
			return err
		}
		reservation, reserved := c.admissionReservationLocked(key, start.policy)
		if !reserved || reservation.completion != start.completion {
			c.mu.Unlock()
			return sessionruntime.ErrExecutionNoLongerLive
		}
		if _, exists := c.operations[key]; exists {
			c.mu.Unlock()
			return fmt.Errorf("current node %v already has an admitted operation", start.reference)
		}
		c.deleteAdmissionReservationLocked(key, start.policy)
		c.mu.Unlock()
		var publishErr error
		admitted := false
		handle, launch, publishErr = publication.Publish(ctx, func() error {
			receipt, err := c.store.AdmitCurrentNode(context.WithoutCancel(ctx), start.reference)
			admitted = receipt.Committed
			if admissionErr := classifyCurrentNodeAdmission(receipt, err); admissionErr != nil {
				return admissionErr
			}
			c.mu.Lock()
			c.operations[key] = &currentNodeOperation{
				ref: workflow.CurrentNodeOperationRef{
					OperationID: operationID,
					CurrentNode: start.reference,
				},
				policy:             start.policy,
				agentCapacityLease: start.agentCapacityLease,
			}
			c.mu.Unlock()
			return nil
		}, func(published sessionruntime.ExecutionHandle) {
			workflowRef, ok := published.Scope().Workflow()
			if !ok {
				panic("published Script execution has no Workflow metadata")
			}
			c.mu.Lock()
			operation := c.operations[key]
			if operation == nil || operation.ref.OperationID != operationID {
				c.mu.Unlock()
				panic("published Script execution has no matching admitted operation")
			}
			operation.workflow = &workflowRef
			c.mu.Unlock()
		})
		if publishErr != nil {
			return currentNodeAdmissionError{cause: publishErr, admitted: admitted}
		}
		return nil
	})
	if err != nil {
		var admissionErr currentNodeAdmissionError
		if errors.As(err, &admissionErr) {
			return nil, admissionErr
		}
		return nil, currentNodeAdmissionError{cause: err}
	}
	launch()
	scope := handle.Scope()
	scopeRef, ok := scope.Workflow()
	if !ok || !scopeRef.CurrentNode.Equal(start.reference) {
		return nil, currentNodeAdmissionError{
			cause:    errors.New("detached Script publication started a mismatched Workflow scope"),
			admitted: true,
		}
	}
	if !start.policy.isAutomatic() {
		c.wakeAdmissionWorker()
	}
	return handle, nil
}

func (c *CurrentNodeController) admitPreparedAgent(
	ctx context.Context,
	start currentNodeQueuedStart,
	key workflow.CurrentNodeReferenceKey,
	operationID runtimeids.CurrentNodeOperationID,
	publication CurrentNodeAgentPublication,
) (sessionruntime.ExecutionHandle, error) {
	defer func() {
		if err := publication.Cancel(); err != nil {
			c.mu.Lock()
			c.workerErr = errors.Join(c.workerErr, err)
			c.mu.Unlock()
		}
	}()
	var handle sessionruntime.ExecutionHandle
	var launch func()
	err := c.runTaskMutation(ctx, start.reference.TaskID, func(ctx context.Context) error {
		c.mu.Lock()
		if err := c.ensureTaskAvailableLocked(start.reference.TaskID); err != nil {
			c.mu.Unlock()
			return err
		}
		reservation, reserved := c.admissionReservationLocked(key, start.policy)
		if !reserved || reservation.completion != start.completion {
			c.mu.Unlock()
			return sessionruntime.ErrExecutionNoLongerLive
		}
		if _, exists := c.operations[key]; exists {
			c.mu.Unlock()
			return fmt.Errorf("current node %v already has an admitted operation", start.reference)
		}
		c.deleteAdmissionReservationLocked(key, start.policy)
		c.transitionAgentCapacityLocked(
			start.agentCapacityLease,
			currentNodeAgentCapacityReservation,
			currentNodeAgentCapacityLive,
		)
		c.mu.Unlock()
		var publishErr error
		admitted := false
		handle, launch, publishErr = publication.Publish(ctx, func() error {
			receipt, err := c.store.AdmitCurrentNode(context.WithoutCancel(ctx), start.reference)
			admitted = receipt.Committed
			if admissionErr := classifyCurrentNodeAdmission(receipt, err); admissionErr != nil {
				return admissionErr
			}
			c.mu.Lock()
			c.operations[key] = &currentNodeOperation{
				ref: workflow.CurrentNodeOperationRef{
					OperationID: operationID,
					CurrentNode: start.reference,
				},
				policy:             start.policy,
				agentCapacityLease: start.agentCapacityLease,
			}
			c.mu.Unlock()
			return nil
		}, func(published sessionruntime.ExecutionHandle) {
			workflowRef, ok := published.Scope().Workflow()
			if !ok {
				panic("published Agent execution has no Workflow metadata")
			}
			c.mu.Lock()
			operation := c.operations[key]
			if operation == nil || operation.ref.OperationID != operationID {
				c.mu.Unlock()
				panic("published Agent execution has no matching admitted operation")
			}
			operation.workflow = &workflowRef
			c.mu.Unlock()
		})
		if publishErr != nil {
			return currentNodeAdmissionError{cause: publishErr, admitted: admitted}
		}
		return nil
	})
	if err != nil {
		c.mu.Lock()
		c.releaseAgentCapacityLocked(start.agentCapacityLease)
		c.mu.Unlock()
		var admissionErr currentNodeAdmissionError
		if errors.As(err, &admissionErr) {
			return nil, admissionErr
		}
		return nil, currentNodeAdmissionError{cause: err}
	}
	launch()
	scope := handle.Scope()
	scopeRef, ok := scope.Workflow()
	if !ok || !scopeRef.CurrentNode.Equal(start.reference) {
		return nil, currentNodeAdmissionError{
			cause:    errors.New("detached Agent publication started a mismatched Workflow scope"),
			admitted: true,
		}
	}
	if !start.policy.isAutomatic() {
		c.wakeAdmissionWorker()
	}
	return handle, nil
}

func classifyCurrentNodeAdmission(receipt session.CommitReceipt, err error) error {
	if receipt.Committed {
		return nil
	}
	if err == nil {
		panic("Current Node admission returned neither a commit nor an error")
	}
	if errors.Is(err, session.ErrMutationDefinitelyUncommitted) {
		return err
	}
	panic(fmt.Sprintf("Current Node admission commit certainty is indeterminate: %v", err))
}

func (c *CurrentNodeController) enqueueAutomaticIntents(intents []CurrentNodeAutomaticIntent) {
	c.enqueueStarts(automaticQueuedStarts(intents))
}

func (c *CurrentNodeController) steerStartsAssignments(ctx context.Context, starts []currentNodeQueuedStart) ([]currentNodeQueuedStart, error) {
	steered := append([]currentNodeQueuedStart(nil), starts...)
	for index := range steered {
		if steered[index].nodeKind == workflow.NodeKindScript {
			continue
		}
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
		if pendingStarts[index].nodeKind == workflow.NodeKindScript {
			continue
		}
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
	for index, start := range starts {
		if start.nodeKind == workflow.NodeKindScript {
			continue
		}
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
		if start.nodeKind == workflow.NodeKindScript {
			outcome.committed = append(outcome.committed, start)
			continue
		}
		if start.assignmentSteer == nil {
			outcome.err = errors.Join(outcome.err, fmt.Errorf(
				"current node assignment %v has no steer completion",
				start.reference,
			))
			continue
		}
		assignmentSteer, prepareErr := prepareCurrentNodeAssignmentSteer(ctx, start.assignmentSteer)
		if prepareErr != nil {
			outcome.err = errors.Join(outcome.err, fmt.Errorf(
				"prepare current node assignment %v: %w",
				start.reference,
				prepareErr,
			))
			continue
		}
		start.assignmentSteer = assignmentSteer
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
			c.handleCurrentNodeStartFailures(automaticFailureRecoveryStarts(starts, outcome.committed), false, cause)
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
			c.workerErr = errors.Join(c.workerErr, err)
		}
	}
	c.mu.Unlock()
	c.wakeAdmissionWorker()
}

func (c *CurrentNodeController) queueExplicitStartLocked(start currentNodeQueuedStart) error {
	if start.policy != currentNodeAdmissionExplicitOverride {
		return errors.New("explicit current node start cannot be automatic")
	}
	if start.completion == nil {
		start.completion = newCurrentNodeAdmissionCompletion()
	}
	key, err := start.reference.Key()
	if err != nil {
		return err
	}
	if c.currentNodeOwnedLocked(key) {
		return nil
	}
	c.explicitQueue = append(c.explicitQueue, start)
	c.explicitQueued[key] = struct{}{}
	c.wakeAdmissionWorker()
	return nil
}

func (c *CurrentNodeController) queueAutomaticStartLocked(start currentNodeQueuedStart) error {
	if !start.policy.isAutomatic() {
		return errors.New("automatic current node start requires an automatic admission policy")
	}
	if start.completion == nil {
		start.completion = newCurrentNodeAdmissionCompletion()
	}
	key, err := start.reference.Key()
	if err != nil {
		return err
	}
	if c.currentNodeOwnedLocked(key) {
		return nil
	}
	c.automaticQueue.append(start)
	c.queued[key] = struct{}{}
	c.wakeAdmissionWorker()
	return nil
}

func (c *CurrentNodeController) currentNodeOwnedLocked(key workflow.CurrentNodeReferenceKey) bool {
	for _, batch := range c.preparationQueue {
		for _, start := range batch.starts {
			startKey, err := start.reference.Key()
			if err != nil {
				panic(fmt.Sprintf("inspect queued task preparation ownership: %v", err))
			}
			if startKey == key {
				return true
			}
		}
	}
	for _, batch := range c.preparationRunning {
		for _, start := range batch.starts {
			startKey, err := start.reference.Key()
			if err != nil {
				panic(fmt.Sprintf("inspect running task preparation ownership: %v", err))
			}
			if startKey == key {
				return true
			}
		}
	}
	if _, queued := c.explicitQueued[key]; queued {
		return true
	}
	if _, reserved := c.explicitReservations[key]; reserved {
		return true
	}
	if _, queued := c.queued[key]; queued {
		return true
	}
	if _, reserved := c.automaticReservations[key]; reserved {
		return true
	}
	_, admitted := c.operations[key]
	return admitted
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
			if batch, ok := c.takeTaskPreparationBatch(); ok {
				go c.runTaskPreparationBatch(batch)
				continue
			}
			start, ok := c.takeExplicitStart()
			if !ok {
				start, ok = c.takeAutomaticIntent()
				if !ok {
					break
				}
			}
			go c.runAdmission(start)
		}
	}
}

func (c *CurrentNodeController) runAdmission(start currentNodeQueuedStart) {
	defer c.admissionWG.Done()
	defer c.finishAdmissionWorker(start)
	defer c.finishTaskInterruptAdmission(start.reference)
	var (
		handle sessionruntime.ExecutionHandle
		err    error
	)
	defer func() {
		start.completion.resolve(handle, err)
	}()
	handle, err = c.admit(c.workerContext, start)
	if err != nil {
		key, keyErr := start.reference.Key()
		if keyErr != nil {
			panic(fmt.Sprintf("inspect failed current node admission: %v", keyErr))
		}
		c.mu.Lock()
		interrupted := c.interrupts.currentNodeFenced(key)
		c.mu.Unlock()
		if errors.Is(err, context.Canceled) ||
			errors.Is(err, ErrTaskExecutionNotQuiescent) ||
			interrupted {
			return
		}
		var failure currentNodeAdmissionError
		if !errors.As(err, &failure) {
			failure = currentNodeAdmissionError{cause: err}
		}
		c.handleAdmissionFailure(start, failure.admitted, failure.cause)
	}
}

func (c *CurrentNodeController) takeExplicitStart() (currentNodeQueuedStart, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.inFlightAdmissionCountLocked(currentNodeAdmissionExplicitOverride) >= explicitAdmissionConcurrency || len(c.explicitQueue) == 0 {
		return currentNodeQueuedStart{}, false
	}
	start := c.explicitQueue[0]
	c.explicitQueue = c.explicitQueue[1:]
	reference := start.reference
	key, err := reference.Key()
	if err != nil {
		panic(fmt.Sprintf("take explicit current node start: %v", err))
	}
	delete(c.explicitQueued, key)
	if start.completion == nil {
		start.completion = newCurrentNodeAdmissionCompletion()
	}
	c.explicitReservations[key] = start
	c.admissionWorkers[key] = start
	c.admissionWG.Add(1)
	return start, true
}

func (c *CurrentNodeController) takeAutomaticIntent() (currentNodeQueuedStart, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.automaticQueue.len() == 0 {
		return currentNodeQueuedStart{}, false
	}
	agentAvailable := c.agentCapacityActive < c.agentConcurrency
	entry, ok := c.automaticQueue.selectEntry(c.lastAutomaticTask, agentAvailable)
	if !ok {
		return currentNodeQueuedStart{}, false
	}
	start := c.automaticQueue.remove(entry)
	key, err := start.reference.Key()
	if err != nil {
		panic(fmt.Sprintf("take automatic current node intent: %v", err))
	}
	delete(c.queued, key)
	start.taskPromptDelivery = workflowruntime.TaskPromptDeliveryResume
	if start.completion == nil {
		start.completion = newCurrentNodeAdmissionCompletion()
	}
	if start.policy.countsAgentCapacity() {
		start.agentCapacityLease = &currentNodeAgentCapacityLease{
			owner: currentNodeAgentCapacityReservation,
		}
		c.agentCapacityActive++
	}
	c.automaticReservations[key] = start
	c.admissionWorkers[key] = start
	c.admissionWG.Add(1)
	taskID := start.reference.TaskID
	c.lastAutomaticTask = &taskID
	return start, true
}

func (c *CurrentNodeController) transitionAgentCapacityLocked(
	lease *currentNodeAgentCapacityLease,
	from currentNodeAgentCapacityOwner,
	to currentNodeAgentCapacityOwner,
) {
	if lease == nil {
		return
	}
	if lease.owner != from {
		panic(fmt.Sprintf("automatic Agent capacity owner transition %d -> %d from %d", from, to, lease.owner))
	}
	lease.owner = to
}

func (c *CurrentNodeController) releaseAgentCapacityLocked(lease *currentNodeAgentCapacityLease) {
	if lease == nil || lease.owner == currentNodeAgentCapacityReleased {
		return
	}
	switch lease.owner {
	case currentNodeAgentCapacityReservation, currentNodeAgentCapacityLive:
		lease.owner = currentNodeAgentCapacityReleased
	default:
		panic(fmt.Sprintf("automatic Agent capacity has invalid owner %d", lease.owner))
	}
	if c.agentCapacityActive <= 0 {
		panic("automatic Agent capacity released without an active reservation, gate, or live scope")
	}
	c.agentCapacityActive--
}

func (c *CurrentNodeController) inFlightAdmissionCountLocked(policy currentNodeAdmissionPolicy) int {
	count := 0
	for key, start := range c.admissionWorkers {
		if start.policy == policy {
			if policy.countsAgentCapacity() {
				if _, admitted := c.operations[key]; admitted {
					continue
				}
			}
			count++
		}
	}
	return count
}

func (c *CurrentNodeController) admissionReservationLocked(key workflow.CurrentNodeReferenceKey, policy currentNodeAdmissionPolicy) (currentNodeQueuedStart, bool) {
	if policy.isAutomatic() {
		start, exists := c.automaticReservations[key]
		return start, exists
	}
	start, exists := c.explicitReservations[key]
	return start, exists
}

func (c *CurrentNodeController) deleteAdmissionReservationLocked(key workflow.CurrentNodeReferenceKey, policy currentNodeAdmissionPolicy) {
	if policy.isAutomatic() {
		delete(c.automaticReservations, key)
		return
	}
	delete(c.explicitReservations, key)
}

func (c *CurrentNodeController) releaseReservation(
	key workflow.CurrentNodeReferenceKey,
	policy currentNodeAdmissionPolicy,
	capacityLease *currentNodeAgentCapacityLease,
) {
	c.mu.Lock()
	_, reserved := c.admissionReservationLocked(key, policy)
	c.deleteAdmissionReservationLocked(key, policy)
	if capacityLease != nil && capacityLease.owner == currentNodeAgentCapacityReservation {
		c.releaseAgentCapacityLocked(capacityLease)
	}
	closed := c.closed
	c.mu.Unlock()
	if (reserved || policy.isAutomatic()) && !closed {
		c.wakeAdmissionWorker()
	}
}

func (c *CurrentNodeController) finishAdmissionWorker(start currentNodeQueuedStart) {
	key, err := start.reference.Key()
	if err != nil {
		panic(fmt.Sprintf("finish current node admission worker: %v", err))
	}
	c.mu.Lock()
	current, exists := c.admissionWorkers[key]
	if !exists || current.completion != start.completion || current.policy != start.policy {
		c.mu.Unlock()
		panic("current node admission worker ownership was replaced before completion")
	}
	delete(c.admissionWorkers, key)
	closed := c.closed
	c.mu.Unlock()
	if !closed {
		c.wakeAdmissionWorker()
	}
}

func (c *CurrentNodeController) handleAdmissionFailure(start currentNodeQueuedStart, admitted bool, cause error) {
	c.handleCurrentNodeStartFailures([]currentNodeQueuedStart{start}, admitted, cause)
}

func (c *CurrentNodeController) handleCurrentNodeStartFailures(
	starts []currentNodeQueuedStart,
	admitted bool,
	cause error,
) {
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return
	}
	cleanupCtx, cancel := context.WithTimeout(context.Background(), interruptCleanupTimeout)
	defer cancel()
	err := c.recoverCurrentNodeStartFailures(cleanupCtx, starts, admitted, cause)
	if err == nil {
		return
	}
	c.mu.Lock()
	c.workerErr = errors.Join(c.workerErr, cause, err)
	c.mu.Unlock()
}

func (c *CurrentNodeController) recoverCurrentNodeStartFailures(
	ctx context.Context,
	starts []currentNodeQueuedStart,
	admitted bool,
	cause error,
) error {
	taskIDs := make([]workflow.TaskID, 0, len(starts))
	for _, start := range starts {
		taskIDs = append(taskIDs, start.reference.TaskID)
	}
	return c.runTaskMutations(ctx, taskIDs, func(ctx context.Context) error {
		return c.interruptCurrentNodeStartFailures(ctx, starts, admitted, cause)
	})
}

func (c *CurrentNodeController) interruptCurrentNodeStartFailures(
	ctx context.Context,
	starts []currentNodeQueuedStart,
	admitted bool,
	cause error,
) error {
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
	seen := make(map[workflow.CurrentNodeReferenceKey]struct{}, len(starts))
	var interruptErrs []error
	for _, start := range starts {
		key, err := start.reference.Key()
		if err != nil {
			return err
		}
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		err = interrupt(ctx, start.reference, reasonCurrentNodeRuntimeStartFailed, detail)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			if start.policy.isAutomatic() {
				operation := "ready_start"
				expectedScheduling := workflow.CurrentNodeSchedulingReady
				if admitted {
					operation = "admitted_start"
					expectedScheduling = workflow.CurrentNodeSchedulingAdmitted
				}
				panicCurrentNodeAutomaticInterruptionPersistence(
					operation,
					start,
					expectedScheduling,
					cause,
					err,
				)
			}
			interruptErrs = append(interruptErrs, err)
			continue
		}
		c.publishPendingInterruptedCurrentNode(ctx, start.reference, reasonCurrentNodeRuntimeStartFailed)
	}
	return errors.Join(interruptErrs...)
}

func automaticFailureRecoveryStarts(
	starts []currentNodeQueuedStart,
	committed []currentNodeQueuedStart,
) []currentNodeQueuedStart {
	for _, start := range starts {
		if start.policy.isAutomatic() {
			return starts
		}
	}
	return committed
}

func panicCurrentNodeAutomaticInterruptionPersistence(
	operation string,
	start currentNodeQueuedStart,
	expectedScheduling workflow.CurrentNodeSchedulingState,
	originalFailure error,
	interruptionFailure error,
) {
	panic(&CurrentNodeAutomaticInterruptionPersistencePanic{
		Operation:           operation,
		Reference:           start.reference,
		ExpectedScheduling:  expectedScheduling,
		OriginalFailure:     originalFailure,
		InterruptionFailure: interruptionFailure,
	})
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
			nodeKind:  intent.NodeKind,
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
