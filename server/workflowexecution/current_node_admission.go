package workflowexecution

import (
	"context"
	"errors"
	"fmt"

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
	currentNodeAgentCapacityGate
	currentNodeAgentCapacityLive
	currentNodeAgentCapacityReleased
)

type currentNodeAgentCapacityLease struct {
	owner currentNodeAgentCapacityOwner
}

type currentNodeQueuedStart struct {
	reference          workflow.CurrentNodeReference
	taskPromptDelivery workflowruntime.TaskPromptDelivery
	assignment         *CurrentNodeClassifiedAssignment
	policy             currentNodeAdmissionPolicy
	done               chan struct{}
	agentCapacityLease *currentNodeAgentCapacityLease
}

type currentNodeAdmissionGate struct {
	reference          workflow.CurrentNodeReference
	lease              sessionruntime.WorkflowExecutionLease
	policy             currentNodeAdmissionPolicy
	done               <-chan struct{}
	agentCapacityLease *currentNodeAgentCapacityLease
}

type currentNodeLiveScope struct {
	reference          workflow.CurrentNodeReference
	lease              sessionruntime.WorkflowExecutionLease
	policy             currentNodeAdmissionPolicy
	agentCapacityLease *currentNodeAgentCapacityLease
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

func (e currentNodeAdmissionError) Error() string {
	return e.cause.Error()
}

func (e currentNodeAdmissionError) Unwrap() error {
	return e.cause
}

func (c *CurrentNodeController) admit(ctx context.Context, start currentNodeQueuedStart) (err error) {
	if c == nil {
		return errors.New("current node workflow controller is required")
	}
	reference := start.reference
	if err := reference.Validate(); err != nil {
		return err
	}
	key, err := reference.Key()
	if err != nil {
		return err
	}
	defer c.releaseReservation(key, start.policy, start.agentCapacityLease)
	if start.taskPromptDelivery == workflowruntime.TaskPromptDeliveryAssignment &&
		start.assignment == nil {
		prepared, err := c.steerAssignment(ctx, reference)
		if err != nil {
			return err
		}
		decision := classifyCurrentNodeAssignment(ctx, reference, prepared)
		if decision.diagnostic != nil || decision.assignment == nil {
			return fmt.Errorf("wait for initial current node assignment %v: %w", reference, decision.diagnostic)
		}
		start.assignment = decision.assignment
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return errors.New("current node workflow controller is closed")
	}
	c.mu.Unlock()

	var lease sessionruntime.WorkflowExecutionLease
retryAdmission:
	if err := c.permit.Run(ctx, func(ctx context.Context) error {
		c.mu.Lock()
		if err := c.ensureTaskAvailableLocked(reference.TaskID); err != nil {
			c.mu.Unlock()
			return err
		}
		reservation, reserved := c.admissionReservationLocked(key, start.policy)
		if !reserved || reservation.done != start.done {
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
		if _, exists := c.gates[key]; exists {
			c.mu.Unlock()
			next.Cancel()
			return fmt.Errorf("current node %v is already being admitted", reference)
		}
		if _, exists := c.liveByNode[key]; exists {
			c.mu.Unlock()
			next.Cancel()
			return fmt.Errorf("current node %v already has a live execution scope", reference)
		}
		if _, stopping := c.stopping[next.ScopeID()]; stopping {
			c.mu.Unlock()
			next.Cancel()
			return sessionruntime.ErrExecutionNoLongerLive
		}
		reservation, reserved = c.admissionReservationLocked(key, start.policy)
		if !reserved || reservation.done != start.done {
			c.mu.Unlock()
			next.Cancel()
			return sessionruntime.ErrExecutionNoLongerLive
		}
		c.deleteAdmissionReservationLocked(key, start.policy)
		c.transitionAgentCapacityLocked(
			start.agentCapacityLease,
			currentNodeAgentCapacityReservation,
			currentNodeAgentCapacityGate,
		)
		// The gate precedes the durable restart marker, so any conflicting
		// lifecycle mutation sees admission before slow runner preparation.
		c.gates[key] = currentNodeAdmissionGate{
			reference:          reference,
			lease:              next,
			policy:             start.policy,
			done:               start.done,
			agentCapacityLease: start.agentCapacityLease,
		}
		c.mu.Unlock()

		if err := c.store.AdmitCurrentNode(ctx, reference); err != nil {
			c.removeGate(key, next.ScopeID())
			next.Cancel()
			return err
		}
		lease = next
		return nil
	}); err != nil {
		if !errors.Is(err, ErrTaskExecutionNotQuiescent) {
			return currentNodeAdmissionError{cause: err}
		}
		c.mu.Lock()
		fence := c.interrupts.taskFence(reference.TaskID)
		selected := c.interrupts.currentNodeFenced(key)
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
	if err := c.runner.StartCurrentNode(ctx, reference, start.taskPromptDelivery, start.assignment, lease, c); err != nil {
		return currentNodeAdmissionError{
			cause:    c.discardAdmission(reference, key, lease, err),
			admitted: true,
		}
	}
	if err := c.permit.Run(ctx, func(context.Context) error {
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
		gate, exists := c.gates[key]
		if !exists || gate.lease.ScopeID() != lease.ScopeID() {
			return errors.New("current node admission gate was replaced before live scope registration")
		}
		if err := c.ensureTaskAvailableLocked(reference.TaskID); err != nil {
			return err
		}
		if _, stopping := c.stopping[lease.ScopeID()]; stopping {
			return sessionruntime.ErrExecutionNoLongerLive
		}
		delete(c.gates, key)
		c.transitionAgentCapacityLocked(
			gate.agentCapacityLease,
			currentNodeAgentCapacityGate,
			currentNodeAgentCapacityLive,
		)
		c.live[lease.ScopeID()] = currentNodeLiveScope{
			reference:          reference,
			lease:              lease,
			policy:             gate.policy,
			agentCapacityLease: gate.agentCapacityLease,
		}
		c.liveByNode[key] = lease.ScopeID()
		lease.Release()
		return nil
	}); err != nil {
		return currentNodeAdmissionError{
			cause:    c.discardAdmission(reference, key, lease, err),
			admitted: true,
		}
	}
	if !start.policy.isAutomatic() {
		c.wakeAdmissionWorker()
	}
	return nil
}

func (c *CurrentNodeController) removeGate(key workflow.CurrentNodeReferenceKey, scopeID runtimeids.ExecutionScopeID) {
	c.mu.Lock()
	wake := false
	if gate, exists := c.gates[key]; exists && gate.lease.ScopeID() == scopeID {
		delete(c.gates, key)
		delete(c.stopping, scopeID)
		c.releaseAgentCapacityLocked(gate.agentCapacityLease)
		wake = true
	}
	c.mu.Unlock()
	if wake {
		c.wakeAdmissionWorker()
	}
}

func (c *CurrentNodeController) discardAdmission(reference workflow.CurrentNodeReference, key workflow.CurrentNodeReferenceKey, lease sessionruntime.WorkflowExecutionLease, cause error) error {
	c.removeGate(key, lease.ScopeID())
	c.mu.Lock()
	if live, exists := c.live[lease.ScopeID()]; exists {
		c.releaseAgentCapacityLocked(live.agentCapacityLease)
		delete(c.live, lease.ScopeID())
	}
	if current, exists := c.liveByNode[key]; exists && current == lease.ScopeID() {
		delete(c.liveByNode, key)
	}
	c.mu.Unlock()
	lease.Cancel()
	return cause
}

func (c *CurrentNodeController) enqueueAutomaticIntents(intents []CurrentNodeAutomaticIntent) {
	c.enqueueStarts(automaticQueuedStarts(intents))
}

type currentNodePreparedAssignment struct {
	start       currentNodeQueuedStart
	prepared    CurrentNodeAssignmentSteer
	preparation error
}

type currentNodeAssignmentUncommittedError struct {
	reference workflow.CurrentNodeReference
}

type currentNodeAssignmentDecision struct {
	assignment *CurrentNodeClassifiedAssignment
	diagnostic error
}

func newCurrentNodeAssignmentUncommittedError(
	reference workflow.CurrentNodeReference,
) *currentNodeAssignmentUncommittedError {
	return &currentNodeAssignmentUncommittedError{reference: reference}
}

func (e *currentNodeAssignmentUncommittedError) Error() string {
	if e == nil {
		return "current node assignment was not committed"
	}
	return fmt.Sprintf("current node assignment %v was not committed", e.reference)
}

func classifyCurrentNodeAssignment(
	ctx context.Context,
	reference workflow.CurrentNodeReference,
	prepared CurrentNodeAssignmentSteer,
) currentNodeAssignmentDecision {
	receipt, diagnostic := prepared.Wait(ctx)
	if receipt.Committed {
		return currentNodeAssignmentDecision{
			assignment: newCurrentNodeClassifiedAssignment(reference, prepared),
			diagnostic: diagnostic,
		}
	}
	if diagnostic == nil {
		diagnostic = newCurrentNodeAssignmentUncommittedError(reference)
	}
	return currentNodeAssignmentDecision{diagnostic: diagnostic}
}

func (c *CurrentNodeController) prepareCurrentNodeAssignments(
	ctx context.Context,
	starts []currentNodeQueuedStart,
) []currentNodePreparedAssignment {
	prepared := make([]currentNodePreparedAssignment, 0, len(starts))
	for _, start := range starts {
		assignment, err := c.steerAssignment(ctx, start.reference)
		prepared = append(prepared, currentNodePreparedAssignment{
			start:       start,
			prepared:    assignment,
			preparation: err,
		})
	}
	return prepared
}

func (c *CurrentNodeController) classifyAutomaticStarts(
	ctx context.Context,
	starts []currentNodeQueuedStart,
	holdFor *runtimeids.ExecutionScopeID,
) ([]currentNodeQueuedStart, error) {
	return c.classifyPreparedAutomaticStarts(
		ctx,
		c.prepareCurrentNodeAssignments(ctx, starts),
		true,
		holdFor,
	)
}

func (c *CurrentNodeController) classifyPreparedAutomaticStarts(
	ctx context.Context,
	prepared []currentNodePreparedAssignment,
	transferUnresolved bool,
	holdFor *runtimeids.ExecutionScopeID,
) ([]currentNodeQueuedStart, error) {
	accepted := make([]currentNodeQueuedStart, 0, len(prepared))
	pending := make([]currentNodePreparedAssignment, 0, len(prepared))
	var diagnostics []error
	for _, candidate := range prepared {
		if candidate.preparation != nil {
			cause := candidate.preparation
			diagnostics = append(diagnostics, errors.Join(
				cause,
				c.interruptUncommittedAutomaticStart(ctx, candidate.start, cause),
			))
			continue
		}
		decision := classifyCurrentNodeAssignment(
			ctx,
			candidate.start.reference,
			candidate.prepared,
		)
		if decision.assignment == nil {
			if cancellation := context.Cause(ctx); transferUnresolved &&
				cancellation != nil &&
				errors.Is(decision.diagnostic, cancellation) {
				pending = append(pending, candidate)
				diagnostics = append(diagnostics, fmt.Errorf(
					"wait for current node assignment %v: %w",
					candidate.start.reference,
					decision.diagnostic,
				))
				continue
			}
		}
		if decision.assignment != nil {
			candidate.start.assignment = decision.assignment
			accepted = append(accepted, candidate.start)
			if decision.diagnostic != nil {
				diagnostics = append(diagnostics, fmt.Errorf(
					"wait for current node assignment %v: %w",
					candidate.start.reference,
					decision.diagnostic,
				))
			}
			continue
		}
		cause := decision.diagnostic
		diagnostics = append(diagnostics, errors.Join(
			fmt.Errorf(
				"wait for current node assignment %v: %w",
				candidate.start.reference,
				cause,
			),
			c.interruptUncommittedAutomaticStart(ctx, candidate.start, cause),
		))
	}
	if len(pending) != 0 {
		c.continueCurrentNodeAssignmentStarts(pending, holdFor)
	}
	return accepted, errors.Join(diagnostics...)
}

func (c *CurrentNodeController) interruptUncommittedAutomaticStart(
	ctx context.Context,
	start currentNodeQueuedStart,
	cause error,
) error {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), interruptCleanupTimeout)
	defer cancel()
	committed, diagnostic := c.interruptCurrentNodeStartFailure(cleanupCtx, start, false, cause)
	if !committed && start.policy.isAutomatic() {
		panicCurrentNodeAutomaticInterruptionPersistence(
			"assignment",
			start,
			workflow.CurrentNodeSchedulingReady,
			cause,
			diagnostic,
		)
	}
	return diagnostic
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

func (c *CurrentNodeController) continueCurrentNodeAssignmentStarts(
	prepared []currentNodePreparedAssignment,
	holdFor *runtimeids.ExecutionScopeID,
) {
	if len(prepared) == 0 {
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
		accepted, diagnostic := c.classifyPreparedAutomaticStarts(
			c.workerContext,
			prepared,
			false,
			nil,
		)
		if context.Cause(c.workerContext) != nil {
			return
		}
		c.deliverClassifiedStarts(accepted, holdFor)
		if diagnostic != nil {
			c.mu.Lock()
			c.workerDiagnostics = errors.Join(c.workerDiagnostics, diagnostic)
			c.mu.Unlock()
		}
	}()
}

func (c *CurrentNodeController) deliverClassifiedStarts(
	starts []currentNodeQueuedStart,
	holdFor *runtimeids.ExecutionScopeID,
) {
	if len(starts) == 0 {
		return
	}
	if holdFor == nil {
		c.enqueueStarts(starts)
		return
	}
	c.mu.Lock()
	if _, live := c.live[*holdFor]; live {
		if _, completed := c.completed[*holdFor]; completed {
			c.heldStarts[*holdFor] = append(c.heldStarts[*holdFor], starts...)
			c.mu.Unlock()
			return
		}
	}
	c.mu.Unlock()
	c.enqueueStarts(starts)
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
	if _, gated := c.gates[key]; gated {
		return true
	}
	_, live := c.liveByNode[key]
	return live
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
	defer close(start.done)
	defer c.finishTaskInterruptAdmission(start.reference)
	defer c.finishAdmissionWorker(start)
	if err := c.admit(c.workerContext, start); err != nil {
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
	start.done = make(chan struct{})
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
	start.done = make(chan struct{})
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
	case currentNodeAgentCapacityReservation, currentNodeAgentCapacityGate, currentNodeAgentCapacityLive:
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
				if _, live := c.liveByNode[key]; live {
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
	if !exists || current.done != start.done || current.policy != start.policy {
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

func (c *CurrentNodeController) handleAdmissionFailure(
	start currentNodeQueuedStart,
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
	committed, diagnostic := c.interruptCurrentNodeStartFailure(cleanupCtx, start, admitted, cause)
	if !committed && start.policy.isAutomatic() {
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
			diagnostic,
		)
	}
	if diagnostic == nil {
		return
	}
	c.mu.Lock()
	if committed {
		c.workerDiagnostics = errors.Join(c.workerDiagnostics, cause, diagnostic)
	} else {
		c.workerErr = errors.Join(c.workerErr, cause, diagnostic)
	}
	c.mu.Unlock()
}

func (c *CurrentNodeController) interruptCurrentNodeStartFailure(
	ctx context.Context,
	start currentNodeQueuedStart,
	admitted bool,
	cause error,
) (bool, error) {
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
	var committed bool
	err := c.permit.Run(ctx, func(ctx context.Context) error {
		var diagnostic error
		committed, diagnostic = classifyCurrentNodeInterruption(interrupt(
			ctx,
			start.reference,
			reasonCurrentNodeRuntimeStartFailed,
			detail,
		))
		return diagnostic
	})
	if committed {
		c.publishPendingInterruptedCurrentNode(
			ctx,
			start.reference,
			reasonCurrentNodeRuntimeStartFailed,
		)
	}
	return committed, err
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
