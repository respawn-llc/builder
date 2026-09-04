package workflowrunner

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"path/filepath"
	"strings"
	"sync"
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
	"core/server/workflowstore"
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
	taskAwarenessSource  workflowruntime.TaskAwarenessSource
	closed               atomic.Bool
}

type StarterOptions struct {
	RuntimeClientFactory runtimewire.RuntimeClientFactory
	RuntimeAuthority     *sessionruntime.Authority
	TaskDependencies     TaskDependencyCounter
}

func NewStarter(cfg config.App, metadataStore *metadata.Store, store RuntimeStore, authManager *auth.Manager, attention WorkflowAttentionRegistry, opts StarterOptions) (*Starter, error) {
	if strings.TrimSpace(cfg.PersistenceRoot) == "" {
		return nil, errors.New("workflow runtime persistence root is required")
	}
	if metadataStore == nil || store == nil || opts.RuntimeAuthority == nil || opts.TaskDependencies == nil {
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
		taskAwarenessSource:  taskAwarenessSource,
	}, nil
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
		return nil, fmt.Errorf("Script current node %v has no Agent assignment", reference)
	}
	if input.Node.Kind != workflow.NodeKindAgent {
		return nil, fmt.Errorf("current node %v is not executable", reference)
	}
	steer, err := s.prepareCurrentNodeAgentAssignment(ctx, input, true)
	if err != nil {
		return nil, err
	}
	if err := steer.Prepare(ctx); err != nil {
		return nil, err
	}
	return steer, nil
}

func (s *Starter) prepareCurrentNodeAgentAssignment(
	ctx context.Context,
	input workflowstore.CurrentNodeStartContext,
	bindSession bool,
) (*currentNodeAgentAssignmentSteer, error) {
	selection, err := currentNodeAgentExecutionSelection(input)
	if err != nil {
		return nil, err
	}
	if err := s.validateRole(selection.Assignee); err != nil {
		return nil, err
	}
	prepared, err := s.prepareCurrentNodeAgentSession(ctx, input, false, false, bindSession)
	if err != nil {
		return nil, err
	}
	assignment, err := s.currentNodeAgentAssignment(ctx, input, prepared)
	if err != nil {
		return nil, prepared.cleanup(err)
	}
	return &currentNodeAgentAssignmentSteer{
		reference:  input.CurrentNode.Reference,
		input:      input,
		prepared:   prepared,
		starter:    s,
		assignment: assignment,
		ready:      make(chan struct{}),
	}, nil
}

type currentNodeAgentAssignmentSteer struct {
	reference  workflow.CurrentNodeReference
	input      workflowstore.CurrentNodeStartContext
	prepared   preparedCurrentNodeAgentSession
	starter    *Starter
	assignment runtime.WorkflowAssignment
	ready      chan struct{}
	mu         sync.Mutex
	started    bool
	settled    bool
	receipt    session.CommitReceipt
	err        error
}

func (s *currentNodeAgentAssignmentSteer) Prepare(ctx context.Context) error {
	if s == nil {
		return errors.New("current node agent assignment steer is required")
	}
	s.mu.Lock()
	if s.started {
		ready := s.ready
		s.mu.Unlock()
		select {
		case <-ready:
			return s.err
		case <-ctx.Done():
			return context.Cause(ctx)
		}
	}
	if s.settled {
		err := s.err
		s.mu.Unlock()
		return err
	}
	s.started = true
	s.mu.Unlock()

	var steer runtime.WorkflowAssignmentSteer
	admission, err := s.starter.runtimeAuthority.WithDormantSessionStore(ctx, s.prepared.plan.Descriptor, func(_ context.Context, store *session.Store) error {
		var steerErr error
		steer, steerErr = runtime.SteerPersistedWorkflowAssignment(
			store,
			s.assignment,
			runtime.PersistedWorkflowAssignmentContext{
				Workdir:                 s.prepared.root.EffectiveRoot(),
				GlobalConfigDir:         s.starter.cfg.PersistenceRoot,
				Model:                   s.prepared.plan.ActiveSettings.Model,
				ThinkingLevel:           s.prepared.plan.ActiveSettings.ThinkingLevel,
				SkillPolicy:             config.ResolveSkillPolicy(s.prepared.plan.ActiveSettings),
				SubagentCatalogSettings: s.prepared.plan.ActiveSettings,
				EnabledTools:            workflowRuntimeEnabledTools(s.prepared.plan.EnabledTools),
			},
		)
		return steerErr
	})
	if err == nil && admission.RuntimeAvailable {
		err = s.starter.runtimeAuthority.WithCurrentRuntime(ctx, s.prepared.plan.Descriptor.SessionID(), func(_ context.Context, engine *runtime.Engine) error {
			selection, selectionErr := currentNodeAgentExecutionSelection(s.input)
			if selectionErr != nil {
				return selectionErr
			}
			snapshot, snapshotErr := runtime.NewWorkflowAssignmentSnapshot(s.assignment)
			if snapshotErr != nil {
				return snapshotErr
			}
			thinkingMutation := workflowThinkingMutationFor(s.input, selection)
			switch thinkingMutation.Kind() {
			case launch.WorkflowThinkingMutationSet:
				snapshot = snapshot.WithThinkingLevel(string(thinkingMutation.Value()))
			case launch.WorkflowThinkingMutationClear:
				snapshot = snapshot.WithThinkingLevel("")
			}
			var steerErr error
			steer, steerErr = engine.SteerWorkflowAssignmentSnapshot(snapshot)
			return steerErr
		})
	}
	var receipt session.CommitReceipt
	if err == nil {
		receipt, err = steer.Wait(ctx)
	}
	if !receipt.Committed {
		err = s.prepared.cleanup(err)
	} else {
		var bindingErr error
		if s.prepared.bindSession != nil {
			bindingErr = s.prepared.bindSession(context.WithoutCancel(ctx))
		}
		if bindingErr != nil {
			err = s.prepared.cleanup(errors.Join(err, bindingErr))
		} else {
			s.prepared.cleanup = func(err error) error { return err }
		}
	}
	s.mu.Lock()
	s.receipt = receipt
	s.err = err
	s.settled = true
	close(s.ready)
	s.mu.Unlock()
	return err
}

