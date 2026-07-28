package workflowexecution

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"core/server/sessionruntime"
	askquestion "core/server/tools"
	"core/server/workflow"
	"core/server/workflowruntime"
	"core/server/workflowstore"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

const reasonProtocolViolationCap workflow.CurrentNodeInterruptionReason = "workflow_protocol_violation_cap"

// CurrentNodeRunner starts a lease that has already been admitted under the
// controller mutation permit. The runner owns slow launch preparation; the
// controller owns its gate and live-scope registration.
type CurrentNodeRunner interface {
	StartCurrentNode(context.Context, workflow.CurrentNodeReference, sessionruntime.WorkflowExecutionLease, workflowruntime.Controller) error
}

type CurrentNodeControllerConfig struct {
	AutomaticConcurrency int
	Attention            CurrentNodeAttentionLifecycle
}

type CurrentNodeAttentionLifecycle interface {
	PublishPendingInterruptedCurrentNode(context.Context, workflow.CurrentNodeReference)
	FinalizeTaskResolution(workflowstore.TaskAttentionResolution)
}

// CurrentNodeAutomaticIntent is volatile automatic work. It has a Current Node
// natural reference rather than a replacement execution identity.
type CurrentNodeAutomaticIntent struct {
	CurrentNode workflow.CurrentNodeReference
}

type CurrentNodeExplicitStart struct {
	CurrentNode workflow.CurrentNodeReference
}

type CurrentNodeAdmissionGateSnapshot struct {
	CurrentNode workflow.CurrentNodeReference
	ScopeID     runtimeids.ExecutionScopeID
	Automatic   bool
}

type CurrentNodeLiveScopeSnapshot struct {
	CurrentNode workflow.CurrentNodeReference
	ScopeID     runtimeids.ExecutionScopeID
	Automatic   bool
}

type CurrentNodeHeldIntentSnapshot struct {
	CurrentNode workflow.CurrentNodeReference
	SourceScope runtimeids.ExecutionScopeID
	Automatic   bool
}

// CurrentNodeExecutionSnapshot is immutable live controller state. Durable
// Current Node scheduling rows are intentionally not inferred from this view.
type CurrentNodeExecutionSnapshot struct {
	AutomaticIntents  []CurrentNodeAutomaticIntent
	ExplicitStarts    []CurrentNodeExplicitStart
	HeldIntents       []CurrentNodeHeldIntentSnapshot
	Gates             []CurrentNodeAdmissionGateSnapshot
	LiveScopes        []CurrentNodeLiveScopeSnapshot
	InterruptingTasks []workflow.TaskID
}

