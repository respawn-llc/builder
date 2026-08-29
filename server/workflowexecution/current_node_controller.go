package workflowexecution

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"

	"core/server/session"
	"core/server/sessionruntime"
	"core/server/workflow"
	"core/server/workflowruntime"
	"core/server/workflowstore"
	"core/shared/runtimeids"
)

const (
	reasonProtocolViolationCap workflow.CurrentNodeInterruptionReason = "workflow_protocol_violation_cap"
)

type CurrentNodeAssignmentSteerer interface {
	SteerCurrentNodeAssignment(context.Context, workflow.CurrentNodeReference) (CurrentNodeAssignmentSteer, error)
}

type CurrentNodeManualMoveAssignmentPreparer interface {
	PrepareManualMoveAssignments(
		context.Context,
		[]workflowstore.CurrentNodeStartContext,
	) (
		workflowstore.ManualMoveTargetAssignmentPreparation,
		map[workflow.CurrentNodeReferenceKey]CurrentNodeAssignmentSteer,
		error,
	)
}

type CurrentNodeAssignmentSteer interface {
	Wait(context.Context) (session.CommitReceipt, error)
}

type CurrentNodeAssignmentPreparation interface {
	Prepare(context.Context) error
}

type CurrentNodeAgentRunner interface {
	StartAgentCurrentNode(
		context.Context,
		workflow.CurrentNodeReference,
		workflowruntime.TaskPromptDelivery,
		CurrentNodeAssignmentSteer,
		*WorkflowSessionContinuation,
		func(),
		workflowruntime.Controller,
	) (sessionruntime.ExecutionHandle, error)
}

type CurrentNodeScriptPublicationPreparation interface {
	PrepareScriptPublication(
		context.Context,
		workflow.CurrentNodeReference,
		workflowruntime.Controller,
	) (CurrentNodeScriptPublication, error)
}

type CurrentNodePublicationRunner interface {
	CurrentNodeAgentRunner
	CurrentNodeScriptPublicationPreparation
}

type CurrentNodeScriptPublication interface {
	Publish(context.Context, func() error, func(sessionruntime.ExecutionHandle)) (sessionruntime.ExecutionHandle, func(), error)
	Cancel()
}

type CurrentNodeControllerConfig struct {
	AgentConcurrency  int
	Attention         CurrentNodeAttentionLifecycle
	AssignmentSteerer CurrentNodeAssignmentSteerer
}

type CurrentNodeAttentionLifecycle interface {
	PublishPendingInterruptedCurrentNode(context.Context, workflow.CurrentNodeReference)
	FinalizeTaskResolution(workflowstore.TaskAttentionResolution)
}

// CurrentNodeAutomaticIntent is volatile automatic work. It has a Current Node
// natural reference rather than a replacement execution identity.
type CurrentNodeAutomaticIntent = workflowstore.CurrentNodeAutomaticIntent

