package workflowrunner

import (
	"context"
	"encoding/json"
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
	"core/server/workflowruntime"
	"core/server/workflowstore"
	"core/shared/config"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/sessioncontract"
	"core/shared/toolspec"
)

const (
	ReasonRuntimeCanceled = "workflow_runtime_canceled"
	ReasonRuntimeFailed   = "workflow_runtime_failed"
)

var errWorkflowShellCompletionRequiresShell = errors.New("workflow shell_command completion requires shell tool availability for this run")

type RuntimeStore interface {
	GetRun(context.Context, workflow.RunID) (workflowstore.RunRecord, error)
	ListRuns(context.Context, workflow.TaskID) ([]workflowstore.RunRecord, error)
	GetRunStartContext(context.Context, workflow.RunID) (workflowstore.RunStartContext, error)
	GetRunCompletionContext(context.Context, workflow.RunID) (workflowstore.RunCompletionContext, error)
	AttachRunSession(context.Context, workflow.RunID, int64, string) error
	SetRunEffectiveCompletionMode(context.Context, workflow.RunID, int64, string) error
	SetRunWaitingAsk(context.Context, workflow.RunID, int64, string) error
	ClearRunWaitingAsk(context.Context, workflow.RunID, int64, string) error
	CompleteRun(context.Context, workflowstore.CompleteRunRequest) (workflowstore.CompleteRunResult, error)
	RecordProtocolViolation(context.Context, workflowstore.RecordProtocolViolationRequest) (workflowstore.RecordProtocolViolationResult, error)
	ResetProtocolViolationBudget(context.Context, workflowstore.ResetProtocolViolationBudgetRequest) error
	CountTaskComments(context.Context, workflow.TaskID) (int64, error)
	InterruptRun(context.Context, workflow.RunID, string, string) error
	InterruptRunGeneration(context.Context, workflow.RunID, int64, string, string) error
}

type LockedTaskWorktreeRestorer interface {
	RestoreLockedTaskWorktree(ctx context.Context, req LockedTaskWorktreeRestoreRequest) error
}

type LockedTaskWorktreeRestoreRequest struct {
	TaskID           workflow.TaskID
	SetupOperationID serverapi.WorktreeSetupOperationID
}

type RuntimeEventRegistry interface {
	PublishRuntimeEvent(sessionID string, evt runtime.Event)
}

type Starter struct {
	cfg                  config.App
	metadata             *metadata.Store
	store                RuntimeStore
	authManager          *auth.Manager
	runtimes             RuntimeEventRegistry
	runtimeAuthority     *sessionruntime.Authority
	storeOptions         []session.StoreOption
	clientFactory        func(SchedulerStartRunRequest) llm.Client
	runtimeClientFactory runtimewire.RuntimeClientFactory
	worktrees            LockedTaskWorktreeRestorer
	attentionFinalizer   workflowAttentionFinalizer

	closed atomic.Bool
}

type StarterOptions struct {
	ClientFactory        func(SchedulerStartRunRequest) llm.Client
	RuntimeClientFactory runtimewire.RuntimeClientFactory
	Worktrees            LockedTaskWorktreeRestorer
	RuntimeAuthority     *sessionruntime.Authority
	AttentionFinalizer   workflowAttentionFinalizer
}

type workflowAttentionFinalizer interface {
	FinalizeTransition(context.Context, workflowattention.TransitionResult)
}

type workflowInterruptedRunFinalizer interface {
	FinalizeInterruptedRun(context.Context, workflow.RunID)
}

func NewStarter(cfg config.App, metadataStore *metadata.Store, store RuntimeStore, authManager *auth.Manager, runtimes RuntimeEventRegistry, opts StarterOptions) (*Starter, error) {
	if strings.TrimSpace(cfg.PersistenceRoot) == "" {
		return nil, errors.New("workflow runtime persistence root is required")
	}
	if metadataStore == nil {
		return nil, errors.New("workflow runtime metadata store is required")
	}
	if store == nil {
		return nil, errors.New("workflow runtime store is required")
	}
	if opts.RuntimeAuthority == nil {
		return nil, errors.New("workflow runtime authority is required")
	}
	if opts.ClientFactory != nil && opts.RuntimeClientFactory != nil {
		return nil, runtimewire.ErrRuntimeClientFactoryConflict
	}
	return &Starter{
		cfg:                  cfg,
		metadata:             metadataStore,
		store:                store,
		authManager:          authManager,
		runtimes:             runtimes,
		runtimeAuthority:     opts.RuntimeAuthority,
		storeOptions:         metadataStore.AuthoritativeSessionStoreOptions(),
		clientFactory:        opts.ClientFactory,
		runtimeClientFactory: opts.RuntimeClientFactory,
		worktrees:            opts.Worktrees,
		attentionFinalizer:   opts.AttentionFinalizer,
	}, nil
}

