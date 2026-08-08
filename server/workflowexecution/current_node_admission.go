package workflowexecution

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"core/server/session"
	"core/server/sessionruntime"
	askquestion "core/server/tools"
	"core/server/workflow"
	"core/server/workflowruntime"
	"core/server/workflowstore"
	"core/shared/runtimeids"
)

func (c *CurrentNodeController) PromptPendingScope(
	scope sessionruntime.ExecutionScope,
	request askquestion.AskQuestionRequest,
	createdAt time.Time,
) error {
	if c == nil {
		return errors.New("current node workflow controller is required")
	}
	if _, workflowScoped := scope.Workflow(); !workflowScoped {
		return nil
	}
	prompt := workflowstore.LifecyclePendingPrompt{
		ID:          request.ID,
		CreatedAt:   createdAt,
		Question:    request.Question,
		Suggestions: append([]string(nil), request.Suggestions...),
	}
	if request.Approval {
		prompt.Kind = workflowstore.LifecyclePendingPromptSessionApproval
		for _, option := range request.ApprovalOptions {
			prompt.ApprovalDecisions = append(
				prompt.ApprovalDecisions,
				workflowstore.LifecycleApprovalDecision(option.Decision),
			)
		}
	} else {
		prompt.Kind = workflowstore.LifecyclePendingPromptQuestion
		if request.RecommendedOptionIndex > 0 {
			recommended := request.RecommendedOptionIndex
			prompt.RecommendedOptionIndex = &recommended
		}
	}
	return c.publication.PublishExactPromptPending(
		context.Background(),
		scope.ID(),
		prompt,
	)
}

func (c *CurrentNodeController) PromptResolvedScope(
	scope sessionruntime.ExecutionScope,
	requestID string,
) error {
	if c == nil {
		return errors.New("current node workflow controller is required")
	}
	if _, workflowScoped := scope.Workflow(); !workflowScoped {
		return nil
	}
	err := c.publication.PublishExactPromptResolved(
		context.Background(),
		scope.ID(),
		requestID,
	)
	if errors.Is(err, workflowstore.ErrLifecycleExactNotPublished) {
		return nil
	}
	return err
}

const (
	explicitAdmissionConcurrency                                               = 8
	reasonCurrentNodeRuntimeStartFailed workflow.CurrentNodeInterruptionReason = "workflow_runtime_start_failed"
)

var errCurrentNodeRunningActivationInterrupted = errors.New("current node running activation was interrupted")

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

type currentNodeAdmissionError struct {
	cause    error
	admitted bool
}

