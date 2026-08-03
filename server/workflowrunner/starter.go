package workflowrunner

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"path/filepath"
	"strings"
	"sync/atomic"
	"text/template"
	"time"

	"core/prompts"
	"core/server/auth"
	"core/server/launch"
	"core/server/llm"
	"core/server/metadata"
	"core/server/runtime"
	"core/server/runtimewire"
	"core/server/session"
	"core/server/sessionruntime"
	askquestion "core/server/tools"
	"core/server/workflow"
	"core/server/workflowattention"
	"core/server/workflowexecution"
	"core/server/workflowruntime"
	"core/server/workflowscript"
	"core/server/workflowstore"
	"core/server/worktree"
	"core/shared/config"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/sessioncontract"
	"core/shared/textutil"
	"core/shared/toolspec"
)

const (
	ReasonRuntimeFailed = "workflow_runtime_failed"
)

type RuntimeStore interface {
	ResolveCurrentNodeStartContext(context.Context, workflow.CurrentNodeReference) (workflowstore.CurrentNodeStartContext, error)
	BindSessionToCurrentNode(context.Context, workflowstore.CurrentNodeSessionBindingRequest) (workflowstore.TaskSessionAssociation, error)
	ValidateCurrentNodeSessionBinding(context.Context, runtimeids.SessionID, workflow.CurrentNodeReference) error
	CountTaskComments(context.Context, workflow.TaskID) (int64, error)
}

type ExecutionTargetPreparer = workflowexecution.LaunchTargetPreparer

type WorkflowAttentionRegistry interface {
	workflowattention.QuestionAttentionRegistry
	workflowattention.ApprovalQuestionAttentionRegistry
}

type Starter struct {
	cfg                  config.App
	metadata             *metadata.Store
	store                RuntimeStore
	authManager          *auth.Manager
	attention            WorkflowAttentionRegistry
	runtimeAuthority     *sessionruntime.Authority
	storeOptions         []session.StoreOption
	runtimeClientFactory runtimewire.RuntimeClientFactory
	mutationPermit       *workflowexecution.MutationPermit
	taskAwarenessSource  workflowruntime.TaskAwarenessSource
	executionTarget      ExecutionTargetPreparer
	closed               atomic.Bool
}

type StarterOptions struct {
	RuntimeClientFactory runtimewire.RuntimeClientFactory
	RuntimeAuthority     *sessionruntime.Authority
	MutationPermit       *workflowexecution.MutationPermit
	TaskDependencies     TaskDependencyCounter
	ExecutionTarget      ExecutionTargetPreparer
}

func NewStarter(cfg config.App, metadataStore *metadata.Store, store RuntimeStore, authManager *auth.Manager, attention WorkflowAttentionRegistry, opts StarterOptions) (*Starter, error) {
	if strings.TrimSpace(cfg.PersistenceRoot) == "" {
		return nil, errors.New("workflow runtime persistence root is required")
	}
	if metadataStore == nil || store == nil || opts.RuntimeAuthority == nil || opts.MutationPermit == nil || opts.TaskDependencies == nil {
		return nil, errors.New("workflow runtime dependencies are required")
	}
	taskAwarenessSource, err := NewTaskAwarenessSource(store, opts.TaskDependencies)
	if err != nil {
		return nil, err
	}
	return &Starter{
		cfg:                  cfg,
		metadata:             metadataStore,
		store:                store,
		authManager:          authManager,
		attention:            attention,
		runtimeAuthority:     opts.RuntimeAuthority,
		storeOptions:         metadataStore.AuthoritativeSessionStoreOptions(),
		runtimeClientFactory: opts.RuntimeClientFactory,
		mutationPermit:       opts.MutationPermit,
		taskAwarenessSource:  taskAwarenessSource,
		executionTarget:      opts.ExecutionTarget,
	}, nil
}

func (s *Starter) StartCurrentNodeWithPreparation(
	ctx context.Context,
	reference workflow.CurrentNodeReference,
	preparation workflowexecution.LaunchPreparation,
	taskPromptDelivery workflowruntime.TaskPromptDelivery,
	assignmentSteer workflowexecution.CurrentNodeAssignmentSteer,
	lease sessionruntime.WorkflowExecutionLease,
	controller workflowruntime.Controller,
) (err error) {
	defer func() {
		err = currentNodeStartFailure(err)
	}()
	if s.closed.Load() {
		return errors.New("workflow runtime starter closed")
	}
	if !lease.Workflow().CurrentNode.Equal(reference) {
		return errors.New("workflow execution lease does not match current node")
	}
	if err := preparation.Validate(); err != nil {
		return err
	}
	var preparedRoot *workflowstore.ExecutionRoot
	if preparation.Kind != workflowexecution.LaunchPreparationEstablishedRoot {
		if s.executionTarget == nil {
			return errors.New("workflow execution target preparer is required")
		}
		var root workflowstore.ExecutionRoot
		var err error
		if preparation.Coordinator != nil {
			root, err = preparation.Coordinator.Prepare(ctx, reference, preparation, s.executionTarget)
		} else {
			root, err = s.executionTarget.PrepareExecutionTarget(ctx, reference, preparation)
		}
		if err != nil {
			if preparation.Kind == workflowexecution.LaunchPreparationEstablishUnlockedNone ||
				preparation.Kind == workflowexecution.LaunchPreparationEstablishUnlockedManaged {
				return &workflowexecution.ExecutionTargetPreparationFailure{Cause: err}
			}
			return err
		}
		preparedRoot = &root
	}
	input, err := s.store.ResolveCurrentNodeStartContext(ctx, reference)
	if err != nil {
		return err
	}
	if preparedRoot != nil {
		input.ExecutionRoot = preparedRoot
	}
	var startErr error
	switch input.Node.Kind {
	case workflow.NodeKindScript:
		startErr = s.startCurrentNodeScript(ctx, input, lease, controller)
	case workflow.NodeKindAgent:
		if err := s.validateRole(input.Node.SubagentRole); err != nil {
			startErr = err
			break
		}
		startErr = s.startCurrentNodeAgent(ctx, input, taskPromptDelivery, assignmentSteer, lease, controller)
	default:
		startErr = fmt.Errorf("current node %v is not executable", reference)
	}
	return currentNodeStartFailure(startErr)
}