// CurrentNodeController is the sole workflowruntime.Controller. Its mutex,
// per-Task mutation coordinator, and Authority operations define the ordering
// for lifecycle, admission, interruption, and completion operations.
type CurrentNodeController struct {
	store interface {
		StartTask(context.Context, workflow.TaskID) (workflowstore.StartTaskResult, error)
		ListCurrentNodes(context.Context, workflow.TaskID) ([]workflow.CurrentNode, error)
		InterruptedExecutableCurrentNodes(context.Context, workflow.TaskID) ([]workflow.CurrentNode, error)
		PreflightTaskResume(context.Context, workflow.TaskID) ([]workflowstore.CurrentNodeResumeClassification, error)
		AdmitCurrentNode(context.Context, workflow.CurrentNodeReference) (session.CommitReceipt, error)
		ResumeCurrentNode(context.Context, workflow.CurrentNodeReference) (workflowstore.InterruptedCurrentNodeAttentionProjection, bool, error)
		PendingApproval(context.Context, workflow.ApprovalID) (workflow.PendingApproval, error)
		ApplyPendingApproval(context.Context, workflow.ApprovalID) (workflowstore.PendingApprovalApplyResult, error)
		InterruptAdmittedCurrentNode(context.Context, workflow.CurrentNodeReference, workflow.CurrentNodeInterruptionReason, workflow.CurrentNodeInterruptionDetail) error
		InterruptCurrentNode(context.Context, workflow.CurrentNodeReference, workflow.CurrentNodeInterruptionReason, workflow.CurrentNodeInterruptionDetail) error
		RecoverExecutableCurrentNodes(context.Context, workflow.CurrentNodeInterruptionReason, workflow.CurrentNodeInterruptionDetail) ([]workflow.CurrentNodeReference, error)
		ResolveIdleExecutableCurrentNode(context.Context, workflowstore.IdleCurrentNodeSelector) (workflow.CurrentNode, error)
		CompleteCurrentNode(context.Context, workflowstore.CurrentNodeCompletionRequest) (workflowstore.CurrentNodeCompletionOutcome, error)
		ValidateCurrentNodeSessionBinding(context.Context, runtimeids.SessionID, workflow.CurrentNodeReference) error
		ResolveCurrentSessionStartContext(context.Context, runtimeids.SessionID) (workflowstore.CurrentNodeStartContext, error)
		TaskIDForSession(context.Context, runtimeids.SessionID) (*workflow.TaskID, error)
		TaskExecutionScope(context.Context, workflow.TaskID) (workflowstore.TaskExecutionScope, error)
	}
	runner    CurrentNodePublicationRunner
	steerer   CurrentNodeAssignmentSteerer
	authority *sessionruntime.Authority
	mutations *TaskMutationCoordinator
	attention CurrentNodeAttentionLifecycle

	agentConcurrency int
	workerContext    context.Context
	workerCancel     context.CancelFunc
	workerWake       chan struct{}
	workerWG         sync.WaitGroup
	admissionWG      sync.WaitGroup
	preparationWG    sync.WaitGroup

	mu                    sync.Mutex
	closed                bool
	explicitQueue         []currentNodeQueuedStart
	explicitQueued        map[workflow.CurrentNodeReferenceKey]struct{}
	explicitReservations  map[workflow.CurrentNodeReferenceKey]currentNodeQueuedStart
	preparationQueue      []*taskPreparationBatch
	preparationRunning    []*taskPreparationBatch
	automaticQueue        currentNodeAutomaticQueue
	queued                map[workflow.CurrentNodeReferenceKey]struct{}
	automaticReservations map[workflow.CurrentNodeReferenceKey]currentNodeQueuedStart
	admissionWorkers      map[workflow.CurrentNodeReferenceKey]currentNodeQueuedStart
	agentCapacityActive   int
	interrupts            currentNodeInterruptState
	workerErr             error
	lastAutomaticTask     *workflow.TaskID
	taskExecutionReads    atomic.Pointer[workflowTaskControllerReadSnapshot]
	lifecycleBarrier      sync.RWMutex
	closeMu               sync.Mutex
	closing               bool
}