func (s *Starter) StartWorkflowRun(ctx context.Context, req SchedulerStartRunRequest) error {
	if strings.TrimSpace(string(req.RunID)) == "" {
		return errors.New("workflow run id is required")
	}
	if s.closed.Load() {
		return errors.New("workflow runtime starter closed")
	}
	input, err := s.store.GetRunStartContext(ctx, req.RunID)
	if err != nil {
		if s.worktrees == nil || !recoverableManagedExecutionRootError(err) {
			return err
		}
		if restoreErr := s.worktrees.RestoreLockedTaskWorktree(ctx, LockedTaskWorktreeRestoreRequest{TaskID: req.TaskID, SetupOperationID: serverapi.NewWorktreeSetupOperationID()}); restoreErr != nil {
			return restoreErr
		}
		input, err = s.store.GetRunStartContext(ctx, req.RunID)
		if err != nil {
			return err
		}
	} else if input.ExecutionRoot != nil && input.ExecutionRoot.Managed != nil && s.worktrees != nil {
		if err := s.worktrees.RestoreLockedTaskWorktree(ctx, LockedTaskWorktreeRestoreRequest{TaskID: req.TaskID, SetupOperationID: serverapi.NewWorktreeSetupOperationID()}); err != nil {
			return err
		}
		input, err = s.store.GetRunStartContext(ctx, req.RunID)
		if err != nil {
			return err
		}
	}
	executionRoot, err := requireRunExecutionRoot(input)
	if err != nil {
		return err
	}
	if input.Run.Generation != req.Generation {
		return fmt.Errorf("stale workflow run generation: got %d want %d", req.Generation, input.Run.Generation)
	}
	if input.Node.Kind == workflow.NodeKindScript {
		return s.startScriptWorkflowRun(req, input)
	}
	if input.Node.Kind != workflow.NodeKindAgent {
		return fmt.Errorf("workflow node %q is %q, want executable agent or script", input.Node.ID, input.Node.Kind)
	}
	if err := s.validateRole(input.Node.SubagentRole); err != nil {
		return err
	}
	plan, warnings, err := s.planSession(ctx, input)
	if err != nil {
		return err
	}
	// When the plan reuses an existing session (resume, continue, or in-place
	// compact-and-continue), it is the previous node's persisted session — never
	// dispose of it on setup failure. Only freshly created run sessions
	// (new-session and fan-out clones) are disposable.
	//
	// For reused sessions, snapshot previous listing/reminder metadata so setup
	// mutations can be rolled back if any later setup step fails.
	var prevReminderState *session.WorktreeReminderState
	var prevListingMetadata *sessionListingMetadata
	if reusesExistingSession(input) {
		prevListingMetadata = &sessionListingMetadata{Name: plan.SessionName, FirstPromptPreview: plan.FirstPromptPreview}
		if wr := plan.WorktreeReminder; wr != nil {
			snap := *wr
			prevReminderState = &snap
		}
	}
	cleanupSession := func() error {
		if reusesExistingSession(input) {
			return s.withSessionStore(context.WithoutCancel(ctx), plan.Descriptor, func(_ context.Context, store *session.Store) error {
				return errors.Join(restoreSessionListingMetadata(store, prevListingMetadata), store.SetWorktreeReminderState(prevReminderState))
			})
		}
		return s.cleanupSession(ctx, plan.Descriptor)
	}
	if err := s.applyWorkflowSessionMetadata(ctx, input, &plan); err != nil {
		return errors.Join(err, cleanupSession())
	}
	client := llm.Client(nil)
	if s.clientFactory != nil {
		client = s.clientFactory(req)
	}
	if s.runtimeClientFactory != nil {
		client, err = s.newWorkflowProviderClient(ctx, plan)
		if err != nil {
			return errors.Join(err, cleanupSession())
		}
	}
	effectiveMode, client, err := s.resolveAndPersistWorkflowCompletionMode(ctx, req, input, plan, client)
	if err != nil {
		return errors.Join(err, cleanupSession())
	}
	var reminder *session.WorktreeReminderState
	if executionRoot.Managed != nil {
		reminder = &session.WorktreeReminderState{
			Mode: session.WorktreeReminderModeEnter,
			WorktreeContext: session.WorktreeContext{
				WorktreePath:  executionRoot.Managed.Root,
				WorkspaceRoot: executionRoot.SourceWorkspaceRoot,
				EffectiveCwd:  executionRoot.EffectiveRoot(),
			},
		}
	}
	if err := s.withSessionStore(ctx, plan.Descriptor, func(_ context.Context, store *session.Store) error {
		return store.SetWorktreeReminderState(reminder)
	}); err != nil {
		return errors.Join(err, cleanupSession())
	}
	plan.WorktreeReminder = reminder
	targetUpdate := metadata.SessionExecutionTargetUpdate{
		SessionID:  plan.Descriptor.SessionID().String(),
		Workspace:  &metadata.SessionExecutionTargetUpdateWorkspace{ID: executionRoot.SourceWorkspaceID},
		CwdRelpath: ".",
	}
	if executionRoot.Managed != nil {
		targetUpdate.Worktree = &metadata.SessionExecutionTargetUpdateWorktree{ID: executionRoot.Managed.WorktreeID}
	}
	if err := s.metadata.UpdateSessionExecutionTarget(ctx, targetUpdate); err != nil {
		return errors.Join(err, cleanupSession())
	}
	var previousWorkflowSession *session.WorkflowSessionState
	if workflowSession := plan.WorkflowSession; workflowSession != nil {
		snap := *workflowSession
		previousWorkflowSession = &snap
	}
	restoreWorkflowSession := func() error {
		return s.withSessionStore(context.WithoutCancel(ctx), plan.Descriptor, func(_ context.Context, store *session.Store) error {
			return store.SetWorkflowSessionState(previousWorkflowSession)
		})
	}
	workflowSession := &session.WorkflowSessionState{
		RunID:      string(req.RunID),
		TaskID:     string(input.Task.ID),
		WorkflowID: string(input.Task.WorkflowID),
	}
	if err := s.withSessionStore(ctx, plan.Descriptor, func(_ context.Context, store *session.Store) error {
		return store.SetWorkflowSessionState(workflowSession)
	}); err != nil {
		return errors.Join(err, cleanupSession())
	}
	plan.WorkflowSession = workflowSession
	if err := s.store.AttachRunSession(ctx, req.RunID, req.Generation, plan.Descriptor.SessionID().String()); err != nil {
		return errors.Join(err, restoreWorkflowSession(), cleanupSession())
	}
	return s.startAgentExecution(ctx, req, input, plan, warnings, client, effectiveMode)
}

func recoverableManagedExecutionRootError(err error) bool {
	var rootErr *workflowstore.ExecutionRootError
	if !errors.As(err, &rootErr) {
		return false
	}
	return rootErr.Kind == workflowstore.ExecutionRootErrorManagedRelationMissing ||
		rootErr.Kind == workflowstore.ExecutionRootErrorManagedRecordMissing
}

func requireRunExecutionRoot(input workflowstore.RunStartContext) (workflowstore.ExecutionRoot, error) {
	if input.ExecutionRoot == nil {
		return workflowstore.ExecutionRoot{}, fmt.Errorf("workflow run %q has no execution root", input.Run.ID)
	}
	root := *input.ExecutionRoot
	if err := root.Validate(); err != nil {
		return workflowstore.ExecutionRoot{}, fmt.Errorf("workflow run %q has an invalid execution root: %w", input.Run.ID, err)
	}
	if strings.TrimSpace(root.EffectiveRoot()) == "" {
		return workflowstore.ExecutionRoot{}, fmt.Errorf("workflow run %q has no effective execution root", input.Run.ID)
	}
	return root, nil
}

func (s *Starter) withSessionStore(ctx context.Context, descriptor session.SessionDescriptor, callback func(context.Context, *session.Store) error) error {
	return s.runtimeAuthority.WithSessionStore(ctx, descriptor, callback)
}

func (s *Starter) cleanupSession(ctx context.Context, descriptor session.SessionDescriptor) error {
	if ctx == nil {
		ctx = context.Background()
	}
	cleanupCtx := context.WithoutCancel(ctx)
	sessionID := descriptor.SessionID().String()
	return errors.Join(
		s.withSessionStore(cleanupCtx, descriptor, func(_ context.Context, store *session.Store) error {
			return store.RemoveDurable()
		}),
		s.metadata.DeleteSessionRecordByID(cleanupCtx, sessionID),
	)
}