type stagedCurrentNodeRun struct {
	key     workflow.CurrentNodeReferenceKey
	run     *currentNodeRun
	created bool
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

type pendingCurrentNodeAssignmentEnsure struct {
	ready  chan struct{}
	once   sync.Once
	ensure CurrentNodeAssignmentEnsure
	err    error
}

func newPendingCurrentNodeAssignmentEnsure() *pendingCurrentNodeAssignmentEnsure {
	return &pendingCurrentNodeAssignmentEnsure{ready: make(chan struct{})}
}

func (s *pendingCurrentNodeAssignmentEnsure) resolve(ensure CurrentNodeAssignmentEnsure, err error) {
	s.once.Do(func() {
		s.ensure = ensure
		s.err = err
		close(s.ready)
	})
}

func (s *pendingCurrentNodeAssignmentEnsure) Wait(ctx context.Context) (session.CommitReceipt, error) {
	ensure, err := s.resolved(ctx)
	if err != nil {
		return session.CommitReceipt{}, err
	}
	return ensure.Wait(ctx)
}

func (s *pendingCurrentNodeAssignmentEnsure) resolved(ctx context.Context) (CurrentNodeAssignmentEnsure, error) {
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
	if s.ensure == nil {
		return nil, errors.New("resolved current node assignment ensure is absent")
	}
	return s.ensure, nil
}

func resolvedCurrentNodeAssignmentEnsure(ctx context.Context, ensure CurrentNodeAssignmentEnsure) (CurrentNodeAssignmentEnsure, error) {
	if ensure == nil {
		return nil, nil
	}
	if pending, ok := ensure.(*pendingCurrentNodeAssignmentEnsure); ok {
		var err error
		ensure, err = pending.resolved(ctx)
		if err != nil {
			return nil, err
		}
	}
	receipt, err := ensure.Wait(ctx)
	if err != nil {
		return nil, err
	}
	if !receipt.Committed {
		return nil, errors.New("current node assignment was not committed")
	}
	return ensure, nil
}

func (e currentNodeAdmissionError) Error() string {
	return e.cause.Error()
}

func (e currentNodeAdmissionError) Unwrap() error {
	return e.cause
}

func (c *CurrentNodeController) admit(ctx context.Context, key workflow.CurrentNodeReferenceKey) (err error) {
	if c == nil {
		return errors.New("current node workflow controller is required")
	}
	c.mu.Lock()
	run, exists := c.runs.get(key)
	if !exists {
		c.mu.Unlock()
		return sessionruntime.ErrExecutionNoLongerLive
	}
	if run.disposition != currentNodeRunDispositionQueued {
		c.mu.Unlock()
		return sessionruntime.ErrExecutionNoLongerLive
	}
	reference := run.reference
	policy := run.policy
	preparation := run.preparation
	taskPromptDelivery := run.taskPromptDelivery
	assignmentEnsure := run.assignmentEnsure
	agentCapacityLease := run.agentCapacityLease
	c.mu.Unlock()
	if err := reference.Validate(); err != nil {
		return err
	}
	if preparation != nil {
		if err := preparation(ctx); err != nil {
			return err
		}
	}
	if assignmentEnsure == nil {
		assignmentEnsure, err = c.ensureAssignment(ctx, reference, taskPromptDelivery)
		if err != nil {
			return err
		}
	}
	assignmentEnsure, err = resolvedCurrentNodeAssignmentEnsure(ctx, assignmentEnsure)
	if err != nil {
		return err
	}
	c.mu.Lock()
	current, currentExists := c.runs.get(key)
	if !currentExists || current != run || run.disposition != currentNodeRunDispositionQueued {
		c.mu.Unlock()
		return sessionruntime.ErrExecutionNoLongerLive
	}
	run.assignmentEnsure = assignmentEnsure
	run.assignmentReadiness = currentNodeAssignmentReady
	if c.closed {
		c.mu.Unlock()
		return errors.New("current node workflow controller is closed")
	}
	c.mu.Unlock()

	var lease sessionruntime.WorkflowExecutionLease
retryAdmission:
	if err := c.lifecycle.Run(ctx, reference.TaskID, func(ctx context.Context) error {
		c.mu.Lock()
		if err := c.ensureTaskAvailableLocked(reference.TaskID); err != nil {
			c.mu.Unlock()
			return err
		}
		current, currentExists := c.runs.get(key)
		if !currentExists || current != run || run.disposition != currentNodeRunDispositionQueued {
			c.mu.Unlock()
			return sessionruntime.ErrExecutionNoLongerLive
		}
		if !c.admissionReservationLocked(key, policy) {
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
		current, currentExists = c.runs.get(key)
		if !currentExists || current != run || run.disposition != currentNodeRunDispositionQueued {
			c.mu.Unlock()
			next.Cancel()
			return sessionruntime.ErrExecutionNoLongerLive
		}
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
		if run.phase == currentNodeRunRunning {
			c.mu.Unlock()
			next.Cancel()
			return fmt.Errorf("current node %v already has a live execution scope", reference)
		}
		if _, stopping := c.stopping[next.ScopeID()]; stopping {
			c.mu.Unlock()
			next.Cancel()
			return sessionruntime.ErrExecutionNoLongerLive
		}
		if !c.admissionReservationLocked(key, policy) {
			c.mu.Unlock()
			next.Cancel()
			return sessionruntime.ErrExecutionNoLongerLive
		}
		c.deleteAdmissionReservationLocked(key, policy)
		c.transitionAgentCapacityLocked(
			agentCapacityLease,
			currentNodeAgentCapacityReservation,
			currentNodeAgentCapacityGate,
		)
		// The gate precedes the durable restart marker, so any conflicting
		// lifecycle mutation sees admission before slow runner preparation.
		c.gates[key] = struct{}{}
		run.phase = currentNodeRunGated
		run.executionLease = &next
		c.mu.Unlock()

		if err := c.publication.PublishCurrentNodeAdmission(ctx, reference); err != nil {
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
	if err := c.runner.StartCurrentNode(ctx, reference, taskPromptDelivery, assignmentEnsure, lease, c); err != nil {
		return currentNodeAdmissionError{
			cause:    err,
			admitted: true,
		}
	}
	var handle sessionruntime.ExecutionHandle
	if err := c.lifecycle.Run(ctx, reference.TaskID, func(context.Context) error {
		var ok bool
		handle, ok = c.authority.ExecutionByScope(lease.ScopeID())
		if !ok {
			return errors.New("current node runner returned without its exact live scope")
		}
		scopeRef, ok := handle.Scope().Workflow()
		if !ok || !scopeRef.CurrentNode.Equal(reference) {
			return errors.New("current node runner started a mismatched workflow scope")
		}
		c.mu.Lock()
		_, gated := c.gates[key]
		run, exists := c.runs.get(key)
		if exists &&
			run.disposition == currentNodeRunDispositionStopped &&
			run.stop != nil &&
			run.stop.reason == currentNodeRunStopSourceRetired {
			c.mu.Unlock()
			return errors.New("current node exact scope finalized before live registration")
		}
		if !gated ||
			!exists ||
			run.disposition != currentNodeRunDispositionQueued ||
			run.executionLease == nil ||
			run.executionLease.ScopeID() != lease.ScopeID() {
			c.mu.Unlock()
			return errors.New("current node admission gate was replaced before live scope registration")
		}
		if err := c.ensureTaskAvailableLocked(reference.TaskID); err != nil {
			c.mu.Unlock()
			return err
		}
		if _, stopping := c.stopping[lease.ScopeID()]; stopping {
			c.mu.Unlock()
			return sessionruntime.ErrExecutionNoLongerLive
		}
		lease.Release()
		c.mu.Unlock()
		return nil
	}); err != nil {
		return currentNodeAdmissionError{
			cause:    err,
			admitted: true,
		}
	}
	if err := handle.PublishRunning(ctx, c); err != nil {
		if errors.Is(err, errCurrentNodeRunningActivationInterrupted) {
			return nil
		}
		return currentNodeAdmissionError{
			cause:    errors.Join(err, run.exactPublication.await(ctx)),
			admitted: true,
		}
	}
	if err := run.exactPublication.await(ctx); err != nil {
		return currentNodeAdmissionError{
			cause:    err,
			admitted: true,
		}
	}
	if !policy.isAutomatic() {
		c.wakeAdmissionWorker()
	}
	return nil
}

func (c *CurrentNodeController) PublishCurrentNodeRunningExecution(
	ctx context.Context,
	running sessionruntime.TaskExecution,
	activation sessionruntime.WorkflowRunningActivation,
) error {
	exact, err := lifecycleExactExecutionFromRunning(running)
	if err != nil {
		return err
	}
	return c.PublishCurrentNodeExactExecution(ctx, exact, activation)
}

func (c *CurrentNodeController) PublishWorkflowRunning(
	ctx context.Context,
	running sessionruntime.TaskExecution,
	activation sessionruntime.WorkflowRunningActivation,
) error {
	return c.PublishCurrentNodeRunningExecution(ctx, running, activation)
}

func lifecycleExactExecutionFromRunning(
	running sessionruntime.TaskExecution,
) (workflowstore.LifecycleExactExecution, error) {
	if running.Queued {
		return workflowstore.LifecycleExactExecution{}, errors.New("Authority reported a queued execution as running")
	}
	exact := workflowstore.LifecycleExactExecution{
		ProjectID:   running.Ref.ProjectID,
		WorkflowID:  running.Ref.WorkflowID,
		CurrentNode: running.Ref.CurrentNode,
		ScopeID:     running.ScopeID,
		Phase:       workflowstore.LifecycleExactExecutionRunning,
	}
	switch {
	case running.Agent != nil && running.Script == nil:
		exact.Agent = &workflowstore.LifecycleAgentExecutionTarget{
			SessionID: running.Agent.SessionID,
		}
	case running.Script != nil && running.Agent == nil:
		exact.Script = &workflowstore.LifecycleScriptExecutionTarget{
			Path: running.Script.Path,
		}
	default:
		return workflowstore.LifecycleExactExecution{}, errors.New("Authority running execution has an invalid target")
	}
	for _, prompt := range running.PendingPrompts {
		target := workflowstore.LifecyclePendingPrompt{ID: prompt.ID}
		switch prompt.Kind {
		case sessionruntime.PendingPromptKindQuestion:
			target.Kind = workflowstore.LifecyclePendingPromptQuestion
		case sessionruntime.PendingPromptKindSessionApproval:
			target.Kind = workflowstore.LifecyclePendingPromptSessionApproval
		default:
			return workflowstore.LifecycleExactExecution{}, errors.New("Authority running execution has an invalid pending prompt kind")
		}
		exact.PendingPrompts = append(exact.PendingPrompts, target)
	}
	return exact, nil
}

// PublishCurrentNodeExactExecution publishes one execution only after
// Authority has confirmed that its Agent loop or Script process is live.
func (c *CurrentNodeController) PublishCurrentNodeExactExecution(
	ctx context.Context,
	exact workflowstore.LifecycleExactExecution,
	activation sessionruntime.WorkflowRunningActivation,
) error {
	if c == nil {
		return errors.New("current node workflow controller is required")
	}
	if activation == nil {
		return errors.New("workflow running activation is required")
	}
	key, err := exact.CurrentNode.Key()
	if err != nil {
		return err
	}
	return c.lifecycle.Run(ctx, exact.CurrentNode.TaskID, func(ctx context.Context) error {
		c.mu.Lock()
		run, exists := c.runs.get(key)
		_, gated := c.gates[key]
		if !exists ||
			!gated ||
			run.disposition != currentNodeRunDispositionQueued ||
			run.executionLease == nil ||
			run.executionLease.ScopeID() != exact.ScopeID {
			c.mu.Unlock()
			return sessionruntime.ErrExecutionNoLongerLive
		}
		if err := c.ensureTaskAvailableLocked(exact.CurrentNode.TaskID); err != nil {
			c.mu.Unlock()
			return err
		}
		if run.agentActivation == nil && exact.Script == nil {
			c.mu.Unlock()
			return errors.New("Script Exact Scope has no Script target")
		}
		run.phase = currentNodeRunPublishing
		if err := run.transitionDisposition(currentNodeRunDispositionPublishing, nil); err != nil {
			c.mu.Unlock()
			return err
		}
		c.exactScopes[exact.ScopeID] = key
		c.mu.Unlock()

		activationErr := activation.Commit(func(scope sessionruntime.ExecutionScope) error {
			scopeRef, workflowScope := scope.Workflow()
			if scope.ID() != exact.ScopeID ||
				!workflowScope ||
				scopeRef.ProjectID != exact.ProjectID ||
				scopeRef.WorkflowID != exact.WorkflowID ||
				!scopeRef.CurrentNode.Equal(exact.CurrentNode) {
				return errors.New("Authority Exact Scope does not match its lifecycle publication")
			}
			var agentActivationFailure error
			var agentActivationResult currentNodeAgentActivationResult
			if run.agentActivation != nil {
				resource, ok := scope.Resource()
				if !ok || exact.Agent == nil || resource.SessionID() != exact.Agent.SessionID {
					agentActivationFailure = errors.New("Agent Run Exact Scope has no matching Session resource")
				} else {
					if run.retainedSessionID != nil && *run.retainedSessionID != resource.SessionID() {
						return errors.New("Agent Exact Scope belongs to a different retained Session")
					}
					agentActivationResult = currentNodeAgentActivationResult{
						resource: resource,
						scopeID:  exact.ScopeID,
					}
				}
			}
			c.mu.Lock()
			current, currentExists := c.runs.get(key)
			indexedKey, indexed := c.exactScopes[exact.ScopeID]
			_, gated := c.gates[key]
			if !currentExists ||
				current != run ||
				!indexed ||
				indexedKey != key ||
				!gated ||
				current.disposition != currentNodeRunDispositionPublishing ||
				current.executionLease == nil ||
				current.executionLease.ScopeID() != exact.ScopeID {
				c.mu.Unlock()
				return sessionruntime.ErrExecutionNoLongerLive
			}
			if _, stopping := c.stopping[exact.ScopeID]; stopping {
				c.mu.Unlock()
				return sessionruntime.ErrExecutionNoLongerLive
			}
			c.mu.Unlock()

			if err := c.publication.PublishExactRegistration(ctx, exact); err != nil {
				return err
			}

			c.mu.Lock()
			defer c.mu.Unlock()
			publishedRun, publishedExists := c.runs.get(key)
			_, publishedGate := c.gates[key]
			indexedKey, publishedExact := c.exactScopes[exact.ScopeID]
			if !publishedExists ||
				publishedRun != run ||
				!publishedGate ||
				!publishedExact ||
				indexedKey != key ||
				publishedRun.disposition != currentNodeRunDispositionPublishing ||
				publishedRun.executionLease == nil ||
				publishedRun.executionLease.ScopeID() != exact.ScopeID {
				panic(fmt.Sprintf(
					"published Exact registration lost Current Node Run ownership: current_node=%v scope=%s",
					exact.CurrentNode,
					exact.ScopeID,
				))
			}
			delete(c.gates, key)
			c.transitionAgentCapacityLocked(
				run.agentCapacityLease,
				currentNodeAgentCapacityGate,
				currentNodeAgentCapacityLive,
			)
			run.phase = currentNodeRunRunning
			if err := run.transitionDisposition(currentNodeRunDispositionRunning, nil); err != nil {
				panic(fmt.Sprintf("publish Exact registration Run transition: %v", err))
			}
			if run.agentActivation != nil {
				if agentActivationFailure != nil {
					run.agentActivation.resolve(currentNodeAgentActivationResult{}, agentActivationFailure)
				} else {
					retained := agentActivationResult.resource.SessionID()
					run.retainedSessionID = &retained
					run.agentActivation.resolve(agentActivationResult, nil)
				}
			}
			run.exactPublication.resolve(nil)
			return nil
		})
		if activationErr != nil {
			c.mu.Lock()
			interruptionOwned := false
			if indexedKey, indexed := c.exactScopes[exact.ScopeID]; indexed && indexedKey == key {
				delete(c.exactScopes, exact.ScopeID)
			}
			if current, currentExists := c.runs.get(key); currentExists && current == run {
				switch current.disposition {
				case currentNodeRunDispositionPublishing:
					if rollbackErr := current.rollbackRunningPublication(); rollbackErr != nil {
						c.mu.Unlock()
						panic(fmt.Sprintf("roll back Exact registration Run transition: %v", rollbackErr))
					}
					current.phase = currentNodeRunGated
				case currentNodeRunDispositionStopped:
					current.phase = currentNodeRunGated
					interruptionOwned = current.stop != nil &&
						current.stop.reason == currentNodeRunStopInterrupted
				default:
					c.mu.Unlock()
					panic(fmt.Sprintf(
						"failed Exact registration left Current Node Run in disposition %d",
						current.disposition,
					))
				}
			}
			c.mu.Unlock()
			if interruptionOwned {
				return errors.Join(
					errCurrentNodeRunningActivationInterrupted,
					sessionruntime.ErrExecutionNoLongerLive,
					activationErr,
				)
			}
			return activationErr
		}
		return nil
	})
}

func (c *CurrentNodeController) PublishCurrentNodeExactFinalizing(
	ctx context.Context,
	scopeID runtimeids.ExecutionScopeID,
) error {
	if c == nil {
		return errors.New("current node workflow controller is required")
	}
	if scopeID.IsZero() {
		return errors.New("workflow Exact Scope id is required")
	}
	c.mu.Lock()
	_, exact := c.exactScopes[scopeID]
	if !exact {
		for _, run := range c.runs.byCurrentNode {
			if run.executionLease != nil &&
				run.executionLease.ScopeID() == scopeID &&
				run.disposition == currentNodeRunDispositionQueued {
				c.mu.Unlock()
				return nil
			}
		}
		c.mu.Unlock()
		return sessionruntime.ErrExecutionNoLongerLive
	}
	c.mu.Unlock()
	return c.publication.PublishExactFinalizing(ctx, scopeID)
}

func (c *CurrentNodeController) removeGate(key workflow.CurrentNodeReferenceKey, scopeID runtimeids.ExecutionScopeID) {
	c.mu.Lock()
	wake := false
	if run, exists := c.runs.get(key); exists {
		_, gated := c.gates[key]
		if !gated || run.executionLease == nil || run.executionLease.ScopeID() != scopeID {
			c.mu.Unlock()
			return
		}
		delete(c.gates, key)
		delete(c.stopping, scopeID)
		c.releaseAgentCapacityLocked(run.agentCapacityLease)
		run.executionLease = nil
		wake = true
	}
	c.mu.Unlock()
	if wake {
		c.wakeAdmissionWorker()
	}
}

func (c *CurrentNodeController) enqueueAutomaticIntents(intents []CurrentNodeAutomaticIntent) {
	c.enqueueStarts(automaticQueuedStarts(intents))
}

func (c *CurrentNodeController) ensureStartsAssignments(ctx context.Context, starts []currentNodeQueuedStart) ([]currentNodeQueuedStart, error) {
	ensured := append([]currentNodeQueuedStart(nil), starts...)
	for index := range ensured {
		assignment, err := c.ensureAssignment(
			ctx,
			ensured[index].reference,
			ensured[index].taskPromptDelivery,
		)
		if err != nil {
			return ensured[:index], err
		}
		ensured[index].assignmentEnsure = assignment
	}
	return ensured, nil
}

func (c *CurrentNodeController) ensureAndWaitStarts(
	ctx context.Context,
	starts []currentNodeQueuedStart,
	recovery currentNodeStartFailureRecovery,
) ([]currentNodeQueuedStart, error) {
	ensured, ensureErr := c.ensureStartsAssignments(ctx, starts)
	outcome := waitCurrentNodeAssignmentEnsures(ctx, ensured)
	cause := errors.Join(ensureErr, outcome.err)
	if cause != nil {
		if len(outcome.pending) != 0 {
			c.continueCurrentNodeAssignmentStarts(ensured, ensureErr)
			return nil, cause
		}
		recoveryStarts := outcome.committed
		if recovery == recoverAllCurrentNodeStarts {
			recoveryStarts = starts
		}
		return nil, errors.Join(cause, c.recoverCurrentNodeStartFailures(
			ctx,
			recoveryStarts,
			false,
			currentNodeRunStopWorkerFailed,
			cause,
		))
	}
	return ensured, nil
}

type currentNodeStartFailureRecovery uint8

const (
	recoverCommittedCurrentNodeStarts currentNodeStartFailureRecovery = iota
	recoverAllCurrentNodeStarts
)

func pendingCurrentNodeAssignmentStarts(starts []currentNodeQueuedStart) ([]currentNodeQueuedStart, []*pendingCurrentNodeAssignmentEnsure) {
	pendingStarts := append([]currentNodeQueuedStart(nil), starts...)
	pending := make([]*pendingCurrentNodeAssignmentEnsure, len(pendingStarts))
	for index := range pendingStarts {
		pending[index] = newPendingCurrentNodeAssignmentEnsure()
		pendingStarts[index].assignmentEnsure = pending[index]
	}
	return pendingStarts, pending
}

func (c *CurrentNodeController) resolvePendingCurrentNodeAssignmentEnsures(
	ctx context.Context,
	starts []currentNodeQueuedStart,
	pending []*pendingCurrentNodeAssignmentEnsure,
) error {
	var result error
	for index, start := range starts {
		assignment, err := c.ensureAssignment(ctx, start.reference, start.taskPromptDelivery)
		pending[index].resolve(assignment, err)
		if err != nil {
			result = errors.Join(result, err)
		}
	}
	return result
}

func (c *CurrentNodeController) ensureAssignment(
	ctx context.Context,
	reference workflow.CurrentNodeReference,
	delivery workflowruntime.TaskPromptDelivery,
) (CurrentNodeAssignmentEnsure, error) {
	assignment, err := c.ensurer.EnsureCurrentNodeAssignment(ctx, reference, delivery)
	if err != nil {
		return nil, fmt.Errorf("ensure current node assignment %v: %w", reference, err)
	}
	if assignment == nil {
		return nil, fmt.Errorf("ensure current node assignment %v returned no completion", reference)
	}
	return assignment, nil
}

type currentNodeAssignmentWaitOutcome struct {
	committed []currentNodeQueuedStart
	ready     []currentNodeQueuedStart
	pending   []currentNodeQueuedStart
	failed    []currentNodeQueuedStart
	err       error
}

func waitCurrentNodeAssignmentEnsures(
	ctx context.Context,
	starts []currentNodeQueuedStart,
) currentNodeAssignmentWaitOutcome {
	outcome := currentNodeAssignmentWaitOutcome{
		committed: make([]currentNodeQueuedStart, 0, len(starts)),
		pending:   make([]currentNodeQueuedStart, 0, len(starts)),
	}
	for _, start := range starts {
		if start.assignmentEnsure == nil {
			outcome.err = errors.Join(outcome.err, fmt.Errorf(
				"current node assignment %v has no ensure completion",
				start.reference,
			))
			continue
		}
		receipt, err := start.assignmentEnsure.Wait(ctx)
		if receipt.Committed {
			outcome.committed = append(outcome.committed, start)
		}
		if err != nil {
			if cause := context.Cause(ctx); !receipt.Committed && cause != nil && errors.Is(err, cause) {
				outcome.pending = append(outcome.pending, start)
			} else {
				outcome.failed = append(outcome.failed, start)
			}
			outcome.err = errors.Join(outcome.err, fmt.Errorf(
				"wait for current node assignment %v: %w",
				start.reference,
				err,
			))
			continue
		}
		if !receipt.Committed {
			outcome.failed = append(outcome.failed, start)
			outcome.err = errors.Join(outcome.err, fmt.Errorf(
				"current node assignment %v was not committed",
				start.reference,
			))
			continue
		}
		outcome.ready = append(outcome.ready, start)
	}
	return outcome
}

func stagedCurrentNodeRunsForStarts(
	staged []stagedCurrentNodeRun,
	starts []currentNodeQueuedStart,
) []stagedCurrentNodeRun {
	if len(starts) == 0 {
		return nil
	}
	wanted := make(map[workflow.CurrentNodeReferenceKey]struct{}, len(starts))
	for index := range starts {
		wanted[mustCurrentNodeRunKey(&starts[index])] = struct{}{}
	}
	result := make([]stagedCurrentNodeRun, 0, len(starts))
	for _, stagedRun := range staged {
		if _, exists := wanted[stagedRun.key]; exists {
			result = append(result, stagedRun)
		}
	}
	return result
}

func (c *CurrentNodeController) completePublishedCurrentNodeAssignmentStarts(
	ctx context.Context,
	staged []stagedCurrentNodeRun,
	outcome currentNodeAssignmentWaitOutcome,
) error {
	var interruptionErr error
	if len(outcome.failed) > 0 {
		leases := c.stopCurrentNodeStartRuns(
			outcome.failed,
			currentNodeRunStopWorkerFailed,
			outcome.err,
		)
		for _, lease := range leases {
			lease.Cancel()
		}
		interruptionErr = c.interruptCurrentNodeStartFailures(ctx, outcome.failed, false, outcome.err)
		if interruptionErr == nil {
			c.discardRuns(
				currentNodeRunKeys(outcome.failed),
				currentNodeRunStopWorkerFailed,
				outcome.err,
			)
		}
	}
	c.makeStagedRunCreationsSchedulable(stagedCurrentNodeRunsForStarts(staged, outcome.ready))
	return errors.Join(outcome.err, interruptionErr)
}

func currentNodeRunKeys(starts []currentNodeQueuedStart) []workflow.CurrentNodeReferenceKey {
	keys := make([]workflow.CurrentNodeReferenceKey, 0, len(starts))
	for index := range starts {
		keys = append(keys, mustCurrentNodeRunKey(&starts[index]))
	}
	return keys
}

func (c *CurrentNodeController) continuePublishedCurrentNodeAssignmentStarts(
	staged []stagedCurrentNodeRun,
	starts []currentNodeQueuedStart,
) {
	if len(starts) == 0 {
		return
	}
	taskID, err := currentNodeStartsTaskID(starts)
	if err != nil {
		panic(fmt.Sprintf("continue published current node assignment starts: %v", err))
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
		outcome := waitCurrentNodeAssignmentEnsures(c.workerContext, starts)
		if context.Cause(c.workerContext) != nil {
			return
		}
		if err := c.lifecycle.Run(c.workerContext, taskID, func(ctx context.Context) error {
			return c.completePublishedCurrentNodeAssignmentStarts(ctx, staged, outcome)
		}); err != nil && context.Cause(c.workerContext) == nil {
			c.mu.Lock()
			c.workerErr = errors.Join(c.workerErr, err)
			c.mu.Unlock()
		}
	}()
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
		outcome := waitCurrentNodeAssignmentEnsures(c.workerContext, starts)
		if context.Cause(c.workerContext) != nil {
			return
		}
		cause := errors.Join(priorErr, outcome.err)
		if cause != nil {
			c.handleCurrentNodeStartFailures(outcome.committed, false, cause)
			return
		}
		taskID, err := currentNodeStartsTaskID(starts)
		if err != nil {
			c.mu.Lock()
			c.workerErr = errors.Join(c.workerErr, err)
			c.mu.Unlock()
			return
		}
		if err := c.lifecycle.Run(c.workerContext, taskID, func(context.Context) error {
			c.enqueueStarts(starts)
			return nil
		}); err != nil && context.Cause(c.workerContext) == nil {
			c.mu.Lock()
			c.workerErr = errors.Join(c.workerErr, err)
			c.mu.Unlock()
		}
	}()
}

func (c *CurrentNodeController) enqueueStarts(starts []currentNodeQueuedStart) {
	if len(starts) == 0 || c == nil {
		return
	}
	grouped := make(map[workflow.TaskID][]currentNodeQueuedStart)
	taskOrder := make([]workflow.TaskID, 0)
	for _, start := range starts {
		taskID := start.reference.TaskID
		if _, exists := grouped[taskID]; !exists {
			taskOrder = append(taskOrder, taskID)
		}
		grouped[taskID] = append(grouped[taskID], start)
	}
	for _, taskID := range taskOrder {
		if err := c.publishAndEnqueueStarts(context.Background(), grouped[taskID]); err != nil {
			c.mu.Lock()
			c.workerErr = errors.Join(c.workerErr, err)
			c.mu.Unlock()
		}
	}
}

func (c *CurrentNodeController) publishAndEnqueueStarts(
	ctx context.Context,
	starts []currentNodeQueuedStart,
) error {
	if len(starts) == 0 {
		return nil
	}
	delta, err := currentNodeRunCreationDelta(starts)
	if err != nil {
		return err
	}
	staged, err := c.stageCurrentNodeRunCreations(starts)
	if err != nil {
		return err
	}
	if err := c.publication.Publish(ctx, delta); err != nil {
		c.rollbackStagedRunCreations(staged, err)
		return err
	}
	c.makeStagedRunCreationsSchedulable(staged)
	return nil
}

func (c *CurrentNodeController) stageCurrentNodeRunCreations(
	starts []currentNodeQueuedStart,
) ([]stagedCurrentNodeRun, error) {
	staged := make([]stagedCurrentNodeRun, 0, len(starts))
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, errors.New("current node workflow controller is closed")
	}
	for index := range starts {
		run, created, err := c.runs.register(&starts[index])
		if err != nil {
			c.mu.Unlock()
			c.rollbackStagedRunCreations(staged, err)
			return nil, err
		}
		if !created {
			c.mu.Unlock()
			err := fmt.Errorf("Current Node %v already has a Run before publication", starts[index].reference)
			c.rollbackStagedRunCreations(staged, err)
			return nil, err
		}
		staged = append(staged, stagedCurrentNodeRun{
			key:     mustCurrentNodeRunKey(run),
			run:     run,
			created: created,
		})
	}
	c.mu.Unlock()
	return staged, nil
}

func (c *CurrentNodeController) makeStagedRunCreationsSchedulable(
	staged []stagedCurrentNodeRun,
) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, stagedRun := range staged {
		run, exists := c.runs.get(stagedRun.key)
		if !exists || run != stagedRun.run || run.disposition != currentNodeRunDispositionQueued {
			panic(fmt.Sprintf(
				"published Current Node Run changed before becoming schedulable: current_node=%v",
				stagedRun.run.reference,
			))
		}
		run.phase = currentNodeRunQueued
		if run.policy.isAutomatic() {
			c.automaticQueue.append(stagedRun.key, run)
			c.queued[stagedRun.key] = struct{}{}
		} else {
			c.explicitQueue = append(c.explicitQueue, stagedRun.key)
			c.explicitQueued[stagedRun.key] = struct{}{}
		}
	}
	c.wakeAdmissionWorker()
}

func (c *CurrentNodeController) rollbackStagedRunCreations(
	staged []stagedCurrentNodeRun,
	cause error,
) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, stagedRun := range staged {
		if !stagedRun.created {
			continue
		}
		run, exists := c.runs.get(stagedRun.key)
		if !exists || run != stagedRun.run {
			continue
		}
		run.stopOnce(currentNodeRunStopWorkerFailed, cause)
		c.runs.delete(stagedRun.key)
	}
}

func (c *CurrentNodeController) queueExplicitStartLocked(start currentNodeQueuedStart) error {
	if start.policy != currentNodeAdmissionExplicitOverride {
		return errors.New("explicit current node start cannot be automatic")
	}
	if c.closed {
		return errors.New("current node workflow controller is closed")
	}
	key, err := start.reference.Key()
	if err != nil {
		return err
	}
	run, _, err := c.runs.register(&start)
	if err != nil {
		return err
	}
	run.phase = currentNodeRunQueued
	c.explicitQueue = append(c.explicitQueue, key)
	c.explicitQueued[key] = struct{}{}
	c.wakeAdmissionWorker()
	return nil
}

func (c *CurrentNodeController) queueAutomaticStartLocked(start currentNodeQueuedStart) error {
	if !start.policy.isAutomatic() {
		return errors.New("automatic current node start requires an automatic admission policy")
	}
	if c.closed {
		return errors.New("current node workflow controller is closed")
	}
	key, err := start.reference.Key()
	if err != nil {
		return err
	}
	run, _, err := c.runs.register(&start)
	if err != nil {
		return err
	}
	run.phase = currentNodeRunQueued
	c.automaticQueue.append(key, run)
	c.queued[key] = struct{}{}
	c.wakeAdmissionWorker()
	return nil
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
		case <-c.lifecycle.releasedSignal():
		}
		for {
			key, ok := c.takeExplicitStart()
			if !ok {
				key, ok = c.takeAutomaticIntent()
				if !ok {
					break
				}
			}
			go c.runAdmission(key)
		}
	}
}

