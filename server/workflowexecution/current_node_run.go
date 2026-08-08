package workflowexecution

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"core/server/sessionruntime"
	"core/server/workflow"
	"core/server/workflowruntime"
	"core/server/workflowstore"
	"core/shared/runtimeids"
)

type currentNodeRunPhase uint8

const (
	currentNodeRunQueued currentNodeRunPhase = iota + 1
	currentNodeRunReserved
	currentNodeRunGated
	currentNodeRunPublishing
	currentNodeRunRunning
)

type currentNodeRunDisposition uint8

const (
	currentNodeRunDispositionQueued currentNodeRunDisposition = iota + 1
	currentNodeRunDispositionPublishing
	currentNodeRunDispositionRunning
	currentNodeRunDispositionStopped
)

type currentNodeRunStopReason uint8

const (
	currentNodeRunStopInterrupted currentNodeRunStopReason = iota + 1
	currentNodeRunStopAdmissionFailed
	currentNodeRunStopSourceRetired
	currentNodeRunStopControllerClosed
	currentNodeRunStopWorkerFailed
)

type currentNodeRunStop struct {
	reason currentNodeRunStopReason
	cause  error
}

type currentNodeAssignmentReadiness uint8

const (
	currentNodeAssignmentPending currentNodeAssignmentReadiness = iota + 1
	currentNodeAssignmentReady
)

type currentNodeAgentActivationResult struct {
	resource runtimeids.SessionResourceRef
	scopeID  runtimeids.ExecutionScopeID
}

type currentNodeAgentActivationOutcome struct {
	ready  chan struct{}
	once   sync.Once
	result currentNodeAgentActivationResult
	err    error
}

func newCurrentNodeAgentActivationOutcome() *currentNodeAgentActivationOutcome {
	return &currentNodeAgentActivationOutcome{ready: make(chan struct{})}
}

func (o *currentNodeAgentActivationOutcome) resolve(result currentNodeAgentActivationResult, err error) {
	o.once.Do(func() {
		o.result = result
		o.err = err
		close(o.ready)
	})
}

func (o *currentNodeAgentActivationOutcome) await(ctx context.Context) (currentNodeAgentActivationResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-o.ready:
		return o.result, o.err
	case <-ctx.Done():
		return currentNodeAgentActivationResult{}, context.Cause(ctx)
	}
}

type currentNodeExactPublicationOutcome struct {
	ready chan struct{}
	once  sync.Once
	err   error
}

func newCurrentNodeExactPublicationOutcome() *currentNodeExactPublicationOutcome {
	return &currentNodeExactPublicationOutcome{ready: make(chan struct{})}
}

func (o *currentNodeExactPublicationOutcome) resolve(err error) {
	o.once.Do(func() {
		o.err = err
		close(o.ready)
	})
}

func (o *currentNodeExactPublicationOutcome) await(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-o.ready:
		return o.err
	case <-ctx.Done():
		return context.Cause(ctx)
	}
}

type currentNodeRun struct {
	reference                    workflow.CurrentNodeReference
	nodeKind                     workflow.NodeKind
	policy                       currentNodeAdmissionPolicy
	preparation                  TaskStartPreparation
	taskPromptDelivery           workflowruntime.TaskPromptDelivery
	assignmentEnsure             CurrentNodeAssignmentEnsure
	assignmentReadiness          currentNodeAssignmentReadiness
	resultFinalizationDependency *runtimeids.ExecutionScopeID
	agentCapacityLease           *currentNodeAgentCapacityLease
	phase                        currentNodeRunPhase
	disposition                  currentNodeRunDisposition
	stop                         *currentNodeRunStop
	admissionDone                chan struct{}
	admissionContext             context.Context
	admissionCancel              context.CancelCauseFunc
	executionLease               *sessionruntime.WorkflowExecutionLease
	retainedSessionID            *runtimeids.SessionID
	agentActivation              *currentNodeAgentActivationOutcome
	pendingCompletionRequest     *workflowstore.CurrentNodeCompletionRequest
	finalizationPublishing       bool
	finalizationPublicationDone  chan struct{}
	exactPublication             *currentNodeExactPublicationOutcome
}

