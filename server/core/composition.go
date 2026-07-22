package core

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"core/prompts"
	"core/server/attentionnotify"
	"core/server/authservice"
	serverbootstrap "core/server/bootstrap"
	"core/server/capabilityfacts"
	"core/server/metadata"

	"core/server/processview"
	"core/server/projectview"
	"core/server/promptcontrol"
	"core/server/registry"
	"core/server/runtime"
	"core/server/runtimecontrol"
	"core/server/runtimeops"
	"core/server/runtimewire"
	"core/server/serverstatus"
	"core/server/sessionruntime"
	"core/server/sessionservice"
	"core/server/sessionview"
	"core/server/sleepguard"
	askquestion "core/server/tools"

	"core/server/workflow"
	"core/server/workflowattention"
	"core/server/workflowrunner"
	"core/server/workflowstore"
	"core/server/workflowsvc"
	"core/server/workflowview"
	"core/server/worktree"
	rpccontract "core/shared/apicontract"
	"core/shared/config"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

func New(cfg config.App, authSupport serverbootstrap.AuthSupport, runtimeSupport serverbootstrap.RuntimeSupport) (*Core, error) {
	return NewWithContext(context.Background(), cfg, authSupport, runtimeSupport)
}

func NewWithContext(ctx context.Context, cfg config.App, authSupport serverbootstrap.AuthSupport, runtimeSupport serverbootstrap.RuntimeSupport) (*Core, error) {
	return NewWithContextOptions(ctx, cfg, authSupport, runtimeSupport, Options{})
}

type Options struct {
	RuntimeClientFactory runtimewire.RuntimeClientFactory
	RootLease            *RootLockLease
}