func currentNodeStartFailure(cause error) error {
	if cause == nil {
		return nil
	}
	var existing *workflowexecution.CurrentNodeStartFailure
	if errors.As(cause, &existing) {
		return cause
	}
	reason := workflow.CurrentNodeInterruptionReason(workflow.CurrentNodeInterruptionCodeRuntimeStartFailed)
	detail := workflow.CurrentNodeInterruptionDetail{
		Code:   string(reason),
		Fields: map[string]string{"error": cause.Error()},
	}
	var targetPreparation *workflowexecution.ExecutionTargetPreparationFailure
	if errors.As(cause, &targetPreparation) {
		detail = workflow.CurrentNodeInterruptionDetail{
			Code:   workflow.CurrentNodeInterruptionCodeExecutionTargetPreparationFailed,
			Fields: map[string]string{"error": targetPreparation.Error()},
		}
	}
	var validation workflowscript.ValidationError
	if errors.As(cause, &validation) {
		reason = workflow.CurrentNodeInterruptionReason(workflowscript.ReasonValidationFailed)
		detail = workflow.CurrentNodeInterruptionDetail{
			Code: workflowscript.ReasonValidationFailed,
			Fields: map[string]string{
				"code":          validation.Diagnostic.Code,
				"raw_path":      validation.Diagnostic.RawPath,
				"resolved_path": validation.Diagnostic.ResolvedPath,
			},
		}
	}
	var target *serverapi.WorkflowExecutionTargetResolutionError
	if errors.As(cause, &target) {
		detail = workflow.CurrentNodeInterruptionDetail{
			Code: workflow.CurrentNodeInterruptionCodeExecutionTargetResolutionFailed,
			Fields: map[string]string{
				"code":          string(target.Code),
				"requested_ref": target.RequestedRef,
			},
		}
	}
	var revisionResolution *worktree.GitRevisionResolutionError
	if errors.As(cause, &revisionResolution) {
		detail = workflow.CurrentNodeInterruptionDetail{
			Code: workflow.CurrentNodeInterruptionCodeExecutionTargetResolutionFailed,
			Fields: map[string]string{
				"code":          string(revisionResolution.Kind),
				"requested_ref": revisionResolution.RequestedRef,
			},
		}
	}
	var defaultBranchResolution *worktree.GitDefaultBranchResolutionError
	if errors.As(cause, &defaultBranchResolution) {
		detail = workflow.CurrentNodeInterruptionDetail{
			Code: workflow.CurrentNodeInterruptionCodeExecutionTargetResolutionFailed,
			Fields: map[string]string{
				"code":           string(defaultBranchResolutionCode(defaultBranchResolution.Kind)),
				"selection_mode": string(workflow.ExecutionTargetModeDefaultBranch),
			},
		}
	}
	var locked *worktree.LockedTaskWorktreeError
	if errors.As(cause, &locked) && targetPreparation == nil {
		detail = workflow.CurrentNodeInterruptionDetail{
			Code:   "workflow_locked_execution_target_unavailable",
			Fields: map[string]string{"cause": string(locked.Cause)},
		}
	}
	var retained *serverapi.WorktreeSetupRetainedError
	if errors.As(cause, &retained) {
		detail = workflow.CurrentNodeInterruptionDetail{
			Code:   "workflow_worktree_setup_failed",
			Fields: map[string]string{"diagnostic": retained.Diagnostic},
		}
	}
	return &workflowexecution.CurrentNodeStartFailure{
		Cause:  cause,
		Reason: reason,
		Detail: detail,
	}
}

func defaultBranchResolutionCode(kind worktree.GitDefaultBranchResolutionErrorKind) serverapi.WorkflowExecutionTargetUnavailableCause {
	switch kind {
	case worktree.GitDefaultBranchResolutionErrorMissing:
		return serverapi.WorkflowExecutionTargetUnavailableCauseDefaultBranchMissing
	case worktree.GitDefaultBranchResolutionErrorAmbiguous:
		return serverapi.WorkflowExecutionTargetUnavailableCauseDefaultBranchAmbiguous
	default:
		return serverapi.WorkflowExecutionTargetUnavailableCauseGitFailure
	}
}

func (s *Starter) SteerCurrentNodeAssignment(
	ctx context.Context,
	reference workflow.CurrentNodeReference,
) (workflowexecution.CurrentNodeAssignmentSteer, error) {
	if s.closed.Load() {
		return nil, errors.New("workflow runtime starter closed")
	}
	input, err := s.store.ResolveCurrentNodeStartContext(ctx, reference)
	if err != nil {
		return nil, err
	}
	if input.Node.Kind == workflow.NodeKindScript {
		return runtime.CompletedWorkflowAssignmentSteer(session.CommitReceipt{Committed: true}, nil), nil
	}
	if input.Node.Kind != workflow.NodeKindAgent {
		return nil, fmt.Errorf("current node %v is not executable", reference)
	}
	if err := s.validateRole(input.Node.SubagentRole); err != nil {
		return nil, err
	}
	prepared, err := s.prepareCurrentNodeAgentSession(ctx, input, false, false)
	if err != nil {
		return nil, err
	}
	instructions, err := BuildCurrentSessionTaskInstructions(input)
	if err != nil {
		return nil, prepared.cleanup(err)
	}
	awareness, err := s.taskAwarenessSource.TaskAwareness(ctx, input.Task.ID)
	if err != nil {
		return nil, prepared.cleanup(err)
	}
	assignment := runtime.WorkflowAssignment{
		ContextMode:    input.ContextMode,
		CompletionMode: prepared.mode,
		Prompt: workflowruntime.PromptContract{
			Identity:               workflowruntime.CurrentNodePromptIdentity(input.CurrentNode.Reference),
			CompletionMode:         prepared.mode,
			UseAutomaticToolChoice: !prepared.plan.ActiveSettings.Workflow.UseRequiredToolCalls,
			Instructions:           instructions,
			Transitions:            workflowCompletionTransitions(input.TransitionOptions, input.TransitionIDs),
			TaskAwareness:          awareness,
		},
	}
	var steer runtime.WorkflowAssignmentSteer
	admission, steerErr := s.runtimeAuthority.WithDormantSessionStore(ctx, prepared.plan.Descriptor, func(_ context.Context, store *session.Store) error {
		var err error
		steer, err = runtime.SteerPersistedWorkflowAssignment(store, assignment)
		return err
	})
	if steerErr == nil && admission.RuntimeAvailable {
		steerErr = s.runtimeAuthority.WithCurrentRuntime(ctx, prepared.plan.Descriptor.SessionID(), func(_ context.Context, engine *runtime.Engine) error {
			var err error
			steer, err = engine.SteerWorkflowAssignment(assignment)
			return err
		})
	}
	if steerErr != nil {
		return nil, prepared.cleanup(steerErr)
	}
	prepared.cleanup = func(err error) error { return err }
	return &currentNodeAgentAssignmentSteer{
		reference:  reference,
		completion: steer,
		prepared:   prepared,
	}, nil
}