func NewCurrentNodeController(
	store interface {
		StartTask(context.Context, workflow.TaskID) (workflowstore.StartTaskResult, error)
		ListCurrentNodes(context.Context, workflow.TaskID) ([]workflow.CurrentNode, error)
		InterruptedExecutableCurrentNodes(context.Context, workflow.TaskID) ([]workflow.CurrentNode, error)
		PreflightTaskResume(context.Context, workflow.TaskID) ([]workflowstore.CurrentNodeResumeClassification, error)
		AdmitCurrentNode(context.Context, workflow.CurrentNodeReference) (session.CommitReceipt, error)
		ResumeCurrentNode(context.Context, workflow.CurrentNodeReference) (workflowstore.InterruptedCurrentNodeAttentionProjection, bool, error)
		PendingApproval(context.Context, workflow.ApprovalID) (workflow.PendingApproval, error)
		ApplyPendingApproval(context.Context, workflow.ApprovalID) (workflowstore.PendingApprovalApplyResult, error)
		InterruptAdmittedCurrentNode(context.Context, workflow.CurrentNodeReference, workflow.CurrentNodeInterruptionReason, workflow.CurrentNodeInterruptionDetail) error
		InterruptCurrentNode(context.Context, workflow.CurrentNodeReference, workflow.CurrentNodeInterruptionReason, workflow.CurrentNodeInterruptionDetail) error
		RecoverExecutableCurrentNodes(context.Context, workflow.CurrentNodeInterruptionReason, workflow.CurrentNodeInterruptionDetail) ([]workflow.CurrentNodeReference, error)
		ResolveIdleExecutableCurrentNode(context.Context, workflowstore.IdleCurrentNodeSelector) (workflow.CurrentNode, error)
		CompleteCurrentNode(context.Context, workflowstore.CurrentNodeCompletionRequest) (workflowstore.CurrentNodeCompletionOutcome, error)
		ValidateCurrentNodeSessionBinding(context.Context, runtimeids.SessionID, workflow.CurrentNodeReference) error
		ResolveCurrentSessionStartContext(context.Context, runtimeids.SessionID) (workflowstore.CurrentNodeStartContext, error)
		TaskIDForSession(context.Context, runtimeids.SessionID) (*workflow.TaskID, error)
		TaskExecutionScope(context.Context, workflow.TaskID) (workflowstore.TaskExecutionScope, error)
	},
	runner CurrentNodePublicationRunner,
	authority *sessionruntime.Authority,
	mutations *TaskMutationCoordinator,
	cfg CurrentNodeControllerConfig,
) (*CurrentNodeController, error) {
	if store == nil {
		return nil, errors.New("current node workflow store is required")
	}
	if runner == nil {
		return nil, errors.New("current node workflow runner is required")
	}
	if cfg.AssignmentSteerer == nil {
		return nil, errors.New("current node assignment steerer is required")
	}
	if authority == nil {
		return nil, errors.New("session runtime authority is required")
	}
	if mutations == nil {
		return nil, errors.New("task mutation coordinator is required")
	}
	if cfg.AgentConcurrency <= 0 {
		return nil, errors.New("workflow agent concurrency must be positive")
	}
	workerContext, workerCancel := context.WithCancel(context.Background())
	controller := &CurrentNodeController{
		store:                 store,
		runner:                runner,
		steerer:               cfg.AssignmentSteerer,
		authority:             authority,
		mutations:             mutations,
		attention:             cfg.Attention,
		agentConcurrency:      cfg.AgentConcurrency,
		workerContext:         workerContext,
		workerCancel:          workerCancel,
		workerWake:            make(chan struct{}, 1),
		explicitQueued:        make(map[workflow.CurrentNodeReferenceKey]struct{}),
		explicitReservations:  make(map[workflow.CurrentNodeReferenceKey]currentNodeQueuedStart),
		queued:                make(map[workflow.CurrentNodeReferenceKey]struct{}),
		automaticReservations: make(map[workflow.CurrentNodeReferenceKey]currentNodeQueuedStart),
		admissionWorkers:      make(map[workflow.CurrentNodeReferenceKey]currentNodeQueuedStart),
		interrupts:            newCurrentNodeInterruptState(),
	}
	controller.taskExecutionReads.Store(&workflowTaskControllerReadSnapshot{
		concurrencyQueued: map[workflow.TaskID][]workflow.CurrentNodeReference{},
		quiescence:        map[workflow.TaskID]bool{},
	})
	controller.workerWG.Add(1)
	go controller.runAdmissions()
	return controller, nil
}

func (c *CurrentNodeController) CompleteAgentCurrentNode(
	ctx context.Context,
	req workflowruntime.AgentCompletionRequest,
) (workflowruntime.CompletionResult, error) {
	return c.completeAgentCurrentNode(ctx, req)
}

func (c *CurrentNodeController) CompleteScriptCurrentNode(
	ctx context.Context,
	req workflowruntime.ScriptCompletionRequest,
) (workflowruntime.CompletionResult, error) {
	if req.ScopeID.IsZero() {
		return workflowruntime.CompletionResult{}, errors.New("Script completion requires an Exact Execution Scope")
	}
	return c.completeLiveCurrentNode(
		ctx,
		req.ScopeID,
		req.TransitionID,
		req.OutputValues,
		req.Commentary,
		func(commit func() (workflowruntime.CompletionResult, error)) (workflowruntime.CompletionResult, error) {
			return c.authority.CompleteFinalizingScript(req.ScopeID, commit)
		},
	)
}

func (c *CurrentNodeController) CompleteSessionCurrentNode(
	ctx context.Context,
	sessionID runtimeids.SessionID,
	runID runtimeids.RunID,
	stepID runtimeids.StepID,
	transitionID string,
	outputValues map[string]string,
	commentary string,
) (workflowruntime.CompletionResult, error) {
	if c == nil {
		return workflowruntime.CompletionResult{}, errors.New("current node workflow controller is required")
	}
	if sessionID.IsZero() {
		return workflowruntime.CompletionResult{}, errors.New("session id is required")
	}
	handle, live := c.authority.SessionExecution(sessionID)
	if !live || handle.Scope().Kind() != sessionruntime.ExecutionScopeAgent {
		return workflowruntime.CompletionResult{}, sessionruntime.ErrExecutionNoLongerLive
	}
	return c.CompleteAgentCurrentNode(ctx, workflowruntime.AgentCompletionRequest{
		Provenance: workflowruntime.AgentCompletionProvenance{
			ScopeID: handle.Scope().ID(),
			RunID:   runID,
			StepID:  stepID,
		},
		SessionID:    sessionID,
		TransitionID: transitionID,
		OutputValues: outputValues,
		Commentary:   commentary,
	})
}