func (s *Starter) Close() error {
	if s == nil || s.closed.Swap(true) {
		return nil
	}
	return s.runtimeAuthority.StopWorkflowExecutions(context.Background())
}

func (s *Starter) CancelTaskRuns(ctx context.Context, taskID workflow.TaskID) error {
	runs, err := s.store.ListRuns(ctx, taskID)
	if err != nil {
		return err
	}
	var stopErrs []error
	for _, run := range runs {
		ref := sessionruntime.WorkflowExecutionRef{RunID: run.ID, Generation: run.Generation}
		if execution, ok := s.runtimeAuthority.ExecutionByWorkflow(ref); ok {
			if err := execution.Stop(ctx); err != nil {
				stopErrs = append(stopErrs, err)
			}
		}
	}
	return errors.Join(stopErrs...)
}

func (s *Starter) CancelRun(ctx context.Context, runID workflow.RunID) error {
	run, err := s.store.GetRun(ctx, runID)
	if err != nil {
		return err
	}
	ref := sessionruntime.WorkflowExecutionRef{RunID: run.ID, Generation: run.Generation}
	if execution, ok := s.runtimeAuthority.ExecutionByWorkflow(ref); ok {
		return execution.Stop(ctx)
	}
	return nil
}

func (s *Starter) RequestCancelRun(runID workflow.RunID) bool {
	run, err := s.store.GetRun(context.Background(), runID)
	if err != nil {
		return false
	}
	ref := sessionruntime.WorkflowExecutionRef{RunID: run.ID, Generation: run.Generation}
	execution, ok := s.runtimeAuthority.ExecutionByWorkflow(ref)
	return ok && execution.RequestStop()
}

// reusesExistingSession reports whether planSession reuses a pre-existing
// session (resume of a started run, continue_session, or in-place
// compact_and_continue_session) rather than creating a disposable one
// (new_session or a fan-out clone). Reused sessions belong to a prior node and
// must not be cleaned up on setup failure.
func reusesExistingSession(input workflowstore.RunStartContext) bool {
	if strings.TrimSpace(input.Run.SessionID) != "" {
		return true
	}
	switch input.ContextMode {
	case workflow.ContextModeContinueSession:
		return true
	case workflow.ContextModeCompactAndContinueSession:
		return !input.IsFanoutBranch
	default:
		return false
	}
}

func (s *Starter) planSession(ctx context.Context, input workflowstore.RunStartContext) (launch.SessionPlan, []string, error) {
	executionRoot, err := requireRunExecutionRoot(input)
	if err != nil {
		return launch.SessionPlan{}, nil, err
	}
	cfg := s.cfg
	cfg.WorkspaceRoot = executionRoot.SourceWorkspaceRoot
	projectID := strings.TrimSpace(input.Task.ProjectID)
	if projectID == "" {
		return launch.SessionPlan{}, nil, errors.New("workflow task project id is required")
	}
	containerDir := filepath.Join(filepath.Join(cfg.PersistenceRoot, "projects"), projectID, "sessions")
	planner := launch.Planner{
		Config:            cfg,
		ContainerDir:      containerDir,
		StoreOptions:      s.storeOptions,
		PersistedSessions: s.metadata,
		MetadataStoreOpener: func(string) (launch.MetadataExecutionTargetStore, error) {
			return s.metadata, nil
		},
	}
	// A fan-out branch creates a brand-new disposable clone before the rest of
	// planning runs. If any later planning step fails, StartWorkflowRun's cleanup
	// hook never sees it, so remove the clone here on failure to avoid orphaning
	// an unattached session directory.
	disposableCloneID := ""
	planSucceeded := false
	defer func() {
		if !planSucceeded && disposableCloneID != "" {
			s.removeFanoutClone(ctx, containerDir, disposableCloneID)
		}
	}()
	var plan launch.SessionPlan
	launchRequest, err := sessionLaunchRequestForWorkflowRun(input)
	if err != nil {
		return launch.SessionPlan{}, nil, err
	}
	overrides := launchRequest.Overrides
	skipPersistedRoleValidation := overrides.HasAny()
	if strings.TrimSpace(input.Run.SessionID) != "" {
		plan, err = planner.PlanSession(ctx, launch.SessionRequest{Mode: launch.ModeHeadless, Intent: launchRequest.Intent, SkipContinuationAgentRoleValidation: skipPersistedRoleValidation})
		if err != nil {
			return launch.SessionPlan{}, nil, err
		}
		if err := s.withSessionStore(ctx, plan.Descriptor, func(_ context.Context, store *session.Store) error {
			return store.EnsureDurable()
		}); err != nil {
			return launch.SessionPlan{}, nil, err
		}
	} else {
		switch input.ContextMode {
		case "", workflow.ContextModeNewSession:
			plan, err = planner.PlanSession(ctx, launch.SessionRequest{Mode: launch.ModeHeadless, Intent: launchRequest.Intent, SkipContinuationAgentRoleValidation: skipPersistedRoleValidation})
		case workflow.ContextModeContinueSession:
			plan, err = planner.PlanSession(ctx, launch.SessionRequest{Mode: launch.ModeHeadless, Intent: launchRequest.Intent, SkipContinuationAgentRoleValidation: skipPersistedRoleValidation})
		case workflow.ContextModeCompactAndContinueSession:
			// In-place continuation reuses the source session; the runtime runs a real
			// compaction before the node turn. A fan-out branch instead continues in an
			// isolated full clone of the source so parallel branches never compact or
			// mutate the shared source session concurrently.
			continuationSessionID := input.SourceSessionID
			if input.IsFanoutBranch {
				continuationSessionID, err = s.cloneSourceSessionForFanout(containerDir, input.SourceSessionID)
				if err != nil {
					return launch.SessionPlan{}, nil, err
				}
				disposableCloneID = continuationSessionID
				clonedID, parseErr := runtimeids.ParseSessionID(continuationSessionID)
				if parseErr != nil {
					return launch.SessionPlan{}, nil, parseErr
				}
				launchRequest.Intent = serverapi.OpenExistingSessionLaunchIntent(clonedID)
			}
			plan, err = planner.PlanSession(ctx, launch.SessionRequest{Mode: launch.ModeHeadless, Intent: launchRequest.Intent, SkipContinuationAgentRoleValidation: skipPersistedRoleValidation})
		default:
			return launch.SessionPlan{}, nil, fmt.Errorf("unsupported workflow context mode %q", input.ContextMode)
		}
		if err != nil {
			return launch.SessionPlan{}, nil, err
		}
		if err := s.withSessionStore(ctx, plan.Descriptor, func(_ context.Context, store *session.Store) error {
			return store.EnsureDurable()
		}); err != nil {
			return launch.SessionPlan{}, nil, err
		}
	}
	if compactAndContinueRequiresFreshContract(input, plan) {
		if err := s.withSessionStore(ctx, plan.Descriptor, func(_ context.Context, store *session.Store) error {
			return store.ResetLockedContractForCompactionBoundary()
		}); err != nil {
			return launch.SessionPlan{}, nil, err
		}
		plan, err = planner.PlanSession(ctx, launch.SessionRequest{
			Mode:                                launch.ModeHeadless,
			Intent:                              mustOpenWorkflowSessionIntent(plan.Descriptor.SessionID().String()),
			SkipContinuationAgentRoleValidation: skipPersistedRoleValidation,
		})
		if err != nil {
			return launch.SessionPlan{}, nil, err
		}
	}
	overrides = workflowRunPromptOverrides(input.Node.SubagentRole)
	plan, warnings, err := planner.ApplyRunPromptOverridesWithOptions(plan, overrides, auth.EmptyState(), launch.RunPromptOverrideOptions{
		AllowLockedAgentRoleChange: allowLockedWorkflowContinuationRoleChange(plan, overrides),
	})
	if err != nil {
		return launch.SessionPlan{}, nil, err
	}
	planSucceeded = true
	return plan, warnings, nil
}

