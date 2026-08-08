package workflowexecution

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
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

var errCurrentNodeControllerClosed = errors.New("current node workflow controller is closed")

// CurrentNodeRunner starts a lease that has already been admitted under the
// Task lifecycle writer. The runner owns slow launch preparation; the
// controller reacquires the writer before live-scope registration.
type CurrentNodeRunner interface {
	StartCurrentNode(
		context.Context,
		workflow.CurrentNodeReference,
		workflowruntime.TaskPromptDelivery,
		CurrentNodeAssignmentEnsure,
		sessionruntime.WorkflowExecutionLease,
		workflowruntime.Controller,
	) error
}

type CurrentNodeAssignmentEnsurer interface {
	EnsureCurrentNodeAssignment(
		context.Context,
		workflow.CurrentNodeReference,
		workflowruntime.TaskPromptDelivery,
	) (CurrentNodeAssignmentEnsure, error)
}

type CurrentNodeAssignmentEnsure interface {
	Wait(context.Context) (session.CommitReceipt, error)
}

type LifecyclePublication interface {
	Publish(context.Context, workflowstore.TaskLifecycleDelta) error
	PublishTaskStart(
		context.Context,
		workflow.TaskID,
		workflowstore.TaskStartPublicationStage,
	) (workflowstore.StartTaskResult, error)
	PublishCurrentNodeCompletion(
		context.Context,
		workflowstore.CurrentNodeCompletionRequest,
		workflowstore.CurrentNodeCompletionPublicationStage,
	) (workflowstore.CurrentNodeCompletionResult, workflowstore.LifecyclePublicationOutcome, error)
	PrepareCurrentNodeCompletion(
		context.Context,
		workflowstore.CurrentNodeCompletionRequest,
		workflowstore.CurrentNodeCompletionPublicationStage,
	) (workflowstore.PreparedCurrentNodeCompletionPublication, error)
	PreviewCurrentNodeCompletion(
		context.Context,
		workflowstore.CurrentNodeCompletionRequest,
	) (workflowstore.CurrentNodeCompletionResult, error)
	PublishPendingApproval(
		context.Context,
		workflow.ApprovalID,
		workflowstore.PendingApprovalPublicationStage,
	) (workflowstore.PendingApprovalApplyResult, error)
	PublishManualMove(
		context.Context,
		workflowstore.ManualMovePreparation,
		*workflowstore.ExecutionTargetCandidate,
		workflowstore.ManualMovePublicationStage,
	) (workflowstore.ManualMoveResult, error)
	PublishCurrentNodeAdmission(
		context.Context,
		workflow.CurrentNodeReference,
	) error
	PublishExactRegistration(
		context.Context,
		workflowstore.LifecycleExactExecution,
		workflowstore.LifecycleExactRegistrationActivation,
	) error
	PublishExactPromptPending(
		context.Context,
		runtimeids.ExecutionScopeID,
		workflowstore.LifecyclePendingPrompt,
	) error
	PublishExactPromptResolved(
		context.Context,
		runtimeids.ExecutionScopeID,
		string,
	) error
	PublishExactFinalizing(context.Context, runtimeids.ExecutionScopeID) error
	PublishCurrentNodeInterruption(
		context.Context,
		[]workflow.CurrentNodeReference,
		workflowstore.CurrentNodeInterruptionPredecessor,
		workflowstore.LifecycleFieldPresence,
		workflow.CurrentNodeInterruptionReason,
		workflow.CurrentNodeInterruptionDetail,
		[]workflowstore.LifecycleExactExecution,
	) (workflowstore.LifecyclePublicationOutcome, error)
	PublishResume(
		context.Context,
		workflowstore.QueuedTaskLifecycleDelta,
	) ([]workflowstore.InterruptedCurrentNodeAttentionProjection, error)
	PublishTaskDeletion(
		context.Context,
		workflow.TaskID,
	) (workflowstore.DeleteTaskResult, error)
	PublishWorkflowDeletion(
		context.Context,
		workflowstore.WorkflowDeleteRequest,
	) (workflowstore.WorkflowDeleteResult, error)
	PublishProjectDeletion(
		context.Context,
		workflowstore.ProjectDeleteRequest,
	) ([]serverapi.ProjectDeleteBlocker, error)
	Capture(context.Context) (workflowstore.LifecycleCapture, error)
	Close() error
}