// currentNodeQueuedStart is a pre-registry launch description. Once accepted,
// the registry-owned currentNodeRun is the only execution owner.
type currentNodeQueuedStart = currentNodeRun

func newCurrentNodeRun(
	reference workflow.CurrentNodeReference,
	nodeKind workflow.NodeKind,
	policy currentNodeAdmissionPolicy,
) *currentNodeRun {
	run := &currentNodeRun{
		reference:           reference,
		nodeKind:            nodeKind,
		policy:              policy,
		assignmentReadiness: currentNodeAssignmentPending,
		phase:               currentNodeRunQueued,
		disposition:         currentNodeRunDispositionQueued,
		exactPublication:    newCurrentNodeExactPublicationOutcome(),
	}
	if nodeKind == workflow.NodeKindAgent {
		run.agentActivation = newCurrentNodeAgentActivationOutcome()
	}
	return run
}

func (r *currentNodeRun) transitionDisposition(
	next currentNodeRunDisposition,
	stop *currentNodeRunStop,
) error {
	if r == nil {
		return errors.New("current node Run is required")
	}
	switch next {
	case currentNodeRunDispositionPublishing:
		if r.disposition != currentNodeRunDispositionQueued {
			return fmt.Errorf("current node %v Run cannot transition from disposition %d to publishing", r.reference, r.disposition)
		}
		if stop != nil {
			return errors.New("publishing Run disposition cannot carry a stop")
		}
	case currentNodeRunDispositionRunning:
		if r.disposition != currentNodeRunDispositionPublishing {
			return fmt.Errorf("current node %v Run cannot transition from disposition %d to running", r.reference, r.disposition)
		}
		if stop != nil {
			return errors.New("running Run disposition cannot carry a stop")
		}
	case currentNodeRunDispositionStopped:
		if r.disposition != currentNodeRunDispositionQueued &&
			r.disposition != currentNodeRunDispositionPublishing &&
			r.disposition != currentNodeRunDispositionRunning {
			return fmt.Errorf("current node %v Run cannot transition from disposition %d to stopped", r.reference, r.disposition)
		}
		if stop == nil {
			return errors.New("stopped Run disposition requires a typed reason")
		}
		if !stop.reason.valid() {
			return fmt.Errorf("stopped Run disposition has invalid reason %d", stop.reason)
		}
	default:
		return fmt.Errorf("current node %v Run has invalid disposition %d", r.reference, next)
	}
	r.disposition = next
	if stop == nil {
		r.stop = nil
	} else {
		value := *stop
		r.stop = &value
	}
	if next == currentNodeRunDispositionStopped && r.exactPublication != nil {
		r.exactPublication.resolve(stop.cause)
	}
	return nil
}

func (r *currentNodeRun) rollbackRunningPublication() error {
	if r == nil {
		return errors.New("current node Run is required")
	}
	if r.disposition != currentNodeRunDispositionPublishing {
		return fmt.Errorf(
			"current node %v Run cannot roll back publication from disposition %d",
			r.reference,
			r.disposition,
		)
	}
	r.disposition = currentNodeRunDispositionQueued
	r.stop = nil
	return nil
}

func (r *currentNodeRun) stopOnce(reason currentNodeRunStopReason, cause error) bool {
	if r.disposition == currentNodeRunDispositionStopped {
		return false
	}
	if err := r.transitionDisposition(currentNodeRunDispositionStopped, &currentNodeRunStop{
		reason: reason,
		cause:  cause,
	}); err != nil {
		panic(fmt.Sprintf("stop current node Run: %v", err))
	}
	if r.admissionCancel != nil && reason.cancelsAdmission() {
		r.admissionCancel(cause)
	}
	if r.agentActivation != nil {
		r.agentActivation.resolve(currentNodeAgentActivationResult{}, cause)
	}
	return true
}