func NewWithContextOptions(ctx context.Context, cfg config.App, authSupport serverbootstrap.AuthSupport, runtimeSupport serverbootstrap.RuntimeSupport, opts Options) (*Core, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	rootLease := opts.RootLease
	ownsIncomingRootLease := rootLease != nil
	if rootLease == nil {
		var err error
		rootLease, err = AcquireRootLock(cfg.PersistenceRoot)
		if err != nil {
			return nil, fmt.Errorf("persistence bundle: root lock: %w", err)
		}
	}
	closeRootLeaseOnFailure := func() {
		if !ownsIncomingRootLease {
			_ = rootLease.Close()
		}
	}
	generatedSupport, err := serverbootstrap.BuildGeneratedSupport(ctx, cfg.PersistenceRoot)
	if err != nil {
		closeRootLeaseOnFailure()
		return nil, fmt.Errorf("persistence bundle: generated support: %w", err)
	}
	runtimeSupport.Generated = generatedSupport
	metadataStore, err := metadata.Open(cfg.PersistenceRoot)
	if err != nil {
		closeRootLeaseOnFailure()
		return nil, fmt.Errorf("persistence bundle: metadata store: %w", err)
	}
	if err := validateAuthBundleSupport(authSupport); err != nil {
		closeRootLeaseOnFailure()
		_ = metadataStore.Close()
		return nil, err
	}
	if err := validateRuntimeBundleSupport(runtimeSupport); err != nil {
		closeRootLeaseOnFailure()
		_ = metadataStore.Close()
		return nil, err
	}
	storeOptions := metadataStore.AuthoritativeSessionStoreOptions()
	attentionBroker := attentionnotify.NewBroker()
	runtimeRegistry := registry.NewRuntimeRegistry().WithAttentionNotifications(attentionBroker)
	runtimeRegistry.WithTranscriptContractViolationPanic(cfg.Settings.Debug)
	var workflowScheduler *workflowrunner.SchedulerService
	runtimeAuthority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{
		PersistenceRoot: cfg.PersistenceRoot,
		AuthManager:     authSupport.AuthManager,
		Background:      runtimeSupport.Background,
		StoreOptions:    storeOptions,
		PromptFeed:      runtimeRegistry,
		EventFeed: func(resource sessionruntime.AgentResourceDescriptor, event runtime.Event) {
			runtimeRegistry.PublishAuthorityRuntimeEvent(resource.Ref, event)
		},
		ResourceLifecycle: runtimeRegistry,
		StepLifecycle:     authorityStepLifecycle{registry: runtimeRegistry},
		ExecutionFinalized: sessionruntime.ExecutionFinalizedFunc(func(ref sessionruntime.WorkflowExecutionRef) {
			if workflowScheduler != nil {
				workflowScheduler.RuntimeFinished(ref.RunID, ref.Generation)
			}
		}),
	})
	sleepManager, sleepErr := sleepguard.NewManager(cfg.Settings.PreventSleep, func(err error) {
		runtimeRegistry.PublishRuntimeEventToAll(runtime.Event{
			Kind:  runtime.EventSleepGuardFailed,
			Error: err.Error(),
		})
	})
	if sleepErr != nil {
		fmt.Fprintf(os.Stderr, "sleepguard: always-mode acquire failed at startup: %v\n", sleepErr)
	}
	if observer := sleepManager.RuntimeActiveObserver(); observer != nil {
		runtimeRegistry.SetSleepObserver(observer)
	}
	projectService, err := projectview.NewMetadataService(metadataStore, "")
	if err != nil {
		closeRootLeaseOnFailure()
		_ = metadataStore.Close()
		return nil, fmt.Errorf("projects bundle: metadata service: %w", err)
	}
	capabilityFactsService := capabilityfacts.NewService(capabilityfacts.Options{Config: cfg, AuthManager: authSupport.AuthManager})
	askService := promptcontrol.NewAskViewService(runtimeRegistry)
	approvalService := promptcontrol.NewApprovalViewService(runtimeRegistry)
	processService := processview.NewProcessViewService(runtimeSupport.Background)
	processOutputService := processview.NewProcessOutputService(runtimeSupport.Background, runtimeSupport.Background)
	sessionRuntimeAPI := sessionruntime.NewAPI(metadataStore, runtimeSupport.FastModeState, runtimeAuthority, sessionruntime.APIOptions{
		RuntimeClientFactory: opts.RuntimeClientFactory,
		RecoveredWarningProvider: func() (string, bool, error) {
			nonEmpty, err := prompts.RecoveredRootNonEmptyFor(cfg.PersistenceRoot)
			if err != nil {
				return "", false, err
			}
			if !nonEmpty {
				return "", false, nil
			}
			warning, warnErr := prompts.RecoveredWarningFor(cfg.PersistenceRoot)
			if warnErr != nil {
				return "", false, warnErr
			}
			return warning, true, nil
		},
	})
	projectService.WithRuntimeAuthority(runtimeAuthority)
	sessionStoreResolver := registry.NewGlobalPersistenceSessionResolver(cfg.PersistenceRoot, storeOptions...)
	promptControlService := promptcontrol.NewPromptControlService(authorityPromptResponder{authority: runtimeAuthority})
	runtimeOperations := runtimeops.NewCoordinator()
	runtimeRegistry.WithOperationCoordinator(runtimeOperations)
	runtimeRegistry.WithExecutionTargetResolver(metadataStore.ResolveSessionExecutionTarget)
	runtimeControlService := runtimecontrol.NewService(runtimeAuthority).
		WithRuntimeActivityResolver(runtimeRegistry).
		WithOperationCoordinator(runtimeOperations).
		WithPromptHistoryStore(metadataStore).
		WithWorkflowSessionResolver(sessionStoreResolver).
		WithPersistedSessionResolver(metadataStore)
	gitInspector := worktree.NewGitInspector(nil)
	worktreeService := worktree.NewService(metadataStore, gitInspector, runtimeAuthority, runtimeRegistry, runtimeSupport.Background, worktree.ServiceOptions{
		BaseDir: cfg.Settings.Worktrees.BaseDir,
		ResolveSetup: func(sourceWorkspaceRoot string) (config.WorktreeSettings, error) {
			return config.LoadWorktreeSetupSettings(sourceWorkspaceRoot, cfg.PersistenceRoot)
		},
	})
	projectViews := projectService
	authBootstrapService := authservice.NewBootstrapService(authSupport.AuthManager, authSupport.OAuthOptions, cfg.Settings, rpccontract.AllowedPreAuthMethods())
	authStatusService := authservice.NewStatusService(authSupport.AuthManager, cfg.Settings)
	updateStatusService := serverstatus.NewUpdateStatusService(config.Version, cfg.Settings.Debug)
	serverStatusService := serverstatus.NewServerStatusService(authSupport.AuthManager, cfg, updateStatusService)
	sessionViewService := sessionview.NewService(sessionStoreResolver, runtimeRegistry, runtimeAuthority, metadataStore).
		WithExecutionEnvironmentConfig(cfg).
		WithExecutionEnvironmentAuth(authStatusService).
		WithExecutionEnvironmentGit(gitInspector).
		WithOperationCoordinator(runtimeOperations).
		WithCacheWarningMode(cfg.Settings.CacheWarningMode)
	sessionWorkspaceRetargeter := sessionservice.NewSessionWorkspaceRetargeter(metadataStore, runtimeAuthority, runtimeRegistry, runtimeSupport.Background)
	sessionLifecycleService := sessionservice.NewGlobalSessionLifecycleService(cfg.PersistenceRoot, runtimeAuthority, authSupport.AuthManager).
		WithWorkspaceRetargeter(sessionWorkspaceRetargeter).
		WithNavigationTargetResolver(metadataStore)
	var workflowRuntimeStarter *workflowrunner.Starter
	cleanupNewFailure := func() {
		sleepManager.Close()
		_ = worktreeService.Close()
		if workflowScheduler != nil {
			_ = workflowScheduler.Close()
		}
		if workflowRuntimeStarter != nil {
			_ = workflowRuntimeStarter.Close()
		}
		_ = runtimeAuthority.Close(context.Background())
		closeRootLeaseOnFailure()
		_ = metadataStore.Close()
		if runtimeSupport.Background != nil {
			_ = runtimeSupport.Background.Close()
		}
	}
	workflowRoleResolver := configRoleResolver{settings: cfg.Settings}
	workflowStore, err := workflowstore.New(metadataStore, workflowstore.WithRoleResolver(workflowRoleResolver))
	if err != nil {
		cleanupNewFailure()
		return nil, fmt.Errorf("workflow bundle: store: %w", err)
	}
	workflowDefinitions, err := workflowview.NewDefinitionProjection(workflowStore)
	if err != nil {
		cleanupNewFailure()
		return nil, fmt.Errorf("workflow bundle: definitions: %w", err)
	}
	workflowTaskProjector := workflowview.NewTaskProjector()
	workflowBoard, err := workflowview.NewBoard(metadataStore, workflowDefinitions, workflowRoleResolver, workflowTaskProjector)
	if err != nil {
		cleanupNewFailure()
		return nil, fmt.Errorf("workflow bundle: board: %w", err)
	}
	workflowTaskList, err := workflowview.NewTaskList(metadataStore, workflowDefinitions, workflowTaskProjector)
	if err != nil {
		cleanupNewFailure()
		return nil, fmt.Errorf("workflow bundle: task list: %w", err)
	}
	workflowTaskDetail, err := workflowview.NewTaskDetail(metadataStore, workflowTaskProjector, runtimeAuthority)
	if err != nil {
		cleanupNewFailure()
		return nil, fmt.Errorf("workflow bundle: task detail: %w", err)
	}
	workflowActivity, err := workflowview.NewActivity(metadataStore, workflowDefinitions, workflowTaskProjector)
	if err != nil {
		cleanupNewFailure()
		return nil, fmt.Errorf("workflow bundle: activity: %w", err)
	}
	workflowAttention, err := workflowview.NewAttention(
		metadataStore,
		workflowDefinitions,
		workflowTaskProjector,
		workflowRoleResolver,
		workflowViewActiveTranscriptSource{views: sessionViewService},
		workflowViewPendingPromptSource{prompts: runtimeRegistry},
	)
	if err != nil {
		cleanupNewFailure()
		return nil, fmt.Errorf("workflow bundle: attention: %w", err)
	}
	workflowAttentionFinalizer := workflowattention.NewFinalizer(workflowApprovalProjection{store: workflowStore}, attentionBroker)
	automaticIntents := workflowrunner.NewAutomaticIntents()
	workflowRuntimeStarter, err = workflowrunner.NewStarter(cfg, metadataStore, workflowStore, authSupport.AuthManager, runtimeRegistry, workflowrunner.StarterOptions{RuntimeClientFactory: opts.RuntimeClientFactory, Worktrees: runtimeTaskWorktreeRestorer{service: worktreeService}, RuntimeAuthority: runtimeAuthority, AttentionFinalizer: workflowAttentionFinalizer, AutomaticIntents: automaticIntents})
	if err != nil {
		cleanupNewFailure()
		return nil, fmt.Errorf("workflow bundle: runtime starter: %w", err)
	}
	workflowScheduler, err = workflowrunner.NewSchedulerService(workflowStore, workflowRuntimeStarter, workflowrunner.SchedulerConfig{Concurrency: cfg.Settings.Workflow.Concurrency}, workflowrunner.WithSchedulerPendingAskResolver(runtimePendingAskResolver{prompts: runtimeRegistry}), workflowrunner.WithSchedulerAttentionFinalizer(workflowAttentionFinalizer), workflowrunner.WithAutomaticIntents(automaticIntents))
	if err != nil {
		cleanupNewFailure()
		return nil, fmt.Errorf("workflow bundle: scheduler: %w", err)
	}
	workflowService, err := workflowsvc.New(workflowStore, workflowsvc.ReadModels{
		Definitions: workflowDefinitions,
		Board:       workflowBoard,
		TaskList:    workflowTaskList,
		TaskDetail:  workflowTaskDetail,
		Activity:    workflowActivity,
		Attention:   workflowAttention,
	}, workflowRoleResolver, workflowsvc.WithExecutionTargetInfrastructure(taskExecutionTargetInfrastructure{service: worktreeService, git: gitInspector}), workflowsvc.WithTaskWorktreeDeleter(taskWorktreeDeleter{service: worktreeService}), workflowsvc.WithTaskRuntimeCanceler(workflowRuntimeStarter), workflowsvc.WithSchedulerNotifier(workflowScheduler), workflowsvc.WithPromptResponder(authorityPromptResponder{authority: runtimeAuthority}), workflowsvc.WithWorkflowAttentionFinalizer(workflowAttentionFinalizer))
	if err != nil {
		cleanupNewFailure()
		return nil, fmt.Errorf("workflow bundle: service: %w", err)
	}
	core := &Core{bundles: composeBundles(bundleCompositionInput{
		cfg:                     cfg,
		authSupport:             authSupport,
		capabilityFactsService:  capabilityFactsService,
		runtimeSupport:          runtimeSupport,
		rootLease:               rootLease,
		metadataStore:           metadataStore,
		runtimeRegistry:         runtimeRegistry,
		runtimeAuthority:        runtimeAuthority,
		projectViews:            projectViews,
		authBootstrapService:    authBootstrapService,
		authStatusService:       authStatusService,
		askService:              askService,
		approvalService:         approvalService,
		processService:          processService,
		processOutputService:    processOutputService,
		promptControlService:    promptControlService,
		attentionService:        runtimeRegistry,
		runtimeControlService:   runtimeControlService,
		serverStatusService:     serverStatusService,
		sessionRuntimeAPI:       sessionRuntimeAPI,
		sessionViewService:      sessionViewService,
		sessionLifecycleService: sessionLifecycleService,
		updateStatusService:     updateStatusService,
		workflowService:         workflowService,
		workflowScheduler:       workflowScheduler,
		workflowRuntimeStarter:  workflowRuntimeStarter,
		worktreeService:         worktreeService,
		sleepManager:            sleepManager,
	})}
	if strings.TrimSpace(cfg.WorkspaceRoot) != "" {
		binding, err := metadataStore.EnsureWorkspaceBinding(context.Background(), cfg.WorkspaceRoot)
		if err != nil && !errors.Is(err, serverapi.ErrWorkspaceNotRegistered) {
			_ = core.Close()
			return nil, fmt.Errorf("projects bundle: workspace binding: %w", err)
		}
		if err == nil {
			core.bundles.Projects.projectID = binding.ProjectID
			projectSessionsDir := filepath.Join(filepath.Join(cfg.PersistenceRoot, "projects"), binding.ProjectID, "sessions")
			if err := os.MkdirAll(projectSessionsDir, 0o755); err != nil {
				_ = core.Close()
				return nil, fmt.Errorf("projects bundle: sessions root: %w", err)
			}
			core.bundles.Sessions.sessionLaunch, err = core.SessionLaunchClientForProjectWorkspace(context.Background(), binding.ProjectID, cfg.WorkspaceRoot)
			if err != nil {
				_ = core.Close()
				return nil, fmt.Errorf("sessions bundle: session launch client: %w", err)
			}
			core.bundles.Sessions.runPrompt, err = core.RunPromptClientForProjectWorkspace(context.Background(), binding.ProjectID, cfg.WorkspaceRoot)
			if err != nil {
				_ = core.Close()
				return nil, fmt.Errorf("sessions bundle: run prompt client: %w", err)
			}
		}
	}
	if err := workflowScheduler.Start(context.Background()); err != nil {
		_ = core.Close()
		return nil, fmt.Errorf("workflow bundle: scheduler start: %w", err)
	}
	return core, nil
}