type workflowSessionLaunchRequest struct {
	Intent    serverapi.SessionLaunchIntent
	Overrides serverapi.RunPromptOverrides
}

func sessionLaunchRequestForWorkflowRun(input workflowstore.RunStartContext) (workflowSessionLaunchRequest, error) {
	request := workflowSessionLaunchRequest{Overrides: workflowRunPromptOverrides(input.Node.SubagentRole)}
	if resumedID := strings.TrimSpace(input.Run.SessionID); resumedID != "" {
		intent, err := openWorkflowSessionIntent(resumedID)
		request.Intent = intent
		return request, err
	}
	switch input.ContextMode {
	case "", workflow.ContextModeNewSession:
		request.Intent = serverapi.CreateNewSessionLaunchIntent(serverapi.IndependentSessionCreateOrigin())
	case workflow.ContextModeContinueSession:
		intent, err := openWorkflowSessionIntent(input.SourceSessionID)
		if err != nil {
			return workflowSessionLaunchRequest{}, fmt.Errorf("continue_session requires a valid source session: %w", err)
		}
		request.Intent = intent
	case workflow.ContextModeCompactAndContinueSession:
		intent, err := openWorkflowSessionIntent(input.SourceSessionID)
		if err != nil {
			return workflowSessionLaunchRequest{}, fmt.Errorf("compact_and_continue_session requires a valid source session: %w", err)
		}
		request.Intent = intent
	default:
		return workflowSessionLaunchRequest{}, fmt.Errorf("unsupported workflow context mode %q", input.ContextMode)
	}
	return request, nil
}

func openWorkflowSessionIntent(raw string) (serverapi.SessionLaunchIntent, error) {
	sessionID, err := runtimeids.ParseSessionID(strings.TrimSpace(raw))
	if err != nil {
		return serverapi.SessionLaunchIntent{}, err
	}
	return serverapi.OpenExistingSessionLaunchIntent(sessionID), nil
}

func mustOpenWorkflowSessionIntent(raw string) serverapi.SessionLaunchIntent {
	intent, err := openWorkflowSessionIntent(raw)
	if err != nil {
		panic(fmt.Sprintf("workflow planned invalid session ID %q: %v", raw, err))
	}
	return intent
}

func compactAndContinueRequiresFreshContract(input workflowstore.RunStartContext, plan launch.SessionPlan) bool {
	if input.ContextMode != workflow.ContextModeCompactAndContinueSession {
		return false
	}
	activeWorkflowSession := plan.WorkflowSession
	return activeWorkflowSession == nil || strings.TrimSpace(activeWorkflowSession.RunID) != strings.TrimSpace(string(input.Run.ID))
}

func allowLockedWorkflowContinuationRoleChange(plan launch.SessionPlan, overrides serverapi.RunPromptOverrides) bool {
	if !plan.ModelContractLocked {
		return false
	}
	roleOverride, err := overrides.AgentRoleOverride()
	if err != nil || !roleOverride.Present {
		return false
	}
	var currentRole *string
	if plan.Continuation != nil {
		currentRole = plan.Continuation.AgentRole
	}
	if currentRole == nil {
		return !roleOverride.Default
	}
	return roleOverride.Default || *currentRole != roleOverride.Role
}

func applyWorkflowSessionPromptOverrides(plan launch.SessionPlan, input workflowstore.RunStartContext) (launch.SessionPlan, []string, error) {
	overrides := workflowRunPromptOverrides(input.Node.SubagentRole)
	role, err := overrides.AgentRoleOverride()
	if err != nil {
		return launch.SessionPlan{}, nil, err
	}
	baseSettings := plan.BaseSettings
	if strings.TrimSpace(baseSettings.Model) == "" {
		baseSettings = plan.ActiveSettings
	}
	baseSource := plan.BaseSource
	if baseSource.Sources == nil {
		baseSource = plan.Source
	}
	toolLock := plan.Locked
	if allowLockedWorkflowContinuationRoleChange(plan, overrides) {
		toolLock = nil
	}
	prepared, err := launch.PrepareRunPromptOverridesWithContext(config.App{
		WorkspaceRoot: plan.WorkspaceRoot,
		Settings:      baseSettings,
		Source:        baseSource,
	}, overrides, auth.EmptyState(), launch.RunPromptPreparationContext{
		ModelLock: plan.Locked,
		ToolLock:  toolLock,
		OmittedTarget: &launch.PreparedBaseTarget{
			Settings:     plan.ActiveSettings,
			Source:       plan.Source,
			EnabledTools: plan.EnabledTools,
		},
	})
	if err != nil {
		return launch.SessionPlan{}, nil, err
	}
	var warnings []string
	switch {
	case !role.Present || role.Default:
		if prepared.BaseTarget == nil {
			return launch.SessionPlan{}, nil, errors.New("prepared workflow base target is required")
		}
		plan.ActiveSettings = prepared.BaseTarget.Settings
		plan.Source = prepared.BaseTarget.Source
		plan.EnabledTools = append([]toolspec.ID(nil), prepared.BaseTarget.EnabledTools...)
	default:
		if prepared.NamedTarget == nil {
			return launch.SessionPlan{}, nil, errors.New("prepared workflow agent target is required")
		}
		plan.ActiveSettings = prepared.NamedTarget.Settings
		plan.Source = prepared.NamedTarget.Source
		plan.EnabledTools = append([]toolspec.ID(nil), prepared.NamedTarget.EnabledTools...)
		if prepared.NamedTarget.Warning != nil {
			warnings = append(warnings, *prepared.NamedTarget.Warning)
		}
	}
	if !plan.ModelContractLocked {
		plan.ConfiguredModelName = plan.ActiveSettings.Model
	}
	continuation := session.ContinuationContext{OpenAIBaseURL: plan.ActiveSettings.OpenAIBaseURL}
	if plan.Continuation != nil {
		continuation = *plan.Continuation
		continuation.OpenAIBaseURL = plan.ActiveSettings.OpenAIBaseURL
	}
	if role.Present && !role.Default {
		selected := role.Role
		continuation.AgentRole = &selected
	} else {
		continuation.AgentRole = nil
	}
	plan.Continuation = &continuation
	return plan, warnings, nil
}

