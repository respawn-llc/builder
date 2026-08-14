package workflowexecution

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
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
	reasonProtocolViolationCap                      workflow.CurrentNodeInterruptionReason = "workflow_protocol_violation_cap"
	reasonCurrentNodeRuntimeFinalizedWithoutOutcome workflow.CurrentNodeInterruptionReason = "workflow_runtime_finalized_without_outcome"
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

type CurrentNodeAgentPublicationPreparation interface {
	PrepareAgentPublication(
		context.Context,
		workflowruntime.TaskPromptDelivery,
		runtimeids.CurrentNodeOperationID,
		workflowruntime.Controller,
	) (CurrentNodeAgentPublication, error)
}

type CurrentNodeAgentPublicationRunner interface {
	PrepareAgentPublication(
		context.Context,
		workflow.CurrentNodeReference,
		runtimeids.CurrentNodeOperationID,
		workflowruntime.TaskPromptDelivery,
		CurrentNodeAssignmentSteer,
		workflowruntime.Controller,
	) (CurrentNodeAgentPublication, error)
}

type CurrentNodeAgentPublication interface {
	Publish(context.Context, func() error, func(sessionruntime.ExecutionHandle)) (sessionruntime.ExecutionHandle, func(), error)
	Cancel() error
}

type CurrentNodeScriptPublicationPreparation interface {
	PrepareScriptPublication(
		context.Context,
		workflow.CurrentNodeReference,
		runtimeids.CurrentNodeOperationID,
		workflowruntime.Controller,
	) (CurrentNodeScriptPublication, error)
}

type CurrentNodePublicationRunner interface {
	CurrentNodeAgentPublicationRunner
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

type currentNodePostTurnFinalization struct {
	sessionID      *runtimeids.SessionID
	classification workflow.SessionReuseClassification
	reference      workflow.CurrentNodeReference
	starts         []currentNodeQueuedStart
}

type currentNodeOperation struct {
	ref                   workflow.CurrentNodeOperationRef
	workflow              *sessionruntime.WorkflowExecutionRef
	policy                currentNodeAdmissionPolicy
	agentCapacityLease    *currentNodeAgentCapacityLease
	completion            *workflowstore.CurrentNodeCompletionResult
	postTurnFinalization  *currentNodePostTurnFinalization
	postTurnSettlement    *workflowruntime.PostTurnSettlement
	heldStarts            []currentNodeQueuedStart
	authorityRetired      bool
	retirementDisposition sessionruntime.WorkflowRetirementDisposition
}

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
	operations            map[workflow.CurrentNodeReferenceKey]*currentNodeOperation
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
		operations:            make(map[workflow.CurrentNodeReferenceKey]*currentNodeOperation),
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
) (workflowruntime.CompletionOutcome, error) {
	completed, operation, diagnostic, err := c.completeAgentCurrentNode(ctx, req)
	if err != nil {
		return workflowruntime.RejectedCompletionOutcome(err), err
	}
	return workflowruntime.AcceptedCompletionOutcome(workflowruntime.AcceptedCompletion{
		Result: workflowruntime.CompletionResult{
			TransitionID:    workflow.TransitionID(req.TransitionID),
			State:           "applied",
			Operation:       operation,
			CommittedResult: completed,
		},
		Diagnostic: diagnostic,
	}), nil
}

func (c *CurrentNodeController) CompleteScriptCurrentNode(
	ctx context.Context,
	req workflowruntime.ScriptCompletionRequest,
) (workflowruntime.CompletionOutcome, error) {
	if req.ScopeID.IsZero() {
		err := errors.New("Script completion requires an Exact Execution Scope")
		return workflowruntime.RejectedCompletionOutcome(err), err
	}
	completed, operation, diagnostic, err := c.completeLiveCurrentNode(
		ctx,
		req.ScopeID,
		req.TransitionID,
		req.OutputValues,
		req.Commentary,
		func(commit func() (workflowruntime.CompletionDecision, error)) (workflowruntime.CompletionDecision, error) {
			return c.authority.CompleteFinalizingScript(req.ScopeID, commit)
		},
	)
	if err != nil {
		return workflowruntime.RejectedCompletionOutcome(err), err
	}
	return workflowruntime.AcceptedCompletionOutcome(workflowruntime.AcceptedCompletion{
		Result: workflowruntime.CompletionResult{
			TransitionID:    workflow.TransitionID(req.TransitionID),
			State:           "applied",
			Operation:       operation,
			CommittedResult: completed,
		},
		Diagnostic: diagnostic,
	}), nil
}