func (r currentNodeRunStopReason) valid() bool {
	switch r {
	case currentNodeRunStopInterrupted,
		currentNodeRunStopAdmissionFailed,
		currentNodeRunStopSourceRetired,
		currentNodeRunStopControllerClosed,
		currentNodeRunStopWorkerFailed:
		return true
	default:
		return false
	}
}

func (r currentNodeRunStopReason) cancelsAdmission() bool {
	switch r {
	case currentNodeRunStopInterrupted, currentNodeRunStopControllerClosed, currentNodeRunStopWorkerFailed:
		return true
	case currentNodeRunStopAdmissionFailed, currentNodeRunStopSourceRetired:
		return false
	default:
		panic(fmt.Sprintf("current node Run has invalid stop reason %d", r))
	}
}

func (r *currentNodeRun) key() (workflow.CurrentNodeReferenceKey, error) {
	if r == nil {
		return nil, errors.New("current node Run is required")
	}
	return r.reference.Key()
}

func (r *currentNodeRun) joinAgentActivation(
	sessionID runtimeids.SessionID,
) (*currentNodeAgentActivationOutcome, error) {
	if r == nil || r.nodeKind != workflow.NodeKindAgent || r.agentActivation == nil {
		return nil, errors.New("current node Run is not an Agent Run")
	}
	if sessionID.IsZero() {
		return nil, errors.New("retained Session id is required")
	}
	if r.retainedSessionID != nil && *r.retainedSessionID != sessionID {
		return nil, fmt.Errorf(
			"Agent Run for current node %v belongs to retained Session %s, not %s",
			r.reference,
			r.retainedSessionID.String(),
			sessionID.String(),
		)
	}
	if r.retainedSessionID == nil {
		retained := sessionID
		r.retainedSessionID = &retained
	}
	return r.agentActivation, nil
}

type currentNodeRunRegistry struct {
	byCurrentNode map[workflow.CurrentNodeReferenceKey]*currentNodeRun
}

type currentNodeRunConflictError struct {
	reference           workflow.CurrentNodeReference
	existingKind        workflow.NodeKind
	requestedKind       workflow.NodeKind
	existingPolicy      currentNodeAdmissionPolicy
	requestedPolicy     currentNodeAdmissionPolicy
	existingDependency  *runtimeids.ExecutionScopeID
	requestedDependency *runtimeids.ExecutionScopeID
}

func (e *currentNodeRunConflictError) Error() string {
	return fmt.Sprintf(
		"current node %v already has a Run (kind=%q policy=%d source_finalization=%s); requested kind=%q policy=%d source_finalization=%s",
		e.reference,
		e.existingKind,
		e.existingPolicy,
		executionScopeIDOrAbsent(e.existingDependency),
		e.requestedKind,
		e.requestedPolicy,
		executionScopeIDOrAbsent(e.requestedDependency),
	)
}

func newCurrentNodeRunRegistry() currentNodeRunRegistry {
	return currentNodeRunRegistry{
		byCurrentNode: make(map[workflow.CurrentNodeReferenceKey]*currentNodeRun),
	}
}

func (r *currentNodeRunRegistry) register(run *currentNodeRun) (*currentNodeRun, bool, error) {
	key, err := run.key()
	if err != nil {
		return nil, false, err
	}
	if run.nodeKind != workflow.NodeKindAgent && run.nodeKind != workflow.NodeKindScript {
		return nil, false, fmt.Errorf("current node Run has non-executable Node kind %q", run.nodeKind)
	}
	if existing := r.byCurrentNode[key]; existing != nil {
		return nil, false, &currentNodeRunConflictError{
			reference:           run.reference,
			existingKind:        existing.nodeKind,
			requestedKind:       run.nodeKind,
			existingPolicy:      existing.policy,
			requestedPolicy:     run.policy,
			existingDependency:  cloneExecutionScopeID(existing.resultFinalizationDependency),
			requestedDependency: cloneExecutionScopeID(run.resultFinalizationDependency),
		}
	}
	r.byCurrentNode[key] = run
	return run, true, nil
}