type sessionListingMetadata struct {
	Name               string
	FirstPromptPreview string
}

func restoreSessionListingMetadata(store *session.Store, metadata *sessionListingMetadata) error {
	if store == nil || metadata == nil {
		return nil
	}
	return store.SetListingMetadata(metadata.Name, metadata.FirstPromptPreview)
}

func (s *Starter) applyWorkflowSessionMetadata(ctx context.Context, input workflowstore.RunStartContext, plan *launch.SessionPlan) error {
	if plan == nil {
		return errors.New("workflow session plan is required")
	}
	name, err := workflowSessionName(input)
	if err != nil {
		return err
	}
	preview, err := renderTransitionPrompt(input.PromptTemplate, input)
	if err != nil {
		return err
	}
	if err := s.withSessionStore(ctx, plan.Descriptor, func(_ context.Context, store *session.Store) error {
		return store.SetListingMetadata(name, preview)
	}); err != nil {
		return err
	}
	plan.SessionName = name
	plan.FirstPromptPreview = preview
	return nil
}

func workflowSessionName(input workflowstore.RunStartContext) (string, error) {
	taskDisplayID := strings.TrimSpace(input.Task.ShortID)
	if taskDisplayID == "" {
		taskDisplayID = strings.TrimSpace(string(input.Task.ID))
	}
	if taskDisplayID == "" {
		return "", errors.New("workflow session metadata requires a task id")
	}
	sourceDisplayName := strings.TrimSpace(input.AcceptedTransitionPath.SourceNodeDisplayName)
	if sourceDisplayName == "" {
		return "", errors.New("workflow session metadata requires accepted transition source display name")
	}
	targetDisplayName := strings.TrimSpace(input.AcceptedTransitionPath.TargetNodeDisplayName)
	if targetDisplayName == "" {
		return "", errors.New("workflow session metadata requires accepted transition target display name")
	}
	return fmt.Sprintf("%s: %s -> %s", taskDisplayID, sourceDisplayName, targetDisplayName), nil
}

func (s *Starter) resolveAndPersistWorkflowCompletionMode(ctx context.Context, req SchedulerStartRunRequest, input workflowstore.RunStartContext, plan launch.SessionPlan, client llm.Client) (workflowruntime.CompletionMode, llm.Client, error) {
	shellAvailable := toolIDEnabled(plan.EnabledTools, toolspec.ToolExecCommand)
	if stored := optionalRunCompletionMode(input.Run.EffectiveCompletionMode); stored != "" {
		mode, err := workflowruntime.ParseCompletionMode(stored)
		if err != nil {
			return "", client, err
		}
		if mode == workflowruntime.CompletionModeShellCommand && !shellAvailable {
			return "", client, errWorkflowShellCompletionRequiresShell
		}
		return mode, client, nil
	}
	configuredMode := s.cfg.Settings.Workflow.CompletionMode
	if nodeMode := strings.TrimSpace(input.Node.CompletionMode); nodeMode != "" {
		configuredMode = config.WorkflowCompletionMode(nodeMode)
	}
	if configuredMode == config.WorkflowCompletionModeShellCommand && !shellAvailable {
		return "", client, errWorkflowShellCompletionRequiresShell
	}
	selection := workflowruntime.CompletionModeSelection{
		ConfiguredMode:         configuredMode,
		HasContinueSessionEdge: input.WorkflowHasContinueSessionEdge,
		ShellAvailable:         shellAvailable,
	}
	resolvedClient := client
	if workflowCompletionModeNeedsProviderCapabilities(selection) {
		caps, nextClient, err := s.workflowProviderCapabilities(ctx, plan, client)
		if err != nil {
			return "", nextClient, fmt.Errorf("resolve provider capabilities for workflow completion: %w", err)
		}
		selection.ProviderCapabilities = caps
		resolvedClient = nextClient
	}
	mode, err := workflowruntime.SelectCompletionMode(selection)
	if err != nil {
		return "", resolvedClient, err
	}
	if err := s.store.SetRunEffectiveCompletionMode(ctx, req.RunID, req.Generation, string(mode)); err != nil {
		return "", resolvedClient, err
	}
	return mode, resolvedClient, nil
}

func workflowCompletionModeNeedsProviderCapabilities(selection workflowruntime.CompletionModeSelection) bool {
	switch selection.ConfiguredMode {
	case config.WorkflowCompletionModeStructuredOutput:
		return true
	case config.WorkflowCompletionModeAuto, "":
		return selection.ShellAvailable && !selection.HasContinueSessionEdge
	default:
		return false
	}
}

func (s *Starter) workflowProviderCapabilities(ctx context.Context, plan launch.SessionPlan, client llm.Client) (llm.ProviderCapabilities, llm.Client, error) {
	if caps, ok := llm.ProviderCapabilitiesFromLocked(plan.Locked); ok {
		return caps, client, nil
	}
	if caps, ok := llm.ProviderCapabilitiesFromOverride(plan.ActiveSettings.ProviderCapabilities); ok {
		return caps, client, nil
	}
	if client == nil {
		created, err := s.newWorkflowProviderClient(ctx, plan)
		if err != nil {
			return llm.ProviderCapabilities{}, nil, err
		}
		client = created
	}
	provider, ok := client.(llm.ProviderCapabilitiesClient)
	if !ok {
		return llm.ProviderCapabilities{}, client, fmt.Errorf("provider capabilities are unavailable for client %T", client)
	}
	caps, err := provider.ProviderCapabilities(ctx)
	if err != nil {
		return llm.ProviderCapabilities{}, client, err
	}
	return caps, client, nil
}