type currentNodeAgentAssignmentSteer struct {
	reference  workflow.CurrentNodeReference
	completion runtime.WorkflowAssignmentSteer
	prepared   preparedCurrentNodeAgentSession
}

func (s *currentNodeAgentAssignmentSteer) Wait(ctx context.Context) (session.CommitReceipt, error) {
	if s == nil {
		return session.CommitReceipt{}, errors.New("current node agent assignment steer is required")
	}
	return s.completion.Wait(ctx)
}

type preparedCurrentNodeAgentSession struct {
	root    workflowstore.ExecutionRoot
	plan    launch.SessionPlan
	client  llm.Client
	mode    workflowruntime.CompletionMode
	cleanup func(error) error
}

func (s *Starter) prepareCurrentNodeAgentSession(
	ctx context.Context,
	input workflowstore.CurrentNodeStartContext,
	requireRuntimeClient bool,
	sessionPrepared bool,
) (preparedCurrentNodeAgentSession, error) {
	root, err := requireCurrentNodeExecutionRoot(input)
	if err != nil {
		return preparedCurrentNodeAgentSession{}, err
	}
	plan, disposable, err := s.planCurrentNodeSession(ctx, input, root, sessionPrepared)
	if err != nil {
		return preparedCurrentNodeAgentSession{}, err
	}
	sessionBound := false
	cleanup := func(err error) error {
		if !disposable {
			return err
		}
		cleanupCtx := context.WithoutCancel(ctx)
		if sessionBound && input.CurrentNode.SessionID != nil {
			cloneSessionID := plan.Descriptor.SessionID()
			if _, restoreErr := s.store.BindSessionToCurrentNode(cleanupCtx, workflowstore.CurrentNodeSessionBindingRequest{
				Association: workflowstore.TaskSessionAssociationRequest{
					SessionID:    *input.CurrentNode.SessionID,
					CurrentNode:  input.CurrentNode.Reference,
					AssociatedAt: time.Now().UTC(),
				},
				ExpectedCurrentSessionID: &cloneSessionID,
			}); restoreErr != nil {
				return errors.Join(err, fmt.Errorf(
					"restore current node %v source Session %q before clone cleanup: %w",
					input.CurrentNode.Reference,
					input.CurrentNode.SessionID,
					restoreErr,
				))
			}
		}
		return errors.Join(err, s.cleanupSession(cleanupCtx, plan.Descriptor))
	}
	if err := s.applyCurrentNodeSessionMetadata(ctx, input, &plan); err != nil {
		return preparedCurrentNodeAgentSession{}, cleanup(err)
	}
	var client llm.Client
	if requireRuntimeClient {
		client, err = s.newWorkflowProviderClient(ctx, plan)
		if err != nil {
			return preparedCurrentNodeAgentSession{}, cleanup(err)
		}
	}
	mode, client, err := s.resolveCurrentNodeCompletionMode(ctx, input, plan, client)
	if err != nil {
		return preparedCurrentNodeAgentSession{}, cleanup(err)
	}
	if sessionPrepared {
		if err := s.store.ValidateCurrentNodeSessionBinding(
			ctx,
			plan.Descriptor.SessionID(),
			input.CurrentNode.Reference,
		); err != nil {
			return preparedCurrentNodeAgentSession{}, cleanup(err)
		}
	} else {
		if _, err := s.store.BindSessionToCurrentNode(ctx, workflowstore.CurrentNodeSessionBindingRequest{
			Association: workflowstore.TaskSessionAssociationRequest{
				SessionID:    plan.Descriptor.SessionID(),
				CurrentNode:  input.CurrentNode.Reference,
				AssociatedAt: time.Now().UTC(),
			},
			ExpectedCurrentSessionID: input.SourceSessionID,
		}); err != nil {
			return preparedCurrentNodeAgentSession{}, cleanup(err)
		}
		sessionBound = true
	}
	return preparedCurrentNodeAgentSession{
		root:    root,
		plan:    plan,
		client:  client,
		mode:    mode,
		cleanup: cleanup,
	}, nil
}