func (r *currentNodeRunRegistry) get(key workflow.CurrentNodeReferenceKey) (*currentNodeRun, bool) {
	run, exists := r.byCurrentNode[key]
	return run, exists
}

func (r *currentNodeRunRegistry) delete(key workflow.CurrentNodeReferenceKey) {
	delete(r.byCurrentNode, key)
}

func (r *currentNodeRunRegistry) clear() {
	r.byCurrentNode = make(map[workflow.CurrentNodeReferenceKey]*currentNodeRun)
}

func (c *CurrentNodeController) runByScopeLocked(
	scopeID runtimeids.ExecutionScopeID,
) (*currentNodeRun, workflow.CurrentNodeReferenceKey, bool) {
	key, exists := c.exactScopes[scopeID]
	if !exists {
		return nil, nil, false
	}
	run, exists := c.runs.get(key)
	if !exists {
		panic("exact-scope index lost its current node Run")
	}
	return run, key, true
}

func (c *CurrentNodeController) runByExecutionScopeLocked(
	scopeID runtimeids.ExecutionScopeID,
) (*currentNodeRun, workflow.CurrentNodeReferenceKey, bool) {
	if run, key, exact := c.runByScopeLocked(scopeID); exact {
		return run, key, true
	}
	for key := range c.gates {
		run, exists := c.runs.get(key)
		if !exists {
			panic("admission gate lost its current node Run")
		}
		if run.executionLease != nil && run.executionLease.ScopeID() == scopeID {
			return run, key, true
		}
	}
	return nil, nil, false
}

func (c *CurrentNodeController) joinAgentRunActivation(
	reference workflow.CurrentNodeReference,
	sessionID runtimeids.SessionID,
) (*currentNodeAgentActivationOutcome, error) {
	if c == nil {
		return nil, errors.New("current node workflow controller is required")
	}
	key, err := reference.Key()
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	run, exists := c.runs.get(key)
	if !exists {
		return nil, sessionruntime.ErrExecutionNoLongerLive
	}
	outcome, err := run.joinAgentActivation(sessionID)
	if err != nil {
		return nil, err
	}
	return outcome, nil
}

func (c *CurrentNodeController) registerHeldRunsLocked(
	sourceScope runtimeids.ExecutionScopeID,
	starts []currentNodeQueuedStart,
) error {
	if c.closed {
		return errors.New("current node workflow controller is closed")
	}
	keys := make([]workflow.CurrentNodeReferenceKey, 0, len(starts))
	for index := range starts {
		dependency := sourceScope
		starts[index].resultFinalizationDependency = &dependency
		run, _, err := c.runs.register(&starts[index])
		if err != nil {
			return err
		}
		keys = append(keys, mustCurrentNodeRunKey(run))
	}
	c.heldStarts[sourceScope] = append(c.heldStarts[sourceScope], keys...)
	return nil
}

func currentNodeRunCreationDelta(
	starts []currentNodeQueuedStart,
) (workflowstore.TaskLifecycleDelta, error) {
	taskID, err := currentNodeStartsTaskID(starts)
	if err != nil {
		return workflowstore.TaskLifecycleDelta{}, err
	}
	changes := make([]workflowstore.LifecycleRunDelta, 0, len(starts))
	for _, start := range starts {
		changes = append(changes, workflowstore.LifecycleRunDelta{
			CurrentNode: start.reference,
			Expect:      workflowstore.LifecycleFieldAbsent,
			Next:        workflowstore.LifecycleFieldPresent,
		})
	}
	return workflowstore.NewTaskLifecycleDelta(taskID, changes, nil)
}

func currentNodeCompletionLifecycleDelta(
	source workflow.CurrentNodeReference,
	sourceRun workflowstore.LifecycleFieldPresence,
	starts []currentNodeQueuedStart,
) (workflowstore.TaskLifecycleDelta, error) {
	changes := []workflowstore.LifecycleRunDelta{{
		CurrentNode: source,
		Expect:      sourceRun,
		Next:        sourceRun,
	}}
	for _, start := range starts {
		changes = append(changes, workflowstore.LifecycleRunDelta{
			CurrentNode: start.reference,
			Expect:      workflowstore.LifecycleFieldAbsent,
			Next:        workflowstore.LifecycleFieldPresent,
		})
	}
	return workflowstore.NewTaskLifecycleDelta(source.TaskID, changes, nil)
}

