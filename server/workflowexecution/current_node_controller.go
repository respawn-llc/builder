package workflowexecution

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
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
	ref                  sessionruntime.WorkflowOperationRef
	workflow             *sessionruntime.WorkflowExecutionRef
	policy               currentNodeAdmissionPolicy
	agentCapacityLease   *currentNodeAgentCapacityLease
	completion           *workflowstore.CurrentNodeCompletionResult
	postTurnFinalization *currentNodePostTurnFinalization
	heldStarts           []currentNodeQueuedStart
}

// CurrentNodeController is the sole workflowruntime.Controller. Its mutex,
// mutation permit, and Authority operations define the ordering for all
// lifecycle, admission, interruption, and completion operations.
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
		ApplyManualMove(context.Context, workflowstore.ManualMovePreparation, *workflowstore.ExecutionTargetCandidate) (workflowstore.ManualMoveResult, error)
		InterruptAdmittedCurrentNode(context.Context, workflow.CurrentNodeReference, workflow.CurrentNodeInterruptionReason, workflow.CurrentNodeInterruptionDetail) error
		InterruptCurrentNode(context.Context, workflow.CurrentNodeReference, workflow.CurrentNodeInterruptionReason, workflow.CurrentNodeInterruptionDetail) error
		RecoverExecutableCurrentNodes(context.Context, workflow.CurrentNodeInterruptionReason, workflow.CurrentNodeInterruptionDetail) ([]workflow.CurrentNodeReference, error)
		ResolveIdleExecutableCurrentNode(context.Context, workflowstore.IdleCurrentNodeSelector) (workflow.CurrentNode, error)
		CompleteCurrentNode(context.Context, workflowstore.CurrentNodeCompletionRequest) (workflowstore.CurrentNodeCompletionResult, error)
		ValidateCurrentNodeSessionBinding(context.Context, runtimeids.SessionID, workflow.CurrentNodeReference) error
		TaskExecutionScope(context.Context, workflow.TaskID) (workflowstore.TaskExecutionScope, error)
	}
	runner    CurrentNodePublicationRunner
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
		ApplyManualMove(context.Context, workflowstore.ManualMovePreparation, *workflowstore.ExecutionTargetCandidate) (workflowstore.ManualMoveResult, error)
		InterruptAdmittedCurrentNode(context.Context, workflow.CurrentNodeReference, workflow.CurrentNodeInterruptionReason, workflow.CurrentNodeInterruptionDetail) error
		InterruptCurrentNode(context.Context, workflow.CurrentNodeReference, workflow.CurrentNodeInterruptionReason, workflow.CurrentNodeInterruptionDetail) error
		RecoverExecutableCurrentNodes(context.Context, workflow.CurrentNodeInterruptionReason, workflow.CurrentNodeInterruptionDetail) ([]workflow.CurrentNodeReference, error)
		ResolveIdleExecutableCurrentNode(context.Context, workflowstore.IdleCurrentNodeSelector) (workflow.CurrentNode, error)
		CompleteCurrentNode(context.Context, workflowstore.CurrentNodeCompletionRequest) (workflowstore.CurrentNodeCompletionResult, error)
		ValidateCurrentNodeSessionBinding(context.Context, runtimeids.SessionID, workflow.CurrentNodeReference) error
		TaskExecutionScope(context.Context, workflow.TaskID) (workflowstore.TaskExecutionScope, error)
	},
	runner CurrentNodePublicationRunner,
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
) (workflowruntime.CompletionResult, error) {
	_, err := c.completeAgentCurrentNode(ctx, req)
	if err != nil {
		return workflowruntime.CompletionResult{}, err
	}
	return workflowruntime.CompletionResult{
		TransitionID: workflow.TransitionID(req.TransitionID),
		State:        "applied",
	}, nil
}

func (c *CurrentNodeController) CompleteScriptCurrentNode(
	ctx context.Context,
	req workflowruntime.ScriptCompletionRequest,
) (workflowruntime.CompletionResult, error) {
	if req.ScopeID.IsZero() {
		return workflowruntime.CompletionResult{}, errors.New("Script completion requires an Exact Execution Scope")
	}
	_, err := c.completeLiveCurrentNode(
		ctx,
		req.ScopeID,
		nil,
		req.TransitionID,
		req.OutputValues,
		req.Commentary,
		func(commit func() error) error {
			return c.authority.CompleteFinalizingScript(req.ScopeID, commit)
		},
	)
	if err != nil {
		return workflowruntime.CompletionResult{}, err
	}
	return workflowruntime.CompletionResult{
		TransitionID: workflow.TransitionID(req.TransitionID),
		State:        "applied",
	}, nil
}