type CurrentNodeControllerConfig struct {
	AgentConcurrency  int
	Attention         CurrentNodeAttentionLifecycle
	AssignmentEnsurer CurrentNodeAssignmentEnsurer
	Publication       LifecyclePublication
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

type currentNodePostTurnFinalization struct {
	sessionID      *runtimeids.SessionID
	classification workflow.SessionReuseClassification
	reference      workflow.CurrentNodeReference
	starts         []currentNodeQueuedStart
	pending        []*pendingCurrentNodeAssignmentEnsure
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

// CurrentNodeController is the sole workflowruntime.Controller. Its per-Task
// lifecycle writer, mutex, and Authority operations define execution ordering.
type CurrentNodeController struct {
	store interface {
		InterruptedExecutableCurrentNodes(context.Context, workflow.TaskID) ([]workflow.CurrentNode, error)
		PreflightTaskResume(context.Context, workflow.TaskID) ([]workflowstore.CurrentNodeResumeClassification, error)
		CurrentNodeKind(context.Context, workflow.CurrentNodeReference) (workflow.NodeKind, error)
		PendingApproval(context.Context, workflow.ApprovalID) (workflow.PendingApproval, error)
		RecoverExecutableCurrentNodes(context.Context, workflow.CurrentNodeInterruptionReason, workflow.CurrentNodeInterruptionDetail) ([]workflow.CurrentNodeReference, error)
		ResolveIdleExecutableCurrentNode(context.Context, workflowstore.IdleCurrentNodeSelector) (workflow.CurrentNode, error)
		ValidateCurrentNodeSessionBinding(context.Context, runtimeids.SessionID, workflow.CurrentNodeReference) error
		TaskIDForSession(context.Context, runtimeids.SessionID) (*workflow.TaskID, error)
		EnsureCurrentSessionStartContext(context.Context, runtimeids.SessionID) (workflowstore.CurrentNodeStartContext, error)
		TaskExecutionScope(context.Context, workflow.TaskID) (workflowstore.TaskExecutionScope, error)
	}
	runner      CurrentNodeRunner
	ensurer     CurrentNodeAssignmentEnsurer
	authority   *sessionruntime.Authority
	permit      *MutationPermit
	lifecycle   *TaskLifecycleCoordinator
	publication LifecyclePublication
	attention   CurrentNodeAttentionLifecycle

	agentConcurrency int
	workerContext    context.Context
	workerCancel     context.CancelFunc
	workerWake       chan struct{}
	workerWG         sync.WaitGroup
	admissionWG      sync.WaitGroup

	mu                    sync.Mutex
	closed                bool
	runs                  currentNodeRunRegistry
	gates                 map[workflow.CurrentNodeReferenceKey]struct{}
	exactScopes           map[runtimeids.ExecutionScopeID]workflow.CurrentNodeReferenceKey
	stopping              map[runtimeids.ExecutionScopeID]struct{}
	completed             map[runtimeids.ExecutionScopeID]struct{}
	postTurnFinalization  map[runtimeids.ExecutionScopeID]currentNodePostTurnFinalization
	violations            map[runtimeids.ExecutionScopeID]int64
	heldStarts            map[runtimeids.ExecutionScopeID][]workflow.CurrentNodeReferenceKey
	explicitQueue         []workflow.CurrentNodeReferenceKey
	explicitQueued        map[workflow.CurrentNodeReferenceKey]struct{}
	explicitReservations  map[workflow.CurrentNodeReferenceKey]struct{}
	automaticQueue        currentNodeAutomaticQueue
	queued                map[workflow.CurrentNodeReferenceKey]struct{}
	automaticReservations map[workflow.CurrentNodeReferenceKey]struct{}
	admissionWorkers      map[workflow.CurrentNodeReferenceKey]struct{}
	agentCapacityActive   int
	interrupts            currentNodeInterruptState
	workerErr             error
	lastAutomaticTask     *workflow.TaskID
}

func NewCurrentNodeController(
	store interface {
		InterruptedExecutableCurrentNodes(context.Context, workflow.TaskID) ([]workflow.CurrentNode, error)
		PreflightTaskResume(context.Context, workflow.TaskID) ([]workflowstore.CurrentNodeResumeClassification, error)
		CurrentNodeKind(context.Context, workflow.CurrentNodeReference) (workflow.NodeKind, error)
		PendingApproval(context.Context, workflow.ApprovalID) (workflow.PendingApproval, error)
		RecoverExecutableCurrentNodes(context.Context, workflow.CurrentNodeInterruptionReason, workflow.CurrentNodeInterruptionDetail) ([]workflow.CurrentNodeReference, error)
		ResolveIdleExecutableCurrentNode(context.Context, workflowstore.IdleCurrentNodeSelector) (workflow.CurrentNode, error)
		ValidateCurrentNodeSessionBinding(context.Context, runtimeids.SessionID, workflow.CurrentNodeReference) error
		TaskIDForSession(context.Context, runtimeids.SessionID) (*workflow.TaskID, error)
		EnsureCurrentSessionStartContext(context.Context, runtimeids.SessionID) (workflowstore.CurrentNodeStartContext, error)
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
	if cfg.AssignmentEnsurer == nil {
		return nil, errors.New("current node assignment ensurer is required")
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
	publication := cfg.Publication
	if publication == nil {
		if source, ok := store.(interface{ lifecyclePublication() LifecyclePublication }); ok {
			publication = source.lifecyclePublication()
		} else {
			workflowStore, ok := store.(*workflowstore.Store)
			if !ok {
				return nil, errors.New("lifecycle publication is required")
			}
			var err error
			publication, err = workflowstore.NewLifecyclePublication(workflowStore)
			if err != nil {
				return nil, err
			}
		}
	}
	workerContext, workerCancel := context.WithCancel(context.Background())
	controller := &CurrentNodeController{
		store:                 store,
		runner:                runner,
		ensurer:               cfg.AssignmentEnsurer,
		authority:             authority,
		permit:                permit,
		lifecycle:             NewTaskLifecycleCoordinator(),
		publication:           publication,
		attention:             cfg.Attention,
		agentConcurrency:      cfg.AgentConcurrency,
		workerContext:         workerContext,
		workerCancel:          workerCancel,
		workerWake:            make(chan struct{}, 1),
		runs:                  newCurrentNodeRunRegistry(),
		gates:                 make(map[workflow.CurrentNodeReferenceKey]struct{}),
		exactScopes:           make(map[runtimeids.ExecutionScopeID]workflow.CurrentNodeReferenceKey),
		stopping:              make(map[runtimeids.ExecutionScopeID]struct{}),
		completed:             make(map[runtimeids.ExecutionScopeID]struct{}),
		postTurnFinalization:  make(map[runtimeids.ExecutionScopeID]currentNodePostTurnFinalization),
		violations:            make(map[runtimeids.ExecutionScopeID]int64),
		heldStarts:            make(map[runtimeids.ExecutionScopeID][]workflow.CurrentNodeReferenceKey),
		explicitQueued:        make(map[workflow.CurrentNodeReferenceKey]struct{}),
		explicitReservations:  make(map[workflow.CurrentNodeReferenceKey]struct{}),
		queued:                make(map[workflow.CurrentNodeReferenceKey]struct{}),
		automaticReservations: make(map[workflow.CurrentNodeReferenceKey]struct{}),
		admissionWorkers:      make(map[workflow.CurrentNodeReferenceKey]struct{}),
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

func (c *CurrentNodeController) CaptureLifecycle(ctx context.Context) (workflowstore.LifecycleCapture, error) {
	if c == nil || c.publication == nil {
		return nil, errors.New("current node lifecycle publication is required")
	}
	return c.publication.Capture(ctx)
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
	owned, _, ownedLive := c.runByScopeLocked(handle.Scope().ID())
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

func (c *CurrentNodeController) InterruptSessionExecution(
	ctx context.Context,
	sessionID runtimeids.SessionID,
) (bool, error) {
	if c == nil {
		return false, errors.New("current node workflow controller is required")
	}
	if sessionID.IsZero() {
		return false, errors.New("session id is required")
	}
	handle, live := c.authority.SessionExecution(sessionID)
	if !live {
		return false, nil
	}
	scopeRef, workflowScoped := handle.Scope().Workflow()
	if !workflowScoped {
		return false, nil
	}
	err := c.Interrupt(ctx, InterruptSelector{
		TaskID:    scopeRef.CurrentNode.TaskID,
		SessionID: &sessionID,
	})
	if errors.Is(err, ErrNoInterruptibleExecution) {
		return true, nil
	}
	return true, err
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
	err := c.lifecycle.Run(ctx, taskID, func(ctx context.Context) error {
		resolution, err := c.authority.ResolvePendingWorkflowPrompt(taskID, askID)
		if err != nil {
			return err
		}
		c.mu.Lock()
		live, _, isLive := c.runByScopeLocked(resolution.ScopeID)
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
	var pending []*pendingCurrentNodeAssignmentEnsure
	c.mu.Lock()
	initial, _, exists := c.runByScopeLocked(req.ScopeID)
	if !exists {
		c.mu.Unlock()
		return workflowstore.CurrentNodeCompletionResult{}, sessionruntime.ErrExecutionNoLongerLive
	}
	taskID := initial.reference.TaskID
	c.mu.Unlock()
	err := c.lifecycle.Run(ctx, taskID, func(ctx context.Context) error {
		c.mu.Lock()
		live, _, exists := c.runByScopeLocked(req.ScopeID)
		if !exists || live != initial {
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
		if live.disposition != currentNodeRunDispositionRunning &&
			live.disposition != currentNodeRunDispositionQueued {
			c.mu.Unlock()
			return sessionruntime.ErrExecutionNoLongerLive
		}
		c.mu.Unlock()
		handle, ok := c.authority.ExecutionByScope(req.ScopeID)
		if !ok {
			return sessionruntime.ErrExecutionNoLongerLive
		}
		if err := c.authority.WithExactExecutions([]sessionruntime.ExecutionHandle{handle}, func() error {
			c.mu.Lock()
			exact, _, stillLive := c.runByScopeLocked(req.ScopeID)
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
			completionRequest := workflowstore.CurrentNodeCompletionRequest{
				Source:       exact.reference,
				TransitionID: req.TransitionID,
				OutputValues: req.OutputValues,
				Commentary:   req.Commentary,
			}
			stage := func(result workflowstore.CurrentNodeCompletionResult) (
				workflowstore.TaskLifecycleDelta,
				func(error),
				error,
			) {
				intents, intentErr := currentNodeAutomaticIntents(result.AutomaticIntents)
				if intentErr != nil {
					return workflowstore.TaskLifecycleDelta{}, nil, intentErr
				}
				starts, pending = pendingCurrentNodeAssignmentStarts(automaticQueuedStarts(intents))
				c.mu.Lock()
				if err := c.registerHeldRunsLocked(req.ScopeID, starts); err != nil {
					c.mu.Unlock()
					return workflowstore.TaskLifecycleDelta{}, nil, err
				}
				c.mu.Unlock()
				delta, deltaErr := currentNodeSuccessfulFinalizationLifecycleDelta(
					exact.reference,
					req.ScopeID,
					starts,
				)
				if deltaErr != nil {
					c.rollbackHeldRunCreations(req.ScopeID, deltaErr)
					return workflowstore.TaskLifecycleDelta{}, nil, deltaErr
				}
				return delta, func(cause error) {
					c.rollbackHeldRunCreations(req.ScopeID, cause)
				}, nil
			}
			if handle.Scope().Kind() == sessionruntime.ExecutionScopeAgent {
				preview, previewErr := c.publication.PreviewCurrentNodeCompletion(ctx, completionRequest)
				if previewErr != nil {
					return previewErr
				}
				c.mu.Lock()
				current, _, stillLive := c.runByScopeLocked(req.ScopeID)
				if !stillLive || current != exact {
					c.mu.Unlock()
					return sessionruntime.ErrExecutionNoLongerLive
				}
				if exact.pendingCompletionRequest != nil {
					c.mu.Unlock()
					return errors.New("current node completion is already pending finalization")
				}
				copiedRequest := completionRequest
				copiedRequest.OutputValues = workflow.CloneStringMap(completionRequest.OutputValues)
				exact.pendingCompletionRequest = &copiedRequest
				completed = preview
				c.mu.Unlock()
				return nil
			}
			prepared, completionErr := c.publication.PrepareCurrentNodeCompletion(
				ctx,
				completionRequest,
				stage,
			)
			if completionErr != nil {
				return completionErr
			}
			if err := c.beginCurrentNodeFinalizationPublication(exact, req.ScopeID); err != nil {
				return errors.Join(err, prepared.Rollback(err))
			}
			var publicationOutcome workflowstore.LifecyclePublicationOutcome
			completed, publicationOutcome, completionErr = prepared.Publish(ctx)
			c.finishCurrentNodeFinalizationPublication(exact)
			if publicationOutcome.Committed() {
				c.mu.Lock()
				c.completed[req.ScopeID] = struct{}{}
				c.mu.Unlock()
				if confirmErr := c.authority.ConfirmWorkflowDisposition(req.ScopeID); confirmErr != nil &&
					!errors.Is(confirmErr, sessionruntime.ErrExecutionNoLongerLive) {
					completionErr = errors.Join(completionErr, confirmErr)
				}
			}
			if completionErr != nil {
				return completionErr
			}
			if c.stageCurrentNodePostTurnFinalization(
				ctx,
				req.ScopeID,
				exact.reference,
				completed,
				req.SessionID,
				starts,
				pending,
			) {
				return nil
			}
			return nil
		}); err != nil {
			exactErr := err
			resolveErr := c.resolvePendingCurrentNodeAssignmentEnsures(ctx, starts, pending)
			return errors.Join(exactErr, resolveErr)
		}
		if completed.PostCompletionEligible {
			return nil
		}
		return c.resolvePendingCurrentNodeAssignmentEnsures(ctx, starts, pending)
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

func (c *CurrentNodeController) stageCurrentNodePostTurnFinalization(
	ctx context.Context,
	scopeID runtimeids.ExecutionScopeID,
	reference workflow.CurrentNodeReference,
	completed workflowstore.CurrentNodeCompletionResult,
	sourceSessionID *runtimeids.SessionID,
	starts []currentNodeQueuedStart,
	pending []*pendingCurrentNodeAssignmentEnsure,
) bool {
	if !completed.PostCompletionEligible {
		return false
	}
	analysis := workflow.SessionReuseAnalysisInput{}
	if completed.SessionReuse != nil {
		analysis = *completed.SessionReuse
		analysis.RetainedAssociations = c.loadSessionReuseAssociations(ctx, analysis)
	}
	if sourceSessionID == nil && analysis.CompletedCurrentNode.SessionID != nil {
		value := *analysis.CompletedCurrentNode.SessionID
		sourceSessionID = &value
	}
	c.mu.Lock()
	c.postTurnFinalization[scopeID] = currentNodePostTurnFinalization{
		sessionID:      sourceSessionID,
		classification: workflow.ClassifyWorkflowSessionReuse(analysis),
		reference:      reference,
		starts:         append([]currentNodeQueuedStart(nil), starts...),
		pending:        append([]*pendingCurrentNodeAssignmentEnsure(nil), pending...),
	}
	c.mu.Unlock()
	return true
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
	c.mu.Lock()
	initial, exists := c.postTurnFinalization[scopeID]
	c.mu.Unlock()
	if !exists {
		return nil
	}
	var phase currentNodePostTurnFinalization
	if err := c.lifecycle.Run(ctx, initial.reference.TaskID, func(context.Context) error {
		c.mu.Lock()
		current, exists := c.postTurnFinalization[scopeID]
		if !exists {
			c.mu.Unlock()
			return nil
		}
		if current.sessionID == nil || *current.sessionID != sessionID {
			c.mu.Unlock()
			return fmt.Errorf("workflow post-turn Session %s does not match source Session", sessionID)
		}
		run, _, live := c.runByScopeLocked(scopeID)
		if !live || !run.reference.Equal(current.reference) {
			c.mu.Unlock()
			return sessionruntime.ErrExecutionNoLongerLive
		}
		if _, stopping := c.stopping[scopeID]; stopping {
			c.mu.Unlock()
			return sessionruntime.ErrExecutionNoLongerLive
		}
		phase = current
		c.mu.Unlock()
		return nil
	}); err != nil {
		return err
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

	resolveErr := c.resolvePendingCurrentNodeAssignmentEnsures(ctx, phase.starts, phase.pending)
	return c.lifecycle.Run(ctx, phase.reference.TaskID, func(context.Context) error {
		c.mu.Lock()
		defer c.mu.Unlock()
		current, stillFinalizing := c.postTurnFinalization[scopeID]
		if !stillFinalizing || !current.reference.Equal(phase.reference) {
			return sessionruntime.ErrExecutionNoLongerLive
		}
		run, _, live := c.runByScopeLocked(scopeID)
		if !live || !run.reference.Equal(phase.reference) {
			return sessionruntime.ErrExecutionNoLongerLive
		}
		if _, stopping := c.stopping[scopeID]; stopping {
			return sessionruntime.ErrExecutionNoLongerLive
		}
		delete(c.postTurnFinalization, scopeID)
		return resolveErr
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
	initial, err := c.store.ResolveIdleExecutableCurrentNode(ctx, selector)
	if err != nil {
		return workflowstore.CurrentNodeCompletionResult{}, err
	}
	return runTaskLifecycle(ctx, c.lifecycle, initial.Reference.TaskID, func(ctx context.Context) (workflowstore.CurrentNodeCompletionResult, error) {
		source, err := c.resolveQuiescentIdleCurrentNode(ctx, selector)
		if err != nil {
			return workflowstore.CurrentNodeCompletionResult{}, err
		}
		var starts []currentNodeQueuedStart
		var pending []*pendingCurrentNodeAssignmentEnsure
		var staged []stagedCurrentNodeRun
		completed, publicationOutcome, completionErr := c.publication.PublishCurrentNodeCompletion(ctx, workflowstore.CurrentNodeCompletionRequest{
			Source:       source.Reference,
			TransitionID: transitionID,
			OutputValues: outputValues,
			Commentary:   commentary,
		}, func(result workflowstore.CurrentNodeCompletionResult) (
			workflowstore.TaskLifecycleDelta,
			func(error),
			error,
		) {
			intents, err := currentNodeAutomaticIntents(result.AutomaticIntents)
			if err != nil {
				return workflowstore.TaskLifecycleDelta{}, nil, err
			}
			starts, pending = pendingCurrentNodeAssignmentStarts(automaticQueuedStarts(intents))
			staged, err = c.stageCurrentNodeRunCreations(starts)
			if err != nil {
				return workflowstore.TaskLifecycleDelta{}, nil, err
			}
			delta, err := currentNodeCompletionLifecycleDelta(
				source.Reference,
				workflowstore.LifecycleFieldAbsent,
				starts,
			)
			if err != nil {
				c.rollbackStagedRunCreations(staged, err)
				return workflowstore.TaskLifecycleDelta{}, nil, err
			}
			return delta, func(cause error) {
				c.rollbackStagedRunCreations(staged, cause)
			}, nil
		})
		if !publicationOutcome.Committed() && completionErr != nil {
			return workflowstore.CurrentNodeCompletionResult{}, completionErr
		}
		resolveErr := c.resolvePendingCurrentNodeAssignmentEnsures(ctx, starts, pending)
		outcome := waitCurrentNodeAssignmentEnsures(ctx, starts)
		if len(outcome.pending) > 0 {
			c.continuePublishedCurrentNodeAssignmentStarts(
				stagedCurrentNodeRunsForStarts(staged, outcome.pending),
				outcome.pending,
			)
		}
		if err := errors.Join(
			completionErr,
			resolveErr,
			c.completePublishedCurrentNodeAssignmentStarts(ctx, staged, outcome),
		); err != nil {
			return workflowstore.CurrentNodeCompletionResult{}, err
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
	selector := workflowstore.IdleCurrentNodeSelector{SessionID: req.SessionID}
	initial, err := c.store.ResolveIdleExecutableCurrentNode(ctx, selector)
	if err != nil {
		return workflowruntime.ViolationResult{}, err
	}
	result, err := runTaskLifecycle(ctx, c.lifecycle, initial.Reference.TaskID, func(ctx context.Context) (workflowruntime.ViolationResult, error) {
		source, err := c.resolveQuiescentIdleCurrentNode(ctx, selector)
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
		publicationOutcome, err := c.publication.PublishCurrentNodeInterruption(
			ctx,
			[]workflow.CurrentNodeReference{source.Reference},
			workflowstore.CurrentNodeInterruptionFromReadyOrAdmitted,
			workflowstore.LifecycleFieldAbsent,
			reasonProtocolViolationCap,
			workflow.NewCurrentNodeInterruptionDetail(string(reasonProtocolViolationCap), workflowProtocolViolationCause(req)),
			nil,
		)
		if !publicationOutcome.Committed() && err != nil {
			return workflowruntime.ViolationResult{}, err
		}
		reference := source.Reference
		interruptedReference = &reference
		c.clearProtocolViolations(req.ScopeID)
		return result, err
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
		selector := workflowstore.IdleCurrentNodeSelector{SessionID: req.SessionID}
		initial, resolveErr := c.store.ResolveIdleExecutableCurrentNode(ctx, selector)
		if resolveErr != nil {
			return resolveErr
		}
		_, err = runTaskLifecycle(ctx, c.lifecycle, initial.Reference.TaskID, func(ctx context.Context) (workflow.CurrentNode, error) {
			source, err := c.resolveQuiescentIdleCurrentNode(ctx, selector)
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
	if !completed {
		if run, _, live := c.runByScopeLocked(req.ScopeID); live {
			completed = run.pendingCompletionRequest != nil
		}
	}
	c.mu.Unlock()
	return workflowruntime.CompletionObservationResult{Completed: completed}, nil
}

func (c *CurrentNodeController) FinalizeCurrentNodeResult(
	ctx context.Context,
	scopeID runtimeids.ExecutionScopeID,
	runErr error,
) error {
	if c == nil {
		return errors.New("current node workflow controller is required")
	}
	if scopeID.IsZero() {
		return errors.New("workflow exact execution scope id is required")
	}
	c.mu.Lock()
	initial, _, live := c.runByScopeLocked(scopeID)
	if !live {
		c.mu.Unlock()
		return sessionruntime.ErrExecutionNoLongerLive
	}
	taskID := initial.reference.TaskID
	c.mu.Unlock()
	return c.lifecycle.Run(ctx, taskID, func(ctx context.Context) error {
		c.mu.Lock()
		current, _, stillLive := c.runByScopeLocked(scopeID)
		if !stillLive || current != initial {
			c.mu.Unlock()
			return sessionruntime.ErrExecutionNoLongerLive
		}
		pendingRequest := current.pendingCompletionRequest
		interrupted := c.interrupts.scopeFenced(scopeID)
		c.mu.Unlock()

		if pendingRequest == nil {
			if interrupted {
				return nil
			}
			if runErr != nil {
				return c.publishCurrentNodeFinalizationFailure(
					ctx,
					current,
					scopeID,
					runErr,
				)
			}
			return c.publishCurrentNodeFinalizationFailure(
				ctx,
				current,
				scopeID,
				errors.New("Agent execution returned without an accepted Current Node completion"),
			)
		}
		if interrupted {
			c.mu.Lock()
			if current.pendingCompletionRequest == pendingRequest {
				current.pendingCompletionRequest = nil
			}
			c.mu.Unlock()
			return ErrTaskExecutionNotQuiescent
		}
		if cause := context.Cause(ctx); cause != nil {
			c.mu.Lock()
			if current.pendingCompletionRequest == pendingRequest {
				current.pendingCompletionRequest = nil
			}
			c.mu.Unlock()
			return cause
		}
		if runErr != nil {
			c.mu.Lock()
			if current.pendingCompletionRequest == pendingRequest {
				current.pendingCompletionRequest = nil
			}
			c.mu.Unlock()
			return errors.Join(
				runErr,
				c.publishCurrentNodeFinalizationFailure(ctx, current, scopeID, runErr),
			)
		}

		var starts []currentNodeQueuedStart
		var pending []*pendingCurrentNodeAssignmentEnsure
		prepared, err := c.publication.PrepareCurrentNodeCompletion(
			ctx,
			*pendingRequest,
			func(result workflowstore.CurrentNodeCompletionResult) (
				workflowstore.TaskLifecycleDelta,
				func(error),
				error,
			) {
				intents, intentErr := currentNodeAutomaticIntents(result.AutomaticIntents)
				if intentErr != nil {
					return workflowstore.TaskLifecycleDelta{}, nil, intentErr
				}
				starts, pending = pendingCurrentNodeAssignmentStarts(automaticQueuedStarts(intents))
				c.mu.Lock()
				if err := c.registerHeldRunsLocked(scopeID, starts); err != nil {
					c.mu.Unlock()
					return workflowstore.TaskLifecycleDelta{}, nil, err
				}
				c.mu.Unlock()
				delta, deltaErr := currentNodeSuccessfulFinalizationLifecycleDelta(
					current.reference,
					scopeID,
					starts,
				)
				if deltaErr != nil {
					c.rollbackHeldRunCreations(scopeID, deltaErr)
					return workflowstore.TaskLifecycleDelta{}, nil, deltaErr
				}
				return delta, func(cause error) {
					c.rollbackHeldRunCreations(scopeID, cause)
				}, nil
			},
		)
		if err == nil {
			err = c.beginCurrentNodeFinalizationPublication(current, scopeID)
		}
		var completed workflowstore.CurrentNodeCompletionResult
		var publicationOutcome workflowstore.LifecyclePublicationOutcome
		if err == nil {
			completed, publicationOutcome, err = prepared.Publish(ctx)
			c.finishCurrentNodeFinalizationPublication(current)
		} else if prepared != nil {
			err = errors.Join(err, prepared.Rollback(err))
		}
		if publicationOutcome.Committed() {
			c.mu.Lock()
			if current.pendingCompletionRequest != pendingRequest {
				c.mu.Unlock()
				return errors.New("Current Node pending completion changed during finalization")
			}
			current.pendingCompletionRequest = nil
			c.completed[scopeID] = struct{}{}
			c.mu.Unlock()
			postTurnFinalization := c.stageCurrentNodePostTurnFinalization(
				ctx,
				scopeID,
				current.reference,
				completed,
				nil,
				starts,
				pending,
			)
			confirmErr := c.authority.ConfirmWorkflowDisposition(scopeID)
			if errors.Is(confirmErr, sessionruntime.ErrExecutionNoLongerLive) {
				confirmErr = nil
			}
			if postTurnFinalization {
				return errors.Join(err, confirmErr)
			}
			return errors.Join(
				err,
				confirmErr,
				c.resolvePendingCurrentNodeAssignmentEnsures(ctx, starts, pending),
			)
		}
		if err != nil {
			c.mu.Lock()
			if current.pendingCompletionRequest == pendingRequest {
				current.pendingCompletionRequest = nil
			}
			c.mu.Unlock()
			if cause := context.Cause(ctx); cause != nil {
				return errors.Join(err, cause)
			}
			return errors.Join(
				err,
				c.publishCurrentNodeFinalizationFailure(ctx, current, scopeID, err),
			)
		}
		c.mu.Lock()
		if current.pendingCompletionRequest != pendingRequest {
			c.mu.Unlock()
			return errors.New("Current Node pending completion changed during finalization")
		}
		current.pendingCompletionRequest = nil
		c.completed[scopeID] = struct{}{}
		c.mu.Unlock()
		return c.resolvePendingCurrentNodeAssignmentEnsures(ctx, starts, pending)
	})
}

func (c *CurrentNodeController) beginCurrentNodeFinalizationPublication(
	run *currentNodeRun,
	scopeID runtimeids.ExecutionScopeID,
) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	current, _, live := c.runByScopeLocked(scopeID)
	if !live || current != run {
		return sessionruntime.ErrExecutionNoLongerLive
	}
	if run.finalizationPublishing {
		return errors.New("Current Node finalization publication is already active")
	}
	run.finalizationPublishing = true
	run.finalizationPublicationDone = make(chan struct{})
	return nil
}

func (c *CurrentNodeController) finishCurrentNodeFinalizationPublication(run *currentNodeRun) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if run == nil || !run.finalizationPublishing || run.finalizationPublicationDone == nil {
		panic("Current Node finalization publication completion has no active publication")
	}
	run.finalizationPublishing = false
	close(run.finalizationPublicationDone)
}

func (c *CurrentNodeController) publishCurrentNodeFinalizationFailure(
	ctx context.Context,
	run *currentNodeRun,
	scopeID runtimeids.ExecutionScopeID,
	cause error,
) error {
	if run == nil {
		return errors.New("Current Node finalization failure requires a Run")
	}
	const reason workflow.CurrentNodeInterruptionReason = "workflow_result_finalization_failed"
	detail := workflow.NewCurrentNodeInterruptionDetail(string(reason), cause)
	publicationOutcome, publicationErr := c.publication.PublishCurrentNodeInterruption(
		ctx,
		[]workflow.CurrentNodeReference{run.reference},
		workflowstore.CurrentNodeInterruptionFromAdmitted,
		workflowstore.LifecycleFieldPresent,
		reason,
		detail,
		[]workflowstore.LifecycleExactExecution{{
			CurrentNode: run.reference,
			ScopeID:     scopeID,
		}},
	)
	if !publicationOutcome.Committed() {
		return publicationErr
	}
	c.mu.Lock()
	run.stopOnce(currentNodeRunStopWorkerFailed, cause)
	c.mu.Unlock()
	c.publishPendingInterruptedCurrentNode(ctx, run.reference, reason)
	confirmErr := c.authority.ConfirmWorkflowDisposition(scopeID)
	if errors.Is(confirmErr, sessionruntime.ErrExecutionNoLongerLive) {
		confirmErr = nil
	}
	return errors.Join(publicationErr, confirmErr)
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
	initial, _, exists := c.runByExecutionScopeLocked(scopeID)
	if !exists {
		c.mu.Unlock()
		return sessionruntime.ErrExecutionNoLongerLive
	}
	taskID := initial.reference.TaskID
	c.mu.Unlock()
	var notificationErr error
	if err := c.lifecycle.Run(ctx, taskID, func(ctx context.Context) error {
		c.mu.Lock()
		if _, stopping := c.stopping[scopeID]; stopping {
			c.mu.Unlock()
			return sessionruntime.ErrExecutionNoLongerLive
		}
		live, _, exists := c.runByExecutionScopeLocked(scopeID)
		if !exists || live != initial {
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
		if live.disposition != currentNodeRunDispositionRunning &&
			live.disposition != currentNodeRunDispositionQueued {
			c.mu.Unlock()
			return sessionruntime.ErrExecutionNoLongerLive
		}
		if live.executionLease == nil {
			c.mu.Unlock()
			return sessionruntime.ErrExecutionNoLongerLive
		}
		_, completed := c.completed[scopeID]
		lease = *live.executionLease
		c.mu.Unlock()
		expectedRun := workflowstore.LifecycleFieldPresent
		expectedExact := []workflowstore.LifecycleExactExecution(nil)
		if completed {
			expectedRun = workflowstore.LifecycleFieldAbsent
		} else if live.disposition == currentNodeRunDispositionRunning {
			expectedExact = []workflowstore.LifecycleExactExecution{{
				CurrentNode: lease.Workflow().CurrentNode,
				ScopeID:     scopeID,
			}}
		}
		publicationOutcome, publicationErr := c.publication.PublishCurrentNodeInterruption(
			ctx,
			[]workflow.CurrentNodeReference{lease.Workflow().CurrentNode},
			workflowstore.CurrentNodeInterruptionFromAdmitted,
			expectedRun,
			reason,
			detail,
			expectedExact,
		)
		if !publicationOutcome.Committed() {
			return publicationErr
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		current, _, stillOwned := c.runByExecutionScopeLocked(scopeID)
		if stillOwned && current == live {
			current.stopOnce(currentNodeRunStopInterrupted, cause)
		}
		notificationErr = publicationErr
		return nil
	}); err != nil {
		return err
	}
	if err := c.authority.ConfirmWorkflowDisposition(scopeID); err != nil &&
		!errors.Is(err, sessionruntime.ErrExecutionNoLongerLive) {
		return err
	}
	c.publishPendingInterruptedCurrentNode(ctx, lease.Workflow().CurrentNode, reason)
	if handle, live := c.authority.ExecutionByScope(scopeID); live {
		handle.RequestStop()
	}
	return notificationErr
}

func (c *CurrentNodeController) ExecutionFinalized(scope sessionruntime.ExecutionScope) {
	ref, ok := scope.Workflow()
	if !ok || c == nil {
		return
	}
	var (
		heldKeys     []workflow.CurrentNodeReferenceKey
		transferHeld bool
		closed       bool
	)
	if err := c.lifecycle.Run(context.Background(), ref.CurrentNode.TaskID, func(ctx context.Context) error {
		key, err := ref.CurrentNode.Key()
		if err != nil {
			return fmt.Errorf("workflow execution finalized invalid current node: %w", err)
		}
		c.mu.Lock()
		live, _, isLive := c.runByScopeLocked(scope.ID())
		if !isLive {
			if gated, exists := c.runs.get(key); exists &&
				gated.executionLease != nil &&
				gated.executionLease.ScopeID() == scope.ID() {
				live = gated
			}
		}
		_, completed := c.completed[scope.ID()]
		interrupted := c.interrupts.scopeFenced(scope.ID())
		_, postTurnFinalizing := c.postTurnFinalization[scope.ID()]
		closed = c.closed
		completionPublished := isLive &&
			live.executionLease != nil &&
			live.executionLease.Workflow().CurrentNode.Equal(ref.CurrentNode) &&
			completed &&
			!interrupted &&
			!postTurnFinalizing &&
			!closed
		c.mu.Unlock()
		c.mu.Lock()
		defer c.mu.Unlock()
		live, _, isLive = c.runByScopeLocked(scope.ID())
		if completionPublished && !isLive {
			return errors.New("completed Current Node Run changed before Authority retirement")
		}
		if isLive {
			c.releaseAgentCapacityLocked(live.agentCapacityLease)
		}
		delete(c.exactScopes, scope.ID())
		delete(c.violations, scope.ID())
		delete(c.stopping, scope.ID())
		delete(c.completed, scope.ID())
		delete(c.postTurnFinalization, scope.ID())
		heldKeys = append([]workflow.CurrentNodeReferenceKey(nil), c.heldStarts[scope.ID()]...)
		delete(c.heldStarts, scope.ID())
		if live != nil {
			if _, gated := c.gates[key]; gated && live.executionLease != nil && live.executionLease.ScopeID() == scope.ID() {
				c.releaseAgentCapacityLocked(live.agentCapacityLease)
				delete(c.gates, key)
			}
			if live.agentActivation != nil {
				live.agentActivation.resolve(currentNodeAgentActivationResult{}, sessionruntime.ErrExecutionNoLongerLive)
			}
			live.stopOnce(currentNodeRunStopSourceRetired, sessionruntime.ErrExecutionNoLongerLive)
		}
		if isLive {
			c.runs.delete(key)
		}
		c.interrupts.finishScope(scope.ID())
		closed = c.closed
		transferHeld = isLive &&
			live.executionLease != nil &&
			live.executionLease.Workflow().CurrentNode.Equal(ref.CurrentNode) &&
			completed &&
			!interrupted &&
			!postTurnFinalizing &&
			!closed
		return nil
	}); err != nil {
		panic(fmt.Sprintf("finalize workflow execution: %v", err))
	}
	if !closed {
		c.wakeAdmissionWorker()
	}
	if !transferHeld {
		if len(heldKeys) != 0 {
			if err := c.lifecycle.Run(context.Background(), ref.CurrentNode.TaskID, func(context.Context) error {
				c.discardRuns(heldKeys, currentNodeRunStopSourceRetired, sessionruntime.ErrExecutionNoLongerLive)
				return nil
			}); err != nil {
				panic(fmt.Sprintf("discard finalized workflow successor Runs: %v", err))
			}
		}
		if !closed {
			c.wakeAdmissionWorker()
		}
		return
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), interruptCleanupTimeout)
	defer cancel()
	starts := c.runCandidates(heldKeys)
	outcome := waitCurrentNodeAssignmentEnsures(waitCtx, starts)
	if outcome.err != nil {
		if len(outcome.pending) != 0 {
			c.continueHeldRunAssignments(heldKeys, starts)
			return
		}
		c.handleCurrentNodeStartFailures(outcome.committed, false, outcome.err)
		if err := c.lifecycle.Run(context.Background(), ref.CurrentNode.TaskID, func(context.Context) error {
			c.discardRuns(heldKeys, currentNodeRunStopWorkerFailed, outcome.err)
			return nil
		}); err != nil {
			panic(fmt.Sprintf("discard failed workflow successor Runs: %v", err))
		}
		return
	}
	if err := c.lifecycle.Run(context.Background(), ref.CurrentNode.TaskID, func(context.Context) error {
		c.enqueueHeldRuns(heldKeys)
		return nil
	}); err != nil {
		panic(fmt.Sprintf("transfer finalized workflow successor Runs: %v", err))
	}
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
		LiveScopes:       make([]CurrentNodeLiveScopeSnapshot, 0, len(c.exactScopes)),
	}
	for entry := c.automaticQueue.first; entry != nil; entry = entry.globalNext {
		start, exists := c.runs.get(entry.key)
		if !exists {
			panic("automatic queue snapshot lost its Run")
		}
		snapshot.AutomaticIntents = append(snapshot.AutomaticIntents, CurrentNodeAutomaticIntent{
			CurrentNode: start.reference,
			NodeKind:    start.nodeKind,
		})
	}
	for key := range c.automaticReservations {
		start, exists := c.runs.get(key)
		if !exists {
			panic("automatic reservation snapshot lost its Run")
		}
		snapshot.AutomaticIntents = append(snapshot.AutomaticIntents, CurrentNodeAutomaticIntent{
			CurrentNode: start.reference,
			NodeKind:    start.nodeKind,
		})
	}
	for _, key := range c.explicitQueue {
		start, exists := c.runs.get(key)
		if !exists {
			panic("explicit queue snapshot lost its Run")
		}
		snapshot.ExplicitStarts = append(snapshot.ExplicitStarts, CurrentNodeExplicitStart{CurrentNode: start.reference})
	}
	for key := range c.explicitReservations {
		start, exists := c.runs.get(key)
		if !exists {
			panic("explicit reservation snapshot lost its Run")
		}
		snapshot.ExplicitStarts = append(snapshot.ExplicitStarts, CurrentNodeExplicitStart{CurrentNode: start.reference})
	}
	for key := range c.gates {
		gate, exists := c.runs.get(key)
		if !exists || gate.executionLease == nil {
			panic("admission gate snapshot lost its Run")
		}
		snapshot.Gates = append(snapshot.Gates, CurrentNodeAdmissionGateSnapshot{
			CurrentNode: gate.reference,
			ScopeID:     gate.executionLease.ScopeID(),
			Automatic:   gate.policy.isAutomatic(),
		})
	}
	for scopeID, key := range c.exactScopes {
		live, exists := c.runs.get(key)
		if !exists {
			panic("exact scope snapshot lost its Run")
		}
		snapshot.LiveScopes = append(snapshot.LiveScopes, CurrentNodeLiveScopeSnapshot{
			CurrentNode: live.reference,
			ScopeID:     scopeID,
			Automatic:   live.policy.isAutomatic(),
		})
	}
	for sourceScope, keys := range c.heldStarts {
		for _, key := range keys {
			start, exists := c.runs.get(key)
			if !exists {
				panic("held-start snapshot lost its Run")
			}
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
	c.postTurnFinalization = make(map[runtimeids.ExecutionScopeID]currentNodePostTurnFinalization)
	taskSet := make(map[workflow.TaskID]struct{})
	for _, run := range c.runs.byCurrentNode {
		taskSet[run.reference.TaskID] = struct{}{}
	}
	taskIDs := make([]workflow.TaskID, 0, len(taskSet))
	for taskID := range taskSet {
		taskIDs = append(taskIDs, taskID)
	}
	sort.Slice(taskIDs, func(i, j int) bool {
		return taskIDs[i] < taskIDs[j]
	})
	gates := make([]sessionruntime.WorkflowExecutionLease, 0, len(c.gates))
	for key := range c.gates {
		run, exists := c.runs.get(key)
		if exists && run.executionLease != nil {
			gates = append(gates, *run.executionLease)
		}
	}
	liveScopes := make([]runtimeids.ExecutionScopeID, 0, len(c.exactScopes))
	for scopeID := range c.exactScopes {
		liveScopes = append(liveScopes, scopeID)
	}
	c.mu.Unlock()

	c.workerCancel()
	closeCause := errCurrentNodeControllerClosed
	for _, taskID := range taskIDs {
		if err := c.lifecycle.Run(context.Background(), taskID, func(context.Context) error {
			return nil
		}); err != nil {
			return fmt.Errorf("wait for task lifecycle before controller close: %w", err)
		}
	}
	publicationErr := c.publication.Close()
	c.mu.Lock()
	for _, run := range c.runs.byCurrentNode {
		c.releaseAgentCapacityLocked(run.agentCapacityLease)
		run.stopOnce(currentNodeRunStopControllerClosed, closeCause)
	}
	c.mu.Unlock()
	for _, gate := range gates {
		gate.Cancel()
	}
	handles := make([]sessionruntime.ExecutionHandle, 0, len(liveScopes))
	for _, scopeID := range liveScopes {
		handle, exists := c.authority.ExecutionByScope(scopeID)
		if exists {
			_ = c.authority.ConfirmWorkflowDisposition(scopeID)
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
	c.explicitQueue = nil
	c.explicitQueued = make(map[workflow.CurrentNodeReferenceKey]struct{})
	c.automaticQueue.clear()
	c.queued = make(map[workflow.CurrentNodeReferenceKey]struct{})
	c.heldStarts = make(map[runtimeids.ExecutionScopeID][]workflow.CurrentNodeReferenceKey)
	c.runs.clear()
	c.gates = make(map[workflow.CurrentNodeReferenceKey]struct{})
	c.exactScopes = make(map[runtimeids.ExecutionScopeID]workflow.CurrentNodeReferenceKey)
	c.explicitReservations = make(map[workflow.CurrentNodeReferenceKey]struct{})
	c.automaticReservations = make(map[workflow.CurrentNodeReferenceKey]struct{})
	c.admissionWorkers = make(map[workflow.CurrentNodeReferenceKey]struct{})
	c.mu.Unlock()
	return errors.Join(errors.Join(stopErrs...), workerErr, publicationErr)
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
	live, _, exists := c.runByScopeLocked(scopeID)
	c.mu.Unlock()
	if !exists || live.executionLease == nil {
		return sessionruntime.WorkflowExecutionLease{}, sessionruntime.ErrExecutionNoLongerLive
	}
	return *live.executionLease, nil
}

var _ workflowruntime.Controller = (*CurrentNodeController)(nil)
var _ sessionruntime.ExecutionFinalized = (*CurrentNodeController)(nil)