func (c *CurrentNodeController) completeAgentCurrentNode(
	ctx context.Context,
	req workflowruntime.AgentCompletionRequest,
) (workflowruntime.CompletionResult, error) {
	if c == nil {
		return workflowruntime.CompletionResult{}, errors.New("current node workflow controller is required")
	}
	if req.Provenance.ScopeID.IsZero() || req.Provenance.RunID.IsZero() ||
		req.Provenance.StepID.IsZero() || req.SessionID.IsZero() {
		return workflowruntime.CompletionResult{}, errors.New("Agent completion requires Scope, Session, Run, and Step provenance")
	}
	handle, live := c.authority.SessionExecution(req.SessionID)
	if !live || handle.Scope().Kind() != sessionruntime.ExecutionScopeAgent ||
		handle.Scope().ID() != req.Provenance.ScopeID {
		return workflowruntime.CompletionResult{}, sessionruntime.ErrExecutionNoLongerLive
	}
	return c.completeLiveCurrentNode(
		ctx,
		req.Provenance.ScopeID,
		req.TransitionID,
		req.OutputValues,
		req.Commentary,
		func(commit func() (workflowruntime.CompletionResult, error)) (workflowruntime.CompletionResult, error) {
			return c.authority.CompleteAgentStep(
				ctx,
				req.Provenance.ScopeID,
				req.Provenance.RunID,
				req.Provenance.StepID,
				commit,
			)
		},
	)
}

func (c *CurrentNodeController) completeLiveCurrentNode(
	ctx context.Context,
	scopeID runtimeids.ExecutionScopeID,
	transitionID string,
	outputValues map[string]string,
	commentary string,
	validateAndCommit func(
		func() (workflowruntime.CompletionResult, error),
	) (workflowruntime.CompletionResult, error),
) (workflowruntime.CompletionResult, error) {
	handle, live := c.authority.ExecutionByScope(scopeID)
	if !live {
		return workflowruntime.CompletionResult{}, sessionruntime.ErrExecutionNoLongerLive
	}
	workflowRef, workflowScoped := handle.Scope().Workflow()
	if !workflowScoped {
		return workflowruntime.CompletionResult{}, sessionruntime.ErrExecutionNoLongerLive
	}
	var result workflowruntime.CompletionResult
	err := c.runTaskMutation(ctx, workflowRef.CurrentNode.TaskID, func(ctx context.Context) error {
		c.mu.Lock()
		if err := c.ensureTaskAvailableLocked(workflowRef.CurrentNode.TaskID); err != nil {
			c.mu.Unlock()
			return err
		}
		c.mu.Unlock()
		committed, err := validateAndCommit(func() (workflowruntime.CompletionResult, error) {
			c.mu.Lock()
			if err := c.ensureTaskAvailableLocked(workflowRef.CurrentNode.TaskID); err != nil {
				c.mu.Unlock()
				return workflowruntime.CompletionResult{}, err
			}
			c.mu.Unlock()
			outcome, completionErr := c.store.CompleteCurrentNode(ctx, workflowstore.CurrentNodeCompletionRequest{
				Source:       workflowRef.CurrentNode,
				TransitionID: transitionID,
				OutputValues: outputValues,
				Commentary:   commentary,
			})
			if completionErr != nil {
				return workflowruntime.CompletionResult{}, completionErr
			}
			if !outcome.CommitReceipt.Committed {
				return workflowruntime.CompletionResult{}, errors.New("current node completion returned without a committed mutation")
			}
			return workflowruntime.CompletionResult{
				TransitionID:    workflow.TransitionID(transitionID),
				State:           workflowruntime.CompletionStateApplied,
				CommittedResult: outcome.CurrentNodeCompletionResult,
				Diagnostic:      outcome.PostCommitDiagnostic,
			}, nil
		})
		if err != nil {
			return err
		}
		if !committed.IsApplied() {
			return errors.New("current node completion returned without an applied result")
		}
		result = committed
		return nil
	})
	return result, err
}