// CurrentNodeController is the sole workflowruntime.Controller. Its mutex,
// mutation permit, and Authority operations define the ordering for all
// lifecycle, admission, interruption, and completion operations.
type CurrentNodeController struct {
	store interface {
		StartTaskWithExecutionTarget(context.Context, workflow.TaskID, *workflowstore.ExecutionTargetCandidate) (workflowstore.StartTaskResult, error)
		InterruptedExecutableCurrentNodes(context.Context, workflow.TaskID) ([]workflow.CurrentNode, error)
		AdmitCurrentNode(context.Context, workflow.CurrentNodeReference) error
		ResumeCurrentNode(context.Context, workflow.CurrentNodeReference) (workflowstore.InterruptedCurrentNodeAttentionProjection, bool, error)
		PendingApproval(context.Context, workflow.ApprovalID) (workflow.PendingApproval, error)
		ApplyPendingApproval(context.Context, workflow.ApprovalID) (workflowstore.PendingApprovalApplyResult, error)
		ApplyManualMove(context.Context, workflowstore.ManualMovePreparation, *workflowstore.ExecutionTargetCandidate) (workflowstore.ManualMoveResult, error)
		InterruptAdmittedCurrentNode(context.Context, workflow.CurrentNodeReference, workflow.CurrentNodeInterruptionReason, workflow.CurrentNodeInterruptionDetail) error
		InterruptCurrentNode(context.Context, workflow.CurrentNodeReference, workflow.CurrentNodeInterruptionReason, workflow.CurrentNodeInterruptionDetail) error
		RecoverExecutableCurrentNodes(context.Context, workflow.CurrentNodeInterruptionReason, workflow.CurrentNodeInterruptionDetail) ([]workflow.CurrentNodeReference, error)
		ResolveIdleExecutableCurrentNode(context.Context, workflowstore.IdleCurrentNodeSelector) (workflow.CurrentNode, error)
		CompleteCurrentNode(context.Context, workflowstore.CurrentNodeCompletionRequest) (workflowstore.CurrentNodeCompletionResult, error)
		ValidateCurrentNodeSessionBinding(context.Context, runtimeids.SessionID, workflow.CurrentNodeReference) error
		TaskExecutionScope(context.Context, workflow.TaskID) (workflowstore.TaskExecutionScope, error)
	}
	runner    CurrentNodeRunner
	authority *sessionruntime.Authority
	permit    *MutationPermit
	attention CurrentNodeAttentionLifecycle

	automaticConcurrency int
	workerContext        context.Context
	workerCancel         context.CancelFunc
	workerWake           chan struct{}
	workerWG             sync.WaitGroup
	admissionWG          sync.WaitGroup

	mu                    sync.Mutex
	closed                bool
	gates                 map[workflow.CurrentNodeReferenceKey]currentNodeAdmissionGate
	live                  map[runtimeids.ExecutionScopeID]currentNodeLiveScope
	liveByNode            map[workflow.CurrentNodeReferenceKey]runtimeids.ExecutionScopeID
	stopping              map[runtimeids.ExecutionScopeID]struct{}
	completed             map[runtimeids.ExecutionScopeID]struct{}
	violations            map[runtimeids.ExecutionScopeID]int64
	heldStarts            map[runtimeids.ExecutionScopeID][]currentNodeQueuedStart
	explicitQueue         []workflow.CurrentNodeReference
	explicitQueued        map[workflow.CurrentNodeReferenceKey]struct{}
	explicitReservations  map[workflow.CurrentNodeReferenceKey]currentNodeQueuedStart
	automaticQueue        []CurrentNodeAutomaticIntent
	queued                map[workflow.CurrentNodeReferenceKey]struct{}
	automaticReservations map[workflow.CurrentNodeReferenceKey]currentNodeQueuedStart
	admissionWorkers      map[workflow.CurrentNodeReferenceKey]currentNodeQueuedStart
	interrupts            currentNodeInterruptState
	workerErr             error
	lastAutomaticTask     *workflow.TaskID
}