func (c *CurrentNodeController) CompleteSessionCurrentNode(
	ctx context.Context,
	sessionID runtimeids.SessionID,
	runID runtimeids.RunID,
	stepID runtimeids.StepID,
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
	if !live || handle.Scope().Kind() != sessionruntime.ExecutionScopeAgent {
		return workflowstore.CurrentNodeCompletionResult{}, sessionruntime.ErrExecutionNoLongerLive
	}
	return c.completeAgentCurrentNode(ctx, workflowruntime.AgentCompletionRequest{
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
) (workflowstore.CurrentNodeCompletionResult, error) {
	if c == nil {
		return workflowstore.CurrentNodeCompletionResult{}, errors.New("current node workflow controller is required")
	}
	if req.Provenance.ScopeID.IsZero() || req.Provenance.RunID.IsZero() ||
		req.Provenance.StepID.IsZero() || req.SessionID.IsZero() {
		return workflowstore.CurrentNodeCompletionResult{}, errors.New("Agent completion requires Scope, Session, Run, and Step provenance")
	}
	handle, live := c.authority.SessionExecution(req.SessionID)
	if !live || handle.Scope().Kind() != sessionruntime.ExecutionScopeAgent ||
		handle.Scope().ID() != req.Provenance.ScopeID {
		return workflowstore.CurrentNodeCompletionResult{}, sessionruntime.ErrExecutionNoLongerLive
	}
	return c.completeLiveCurrentNode(
		ctx,
		req.Provenance.ScopeID,
		&req.SessionID,
		req.TransitionID,
		req.OutputValues,
		req.Commentary,
		func(commit func() error) error {
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
	sessionID *runtimeids.SessionID,
	transitionID string,
	outputValues map[string]string,
	commentary string,
	validateAndCommit func(func() error) error,
) (workflowstore.CurrentNodeCompletionResult, error) {
	handle, live := c.authority.ExecutionByScope(scopeID)
	if !live {
		return workflowstore.CurrentNodeCompletionResult{}, sessionruntime.ErrExecutionNoLongerLive
	}
	workflowRef, workflowScoped := handle.Scope().Workflow()
	if !workflowScoped {
		return workflowstore.CurrentNodeCompletionResult{}, sessionruntime.ErrExecutionNoLongerLive
	}
	key, err := workflowRef.CurrentNode.Key()
	if err != nil {
		return workflowstore.CurrentNodeCompletionResult{}, err
	}
	var completed workflowstore.CurrentNodeCompletionResult
	var starts []currentNodeQueuedStart
	var pending []*pendingCurrentNodeAssignmentSteer
	err = c.permit.Run(ctx, func(ctx context.Context) error {
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
		if err := validateAndCommit(func() error {
			c.mu.Lock()
			exact := c.operations[key]
			if exact == nil || exact.ref.OperationID != workflowRef.OperationID || exact.completion != nil {
				c.mu.Unlock()
				return sessionruntime.ErrExecutionNoLongerLive
			}
			if err := c.ensureTaskAvailableLocked(workflowRef.CurrentNode.TaskID); err != nil {
				c.mu.Unlock()
				return err
			}
			c.mu.Unlock()
			var completionErr error
			completed, completionErr = c.store.CompleteCurrentNode(ctx, workflowstore.CurrentNodeCompletionRequest{
				Source:       workflowRef.CurrentNode,
				TransitionID: transitionID,
				OutputValues: outputValues,
				Commentary:   commentary,
			})
			if completionErr != nil {
				return completionErr
			}
			intents, intentErr := currentNodeAutomaticIntents(completed.AutomaticIntents)
			if intentErr != nil {
				return intentErr
			}
			starts = automaticQueuedStarts(intents)
			c.mu.Lock()
			exact = c.operations[key]
			if exact == nil || exact.ref.OperationID != workflowRef.OperationID {
				c.mu.Unlock()
				panic("committed Current Node completion lost its admitted operation")
			}
			completion := completed
			exact.completion = &completion
			c.mu.Unlock()
			if completed.PostCompletionEligible {
				analysis := workflow.SessionReuseAnalysisInput{}
				if completed.SessionReuse != nil {
					analysis = *completed.SessionReuse
					analysis.RetainedAssociations = c.loadSessionReuseAssociations(ctx, analysis)
				}
				classification := workflow.ClassifyWorkflowSessionReuse(analysis)
				sourceSessionID := sessionID
				if sourceSessionID == nil && analysis.CompletedCurrentNode.SessionID != nil {
					value := *analysis.CompletedCurrentNode.SessionID
					sourceSessionID = &value
				}
				c.mu.Lock()
				exact = c.operations[key]
				if exact == nil || exact.ref.OperationID != workflowRef.OperationID {
					c.mu.Unlock()
					panic("post-turn completion lost its admitted operation")
				}
				phase := currentNodePostTurnFinalization{
					sessionID:      sourceSessionID,
					classification: classification,
					reference:      workflowRef.CurrentNode,
					starts:         append([]currentNodeQueuedStart(nil), starts...),
				}
				exact.postTurnFinalization = &phase
				exact.heldStarts = append([]currentNodeQueuedStart(nil), starts...)
				c.mu.Unlock()
				return nil
			}
			starts, pending = pendingCurrentNodeAssignmentStarts(starts)
			c.mu.Lock()
			exact = c.operations[key]
			if exact == nil || exact.ref.OperationID != workflowRef.OperationID {
				c.mu.Unlock()
				panic("completed Current Node lost its admitted operation")
			}
			exact.heldStarts = starts
			c.mu.Unlock()
			return nil
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
		return workflowstore.CurrentNodeCompletionResult{}, err
	}
	return completed, nil
}

func (c *CurrentNodeController) loadSessionReuseAssociations(
	ctx context.Context,
	input workflow.SessionReuseAnalysisInput,
) []workflow.SessionReuseAssociation {
	loader, ok := c.store.(interface {
		LoadSessionReuseAssociations(context.Context, []workflow.CurrentNodeReference) ([]workflow.SessionReuseAssociation, error)
	})
	if !ok {
		return nil
	}
	references := workflow.SessionReuseAssociationReferences(input)
	associations, err := loader.LoadSessionReuseAssociations(ctx, references)
	if err != nil {
		slog.Warn(
			"load workflow session reuse associations failed",
			"task_id", input.CompletedCurrentNode.Reference.TaskID,
			"node_id", input.CompletedCurrentNode.Reference.NodeID,
			"error", err,
		)
		return nil
	}
	return associations
}

func (c *CurrentNodeController) FinalizeCurrentNodePostTurn(
	ctx context.Context,
	scopeID runtimeids.ExecutionScopeID,
	sessionID runtimeids.SessionID,
	runtimeState workflowruntime.PostCompletionRuntime,
) error {
	if c == nil {
		return errors.New("current node workflow controller is required")
	}
	if scopeID.IsZero() || sessionID.IsZero() {
		return errors.New("workflow post-turn finalization identities are required")
	}
	handle, live := c.authority.ExecutionByScope(scopeID)
	if !live {
		return nil
	}
	workflowRef, workflowScoped := handle.Scope().Workflow()
	if !workflowScoped {
		return sessionruntime.ErrExecutionNoLongerLive
	}
	key, err := workflowRef.CurrentNode.Key()
	if err != nil {
		return err
	}
	var phase currentNodePostTurnFinalization
	var phaseExists bool
	if err := c.permit.Run(ctx, func(context.Context) error {
		c.mu.Lock()
		operation := c.operations[key]
		if operation == nil || operation.ref.OperationID != workflowRef.OperationID ||
			operation.postTurnFinalization == nil {
			c.mu.Unlock()
			return nil
		}
		current := *operation.postTurnFinalization
		if current.sessionID == nil || *current.sessionID != sessionID {
			c.mu.Unlock()
			return fmt.Errorf("workflow post-turn Session %s does not match source Session", sessionID)
		}
		phase = current
		phaseExists = true
		c.mu.Unlock()
		return nil
	}); err != nil {
		return err
	}

	if !phaseExists {
		return nil
	}
	if phase.classification == workflow.SessionReuseThresholdPossibleReuse &&
		runtimeState.CompactionMode != "none" &&
		runtimeState.PreCompactionTokens <= 0 {
		return errors.New("workflow pre-compaction token limit must be positive")
	}
	shouldCompact := runtimeState.Compact != nil &&
		runtimeState.CompactionMode != "none" &&
		((phase.classification == workflow.SessionReuseGuaranteedCACReuse) ||
			(phase.classification == workflow.SessionReuseThresholdPossibleReuse &&
				runtimeState.PreCompactionTokens > 0 &&
				runtimeState.UsedTokens >= runtimeState.PreCompactionTokens))
	if shouldCompact {
		result := runtimeState.Compact(ctx)
		if cause := context.Cause(ctx); cause != nil {
			if result.Diagnostic != nil {
				return errors.Join(cause, result.Diagnostic)
			}
			return cause
		}
		if result.Diagnostic != nil {
			if errors.Is(result.Diagnostic, context.Canceled) ||
				context.Cause(ctx) != nil {
				return result.Diagnostic
			}
			slog.Warn(
				"workflow post-turn compaction diagnostic",
				"task_id", phase.reference.TaskID,
				"node_id", phase.reference.NodeID,
				"session_id", sessionID,
				"receipt_committed", result.CommitReceipt.Committed,
				"error", result.Diagnostic,
			)
		}
	}

	return c.permit.Run(ctx, func(context.Context) error {
		if _, live := c.authority.ExecutionByWorkflow(workflowRef); !live {
			return sessionruntime.ErrExecutionNoLongerLive
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		operation := c.operations[key]
		if operation == nil || operation.ref.OperationID != workflowRef.OperationID ||
			operation.postTurnFinalization == nil ||
			!operation.postTurnFinalization.reference.Equal(phase.reference) {
			return sessionruntime.ErrExecutionNoLongerLive
		}
		operation.heldStarts = append([]currentNodeQueuedStart(nil), phase.starts...)
		operation.postTurnFinalization = nil
		return nil
	})
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
	if err := c.permit.Run(ctx, func(ctx context.Context) error {
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
	operation := c.operations[key]
	if operation == nil || operation.ref.OperationID != ref.OperationID {
		c.mu.Unlock()
		return
	}
	c.releaseAgentCapacityLocked(operation.agentCapacityLease)
	delete(c.operations, key)
	completed := operation.completion != nil
	starts := append([]currentNodeQueuedStart(nil), operation.heldStarts...)
	interrupted := c.interrupts.operationFenced(ref.OperationID)
	c.interrupts.finishOperation(ref.OperationID)
	closed := c.closed || c.closing
	c.mu.Unlock()
	if !closed {
		c.wakeAdmissionWorker()
	}
	if interrupted || closed {
		if !closed {
			c.wakeAdmissionWorker()
		}
		return
	}
	if !completed {
		if err := c.interruptOutcomeLessFinalization(ref.CurrentNode); err != nil {
			c.mu.Lock()
			c.workerErr = errors.Join(c.workerErr, err)
			c.mu.Unlock()
		}
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
			c.handleCurrentNodeStartFailures(steered, false, steerErr)
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
		c.handleCurrentNodeStartFailures(outcome.committed, false, outcome.err)
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
	interrupted, err := RunMutation(ctx, c.permit, func(ctx context.Context) (bool, error) {
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
	c.workerCancel()
	var (
		startedShutdown     bool
		queuedPreparations  []*taskPreparationBatch
		runningPreparations []*taskPreparationBatch
		workflowExecutions  []sessionruntime.WorkflowExecutionRef
	)
	if err := c.permit.Run(context.Background(), func(context.Context) error {
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.closed {
			return nil
		}
		startedShutdown = true
		c.closed = true
		queuedPreparations = append([]*taskPreparationBatch(nil), c.preparationQueue...)
		c.preparationQueue = nil
		runningPreparations = append([]*taskPreparationBatch(nil), c.preparationRunning...)
		c.explicitQueue = nil
		c.explicitQueued = make(map[workflow.CurrentNodeReferenceKey]struct{})
		c.automaticQueue.clear()
		c.queued = make(map[workflow.CurrentNodeReferenceKey]struct{})
		workflowExecutions = make([]sessionruntime.WorkflowExecutionRef, 0, len(c.operations))
		for _, operation := range c.operations {
			operation.heldStarts = nil
			operation.postTurnFinalization = nil
			if operation.workflow != nil {
				workflowExecutions = append(workflowExecutions, *operation.workflow)
			}
		}
		for _, batch := range queuedPreparations {
			closeQueuedTaskPreparationBatch(batch, preparationShutdownCause())
		}
		for _, batch := range runningPreparations {
			batch.cancel(preparationShutdownCause())
		}
		return nil
	}); err != nil {
		return err
	}
	if !startedShutdown {
		return nil
	}

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
var _ sessionruntime.ExecutionFinalized = (*CurrentNodeController)(nil)