func (c *CurrentNodeController) runAdmission(key workflow.CurrentNodeReferenceKey) {
	c.mu.Lock()
	run, exists := c.runs.get(key)
	if !exists {
		c.mu.Unlock()
		panic("current node admission worker lost its Run")
	}
	reference := run.reference
	admissionDone := run.admissionDone
	admissionContext := run.admissionContext
	activation := run.agentActivation
	c.mu.Unlock()
	defer c.admissionWG.Done()
	defer close(admissionDone)
	defer c.finishTaskInterruptAdmission(reference)
	defer c.finishAdmissionWorker(key, reference.TaskID)
	if err := c.admit(admissionContext, key); err != nil {
		if activation != nil {
			activation.resolve(currentNodeAgentActivationResult{}, err)
		}
		c.mu.Lock()
		interrupted := c.interrupts.currentNodeFenced(key)
		stopped := run.disposition == currentNodeRunDispositionStopped
		stoppedByInterrupt := stopped &&
			run.stop != nil &&
			run.stop.reason == currentNodeRunStopInterrupted
		c.mu.Unlock()
		if interrupted || stoppedByInterrupt {
			return
		}
		if stopped &&
			(errors.Is(err, context.Canceled) ||
				errors.Is(err, sessionruntime.ErrExecutionNoLongerLive) ||
				errors.Is(err, ErrTaskExecutionNotQuiescent)) {
			return
		}
		var failure currentNodeAdmissionError
		if !errors.As(err, &failure) {
			failure = currentNodeAdmissionError{cause: err}
		}
		c.handleAdmissionFailure(reference, failure.admitted, failure.cause)
	}
}