func NewCurrentNodeController(
	store interface {
		StartTaskWithExecutionTarget(context.Context, workflow.TaskID, *workflowstore.ExecutionTargetCandidate) (workflowstore.StartTaskResult, error)
		InterruptedExecutableCurrentNodes(context.Context, workflow.TaskID) ([]workflow.CurrentNode, error)
		AdmitCurrentNode(context.Context, workflow.CurrentNodeReference) error
		ResumeCurrentNode(context.Context, workflow.CurrentNodeReference) (workflowstore.InterruptedCurrentNodeAttentionProjection, bool, error)
		PendingApproval(context.Context, workflow.ApprovalID) (workflow.PendingApproval, error)
		ApplyPendingApproval(context.Context, workflow.ApprovalID) (workflowstore.PendingApprovalApplyResult, error)
		ApplyManualMove(context.Context, workflowstore.ManualMovePreparation, *workflowstore.ExecutionTargetCandidate) (workflowstore.ManualMoveResult, error)
		InterruptAdmittedCurrentNode(context.Context, workflow.CurrentNodeReference, workflow.CurrentNodeInterruptionReason, workflow.CurrentNodeInterruptionDetail) error
		InterruptCurrentNode(context.Context, workflow.CurrentNodeReference, workflow.CurrentNodeInterruptionReason, workflow.CurrentNodeInterruptionDetail) error
		RecoverExecutableCurrentNodes(context.Context, workflow.CurrentNodeInterruptionReason, workflow.CurrentNodeInterruptionDetail) ([]workflow.CurrentNodeReference, error)
		ResolveIdleExecutableCurrentNode(context.Context, workflowstore.IdleCurrentNodeSelector) (workflow.CurrentNode, error)
		CompleteCurrentNode(context.Context, workflowstore.CurrentNodeCompletionRequest) (workflowstore.CurrentNodeCompletionResult, error)
		ValidateCurrentNodeSessionBinding(context.Context, runtimeids.SessionID, workflow.CurrentNodeReference) error
		TaskExecutionScope(context.Context, workflow.TaskID) (workflowstore.TaskExecutionScope, error)
	},
	runner CurrentNodeRunner,
	authority *sessionruntime.Authority,
	permit *MutationPermit,
	cfg CurrentNodeControllerConfig,
) (*CurrentNodeController, error) {
	if store == nil {
		return nil, errors.New("current node workflow store is required")
	}
	if runner == nil {
		return nil, errors.New("current node workflow runner is required")
	}
	if authority == nil {
		return nil, errors.New("session runtime authority is required")
	}
	if permit == nil {
		return nil, errors.New("workflow mutation permit is required")
	}
	if cfg.AutomaticConcurrency <= 0 {
		return nil, errors.New("automatic workflow concurrency must be positive")
	}
	workerContext, workerCancel := context.WithCancel(context.Background())
	controller := &CurrentNodeController{
		store:                 store,
		runner:                runner,
		authority:             authority,
		permit:                permit,
		attention:             cfg.Attention,
		automaticConcurrency:  cfg.AutomaticConcurrency,
		workerContext:         workerContext,
		workerCancel:          workerCancel,
		workerWake:            make(chan struct{}, 1),
		gates:                 make(map[workflow.CurrentNodeReferenceKey]currentNodeAdmissionGate),
		live:                  make(map[runtimeids.ExecutionScopeID]currentNodeLiveScope),
		liveByNode:            make(map[workflow.CurrentNodeReferenceKey]runtimeids.ExecutionScopeID),
		stopping:              make(map[runtimeids.ExecutionScopeID]struct{}),
		completed:             make(map[runtimeids.ExecutionScopeID]struct{}),
		violations:            make(map[runtimeids.ExecutionScopeID]int64),
		heldStarts:            make(map[runtimeids.ExecutionScopeID][]currentNodeQueuedStart),
		explicitQueued:        make(map[workflow.CurrentNodeReferenceKey]struct{}),
		explicitReservations:  make(map[workflow.CurrentNodeReferenceKey]currentNodeQueuedStart),
		queued:                make(map[workflow.CurrentNodeReferenceKey]struct{}),
		automaticReservations: make(map[workflow.CurrentNodeReferenceKey]currentNodeQueuedStart),
		admissionWorkers:      make(map[workflow.CurrentNodeReferenceKey]currentNodeQueuedStart),
		interrupts:            newCurrentNodeInterruptState(),
	}
	controller.workerWG.Add(1)
	go controller.runAdmissions()
	return controller, nil
}

func (c *CurrentNodeController) CompleteCurrentNode(ctx context.Context, req workflowruntime.CompletionRequest) (workflowruntime.CompletionResult, error) {
	_, err := c.completeLiveCurrentNode(ctx, req)
	if err != nil {
		return workflowruntime.CompletionResult{}, err
	}
	return workflowruntime.CompletionResult{TransitionID: workflow.TransitionID(req.TransitionID), State: "applied"}, nil
}

// CompleteSessionCurrentNode completes the one exact live agent scope for a
// Session. It is the agent-facing completion entrypoint; a Session ID is
// sufficient because Exact Scope identity is resolved only from Authority live
// state, never from durable workflow rows.
func (c *CurrentNodeController) CompleteSessionCurrentNode(
	ctx context.Context,
	sessionID runtimeids.SessionID,
	transitionID string,
	outputValues map[string]string,
	commentary string,
) (workflowstore.CurrentNodeCompletionResult, error) {
	if c == nil {
		return workflowstore.CurrentNodeCompletionResult{}, errors.New("current node workflow controller is required")
	}
	if sessionID.IsZero() {
		return workflowstore.CurrentNodeCompletionResult{}, errors.New("session id is required")
	}
	handle, live := c.authority.SessionExecution(sessionID)
	if !live {
		return workflowstore.CurrentNodeCompletionResult{}, sessionruntime.ErrExecutionNoLongerLive
	}
	scopeRef, workflowScoped := handle.Scope().Workflow()
	if !workflowScoped {
		return workflowstore.CurrentNodeCompletionResult{}, sessionruntime.ErrExecutionNoLongerLive
	}
	c.mu.Lock()
	owned, ownedLive := c.live[handle.Scope().ID()]
	c.mu.Unlock()
	if !ownedLive || !owned.reference.Equal(scopeRef.CurrentNode) {
		return workflowstore.CurrentNodeCompletionResult{}, sessionruntime.ErrExecutionNoLongerLive
	}
	return c.completeLiveCurrentNode(ctx, workflowruntime.CompletionRequest{
		ScopeID:      handle.Scope().ID(),
		TransitionID: transitionID,
		OutputValues: outputValues,
		Commentary:   commentary,
	})
}

