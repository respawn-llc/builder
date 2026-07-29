package workflowexecution

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"core/server/sessionruntime"
	"core/server/workflow"
	"core/shared/runtimeids"
)

const explicitAdmissionConcurrency = 8

type currentNodeQueuedStart struct {
	reference workflow.CurrentNodeReference
	automatic bool
	done      chan struct{}
}

type currentNodeAdmissionGate struct {
	reference workflow.CurrentNodeReference
	lease     sessionruntime.WorkflowExecutionLease
	automatic bool
	done      <-chan struct{}
}

type currentNodeLiveScope struct {
	reference workflow.CurrentNodeReference
	lease     sessionruntime.WorkflowExecutionLease
	automatic bool
}

type currentNodeAdmissionError struct {
	cause    error
	admitted bool
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
	defer c.releaseReservation(key, start.automatic)
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return errors.New("current node workflow controller is closed")
	}
	c.mu.Unlock()

	var lease sessionruntime.WorkflowExecutionLease
	if err := c.permit.Run(ctx, func(ctx context.Context) error {
		c.mu.Lock()
		if err := c.ensureTaskAvailableLocked(reference.TaskID); err != nil {
			c.mu.Unlock()
			return err
		}
		reservation, reserved := c.admissionReservationLocked(key, start.automatic)
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
		reservation, reserved = c.admissionReservationLocked(key, start.automatic)
		if !reserved || reservation.done != start.done {
			c.mu.Unlock()
			next.Cancel()
			return sessionruntime.ErrExecutionNoLongerLive
		}
		c.deleteAdmissionReservationLocked(key, start.automatic)
		// The gate precedes the durable restart marker, so any conflicting
		// lifecycle mutation sees admission before slow runner preparation.
		c.gates[key] = currentNodeAdmissionGate{
			reference: reference,
			lease:     next,
			automatic: start.automatic,
			done:      start.done,
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
		return currentNodeAdmissionError{cause: err}
	}
	if err := c.runner.StartCurrentNode(ctx, reference, lease, c); err != nil {
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
		c.live[lease.ScopeID()] = currentNodeLiveScope{reference: reference, lease: lease, automatic: gate.automatic}
		c.liveByNode[key] = lease.ScopeID()
		lease.Release()
		return nil
	}); err != nil {
		return currentNodeAdmissionError{
			cause:    c.discardAdmission(reference, key, lease, err),
			admitted: true,
		}
	}
	if !start.automatic {
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
	delete(c.live, lease.ScopeID())
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
		if start.automatic {
			err = c.queueAutomaticStartLocked(start.reference)
		} else {
			err = c.queueExplicitStartLocked(start.reference)
		}
		if err != nil {
			c.workerErr = errors.Join(c.workerErr, err)
		}
	}
	c.mu.Unlock()
	c.wakeAdmissionWorker()
}

func (c *CurrentNodeController) queueExplicitStartLocked(reference workflow.CurrentNodeReference) error {
	key, err := reference.Key()
	if err != nil {
		return err
	}
	if c.currentNodeOwnedLocked(key) {
		return nil
	}
	c.explicitQueue = append(c.explicitQueue, reference)
	c.explicitQueued[key] = struct{}{}
	c.wakeAdmissionWorker()
	return nil
}

func (c *CurrentNodeController) queueAutomaticStartLocked(reference workflow.CurrentNodeReference) error {
	key, err := reference.Key()
	if err != nil {
		return err
	}
	if c.currentNodeOwnedLocked(key) {
		return nil
	}
	c.automaticQueue = append(c.automaticQueue, CurrentNodeAutomaticIntent{CurrentNode: reference})
	c.queued[key] = struct{}{}
	c.wakeAdmissionWorker()
	return nil
}

func (c *CurrentNodeController) currentNodeOwnedLocked(key workflow.CurrentNodeReferenceKey) bool {
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
			errors.Is(err, sessionruntime.ErrExecutionNoLongerLive) ||
			errors.Is(err, ErrTaskExecutionNotQuiescent) ||
			interrupted {
			return
		}
		var failure currentNodeAdmissionError
		if !errors.As(err, &failure) {
			failure = currentNodeAdmissionError{cause: err}
		}
		c.handleAdmissionFailure(start.reference, failure.admitted, failure.cause)
	}
}

func (c *CurrentNodeController) takeExplicitStart() (currentNodeQueuedStart, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.inFlightAdmissionCountLocked(false) >= explicitAdmissionConcurrency || len(c.explicitQueue) == 0 {
		return currentNodeQueuedStart{}, false
	}
	reference := c.explicitQueue[0]
	c.explicitQueue = c.explicitQueue[1:]
	key, err := reference.Key()
	if err != nil {
		panic(fmt.Sprintf("take explicit current node start: %v", err))
	}
	delete(c.explicitQueued, key)
	start := currentNodeQueuedStart{reference: reference, done: make(chan struct{})}
	c.explicitReservations[key] = start
	c.admissionWorkers[key] = start
	c.admissionWG.Add(1)
	return start, true
}