func (s *currentNodeAgentAssignmentSteer) Wait(ctx context.Context) (session.CommitReceipt, error) {
	if s == nil {
		return session.CommitReceipt{}, errors.New("current node agent assignment steer is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-s.ready:
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.receipt, s.err
	case <-ctx.Done():
		return session.CommitReceipt{}, context.Cause(ctx)
	}
}

func (s *currentNodeAgentAssignmentSteer) SessionID() runtimeids.SessionID {
	if s == nil {
		panic("current node agent assignment steer is required")
	}
	return s.prepared.plan.Descriptor.SessionID()
}

func (s *currentNodeAgentAssignmentSteer) abortUnprepared(cause error) error {
	s.mu.Lock()
	if s.started || s.settled {
		s.mu.Unlock()
		return cause
	}
	s.settled = true
	s.err = cause
	close(s.ready)
	s.mu.Unlock()
	return s.prepared.cleanup(cause)
}

type manualMoveAssignmentRestoration struct {
	projectID           string
	snapshot            runtime.WorkflowAssignmentSnapshot
	assignmentCommitted bool
}

func (s *Starter) PrepareManualMoveAssignments(
	ctx context.Context,
	inputs []workflowstore.CurrentNodeStartContext,
) (
	workflowstore.ManualMoveTargetAssignmentPreparation,
	map[workflow.CurrentNodeReferenceKey]workflowexecution.CurrentNodeAssignmentSteer,
	error,
) {
	assignments := make([]workflowstore.ManualMoveTargetAssignment, 0, len(inputs))
	steers := make(map[workflow.CurrentNodeReferenceKey]workflowexecution.CurrentNodeAssignmentSteer, len(inputs))
	cleanups := make([]func(error) error, 0, len(inputs))
	restorations := make(map[runtimeids.SessionID]manualMoveAssignmentRestoration)
	var diagnostics []error
	abort := func(cause error) error {
		for _, steer := range steers {
			assignment, ok := steer.(*currentNodeAgentAssignmentSteer)
			if ok {
				cause = assignment.abortUnprepared(cause)
			}
		}
		for _, cleanup := range cleanups {
			cause = cleanup(cause)
		}
		for sessionID, restoration := range restorations {
			cause = errors.Join(cause, s.restoreManualMoveAssignmentSnapshot(
				ctx,
				restoration.projectID,
				sessionID,
				restoration.snapshot,
				restoration.assignmentCommitted,
			))
		}
		return cause
	}
	for _, input := range inputs {
		if input.Node.Kind == workflow.NodeKindScript && input.CurrentNode.AgentExecutionSelection == nil {
			continue
		}
		if input.Node.Kind != workflow.NodeKindAgent || input.CurrentNode.AgentExecutionSelection == nil {
			return workflowstore.ManualMoveTargetAssignmentPreparation{}, nil, abort(
				fmt.Errorf("current node %v execution shape is inconsistent", input.CurrentNode.Reference),
			)
		}
		var priorAssignment *runtime.WorkflowAssignmentSnapshot
		if input.CurrentNode.SessionID != nil {
			policy, policyErr := resolveCurrentNodeSessionPolicy(input)
			if policyErr != nil {
				return workflowstore.ManualMoveTargetAssignmentPreparation{}, nil, abort(policyErr)
			}
			if !policy.cloneRetainedSession {
				snapshot, found, snapshotErr := s.captureManualMoveAssignmentSnapshot(
					ctx,
					input.Task.ProjectID,
					*input.CurrentNode.SessionID,
				)
				if snapshotErr != nil {
					return workflowstore.ManualMoveTargetAssignmentPreparation{}, nil, abort(snapshotErr)
				}
				if !found {
					return workflowstore.ManualMoveTargetAssignmentPreparation{}, nil, abort(
						fmt.Errorf("retained Session %q has no prior workflow assignment", input.CurrentNode.SessionID),
					)
				}
				priorAssignment = &snapshot
				restorations[*input.CurrentNode.SessionID] = manualMoveAssignmentRestoration{
					projectID: input.Task.ProjectID,
					snapshot:  snapshot,
				}
			}
		}
		key, err := input.CurrentNode.Reference.Key()
		if err != nil {
			return workflowstore.ManualMoveTargetAssignmentPreparation{}, nil, abort(err)
		}
		steer, err := s.prepareCurrentNodeAgentAssignment(ctx, input, false)
		if err != nil {
			return workflowstore.ManualMoveTargetAssignmentPreparation{}, nil, abort(err)
		}
		steers[key] = steer
		cleanup := steer.prepared.cleanup
		prepareErr := steer.Prepare(ctx)
		receipt, waitErr := steer.Wait(ctx)
		if !receipt.Committed {
			return workflowstore.ManualMoveTargetAssignmentPreparation{}, nil, abort(errors.Join(
				prepareErr,
				waitErr,
				errors.New("Manual Move workflow assignment was not committed"),
			))
		}
		cleanups = append(cleanups, cleanup)
		if assignmentErr := errors.Join(prepareErr, waitErr); assignmentErr != nil {
			diagnostics = append(diagnostics, assignmentErr)
		}
		assignments = append(assignments, workflowstore.ManualMoveTargetAssignment{
			CurrentNode: input.CurrentNode.Reference,
			SessionID:   steer.SessionID(),
		})
		if priorAssignment != nil {
			restoration := restorations[steer.SessionID()]
			restoration.assignmentCommitted = true
			restorations[steer.SessionID()] = restoration
		}
	}
	return workflowstore.ManualMoveTargetAssignmentPreparation{
		Assignments: assignments,
		Diagnostic:  errors.Join(diagnostics...),
		Abort:       abort,
	}, steers, nil
}

func (s *Starter) captureManualMoveAssignmentSnapshot(
	ctx context.Context,
	projectID string,
	sessionID runtimeids.SessionID,
) (runtime.WorkflowAssignmentSnapshot, bool, error) {
	descriptor, err := session.NewScopedOpenSessionDescriptor(
		sessionID,
		filepath.Join(s.cfg.PersistenceRoot, "projects", projectID, "sessions"),
	)
	if err != nil {
		return runtime.WorkflowAssignmentSnapshot{}, false, err
	}
	var (
		snapshot runtime.WorkflowAssignmentSnapshot
		found    bool
	)
	err = s.withSessionStore(ctx, descriptor, func(_ context.Context, store *session.Store) error {
		var captureErr error
		snapshot, found, captureErr = runtime.CapturePersistedWorkflowAssignment(store)
		return captureErr
	})
	if err == nil {
		runtimeErr := s.runtimeAuthority.WithCurrentRuntime(
			ctx,
			sessionID,
			func(_ context.Context, engine *runtime.Engine) error {
				snapshot = snapshot.WithThinkingLevel(engine.ThinkingLevel())
				return nil
			},
		)
		if runtimeErr != nil && !errors.Is(runtimeErr, serverapi.ErrRuntimeUnavailable) {
			err = runtimeErr
		}
	}
	return snapshot, found, err
}

func (s *Starter) restoreManualMoveAssignmentSnapshot(
	ctx context.Context,
	projectID string,
	sessionID runtimeids.SessionID,
	snapshot runtime.WorkflowAssignmentSnapshot,
	assignmentCommitted bool,
) error {
	restoreCtx := context.WithoutCancel(ctx)
	if !assignmentCommitted {
		runtimeErr := s.runtimeAuthority.WithCurrentRuntime(
			restoreCtx,
			sessionID,
			func(_ context.Context, engine *runtime.Engine) error {
				return engine.RestoreWorkflowAssignmentSnapshotThinking(snapshot)
			},
		)
		if runtimeErr != nil && !errors.Is(runtimeErr, serverapi.ErrRuntimeUnavailable) {
			return fmt.Errorf("restore Manual Move Session %q thinking: %w", sessionID, runtimeErr)
		}
		return nil
	}
	descriptor, err := session.NewScopedOpenSessionDescriptor(
		sessionID,
		filepath.Join(s.cfg.PersistenceRoot, "projects", projectID, "sessions"),
	)
	if err != nil {
		return err
	}
	var steer runtime.WorkflowAssignmentSteer
	admission, steerErr := s.runtimeAuthority.WithDormantSessionStore(
		restoreCtx,
		descriptor,
		func(_ context.Context, store *session.Store) error {
			var err error
			steer, err = runtime.SteerPersistedWorkflowAssignmentSnapshot(store, snapshot)
			return err
		},
	)
	if steerErr == nil && admission.RuntimeAvailable {
		steerErr = s.runtimeAuthority.WithCurrentRuntime(
			restoreCtx,
			sessionID,
			func(_ context.Context, engine *runtime.Engine) error {
				var err error
				steer, err = engine.SteerWorkflowAssignmentSnapshot(snapshot)
				return err
			},
		)
	}
	if steerErr != nil {
		return fmt.Errorf("restore Manual Move Session %q assignment: %w", sessionID, steerErr)
	}
	receipt, waitErr := steer.Wait(restoreCtx)
	if !receipt.Committed {
		return errors.Join(
			fmt.Errorf("restore Manual Move Session %q assignment was not committed", sessionID),
			waitErr,
		)
	}
	return waitErr
}

func (s *Starter) StartAgentCurrentNode(
	ctx context.Context,
	reference workflow.CurrentNodeReference,
	taskPromptDelivery workflowruntime.TaskPromptDelivery,
	assignmentSteer workflowexecution.CurrentNodeAssignmentSteer,
	continuation *workflowexecution.WorkflowSessionContinuation,
	onRetire func(),
	controller workflowruntime.Controller,
) (sessionruntime.ExecutionHandle, error) {
	if s == nil || s.closed.Load() {
		return nil, errors.New("workflow runtime starter closed")
	}
	if assignmentSteer == nil && taskPromptDelivery == workflowruntime.TaskPromptDeliveryResume {
		input, err := s.store.ResolveCurrentNodeStartContext(ctx, reference)
		if err != nil {
			return nil, err
		}
		prepared, err := s.prepareCurrentNodeAgentSession(ctx, input, false, true, true)
		if err != nil {
			return nil, err
		}
		return s.startCurrentNodeAgent(
			ctx,
			input,
			prepared,
			taskPromptDelivery,
			assignmentSteer,
			continuation,
			onRetire,
			controller,
		)
	}
	assignment, ok := assignmentSteer.(*currentNodeAgentAssignmentSteer)
	if !ok || assignment == nil || assignment.starter != s || !assignment.reference.Equal(reference) {
		return nil, fmt.Errorf("current node %v received incompatible assignment %T", reference, assignmentSteer)
	}
	return s.startCurrentNodeAgent(
		ctx,
		assignment.input,
		assignment.prepared,
		taskPromptDelivery,
		assignmentSteer,
		continuation,
		onRetire,
		controller,
	)
}

func (s *Starter) startCurrentNodeAgent(
	ctx context.Context,
	input workflowstore.CurrentNodeStartContext,
	prepared preparedCurrentNodeAgentSession,
	taskPromptDelivery workflowruntime.TaskPromptDelivery,
	assignmentSteer workflowexecution.CurrentNodeAssignmentSteer,
	continuation *workflowexecution.WorkflowSessionContinuation,
	onRetire func(),
	controller workflowruntime.Controller,
) (sessionruntime.ExecutionHandle, error) {
	reference := input.CurrentNode.Reference
	var err error
	if prepared.client == nil {
		prepared.client, err = s.newWorkflowProviderClient(ctx, prepared.plan)
		if err != nil {
			return nil, prepared.cleanup(err)
		}
	}
	if err := s.applyCurrentNodeSessionExecutionTarget(ctx, input, prepared.plan.Descriptor); err != nil {
		return nil, prepared.cleanup(err)
	}
	resource := sessionruntime.AgentResourceSelection(sessionruntime.CurrentAgentResource{})
	var replacementPlan *sessionruntime.AgentRuntimePlan
	if prepared.replaceResource {
		runtimePlan, planErr := s.buildCurrentNodeAgentRuntimePlan(input, prepared, continuation)
		if planErr != nil {
			return nil, prepared.cleanup(planErr)
		}
		resource = sessionruntime.ReplaceAgentResource{}
		replacementPlan = &runtimePlan
	} else {
		err = s.runtimeAuthority.WithCurrentRuntime(ctx, prepared.plan.Descriptor.SessionID(), func(context.Context, *runtime.Engine) error { return nil })
		if errors.Is(err, serverapi.ErrRuntimeUnavailable) {
			runtimePlan, planErr := s.buildCurrentNodeAgentRuntimePlan(input, prepared, continuation)
			if planErr != nil {
				return nil, prepared.cleanup(planErr)
			}
			resource = sessionruntime.OpenAgentResource{}
			replacementPlan = &runtimePlan
		} else if err != nil {
			return nil, prepared.cleanup(err)
		}
	}
	runtimeConfig, err := BuildCurrentNodeRuntimeConfig(
		input,
		runtimeids.NewExecutionScopeID(),
		taskPromptDelivery,
		prepared.mode,
		s.cfg.Settings.Workflow.MaxInvalidCompletionAttempts,
		prepared.plan.ActiveSettings.Workflow.UseRequiredToolCalls,
		controller,
		s.taskAwarenessSource,
	)
	if err != nil {
		return nil, prepared.cleanup(err)
	}
	var onSelectedResult func(sessionruntime.ExecutionResult, error)
	var onSelectedProgress func(runtime.Event)
	if continuation != nil {
		onSelectedResult = continuation.RecordExact
		onSelectedProgress = continuation.PublishEvent
	}
	handle, err := s.runtimeAuthority.StartAgentExecution(
		ctx,
		sessionruntime.AgentExecutionRequest{
			Descriptor: prepared.plan.Descriptor,
			Runtime:    replacementPlan,
			Workflow: &sessionruntime.WorkflowAgentExecution{
				Reference: sessionruntime.WorkflowExecutionRef{
					ProjectID:   input.Task.ProjectID,
					WorkflowID:  input.Workflow.ID,
					CurrentNode: reference,
				},
				Config:                   runtimeConfig,
				AssignmentAlreadyHandled: assignmentSteer != nil,
				OnRetire:                 onRetire,
				OnSelectedResult:         onSelectedResult,
				OnSelectedProgress:       onSelectedProgress,
			},
			Resource: resource,
			Ask: func(askCtx context.Context, scope sessionruntime.ExecutionScope, askReq askquestion.AskQuestionRequest) (askquestion.AskQuestionResolution, error) {
				return s.handleCurrentNodeAsk(askCtx, executionPromptAwaiter{authority: s.runtimeAuthority, scope: scope}, input, prepared.plan.Descriptor.SessionID().String(), askReq)
			},
			Runner: s.currentNodeAgentRunner(input, continuation, controller),
		},
	)
	if err != nil {
		return nil, prepared.cleanup(err)
	}
	prepared.cleanup = func(err error) error { return err }
	return handle, nil
}

func (s *Starter) currentNodeAgentAssignment(
	ctx context.Context,
	input workflowstore.CurrentNodeStartContext,
	prepared preparedCurrentNodeAgentSession,
) (runtime.WorkflowAssignment, error) {
	instructions, err := BuildCurrentSessionTaskInstructions(input)
	if err != nil {
		return runtime.WorkflowAssignment{}, err
	}
	awareness, err := s.taskAwarenessSource.TaskAwareness(ctx, input.Task.ID)
	if err != nil {
		return runtime.WorkflowAssignment{}, err
	}
	return runtime.WorkflowAssignment{
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
	}, nil
}

func currentNodeActiveRuntimeTarget(input workflowstore.CurrentNodeStartContext) (runtimeids.SessionID, bool) {
	if input.Node.Kind != workflow.NodeKindAgent ||
		input.ContextMode != workflow.ContextModeContinueSession ||
		input.EnteringEdge.RequiresApproval ||
		workflow.CanonicalContextSource(input.EnteringEdge.ContextSource).Kind != workflow.ContextSourceImmediateSource ||
		input.SourceSessionID == nil {
		return runtimeids.SessionID{}, false
	}
	policy, err := resolveCurrentNodeSessionPolicy(input)
	if err != nil || policy.cloneRetainedSession {
		return runtimeids.SessionID{}, false
	}
	return *input.SourceSessionID, true
}

type preparedCurrentNodeAgentSession struct {
	root            workflowstore.ExecutionRoot
	plan            launch.SessionPlan
	client          llm.Client
	mode            workflowruntime.CompletionMode
	replaceResource bool
	bindSession     func(context.Context) error
	cleanup         func(error) error
}

func (s *Starter) prepareCurrentNodeAgentSession(
	ctx context.Context,
	input workflowstore.CurrentNodeStartContext,
	requireRuntimeClient bool,
	sessionPrepared bool,
	bindSession bool,
) (preparedCurrentNodeAgentSession, error) {
	root, err := requireCurrentNodeExecutionRoot(input)
	if err != nil {
		return preparedCurrentNodeAgentSession{}, err
	}
	var retainedSnapshot *session.PromptFacingMetadataSnapshot
	var retainedDescriptor session.SessionDescriptor
	restoreRetainedMetadata := func(cause error) error {
		if retainedSnapshot == nil {
			return cause
		}
		restoreErr := s.withSessionStore(
			context.WithoutCancel(ctx),
			retainedDescriptor,
			func(_ context.Context, store *session.Store) error {
				return store.RestorePromptFacingMetadata(*retainedSnapshot)
			},
		)
		return errors.Join(cause, restoreErr)
	}
	policy, err := resolveCurrentNodeSessionPolicy(input)
	if err != nil {
		return preparedCurrentNodeAgentSession{}, err
	}
	if !sessionPrepared && input.CurrentNode.SessionID != nil && !policy.cloneRetainedSession {
		retainedDescriptor, err = session.NewScopedOpenSessionDescriptor(
			*input.CurrentNode.SessionID,
			filepath.Join(s.cfg.PersistenceRoot, "projects", input.Task.ProjectID, "sessions"),
		)
		if err != nil {
			return preparedCurrentNodeAgentSession{}, err
		}
		if err := s.withSessionStore(ctx, retainedDescriptor, func(_ context.Context, store *session.Store) error {
			snapshot := store.PromptFacingMetadataSnapshot()
			retainedSnapshot = &snapshot
			return nil
		}); err != nil {
			return preparedCurrentNodeAgentSession{}, err
		}
	}
	plan, disposable, err := s.planCurrentNodeSession(ctx, input, root, sessionPrepared)
	if err != nil {
		return preparedCurrentNodeAgentSession{}, restoreRetainedMetadata(err)
	}
	sessionBound := false
	cleanup := func(err error) error {
		if retainedSnapshot != nil {
			return restoreRetainedMetadata(err)
		}
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
	var bindPreparedSession func(context.Context) error
	if sessionPrepared {
		if err := s.store.ValidateCurrentNodeSessionBinding(
			ctx,
			plan.Descriptor.SessionID(),
			input.CurrentNode.Reference,
		); err != nil {
			return preparedCurrentNodeAgentSession{}, cleanup(err)
		}
	} else if bindSession {
		bindPreparedSession = func(bindCtx context.Context) error {
			if _, err := s.store.BindSessionToCurrentNode(bindCtx, workflowstore.CurrentNodeSessionBindingRequest{
				Association: workflowstore.TaskSessionAssociationRequest{
					SessionID:    plan.Descriptor.SessionID(),
					CurrentNode:  input.CurrentNode.Reference,
					AssociatedAt: time.Now().UTC(),
				},
				ExpectedCurrentSessionID: input.SourceSessionID,
			}); err != nil {
				return err
			}
			sessionBound = true
			return nil
		}
	}
	prepared := preparedCurrentNodeAgentSession{
		root: root, plan: plan, client: client, mode: mode,
		bindSession: bindPreparedSession, cleanup: cleanup,
	}
	runtimeErr := s.runtimeAuthority.WithCurrentRuntime(
		ctx,
		plan.Descriptor.SessionID(),
		func(_ context.Context, engine *runtime.Engine) error {
			prepared.replaceResource =
				engine.CompactionMode() != string(plan.ActiveSettings.CompactionMode)
			return nil
		},
	)
	if runtimeErr != nil && !errors.Is(runtimeErr, serverapi.ErrRuntimeUnavailable) {
		return preparedCurrentNodeAgentSession{}, cleanup(runtimeErr)
	}
	return prepared, nil
}

func (s *Starter) buildCurrentNodeAgentRuntimePlan(
	input workflowstore.CurrentNodeStartContext,
	prepared preparedCurrentNodeAgentSession,
	continuation *workflowexecution.WorkflowSessionContinuation,
) (sessionruntime.AgentRuntimePlan, error) {
	projectWorkspaceBoundary := prepared.plan.ProjectWorkspaceBoundary.Clone()
	filesystemContext, err := runtimewire.NewFilesystemContext(prepared.root.EffectiveRoot(), prepared.root.EffectiveRoot(), projectWorkspaceBoundary)
	if err != nil {
		return sessionruntime.AgentRuntimePlan{}, err
	}
	pathContext, err := s.currentNodeManagedWorktreePathContext(prepared.plan, prepared.root)
	if err != nil {
		return sessionruntime.AgentRuntimePlan{}, err
	}
	return sessionruntime.NewAgentRuntimePlan(sessionruntime.AgentRuntimePlanOptions{
		Settings: prepared.plan.ActiveSettings, EnabledTools: workflowRuntimeEnabledTools(prepared.plan.EnabledTools),
		FilesystemContext: askquestion.FilesystemContext{Access: filesystemContext.Access, ManagedWorktree: pathContext}, Sources: prepared.plan.Source.Sources, Headless: true, Client: prepared.client,
		QuestionsEnabled:      textutil.Value(prepared.plan.QuestionsEnabled),
		AutoCompactionEnabled: textutil.Value(prepared.plan.AutoCompactionEnabled),
		ReviewerClientFactory: s.runtimeClientFactory,
		StartLogLines:         []string{fmt.Sprintf("workflow.runtime.start task_id=%s session_id=%s node_id=%s execution_root=%s model=%s", input.Task.ID, prepared.plan.Descriptor.SessionID(), input.Node.ID, prepared.root.EffectiveRoot(), prepared.plan.ActiveSettings.Model)},
		AskQuestionBatchSkipped: func(batch askquestion.AskQuestionBatchMetadata) {
			if s.attention == nil {
				return
			}
			if err := workflowattention.PrepareSkippedTaskQuestionBatch(s.attention, currentNodeQuestionContext(input, prepared.plan.Descriptor.SessionID().String()), batch, time.Now().UTC()); err != nil {
				slog.Warn("prepare skipped current-node workflow question batch failed", "task_id", input.Task.ID, "node_id", input.Node.ID, "error", err)
			}
		},
	})
}

func (s *Starter) currentNodeAgentRunner(
	input workflowstore.CurrentNodeStartContext,
	continuation *workflowexecution.WorkflowSessionContinuation,
	controller workflowruntime.Controller,
) sessionruntime.AgentRunner {
	return func(runCtx context.Context, scope sessionruntime.ExecutionScope, bridge sessionruntime.AgentRuntimeBridge) error {
		var turnEngine *runtime.Engine
		var turnResult runtime.WorkflowTurnResult
		var turnErr error
		turnErr = bridge.WithEngine(runCtx, func(engineCtx context.Context, engine *runtime.Engine) error {
			turnEngine = engine
			if continuation != nil {
				if name := textutil.OptionalTrimmedString(engine.SessionName()); name != nil {
					if err := continuation.RecordSessionName(*name); err != nil {
						return err
					}
				}
			}
			if continuation != nil {
				continuationInput := continuation.Input()
				var continuationText string
				var continuationSteer *runtime.AgentSteer
				switch input := continuationInput.(type) {
				case workflowexecution.WorkflowSessionTextInput:
					continuationText = input.Text
				case workflowexecution.WorkflowSessionSteerInput:
					continuationSteer = input.Steer
				}
				if input.ContextMode == workflow.ContextModeCompactAndContinueSession {
					turnResult, turnErr = engine.SubmitWorkflowContinuationTurnWithInputAndStepHook(
						metadata.WithQueryFailureDiagnostics(engineCtx),
						input.CurrentNode.Reference,
						continuationText,
						continuationSteer,
						continuation.RegisterStep,
					)
				} else {
					turnResult, turnErr = engine.SubmitWorkflowTurnWithInputAndStepHook(
						metadata.WithQueryFailureDiagnostics(engineCtx),
						input.CurrentNode.Reference,
						continuationText,
						continuationSteer,
						continuation.RegisterStep,
					)
				}
				continuation.RecordTurn(runtime.WorkflowTurnUserResult(turnResult), turnErr)
				return turnErr
			}
			if input.ContextMode == workflow.ContextModeCompactAndContinueSession {
				result, err := engine.SubmitWorkflowContinuationTurn(metadata.WithQueryFailureDiagnostics(engineCtx))
				turnResult = result
				if err != nil {
					return err
				}
			} else {
				result, err := engine.SubmitWorkflowTurn(metadata.WithQueryFailureDiagnostics(engineCtx))
				turnResult = result
				if err != nil {
					return err
				}
			}
			return nil
		})
		if continuation != nil && turnEngine == nil {
			continuation.RecordTurn(runtime.UserTurnResult{}, turnErr)
		}
		if turnResult.Completion != nil && turnEngine != nil {
			completion := *turnResult.Completion
			var stepHook func(string)
			if continuation != nil {
				stepHook = continuation.RegisterStep
			}
			compactionErr := compactCompletedWorkflowSession(runCtx, turnEngine, completion.CommittedResult, stepHook)
			continuationErr := controller.ContinueCurrentNode(
				context.WithoutCancel(runCtx),
				completion.CommittedResult,
			)
			if completion.Diagnostic != nil {
				slog.Error(
					"Workflow completion committed with a diagnostic",
					"task_id", input.Task.ID,
					"node_id", input.Node.ID,
					"session_id", turnEngine.SessionID(),
					"error", completion.Diagnostic,
				)
			}
			if postCompletionErr := errors.Join(turnErr, compactionErr, continuationErr); postCompletionErr != nil {
				slog.Error(
					"finish accepted Workflow completion",
					"task_id", input.Task.ID,
					"node_id", input.Node.ID,
					"session_id", turnEngine.SessionID(),
					"error", postCompletionErr,
				)
			}
			return nil
		}
		if turnErr == nil {
			turnErr = errors.New("workflow Agent execution ended without a completion outcome")
			return errors.Join(
				turnErr,
				s.failCurrentNodeScope(
					context.WithoutCancel(runCtx),
					controller,
					scope,
					"workflow_runtime_finalized_without_outcome",
					turnErr,
				),
			)
		}
		reason := ReasonRuntimeFailed
		if errors.Is(turnErr, context.Canceled) || context.Cause(runCtx) != nil {
			reason = string(workflow.CurrentNodeInterruptionReasonRuntimeCanceled)
		}
		return errors.Join(turnErr, s.failCurrentNodeScope(context.WithoutCancel(runCtx), controller, scope, reason, turnErr))
	}
}

func compactCompletedWorkflowSession(
	ctx context.Context,
	engine *runtime.Engine,
	completed workflowstore.CurrentNodeCompletionResult,
	stepHooks ...func(string),
) error {
	if engine == nil || !completed.PostCompletionEligible || engine.CompactionMode() == "none" {
		return nil
	}
	shouldCompact := completed.SessionReuseClassification == workflow.SessionReuseGuaranteedCACReuse
	if completed.SessionReuseClassification == workflow.SessionReuseThresholdPossibleReuse {
		threshold, err := engine.WorkflowPreCompactionTokenLimit()
		if err != nil {
			return err
		}
		shouldCompact = engine.ContextUsage().UsedTokens >= threshold
	}
	if !shouldCompact {
		return nil
	}
	if len(stepHooks) > 0 && stepHooks[0] != nil {
		_, err := engine.CompactContextForWorkflowPostCompletion(ctx, stepHooks[0])
		return err
	}
	_, err := engine.CompactContextForWorkflowPostCompletion(ctx)
	return err
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
	planner := launch.Planner{Config: cfg, ContainerDir: containerDir, StoreOptions: s.storeOptions, PersistedSessions: s.metadata, ExecutionTargets: s.metadata, ProjectWorkspaceBoundary: s.metadata, MetadataStoreOpener: func(string) (launch.MetadataExecutionTargetStore, error) { return s.metadata, nil }}
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
	if input.ContextMode == workflow.ContextModeCompactAndContinueSession {
		runtimeErr := s.runtimeAuthority.WithCurrentRuntime(
			ctx,
			plan.Descriptor.SessionID(),
			func(_ context.Context, engine *runtime.Engine) error {
				return engine.ResetLockedContractForWorkflowCompactionBoundary()
			},
		)
		switch {
		case runtimeErr == nil:
		case errors.Is(runtimeErr, serverapi.ErrRuntimeUnavailable):
			if err := s.withSessionStore(ctx, plan.Descriptor, func(_ context.Context, store *session.Store) error {
				return store.ResetLockedContractForCompactionBoundary()
			}); err != nil {
				return launch.SessionPlan{}, disposable, err
			}
		default:
			return launch.SessionPlan{}, disposable, runtimeErr
		}
		plan, err = planner.PlanSession(ctx, launch.SessionRequest{
			Mode:                                launch.ModeHeadless,
			Intent:                              serverapi.OpenExistingSessionLaunchIntent(plan.Descriptor.SessionID()),
			SkipContinuationAgentRoleValidation: sessionPrepared,
		})
		if err != nil {
			return launch.SessionPlan{}, disposable, err
		}
	}
	if sessionPrepared {
		selection, err := currentNodeAgentExecutionSelection(input)
		if err != nil {
			return launch.SessionPlan{}, disposable, err
		}
		thinkingMutation := workflowThinkingMutationFor(input, selection)
		if thinkingMutation.Kind() != launch.WorkflowThinkingMutationUnchanged {
			if err := s.withSessionStore(ctx, plan.Descriptor, func(_ context.Context, store *session.Store) error {
				var applyErr error
				plan, _, applyErr = planner.ApplyRunPromptOverridesWithStore(
					plan,
					store,
					serverapi.RunPromptOverrides{},
					auth.EmptyState(),
					launch.RunPromptOverrideOptions{WorkflowThinking: thinkingMutation},
				)
				return applyErr
			}); err != nil {
				return launch.SessionPlan{}, disposable, err
			}
		}
		return plan, disposable, nil
	}
	selection, err := currentNodeAgentExecutionSelection(input)
	if err != nil {
		return launch.SessionPlan{}, disposable, err
	}
	thinkingMutation := workflowThinkingMutationFor(input, selection)
	if policy.assignee != currentNodeSessionAssigneeEstablishTarget &&
		thinkingMutation.Kind() == launch.WorkflowThinkingMutationUnchanged {
		return plan, disposable, nil
	}
	options := launch.RunPromptOverrideOptions{}
	if selection.Origin == workflow.AssigneeOriginTransitionSelected {
		options.RequiredTools = []toolspec.ID{toolspec.ToolAskQuestion}
	}
	options.WorkflowThinking = thinkingMutation
	overrides := serverapi.RunPromptOverrides{}
	if policy.assignee == currentNodeSessionAssigneeEstablishTarget {
		overrides = workflowPromptOverrides(selection.Assignee)
	}
	err = s.withSessionStore(ctx, plan.Descriptor, func(_ context.Context, store *session.Store) error {
		var applyErr error
		plan, _, applyErr = planner.ApplyRunPromptOverridesWithStore(
			plan,
			store,
			overrides,
			auth.EmptyState(),
			options,
		)
		return applyErr
	})
	return plan, disposable, err
}

type currentNodeSessionAssigneePolicy = workflow.AssigneeSessionPolicy

const (
	currentNodeSessionAssigneeEstablishTarget = workflow.AssigneeSessionPolicyEstablishTarget
	currentNodeSessionAssigneePreserve        = workflow.AssigneeSessionPolicyPreserve
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
		return currentNodeSessionPolicy{}, fmt.Errorf("current node session policy does not support context source %q", source.Kind)
	}
	assignee, err := workflow.ResolveAssigneeSessionPolicy(workflow.AssigneeSessionPolicyRequest{
		ContextMode:           input.ContextMode,
		ContextSource:         source,
		TargetSessionResolved: input.CurrentNode.SessionID != nil,
	})
	if err != nil {
		return currentNodeSessionPolicy{}, err
	}
	return currentNodeSessionPolicy{
		cloneRetainedSession: input.IsFanoutBranch && !targetOwned && input.ContextMode != workflow.ContextModeNewSession,
		assignee:             assignee,
	}, nil
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
	preview, err := renderCurrentNodePrompt(input.TransitionPrompt, input)
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
	return s.metadata.UpdateSessionExecutionTarget(ctx, update)
}

func (s *Starter) currentNodeManagedWorktreePathContext(plan launch.SessionPlan, root workflowstore.ExecutionRoot) (*askquestion.ManagedWorktreePathContext, error) {
	if strings.TrimSpace(s.cfg.Settings.Worktrees.BaseDir) == "" {
		return nil, nil
	}
	var currentRoot *string
	if root.Managed != nil {
		currentRoot = &root.Managed.Root
	}
	return askquestion.NewManagedWorktreePathContext(s.cfg.Settings.Worktrees.BaseDir, currentRoot, plan.ManagedWorktreeRoots)
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
	return renderWorkflowPrompt(text, workflowPromptInput{Task: input.Task, Workflow: input.Workflow, Node: input.Node, CurrentNode: input.CurrentNode.Reference, ContextMode: input.ContextMode, SourceSessionID: source, TransitionOptions: input.TransitionOptions, TransitionIDs: input.TransitionIDs, TransitionPrompt: text, ParameterValues: input.ParameterValues, PriorValues: input.CurrentNode.PriorValues})
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

func currentNodeAgentExecutionSelection(input workflowstore.CurrentNodeStartContext) (workflow.AgentExecutionSelection, error) {
	if input.CurrentNode.AgentExecutionSelection == nil {
		return workflow.AgentExecutionSelection{}, errors.New("Agent Current Node execution selection is required")
	}
	selection := input.CurrentNode.AgentExecutionSelection.Clone()
	if err := selection.Validate(); err != nil {
		return workflow.AgentExecutionSelection{}, err
	}
	return selection, nil
}

func workflowThinkingMutationFor(input workflowstore.CurrentNodeStartContext, selection workflow.AgentExecutionSelection) launch.WorkflowThinkingMutation {
	if selection.Thinking != nil {
		return launch.SetWorkflowThinking(*selection.Thinking)
	}
	if selection.Origin == workflow.AssigneeOriginRetainedSession &&
		workflow.CanonicalThinkingSelection(input.EnteringEdge.ThinkingSelection) == workflow.ThinkingSelectionPreviousNode {
		return launch.ClearWorkflowThinking()
	}
	return launch.KeepWorkflowThinking()
}

type executionPromptAwaiter struct {
	authority *sessionruntime.Authority
	scope     sessionruntime.ExecutionScope
}

func (a executionPromptAwaiter) AwaitPromptResolution(ctx context.Context, _ string, req askquestion.AskQuestionRequest) (askquestion.AskQuestionResolution, error) {
	return a.authority.AwaitPromptResolution(ctx, a.scope.ID(), req)
}

func currentNodeQuestionContext(input workflowstore.CurrentNodeStartContext, sessionID string) workflowattention.TaskQuestionContext {
	return workflowattention.TaskQuestionContext{Task: input.Task, CurrentNode: input.CurrentNode.Reference, SessionID: sessionID}
}

func (s *Starter) handleCurrentNodeAsk(ctx context.Context, awaiter workflowattention.QuestionAwaiter, input workflowstore.CurrentNodeStartContext, sessionID string, askReq askquestion.AskQuestionRequest) (askquestion.AskQuestionResolution, error) {
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
	return buildWorkflowTaskInstructions(workflowPromptInput{Task: input.Task, Workflow: input.Workflow, Node: input.Node, CurrentNode: input.CurrentNode.Reference, ContextMode: input.ContextMode, SourceSessionID: source, TransitionOptions: input.TransitionOptions, TransitionIDs: input.TransitionIDs, TransitionPrompt: input.TransitionPrompt, ParameterValues: input.ParameterValues, PriorValues: input.CurrentNode.PriorValues})
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
	TransitionPrompt  string
	ParameterValues   map[string]string
	PriorValues       workflow.MaterializedPriorValues
}

func buildWorkflowTaskInstructions(input workflowPromptInput) (workflowruntime.TaskInstructions, error) {
	prompt, err := renderWorkflowPrompt(input.TransitionPrompt, input)
	if err != nil {
		return workflowruntime.TaskInstructions{}, err
	}
	shortID := input.Task.ShortID
	if shortID == "" {
		shortID = string(input.Task.ID)
	}
	return workflowruntime.TaskInstructions{CurrentNode: input.CurrentNode, TaskShortID: shortID, TaskTitle: input.Task.Title, TaskBody: input.Task.Body, WorkflowID: input.Task.WorkflowID, WorkflowName: strings.TrimSpace(input.Workflow.Name), NodeKey: string(input.Node.Key), NodeDisplayName: input.Node.DisplayName, ContextMode: string(input.ContextMode), SourceSessionID: input.SourceSessionID, Transitions: workflowInstructionTransitions(input.TransitionOptions, input.TransitionIDs), TransitionPrompt: prompt}, nil
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

var _ workflowexecution.CurrentNodePublicationRunner = (*Starter)(nil)