func (c *CurrentNodeController) takeExplicitStart() (workflow.CurrentNodeReferenceKey, bool) {
	c.mu.Lock()
	if c.closed || c.inFlightAdmissionCountLocked(currentNodeAdmissionExplicitOverride) >= explicitAdmissionConcurrency || len(c.explicitQueue) == 0 {
		c.mu.Unlock()
		return nil, false
	}
	type candidate struct {
		key workflow.CurrentNodeReferenceKey
		run *currentNodeRun
	}
	candidates := make([]candidate, 0, len(c.explicitQueue))
	for _, key := range c.explicitQueue {
		run, exists := c.runs.get(key)
		if !exists {
			c.mu.Unlock()
			panic("explicit queue lost its Run")
		}
		candidates = append(candidates, candidate{key: key, run: run})
	}
	c.mu.Unlock()

	busyTasks := make(map[workflow.TaskID]struct{})
	for _, candidate := range candidates {
		taskID := candidate.run.reference.TaskID
		if _, busy := busyTasks[taskID]; busy {
			continue
		}
		selected := false
		acquired, err := c.lifecycle.tryRun(c.workerContext, taskID, func(context.Context) error {
			c.mu.Lock()
			defer c.mu.Unlock()
			if c.closed ||
				c.inFlightAdmissionCountLocked(currentNodeAdmissionExplicitOverride) >= explicitAdmissionConcurrency {
				return nil
			}
			run, exists := c.runs.get(candidate.key)
			if !exists || run != candidate.run || run.disposition != currentNodeRunDispositionQueued {
				return nil
			}
			index := -1
			for queuedIndex, queuedKey := range c.explicitQueue {
				if queuedKey == candidate.key {
					index = queuedIndex
					break
				}
			}
			if index < 0 {
				return nil
			}
			copy(c.explicitQueue[index:], c.explicitQueue[index+1:])
			c.explicitQueue = c.explicitQueue[:len(c.explicitQueue)-1]
			delete(c.explicitQueued, candidate.key)
			run.admissionDone = make(chan struct{})
			run.admissionContext, run.admissionCancel = context.WithCancelCause(c.workerContext)
			run.phase = currentNodeRunReserved
			c.explicitReservations[candidate.key] = struct{}{}
			c.admissionWorkers[candidate.key] = struct{}{}
			c.admissionWG.Add(1)
			selected = true
			return nil
		})
		if err != nil {
			return nil, false
		}
		if !acquired {
			busyTasks[taskID] = struct{}{}
			continue
		}
		if selected {
			return candidate.key, true
		}
	}
	return nil, false
}

