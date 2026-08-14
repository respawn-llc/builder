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

// CurrentNodeRunner starts a lease that has already been admitted under the
// Task mutation owner. The runner owns slow launch preparation; the controller
// owns its gate and live-scope registration.
type CurrentNodeRunner interface {
	StartCurrentNode(
		context.Context,
		workflow.CurrentNodeReference,
		workflowruntime.TaskPromptDelivery,
		*CurrentNodeClassifiedAssignment,
		sessionruntime.WorkflowExecutionLease,
		workflowruntime.Controller,
	) error
}

type CurrentNodeAssignmentSteerer interface {
	SteerCurrentNodeAssignment(context.Context, workflow.CurrentNodeReference) (CurrentNodeAssignmentSteer, error)
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

type CurrentNodeSessionAssignmentSteer interface {
	CurrentNodeAssignmentSteer
	SessionID() runtimeids.SessionID
}

// CurrentNodeClassifiedAssignment is the immutable durable assignment proof
// consumed by admission and the real Current Node runner.
type CurrentNodeClassifiedAssignment struct {
	reference workflow.CurrentNodeReference
	prepared  CurrentNodeAssignmentSteer
}

func newCurrentNodeClassifiedAssignment(
	reference workflow.CurrentNodeReference,
	prepared CurrentNodeAssignmentSteer,
) *CurrentNodeClassifiedAssignment {
	if prepared == nil {
		panic("classified Current Node assignment requires prepared assignment")
	}
	return &CurrentNodeClassifiedAssignment{
		reference: reference,
		prepared:  prepared,
	}
}

func (a *CurrentNodeClassifiedAssignment) Reference() workflow.CurrentNodeReference {
	if a == nil {
		panic("classified Current Node assignment is required")
	}
	return a.reference
}

func (a *CurrentNodeClassifiedAssignment) PreparedAssignment() CurrentNodeAssignmentSteer {
	if a == nil {
		panic("classified Current Node assignment is required")
	}
	return a.prepared
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

type currentNodeStore interface {
	StartTask(context.Context, workflow.TaskID) (workflowstore.StartTaskResult, error)
	ListCurrentNodes(context.Context, workflow.TaskID) ([]workflow.CurrentNode, error)
	InterruptedExecutableCurrentNodes(context.Context, workflow.TaskID) ([]workflow.CurrentNode, error)
	PreflightTaskResume(context.Context, workflow.TaskID) ([]workflowstore.CurrentNodeResumeClassification, error)
	AdmitCurrentNode(context.Context, workflow.CurrentNodeReference) error
	ResumeCurrentNode(context.Context, workflow.CurrentNodeReference) (workflowstore.InterruptedCurrentNodeAttentionProjection, bool, error)
	PendingApproval(context.Context, workflow.ApprovalID) (workflow.PendingApproval, error)
	ApplyPendingApproval(context.Context, workflow.ApprovalID) (workflowstore.PendingApprovalApplyResult, error)
	ApplyManualMoveWithTargetAssignments(
		context.Context,
		workflowstore.ManualMovePreparation,
		*workflowstore.ExecutionTargetCandidate,
		workflowstore.ManualMoveTargetAssignmentPreparer,
	) (workflowstore.ManualMoveResult, error)
	InterruptAdmittedCurrentNode(context.Context, workflow.CurrentNodeReference, workflow.CurrentNodeInterruptionReason, workflow.CurrentNodeInterruptionDetail) error
	InterruptCurrentNode(context.Context, workflow.CurrentNodeReference, workflow.CurrentNodeInterruptionReason, workflow.CurrentNodeInterruptionDetail) error
	ReplaceUserInterruptionWithAssignmentFailure(context.Context, workflow.CurrentNodeReference, workflow.CurrentNodeInterruptionDetail) error
	RecoverExecutableCurrentNodes(context.Context, workflow.CurrentNodeInterruptionReason, workflow.CurrentNodeInterruptionDetail) ([]workflow.CurrentNodeReference, error)
	ResolveIdleExecutableCurrentNode(context.Context, workflowstore.IdleCurrentNodeSelector) (workflow.CurrentNode, error)
	CompleteCurrentNode(context.Context, workflowstore.CurrentNodeCompletionRequest) (workflowstore.CurrentNodeCompletionResult, error)
	RepairCurrentNodeSessionProvenanceForResume(context.Context, workflow.CurrentNode) error
	ValidateCurrentNodeSessionBinding(context.Context, runtimeids.SessionID, workflow.CurrentNodeReference) error
	TaskExecutionScope(context.Context, workflow.TaskID) (workflowstore.TaskExecutionScope, error)
}

// CurrentNodeController is the sole workflowruntime.Controller. Task mutation
// ownership, its mutex, and Authority operations define lifecycle ordering.
type CurrentNodeController struct {
	store     currentNodeStore
	runner    CurrentNodeRunner
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
	gates                 map[workflow.CurrentNodeReferenceKey]currentNodeAdmissionGate
	live                  map[runtimeids.ExecutionScopeID]currentNodeLiveScope
	liveByNode            map[workflow.CurrentNodeReferenceKey]runtimeids.ExecutionScopeID
	stopping              map[runtimeids.ExecutionScopeID]struct{}
	completed             map[runtimeids.ExecutionScopeID]struct{}
	operationallyBlocked  map[runtimeids.ExecutionScopeID]struct{}
	postTurnFinalization  map[runtimeids.ExecutionScopeID]currentNodePostTurnFinalization
	violations            map[runtimeids.ExecutionScopeID]int64
	heldStarts            map[runtimeids.ExecutionScopeID][]currentNodeQueuedStart
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
	workerDiagnostics     error
	lastAutomaticTask     *workflow.TaskID
	taskExecutionReads    atomic.Pointer[workflowTaskControllerReadSnapshot]
	lifecycleBarrier      sync.RWMutex
	closeMu               sync.Mutex
	closing               bool
}

func NewCurrentNodeController(
	store currentNodeStore,
	runner CurrentNodeRunner,
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
		gates:                 make(map[workflow.CurrentNodeReferenceKey]currentNodeAdmissionGate),
		live:                  make(map[runtimeids.ExecutionScopeID]currentNodeLiveScope),
		liveByNode:            make(map[workflow.CurrentNodeReferenceKey]runtimeids.ExecutionScopeID),
		stopping:              make(map[runtimeids.ExecutionScopeID]struct{}),
		completed:             make(map[runtimeids.ExecutionScopeID]struct{}),
		operationallyBlocked:  make(map[runtimeids.ExecutionScopeID]struct{}),
		postTurnFinalization:  make(map[runtimeids.ExecutionScopeID]currentNodePostTurnFinalization),
		violations:            make(map[runtimeids.ExecutionScopeID]int64),
		heldStarts:            make(map[runtimeids.ExecutionScopeID][]currentNodeQueuedStart),
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

func (c *CurrentNodeController) CompleteCurrentNode(ctx context.Context, req workflowruntime.CompletionRequest) (workflowruntime.CompletionResult, error) {
	completed, committed, err := c.completeLiveCurrentNode(ctx, req)
	if !committed &&
		errors.Is(err, sessionruntime.ErrExecutionNoLongerLive) &&
		req.SessionID != nil {
		completed, err = c.CompleteIdleCurrentNode(ctx, workflowstore.IdleCurrentNodeSelector{
			SessionID: req.SessionID,
		}, req.TransitionID, req.OutputValues, req.Commentary)
		committed = err == nil || completed.Committed()
		if err == nil {
			c.clearProtocolViolations(req.ScopeID)
		}
	}
	if committed {
		return workflowruntime.CompletionResult{
			TransitionID: workflow.TransitionID(req.TransitionID),
			State:        workflowruntime.CompletionStateApplied,
		}, err
	}
	if err != nil {
		return workflowruntime.CompletionResult{}, err
	}
	return workflowruntime.CompletionResult{}, errors.New("current node completion returned without a committed mutation")
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
	completed, _, err := c.completeLiveCurrentNode(ctx, workflowruntime.CompletionRequest{
		ScopeID:      handle.Scope().ID(),
		TransitionID: transitionID,
		OutputValues: outputValues,
		Commentary:   commentary,
	})
	return completed, err
}

func (c *CurrentNodeController) completeLiveCurrentNode(
	ctx context.Context,
	req workflowruntime.CompletionRequest,
) (workflowstore.CurrentNodeCompletionResult, bool, error) {
	var completed workflowstore.CurrentNodeCompletionResult
	var committed bool
	var starts []currentNodeQueuedStart
	var prepared *currentNodeAssignmentBatch
	taskID, err := c.taskIDForScope(req.ScopeID)
	if err != nil {
		return completed, false, err
	}
	err = c.runTaskMutation(ctx, taskID, func(ctx context.Context) error {
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
			committed = true
			intents, intentErr := currentNodeAutomaticIntents(completed.AutomaticIntents)
			if intentErr != nil {
				return intentErr
			}
			starts = automaticQueuedStarts(intents)
			c.mu.Lock()
			c.completed[req.ScopeID] = struct{}{}
			c.mu.Unlock()
			if completed.PostCompletionEligible {
				analysis := workflow.SessionReuseAnalysisInput{}
				if completed.SessionReuse != nil {
					analysis = *completed.SessionReuse
					analysis.RetainedAssociations = c.loadSessionReuseAssociations(ctx, analysis)
				}
				classification := workflow.ClassifyWorkflowSessionReuse(analysis)
				sourceSessionID := req.SessionID
				if sourceSessionID == nil && analysis.CompletedCurrentNode.SessionID != nil {
					value := *analysis.CompletedCurrentNode.SessionID
					sourceSessionID = &value
				}
				c.mu.Lock()
				c.postTurnFinalization[req.ScopeID] = currentNodePostTurnFinalization{
					sessionID:      sourceSessionID,
					classification: classification,
					reference:      exact.reference,
					starts:         append([]currentNodeQueuedStart(nil), starts...),
				}
				c.heldStarts[req.ScopeID] = append([]currentNodeQueuedStart(nil), starts...)
				c.mu.Unlock()
				return nil
			}
			prepared = c.prepareCurrentNodeAssignments(ctx, starts, true, &req.ScopeID)
			return nil
		}); err != nil {
			return err
		}
		if completed.PostCompletionEligible {
			return nil
		}
		_, assignmentErr := c.classifyPreparedAutomaticStarts(prepared)
		return assignmentErr
	})
	if err != nil {
		return completed, committed, err
	}
	return completed, committed, nil
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
	var phase currentNodePostTurnFinalization
	var phaseExists bool
	c.mu.Lock()
	current, exists := c.postTurnFinalization[scopeID]
	if exists {
		if current.sessionID == nil || *current.sessionID != sessionID {
			c.mu.Unlock()
			return fmt.Errorf("workflow post-turn Session %s does not match source Session", sessionID)
		}
		phase = current
		phaseExists = true
	}
	c.mu.Unlock()

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

	return c.runTaskMutation(ctx, phase.reference.TaskID, func(context.Context) error {
		c.mu.Lock()
		defer c.mu.Unlock()
		current, stillFinalizing := c.postTurnFinalization[scopeID]
		if !stillFinalizing || !current.reference.Equal(phase.reference) {
			return sessionruntime.ErrExecutionNoLongerLive
		}
		if _, live := c.live[scopeID]; !live {
			return sessionruntime.ErrExecutionNoLongerLive
		}
		if _, stopping := c.stopping[scopeID]; stopping {
			return sessionruntime.ErrExecutionNoLongerLive
		}
		c.heldStarts[scopeID] = append([]currentNodeQueuedStart(nil), phase.starts...)
		delete(c.postTurnFinalization, scopeID)
		return nil
	})
}

var _ workflowruntime.PostTurnFinalizer = (*CurrentNodeController)(nil)

func (c *CurrentNodeController) FinalizeCurrentNodeOperationalCompletionBlock(
	ctx context.Context,
	scopeID runtimeids.ExecutionScopeID,
) error {
	if c == nil {
		return errors.New("current node workflow controller is required")
	}
	if scopeID.IsZero() {
		return errors.New("workflow operational completion block scope is required")
	}
	taskID, err := c.taskIDForScope(scopeID)
	if err != nil {
		return err
	}
	return c.runTaskMutation(ctx, taskID, func(context.Context) error {
		c.mu.Lock()
		defer c.mu.Unlock()
		if _, live := c.live[scopeID]; !live {
			return sessionruntime.ErrExecutionNoLongerLive
		}
		if _, stopping := c.stopping[scopeID]; stopping {
			return sessionruntime.ErrExecutionNoLongerLive
		}
		c.operationallyBlocked[scopeID] = struct{}{}
		return nil
	})
}

var _ workflowruntime.OperationalCompletionBlockFinalizer = (*CurrentNodeController)(nil)

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
	source, err := c.store.ResolveIdleExecutableCurrentNode(ctx, selector)
	if err != nil {
		return workflowstore.CurrentNodeCompletionResult{}, err
	}
	taskID := source.Reference.TaskID
	return runCurrentNodeTaskMutation(ctx, c, taskID, func(ctx context.Context) (workflowstore.CurrentNodeCompletionResult, error) {
		source, err := c.resolveQuiescentIdleCurrentNode(ctx, selector)
		if err != nil {
			return workflowstore.CurrentNodeCompletionResult{}, err
		}
		if source.Reference.TaskID != taskID {
			return workflowstore.CurrentNodeCompletionResult{}, errors.New("idle current node changed Task during completion")
		}
		completed, completionErr := c.store.CompleteCurrentNode(ctx, workflowstore.CurrentNodeCompletionRequest{
			Source:       source.Reference,
			TransitionID: transitionID,
			OutputValues: outputValues,
			Commentary:   commentary,
		})
		if completionErr != nil {
			return workflowstore.CurrentNodeCompletionResult{}, completionErr
		}
		intents, intentErr := currentNodeAutomaticIntents(completed.AutomaticIntents)
		if intentErr != nil {
			return completed, intentErr
		}
		_, startErr := c.classifyAutomaticStarts(ctx, automaticQueuedStarts(intents), nil)
		return completed, startErr
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
	source, err := c.store.ResolveIdleExecutableCurrentNode(ctx, workflowstore.IdleCurrentNodeSelector{SessionID: req.SessionID})
	if err != nil {
		return workflowruntime.ViolationResult{}, err
	}
	taskID := source.Reference.TaskID
	result, err := runCurrentNodeTaskMutation(ctx, c, taskID, func(ctx context.Context) (workflowruntime.ViolationResult, error) {
		source, err := c.resolveQuiescentIdleCurrentNode(ctx, workflowstore.IdleCurrentNodeSelector{
			SessionID: req.SessionID,
		})
		if err != nil {
			return workflowruntime.ViolationResult{}, err
		}
		if source.Reference.TaskID != taskID {
			return workflowruntime.ViolationResult{}, errors.New("idle current node changed Task during protocol violation handling")
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
		interruptErr := c.store.InterruptCurrentNode(
			ctx,
			source.Reference,
			reasonProtocolViolationCap,
			workflow.NewCurrentNodeInterruptionDetail(string(reasonProtocolViolationCap), workflowProtocolViolationCause(req)),
		)
		committed, diagnostic := classifyCurrentNodeInterruption(interruptErr)
		if !committed {
			return workflowruntime.ViolationResult{}, diagnostic
		}
		reference := source.Reference
		interruptedReference = &reference
		c.clearProtocolViolations(req.ScopeID)
		return result, diagnostic
	})
	if err != nil && interruptedReference == nil {
		return workflowruntime.ViolationResult{}, err
	}
	if interruptedReference != nil {
		c.publishPendingInterruptedCurrentNode(ctx, *interruptedReference, reasonProtocolViolationCap)
	}
	return result, err
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
		source, resolveErr := c.store.ResolveIdleExecutableCurrentNode(ctx, workflowstore.IdleCurrentNodeSelector{SessionID: req.SessionID})
		if resolveErr != nil {
			return resolveErr
		}
		taskID := source.Reference.TaskID
		_, err = runCurrentNodeTaskMutation(ctx, c, taskID, func(ctx context.Context) (workflow.CurrentNode, error) {
			source, err := c.resolveQuiescentIdleCurrentNode(ctx, workflowstore.IdleCurrentNodeSelector{
				SessionID: req.SessionID,
			})
			if err != nil {
				return source, err
			}
			if source.Reference.TaskID != taskID {
				return workflow.CurrentNode{}, errors.New("idle current node changed Task during protocol violation reset")
			}
			c.clearProtocolViolations(req.ScopeID)
			return source, nil
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
	detail := workflow.NewCurrentNodeInterruptionDetail(string(reason), cause)
	var lease sessionruntime.WorkflowExecutionLease
	var interrupted bool
	taskID, err := c.taskIDForScope(scopeID)
	if err != nil {
		return err
	}
	if err := c.runTaskMutation(ctx, taskID, func(ctx context.Context) error {
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
		committed, diagnostic := classifyCurrentNodeInterruption(
			c.store.InterruptAdmittedCurrentNode(ctx, lease.Workflow().CurrentNode, reason, detail),
		)
		interrupted = committed
		return diagnostic
	}); err != nil {
		if !interrupted {
			return err
		}
		c.publishPendingInterruptedCurrentNode(ctx, lease.Workflow().CurrentNode, reason)
		if handle, live := c.authority.ExecutionByScope(scopeID); live {
			handle.RequestStop()
		}
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
	_, operationallyBlocked := c.operationallyBlocked[scope.ID()]
	delete(c.operationallyBlocked, scope.ID())
	delete(c.postTurnFinalization, scope.ID())
	starts := append([]currentNodeQueuedStart(nil), c.heldStarts[scope.ID()]...)
	delete(c.heldStarts, scope.ID())
	if gate, gated := c.gates[key]; gated && gate.lease.ScopeID() == scope.ID() {
		c.releaseAgentCapacityLocked(gate.agentCapacityLease)
		delete(c.gates, key)
	}
	interrupted := c.interrupts.scopeFenced(scope.ID())
	c.interrupts.finishScope(scope.ID())
	closed := c.closed || c.closing
	c.mu.Unlock()
	if !closed {
		c.wakeAdmissionWorker()
	}
	if !isLive || !live.lease.Workflow().CurrentNode.Equal(ref.CurrentNode) || interrupted || operationallyBlocked || closed {
		if !closed {
			c.wakeAdmissionWorker()
		}
		return
	}
	if !completed {
		if err := c.interruptOutcomeLessFinalization(live.reference); err != nil {
			c.mu.Lock()
			if committed, _ := classifyCurrentNodeInterruption(err); committed {
				c.workerDiagnostics = errors.Join(c.workerDiagnostics, err)
			} else {
				c.workerErr = errors.Join(c.workerErr, err)
			}
			c.mu.Unlock()
		}
		return
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), interruptCleanupTimeout)
	defer cancel()
	classified := make([]currentNodeQueuedStart, 0, len(starts))
	unclassified := make([]currentNodeQueuedStart, 0, len(starts))
	for _, start := range starts {
		if start.assignment != nil || start.assignmentWait != nil {
			classified = append(classified, start)
			continue
		}
		unclassified = append(unclassified, start)
	}
	if len(unclassified) != 0 {
		_, diagnostic := c.classifyAutomaticStarts(waitCtx, unclassified, nil)
		if diagnostic != nil {
			c.mu.Lock()
			c.workerDiagnostics = errors.Join(c.workerDiagnostics, diagnostic)
			c.mu.Unlock()
		}
	}
	c.enqueueStarts(classified)
	if len(classified) == 0 {
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
		cause := errors.New("exact workflow execution finalized without a durable completion or interruption outcome")
		committed, diagnostic := classifyCurrentNodeInterruption(c.store.InterruptAdmittedCurrentNode(
			ctx,
			reference,
			reasonCurrentNodeRuntimeFinalizedWithoutOutcome,
			workflow.NewCurrentNodeInterruptionDetail(
				string(reasonCurrentNodeRuntimeFinalizedWithoutOutcome),
				cause,
			),
		))
		if !committed && errors.Is(diagnostic, sql.ErrNoRows) {
			return false, nil
		}
		if !committed {
			return false, fmt.Errorf("interrupt outcome-less finalized Current Node %v: %w", reference, diagnostic)
		}
		return true, diagnostic
	})
	if interrupted {
		c.publishPendingInterruptedCurrentNode(ctx, reference, reasonCurrentNodeRuntimeFinalizedWithoutOutcome)
	}
	return err
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
		gates              []currentNodeAdmissionGate
		liveScopes         []runtimeids.ExecutionScopeID
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
	c.heldStarts = make(map[runtimeids.ExecutionScopeID][]currentNodeQueuedStart)
	c.postTurnFinalization = make(map[runtimeids.ExecutionScopeID]currentNodePostTurnFinalization)
	gates = make([]currentNodeAdmissionGate, 0, len(c.gates))
	for _, gate := range c.gates {
		gates = append(gates, gate)
	}
	liveScopes = make([]runtimeids.ExecutionScopeID, 0, len(c.live))
	for scopeID := range c.live {
		liveScopes = append(liveScopes, scopeID)
	}
	for _, batch := range queuedPreparations {
		closeQueuedTaskPreparationBatch(batch, preparationShutdownCause())
	}
	c.mu.Unlock()
	c.lifecycleBarrier.Unlock()

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
	c.preparationWG.Wait()
	c.admissionWG.Wait()
	c.mu.Lock()
	workerErr := c.workerErr
	workerDiagnostics := c.workerDiagnostics
	c.mu.Unlock()
	return errors.Join(errors.Join(stopErrs...), workerErr, workerDiagnostics)
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

func (c *CurrentNodeController) taskIDForScope(scopeID runtimeids.ExecutionScopeID) (workflow.TaskID, error) {
	if c == nil || scopeID.IsZero() {
		return "", errors.New("workflow exact execution scope id is required")
	}
	c.mu.Lock()
	live, exists := c.live[scopeID]
	c.mu.Unlock()
	if !exists {
		return "", sessionruntime.ErrExecutionNoLongerLive
	}
	return live.reference.TaskID, nil
}

var _ workflowruntime.Controller = (*CurrentNodeController)(nil)
var _ sessionruntime.ExecutionFinalized = (*CurrentNodeController)(nil)