type taskExecutionTargetInfrastructure struct {
	service *worktree.Service
	git     *worktree.GitInspector
}

func (i taskExecutionTargetInfrastructure) ResolveExecutionTarget(ctx context.Context, req workflowsvc.ExecutionTargetResolveRequest) (workflowstore.ExecutionTargetSnapshot, error) {
	if i.git == nil {
		return workflowstore.ExecutionTargetSnapshot{}, errors.New("git inspector is required")
	}
	if err := req.Selection.Validate(); err != nil {
		return workflowstore.ExecutionTargetSnapshot{}, err
	}
	var revision worktree.GitRevision
	var err error
	switch req.Selection.Mode {
	case workflow.ExecutionTargetModeHead:
		revision, err = i.git.ResolveHEAD(ctx, req.SourceWorkspaceRoot)
	case workflow.ExecutionTargetModeDefaultBranch:
		var defaultBranch worktree.GitDefaultBranch
		defaultBranch, err = i.git.ResolveDefaultBranch(ctx, req.SourceWorkspaceRoot)
		if err == nil {
			revision, err = i.git.ResolveRevision(ctx, req.SourceWorkspaceRoot, defaultBranch.Ref)
		}
	case workflow.ExecutionTargetModeCustomRef:
		if req.Selection.CustomRef == nil {
			return workflowstore.ExecutionTargetSnapshot{}, errors.New("custom execution target ref is required")
		}
		revision, err = i.git.ResolveRevision(ctx, req.SourceWorkspaceRoot, *req.Selection.CustomRef)
	default:
		return workflowstore.ExecutionTargetSnapshot{}, fmt.Errorf("execution target mode %q is not managed", req.Selection.Mode)
	}
	if err != nil {
		return workflowstore.ExecutionTargetSnapshot{}, err
	}
	requestedRef := revision.RequestedRef
	commitOID := revision.CommitOID
	return workflowstore.ExecutionTargetSnapshot{
		Mode:         req.Selection.Mode,
		RequestedRef: &requestedRef,
		ResolvedRef:  revision.CanonicalRef,
		CommitOID:    &commitOID,
		Provenance:   workflowstore.ExecutionTargetProvenanceResolved,
	}, nil
}