func (s *Starter) newWorkflowProviderClient(ctx context.Context, plan launch.SessionPlan) (llm.Client, error) {
	active := plan.ActiveSettings
	providerCapabilitiesOverride := workflowProviderCapabilitiesOverride(plan)
	if s.runtimeClientFactory != nil {
		client, err := s.runtimeClientFactory.NewRuntimeClient(ctx, runtimewire.RuntimeClientRequest{
			Purpose:        runtimewire.RuntimeClientPurposeWorkflow,
			SessionID:      plan.Descriptor.SessionID().String(),
			ActiveSettings: plan.ActiveSettings,
			EnabledTools:   append([]toolspec.ID(nil), plan.EnabledTools...),
			WorkspaceRoot:  plan.WorkspaceRoot,
			Sources:        cloneStringMap(plan.Source.Sources),
			ProviderSettings: runtimewire.RuntimeClientProviderSettings{
				Model:                        active.Model,
				ProviderOverride:             active.ProviderOverride,
				OpenAIBaseURL:                active.OpenAIBaseURL,
				ModelVerbosity:               active.ModelVerbosity,
				ProviderIdentifier:           active.ProviderIdentifier,
				Store:                        active.Store,
				ContextWindowTokens:          active.ModelContextWindow,
				Auth:                         "inherit",
				ProviderCapabilitiesOverride: providerCapabilitiesOverride,
			},
		})
		if err != nil {
			return nil, err
		}
		if client == nil {
			return nil, fmt.Errorf("runtime client factory returned nil client for workflow purpose")
		}
		return client, nil
	}
	var authProvider llm.AuthHeaderProvider
	if s.authManager != nil {
		authProvider = s.authManager
	}
	return llm.NewProviderClient(llm.ProviderClientOptions{
		Provider:                     llm.Provider(strings.TrimSpace(active.ProviderOverride)),
		Model:                        active.Model,
		Auth:                         authProvider,
		HTTPClient:                   llm.NewHTTPClient(time.Duration(active.Timeouts.ModelRequestSeconds) * time.Second),
		OpenAIBaseURL:                active.OpenAIBaseURL,
		ModelVerbosity:               string(active.ModelVerbosity),
		ProviderIdentifier:           &active.ProviderIdentifier,
		Store:                        active.Store,
		ContextWindowTokens:          active.ModelContextWindow,
		ProviderCapabilitiesOverride: providerCapabilitiesOverride,
	})
}

func workflowProviderCapabilitiesOverride(plan launch.SessionPlan) *llm.ProviderCapabilities {
	caps, ok := llm.ProviderCapabilitiesFromLockedOrOverride(plan.Locked, plan.ActiveSettings.ProviderCapabilities)
	if !ok {
		return nil
	}
	return &caps
}

func cloneStringMap(values map[string]string) map[string]string {
	return maps.Clone(values)
}

func toolIDEnabled(enabled []toolspec.ID, want toolspec.ID) bool {
	for _, id := range enabled {
		if id == want {
			return true
		}
	}
	return false
}

func workflowRunPromptOverrides(role string) serverapi.RunPromptOverrides {
	normalized := strings.TrimSpace(role)
	if normalized == "" {
		return serverapi.RunPromptOverrides{}
	}
	if workflow.IsDefaultAgentRole(normalized) {
		normalized = workflow.DefaultAgentRole
	}
	return serverapi.RunPromptOverrides{AgentRole: &normalized}
}

// cloneSourceSessionForFanout creates an isolated full clone of the source
// session for a fan-out compact-and-continue branch and returns its session ID,
// so the branch can be compacted/continued without touching the shared source.
func (s *Starter) cloneSourceSessionForFanout(containerDir, sourceSessionID string) (string, error) {
	descriptor, err := workflowSessionDescriptor(containerDir, sourceSessionID)
	if err != nil {
		return "", err
	}
	var cloneID string
	err = s.withSessionStore(context.Background(), descriptor, func(_ context.Context, sourceStore *session.Store) error {
		cloned, cloneErr := session.CloneSession(sourceStore, "", sessioncontract.SessionCategorySubagent)
		if cloneErr != nil {
			return fmt.Errorf("clone source session: %w", cloneErr)
		}
		cloneID = cloned.Meta().SessionID
		return nil
	})
	if err != nil {
		return "", err
	}
	return cloneID, nil
}

// removeFanoutClone deletes a disposable fan-out clone that was created but never
// attached to a started run because planning failed afterward. Best-effort: it
// removes the on-disk session and any metadata record, leaving nothing orphaned.
func (s *Starter) removeFanoutClone(ctx context.Context, containerDir, sessionID string) {
	if strings.TrimSpace(sessionID) == "" {
		return
	}
	cleanupCtx := context.WithoutCancel(ctx)
	if descriptor, err := workflowSessionDescriptor(containerDir, sessionID); err == nil {
		_ = s.withSessionStore(cleanupCtx, descriptor, func(_ context.Context, store *session.Store) error {
			return store.RemoveDurable()
		})
	}
	_ = s.metadata.DeleteSessionRecordByID(cleanupCtx, sessionID)
}

func workflowSessionDescriptor(containerDir string, rawSessionID string) (session.SessionDescriptor, error) {
	sessionID, err := runtimeids.ParseSessionID(strings.TrimSpace(rawSessionID))
	if err != nil {
		return session.SessionDescriptor{}, err
	}
	return session.NewScopedOpenSessionDescriptor(sessionID, containerDir)
}

func (s *Starter) validateRole(role string) error {
	trimmed := strings.TrimSpace(role)
	if workflow.IsDefaultAgentRole(trimmed) {
		return nil
	}
	if config.LookupSubagentRole(s.cfg.Settings, trimmed).Status == config.SubagentRoleLookupPresent {
		return nil
	}
	return fmt.Errorf("workflow validation failed: [%s]", workflow.CodeAgentRoleMissing)
}

