package workflowexecution

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"core/server/session"
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
	StartCurrentNodeWithPreparation(
		context.Context,
		workflow.CurrentNodeReference,
		LaunchPreparation,
		workflowruntime.TaskPromptDelivery,
		CurrentNodeAssignmentSteer,
		sessionruntime.WorkflowExecutionLease,
		workflowruntime.Controller,
	) error
}

type CurrentNodeAssignmentSteerer interface {
	SteerCurrentNodeAssignment(context.Context, workflow.CurrentNodeReference) (CurrentNodeAssignmentSteer, error)
}

type CurrentNodeAssignmentSteer interface {
	Wait(context.Context) (session.CommitReceipt, error)
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
		StartTask(context.Context, workflow.TaskID) (workflowstore.StartTaskResult, error)
		InterruptedExecutableCurrentNodes(context.Context, workflow.TaskID) ([]workflow.CurrentNode, error)
		AdmitCurrentNode(context.Context, workflow.CurrentNodeReference) error
		ResumeCurrentNode(context.Context, workflow.CurrentNodeReference) (workflowstore.InterruptedCurrentNodeAttentionProjection, bool, error)
		PendingApproval(context.Context, workflow.ApprovalID) (workflow.PendingApproval, error)
		ApplyPendingApproval(context.Context, workflow.ApprovalID) (workflowstore.PendingApprovalApplyResult, error)
		ApplyManualMove(context.Context, workflowstore.ManualMovePreparation) (workflowstore.ManualMoveResult, error)
		InterruptAdmittedCurrentNode(context.Context, workflow.CurrentNodeReference, workflow.CurrentNodeInterruptionReason, workflow.CurrentNodeInterruptionDetail) error
		InterruptCurrentNode(context.Context, workflow.CurrentNodeReference, workflow.CurrentNodeInterruptionReason, workflow.CurrentNodeInterruptionDetail) error
		RecoverExecutableCurrentNodes(context.Context, workflow.CurrentNodeInterruptionReason, workflow.CurrentNodeInterruptionDetail) ([]workflow.CurrentNodeReference, error)
		ResolveIdleExecutableCurrentNode(context.Context, workflowstore.IdleCurrentNodeSelector) (workflow.CurrentNode, error)
		CompleteCurrentNode(context.Context, workflowstore.CurrentNodeCompletionRequest) (workflowstore.CurrentNodeCompletionResult, error)
		ValidateCurrentNodeSessionBinding(context.Context, runtimeids.SessionID, workflow.CurrentNodeReference) error
		TaskExecutionScope(context.Context, workflow.TaskID) (workflowstore.TaskExecutionScope, error)
	}
	runner    CurrentNodeRunner
	steerer   CurrentNodeAssignmentSteerer
	authority *sessionruntime.Authority
	permit    *MutationPermit
	attention CurrentNodeAttentionLifecycle

	agentConcurrency int
	workerContext    context.Context
	workerCancel     context.CancelFunc
	workerWake       chan struct{}
	workerWG         sync.WaitGroup
	admissionWG      sync.WaitGroup

	mu                    sync.Mutex
	closed                bool
	gates                 map[workflow.CurrentNodeReferenceKey]currentNodeAdmissionGate
	live                  map[runtimeids.ExecutionScopeID]currentNodeLiveScope
	liveByNode            map[workflow.CurrentNodeReferenceKey]runtimeids.ExecutionScopeID
	stopping              map[runtimeids.ExecutionScopeID]struct{}
	completed             map[runtimeids.ExecutionScopeID]struct{}
	violations            map[runtimeids.ExecutionScopeID]int64
	heldStarts            map[runtimeids.ExecutionScopeID][]currentNodeQueuedStart
	explicitQueue         []currentNodeQueuedStart
	explicitQueued        map[workflow.CurrentNodeReferenceKey]struct{}
	explicitReservations  map[workflow.CurrentNodeReferenceKey]currentNodeQueuedStart
	automaticQueue        currentNodeAutomaticQueue
	queued                map[workflow.CurrentNodeReferenceKey]struct{}
	automaticReservations map[workflow.CurrentNodeReferenceKey]currentNodeQueuedStart
	admissionWorkers      map[workflow.CurrentNodeReferenceKey]currentNodeQueuedStart
	agentCapacityActive   int
	interrupts            currentNodeInterruptState
	workerErr             error
	lastAutomaticTask     *workflow.TaskID
}