func (i taskExecutionTargetInfrastructure) MaterializeExecutionTarget(ctx context.Context, req workflowsvc.ExecutionTargetMaterializeRequest) (workflowstore.ManagedExecutionRoot, error) {
	if i.service == nil {
		return workflowstore.ManagedExecutionRoot{}, errors.New("worktree service is required")
	}
	if err := req.Snapshot.Validate(); err != nil {
		return workflowstore.ManagedExecutionRoot{}, err
	}
	if req.Snapshot.RequestedRef == nil || req.Snapshot.CommitOID == nil {
		return workflowstore.ManagedExecutionRoot{}, errors.New("managed execution target snapshot is incomplete")
	}
	materialized, err := i.service.MaterializeInitialTaskWorktree(ctx, worktree.InitialTaskWorktreeMaterializationRequest{
		TaskID:           req.TaskID,
		SetupOperationID: req.SetupOperationID,
		ResolvedTarget: worktree.GitRevision{
			RequestedRef: *req.Snapshot.RequestedRef,
			CommitOID:    *req.Snapshot.CommitOID,
			CanonicalRef: req.Snapshot.ResolvedRef,
		},
	})
	if err != nil {
		return workflowstore.ManagedExecutionRoot{}, err
	}
	if materialized.Worktree.Variant != serverapi.WorktreeTopologyVariantRegistered || materialized.Worktree.Registered == nil {
		return workflowstore.ManagedExecutionRoot{}, errors.New("materialized task worktree is not registered")
	}
	return workflowstore.ManagedExecutionRoot{
		WorktreeID: materialized.Worktree.Registered.Kent.WorktreeID,
		Root:       materialized.Worktree.Registered.Git.CanonicalRoot,
	}, nil
}