func (c *CurrentNodeController) ContinueCurrentNode(
	ctx context.Context,
	completed workflowstore.CurrentNodeCompletionResult,
) error {
	if c == nil {
		return errors.New("current node workflow controller is required")
	}
	starts := automaticQueuedStarts(completed.AutomaticIntents)
	if len(starts) == 0 {
		c.wakeAdmissionWorker()
		return nil
	}
	starts, err := c.steerAndWaitStarts(ctx, starts, recoverAllCurrentNodeStarts)
	if err != nil {
		return err
	}
	c.enqueueStarts(starts)
	return nil
}

func (c *CurrentNodeController) releaseAgentCapacity(lease *currentNodeAgentCapacityLease) {
	if c == nil || lease == nil {
		return
	}
	c.mu.Lock()
	c.releaseAgentCapacityLocked(lease)
	closed := c.closed || c.closing
	c.mu.Unlock()
	if !closed {
		c.wakeAdmissionWorker()
	}
}

func (c *CurrentNodeController) RecordProtocolViolation(ctx context.Context, req workflowruntime.ViolationRequest) (workflowruntime.ViolationResult, error) {
	count, interrupted, err := c.authority.RecordWorkflowProtocolViolation(req.ScopeID, req.MaxCount)
	if err != nil {
		return workflowruntime.ViolationResult{}, err
	}
	result := workflowruntime.ViolationResult{Count: count, Interrupted: interrupted}
	if result.Interrupted {
		if err := c.FailCurrentNodeScope(ctx, req.ScopeID, reasonProtocolViolationCap, workflowProtocolViolationCause(req)); err != nil {
			return workflowruntime.ViolationResult{}, err
		}
	}
	return result, nil
}

func (c *CurrentNodeController) resolveQuiescentIdleCurrentNode(
	ctx context.Context,
	selector workflowstore.IdleCurrentNodeSelector,
) (workflow.CurrentNode, error) {
	source, err := c.store.ResolveIdleExecutableCurrentNode(ctx, selector)
	if err != nil {
		return workflow.CurrentNode{}, err
	}
	if err := c.EnsureTaskQuiescent(source.Reference.TaskID); err != nil {
		return workflow.CurrentNode{}, err
	}
	return source, nil
}

func workflowProtocolViolationCause(req workflowruntime.ViolationRequest) error {
	if req.Detail != "" {
		return errors.New(req.Detail)
	}
	return errors.New("workflow protocol violation budget exhausted")
}

func (c *CurrentNodeController) ResetProtocolViolationBudget(ctx context.Context, req workflowruntime.ViolationResetRequest) error {
	return c.authority.ResetWorkflowProtocolViolationBudget(req.ScopeID)
}

func (c *CurrentNodeController) FailCurrentNodeScope(
	ctx context.Context,
	scopeID runtimeids.ExecutionScopeID,
	reason workflow.CurrentNodeInterruptionReason,
	cause error,
) error {
	detail := workflow.NewCurrentNodeInterruptionDetail(string(reason), cause)
	handle, live := c.authority.ExecutionByScope(scopeID)
	if !live {
		return sessionruntime.ErrExecutionNoLongerLive
	}
	workflowRef, workflowScoped := handle.Scope().Workflow()
	if !workflowScoped {
		return sessionruntime.ErrExecutionNoLongerLive
	}
	reference := workflowRef.CurrentNode
	if err := c.runTaskMutation(ctx, reference.TaskID, func(ctx context.Context) error {
		return c.authority.WithExactExecutions([]sessionruntime.ExecutionHandle{handle}, func() error {
			c.mu.Lock()
			if c.closed {
				c.mu.Unlock()
				return errors.New("current node workflow controller is closed")
			}
			if c.interrupts.taskActive(reference.TaskID) {
				c.mu.Unlock()
				return ErrTaskExecutionNotQuiescent
			}
			c.mu.Unlock()
			return c.store.InterruptAdmittedCurrentNode(ctx, reference, reason, detail)
		})
	}); err != nil {
		return err
	}
	c.publishPendingInterruptedCurrentNode(ctx, reference, reason)
	handle.RequestStop()
	return nil
}