func (s *Starter) startCurrentNodeAgent(
	ctx context.Context,
	input workflowstore.CurrentNodeStartContext,
	taskPromptDelivery workflowruntime.TaskPromptDelivery,
	assignmentSteer workflowexecution.CurrentNodeAssignmentSteer,
	lease sessionruntime.WorkflowExecutionLease,
	controller workflowruntime.Controller,
) error {
	prepared, resource, err := s.currentNodeAgentSessionForStart(ctx, input, assignmentSteer)
	if err != nil {
		return err
	}
	if err := s.applyCurrentNodeSessionExecutionTarget(ctx, input, prepared.plan.Descriptor); err != nil {
		return prepared.cleanup(err)
	}
	if prepared.client == nil {
		prepared.client, err = s.newWorkflowProviderClient(ctx, prepared.plan)
		if err != nil {
			return prepared.cleanup(err)
		}
	}
	runtimeConfig, err := BuildCurrentNodeRuntimeConfig(
		input,
		lease,
		taskPromptDelivery,
		prepared.mode,
		s.cfg.Settings.Workflow.MaxInvalidCompletionAttempts,
		prepared.plan.ActiveSettings.Workflow.UseRequiredToolCalls,
		controller,
		s.taskAwarenessSource,
	)
	if err != nil {
		return prepared.cleanup(err)
	}
	pathContext, err := currentNodeManagedWorktreePathContext(prepared.plan, prepared.root)
	if err != nil {
		return prepared.cleanup(err)
	}
	runtimePlan, err := sessionruntime.NewAgentRuntimePlan(sessionruntime.AgentRuntimePlanOptions{
		Settings: prepared.plan.ActiveSettings, EnabledTools: workflowRuntimeEnabledTools(prepared.plan.EnabledTools), Workdir: prepared.root.EffectiveRoot(),
		ManagedWorktreePathContext: pathContext, Sources: prepared.plan.Source.Sources, Headless: true, Client: prepared.client,
		ReviewerClientFactory: s.runtimeClientFactory, CurrentNodeExecution: runtimeConfig,
		StartLogLines: []string{fmt.Sprintf("workflow.runtime.start task_id=%s session_id=%s node_id=%s execution_root=%s model=%s", input.Task.ID, prepared.plan.Descriptor.SessionID(), input.Node.ID, prepared.root.EffectiveRoot(), prepared.plan.ActiveSettings.Model)},
		AskQuestionBatchSkipped: func(batch askquestion.AskQuestionBatchMetadata) {
			if s.attention == nil {
				return
			}
			if err := workflowattention.PrepareSkippedTaskQuestionBatch(s.attention, currentNodeQuestionContext(input, prepared.plan.Descriptor.SessionID().String()), batch, time.Now().UTC()); err != nil {
				slog.Warn("prepare skipped current-node workflow question batch failed", "task_id", input.Task.ID, "node_id", input.Node.ID, "error", err)
			}
		},
	})
	if err != nil {
		return prepared.cleanup(err)
	}
	_, err = s.runtimeAuthority.StartAgentExecution(ctx, sessionruntime.AgentExecutionRequest{
		Descriptor: prepared.plan.Descriptor, Runtime: &runtimePlan, Workflow: &lease, Resource: resource,
		Ask: func(askCtx context.Context, scope sessionruntime.ExecutionScope, askReq askquestion.AskQuestionRequest) (askquestion.AskQuestionResponse, error) {
			return s.handleCurrentNodeAsk(askCtx, executionPromptAwaiter{authority: s.runtimeAuthority, scope: scope}, input, prepared.plan.Descriptor.SessionID().String(), askReq)
		},
		Runner: func(runCtx context.Context, scope sessionruntime.ExecutionScope, bridge sessionruntime.AgentRuntimeBridge) error {
			turnErr := bridge.WithEngine(runCtx, func(engineCtx context.Context, engine *runtime.Engine) error {
				if input.ContextMode == workflow.ContextModeCompactAndContinueSession {
					if err := engine.CompactContextForWorkflowContinuation(metadata.WithQueryFailureDiagnostics(engineCtx)); err != nil {
						return err
					}
				}
				_, err := engine.SubmitWorkflowTurn(metadata.WithQueryFailureDiagnostics(engineCtx))
				return err
			})
			if turnErr == nil {
				return nil
			}
			reason := ReasonRuntimeFailed
			if errors.Is(turnErr, context.Canceled) || context.Cause(runCtx) != nil {
				reason = string(workflow.CurrentNodeInterruptionReasonRuntimeCanceled)
			}
			return errors.Join(turnErr, s.failCurrentNodeScope(context.WithoutCancel(runCtx), controller, scope, reason, turnErr))
		},
	})
	if err != nil {
		return prepared.cleanup(err)
	}
	return nil
}

func (s *Starter) currentNodeAgentSessionForStart(
	ctx context.Context,
	input workflowstore.CurrentNodeStartContext,
	assignmentSteer workflowexecution.CurrentNodeAssignmentSteer,
) (preparedCurrentNodeAgentSession, sessionruntime.AgentResourceSelection, error) {
	if assignmentSteer == nil {
		prepared, err := s.prepareCurrentNodeAgentSession(ctx, input, true, input.CurrentNode.SessionID != nil)
		return prepared, sessionruntime.OpenAgentResource{}, err
	}
	assignment, ok := assignmentSteer.(*currentNodeAgentAssignmentSteer)
	if !ok {
		return preparedCurrentNodeAgentSession{}, nil, fmt.Errorf(
			"current node %v received incompatible assignment steer %T",
			input.CurrentNode.Reference,
			assignmentSteer,
		)
	}
	if !assignment.reference.Equal(input.CurrentNode.Reference) {
		return preparedCurrentNodeAgentSession{}, nil, fmt.Errorf(
			"current node assignment steer %v does not match start %v",
			assignment.reference,
			input.CurrentNode.Reference,
		)
	}
	receipt, err := assignment.Wait(ctx)
	if err != nil {
		return preparedCurrentNodeAgentSession{}, nil, err
	}
	if !receipt.Committed {
		return preparedCurrentNodeAgentSession{}, nil, errors.New("current node assignment was not committed")
	}
	return assignment.prepared, sessionruntime.ReplaceAgentResource{}, nil
}