func NewCurrentNodeController(
	store interface {
		StartTask(context.Context, workflow.TaskID) (workflowstore.StartTaskResult, error)
		InterruptedExecutableCurrentNodes(context.Context, workflow.TaskID) ([]workflow.CurrentNode, error)
		AdmitCurrentNode(context.Context, workflow.CurrentNodeReference) error
		ResumeCurrentNode(context.Context, workflow.CurrentNodeReference) (workflowstore.InterruptedCurrentNodeAttentionProjection, bool, error)
		PendingApproval(context.Context, workflow.ApprovalID) (workflow.PendingApproval, error)
		ApplyPendingApproval(context.Context, workflow.ApprovalID) (workflowstore.PendingApprovalApplyResult, error)
		ApplyManualMove(context.Context, workflowstore.ManualMovePreparation) (workflowstore.ManualMoveResult, error)
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
	if cfg.AssignmentSteerer == nil {
		return nil, errors.New("current node assignment steerer is required")
	}
	if authority == nil {
		return nil, errors.New("session runtime authority is required")
	}
	if permit == nil {
		return nil, errors.New("workflow mutation permit is required")
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
		permit:                permit,
		attention:             cfg.Attention,
		agentConcurrency:      cfg.AgentConcurrency,
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
	if errors.Is(err, sessionruntime.ErrExecutionNoLongerLive) && req.SessionID != nil {
		_, err = c.CompleteIdleCurrentNode(ctx, workflowstore.IdleCurrentNodeSelector{
			SessionID: req.SessionID,
		}, req.TransitionID, req.OutputValues, req.Commentary)
		if err == nil {
			c.clearProtocolViolations(req.ScopeID)
		}
	}
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

type WorkflowQuestionAcceptance interface {
	AwaitSuccessor(context.Context) error
}

// AcceptWorkflowQuestion delivers an answer only after resolving one exact
// live Authority prompt and proving its Session still owns the same Current
// Node in durable Task state. Prompt state itself remains volatile.
func (c *CurrentNodeController) AcceptWorkflowQuestion(
	ctx context.Context,
	taskID workflow.TaskID,
	askID string,
	response askquestion.AskQuestionResponse,
	submitErr error,
) (WorkflowQuestionAcceptance, error) {
	if c == nil {
		return nil, errors.New("current node workflow controller is required")
	}
	if strings.TrimSpace(string(taskID)) == "" {
		return nil, errors.New("workflow task id is required")
	}
	askID = strings.TrimSpace(askID)
	if askID == "" {
		return nil, errors.New("workflow ask id is required")
	}
	if strings.TrimSpace(response.RequestID) != askID {
		return nil, errors.New("workflow question response does not match ask id")
	}
	var acceptance sessionruntime.PromptResponseAcceptance
	err := c.permit.Run(ctx, func(ctx context.Context) error {
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
		acceptance, err = c.authority.AcceptPromptResponseForScope(resolution.ScopeID, response, submitErr)
		return err
	})
	if err != nil {
		return nil, err
	}
	return acceptance, nil
}

func (c *CurrentNodeController) completeLiveCurrentNode(ctx context.Context, req workflowruntime.CompletionRequest) (workflowstore.CurrentNodeCompletionResult, error) {
	var completed workflowstore.CurrentNodeCompletionResult
	var starts []currentNodeQueuedStart
	var pending []*pendingCurrentNodeAssignmentSteer
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
		if err := c.authority.WithExactExecutions([]sessionruntime.ExecutionHandle{handle}, func() error {
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
			starts, pending = pendingCurrentNodeAssignmentStarts(automaticQueuedStarts(intents))
			c.mu.Lock()
			c.completed[req.ScopeID] = struct{}{}
			c.heldStarts[req.ScopeID] = starts
			c.mu.Unlock()
			return nil
		}); err != nil {
			return err
		}
		return c.resolvePendingCurrentNodeAssignmentSteers(ctx, starts, pending)
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
		source, err := c.resolveQuiescentIdleCurrentNode(ctx, selector)
		if err != nil {
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
		starts, err := c.steerAndWaitStarts(ctx, automaticQueuedStarts(intents), recoverCommittedCurrentNodeStarts)
		if err != nil {
			return workflowstore.CurrentNodeCompletionResult{}, err
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		for _, start := range starts {
			if err := c.queueAutomaticStartLocked(start); err != nil {
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
		if errors.Is(err, sessionruntime.ErrExecutionNoLongerLive) && req.SessionID != nil {
			return c.recordIdleCurrentNodeProtocolViolation(ctx, req)
		}
		return workflowruntime.ViolationResult{}, err
	}
	result := c.incrementProtocolViolation(req.ScopeID, req.MaxCount)
	if result.Interrupted {
		if err := c.FailCurrentNodeScope(ctx, req.ScopeID, reasonProtocolViolationCap, workflowProtocolViolationCause(req)); err != nil {
			return workflowruntime.ViolationResult{}, err
		}
	}
	return result, nil
}

func (c *CurrentNodeController) recordIdleCurrentNodeProtocolViolation(
	ctx context.Context,
	req workflowruntime.ViolationRequest,
) (workflowruntime.ViolationResult, error) {
	var interruptedReference *workflow.CurrentNodeReference
	result, err := RunMutation(ctx, c.permit, func(ctx context.Context) (workflowruntime.ViolationResult, error) {
		source, err := c.resolveQuiescentIdleCurrentNode(ctx, workflowstore.IdleCurrentNodeSelector{
			SessionID: req.SessionID,
		})
		if err != nil {
			return workflowruntime.ViolationResult{}, err
		}
		if source.Scheduling == nil {
			return workflowruntime.ViolationResult{}, errors.New("idle current node scheduling is required")
		}
		switch source.Scheduling.State {
		case workflow.CurrentNodeSchedulingReady, workflow.CurrentNodeSchedulingInterrupted:
		default:
			return workflowruntime.ViolationResult{}, fmt.Errorf(
				"idle current node has unsupported scheduling state %q",
				source.Scheduling.State,
			)
		}
		result := c.incrementProtocolViolation(req.ScopeID, req.MaxCount)
		if !result.Interrupted {
			return result, nil
		}
		if source.Scheduling.State == workflow.CurrentNodeSchedulingInterrupted {
			c.clearProtocolViolations(req.ScopeID)
			return result, nil
		}
		if err := c.store.InterruptCurrentNode(
			ctx,
			source.Reference,
			reasonProtocolViolationCap,
			workflow.CurrentNodeInterruptionDetail{
				Code:   string(reasonProtocolViolationCap),
				Fields: map[string]string{"error": workflowProtocolViolationCause(req).Error()},
			},
		); err != nil {
			return workflowruntime.ViolationResult{}, err
		}
		reference := source.Reference
		interruptedReference = &reference
		c.clearProtocolViolations(req.ScopeID)
		return result, nil
	})
	if err != nil {
		return workflowruntime.ViolationResult{}, err
	}
	if interruptedReference != nil {
		c.publishPendingInterruptedCurrentNode(ctx, *interruptedReference, reasonProtocolViolationCap)
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

func (c *CurrentNodeController) incrementProtocolViolation(
	scopeID runtimeids.ExecutionScopeID,
	maxCount int,
) workflowruntime.ViolationResult {
	c.mu.Lock()
	c.violations[scopeID]++
	count := c.violations[scopeID]
	c.mu.Unlock()
	return workflowruntime.ViolationResult{
		Count:       count,
		Interrupted: count >= int64(maxCount),
	}
}

func (c *CurrentNodeController) clearProtocolViolations(scopeID runtimeids.ExecutionScopeID) {
	c.mu.Lock()
	delete(c.violations, scopeID)
	c.mu.Unlock()
}

func workflowProtocolViolationCause(req workflowruntime.ViolationRequest) error {
	if req.Detail != "" {
		return errors.New(req.Detail)
	}
	return errors.New("workflow protocol violation budget exhausted")
}

func (c *CurrentNodeController) ResetProtocolViolationBudget(ctx context.Context, req workflowruntime.ViolationResetRequest) error {
	if _, err := c.liveLease(req.ScopeID); err != nil {
		if !errors.Is(err, sessionruntime.ErrExecutionNoLongerLive) || req.SessionID == nil {
			return err
		}
		_, err = RunMutation(ctx, c.permit, func(ctx context.Context) (workflow.CurrentNode, error) {
			source, err := c.resolveQuiescentIdleCurrentNode(ctx, workflowstore.IdleCurrentNodeSelector{
				SessionID: req.SessionID,
			})
			if err == nil {
				c.clearProtocolViolations(req.ScopeID)
			}
			return source, err
		})
		return err
	}
	c.clearProtocolViolations(req.ScopeID)
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
	if isLive {
		c.releaseAgentCapacityLocked(live.agentCapacityLease)
	}
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
		c.releaseAgentCapacityLocked(gate.agentCapacityLease)
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
		if !closed {
			c.wakeAdmissionWorker()
		}
		return
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), interruptCleanupTimeout)
	defer cancel()
	outcome := waitCurrentNodeAssignmentSteers(waitCtx, starts)
	if outcome.err != nil {
		if len(outcome.pending) != 0 {
			c.continueCurrentNodeAssignmentStarts(starts, nil)
			return
		}
		c.handleCurrentNodeStartFailures(outcome.committed, false, outcome.err)
		return
	}
	c.enqueueStarts(starts)
	if len(starts) == 0 {
		c.wakeAdmissionWorker()
	}
}

func (c *CurrentNodeController) Snapshot() CurrentNodeExecutionSnapshot {
	if c == nil {
		return CurrentNodeExecutionSnapshot{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	snapshot := CurrentNodeExecutionSnapshot{
		AutomaticIntents: make([]CurrentNodeAutomaticIntent, 0, c.automaticQueue.len()+len(c.automaticReservations)),
		ExplicitStarts:   make([]CurrentNodeExplicitStart, 0, len(c.explicitQueue)+len(c.explicitReservations)),
		Gates:            make([]CurrentNodeAdmissionGateSnapshot, 0, len(c.gates)),
		LiveScopes:       make([]CurrentNodeLiveScopeSnapshot, 0, len(c.live)),
	}
	for entry := c.automaticQueue.first; entry != nil; entry = entry.globalNext {
		start := entry.start
		snapshot.AutomaticIntents = append(snapshot.AutomaticIntents, CurrentNodeAutomaticIntent{
			CurrentNode: start.reference,
			NodeKind:    start.policy.nodeKind(),
		})
	}
	for _, start := range c.automaticReservations {
		snapshot.AutomaticIntents = append(snapshot.AutomaticIntents, CurrentNodeAutomaticIntent{
			CurrentNode: start.reference,
			NodeKind:    start.policy.nodeKind(),
		})
	}
	for _, start := range c.explicitQueue {
		snapshot.ExplicitStarts = append(snapshot.ExplicitStarts, CurrentNodeExplicitStart{CurrentNode: start.reference})
	}
	for _, start := range c.explicitReservations {
		snapshot.ExplicitStarts = append(snapshot.ExplicitStarts, CurrentNodeExplicitStart{CurrentNode: start.reference})
	}
	for _, gate := range c.gates {
		snapshot.Gates = append(snapshot.Gates, CurrentNodeAdmissionGateSnapshot{
			CurrentNode: gate.reference,
			ScopeID:     gate.lease.ScopeID(),
			Automatic:   gate.policy.isAutomatic(),
		})
	}
	for scopeID, live := range c.live {
		snapshot.LiveScopes = append(snapshot.LiveScopes, CurrentNodeLiveScopeSnapshot{
			CurrentNode: live.reference,
			ScopeID:     scopeID,
			Automatic:   live.policy.isAutomatic(),
		})
	}
	for sourceScope, starts := range c.heldStarts {
		for _, start := range starts {
			snapshot.HeldIntents = append(snapshot.HeldIntents, CurrentNodeHeldIntentSnapshot{
				CurrentNode: start.reference,
				SourceScope: sourceScope,
				Automatic:   start.policy.isAutomatic(),
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
	c.automaticQueue.clear()
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
	handles := make([]sessionruntime.ExecutionHandle, 0, len(liveScopes))
	for _, scopeID := range liveScopes {
		handle, exists := c.authority.ExecutionByScope(scopeID)
		if exists {
			handles = append(handles, handle)
		}
	}
	for _, handle := range handles {
		handle.RequestStop()
	}
	var stopErrs []error
	for _, handle := range handles {
		scopeID := handle.Scope().ID()
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