// AnswerWorkflowQuestion delivers an answer only after resolving one exact
// live Authority prompt and proving its Session still owns the same Current
// Node in durable Task state. Prompt state itself remains volatile.
func (c *CurrentNodeController) AnswerWorkflowQuestion(
	ctx context.Context,
	taskID workflow.TaskID,
	askID string,
	response askquestion.AskQuestionResponse,
	submitErr error,
) error {
	if c == nil {
		return errors.New("current node workflow controller is required")
	}
	if strings.TrimSpace(string(taskID)) == "" {
		return errors.New("workflow task id is required")
	}
	askID = strings.TrimSpace(askID)
	if askID == "" {
		return errors.New("workflow ask id is required")
	}
	if strings.TrimSpace(response.RequestID) != askID {
		return errors.New("workflow question response does not match ask id")
	}
	return c.permit.Run(ctx, func(ctx context.Context) error {
		resolution, err := c.authority.ResolvePendingWorkflowPrompt(taskID, askID)
		if err != nil {
			return err
		}
		c.mu.Lock()
		live, isLive := c.live[resolution.ScopeID]
		c.mu.Unlock()
		if !isLive || !live.reference.Equal(resolution.CurrentNode) {
			return serverapi.ErrPromptNotFound
		}
		if err := c.store.ValidateCurrentNodeSessionBinding(ctx, resolution.SessionID, resolution.CurrentNode); err != nil {
			if errors.Is(err, workflowstore.ErrSessionNotCurrentWorkflowNode) {
				return serverapi.ErrPromptNotFound
			}
			return err
		}
		return c.authority.SubmitPromptResponseForScope(resolution.ScopeID, response, submitErr)
	})
}

func (c *CurrentNodeController) completeLiveCurrentNode(ctx context.Context, req workflowruntime.CompletionRequest) (workflowstore.CurrentNodeCompletionResult, error) {
	var completed workflowstore.CurrentNodeCompletionResult
	err := c.permit.Run(ctx, func(ctx context.Context) error {
		c.mu.Lock()
		live, exists := c.live[req.ScopeID]
		if !exists {
			c.mu.Unlock()
			return sessionruntime.ErrExecutionNoLongerLive
		}
		if _, stopping := c.stopping[req.ScopeID]; stopping {
			c.mu.Unlock()
			return sessionruntime.ErrExecutionNoLongerLive
		}
		if err := c.ensureTaskAvailableLocked(live.reference.TaskID); err != nil {
			c.mu.Unlock()
			return err
		}
		c.mu.Unlock()
		handle, ok := c.authority.ExecutionByScope(req.ScopeID)
		if !ok {
			return sessionruntime.ErrExecutionNoLongerLive
		}
		return c.authority.WithExactExecutions([]sessionruntime.ExecutionHandle{handle}, func() error {
			c.mu.Lock()
			exact, stillLive := c.live[req.ScopeID]
			if !stillLive || !exact.reference.Equal(live.reference) {
				c.mu.Unlock()
				return sessionruntime.ErrExecutionNoLongerLive
			}
			if _, stopping := c.stopping[req.ScopeID]; stopping {
				c.mu.Unlock()
				return sessionruntime.ErrExecutionNoLongerLive
			}
			if err := c.ensureTaskAvailableLocked(exact.reference.TaskID); err != nil {
				c.mu.Unlock()
				return err
			}
			c.mu.Unlock()
			var completionErr error
			completed, completionErr = c.store.CompleteCurrentNode(ctx, workflowstore.CurrentNodeCompletionRequest{
				Source:       exact.reference,
				TransitionID: req.TransitionID,
				OutputValues: req.OutputValues,
				Commentary:   req.Commentary,
			})
			if completionErr != nil {
				return completionErr
			}
			intents, intentErr := currentNodeAutomaticIntents(completed.AutomaticIntents)
			if intentErr != nil {
				return intentErr
			}
			c.mu.Lock()
			c.completed[req.ScopeID] = struct{}{}
			c.heldStarts[req.ScopeID] = automaticQueuedStarts(intents)
			c.mu.Unlock()
			return nil
		})
	})
	if err != nil {
		return workflowstore.CurrentNodeCompletionResult{}, err
	}
	return completed, nil
}