func (i taskExecutionTargetInfrastructure) RestoreExecutionTarget(ctx context.Context, req workflowsvc.ExecutionTargetRestoreRequest) error {
	if i.service == nil {
		return errors.New("worktree service is required")
	}
	_, err := i.service.RestoreLockedTaskWorktree(ctx, worktree.LockedTaskWorktreeRestoreRequest{
		TaskID:           req.TaskID,
		SetupOperationID: req.SetupOperationID,
	})
	return err
}

type runtimeTaskWorktreeRestorer struct {
	service *worktree.Service
}

func (r runtimeTaskWorktreeRestorer) RestoreLockedTaskWorktree(ctx context.Context, req workflowrunner.LockedTaskWorktreeRestoreRequest) error {
	if r.service == nil {
		return errors.New("worktree service is required")
	}
	_, err := r.service.RestoreLockedTaskWorktree(ctx, worktree.LockedTaskWorktreeRestoreRequest{TaskID: req.TaskID, SetupOperationID: req.SetupOperationID})
	return err
}

type workflowApprovalProjection struct {
	store *workflowstore.Store
}

func (p workflowApprovalProjection) PendingApprovalProjection(ctx context.Context, transitionID workflow.TransitionID) (workflowattention.ApprovalProjection, bool, error) {
	if p.store == nil {
		return workflowattention.ApprovalProjection{}, false, nil
	}
	projection, ok, err := p.store.PendingApprovalTransitionProjection(ctx, transitionID)
	if err != nil {
		return workflowattention.ApprovalProjection{}, false, err
	}
	if !ok {
		return workflowattention.ApprovalProjection{}, false, nil
	}
	return workflowattention.ApprovalProjectionFromStore(projection), true, nil
}