func (c *CurrentNodeController) Close() error {
	if c == nil {
		return nil
	}
	c.closeMu.Lock()
	defer c.closeMu.Unlock()
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closing = true
	runningPreparations := append([]*taskPreparationBatch(nil), c.preparationRunning...)
	for _, batch := range runningPreparations {
		batch.cancel(preparationShutdownCause())
	}
	c.mu.Unlock()
	c.workerCancel()
	c.lifecycleBarrier.Lock()
	var (
		queuedPreparations []*taskPreparationBatch
		queuedStarts       []currentNodeQueuedStart
	)
	c.mu.Lock()
	c.closed = true
	c.closing = false
	queuedPreparations = append([]*taskPreparationBatch(nil), c.preparationQueue...)
	c.preparationQueue = nil
	queuedStarts = append([]currentNodeQueuedStart(nil), c.explicitQueue...)
	c.explicitQueue = nil
	c.explicitQueued = make(map[workflow.CurrentNodeReferenceKey]struct{})
	c.automaticQueue.clear()
	c.queued = make(map[workflow.CurrentNodeReferenceKey]struct{})
	for _, batch := range queuedPreparations {
		closeQueuedTaskPreparationBatch(batch, preparationShutdownCause())
	}
	for _, start := range queuedStarts {
		start.completion.resolve(nil, errors.New("current node workflow controller shut down before admission"))
	}
	c.mu.Unlock()
	c.lifecycleBarrier.Unlock()

	stopErr := c.authority.StopWorkflowExecutions(context.Background())
	c.workerWG.Wait()
	c.preparationWG.Wait()
	c.admissionWG.Wait()
	c.mu.Lock()
	workerErr := c.workerErr
	c.mu.Unlock()
	return errors.Join(stopErr, workerErr)
}

func (c *CurrentNodeController) runTaskMutation(
	ctx context.Context,
	taskID workflow.TaskID,
	operation func(context.Context) error,
) error {
	if c == nil {
		return errors.New("current node workflow controller is required")
	}
	active, _ := ctx.Value(currentNodeLifecycleContextKey{}).(*CurrentNodeController)
	if active == c {
		return c.mutations.Run(ctx, taskID, operation)
	}
	c.lifecycleBarrier.RLock()
	defer c.lifecycleBarrier.RUnlock()
	c.mu.Lock()
	closed := c.closed || c.closing
	c.mu.Unlock()
	if closed {
		return errors.New("current node workflow controller is closed")
	}
	return c.mutations.Run(ctx, taskID, func(ctx context.Context) error {
		return operation(context.WithValue(ctx, currentNodeLifecycleContextKey{}, c))
	})
}

func (c *CurrentNodeController) runTaskMutations(
	ctx context.Context,
	taskIDs []workflow.TaskID,
	operation func(context.Context) error,
) error {
	if c == nil {
		return errors.New("current node workflow controller is required")
	}
	unique := make([]workflow.TaskID, 0, len(taskIDs))
	seen := make(map[workflow.TaskID]struct{}, len(taskIDs))
	for _, taskID := range taskIDs {
		if _, exists := seen[taskID]; exists {
			continue
		}
		seen[taskID] = struct{}{}
		unique = append(unique, taskID)
	}
	active, _ := ctx.Value(currentNodeLifecycleContextKey{}).(*CurrentNodeController)
	if active == c {
		return c.mutations.RunMany(ctx, unique, operation)
	}
	c.lifecycleBarrier.RLock()
	defer c.lifecycleBarrier.RUnlock()
	c.mu.Lock()
	closed := c.closed || c.closing
	c.mu.Unlock()
	if closed {
		return errors.New("current node workflow controller is closed")
	}
	return c.mutations.RunMany(ctx, unique, func(ctx context.Context) error {
		return operation(context.WithValue(ctx, currentNodeLifecycleContextKey{}, c))
	})
}

type currentNodeLifecycleContextKey struct{}

func runCurrentNodeTaskMutation[T any](
	ctx context.Context,
	controller *CurrentNodeController,
	taskID workflow.TaskID,
	operation func(context.Context) (T, error),
) (T, error) {
	var result T
	err := controller.runTaskMutation(ctx, taskID, func(ctx context.Context) error {
		var err error
		result, err = operation(ctx)
		return err
	})
	return result, err
}

func (c *CurrentNodeController) requireLiveScope(scopeID runtimeids.ExecutionScopeID) error {
	if c == nil || scopeID.IsZero() {
		return errors.New("workflow exact execution scope id is required")
	}
	handle, exists := c.authority.ExecutionByScope(scopeID)
	if !exists {
		return sessionruntime.ErrExecutionNoLongerLive
	}
	if _, workflowScoped := handle.Scope().Workflow(); !workflowScoped {
		return sessionruntime.ErrExecutionNoLongerLive
	}
	return nil
}

var _ workflowruntime.Controller = (*CurrentNodeController)(nil)