func currentNodeSuccessfulFinalizationLifecycleDelta(
	source workflow.CurrentNodeReference,
	scopeID runtimeids.ExecutionScopeID,
	starts []currentNodeQueuedStart,
) (workflowstore.TaskLifecycleDelta, error) {
	changes := []workflowstore.LifecycleRunDelta{{
		CurrentNode: source,
		Expect:      workflowstore.LifecycleFieldPresent,
		Next:        workflowstore.LifecycleFieldAbsent,
	}}
	for _, start := range starts {
		changes = append(changes, workflowstore.LifecycleRunDelta{
			CurrentNode: start.reference,
			Expect:      workflowstore.LifecycleFieldAbsent,
			Next:        workflowstore.LifecycleFieldPresent,
		})
	}
	return workflowstore.NewTaskLifecycleDelta(
		source.TaskID,
		changes,
		[]workflowstore.LifecycleExactDelta{{
			CurrentNode: source,
			ExpectScope: &scopeID,
		}},
	)
}

func quiescentCurrentNodeReplacementLifecycleDelta(
	mutation workflow.CurrentNodeMutationResult,
	starts []currentNodeQueuedStart,
) (workflowstore.TaskLifecycleDelta, error) {
	if len(mutation.Removed) == 0 {
		return workflowstore.TaskLifecycleDelta{}, errors.New("quiescent Current Node replacement requires removed Current Nodes")
	}
	taskID := mutation.Removed[0].TaskID
	changes := make([]workflowstore.LifecycleRunDelta, 0, len(mutation.Removed)+len(starts))
	for _, reference := range mutation.Removed {
		if reference.TaskID != taskID {
			return workflowstore.TaskLifecycleDelta{}, errors.New("quiescent Current Node replacement cannot cross Tasks")
		}
		changes = append(changes, workflowstore.LifecycleRunDelta{
			CurrentNode: reference,
			Expect:      workflowstore.LifecycleFieldAbsent,
			Next:        workflowstore.LifecycleFieldAbsent,
		})
	}
	for _, start := range starts {
		if start.reference.TaskID != taskID {
			return workflowstore.TaskLifecycleDelta{}, errors.New("quiescent Current Node replacement successor cannot cross Tasks")
		}
		changes = append(changes, workflowstore.LifecycleRunDelta{
			CurrentNode: start.reference,
			Expect:      workflowstore.LifecycleFieldAbsent,
			Next:        workflowstore.LifecycleFieldPresent,
		})
	}
	return workflowstore.NewTaskLifecycleDelta(taskID, changes, nil)
}

func (c *CurrentNodeController) rollbackHeldRunCreations(
	sourceScope runtimeids.ExecutionScopeID,
	cause error,
) {
	c.mu.Lock()
	keys := append([]workflow.CurrentNodeReferenceKey(nil), c.heldStarts[sourceScope]...)
	delete(c.heldStarts, sourceScope)
	c.mu.Unlock()
	c.discardRuns(keys, currentNodeRunStopWorkerFailed, cause)
}

func cloneExecutionScopeID(source *runtimeids.ExecutionScopeID) *runtimeids.ExecutionScopeID {
	if source == nil {
		return nil
	}
	cloned := *source
	return &cloned
}

func executionScopeIDOrAbsent(scopeID *runtimeids.ExecutionScopeID) string {
	if scopeID == nil {
		return "<absent>"
	}
	return scopeID.String()
}

func mustCurrentNodeRunKey(run *currentNodeRun) workflow.CurrentNodeReferenceKey {
	key, err := run.key()
	if err != nil {
		panic(fmt.Sprintf("current node Run has invalid reference: %v", err))
	}
	return key
}