// CompleteIdleCurrentNode applies a forced completion only after the
// controller has established that it owns no active, admitted, queued, or
// retirement-held work for the Task. Unlike a live completion there is no
// predecessor scope, so successor Automatic Intents are enqueued immediately
// after the aggregate mutation commits.
func (c *CurrentNodeController) CompleteIdleCurrentNode(
	ctx context.Context,
	selector workflowstore.IdleCurrentNodeSelector,
	transitionID string,
	outputValues map[string]string,
	commentary string,
) (workflowstore.CurrentNodeCompletionResult, error) {
	if c == nil {
		return workflowstore.CurrentNodeCompletionResult{}, errors.New("current node workflow controller is required")
	}
	return RunMutation(ctx, c.permit, func(ctx context.Context) (workflowstore.CurrentNodeCompletionResult, error) {
		source, err := c.store.ResolveIdleExecutableCurrentNode(ctx, selector)
		if err != nil {
			return workflowstore.CurrentNodeCompletionResult{}, err
		}
		if err := c.EnsureTaskQuiescent(source.Reference.TaskID); err != nil {
			return workflowstore.CurrentNodeCompletionResult{}, err
		}
		completed, err := c.store.CompleteCurrentNode(ctx, workflowstore.CurrentNodeCompletionRequest{
			Source:       source.Reference,
			TransitionID: transitionID,
			OutputValues: outputValues,
			Commentary:   commentary,
		})
		if err != nil {
			return workflowstore.CurrentNodeCompletionResult{}, err
		}
		intents, err := currentNodeAutomaticIntents(completed.AutomaticIntents)
		if err != nil {
			return workflowstore.CurrentNodeCompletionResult{}, err
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		for _, start := range automaticQueuedStarts(intents) {
			if err := c.queueAutomaticStartLocked(start.reference); err != nil {
				return workflowstore.CurrentNodeCompletionResult{}, err
			}
		}
		return completed, nil
	})
}

func (c *CurrentNodeController) RecordProtocolViolation(ctx context.Context, req workflowruntime.ViolationRequest) (workflowruntime.ViolationResult, error) {
	if req.ScopeID.IsZero() {
		return workflowruntime.ViolationResult{}, errors.New("workflow exact execution scope id is required")
	}
	if req.MaxCount <= 0 {
		return workflowruntime.ViolationResult{}, errors.New("workflow protocol violation cap must be positive")
	}
	if _, err := c.liveLease(req.ScopeID); err != nil {
		return workflowruntime.ViolationResult{}, err
	}
	c.mu.Lock()
	c.violations[req.ScopeID]++
	count := c.violations[req.ScopeID]
	c.mu.Unlock()
	interrupted := count >= int64(req.MaxCount)
	if interrupted {
		cause := errors.New("workflow protocol violation budget exhausted")
		if req.Detail != "" {
			cause = errors.New(req.Detail)
		}
		if err := c.FailCurrentNodeScope(ctx, req.ScopeID, reasonProtocolViolationCap, cause); err != nil {
			return workflowruntime.ViolationResult{}, err
		}
	}
	return workflowruntime.ViolationResult{Count: count, Interrupted: interrupted}, nil
}

func (c *CurrentNodeController) ResetProtocolViolationBudget(_ context.Context, req workflowruntime.ViolationResetRequest) error {
	if _, err := c.liveLease(req.ScopeID); err != nil {
		return err
	}
	c.mu.Lock()
	delete(c.violations, req.ScopeID)
	c.mu.Unlock()
	return nil
}

func (c *CurrentNodeController) ObserveCurrentNodeCompletion(_ context.Context, req workflowruntime.CompletionObservationRequest) (workflowruntime.CompletionObservationResult, error) {
	if req.ScopeID.IsZero() {
		return workflowruntime.CompletionObservationResult{}, errors.New("workflow exact execution scope id is required")
	}
	c.mu.Lock()
	_, completed := c.completed[req.ScopeID]
	c.mu.Unlock()
	return workflowruntime.CompletionObservationResult{Completed: completed}, nil
}

func (c *CurrentNodeController) FailCurrentNodeScope(
	ctx context.Context,
	scopeID runtimeids.ExecutionScopeID,
	reason workflow.CurrentNodeInterruptionReason,
	cause error,
) error {
	detail := workflow.CurrentNodeInterruptionDetail{Code: string(reason)}
	if cause != nil {
		detail.Fields = map[string]string{"error": cause.Error()}
	}
	var lease sessionruntime.WorkflowExecutionLease
	if err := c.permit.Run(ctx, func(ctx context.Context) error {
		c.mu.Lock()
		if _, stopping := c.stopping[scopeID]; stopping {
			c.mu.Unlock()
			return sessionruntime.ErrExecutionNoLongerLive
		}
		live, exists := c.live[scopeID]
		if !exists {
			c.mu.Unlock()
			return sessionruntime.ErrExecutionNoLongerLive
		}
		if c.closed {
			c.mu.Unlock()
			return errors.New("current node workflow controller is closed")
		}
		if c.interrupts.taskActive(live.reference.TaskID) {
			c.mu.Unlock()
			return ErrTaskExecutionNotQuiescent
		}
		lease = live.lease
		c.mu.Unlock()
		return c.store.InterruptAdmittedCurrentNode(ctx, lease.Workflow().CurrentNode, reason, detail)
	}); err != nil {
		return err
	}
	c.publishPendingInterruptedCurrentNode(ctx, lease.Workflow().CurrentNode, reason)
	if handle, live := c.authority.ExecutionByScope(scopeID); live {
		handle.RequestStop()
	}
	return nil
}

func (c *CurrentNodeController) ExecutionFinalized(scope sessionruntime.ExecutionScope) {
	ref, ok := scope.Workflow()
	if !ok || c == nil {
		return
	}
	key, err := ref.CurrentNode.Key()
	if err != nil {
		panic(fmt.Sprintf("workflow execution finalized invalid current node: %v", err))
	}
	c.mu.Lock()
	live, isLive := c.live[scope.ID()]
	delete(c.live, scope.ID())
	if current, exists := c.liveByNode[key]; exists && current == scope.ID() {
		delete(c.liveByNode, key)
	}
	delete(c.violations, scope.ID())
	delete(c.stopping, scope.ID())
	_, completed := c.completed[scope.ID()]
	delete(c.completed, scope.ID())
	starts := append([]currentNodeQueuedStart(nil), c.heldStarts[scope.ID()]...)
	delete(c.heldStarts, scope.ID())
	if gate, gated := c.gates[key]; gated && gate.lease.ScopeID() == scope.ID() {
		delete(c.gates, key)
	}
	interrupted := c.interrupts.scopeFenced(scope.ID())
	c.interrupts.finishScope(scope.ID())
	closed := c.closed
	c.mu.Unlock()
	if !closed {
		c.wakeAdmissionWorker()
	}
	if !isLive || !live.lease.Workflow().CurrentNode.Equal(ref.CurrentNode) || !completed || interrupted || closed {
		return
	}
	c.enqueueStarts(starts)
}

func (c *CurrentNodeController) Snapshot() CurrentNodeExecutionSnapshot {
	if c == nil {
		return CurrentNodeExecutionSnapshot{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	snapshot := CurrentNodeExecutionSnapshot{
		AutomaticIntents: make([]CurrentNodeAutomaticIntent, 0, len(c.automaticQueue)+len(c.automaticReservations)),
		ExplicitStarts:   make([]CurrentNodeExplicitStart, 0, len(c.explicitQueue)+len(c.explicitReservations)),
		Gates:            make([]CurrentNodeAdmissionGateSnapshot, 0, len(c.gates)),
		LiveScopes:       make([]CurrentNodeLiveScopeSnapshot, 0, len(c.live)),
	}
	snapshot.AutomaticIntents = append(snapshot.AutomaticIntents, c.automaticQueue...)
	for _, start := range c.automaticReservations {
		snapshot.AutomaticIntents = append(snapshot.AutomaticIntents, CurrentNodeAutomaticIntent{CurrentNode: start.reference})
	}
	for _, reference := range c.explicitQueue {
		snapshot.ExplicitStarts = append(snapshot.ExplicitStarts, CurrentNodeExplicitStart{CurrentNode: reference})
	}
	for _, start := range c.explicitReservations {
		snapshot.ExplicitStarts = append(snapshot.ExplicitStarts, CurrentNodeExplicitStart{CurrentNode: start.reference})
	}
	for _, gate := range c.gates {
		snapshot.Gates = append(snapshot.Gates, CurrentNodeAdmissionGateSnapshot{
			CurrentNode: gate.reference,
			ScopeID:     gate.lease.ScopeID(),
			Automatic:   gate.automatic,
		})
	}
	for scopeID, live := range c.live {
		snapshot.LiveScopes = append(snapshot.LiveScopes, CurrentNodeLiveScopeSnapshot{
			CurrentNode: live.reference,
			ScopeID:     scopeID,
			Automatic:   live.automatic,
		})
	}
	for sourceScope, starts := range c.heldStarts {
		for _, start := range starts {
			snapshot.HeldIntents = append(snapshot.HeldIntents, CurrentNodeHeldIntentSnapshot{
				CurrentNode: start.reference,
				SourceScope: sourceScope,
				Automatic:   start.automatic,
			})
		}
	}
	snapshot.InterruptingTasks = c.interrupts.taskIDs()
	return snapshot
}

func (c *CurrentNodeController) Close() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	c.explicitQueue = nil
	c.explicitQueued = make(map[workflow.CurrentNodeReferenceKey]struct{})
	c.automaticQueue = nil
	c.queued = make(map[workflow.CurrentNodeReferenceKey]struct{})
	c.heldStarts = make(map[runtimeids.ExecutionScopeID][]currentNodeQueuedStart)
	gates := make([]currentNodeAdmissionGate, 0, len(c.gates))
	for _, gate := range c.gates {
		gates = append(gates, gate)
	}
	liveScopes := make([]runtimeids.ExecutionScopeID, 0, len(c.live))
	for scopeID := range c.live {
		liveScopes = append(liveScopes, scopeID)
	}
	c.mu.Unlock()

	c.workerCancel()
	for _, gate := range gates {
		gate.lease.Cancel()
	}
	var stopErrs []error
	for _, scopeID := range liveScopes {
		handle, exists := c.authority.ExecutionByScope(scopeID)
		if !exists {
			continue
		}
		if err := handle.Stop(context.Background()); err != nil {
			stopErrs = append(stopErrs, fmt.Errorf("stop workflow execution scope %s: %w", scopeID, err))
		}
	}
	c.workerWG.Wait()
	c.admissionWG.Wait()
	c.mu.Lock()
	workerErr := c.workerErr
	c.mu.Unlock()
	return errors.Join(errors.Join(stopErrs...), workerErr)
}

func (c *CurrentNodeController) liveLease(scopeID runtimeids.ExecutionScopeID) (sessionruntime.WorkflowExecutionLease, error) {
	if c == nil || scopeID.IsZero() {
		return sessionruntime.WorkflowExecutionLease{}, errors.New("workflow exact execution scope id is required")
	}
	c.mu.Lock()
	if _, stopping := c.stopping[scopeID]; stopping {
		c.mu.Unlock()
		return sessionruntime.WorkflowExecutionLease{}, sessionruntime.ErrExecutionNoLongerLive
	}
	live, exists := c.live[scopeID]
	c.mu.Unlock()
	if !exists {
		return sessionruntime.WorkflowExecutionLease{}, sessionruntime.ErrExecutionNoLongerLive
	}
	return live.lease, nil
}

var _ workflowruntime.Controller = (*CurrentNodeController)(nil)
var _ sessionruntime.ExecutionFinalized = (*CurrentNodeController)(nil)