func (c *CurrentNodeController) takeAutomaticIntent() (workflow.CurrentNodeReferenceKey, bool) {
	c.mu.Lock()
	if c.closed || c.automaticQueue.len() == 0 {
		c.mu.Unlock()
		return nil, false
	}
	agentAvailable := c.agentCapacityActive < c.agentConcurrency
	entry, ok := c.automaticQueue.selectEntry(c.lastAutomaticTask, agentAvailable)
	if !ok {
		c.mu.Unlock()
		return nil, false
	}
	type candidate struct {
		entry *currentNodeAutomaticQueueEntry
		run   *currentNodeRun
	}
	candidates := make([]candidate, 0, c.automaticQueue.len())
	appendCandidate := func(candidateEntry *currentNodeAutomaticQueueEntry) {
		run, exists := c.runs.get(candidateEntry.key)
		if !exists {
			panic("automatic queue lost its Run")
		}
		candidates = append(candidates, candidate{entry: candidateEntry, run: run})
	}
	appendCandidate(entry)
	for candidateEntry := c.automaticQueue.first; candidateEntry != nil; candidateEntry = candidateEntry.globalNext {
		if candidateEntry != entry {
			appendCandidate(candidateEntry)
		}
	}
	c.mu.Unlock()

	busyTasks := make(map[workflow.TaskID]struct{})
	for _, candidate := range candidates {
		taskID := candidate.run.reference.TaskID
		if _, busy := busyTasks[taskID]; busy {
			continue
		}
		selected := false
		acquired, err := c.lifecycle.tryRun(c.workerContext, taskID, func(context.Context) error {
			c.mu.Lock()
			defer c.mu.Unlock()
			if c.closed {
				return nil
			}
			key := candidate.entry.key
			run, exists := c.runs.get(key)
			if !exists || run != candidate.run || run.disposition != currentNodeRunDispositionQueued {
				return nil
			}
			if _, queued := c.queued[key]; !queued {
				return nil
			}
			if run.policy.countsAgentCapacity() && c.agentCapacityActive >= c.agentConcurrency {
				return nil
			}
			c.automaticQueue.remove(candidate.entry, run)
			delete(c.queued, key)
			run.taskPromptDelivery = workflowruntime.TaskPromptDeliveryResume
			run.admissionDone = make(chan struct{})
			run.admissionContext, run.admissionCancel = context.WithCancelCause(c.workerContext)
			run.phase = currentNodeRunReserved
			if run.policy.countsAgentCapacity() {
				run.agentCapacityLease = &currentNodeAgentCapacityLease{
					owner: currentNodeAgentCapacityReservation,
				}
				c.agentCapacityActive++
			}
			c.automaticReservations[key] = struct{}{}
			c.admissionWorkers[key] = struct{}{}
			c.admissionWG.Add(1)
			currentTaskID := run.reference.TaskID
			c.lastAutomaticTask = &currentTaskID
			selected = true
			return nil
		})
		if err != nil {
			return nil, false
		}
		if !acquired {
			busyTasks[taskID] = struct{}{}
			continue
		}
		if selected {
			return candidate.entry.key, true
		}
	}
	return nil, false
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
	for key := range c.admissionWorkers {
		run, exists := c.runs.get(key)
		if !exists {
			panic("admission worker index lost its Run")
		}
		if run.policy == policy {
			if policy.countsAgentCapacity() {
				if run.phase == currentNodeRunRunning {
					continue
				}
			}
			count++
		}
	}
	return count
}