func (s *Starter) planCurrentNodeSession(
	ctx context.Context,
	input workflowstore.CurrentNodeStartContext,
	root workflowstore.ExecutionRoot,
	sessionPrepared bool,
) (launch.SessionPlan, bool, error) {
	policy, err := resolveCurrentNodeSessionPolicy(input)
	if err != nil {
		return launch.SessionPlan{}, false, err
	}
	cfg := s.cfg
	cfg.WorkspaceRoot = root.SourceWorkspaceRoot
	containerDir := filepath.Join(cfg.PersistenceRoot, "projects", input.Task.ProjectID, "sessions")
	var intent serverapi.SessionLaunchIntent
	var disposable bool
	if sessionPrepared {
		if input.CurrentNode.SessionID == nil {
			return launch.SessionPlan{}, false, errors.New("resumed current node has no assigned Session")
		}
		intent = serverapi.OpenExistingSessionLaunchIntent(*input.CurrentNode.SessionID)
	} else {
		intent, disposable, err = s.currentNodeSessionIntent(input, containerDir, policy)
		if err != nil {
			return launch.SessionPlan{}, false, err
		}
	}
	planner := launch.Planner{Config: cfg, ContainerDir: containerDir, StoreOptions: s.storeOptions, PersistedSessions: s.metadata, ExecutionTargets: s.metadata, MetadataStoreOpener: func(string) (launch.MetadataExecutionTargetStore, error) { return s.metadata, nil }}
	plan, err := planner.PlanSession(ctx, launch.SessionRequest{
		Mode:                                launch.ModeHeadless,
		Intent:                              intent,
		SkipContinuationAgentRoleValidation: input.ContextMode == workflow.ContextModeCompactAndContinueSession,
	})
	if err != nil {
		return launch.SessionPlan{}, disposable, err
	}
	if err := s.withSessionStore(ctx, plan.Descriptor, func(_ context.Context, store *session.Store) error { return store.EnsureDurable() }); err != nil {
		return launch.SessionPlan{}, disposable, err
	}
	if input.ContextMode == workflow.ContextModeCompactAndContinueSession && !sessionPrepared {
		if err := s.withSessionStore(ctx, plan.Descriptor, func(_ context.Context, store *session.Store) error {
			return store.ResetLockedContractForCompactionBoundary()
		}); err != nil {
			return launch.SessionPlan{}, disposable, err
		}
		plan, err = planner.PlanSession(ctx, launch.SessionRequest{
			Mode:   launch.ModeHeadless,
			Intent: serverapi.OpenExistingSessionLaunchIntent(plan.Descriptor.SessionID()),
		})
		if err != nil {
			return launch.SessionPlan{}, disposable, err
		}
	}
	if sessionPrepared {
		return plan, disposable, nil
	}
	if err := validateRetainedWorkflowSessionAgentRole(input, plan, policy); err != nil {
		return launch.SessionPlan{}, disposable, err
	}
	if policy.assignee != currentNodeSessionAssigneeEstablishTarget {
		return plan, disposable, nil
	}
	err = s.withSessionStore(ctx, plan.Descriptor, func(_ context.Context, store *session.Store) error {
		var applyErr error
		plan, _, applyErr = planner.ApplyRunPromptOverridesWithStore(
			plan,
			store,
			workflowPromptOverrides(input.Node.SubagentRole),
			auth.EmptyState(),
			launch.RunPromptOverrideOptions{},
		)
		return applyErr
	})
	return plan, disposable, err
}

type currentNodeSessionAssigneePolicy uint8

const (
	currentNodeSessionAssigneeEstablishTarget currentNodeSessionAssigneePolicy = iota + 1
	currentNodeSessionAssigneeRequireTargetMatch
	currentNodeSessionAssigneePreserve
)

type currentNodeSessionPolicy struct {
	cloneRetainedSession bool
	assignee             currentNodeSessionAssigneePolicy
}

func resolveCurrentNodeSessionPolicy(input workflowstore.CurrentNodeStartContext) (currentNodeSessionPolicy, error) {
	source := workflow.CanonicalContextSource(input.EnteringEdge.ContextSource)
	targetOwned := false
	switch source.Kind {
	case workflow.ContextSourceImmediateSource, workflow.ContextSourceSelectedNode:
	case workflow.ContextSourcePreviousTarget, workflow.ContextSourcePreviousTargetOrNew:
		targetOwned = true
	default:
		return currentNodeSessionPolicy{}, fmt.Errorf(
			"current node session policy does not support context source %q",
			source.Kind,
		)
	}
	switch input.ContextMode {
	case workflow.ContextModeNewSession:
		return currentNodeSessionPolicy{
			assignee: currentNodeSessionAssigneeEstablishTarget,
		}, nil
	case workflow.ContextModeCompactAndContinueSession:
		return currentNodeSessionPolicy{
			cloneRetainedSession: input.IsFanoutBranch && !targetOwned,
			assignee:             currentNodeSessionAssigneeEstablishTarget,
		}, nil
	case workflow.ContextModeContinueSession:
		if targetOwned {
			return currentNodeSessionPolicy{
				assignee: currentNodeSessionAssigneePreserve,
			}, nil
		}
		return currentNodeSessionPolicy{
			cloneRetainedSession: input.IsFanoutBranch,
			assignee:             currentNodeSessionAssigneeRequireTargetMatch,
		}, nil
	default:
		return currentNodeSessionPolicy{}, fmt.Errorf(
			"current node session policy does not support context mode %q",
			input.ContextMode,
		)
	}
}

func validateRetainedWorkflowSessionAgentRole(
	input workflowstore.CurrentNodeStartContext,
	plan launch.SessionPlan,
	policy currentNodeSessionPolicy,
) error {
	if input.CurrentNode.SessionID == nil ||
		policy.assignee == currentNodeSessionAssigneeEstablishTarget {
		return nil
	}
	roleOverride, err := workflowPromptOverrides(input.Node.SubagentRole).AgentRoleOverride()
	if err != nil {
		return err
	}
	var requestedRole *string
	if roleOverride.Present && !roleOverride.Default {
		requestedRole = &roleOverride.Role
	}
	var retainedRole *string
	if plan.Continuation != nil {
		retainedRole = plan.Continuation.AgentRole
	}
	if textutil.EqualOptional(retainedRole, requestedRole) {
		return nil
	}
	retainedName := workflow.DefaultAgentRole
	if retainedRole != nil {
		retainedName = *retainedRole
	}
	requestedName := workflow.DefaultAgentRole
	if requestedRole != nil {
		requestedName = *requestedRole
	}
	return fmt.Errorf(
		"%w: session_id=%s current=%q requested=%q",
		launch.ErrLockedAgentRoleChange,
		plan.Descriptor.SessionID(),
		retainedName,
		requestedName,
	)
}

func (s *Starter) currentNodeSessionIntent(
	input workflowstore.CurrentNodeStartContext,
	containerDir string,
	policy currentNodeSessionPolicy,
) (serverapi.SessionLaunchIntent, bool, error) {
	if input.CurrentNode.SessionID == nil {
		return serverapi.CreateNewSessionLaunchIntent(serverapi.IndependentSessionCreateOrigin()), true, nil
	}
	if policy.cloneRetainedSession {
		id, err := s.cloneSourceSessionForFanout(containerDir, input.CurrentNode.SessionID.String())
		if err != nil {
			return serverapi.SessionLaunchIntent{}, false, err
		}
		sessionID, err := runtimeids.ParseSessionID(id)
		if err != nil {
			return serverapi.SessionLaunchIntent{}, false, err
		}
		return serverapi.OpenExistingSessionLaunchIntent(sessionID), true, nil
	}
	return serverapi.OpenExistingSessionLaunchIntent(*input.CurrentNode.SessionID), false, nil
}