func (p workflowApprovalProjection) PendingInterruptedRunProjection(ctx context.Context, runID workflow.RunID) (workflowattention.InterruptedRunProjection, bool, error) {
	if p.store == nil || runID == "" {
		return workflowattention.InterruptedRunProjection{}, false, nil
	}
	projection, ok, err := p.store.PendingInterruptedRunAttentionProjection(ctx, runID)
	if err != nil {
		return workflowattention.InterruptedRunProjection{}, false, err
	}
	if !ok {
		return workflowattention.InterruptedRunProjection{}, false, nil
	}
	return workflowattention.InterruptedRunProjectionFromStore(projection), true, nil
}

type taskWorktreeDeleter struct {
	service *worktree.Service
}

func (d taskWorktreeDeleter) EnsureTaskWorktreeDeletable(ctx context.Context, taskID string) error {
	if d.service == nil {
		return nil
	}
	return d.service.EnsureTaskWorktreeDeletable(ctx, taskID)
}

func (d taskWorktreeDeleter) DeleteTaskWorktree(ctx context.Context, taskID string) error {
	if d.service == nil {
		return nil
	}
	_, err := d.service.DeleteTaskWorktree(ctx, worktree.DeleteTaskWorktreeRequest{TaskID: taskID})
	return err
}

type authorityPromptResponder struct {
	authority *sessionruntime.Authority
}