func (c *CurrentNodeController) takeAutomaticIntent() (currentNodeQueuedStart, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.automaticActiveLocked() >= c.automaticConcurrency || len(c.automaticQueue) == 0 {
		return currentNodeQueuedStart{}, false
	}
	index := 0
	if c.lastAutomaticTask != nil {
		for candidateIndex, candidate := range c.automaticQueue {
			if candidate.CurrentNode.TaskID == *c.lastAutomaticTask {
				index = candidateIndex
				break
			}
		}
	}
	intent := c.automaticQueue[index]
	c.automaticQueue = append(c.automaticQueue[:index], c.automaticQueue[index+1:]...)
	key, err := intent.CurrentNode.Key()
	if err != nil {
		panic(fmt.Sprintf("take automatic current node intent: %v", err))
	}
	delete(c.queued, key)
	start := currentNodeQueuedStart{reference: intent.CurrentNode, automatic: true, done: make(chan struct{})}
	c.automaticReservations[key] = start
	c.admissionWorkers[key] = start
	c.admissionWG.Add(1)
	taskID := intent.CurrentNode.TaskID
	c.lastAutomaticTask = &taskID
	return start, true
}

func (c *CurrentNodeController) automaticActiveLocked() int {
	active := c.inFlightAdmissionCountLocked(true)
	for _, live := range c.live {
		if live.automatic {
			active++
		}
	}
	return active
}

func (c *CurrentNodeController) inFlightAdmissionCountLocked(automatic bool) int {
	count := 0
	for _, start := range c.admissionWorkers {
		if start.automatic == automatic {
			count++
		}
	}
	return count
}

func (c *CurrentNodeController) admissionReservationLocked(key workflow.CurrentNodeReferenceKey, automatic bool) (currentNodeQueuedStart, bool) {
	if automatic {
		start, exists := c.automaticReservations[key]
		return start, exists
	}
	start, exists := c.explicitReservations[key]
	return start, exists
}

func (c *CurrentNodeController) deleteAdmissionReservationLocked(key workflow.CurrentNodeReferenceKey, automatic bool) {
	if automatic {
		delete(c.automaticReservations, key)
		return
	}
	delete(c.explicitReservations, key)
}

func (c *CurrentNodeController) releaseReservation(key workflow.CurrentNodeReferenceKey, automatic bool) {
	c.mu.Lock()
	_, reserved := c.admissionReservationLocked(key, automatic)
	c.deleteAdmissionReservationLocked(key, automatic)
	closed := c.closed
	c.mu.Unlock()
	if (reserved || automatic) && !closed {
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
	if !exists || current.done != start.done || current.automatic != start.automatic {
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

func (c *CurrentNodeController) handleAdmissionFailure(reference workflow.CurrentNodeReference, admitted bool, cause error) {
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return
	}
	cleanupCtx, cancel := context.WithTimeout(context.Background(), interruptCleanupTimeout)
	defer cancel()
	err := c.permit.Run(cleanupCtx, func(ctx context.Context) error {
		interrupt := c.store.InterruptCurrentNode
		if admitted {
			interrupt = c.store.InterruptAdmittedCurrentNode
		}
		return interrupt(ctx, reference, "workflow_runtime_start_failed", workflow.CurrentNodeInterruptionDetail{
			Code:   "workflow_runtime_start_failed",
			Fields: map[string]string{"error": cause.Error()},
		})
	})
	if err == nil {
		c.publishPendingInterruptedCurrentNode(cleanupCtx, reference, "workflow_runtime_start_failed")
		return
	}
	if errors.Is(err, sql.ErrNoRows) {
		return
	}
	c.mu.Lock()
	c.workerErr = errors.Join(c.workerErr, cause, err)
	c.mu.Unlock()
}

func currentNodeAutomaticIntents(references []workflow.CurrentNodeReference) ([]CurrentNodeAutomaticIntent, error) {
	intents := make([]CurrentNodeAutomaticIntent, 0, len(references))
	seen := make(map[workflow.CurrentNodeReferenceKey]struct{}, len(references))
	for index, reference := range references {
		key, err := reference.Key()
		if err != nil {
			return nil, fmt.Errorf("automatic successor current node at index %d: %w", index, err)
		}
		if _, exists := seen[key]; exists {
			return nil, fmt.Errorf("automatic successor current node at index %d is duplicated", index)
		}
		seen[key] = struct{}{}
		intents = append(intents, CurrentNodeAutomaticIntent{CurrentNode: reference})
	}
	return intents, nil
}

func automaticQueuedStarts(intents []CurrentNodeAutomaticIntent) []currentNodeQueuedStart {
	starts := make([]currentNodeQueuedStart, 0, len(intents))
	for _, intent := range intents {
		starts = append(starts, currentNodeQueuedStart{
			reference: intent.CurrentNode,
			automatic: true,
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
		starts = append(starts, currentNodeQueuedStart{reference: currentNode.Reference})
	}
	return starts, nil
}