func (s *Starter) applyCurrentNodeSessionMetadata(ctx context.Context, input workflowstore.CurrentNodeStartContext, plan *launch.SessionPlan) error {
	name, err := workflowSessionNameFromCurrentNode(input)
	if err != nil {
		return err
	}
	preview, err := renderCurrentNodePrompt(input.PromptTemplate, input)
	if err != nil {
		return err
	}
	if err := s.withSessionStore(ctx, plan.Descriptor, func(_ context.Context, store *session.Store) error { return store.SetListingMetadata(name, preview) }); err != nil {
		return err
	}
	plan.SessionName, plan.FirstPromptPreview = &name, preview
	return nil
}

func (s *Starter) applyCurrentNodeSessionExecutionTarget(ctx context.Context, input workflowstore.CurrentNodeStartContext, descriptor session.SessionDescriptor) error {
	root, err := requireCurrentNodeExecutionRoot(input)
	if err != nil {
		return err
	}
	update := metadata.SessionExecutionTargetUpdate{SessionID: descriptor.SessionID().String(), Workspace: &metadata.SessionExecutionTargetUpdateWorkspace{ID: root.SourceWorkspaceID}, CwdRelpath: "."}
	if root.Managed != nil {
		update.Worktree = &metadata.SessionExecutionTargetUpdateWorktree{ID: root.Managed.WorktreeID}
	}
	return s.mutationPermit.Run(ctx, func(ctx context.Context) error { return s.metadata.UpdateSessionExecutionTarget(ctx, update) })
}

func currentNodeManagedWorktreePathContext(plan launch.SessionPlan, root workflowstore.ExecutionRoot) (*askquestion.ManagedWorktreePathContext, error) {
	if root.Managed == nil || strings.TrimSpace(plan.ActiveSettings.Worktrees.BaseDir) == "" {
		return nil, nil
	}
	return askquestion.NewManagedWorktreePathContext(plan.ActiveSettings.Worktrees.BaseDir, &root.Managed.Root)
}

func workflowSessionNameFromCurrentNode(input workflowstore.CurrentNodeStartContext) (string, error) {
	taskID := input.Task.ShortID
	if taskID == "" {
		taskID = string(input.Task.ID)
	}
	if taskID == "" || input.AcceptedTransitionPath.SourceNodeDisplayName == "" || input.AcceptedTransitionPath.TargetNodeDisplayName == "" {
		return "", errors.New("current workflow session metadata is incomplete")
	}
	return fmt.Sprintf("%s: %s -> %s", taskID, input.AcceptedTransitionPath.SourceNodeDisplayName, input.AcceptedTransitionPath.TargetNodeDisplayName), nil
}

func renderCurrentNodePrompt(text string, input workflowstore.CurrentNodeStartContext) (string, error) {
	source := ""
	if input.SourceSessionID != nil {
		source = input.SourceSessionID.String()
	}
	return renderWorkflowPrompt(text, workflowPromptInput{Task: input.Task, Workflow: input.Workflow, Node: input.Node, CurrentNode: input.CurrentNode.Reference, ContextMode: input.ContextMode, SourceSessionID: source, TransitionOptions: input.TransitionOptions, TransitionIDs: input.TransitionIDs, PromptTemplate: text, ParameterValues: input.ParameterValues, PriorValues: input.CurrentNode.PriorValues})
}

func (s *Starter) resolveCurrentNodeCompletionMode(ctx context.Context, input workflowstore.CurrentNodeStartContext, plan launch.SessionPlan, client llm.Client) (workflowruntime.CompletionMode, llm.Client, error) {
	if plan.Locked != nil && plan.Locked.WorkflowCompletionMode != nil {
		mode, err := workflowruntime.ParseCompletionMode(string(*plan.Locked.WorkflowCompletionMode))
		if err != nil {
			return "", client, fmt.Errorf("parse retained Session completion mode: %w", err)
		}
		return mode, client, nil
	}
	configured := s.cfg.Settings.Workflow.CompletionMode
	if input.Node.CompletionMode != "" {
		configured = config.WorkflowCompletionMode(input.Node.CompletionMode)
	}
	selection := workflowruntime.CompletionModeSelection{ConfiguredMode: configured, HasContinueSessionEdge: input.HasContinueSessionOutgoingEdge, ShellAvailable: toolIDEnabled(plan.EnabledTools, toolspec.ToolExecCommand)}
	if workflowCompletionModeNeedsProviderCapabilities(selection) {
		caps, resolved, err := s.workflowProviderCapabilities(ctx, plan, client)
		if err != nil {
			return "", resolved, err
		}
		selection.ProviderCapabilities, client = caps, resolved
	}
	mode, err := workflowruntime.SelectCompletionMode(selection)
	if err != nil {
		return "", client, err
	}
	if plan.Locked != nil {
		if err := s.withSessionStore(ctx, plan.Descriptor, func(_ context.Context, store *session.Store) error {
			_, backfillErr := store.BackfillLockedWorkflowCompletionMode(mode)
			return backfillErr
		}); err != nil {
			return "", client, fmt.Errorf("backfill retained Session completion mode: %w", err)
		}
	}
	return mode, client, nil
}

func (s *Starter) withSessionStore(ctx context.Context, descriptor session.SessionDescriptor, callback func(context.Context, *session.Store) error) error {
	return s.runtimeAuthority.WithSessionStore(ctx, descriptor, callback)
}

func (s *Starter) cleanupSession(ctx context.Context, descriptor session.SessionDescriptor) error {
	return errors.Join(s.withSessionStore(ctx, descriptor, func(_ context.Context, store *session.Store) error { return store.RemoveDurable() }), s.metadata.DeleteSessionRecordByID(ctx, descriptor.SessionID().String()))
}

func (s *Starter) Close() error {
	if s == nil || s.closed.Swap(true) {
		return nil
	}
	return s.runtimeAuthority.StopWorkflowExecutions(context.Background())
}

func workflowCompletionModeNeedsProviderCapabilities(selection workflowruntime.CompletionModeSelection) bool {
	return selection.ConfiguredMode == config.WorkflowCompletionModeStructuredOutput || ((selection.ConfiguredMode == config.WorkflowCompletionModeAuto || selection.ConfiguredMode == "") && selection.ShellAvailable && !selection.HasContinueSessionEdge)
}

