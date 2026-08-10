package workflowexecution

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
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

const (
	reasonProtocolViolationCap                      workflow.CurrentNodeInterruptionReason = "workflow_protocol_violation_cap"
	reasonCurrentNodeRuntimeFinalizedWithoutOutcome workflow.CurrentNodeInterruptionReason = "workflow_runtime_finalized_without_outcome"
)

// CurrentNodeRunner starts a lease that has already been admitted under the
// controller mutation permit. The runner owns slow launch preparation; the
// controller owns its gate and live-scope registration.
type CurrentNodeRunner interface {
	StartCurrentNode(
		context.Context,
		workflow.CurrentNodeReference,
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
	AgentConcurrency      int
	Attention             CurrentNodeAttentionLifecycle
	AssignmentSteerer     CurrentNodeAssignmentSteerer
	LifecycleAvailability *LifecycleFatalAvailability
}

type CurrentNodeAttentionLifecycle interface {
	PublishPendingInterruptedCurrentNode(context.Context, workflow.CurrentNodeReference)
	FinalizeTaskResolution(workflowstore.TaskAttentionResolution)
}

// CurrentNodeAutomaticIntent is volatile automatic work accepted into a
// process-local Run generation.
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
// Task mutation coordinator, and Authority operations define the ordering for all
// lifecycle, admission, interruption, and completion operations.
type CurrentNodeController struct {
	store interface {
		PrepareTaskStart(context.Context, workflow.TaskID) (workflowstore.PreparedCurrentNodeMutation, error)
		PrepareTaskResume(context.Context, workflow.TaskID) (workflowstore.PreparedCurrentNodeMutation, error)
		InterruptedExecutableCurrentNodes(context.Context, workflow.TaskID) ([]workflow.CurrentNode, error)
		AdmitCurrentNode(context.Context, workflow.CurrentNodeReference) error
		PendingApproval(context.Context, workflow.ApprovalID) (workflow.PendingApproval, error)
		PreparePendingApprovalApply(context.Context, workflow.ApprovalID) (workflowstore.PreparedCurrentNodeMutation, error)
		PrepareManualMoveApply(context.Context, workflowstore.ManualMovePreparation, *workflowstore.ExecutionTargetCandidate) (workflowstore.PreparedCurrentNodeMutation, error)
		InterruptAdmittedCurrentNode(context.Context, workflow.CurrentNodeReference, workflow.CurrentNodeInterruptionReason, workflow.CurrentNodeInterruptionDetail) error
		InterruptCurrentNode(context.Context, workflow.CurrentNodeReference, workflow.CurrentNodeInterruptionReason, workflow.CurrentNodeInterruptionDetail) error
		InterruptCurrentNodeSchedulingSet(context.Context, workflow.TaskID, []workflowstore.CurrentNodeSchedulingTarget, workflow.CurrentNodeInterruptionReason, workflow.CurrentNodeInterruptionDetail) (workflowstore.CurrentNodeSchedulingInterruptionResult, error)
		RecoverExecutableCurrentNodes(context.Context, workflow.CurrentNodeInterruptionReason, workflow.CurrentNodeInterruptionDetail) ([]workflow.CurrentNodeReference, error)
		ResolveIdleExecutableCurrentNode(context.Context, workflowstore.IdleCurrentNodeSelector) (workflow.CurrentNode, error)
		PrepareCurrentNodeCompletion(context.Context, workflowstore.CurrentNodeCompletionRequest) (workflowstore.PreparedCurrentNodeCompletion, error)
		PublishCurrentNodeCompletion(context.Context, workflow.TaskID, workflowstore.CurrentNodeCompletionResult) error
		ValidateCurrentNodeSessionBinding(context.Context, runtimeids.SessionID, workflow.CurrentNodeReference) error
		TaskExecutionScope(context.Context, workflow.TaskID) (workflowstore.TaskExecutionScope, error)
	}
	runner                CurrentNodeRunner
	steerer               CurrentNodeAssignmentSteerer
	authority             *sessionruntime.Authority
	taskMutations         *TaskMutationCoordinator
	attention             CurrentNodeAttentionLifecycle
	lifecycleAvailability *LifecycleFatalAvailability

	agentConcurrency int
	workerContext    context.Context
	workerCancel     context.CancelFunc
	workerWake       chan struct{}
	workerWG         sync.WaitGroup

	mu                  sync.Mutex
	closed              bool
	nextRunSequence     uint64
	runs                map[currentNodeRunID]*currentNodeRun
	currentRuns         map[workflow.CurrentNodeReferenceKey]currentNodeRunID
	exactRuns           map[runtimeids.ExecutionScopeID]currentNodeRunID
	violations          map[runtimeids.ExecutionScopeID]int64
	explicitQueue       []currentNodeRunID
	automaticQueue      currentNodeAutomaticQueue
	agentCapacityActive int
	lastAutomaticTask   *workflow.TaskID
}

func NewCurrentNodeController(
	store interface {
		PrepareTaskStart(context.Context, workflow.TaskID) (workflowstore.PreparedCurrentNodeMutation, error)
		PrepareTaskResume(context.Context, workflow.TaskID) (workflowstore.PreparedCurrentNodeMutation, error)
		InterruptedExecutableCurrentNodes(context.Context, workflow.TaskID) ([]workflow.CurrentNode, error)
		AdmitCurrentNode(context.Context, workflow.CurrentNodeReference) error
		PendingApproval(context.Context, workflow.ApprovalID) (workflow.PendingApproval, error)
		PreparePendingApprovalApply(context.Context, workflow.ApprovalID) (workflowstore.PreparedCurrentNodeMutation, error)
		PrepareManualMoveApply(context.Context, workflowstore.ManualMovePreparation, *workflowstore.ExecutionTargetCandidate) (workflowstore.PreparedCurrentNodeMutation, error)
		InterruptAdmittedCurrentNode(context.Context, workflow.CurrentNodeReference, workflow.CurrentNodeInterruptionReason, workflow.CurrentNodeInterruptionDetail) error
		InterruptCurrentNode(context.Context, workflow.CurrentNodeReference, workflow.CurrentNodeInterruptionReason, workflow.CurrentNodeInterruptionDetail) error
		InterruptCurrentNodeSchedulingSet(context.Context, workflow.TaskID, []workflowstore.CurrentNodeSchedulingTarget, workflow.CurrentNodeInterruptionReason, workflow.CurrentNodeInterruptionDetail) (workflowstore.CurrentNodeSchedulingInterruptionResult, error)
		RecoverExecutableCurrentNodes(context.Context, workflow.CurrentNodeInterruptionReason, workflow.CurrentNodeInterruptionDetail) ([]workflow.CurrentNodeReference, error)
		ResolveIdleExecutableCurrentNode(context.Context, workflowstore.IdleCurrentNodeSelector) (workflow.CurrentNode, error)
		PrepareCurrentNodeCompletion(context.Context, workflowstore.CurrentNodeCompletionRequest) (workflowstore.PreparedCurrentNodeCompletion, error)
		PublishCurrentNodeCompletion(context.Context, workflow.TaskID, workflowstore.CurrentNodeCompletionResult) error
		ValidateCurrentNodeSessionBinding(context.Context, runtimeids.SessionID, workflow.CurrentNodeReference) error
		TaskExecutionScope(context.Context, workflow.TaskID) (workflowstore.TaskExecutionScope, error)
	},
	runner CurrentNodeRunner,
	authority *sessionruntime.Authority,
	taskMutations *TaskMutationCoordinator,
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
	if taskMutations == nil {
		return nil, errors.New("task mutation coordinator is required")
	}
	if cfg.AgentConcurrency <= 0 {
		return nil, errors.New("workflow agent concurrency must be positive")
	}
	if cfg.LifecycleAvailability == nil {
		return nil, errors.New("workflow lifecycle fatal availability is required")
	}
	workerContext, workerCancel := context.WithCancel(context.Background())
	controller := &CurrentNodeController{
		store:                 store,
		runner:                runner,
		steerer:               cfg.AssignmentSteerer,
		authority:             authority,
		taskMutations:         taskMutations,
		attention:             cfg.Attention,
		lifecycleAvailability: cfg.LifecycleAvailability,
		agentConcurrency:      cfg.AgentConcurrency,
		workerContext:         workerContext,
		workerCancel:          workerCancel,
		workerWake:            make(chan struct{}, 1),
		runs:                  make(map[currentNodeRunID]*currentNodeRun),
		currentRuns:           make(map[workflow.CurrentNodeReferenceKey]currentNodeRunID),
		exactRuns:             make(map[runtimeids.ExecutionScopeID]currentNodeRunID),
		violations:            make(map[runtimeids.ExecutionScopeID]int64),
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
	owned, ownedLive := c.runByScopeLocked(handle.Scope().ID())
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
	answer askquestion.AskQuestionResolution,
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
	var acceptance sessionruntime.PromptResponseAcceptance
	err := c.taskMutations.Run(ctx, taskID, func(ctx context.Context) error {
		resolution, err := c.authority.ResolvePendingWorkflowPrompt(taskID, askID)
		if err != nil {
			return err
		}
		c.mu.Lock()
		live, isLive := c.runByScopeLocked(resolution.ScopeID)
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
		acceptance, err = c.authority.AcceptPromptResolutionForScope(
			resolution.ScopeID,
			askID,
			answer,
			submitErr,
		)
		return err
	})
	if err != nil {
		return nil, err
	}
	return acceptance, nil
}

func (c *CurrentNodeController) completeLiveCurrentNode(ctx context.Context, req workflowruntime.CompletionRequest) (workflowstore.CurrentNodeCompletionResult, error) {
	var completed workflowstore.CurrentNodeCompletionResult
	c.mu.Lock()
	initial, exists := c.runByScopeLocked(req.ScopeID)
	c.mu.Unlock()
	if !exists {
		return workflowstore.CurrentNodeCompletionResult{}, sessionruntime.ErrExecutionNoLongerLive
	}
	err := c.taskMutations.Run(ctx, initial.reference.TaskID, func(ctx context.Context) error {
		c.mu.Lock()
		live, exists := c.runByScopeLocked(req.ScopeID)
		if !exists {
			c.mu.Unlock()
			return sessionruntime.ErrExecutionNoLongerLive
		}
		if live.stopping() {
			c.mu.Unlock()
			if live.interruptFence != nil {
				return ErrTaskExecutionNotQuiescent
			}
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
			exact, stillLive := c.runByScopeLocked(req.ScopeID)
			if !stillLive || !exact.reference.Equal(live.reference) {
				c.mu.Unlock()
				return sessionruntime.ErrExecutionNoLongerLive
			}
			if exact.stopping() {
				c.mu.Unlock()
				if exact.interruptFence != nil {
					return ErrTaskExecutionNotQuiescent
				}
				return sessionruntime.ErrExecutionNoLongerLive
			}
			if err := c.ensureTaskAvailableLocked(exact.reference.TaskID); err != nil {
				c.mu.Unlock()
				return err
			}
			c.mu.Unlock()
			prepared, completionErr := c.store.PrepareCurrentNodeCompletion(ctx, workflowstore.CurrentNodeCompletionRequest{
				Source:       exact.reference,
				TransitionID: req.TransitionID,
				OutputValues: req.OutputValues,
				Commentary:   req.Commentary,
			})
			if completionErr != nil {
				return completionErr
			}
			completed = prepared.Result()
			intents, intentErr := currentNodeAutomaticIntents(completed.AutomaticIntents)
			if intentErr != nil {
				return errors.Join(intentErr, prepared.Rollback())
			}
			starts := automaticQueuedStarts(intents)
			agentCompletion := completed.PostCompletionEligible ||
				req.SessionID != nil ||
				exact.policy == currentNodeAdmissionAutomaticAgent
			var postTurn *currentNodeRunPostTurn
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
				postTurn = &currentNodeRunPostTurn{
					sessionID:      sourceSessionID,
					classification: classification,
				}
			}
			c.mu.Lock()
			current, stillLive := c.runByScopeLocked(req.ScopeID)
			if !stillLive || current.id != exact.id || current.stopping() {
				c.mu.Unlock()
				return errors.Join(sessionruntime.ErrExecutionNoLongerLive, prepared.Rollback())
			}
			successorRuns, stageErr := c.prepareSuccessorRunsLocked(starts, exact.id)
			if stageErr != nil {
				c.mu.Unlock()
				return errors.Join(stageErr, prepared.Rollback())
			}
			successorPhase := currentNodeRunHeld
			if agentCompletion && completed.PostCompletionEligible {
				exact.completion = currentNodeRunCompletionAgentPostTurnPending
			} else if agentCompletion {
				exact.completion = currentNodeRunCompletionAgentPostTurnSucceeded
			} else {
				exact.completion = currentNodeRunCompletionScriptSucceeded
				successorPhase = currentNodeRunQueued
			}
			sourceRetained := completed.PendingApproval != nil
			if err := c.commitSuccessorRunsLocked(
				prepared,
				exact.id,
				successorRuns,
				sourceRetained,
				successorPhase,
			); err != nil {
				exact.completion = currentNodeRunCompletionNone
				c.mu.Unlock()
				return err
			}
			exact.successors = append([]currentNodeRunID(nil), successorRuns...)
			exact.completionSourceRetained = sourceRetained
			exact.postTurn = postTurn
			c.mu.Unlock()
			if err := c.store.PublishCurrentNodeCompletion(ctx, exact.reference.TaskID, completed); err != nil {
				slog.Warn(
					"publish workflow Current-Node completion event failed",
					"task_id", exact.reference.TaskID,
					"node_id", exact.reference.NodeID,
					"error", err,
				)
			}
			if successorPhase == currentNodeRunQueued {
				c.wakeAdmissionWorker()
			}
			return nil
		}); err != nil {
			return err
		}
		return nil
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
	var phase currentNodePostTurnSnapshot
	var phaseExists bool
	c.mu.Lock()
	initial, exists := c.runByScopeLocked(scopeID)
	c.mu.Unlock()
	if !exists || initial.postTurn == nil {
		return nil
	}
	if err := c.taskMutations.Run(ctx, initial.reference.TaskID, func(context.Context) error {
		c.mu.Lock()
		current, exists := c.runByScopeLocked(scopeID)
		if !exists || current.postTurn == nil {
			c.mu.Unlock()
			return nil
		}
		if current.postTurn.sessionID == nil || *current.postTurn.sessionID != sessionID {
			c.mu.Unlock()
			return fmt.Errorf("workflow post-turn Session %s does not match source Session", sessionID)
		}
		phase = currentNodePostTurnSnapshot{
			sessionID:      current.postTurn.sessionID,
			classification: current.postTurn.classification,
			reference:      current.reference,
		}
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

	return c.taskMutations.Run(ctx, phase.reference.TaskID, func(context.Context) error {
		c.mu.Lock()
		defer c.mu.Unlock()
		current, stillFinalizing := c.runByScopeLocked(scopeID)
		if !stillFinalizing || current.postTurn == nil || !current.reference.Equal(phase.reference) {
			return sessionruntime.ErrExecutionNoLongerLive
		}
		if current.stopping() {
			return sessionruntime.ErrExecutionNoLongerLive
		}
		current.completion = currentNodeRunCompletionAgentPostTurnSucceeded
		current.postTurn = nil
		return nil
	})
}

var _ workflowruntime.PostTurnFinalizer = (*CurrentNodeController)(nil)

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
	return RunTaskMutation(ctx, c.taskMutations, source.Reference.TaskID, func(ctx context.Context) (workflowstore.CurrentNodeCompletionResult, error) {
		source, err := c.resolveQuiescentIdleCurrentNode(ctx, selector)
		if err != nil {
			return workflowstore.CurrentNodeCompletionResult{}, err
		}
		prepared, completionErr := c.store.PrepareCurrentNodeCompletion(ctx, workflowstore.CurrentNodeCompletionRequest{
			Source:       source.Reference,
			TransitionID: transitionID,
			OutputValues: outputValues,
			Commentary:   commentary,
		})
		if completionErr != nil {
			return workflowstore.CurrentNodeCompletionResult{}, completionErr
		}
		completed := prepared.Result()
		intents, intentErr := currentNodeAutomaticIntents(completed.AutomaticIntents)
		if intentErr != nil {
			return workflowstore.CurrentNodeCompletionResult{}, errors.Join(intentErr, prepared.Rollback())
		}
		starts := automaticQueuedStarts(intents)
		starts, pending := pendingCurrentNodeAssignmentStarts(starts)
		c.mu.Lock()
		if err := c.ensureTaskAvailableLocked(source.Reference.TaskID); err != nil {
			c.mu.Unlock()
			return workflowstore.CurrentNodeCompletionResult{}, errors.Join(err, prepared.Rollback())
		}
		runIDs, startErr := c.stageRunsLocked(starts)
		if startErr != nil {
			c.mu.Unlock()
			return workflowstore.CurrentNodeCompletionResult{}, errors.Join(startErr, prepared.Rollback())
		}
		if err := c.commitStagedRunsLocked(prepared, runIDs); err != nil {
			c.mu.Unlock()
			return workflowstore.CurrentNodeCompletionResult{}, err
		}
		c.mu.Unlock()
		if err := c.store.PublishCurrentNodeCompletion(ctx, source.Reference.TaskID, completed); err != nil {
			slog.Warn(
				"publish forced workflow Current-Node completion event failed",
				"task_id", source.Reference.TaskID,
				"node_id", source.Reference.NodeID,
				"error", err,
			)
		}
		if err := c.resolvePreparedCurrentNodeStarts(ctx, starts, pending, runIDs, true); err != nil {
			return completed, err
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
	source, err := c.store.ResolveIdleExecutableCurrentNode(ctx, workflowstore.IdleCurrentNodeSelector{SessionID: req.SessionID})
	if err != nil {
		return workflowruntime.ViolationResult{}, err
	}
	result, err := RunTaskMutation(ctx, c.taskMutations, source.Reference.TaskID, func(ctx context.Context) (workflowruntime.ViolationResult, error) {
		source, err := c.resolveQuiescentIdleCurrentNode(ctx, workflowstore.IdleCurrentNodeSelector{SessionID: req.SessionID})
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
			workflow.NewCurrentNodeInterruptionDetail(string(reasonProtocolViolationCap), workflowProtocolViolationCause(req)),
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
		source, resolveErr := c.store.ResolveIdleExecutableCurrentNode(ctx, workflowstore.IdleCurrentNodeSelector{SessionID: req.SessionID})
		if resolveErr != nil {
			return resolveErr
		}
		_, err = RunTaskMutation(ctx, c.taskMutations, source.Reference.TaskID, func(ctx context.Context) (workflow.CurrentNode, error) {
			source, err := c.resolveQuiescentIdleCurrentNode(ctx, workflowstore.IdleCurrentNodeSelector{SessionID: req.SessionID})
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
	run, exists := c.runByScopeLocked(req.ScopeID)
	completed := exists && run.completion.committed()
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
	c.mu.Lock()
	initial, exists := c.runByScopeLocked(scopeID)
	c.mu.Unlock()
	if !exists {
		return sessionruntime.ErrExecutionNoLongerLive
	}
	if err := c.taskMutations.Run(ctx, initial.reference.TaskID, func(ctx context.Context) error {
		c.mu.Lock()
		live, exists := c.runByScopeLocked(scopeID)
		if !exists {
			c.mu.Unlock()
			return sessionruntime.ErrExecutionNoLongerLive
		}
		if live.stopping() {
			c.mu.Unlock()
			if live.interruptFence != nil {
				return ErrTaskExecutionNotQuiescent
			}
			return sessionruntime.ErrExecutionNoLongerLive
		}
		if c.closed {
			c.mu.Unlock()
			return errors.New("current node workflow controller is closed")
		}
		if err := c.ensureTaskAvailableLocked(live.reference.TaskID); err != nil {
			c.mu.Unlock()
			return err
		}
		if c.taskInterruptActiveLocked(live.reference.TaskID) {
			c.mu.Unlock()
			return ErrTaskExecutionNotQuiescent
		}
		if live.lease == nil {
			c.mu.Unlock()
			return sessionruntime.ErrExecutionNoLongerLive
		}
		lease = *live.lease
		if live.completion.committed() {
			sourceRetained := live.completionSourceRetained
			var targets []workflowstore.CurrentNodeSchedulingTarget
			if sourceRetained {
				targets = []workflowstore.CurrentNodeSchedulingTarget{{
					Reference: live.reference,
					Expected:  live.expectedScheduling,
				}}
			} else {
				targets = c.successorSchedulingTargetsLocked(live)
			}
			c.mu.Unlock()
			var result workflowstore.CurrentNodeSchedulingInterruptionResult
			var err error
			if len(targets) != 0 {
				result, err = c.store.InterruptCurrentNodeSchedulingSet(
					ctx,
					live.reference.TaskID,
					targets,
					reason,
					detail,
				)
			}
			if err != nil {
				c.mu.Lock()
				if current, stillLive := c.runByScopeLocked(scopeID); stillLive {
					c.recordInterruptionPersistenceFailureLocked(current, reason, err)
				}
				c.mu.Unlock()
				return err
			}
			if result.NotificationError != nil {
				slog.Warn(
					"publish workflow Current-Node interruption event failed",
					"task_id", live.reference.TaskID,
					"error", result.NotificationError,
				)
			}
			c.mu.Lock()
			current, stillLive := c.runByScopeLocked(scopeID)
			if !stillLive || current.id != live.id {
				c.mu.Unlock()
				return sessionruntime.ErrExecutionNoLongerLive
			}
			c.removeSuccessorsLocked(current)
			current.stop = currentNodeRunStopInterrupted
			c.mu.Unlock()
			for _, reference := range result.Interrupted {
				c.publishPendingInterruptedCurrentNode(ctx, reference, reason)
			}
			return nil
		}
		c.mu.Unlock()
		if err := c.store.InterruptAdmittedCurrentNode(ctx, lease.Workflow().CurrentNode, reason, detail); err != nil {
			c.mu.Lock()
			if current, stillLive := c.runByScopeLocked(scopeID); stillLive {
				c.recordInterruptionPersistenceFailureLocked(current, reason, err)
			}
			c.mu.Unlock()
			return err
		}
		c.mu.Lock()
		if current, stillLive := c.runByScopeLocked(scopeID); stillLive {
			current.stop = currentNodeRunStopInterrupted
		}
		c.mu.Unlock()
		return nil
	}); err != nil {
		return err
	}
	c.mu.Lock()
	completed := false
	if current, stillLive := c.runByScopeLocked(scopeID); stillLive {
		completed = current.completion.committed()
	}
	c.mu.Unlock()
	if !completed {
		c.publishPendingInterruptedCurrentNode(ctx, lease.Workflow().CurrentNode, reason)
	}
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
	c.mu.Lock()
	live, isLive := c.runByScopeLocked(scope.ID())
	if isLive {
		live.transition(currentNodeRunRetiring)
	}
	delete(c.violations, scope.ID())
	var completion currentNodeRunCompletion
	var successorRuns []currentNodeRunID
	stopping := false
	fatal := false
	if isLive {
		completion = live.completion
		successorRuns = append([]currentNodeRunID(nil), live.successors...)
		stopping = live.stopping()
		fatal = live.callbackErr != nil
	}
	closed := c.closed
	c.mu.Unlock()
	if fatal {
		return
	}
	if !closed {
		c.wakeAdmissionWorker()
	}
	if !isLive || live.lease == nil || !live.lease.Workflow().CurrentNode.Equal(ref.CurrentNode) || stopping || closed {
		if isLive {
			c.mu.Lock()
			c.removeRunLocked(live.id)
			c.mu.Unlock()
		}
		if !closed {
			c.wakeAdmissionWorker()
		}
		return
	}
	switch completion {
	case currentNodeRunCompletionNone:
		if err := c.interruptOutcomeLessFinalization(live.id); err != nil {
			c.mu.Lock()
			if current := c.runs[live.id]; current != nil {
				c.recordLifecycleFatalLocked(current, err)
			}
			c.mu.Unlock()
			return
		}
		c.mu.Lock()
		c.removeRunLocked(live.id)
		c.mu.Unlock()
		c.wakeAdmissionWorker()
		return
	case currentNodeRunCompletionScriptSucceeded:
		c.mu.Lock()
		c.removeRunLocked(live.id)
		c.mu.Unlock()
		c.wakeAdmissionWorker()
		return
	case currentNodeRunCompletionAgentPostTurnPending:
		err := c.interruptCommittedSuccessors(
			live.id,
			reasonCurrentNodeRuntimeFinalizedWithoutOutcome,
			errors.New("Agent workflow execution finalized before post-turn disposition"),
		)
		if err != nil {
			c.mu.Lock()
			if current := c.runs[live.id]; current != nil {
				c.recordLifecycleFatalLocked(current, err)
			}
			c.mu.Unlock()
			return
		}
		c.mu.Lock()
		c.removeRunLocked(live.id)
		c.mu.Unlock()
		c.wakeAdmissionWorker()
		return
	case currentNodeRunCompletionAgentPostTurnSucceeded:
	default:
		panic(fmt.Sprintf("unknown current node Run completion disposition %d", completion))
	}
	c.finalizeAgentSuccessors(live.id, successorRuns)
}

func (c *CurrentNodeController) finalizeAgentSuccessors(
	predecessorID currentNodeRunID,
	successorRuns []currentNodeRunID,
) {
	c.mu.Lock()
	predecessor := c.runs[predecessorID]
	if predecessor == nil {
		c.mu.Unlock()
		return
	}
	for _, id := range successorRuns {
		run := c.runs[id]
		if run == nil || run.predecessor == nil || *run.predecessor != predecessorID {
			panic(fmt.Sprintf("Agent predecessor Run %d lost successor Run %d", predecessorID.sequence, id.sequence))
		}
		if run.phase != currentNodeRunHeld {
			panic(fmt.Sprintf("Agent successor Run %d has phase %d, want held", id.sequence, run.phase))
		}
		c.queueRunLocked(id, run.assignmentSteer)
	}
	c.removeRunLocked(predecessorID)
	c.mu.Unlock()
	c.wakeAdmissionWorker()
}

func (c *CurrentNodeController) interruptCommittedSuccessors(
	predecessorID currentNodeRunID,
	reason workflow.CurrentNodeInterruptionReason,
	cause error,
) error {
	ctx, cancel := context.WithTimeout(context.Background(), interruptCleanupTimeout)
	defer cancel()
	c.mu.Lock()
	predecessor := c.runs[predecessorID]
	if predecessor == nil {
		c.mu.Unlock()
		return sessionruntime.ErrExecutionNoLongerLive
	}
	sourceRetained := predecessor.completionSourceRetained
	sourceReference := predecessor.reference
	taskID := predecessor.reference.TaskID
	targets := c.successorSchedulingTargetsLocked(predecessor)
	if sourceRetained {
		targets = []workflowstore.CurrentNodeSchedulingTarget{{
			Reference: sourceReference,
			Expected:  predecessor.expectedScheduling,
		}}
	}
	c.mu.Unlock()
	interrupted, err := RunTaskMutation(ctx, c.taskMutations, taskID, func(ctx context.Context) ([]workflow.CurrentNodeReference, error) {
		if len(targets) == 0 {
			return nil, nil
		}
		result, err := c.store.InterruptCurrentNodeSchedulingSet(
			ctx,
			taskID,
			targets,
			reason,
			workflow.NewCurrentNodeInterruptionDetail(string(reason), cause),
		)
		if result.NotificationError != nil {
			slog.Warn(
				"publish workflow Current-Node interruption event failed",
				"task_id", taskID,
				"error", result.NotificationError,
			)
		}
		return result.Interrupted, err
	})
	if err != nil {
		return err
	}
	for _, reference := range interrupted {
		c.publishPendingInterruptedCurrentNode(ctx, reference, reason)
	}
	c.mu.Lock()
	if current := c.runs[predecessorID]; current != nil {
		c.removeSuccessorsLocked(current)
		current.stop = currentNodeRunStopInterrupted
	}
	c.mu.Unlock()
	return nil
}

func (c *CurrentNodeController) interruptOutcomeLessFinalization(runID currentNodeRunID) error {
	ctx, cancel := context.WithTimeout(context.Background(), interruptCleanupTimeout)
	defer cancel()
	c.mu.Lock()
	run := c.runs[runID]
	if run == nil {
		c.mu.Unlock()
		return sessionruntime.ErrExecutionNoLongerLive
	}
	reference := run.reference
	c.mu.Unlock()
	interrupted, err := RunTaskMutation(ctx, c.taskMutations, reference.TaskID, func(ctx context.Context) (bool, error) {
		c.mu.Lock()
		closed := c.closed
		current := c.runs[runID]
		c.mu.Unlock()
		if closed {
			return false, nil
		}
		if current == nil || !current.reference.Equal(reference) {
			return false, sessionruntime.ErrExecutionNoLongerLive
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

func (c *CurrentNodeController) Snapshot() CurrentNodeExecutionSnapshot {
	if c == nil {
		return CurrentNodeExecutionSnapshot{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	snapshot := CurrentNodeExecutionSnapshot{
		AutomaticIntents: make([]CurrentNodeAutomaticIntent, 0, c.automaticQueue.len()),
		ExplicitStarts:   make([]CurrentNodeExplicitStart, 0, len(c.explicitQueue)),
	}
	for entry := c.automaticQueue.first; entry != nil; entry = entry.globalNext {
		run := c.runs[entry.runID]
		if run == nil {
			panic("automatic queue points to absent Run")
		}
		snapshot.AutomaticIntents = append(snapshot.AutomaticIntents, CurrentNodeAutomaticIntent{
			CurrentNode: run.reference,
			NodeKind:    run.policy.nodeKind(),
		})
	}
	for _, run := range c.runs {
		switch run.phase {
		case currentNodeRunLaunching:
			if run.lease != nil {
				snapshot.Gates = append(snapshot.Gates, CurrentNodeAdmissionGateSnapshot{
					CurrentNode: run.reference,
					ScopeID:     run.lease.ScopeID(),
					Automatic:   run.policy.isAutomatic(),
				})
			} else if run.policy.isAutomatic() {
				snapshot.AutomaticIntents = append(snapshot.AutomaticIntents, CurrentNodeAutomaticIntent{
					CurrentNode: run.reference,
					NodeKind:    run.policy.nodeKind(),
				})
			} else {
				snapshot.ExplicitStarts = append(snapshot.ExplicitStarts, CurrentNodeExplicitStart{CurrentNode: run.reference})
			}
		case currentNodeRunExact, currentNodeRunRetiring:
			if run.exactScopeID != nil {
				snapshot.LiveScopes = append(snapshot.LiveScopes, CurrentNodeLiveScopeSnapshot{
					CurrentNode: run.reference,
					ScopeID:     *run.exactScopeID,
					Automatic:   run.policy.isAutomatic(),
				})
			}
		}
		if run.phase == currentNodeRunHeld && run.predecessor != nil {
			predecessor := c.runs[*run.predecessor]
			if predecessor != nil && predecessor.exactScopeID != nil {
				snapshot.HeldIntents = append(snapshot.HeldIntents, CurrentNodeHeldIntentSnapshot{
					CurrentNode: run.reference,
					SourceScope: *predecessor.exactScopeID,
					Automatic:   run.policy.isAutomatic(),
				})
			}
		}
	}
	for _, id := range c.explicitQueue {
		run := c.runs[id]
		if run == nil {
			panic("explicit queue points to absent Run")
		}
		snapshot.ExplicitStarts = append(snapshot.ExplicitStarts, CurrentNodeExplicitStart{CurrentNode: run.reference})
	}
	snapshot.InterruptingTasks = c.interruptingTaskIDsLocked()
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
	c.automaticQueue.clear()
	launchLeases := make([]sessionruntime.WorkflowExecutionLease, 0)
	liveScopes := make([]runtimeids.ExecutionScopeID, 0, len(c.exactRuns))
	admissionDone := make([]<-chan struct{}, 0)
	for _, run := range c.runs {
		if run.launchCancel != nil {
			run.launchCancel()
		}
		if run.phase == currentNodeRunLaunching && run.lease != nil {
			launchLeases = append(launchLeases, *run.lease)
		}
		if run.exactScopeID != nil {
			liveScopes = append(liveScopes, *run.exactScopeID)
		}
		if run.admissionDone != nil {
			admissionDone = append(admissionDone, run.admissionDone)
		}
	}
	c.mu.Unlock()

	c.workerCancel()
	for _, lease := range launchLeases {
		lease.Cancel()
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
	for _, done := range admissionDone {
		<-done
	}
	c.mu.Lock()
	var callbackErrs []error
	for _, run := range c.runs {
		callbackErrs = append(callbackErrs, run.callbackErr)
	}
	c.mu.Unlock()
	return errors.Join(errors.Join(stopErrs...), errors.Join(callbackErrs...))
}

func (c *CurrentNodeController) liveLease(scopeID runtimeids.ExecutionScopeID) (sessionruntime.WorkflowExecutionLease, error) {
	if c == nil || scopeID.IsZero() {
		return sessionruntime.WorkflowExecutionLease{}, errors.New("workflow exact execution scope id is required")
	}
	c.mu.Lock()
	live, exists := c.runByScopeLocked(scopeID)
	c.mu.Unlock()
	if !exists || live.stopping() || live.lease == nil {
		return sessionruntime.WorkflowExecutionLease{}, sessionruntime.ErrExecutionNoLongerLive
	}
	return *live.lease, nil
}

var _ workflowruntime.Controller = (*CurrentNodeController)(nil)
var _ sessionruntime.ExecutionFinalized = (*CurrentNodeController)(nil)