func (c *CurrentNodeController) runCandidates(
	keys []workflow.CurrentNodeReferenceKey,
) []currentNodeQueuedStart {
	c.mu.Lock()
	defer c.mu.Unlock()
	starts := make([]currentNodeQueuedStart, 0, len(keys))
	for _, key := range keys {
		run, exists := c.runs.get(key)
		if !exists {
			panic("published held Run disappeared before becoming schedulable")
		}
		if run.disposition != currentNodeRunDispositionQueued {
			panic(fmt.Sprintf(
				"published held Run has invalid disposition before becoming schedulable: current_node=%v disposition=%d",
				run.reference,
				run.disposition,
			))
		}
		starts = append(starts, *run)
	}
	return starts
}

func (c *CurrentNodeController) enqueueHeldRuns(keys []workflow.CurrentNodeReferenceKey) {
	if len(keys) == 0 {
		return
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	for _, key := range keys {
		run, exists := c.runs.get(key)
		if !exists {
			continue
		}
		run.resultFinalizationDependency = nil
		run.phase = currentNodeRunQueued
		if run.policy.isAutomatic() {
			c.automaticQueue.append(key, run)
			c.queued[key] = struct{}{}
		} else {
			c.explicitQueue = append(c.explicitQueue, key)
			c.explicitQueued[key] = struct{}{}
		}
	}
	c.mu.Unlock()
	c.wakeAdmissionWorker()
}

func (c *CurrentNodeController) discardRuns(
	keys []workflow.CurrentNodeReferenceKey,
	reason currentNodeRunStopReason,
	cause error,
) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, key := range keys {
		run, exists := c.runs.get(key)
		if !exists {
			continue
		}
		if run.agentActivation != nil {
			run.agentActivation.resolve(currentNodeAgentActivationResult{}, cause)
		}
		run.stopOnce(reason, cause)
		if _, admitting := c.admissionWorkers[key]; !admitting {
			c.runs.delete(key)
		}
	}
}

func (c *CurrentNodeController) continueHeldRunAssignments(
	keys []workflow.CurrentNodeReferenceKey,
	starts []currentNodeQueuedStart,
) {
	taskID, err := currentNodeStartsTaskID(starts)
	if err != nil {
		panic(fmt.Sprintf("continue held current node Run assignments: %v", err))
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		if err := c.lifecycle.Run(context.Background(), taskID, func(context.Context) error {
			c.discardRuns(keys, currentNodeRunStopControllerClosed, errors.New("current node workflow controller is closed"))
			return nil
		}); err != nil {
			panic(fmt.Sprintf("discard closed held current node Runs: %v", err))
		}
		return
	}
	c.workerWG.Add(1)
	c.mu.Unlock()
	go func() {
		defer c.workerWG.Done()
		outcome := waitCurrentNodeAssignmentEnsures(c.workerContext, starts)
		if context.Cause(c.workerContext) != nil {
			if err := c.lifecycle.Run(context.Background(), taskID, func(context.Context) error {
				c.discardRuns(keys, currentNodeRunStopControllerClosed, context.Cause(c.workerContext))
				return nil
			}); err != nil {
				panic(fmt.Sprintf("discard cancelled held current node Runs: %v", err))
			}
			return
		}
		if outcome.err != nil {
			c.handleCurrentNodeStartFailures(outcome.committed, false, outcome.err)
			if err := c.lifecycle.Run(context.Background(), taskID, func(context.Context) error {
				c.discardRuns(keys, currentNodeRunStopWorkerFailed, outcome.err)
				return nil
			}); err != nil {
				panic(fmt.Sprintf("discard failed held current node Runs: %v", err))
			}
			return
		}
		if err := c.lifecycle.Run(c.workerContext, taskID, func(context.Context) error {
			c.enqueueHeldRuns(keys)
			return nil
		}); err != nil && context.Cause(c.workerContext) == nil {
			panic(fmt.Sprintf("transfer held current node Runs: %v", err))
		}
	}()
}