func (c *CurrentNodeController) admissionReservationLocked(key workflow.CurrentNodeReferenceKey, policy currentNodeAdmissionPolicy) bool {
	if policy.isAutomatic() {
		_, exists := c.automaticReservations[key]
		return exists
	}
	_, exists := c.explicitReservations[key]
	return exists
}

func (c *CurrentNodeController) deleteAdmissionReservationLocked(key workflow.CurrentNodeReferenceKey, policy currentNodeAdmissionPolicy) {
	if policy.isAutomatic() {
		delete(c.automaticReservations, key)
		return
	}
	delete(c.explicitReservations, key)
}

func (c *CurrentNodeController) finishAdmissionWorker(
	key workflow.CurrentNodeReferenceKey,
	taskID workflow.TaskID,
) {
	if err := c.lifecycle.Run(context.Background(), taskID, func(context.Context) error {
		c.mu.Lock()
		defer c.mu.Unlock()
		if _, exists := c.admissionWorkers[key]; !exists {
			panic("current node admission worker ownership was replaced before completion")
		}
		delete(c.admissionWorkers, key)
		if run, exists := c.runs.get(key); exists {
			if c.closed && run.disposition != currentNodeRunDispositionStopped {
				c.deleteAdmissionReservationLocked(key, run.policy)
				c.releaseAgentCapacityLocked(run.agentCapacityLease)
				run.stopOnce(
					currentNodeRunStopControllerClosed,
					errors.New("current node workflow controller is closed"),
				)
			}
			switch run.disposition {
			case currentNodeRunDispositionStopped:
				keepForExecutionScope := false
				if run.executionLease != nil {
					scopeID := run.executionLease.ScopeID()
					indexedKey, exact := c.exactScopes[scopeID]
					if exact && indexedKey != key {
						panic("stopped current node Run has a mismatched Exact Execution Scope index")
					}
					_, gated := c.gates[key]
					keepForExecutionScope = exact || gated
				}
				if !keepForExecutionScope {
					c.runs.delete(key)
				}
			case currentNodeRunDispositionRunning:
			default:
				panic("current node admission worker completed without a running or stopped disposition")
			}
		}
		return nil
	}); err != nil {
		panic(fmt.Sprintf("finish current node admission worker: %v", err))
	}
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if !closed {
		c.wakeAdmissionWorker()
	}
}