func (c *CurrentNodeController) CompleteSessionCurrentNode(
	ctx context.Context,
	sessionID runtimeids.SessionID,
	runID runtimeids.RunID,
	stepID runtimeids.StepID,
	transitionID string,
	outputValues map[string]string,
	commentary string,
) (workflowruntime.CompletionOutcome, error) {
	if c == nil {
		err := errors.New("current node workflow controller is required")
		return workflowruntime.RejectedCompletionOutcome(err), err
	}
	if sessionID.IsZero() {
		err := errors.New("session id is required")
		return workflowruntime.RejectedCompletionOutcome(err), err
	}
	handle, live := c.authority.SessionExecution(sessionID)
	if !live || handle.Scope().Kind() != sessionruntime.ExecutionScopeAgent {
		return workflowruntime.RejectedCompletionOutcome(sessionruntime.ErrExecutionNoLongerLive), sessionruntime.ErrExecutionNoLongerLive
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
) (workflowstore.CurrentNodeCompletionResult, workflow.CurrentNodeOperationRef, error, error) {
	if c == nil {
		return workflowstore.CurrentNodeCompletionResult{}, workflow.CurrentNodeOperationRef{}, nil, errors.New("current node workflow controller is required")
	}
	if req.Provenance.ScopeID.IsZero() || req.Provenance.RunID.IsZero() ||
		req.Provenance.StepID.IsZero() || req.SessionID.IsZero() {
		return workflowstore.CurrentNodeCompletionResult{}, workflow.CurrentNodeOperationRef{}, nil, errors.New("Agent completion requires Scope, Session, Run, and Step provenance")
	}
	handle, live := c.authority.SessionExecution(req.SessionID)
	if !live || handle.Scope().Kind() != sessionruntime.ExecutionScopeAgent ||
		handle.Scope().ID() != req.Provenance.ScopeID {
		return workflowstore.CurrentNodeCompletionResult{}, workflow.CurrentNodeOperationRef{}, nil, sessionruntime.ErrExecutionNoLongerLive
	}
	return c.completeLiveCurrentNode(
		ctx,
		req.Provenance.ScopeID,
		req.TransitionID,
		req.OutputValues,
		req.Commentary,
		func(commit func() (workflowruntime.CompletionDecision, error)) (workflowruntime.CompletionDecision, error) {
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
		func() (workflowruntime.CompletionDecision, error),
	) (workflowruntime.CompletionDecision, error),
) (workflowstore.CurrentNodeCompletionResult, workflow.CurrentNodeOperationRef, error, error) {
	handle, live := c.authority.ExecutionByScope(scopeID)
	if !live {
		return workflowstore.CurrentNodeCompletionResult{}, workflow.CurrentNodeOperationRef{}, nil, sessionruntime.ErrExecutionNoLongerLive
	}
	workflowRef, workflowScoped := handle.Scope().Workflow()
	if !workflowScoped {
		return workflowstore.CurrentNodeCompletionResult{}, workflow.CurrentNodeOperationRef{}, nil, sessionruntime.ErrExecutionNoLongerLive
	}
	key, err := workflowRef.CurrentNode.Key()
	if err != nil {
		return workflowstore.CurrentNodeCompletionResult{}, workflow.CurrentNodeOperationRef{}, nil, err
	}
	var completed workflowstore.CurrentNodeCompletionResult
	var completionDiagnostic error
	var starts []currentNodeQueuedStart
	var settledStarts []currentNodeQueuedStart
	var pending []*pendingCurrentNodeAssignmentSteer
	err = c.runTaskMutation(ctx, workflowRef.CurrentNode.TaskID, func(ctx context.Context) error {
		c.mu.Lock()
		operation := c.operations[key]
		if operation == nil || operation.ref.OperationID != workflowRef.OperationID || operation.completion != nil {
			c.mu.Unlock()
			return sessionruntime.ErrExecutionNoLongerLive
		}
		if err := c.ensureTaskAvailableLocked(workflowRef.CurrentNode.TaskID); err != nil {
			c.mu.Unlock()
			return err
		}
		c.mu.Unlock()
		if _, err := validateAndCommit(func() (workflowruntime.CompletionDecision, error) {
			c.mu.Lock()
			exact := c.operations[key]
			if exact == nil || exact.ref.OperationID != workflowRef.OperationID || exact.completion != nil {
				c.mu.Unlock()
				return workflowruntime.CompletionDecision{}, sessionruntime.ErrExecutionNoLongerLive
			}
			if err := c.ensureTaskAvailableLocked(workflowRef.CurrentNode.TaskID); err != nil {
				c.mu.Unlock()
				return workflowruntime.CompletionDecision{}, err
			}
			c.mu.Unlock()
			outcome, completionErr := c.store.CompleteCurrentNode(ctx, workflowstore.CurrentNodeCompletionRequest{
				Source:       workflowRef.CurrentNode,
				TransitionID: transitionID,
				OutputValues: outputValues,
				Commentary:   commentary,
			})
			decision := workflowruntime.CompletionDecision{
				CommitReceipt:        outcome.CommitReceipt,
				PostCommitDiagnostic: outcome.PostCommitDiagnostic,
			}
			if completionErr != nil {
				return decision, completionErr
			}
			completed = outcome.CurrentNodeCompletionResult
			completionDiagnostic = outcome.PostCommitDiagnostic
			decision.Accepted = &workflowruntime.AcceptedCompletion{
				Result: workflowruntime.CompletionResult{
					TransitionID:    workflow.TransitionID(transitionID),
					State:           "applied",
					Operation:       workflowRef.Operation(),
					CommittedResult: completed,
				},
				Diagnostic: completionDiagnostic,
			}
			starts = automaticQueuedStarts(completed.AutomaticIntents)
			c.mu.Lock()
			exact = c.operations[key]
			if exact == nil || exact.ref.OperationID != workflowRef.OperationID {
				c.mu.Unlock()
				panic("committed Current Node completion lost its admitted operation")
			}
			completion := completed
			exact.completion = &completion
			if !completed.PostCompletionEligible {
				settlement := workflowruntime.PostTurnSettlement{
					Kind:            workflowruntime.PostTurnSettlementSucceeded,
					DiagnosticOwner: workflowruntime.DiagnosticOwnerScriptRunner,
				}
				exact.postTurnSettlement = &settlement
			}
			c.mu.Unlock()
			if completed.PostCompletionEligible {
				sourceSessionID := completed.SourceSessionID
				c.mu.Lock()
				exact = c.operations[key]
				if exact == nil || exact.ref.OperationID != workflowRef.OperationID {
					c.mu.Unlock()
					panic("post-turn completion lost its admitted operation")
				}
				phase := currentNodePostTurnFinalization{
					sessionID:      sourceSessionID,
					classification: completed.SessionReuseClassification,
					reference:      workflowRef.CurrentNode,
					starts:         append([]currentNodeQueuedStart(nil), starts...),
				}
				exact.postTurnFinalization = &phase
				exact.heldStarts = append([]currentNodeQueuedStart(nil), starts...)
				c.mu.Unlock()
				return decision, nil
			}
			starts, pending = pendingCurrentNodeAssignmentStarts(starts)
			c.mu.Lock()
			exact = c.operations[key]
			if exact == nil || exact.ref.OperationID != workflowRef.OperationID {
				c.mu.Unlock()
				panic("completed Current Node lost its admitted operation")
			}
			exact.heldStarts = starts
			settledStarts = c.takeSettledOperationLocked(key, exact)
			c.mu.Unlock()
			return decision, nil
		}); err != nil {
			resolveErr := c.resolvePendingCurrentNodeAssignmentSteers(ctx, starts, pending)
			return errors.Join(err, resolveErr)
		}
		if completed.PostCompletionEligible {
			return nil
		}
		return c.resolvePendingCurrentNodeAssignmentSteers(ctx, starts, pending)
	})
	if err != nil {
		return workflowstore.CurrentNodeCompletionResult{}, workflow.CurrentNodeOperationRef{}, nil, err
	}
	c.releaseSettledCurrentNodeStarts(settledStarts)
	return completed, workflowRef.Operation(), completionDiagnostic, nil
}

func (c *CurrentNodeController) FinalizeCurrentNodePostTurn(
	ctx context.Context,
	operationRef workflow.CurrentNodeOperationRef,
	sessionID runtimeids.SessionID,
	runtimeState workflowruntime.PostCompletionRuntime,
) (workflowruntime.PostTurnSettlement, error) {
	if c == nil {
		return workflowruntime.PostTurnSettlement{}, errors.New("current node workflow controller is required")
	}
	if err := operationRef.Validate(); err != nil {
		return workflowruntime.PostTurnSettlement{}, err
	}
	if sessionID.IsZero() {
		return workflowruntime.PostTurnSettlement{}, errors.New("workflow post-turn Session is required")
	}
	key, err := operationRef.CurrentNode.Key()
	if err != nil {
		return workflowruntime.PostTurnSettlement{}, err
	}
	var phase currentNodePostTurnFinalization
	shutdown := false
	if err := c.runPostTurnTaskMutation(context.WithoutCancel(ctx), operationRef.CurrentNode.TaskID, func(context.Context) error {
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.closed || c.closing {
			shutdown = true
			return nil
		}
		operation := c.operations[key]
		if operation == nil || operation.ref.OperationID != operationRef.OperationID {
			return sessionruntime.ErrExecutionNoLongerLive
		}
		if operation.postTurnSettlement != nil {
			return nil
		}
		if operation.postTurnFinalization == nil {
			return errors.New("workflow operation has no pending post-turn finalization")
		}
		current := *operation.postTurnFinalization
		if current.sessionID == nil || *current.sessionID != sessionID {
			return fmt.Errorf("workflow post-turn Session %s does not match source Session", sessionID)
		}
		phase = current
		return nil
	}); err != nil {
		return workflowruntime.PostTurnSettlement{}, err
	}
	if shutdown {
		return workflowruntime.PostTurnSettlement{
			Kind:            workflowruntime.PostTurnSettlementShutdownDisposed,
			DiagnosticOwner: workflowruntime.DiagnosticOwnerControllerShutdown,
		}, nil
	}

	settlement := workflowruntime.PostTurnSettlement{
		Kind:            workflowruntime.PostTurnSettlementSucceeded,
		DiagnosticOwner: workflowruntime.DiagnosticOwnerAgentRunner,
	}
	if phase.classification == workflow.SessionReuseThresholdPossibleReuse &&
		runtimeState.CompactionMode != "none" &&
		runtimeState.PreCompactionTokens <= 0 {
		settlement.Kind = workflowruntime.PostTurnSettlementAborted
		settlement.Diagnostic = errors.New("workflow pre-compaction token limit must be positive")
	}
	shouldCompact := runtimeState.Compact != nil &&
		settlement.Kind == workflowruntime.PostTurnSettlementSucceeded &&
		runtimeState.CompactionMode != "none" &&
		((phase.classification == workflow.SessionReuseGuaranteedCACReuse) ||
			(phase.classification == workflow.SessionReuseThresholdPossibleReuse &&
				runtimeState.PreCompactionTokens > 0 &&
				runtimeState.UsedTokens >= runtimeState.PreCompactionTokens))
	if shouldCompact {
		result := runtimeState.Compact(ctx)
		settlement.CommitReceipt = result.CommitReceipt
		cause := context.Cause(ctx)
		diagnostic := errors.Join(cause, result.Diagnostic)
		switch {
		case result.CommitReceipt.Committed && diagnostic != nil:
			settlement.Kind = workflowruntime.PostTurnSettlementCompletedWithDiagnostic
			settlement.Diagnostic = diagnostic
		case result.CommitReceipt.Committed:
		case cause != nil || errors.Is(result.Diagnostic, context.Canceled):
			settlement.Kind = workflowruntime.PostTurnSettlementAborted
			settlement.Diagnostic = diagnostic
		case result.Diagnostic != nil:
			settlement.Kind = workflowruntime.PostTurnSettlementCompletedWithDiagnostic
			settlement.Diagnostic = result.Diagnostic
		}
	} else if cause := context.Cause(ctx); cause != nil &&
		settlement.Kind == workflowruntime.PostTurnSettlementSucceeded {
		settlement.Kind = workflowruntime.PostTurnSettlementAborted
		settlement.Diagnostic = cause
	}

	settleCtx := context.WithoutCancel(ctx)
	var starts []currentNodeQueuedStart
	if err := c.runPostTurnTaskMutation(settleCtx, operationRef.CurrentNode.TaskID, func(context.Context) error {
		c.mu.Lock()
		defer c.mu.Unlock()
		operation := c.operations[key]
		if c.closed || c.closing {
			settlement = workflowruntime.PostTurnSettlement{
				Kind:            workflowruntime.PostTurnSettlementShutdownDisposed,
				DiagnosticOwner: workflowruntime.DiagnosticOwnerControllerShutdown,
			}
			return nil
		}
		if operation == nil || operation.ref.OperationID != operationRef.OperationID {
			return sessionruntime.ErrExecutionNoLongerLive
		}
		if operation.postTurnSettlement != nil {
			settlement = *operation.postTurnSettlement
			return nil
		}
		if operation.postTurnFinalization == nil ||
			!operation.postTurnFinalization.reference.Equal(phase.reference) {
			return sessionruntime.ErrExecutionNoLongerLive
		}
		operation.heldStarts = append([]currentNodeQueuedStart(nil), phase.starts...)
		operation.postTurnFinalization = nil
		applied := settlement
		operation.postTurnSettlement = &applied
		starts = c.takeSettledOperationLocked(key, operation)
		return nil
	}); err != nil {
		return workflowruntime.PostTurnSettlement{}, err
	}
	c.releaseSettledCurrentNodeStarts(starts)
	return settlement, nil
}

var _ workflowruntime.PostTurnFinalizer = (*CurrentNodeController)(nil)

func (c *CurrentNodeController) RecordProtocolViolation(ctx context.Context, req workflowruntime.ViolationRequest) (workflowruntime.ViolationResult, error) {
	count, _, interrupted, err := c.authority.RecordWorkflowProtocolViolation(req.ScopeID, req.MaxCount)
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
	key, err := workflowRef.CurrentNode.Key()
	if err != nil {
		return err
	}
	reference := workflowRef.CurrentNode
	if err := c.runTaskMutation(ctx, reference.TaskID, func(ctx context.Context) error {
		return c.authority.WithExactExecutions([]sessionruntime.ExecutionHandle{handle}, func() error {
			c.mu.Lock()
			operation := c.operations[key]
			if operation == nil || operation.ref.OperationID != workflowRef.OperationID {
				c.mu.Unlock()
				return sessionruntime.ErrExecutionNoLongerLive
			}
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

func (c *CurrentNodeController) WorkflowExecutionRetired(outcome sessionruntime.WorkflowRetirementOutcome) {
	if c == nil {
		return
	}
	if err := outcome.Operation.Validate(); err != nil {
		panic(fmt.Sprintf("workflow execution retired invalid operation: %v", err))
	}
	if outcome.Kind != sessionruntime.ExecutionScopeAgent &&
		outcome.Kind != sessionruntime.ExecutionScopeScript {
		panic(fmt.Sprintf("workflow execution retired invalid kind %d", outcome.Kind))
	}
	if outcome.Disposition != sessionruntime.WorkflowRetirementCompleted &&
		outcome.Disposition != sessionruntime.WorkflowRetirementOutcomeLess {
		panic(fmt.Sprintf("workflow execution retired invalid disposition %d", outcome.Disposition))
	}
	key, err := outcome.Operation.CurrentNode.Key()
	if err != nil {
		panic(fmt.Sprintf("workflow execution retired invalid current node: %v", err))
	}
	c.mu.Lock()
	operation := c.operations[key]
	if operation == nil || operation.ref.OperationID != outcome.Operation.OperationID {
		c.mu.Unlock()
		return
	}
	c.releaseAgentCapacityLocked(operation.agentCapacityLease)
	operation.authorityRetired = true
	operation.retirementDisposition = outcome.Disposition
	interrupted := c.interrupts.operationFenced(outcome.Operation.OperationID)
	c.interrupts.finishOperation(outcome.Operation.OperationID)
	closed := c.closed || c.closing
	var starts []currentNodeQueuedStart
	outcomeLess := outcome.Disposition == sessionruntime.WorkflowRetirementOutcomeLess
	if interrupted || closed || outcomeLess {
		delete(c.operations, key)
	} else {
		starts = c.takeSettledOperationLocked(key, operation)
	}
	c.mu.Unlock()
	if !closed {
		c.wakeAdmissionWorker()
	}
	if interrupted || closed {
		return
	}
	if outcomeLess {
		if err := c.interruptOutcomeLessFinalization(outcome.Operation.CurrentNode); err != nil {
			c.mu.Lock()
			c.workerErr = errors.Join(c.workerErr, err)
			c.mu.Unlock()
		}
		return
	}
	c.releaseSettledCurrentNodeStarts(starts)
}

func (c *CurrentNodeController) takeSettledOperationLocked(
	key workflow.CurrentNodeReferenceKey,
	operation *currentNodeOperation,
) []currentNodeQueuedStart {
	if operation == nil ||
		!operation.authorityRetired ||
		operation.retirementDisposition != sessionruntime.WorkflowRetirementCompleted ||
		operation.completion == nil ||
		operation.postTurnSettlement == nil {
		return nil
	}
	starts := append([]currentNodeQueuedStart(nil), operation.heldStarts...)
	delete(c.operations, key)
	return starts
}

func (c *CurrentNodeController) releaseSettledCurrentNodeStarts(starts []currentNodeQueuedStart) {
	if c == nil {
		return
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), interruptCleanupTimeout)
	defer cancel()
	needsAssignmentSteer := false
	for _, start := range starts {
		if start.assignmentSteer == nil {
			needsAssignmentSteer = true
			break
		}
	}
	if needsAssignmentSteer {
		steered, steerErr := c.steerStartsAssignments(waitCtx, starts)
		if steerErr != nil {
			c.handleCurrentNodeStartFailures(automaticFailureRecoveryStarts(starts, steered), false, steerErr)
			return
		}
		starts = steered
	}
	outcome := waitCurrentNodeAssignmentSteers(waitCtx, starts)
	if outcome.err != nil {
		if len(outcome.pending) != 0 {
			c.continueCurrentNodeAssignmentStarts(starts, nil)
			return
		}
		c.handleCurrentNodeStartFailures(automaticFailureRecoveryStarts(starts, outcome.committed), false, outcome.err)
		return
	}
	c.enqueueStarts(starts)
	if len(starts) == 0 {
		c.wakeAdmissionWorker()
	}
}

func (c *CurrentNodeController) interruptOutcomeLessFinalization(reference workflow.CurrentNodeReference) error {
	ctx, cancel := context.WithTimeout(context.Background(), interruptCleanupTimeout)
	defer cancel()
	interrupted, err := runCurrentNodeTaskMutation(ctx, c, reference.TaskID, func(ctx context.Context) (bool, error) {
		c.mu.Lock()
		closed := c.closed || c.closing
		c.mu.Unlock()
		if closed {
			return false, nil
		}
		diagnostic := errors.New("exact workflow execution finalized without a durable completion or interruption outcome")
		err := c.store.InterruptAdmittedCurrentNode(
			ctx,
			reference,
			reasonCurrentNodeRuntimeFinalizedWithoutOutcome,
			workflow.NewCurrentNodeInterruptionDetail(
				string(reasonCurrentNodeRuntimeFinalizedWithoutOutcome),
				diagnostic,
			),
		)
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			return false, fmt.Errorf("interrupt outcome-less finalized Current Node %v: %w", reference, err)
		}
		return true, nil
	})
	if err != nil {
		return err
	}
	if interrupted {
		c.publishPendingInterruptedCurrentNode(ctx, reference, reasonCurrentNodeRuntimeFinalizedWithoutOutcome)
	}
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
		workflowExecutions []sessionruntime.WorkflowExecutionRef
	)
	c.mu.Lock()
	c.closed = true
	c.closing = false
	queuedPreparations = append([]*taskPreparationBatch(nil), c.preparationQueue...)
	c.preparationQueue = nil
	c.explicitQueue = nil
	c.explicitQueued = make(map[workflow.CurrentNodeReferenceKey]struct{})
	c.automaticQueue.clear()
	c.queued = make(map[workflow.CurrentNodeReferenceKey]struct{})
	workflowExecutions = make([]sessionruntime.WorkflowExecutionRef, 0, len(c.operations))
	for _, operation := range c.operations {
		c.releaseAgentCapacityLocked(operation.agentCapacityLease)
		c.interrupts.finishOperation(operation.ref.OperationID)
		if operation.workflow != nil {
			workflowExecutions = append(workflowExecutions, *operation.workflow)
		}
	}
	c.operations = make(map[workflow.CurrentNodeReferenceKey]*currentNodeOperation)
	for _, batch := range queuedPreparations {
		closeQueuedTaskPreparationBatch(batch, preparationShutdownCause())
	}
	c.mu.Unlock()
	c.lifecycleBarrier.Unlock()

	handles := make([]sessionruntime.ExecutionHandle, 0, len(workflowExecutions))
	for _, workflowRef := range workflowExecutions {
		handle, exists := c.authority.ExecutionByWorkflow(workflowRef)
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
	c.preparationWG.Wait()
	c.admissionWG.Wait()
	c.mu.Lock()
	workerErr := c.workerErr
	c.mu.Unlock()
	return errors.Join(errors.Join(stopErrs...), workerErr)
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

func (c *CurrentNodeController) runPostTurnTaskMutation(
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
var _ sessionruntime.WorkflowExecutionRetired = (*CurrentNodeController)(nil)