func (s *Starter) startAgentExecution(ctx context.Context, req SchedulerStartRunRequest, input workflowstore.RunStartContext, plan launch.SessionPlan, warnings []string, client llm.Client, effectiveMode workflowruntime.CompletionMode) error {
	executionRoot, err := requireRunExecutionRoot(input)
	if err != nil {
		return err
	}
	sessionID := plan.Descriptor.SessionID().String()
	startLogLines := []string{fmt.Sprintf(
		"workflow.runtime.start run_id=%s task_id=%s session_id=%s node_id=%s execution_root=%s model=%s",
		req.RunID,
		req.TaskID,
		sessionID,
		req.NodeID,
		executionRoot.EffectiveRoot(),
		plan.ActiveSettings.Model,
	)}
	for _, warning := range warnings {
		startLogLines = append(startLogLines, "workflow.runtime.warning "+warning)
	}
	workflowConfig, err := BuildWorkflowRuntimeConfig(
		input,
		effectiveMode,
		s.cfg.Settings.Workflow.MaxInvalidCompletionAttempts,
		workflowruntime.StoreController{Store: s.store, AttentionFinalizer: s.attentionFinalizer},
		s.store,
	)
	if err != nil {
		return err
	}
	runtimePlan, err := sessionruntime.NewAgentRuntimePlan(sessionruntime.AgentRuntimePlanOptions{
		Settings:                            plan.ActiveSettings,
		EnabledTools:                        workflowRuntimeEnabledTools(plan.EnabledTools),
		Workdir:                             executionRoot.EffectiveRoot(),
		Sources:                             plan.Source.Sources,
		Headless:                            true,
		Client:                              client,
		ReviewerClientFactory:               s.runtimeClientFactory,
		WorkflowRun:                         workflowConfig,
		SkipContinuationAgentRoleValidation: workflowRunPromptOverrides(input.Node.SubagentRole).HasAny(),
		StartLogLines:                       startLogLines,
		AskQuestionBatchSkipped: func(batch askquestion.AskQuestionBatchMetadata) {
			if attention, ok := s.runtimes.(workflowattention.QuestionAttentionRegistry); ok {
				if err := workflowattention.PrepareSkippedTaskQuestionBatch(attention, input, sessionID, req.RunID, batch, time.Now().UTC()); err != nil {
					slog.Warn("prepare skipped workflow question batch failed", "run_id", req.RunID, "task_id", req.TaskID, "batch_id", batch.BatchID, "prompt_id", batch.PromptID, "error", err)
				}
			}
		},
		OnEvent: func(evt runtime.Event) {
			if s.runtimes != nil {
				s.runtimes.PublishRuntimeEvent(sessionID, evt)
			}
		},
	})
	if err != nil {
		return err
	}
	workflowRef := sessionruntime.WorkflowExecutionRef{RunID: req.RunID, Generation: req.Generation}
	_, err = s.runtimeAuthority.StartAgentExecution(ctx, sessionruntime.AgentExecutionRequest{
		Descriptor: plan.Descriptor,
		Runtime:    &runtimePlan,
		Workflow:   &workflowRef,
		Resource:   sessionruntime.ReplaceAgentResource{},
		Ask: func(askCtx context.Context, scope sessionruntime.ExecutionScope, askReq askquestion.AskQuestionRequest) (askquestion.AskQuestionResponse, error) {
			return s.handleWorkflowAsk(askCtx, executionPromptAwaiter{authority: s.runtimeAuthority, scope: scope}, sessionID, req, input, askReq)
		},
		Runner: func(runCtx context.Context, _ sessionruntime.ExecutionScope, bridge sessionruntime.AgentRuntimeBridge) error {
			turnErr := bridge.WithEngine(runCtx, func(engineCtx context.Context, engine *runtime.Engine) error {
				if input.ContextMode == workflow.ContextModeCompactAndContinueSession &&
					engine.LastCompactionWorkflowRunID() != string(req.RunID) {
					if err := engine.CompactContext(engineCtx, ""); err != nil {
						return err
					}
				}
				_, submitErr := engine.SubmitWorkflowTurn(engineCtx)
				return submitErr
			})
			if turnErr != nil {
				reason := ReasonRuntimeFailed
				if errors.Is(turnErr, context.Canceled) || context.Cause(runCtx) != nil {
					reason = ReasonRuntimeCanceled
				}
				s.interrupt(context.Background(), req.RunID, req.Generation, reason, turnErr)
			}
			return turnErr
		},
	})
	return err
}

type executionPromptAwaiter struct {
	authority *sessionruntime.Authority
	scope     sessionruntime.ExecutionScope
}

func (a executionPromptAwaiter) AwaitPromptResponse(ctx context.Context, _ string, req askquestion.AskQuestionRequest) (askquestion.AskQuestionResponse, error) {
	return a.authority.AwaitPromptResponse(ctx, a.scope.ID(), req)
}

func (s *Starter) handleWorkflowAsk(ctx context.Context, awaiter workflowattention.QuestionAwaiter, sessionID string, req SchedulerStartRunRequest, input workflowstore.RunStartContext, askReq askquestion.AskQuestionRequest) (askquestion.AskQuestionResponse, error) {
	if askReq.Approval {
		var approvalAttention workflowattention.ApprovalQuestionAttentionRegistry
		if registry, ok := s.runtimes.(workflowattention.ApprovalQuestionAttentionRegistry); ok {
			approvalAttention = registry
		}
		return workflowattention.HandleTaskApprovalQuestion(ctx, s.store, awaiter, approvalAttention, workflowattention.TaskQuestionRequest{
			SessionID:  sessionID,
			RunID:      req.RunID,
			Generation: req.Generation,
			Input:      input,
			Question:   askReq,
		})
	}
	var attention workflowattention.QuestionAttentionRegistry
	if registry, ok := s.runtimes.(workflowattention.QuestionAttentionRegistry); ok {
		attention = registry
	}
	return workflowattention.HandleTaskQuestion(ctx, s.store, awaiter, attention, workflowattention.TaskQuestionRequest{
		SessionID:  sessionID,
		RunID:      req.RunID,
		Generation: req.Generation,
		Input:      input,
		Question:   askReq,
	})
}

func workflowRuntimeEnabledTools(enabled []toolspec.ID) []toolspec.ID {
	out := make([]toolspec.ID, 0, len(enabled))
	for _, id := range enabled {
		out = append(out, id)
	}
	return out
}

func (s *Starter) interrupt(ctx context.Context, runID workflow.RunID, generation int64, reason string, cause error) {
	detail := "{}"
	if cause != nil {
		if detailed, ok := cause.(interface{ InterruptionDetailJSON() string }); ok && strings.TrimSpace(detailed.InterruptionDetailJSON()) != "" {
			detail = detailed.InterruptionDetailJSON()
		} else if raw, err := json.Marshal(map[string]string{"error": cause.Error()}); err == nil {
			detail = string(raw)
		}
	}
	if err := s.store.InterruptRunGeneration(ctx, runID, generation, reason, detail); err != nil {
		return
	}
	if !workflowattention.ShouldNotifyInterruptedRun(reason) {
		return
	}
	if finalizer, ok := s.attentionFinalizer.(workflowInterruptedRunFinalizer); ok {
		finalizer.FinalizeInterruptedRun(ctx, runID)
	}
}