func (s *Starter) workflowProviderCapabilities(ctx context.Context, plan launch.SessionPlan, client llm.Client) (llm.ProviderCapabilities, llm.Client, error) {
	if caps, ok := llm.ProviderCapabilitiesFromLockedOrOverride(plan.Locked, plan.ActiveSettings.ProviderCapabilities); ok {
		return caps, client, nil
	}
	if client == nil {
		next, err := s.newWorkflowProviderClient(ctx, plan)
		if err != nil {
			return llm.ProviderCapabilities{}, nil, err
		}
		client = next
	}
	provider, ok := client.(llm.ProviderCapabilitiesClient)
	if !ok {
		return llm.ProviderCapabilities{}, client, fmt.Errorf("provider capabilities are unavailable for client %T", client)
	}
	caps, err := provider.ProviderCapabilities(ctx)
	return caps, client, err
}

func (s *Starter) newWorkflowProviderClient(ctx context.Context, plan launch.SessionPlan) (llm.Client, error) {
	active := plan.ActiveSettings
	if s.runtimeClientFactory != nil {
		providerSettings := runtimewire.RuntimeClientProviderSettings{
			Model:               active.Model,
			ProviderOverride:    active.ProviderOverride,
			OpenAIBaseURL:       active.OpenAIBaseURL,
			ModelVerbosity:      active.ModelVerbosity,
			ProviderIdentifier:  active.ProviderIdentifier,
			Store:               active.Store,
			ContextWindowTokens: active.ModelContextWindow,
			Auth:                "inherit",
		}
		if caps, configured := llm.ProviderCapabilitiesFromLockedOrOverride(plan.Locked, active.ProviderCapabilities); configured {
			providerSettings.ProviderCapabilitiesOverride = &caps
		}
		client, err := s.runtimeClientFactory.NewRuntimeClient(ctx, runtimewire.RuntimeClientRequest{
			Purpose:          runtimewire.RuntimeClientPurposeWorkflow,
			SessionID:        plan.Descriptor.SessionID().String(),
			ActiveSettings:   active,
			EnabledTools:     append([]toolspec.ID(nil), plan.EnabledTools...),
			WorkspaceRoot:    plan.WorkspaceRoot,
			Sources:          maps.Clone(plan.Source.Sources),
			ProviderSettings: providerSettings,
		})
		if err != nil {
			return nil, err
		}
		if client == nil {
			return nil, errors.New("runtime client factory returned nil workflow client")
		}
		return client, nil
	}
	var authProvider llm.AuthHeaderProvider
	if s.authManager != nil {
		authProvider = s.authManager
	}
	return llm.NewProviderClient(llm.ProviderClientOptions{Provider: llm.Provider(active.ProviderOverride), Model: active.Model, Auth: authProvider, HTTPClient: llm.NewHTTPClient(time.Duration(active.Timeouts.ModelRequestSeconds) * time.Second), OpenAIBaseURL: active.OpenAIBaseURL, ModelVerbosity: string(active.ModelVerbosity), ProviderIdentifier: &active.ProviderIdentifier, Store: active.Store, ContextWindowTokens: active.ModelContextWindow})
}

func workflowPromptOverrides(role string) serverapi.RunPromptOverrides {
	if workflow.IsDefaultAgentRole(role) {
		role = workflow.DefaultAgentRole
	}
	if strings.TrimSpace(role) == "" {
		return serverapi.RunPromptOverrides{}
	}
	return serverapi.RunPromptOverrides{AgentRole: &role}
}

func (s *Starter) cloneSourceSessionForFanout(containerDir, sourceSessionID string) (string, error) {
	id, err := runtimeids.ParseSessionID(sourceSessionID)
	if err != nil {
		return "", err
	}
	descriptor, err := session.NewScopedOpenSessionDescriptor(id, containerDir)
	if err != nil {
		return "", err
	}
	var cloneID string
	err = s.withSessionStore(context.Background(), descriptor, func(_ context.Context, source *session.Store) error {
		log, err := source.MaterializeEventLog()
		if err != nil {
			return err
		}
		clone, err := session.CloneSession(log, "", sessioncontract.SessionCategorySubagent)
		if err != nil {
			return err
		}
		cloneID = clone.Meta().SessionID
		return nil
	})
	return cloneID, err
}

func (s *Starter) validateRole(role string) error {
	if workflow.IsDefaultAgentRole(role) || config.LookupSubagentRole(s.cfg.Settings, strings.TrimSpace(role)).Status == config.SubagentRoleLookupPresent {
		return nil
	}
	return fmt.Errorf("workflow validation failed: [%s]", workflow.CodeAgentRoleMissing)
}

type executionPromptAwaiter struct {
	authority *sessionruntime.Authority
	scope     sessionruntime.ExecutionScope
}

func (a executionPromptAwaiter) AwaitPromptResponse(ctx context.Context, _ string, req askquestion.AskQuestionRequest) (askquestion.AskQuestionResponse, error) {
	return a.authority.AwaitPromptResponse(ctx, a.scope.ID(), req)
}

func currentNodeQuestionContext(input workflowstore.CurrentNodeStartContext, sessionID string) workflowattention.TaskQuestionContext {
	return workflowattention.TaskQuestionContext{Task: input.Task, CurrentNode: input.CurrentNode.Reference, SessionID: sessionID}
}

func (s *Starter) handleCurrentNodeAsk(ctx context.Context, awaiter workflowattention.QuestionAwaiter, input workflowstore.CurrentNodeStartContext, sessionID string, askReq askquestion.AskQuestionRequest) (askquestion.AskQuestionResponse, error) {
	context := currentNodeQuestionContext(input, sessionID)
	if askReq.Approval {
		return workflowattention.HandleTaskApprovalQuestion(ctx, awaiter, s.attention, workflowattention.TaskQuestionRequest{Context: context, Question: askReq})
	}
	return workflowattention.HandleTaskQuestion(ctx, awaiter, s.attention, workflowattention.TaskQuestionRequest{Context: context, Question: askReq})
}

func workflowRuntimeEnabledTools(enabled []toolspec.ID) []toolspec.ID {
	return append([]toolspec.ID(nil), enabled...)
}