func (c *CurrentNodeController) handleAdmissionFailure(reference workflow.CurrentNodeReference, admitted bool, cause error) {
	c.handleCurrentNodeStartFailuresWithDisposition(
		[]currentNodeQueuedStart{{reference: reference}},
		admitted,
		currentNodeRunStopAdmissionFailed,
		cause,
	)
}

func (c *CurrentNodeController) handleCurrentNodeStartFailures(
	starts []currentNodeQueuedStart,
	admitted bool,
	cause error,
) {
	c.handleCurrentNodeStartFailuresWithDisposition(
		starts,
		admitted,
		currentNodeRunStopWorkerFailed,
		cause,
	)
}

func (c *CurrentNodeController) handleCurrentNodeStartFailuresWithDisposition(
	starts []currentNodeQueuedStart,
	admitted bool,
	stopReason currentNodeRunStopReason,
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
	err := c.recoverCurrentNodeStartFailures(cleanupCtx, starts, admitted, stopReason, cause)
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
	stopReason currentNodeRunStopReason,
	cause error,
) error {
	if len(starts) == 0 {
		return nil
	}
	taskID, err := currentNodeStartsTaskID(starts)
	if err != nil {
		return err
	}
	return c.lifecycle.Run(ctx, taskID, func(ctx context.Context) error {
		c.mu.Lock()
		closed := c.closed
		c.mu.Unlock()
		if closed {
			stopReason = currentNodeRunStopControllerClosed
			cause = errors.New("current node workflow controller is closed")
		}
		leases := c.stopCurrentNodeStartRuns(starts, stopReason, cause)
		for _, lease := range leases {
			lease.Cancel()
		}
		if closed {
			return nil
		}
		return c.interruptCurrentNodeStartFailures(ctx, starts, admitted, cause)
	})
}