func (r authorityPromptResponder) SubmitPromptResponse(sessionID string, response askquestion.AskQuestionResponse, submitErr error) error {
	id, err := runtimeids.ParseSessionID(strings.TrimSpace(sessionID))
	if err != nil {
		return err
	}
	return r.authority.SubmitPromptResponse(id, response, submitErr)
}

type authorityStepLifecycle struct {
	registry *registry.RuntimeRegistry
}

func (s authorityStepLifecycle) StepBegan(ctx context.Context, resource sessionruntime.AgentResourceDescriptor, _ sessionruntime.ExecutionScope, snapshot runtime.StepLifecycleSnapshot) error {
	return runtimewire.NewStepLifecycleSink(resource.Ref.SessionID().String(), s.registry).StepBegan(ctx, snapshot)
}

func (s authorityStepLifecycle) StepEnded(ctx context.Context, resource sessionruntime.AgentResourceDescriptor, _ sessionruntime.ExecutionScope, snapshot runtime.StepLifecycleSnapshot) error {
	return runtimewire.NewStepLifecycleSink(resource.Ref.SessionID().String(), s.registry).StepEnded(ctx, snapshot)
}

type runtimePendingAskResolver struct {
	prompts interface {
		ListPendingPrompts(sessionID string) []registry.PendingPromptSnapshot
	}
}

type workflowViewPendingPromptSource struct {
	prompts interface {
		ListPendingPrompts(sessionID string) []registry.PendingPromptSnapshot
	}
}

type workflowViewActiveTranscriptSource struct {
	views *sessionview.Service
}

func (s workflowViewActiveTranscriptSource) SessionNewestActiveSegmentEntries(ctx context.Context, sessionID string) ([]runtime.ChatEntry, error) {
	if s.views == nil {
		return nil, errors.New("session view service is required")
	}
	return s.views.SessionTranscriptTailEntries(ctx, sessionID)
}

func (s workflowViewPendingPromptSource) ListPendingPrompts(sessionID string) []workflowview.PendingPromptSnapshot {
	if s.prompts == nil {
		return nil
	}
	items := s.prompts.ListPendingPrompts(sessionID)
	out := make([]workflowview.PendingPromptSnapshot, 0, len(items))
	for _, item := range items {
		out = append(out, workflowview.PendingPromptSnapshot{Request: item.Request})
	}
	return out
}

func (r runtimePendingAskResolver) CanRehydrate(_ context.Context, sessionID string, _ workflow.RunID, askID string) (bool, error) {
	if r.prompts == nil || strings.TrimSpace(sessionID) == "" || strings.TrimSpace(askID) == "" {
		return false, nil
	}
	for _, item := range r.prompts.ListPendingPrompts(sessionID) {
		if strings.TrimSpace(item.Request.ID) != strings.TrimSpace(askID) {
			continue
		}
		if !item.Request.Approval {
			return true, nil
		}
		if taskScopedApprovalPendingAsk(item.Request, askID) {
			return true, nil
		}
	}
	return false, nil
}

func taskScopedApprovalPendingAsk(req askquestion.AskQuestionRequest, askID string) bool {
	if !req.IsTaskScopedApprovalQuestion() {
		return false
	}
	for _, focusedAskID := range req.AttentionTarget.Focus.AskIDs {
		if strings.TrimSpace(focusedAskID) == strings.TrimSpace(askID) {
			return true
		}
	}
	return false
}