func toolIDEnabled(enabled []toolspec.ID, want toolspec.ID) bool {
	for _, id := range enabled {
		if id == want {
			return true
		}
	}
	return false
}

func BuildCurrentSessionTaskInstructions(input workflowstore.CurrentNodeStartContext) (workflowruntime.TaskInstructions, error) {
	source := ""
	if input.SourceSessionID != nil {
		source = input.SourceSessionID.String()
	}
	return buildWorkflowTaskInstructions(workflowPromptInput{Task: input.Task, Workflow: input.Workflow, Node: input.Node, CurrentNode: input.CurrentNode.Reference, ContextMode: input.ContextMode, SourceSessionID: source, TransitionOptions: input.TransitionOptions, TransitionIDs: input.TransitionIDs, PromptTemplate: input.PromptTemplate, ParameterValues: input.ParameterValues, PriorValues: input.CurrentNode.PriorValues})
}

type workflowPromptInput struct {
	Task              workflowstore.TaskRecord
	Workflow          workflowstore.WorkflowRecord
	Node              workflowstore.NodeRecord
	CurrentNode       workflow.CurrentNodeReference
	ContextMode       workflow.ContextMode
	SourceSessionID   string
	TransitionOptions []workflowstore.TransitionOption
	TransitionIDs     []string
	PromptTemplate    string
	ParameterValues   map[string]string
	PriorValues       workflow.MaterializedPriorValues
}

func buildWorkflowTaskInstructions(input workflowPromptInput) (workflowruntime.TaskInstructions, error) {
	prompt, err := renderWorkflowPrompt(input.PromptTemplate, input)
	if err != nil {
		return workflowruntime.TaskInstructions{}, err
	}
	shortID := input.Task.ShortID
	if shortID == "" {
		shortID = string(input.Task.ID)
	}
	return workflowruntime.TaskInstructions{CurrentNode: input.CurrentNode, TaskShortID: shortID, TaskTitle: input.Task.Title, TaskBody: input.Task.Body, WorkflowID: input.Task.WorkflowID, WorkflowName: strings.TrimSpace(input.Workflow.Name), NodeKey: string(input.Node.Key), NodeDisplayName: input.Node.DisplayName, ContextMode: string(input.ContextMode), SourceSessionID: input.SourceSessionID, Transitions: workflowInstructionTransitions(input.TransitionOptions, input.TransitionIDs), NodePrompt: prompt}, nil
}

func workflowTransitions(options []workflowstore.TransitionOption, ids []string) []prompts.WorkflowTransition {
	out := make([]prompts.WorkflowTransition, 0, len(options))
	for _, option := range options {
		if strings.TrimSpace(option.ID) != "" {
			out = append(out, prompts.WorkflowTransition{ID: option.ID, DisplayName: option.DisplayName, Description: option.Description})
		}
	}
	if len(out) != 0 {
		return out
	}
	for _, id := range ids {
		if strings.TrimSpace(id) != "" {
			out = append(out, prompts.WorkflowTransition{ID: id})
		}
	}
	return out
}

func workflowInstructionTransitions(options []workflowstore.TransitionOption, ids []string) []workflowruntime.TransitionInstruction {
	transitions := workflowTransitions(options, ids)
	out := make([]workflowruntime.TransitionInstruction, 0, len(transitions))
	for _, transition := range transitions {
		out = append(out, workflowruntime.TransitionInstruction{ID: transition.ID, DisplayName: transition.DisplayName, Description: transition.Description})
	}
	return out
}

func workflowCompletionTransitions(options []workflowstore.TransitionOption, ids []string) []workflowruntime.CompletionTransition {
	out := make([]workflowruntime.CompletionTransition, 0, len(options))
	for _, option := range options {
		if strings.TrimSpace(option.ID) != "" {
			out = append(out, workflowruntime.CompletionTransition{ID: option.ID, DisplayName: option.DisplayName, Description: option.Description, Parameters: append([]workflow.Parameter(nil), option.Parameters...)})
		}
	}
	if len(out) != 0 {
		return out
	}
	for _, id := range ids {
		if strings.TrimSpace(id) != "" {
			out = append(out, workflowruntime.CompletionTransition{ID: id})
		}
	}
	return out
}

type nodePromptTemplateData struct {
	TaskId, TaskShortId, TaskTitle, TaskBody, NodeId, NodeKey, NodeDisplayName string
	Params                                                                     map[string]promptParameterNamespace
}

const currentParameterValueKey = "\x00current"

type promptParameterNamespace map[string]string

func (n promptParameterNamespace) String() string { return n[currentParameterValueKey] }

func renderWorkflowPrompt(text string, input workflowPromptInput) (string, error) {
	if strings.TrimSpace(text) == "" {
		return "", nil
	}
	tmpl, err := template.New("workflow transition prompt").Option("missingkey=error").Parse(text)
	if err != nil {
		return "", fmt.Errorf("parse workflow transition prompt template: %w", err)
	}
	var out strings.Builder
	err = tmpl.Execute(&out, nodePromptTemplateData{
		TaskId:          string(input.Task.ID),
		TaskShortId:     input.Task.ShortID,
		TaskTitle:       input.Task.Title,
		TaskBody:        input.Task.Body,
		NodeId:          string(input.Node.ID),
		NodeKey:         string(input.Node.Key),
		NodeDisplayName: input.Node.DisplayName,
		Params:          promptParameterData(input.ParameterValues, input.PriorValues.TransitionParameters),
	})
	return out.String(), err
}

func promptParameterData(current map[string]string, prior map[workflow.ModelKey]map[string]string) map[string]promptParameterNamespace {
	out := map[string]promptParameterNamespace{workflow.RuntimePromptParameterCommentary: {currentParameterValueKey: ""}}
	for transition, values := range prior {
		namespace := out[string(transition)]
		if namespace == nil {
			namespace = promptParameterNamespace{}
		}
		for key, value := range values {
			namespace[key] = value
		}
		out[string(transition)] = namespace
	}
	for key, value := range current {
		namespace := out[key]
		if namespace == nil {
			namespace = promptParameterNamespace{}
		}
		namespace[currentParameterValueKey] = value
		out[key] = namespace
	}
	return out
}

var _ workflowexecution.CurrentNodeRunner = (*Starter)(nil)