func BuildWorkflowTaskInstructions(input workflowstore.RunStartContext) (workflowruntime.TaskInstructions, error) {
	nodePrompt, err := renderTransitionPrompt(input.PromptTemplate, input)
	if err != nil {
		return workflowruntime.TaskInstructions{}, err
	}
	taskShortID := strings.TrimSpace(input.Task.ShortID)
	if taskShortID == "" {
		taskShortID = string(input.Task.ID)
	}
	workflowShortID := strings.TrimSpace(string(input.Workflow.ID))
	if workflowShortID == "" {
		workflowShortID = string(input.Task.WorkflowID)
	}
	return workflowruntime.TaskInstructions{
		TaskID:          string(input.Task.ID),
		TaskShortID:     taskShortID,
		TaskTitle:       strings.TrimSpace(input.Task.Title),
		TaskBody:        strings.TrimSpace(input.Task.Body),
		WorkflowID:      string(input.Task.WorkflowID),
		WorkflowShortID: workflowShortID,
		NodeID:          string(input.Node.ID),
		NodeKey:         string(input.Node.Key),
		NodeDisplayName: strings.TrimSpace(input.Node.DisplayName),
		ContextMode:     string(input.ContextMode),
		SourceSessionID: strings.TrimSpace(input.SourceSessionID),
		Transitions:     workflowInstructionTransitions(input.TransitionOptions, input.TransitionIDs),
		NodePrompt:      nodePrompt,
	}, nil
}

func workflowTransitions(options []workflowstore.TransitionOption, transitionIDs []string) []prompts.WorkflowTransition {
	capacity := len(options)
	if len(transitionIDs) > capacity {
		capacity = len(transitionIDs)
	}
	out := make([]prompts.WorkflowTransition, 0, capacity)
	if len(options) > 0 {
		for _, option := range options {
			id := strings.TrimSpace(option.ID)
			if id == "" {
				continue
			}
			out = append(out, prompts.WorkflowTransition{ID: id, DisplayName: strings.TrimSpace(option.DisplayName), Description: strings.TrimSpace(option.Description)})
		}
		return out
	}
	for _, id := range transitionIDs {
		trimmed := strings.TrimSpace(id)
		if trimmed != "" {
			out = append(out, prompts.WorkflowTransition{ID: trimmed})
		}
	}
	return out
}

func workflowInstructionTransitions(options []workflowstore.TransitionOption, transitionIDs []string) []workflowruntime.TransitionInstruction {
	transitions := workflowTransitions(options, transitionIDs)
	out := make([]workflowruntime.TransitionInstruction, 0, len(transitions))
	for _, transition := range transitions {
		out = append(out, workflowruntime.TransitionInstruction{ID: transition.ID, DisplayName: transition.DisplayName, Description: transition.Description})
	}
	return out
}

func workflowCompletionContractForRun(run workflowstore.RunRecord, input workflowstore.RunStartContext) workflowruntime.CompletionContract {
	return workflowruntime.CompletionContract{
		RunID:              run.ID,
		ExpectedGeneration: run.Generation,
		RequireGeneration:  true,
		Transitions:        workflowCompletionTransitions(input.TransitionOptions, input.TransitionIDs),
	}
}

func workflowCompletionTransitions(options []workflowstore.TransitionOption, transitionIDs []string) []workflowruntime.CompletionTransition {
	out := make([]workflowruntime.CompletionTransition, 0, len(options))
	if len(options) > 0 {
		for _, option := range options {
			id := strings.TrimSpace(option.ID)
			if id == "" {
				continue
			}
			out = append(out, workflowruntime.CompletionTransition{
				ID:          id,
				DisplayName: strings.TrimSpace(option.DisplayName),
				Description: strings.TrimSpace(option.Description),
				Parameters:  append([]workflow.Parameter(nil), option.Parameters...),
			})
		}
		return out
	}
	for _, id := range transitionIDs {
		trimmed := strings.TrimSpace(id)
		if trimmed != "" {
			out = append(out, workflowruntime.CompletionTransition{ID: trimmed})
		}
	}
	return out
}

type nodePromptTemplateData struct {
	TaskId          string
	TaskShortId     string
	TaskTitle       string
	TaskBody        string
	NodeId          string
	NodeKey         string
	NodeDisplayName string
	Params          map[string]promptParameterNamespace
}

const currentParameterValueKey = "\x00current"

type promptParameterNamespace map[string]string

func (n promptParameterNamespace) String() string {
	return n[currentParameterValueKey]
}

func renderTransitionPrompt(templateText string, input workflowstore.RunStartContext) (string, error) {
	prompt := strings.TrimSpace(templateText)
	if prompt == "" {
		return "", nil
	}
	tmpl, err := template.New("workflow transition prompt").Option("missingkey=error").Parse(prompt)
	if err != nil {
		return "", fmt.Errorf("parse workflow transition prompt template: %w", err)
	}
	var b strings.Builder
	if err := tmpl.Execute(&b, nodePromptTemplateData{
		TaskId:          string(input.Task.ID),
		TaskShortId:     strings.TrimSpace(input.Task.ShortID),
		TaskTitle:       strings.TrimSpace(input.Task.Title),
		TaskBody:        strings.TrimSpace(input.Task.Body),
		NodeId:          string(input.Node.ID),
		NodeKey:         string(input.Node.Key),
		NodeDisplayName: strings.TrimSpace(input.Node.DisplayName),
		Params:          promptParameterData(input.ParameterValues, input.PriorParameterValues),
	}); err != nil {
		return "", fmt.Errorf("render workflow transition prompt template: %w", err)
	}
	return b.String(), nil
}

func promptParameterData(current map[string]string, prior map[string]map[string]string) map[string]promptParameterNamespace {
	out := map[string]promptParameterNamespace{}
	out[workflow.RuntimePromptParameterCommentary] = promptParameterNamespace{currentParameterValueKey: ""}
	for transitionKey, values := range prior {
		key := strings.TrimSpace(transitionKey)
		if key == "" {
			continue
		}
		namespace := out[key]
		if namespace == nil {
			namespace = promptParameterNamespace{}
		}
		for parameterKey, value := range values {
			trimmedParameterKey := strings.TrimSpace(parameterKey)
			if trimmedParameterKey != "" {
				namespace[trimmedParameterKey] = value
			}
		}
		out[key] = namespace
	}
	for parameterKey, value := range current {
		key := strings.TrimSpace(parameterKey)
		if key == "" {
			continue
		}
		namespace := out[key]
		if namespace == nil {
			namespace = promptParameterNamespace{}
		}
		namespace[currentParameterValueKey] = value
		out[key] = namespace
	}
	return out
}

var _ SchedulerRuntimeStarter = (*Starter)(nil)