func (c *CurrentNodeController) stopCurrentNodeStartRuns(
	starts []currentNodeQueuedStart,
	reason currentNodeRunStopReason,
	cause error,
) []sessionruntime.WorkflowExecutionLease {
	c.mu.Lock()
	defer c.mu.Unlock()
	leases := make([]sessionruntime.WorkflowExecutionLease, 0, len(starts))
	for _, start := range starts {
		key, err := start.reference.Key()
		if err != nil {
			continue
		}
		run, exists := c.runs.get(key)
		if !exists || run.disposition == currentNodeRunDispositionStopped {
			continue
		}
		c.deleteAdmissionReservationLocked(key, run.policy)
		c.releaseAgentCapacityLocked(run.agentCapacityLease)
		if run.executionLease != nil {
			lease := *run.executionLease
			leases = append(leases, lease)
			delete(c.gates, key)
			delete(c.exactScopes, lease.ScopeID())
			delete(c.stopping, lease.ScopeID())
			run.executionLease = nil
		}
		run.stopOnce(reason, cause)
	}
	return leases
}

func currentNodeStartsTaskID(starts []currentNodeQueuedStart) (workflow.TaskID, error) {
	if len(starts) == 0 {
		return "", errors.New("current node starts are required")
	}
	taskID := starts[0].reference.TaskID
	for _, start := range starts[1:] {
		if start.reference.TaskID != taskID {
			return "", errors.New("current node start recovery cannot cross Tasks")
		}
	}
	return taskID, nil
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
	detail := workflow.CurrentNodeInterruptionDetail{
		Code:   string(reasonCurrentNodeRuntimeStartFailed),
		Fields: map[string]string{"error": cause.Error()},
	}
	var preparationErr *TaskStartPreparationError
	if errors.As(cause, &preparationErr) {
		detail = preparationErr.InterruptionDetail()
	}
	predecessor := workflowstore.CurrentNodeInterruptionFromReadyOrAdmitted
	if admitted {
		predecessor = workflowstore.CurrentNodeInterruptionFromAdmitted
	}
	publicationOutcome, publicationErr := c.publication.PublishCurrentNodeInterruption(
		ctx,
		references,
		predecessor,
		workflowstore.LifecycleFieldPresent,
		reasonCurrentNodeRuntimeStartFailed,
		detail,
		nil,
	)
	if !publicationOutcome.Committed() {
		return publicationErr
	}
	for _, reference := range references {
		c.publishPendingInterruptedCurrentNode(ctx, reference, reasonCurrentNodeRuntimeStartFailed)
	}
	return publicationErr
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
		starts = append(starts, *newCurrentNodeRun(intent.CurrentNode, intent.NodeKind, policy))
	}
	return starts
}

func (c *CurrentNodeController) currentNodeExplicitStarts(
	ctx context.Context,
	nodes []workflow.CurrentNode,
) ([]currentNodeQueuedStart, error) {
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
		nodeKind, err := c.store.CurrentNodeKind(ctx, currentNode.Reference)
		if err != nil {
			return nil, fmt.Errorf("resolve explicit current node kind at index %d: %w", index, err)
		}
		run := newCurrentNodeRun(currentNode.Reference, nodeKind, currentNodeAdmissionExplicitOverride)
		run.taskPromptDelivery = workflowruntime.TaskPromptDeliveryResume
		starts = append(starts, *run)
	}
	return starts, nil
}
