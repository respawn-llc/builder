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
	"core/server/runtimecommand"
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
	"core/server/workflowexecution"
	"core/server/workflowrunner"
	"core/server/workflowstore"
	"core/server/workflowsvc"
	"core/server/workflowview"
	"core/server/worktree"
	rpccontract "core/shared/apicontract"
	"core/shared/clientui"
	"core/shared/config"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/toolspec"
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
	var workflowController *workflowexecution.CurrentNodeController
	runtimeAuthority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{
		PersistenceRoot: cfg.PersistenceRoot,
		AuthManager:     authSupport.AuthManager,
		Background:      runtimeSupport.Background,
		StoreOptions:    storeOptions,
		PromptFeed:      runtimeRegistry,
		EventFeed: func(resource sessionruntime.AgentResourceDescriptor, event runtime.Event) {
			if err := runtimeRegistry.PublishAuthorityRuntimeEvent(resource.Ref, event); err != nil {
				if cfg.Settings.Debug {
					panic(fmt.Sprintf("publish runtime event for session resource %v: %v", resource.Ref, err))
				}
				fmt.Fprintf(os.Stderr, "publish runtime event for session resource %v: %v\n", resource.Ref, err)
			}
		},
		ResourceLifecycle: runtimeRegistry,
		StepLifecycle:     authorityStepLifecycle{registry: runtimeRegistry},
		ExecutionFinalized: sessionruntime.ExecutionFinalizedFunc(func(scope sessionruntime.ExecutionScope) {
			if workflowController != nil {
				workflowController.ExecutionFinalized(scope)
			}
		}),
	})
	sleepManager, sleepErr := sleepguard.NewManager(cfg.Settings.PreventSleep, func(err error) {
		if publishErr := runtimeRegistry.PublishRuntimeEventToAll(runtime.Event{
			Kind:  runtime.EventSleepGuardFailed,
			Error: err.Error(),
		}); publishErr != nil {
			if cfg.Settings.Debug {
				panic(fmt.Sprintf("publish sleep-guard runtime event: %v", publishErr))
			}
			fmt.Fprintf(os.Stderr, "publish sleep-guard runtime event: %v\n", publishErr)
		}
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
	runtimeRegistry.WithExecutionTargetResolver(metadataStore.ResolveOptionalSessionExecutionTarget)
	if runtimeSupport.Background != nil {
		runtimeRegistry.WithBackgroundProcessSnapshots(runtimeSupport.Background.List)
	}
	runtimeCommandExecution := runtimecommand.NewExecutionAdapter(runtimeAuthority)
	runtimeGoalAuthority := runtimecommand.NewGoalAuthority(runtimeAuthority, runtimeCommandExecution)
	runtimeControlService := runtimecontrol.NewServiceWithGoalCommands(runtimeAuthority, runtimeCommandExecution, runtimeGoalAuthority).
		WithRuntimeActivityResolver(runtimeRegistry).
		WithOperationCoordinator(runtimeOperations).
		WithPromptHistoryStore(metadataStore).
		WithWorkflowTaskSessionResolver(metadataStore).
		WithPersistedSessionResolver(metadataStore)
	runtimeControlService.WithPromptCommandResolver(promptCommandRuntimeResolver{
		effectiveWorkspace: promptCommandEffectiveWorkspaceResolver{
			persistenceRoot: cfg.PersistenceRoot,
		},
		metadataStore: metadataStore,
	})
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
		if workflowController != nil {
			_ = workflowController.Close()
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
	workflowActivity, err := workflowview.NewActivity(metadataStore, workflowTaskProjector)
	if err != nil {
		cleanupNewFailure()
		return nil, fmt.Errorf("workflow bundle: activity: %w", err)
	}
	workflowAttention, err := workflowview.NewAttention(
		metadataStore,
		workflowDefinitions,
		runtimeAuthority,
		workflowViewPendingPromptSource{prompts: runtimeRegistry},
	)
	if err != nil {
		cleanupNewFailure()
		return nil, fmt.Errorf("workflow bundle: attention: %w", err)
	}
	workflowAttentionFinalizer := workflowattention.NewFinalizer(workflowApprovalProjection{store: workflowStore}, attentionBroker)
	runtimeRegistry.WithWorkflowAttentionNotificationSnapshot(workflowAttentionNotificationSnapshotSource{
		attention: workflowAttention,
		finalizer: workflowAttentionFinalizer,
	})
	workflowTaskDependencyCounter, err := workflowview.NewTaskDependencyCounter(metadataStore)
	if err != nil {
		cleanupNewFailure()
		return nil, fmt.Errorf("workflow bundle: task dependencies: %w", err)
	}
	runtimeRegistry.WithWorkflowEventPublisher(workflowStore.PublishWorkflowEvent)
	workflowMutationPermit := workflowexecution.NewMutationPermit()
	workflowRuntimeStarter, err = workflowrunner.NewStarter(cfg, metadataStore, workflowStore, authSupport.AuthManager, runtimeRegistry, workflowrunner.StarterOptions{
		RuntimeClientFactory: opts.RuntimeClientFactory,
		RuntimeAuthority:     runtimeAuthority,
		MutationPermit:       workflowMutationPermit,
		TaskDependencies:     workflowTaskDependencyCounter,
		WorktreeService:      worktreeService,
		GitInspector:         gitInspector,
	})
	if err != nil {
		cleanupNewFailure()
		return nil, fmt.Errorf("workflow bundle: runtime starter: %w", err)
	}
	workflowController, err = workflowexecution.NewCurrentNodeController(
		workflowStore,
		workflowRuntimeStarter,
		runtimeAuthority,
		workflowMutationPermit,
		workflowexecution.CurrentNodeControllerConfig{
			AgentConcurrency:  cfg.Settings.Workflow.Concurrency,
			Attention:         workflowAttentionFinalizer,
			AssignmentSteerer: workflowRuntimeStarter,
		},
	)
	if err != nil {
		cleanupNewFailure()
		return nil, fmt.Errorf("workflow bundle: current node controller: %w", err)
	}
	if _, err := workflowController.Recover(context.Background()); err != nil {
		cleanupNewFailure()
		return nil, fmt.Errorf("workflow bundle: current node recovery: %w", err)
	}
	workflowTaskStatusProjection, err := workflowview.NewTaskStatusProjection(
		metadataStore,
		workflowStore,
		workflowTaskProjector,
		workflowController,
	)
	if err != nil {
		cleanupNewFailure()
		return nil, fmt.Errorf("workflow bundle: task status projection: %w", err)
	}
	workflowTaskList, err := workflowview.NewTaskList(metadataStore, workflowDefinitions, workflowTaskStatusProjection)
	if err != nil {
		cleanupNewFailure()
		return nil, fmt.Errorf("workflow bundle: task list: %w", err)
	}
	workflowTaskSearch, err := workflowview.NewTaskSearch(metadataStore, workflowTaskStatusProjection)
	if err != nil {
		cleanupNewFailure()
		return nil, fmt.Errorf("workflow bundle: task search: %w", err)
	}
	workflowTaskDependencies, err := workflowview.NewTaskDependencies(metadataStore, workflowTaskStatusProjection, workflowTaskDependencyCounter)
	if err != nil {
		cleanupNewFailure()
		return nil, fmt.Errorf("workflow bundle: task dependencies: %w", err)
	}
	workflowBoard, err := workflowview.NewBoard(metadataStore, workflowDefinitions, workflowRoleResolver, workflowTaskStatusProjection)
	if err != nil {
		cleanupNewFailure()
		return nil, fmt.Errorf("workflow bundle: board: %w", err)
	}
	workflowTaskDetail, err := workflowview.NewTaskDetail(metadataStore, workflowTaskStatusProjection, workflowTaskDependencies)
	if err != nil {
		cleanupNewFailure()
		return nil, fmt.Errorf("workflow bundle: task detail: %w", err)
	}
	projectService.WithWorkflowExecution(workflowMutationPermit, workflowController, workflowStore)
	workflowService, err := workflowsvc.New(workflowStore, workflowsvc.ReadModels{
		Definitions:      workflowDefinitions,
		Board:            workflowBoard,
		TaskList:         workflowTaskList,
		TaskSearch:       workflowTaskSearch,
		TaskDetail:       workflowTaskDetail,
		TaskDependencies: workflowTaskDependencies,
		Activity:         workflowActivity,
		Attention:        workflowAttention,
	}, workflowRoleResolver, workflowMutationPermit, workflowsvc.WithTaskWorktreeDeleter(taskWorktreeDeleter{service: worktreeService}), workflowsvc.WithCurrentNodeExecution(workflowController), workflowsvc.WithWorkflowAttentionFinalizer(workflowAttentionFinalizer))
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
		workflowController:      workflowController,
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
	return core, nil
}

type workflowApprovalProjection struct {
	store *workflowstore.Store
}

func (p workflowApprovalProjection) PendingApprovalProjection(ctx context.Context, approvalID workflow.ApprovalID) (workflowattention.ApprovalProjection, bool, error) {
	if p.store == nil {
		return workflowattention.ApprovalProjection{}, false, nil
	}
	projection, ok, err := p.store.PendingApprovalAttentionProjection(ctx, approvalID)
	if err != nil {
		return workflowattention.ApprovalProjection{}, false, err
	}
	if !ok {
		return workflowattention.ApprovalProjection{}, false, nil
	}
	return workflowattention.ApprovalProjectionFromStore(projection), true, nil
}

func (p workflowApprovalProjection) PendingInterruptedCurrentNodeProjection(ctx context.Context, currentNode workflow.CurrentNodeReference) (workflowattention.InterruptedCurrentNodeProjection, bool, error) {
	if p.store == nil {
		return workflowattention.InterruptedCurrentNodeProjection{}, false, nil
	}
	projection, ok, err := p.store.PendingInterruptedCurrentNodeAttentionProjection(ctx, currentNode)
	if err != nil {
		return workflowattention.InterruptedCurrentNodeProjection{}, false, err
	}
	if !ok {
		return workflowattention.InterruptedCurrentNodeProjection{}, false, nil
	}
	return workflowattention.InterruptedCurrentNodeProjectionFromStore(projection), true, nil
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

func (r authorityPromptResponder) AcceptPromptResponse(
	sessionID string,
	response askquestion.AskQuestionResponse,
	submitErr error,
) (promptcontrol.PromptResponseAcceptance, error) {
	id, err := runtimeids.ParseSessionID(strings.TrimSpace(sessionID))
	if err != nil {
		return nil, err
	}
	return r.authority.AcceptPromptResponse(id, response, submitErr)
}

type authorityStepLifecycle struct {
	registry *registry.RuntimeRegistry
}

func (s authorityStepLifecycle) StepBegan(ctx context.Context, resource sessionruntime.AgentResourceDescriptor, snapshot runtime.StepLifecycleSnapshot) error {
	return runtimewire.NewStepLifecycleSink(resource.Ref.SessionID().String(), s.registry).StepBegan(ctx, snapshot)
}

func (s authorityStepLifecycle) StepEnded(ctx context.Context, resource sessionruntime.AgentResourceDescriptor, snapshot runtime.StepLifecycleSnapshot) error {
	return runtimewire.NewStepLifecycleSink(resource.Ref.SessionID().String(), s.registry).StepEnded(ctx, snapshot)
}

type workflowViewPendingPromptSource struct {
	prompts interface {
		ListPendingPrompts(sessionID string) []registry.PendingPromptSnapshot
	}
}

type workflowViewActiveTranscriptSource struct {
	views *sessionview.Service
}

func (s workflowViewActiveTranscriptSource) SessionNewestActiveSegmentQuestions(ctx context.Context, sessionID string) ([]workflowview.PendingQuestionTranscriptEntry, error) {
	if s.views == nil {
		return nil, errors.New("session view service is required")
	}
	entries, err := s.views.SessionTranscriptTailEntries(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	questions := make([]workflowview.PendingQuestionTranscriptEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.Role != "tool_call" ||
			entry.ToolCall == nil ||
			entry.ToolCall.ToolName != string(toolspec.ToolAskQuestion) {
			continue
		}
		recommendedOptionIndex, err := promptcontrol.DecodeLegacyRecommendedOptionIndex(
			entry.ToolCall.RecommendedOptionIndex,
			len(entry.ToolCall.Suggestions),
		)
		if err != nil {
			return nil, fmt.Errorf("session %q pending ask %q: %w", sessionID, entry.ToolCallID, err)
		}
		questions = append(questions, workflowview.PendingQuestionTranscriptEntry{
			AskID:                  entry.ToolCallID,
			Question:               entry.ToolCall.Question,
			Suggestions:            append([]string(nil), entry.ToolCall.Suggestions...),
			RecommendedOptionIndex: recommendedOptionIndex,
		})
	}
	return questions, nil
}

func (s workflowViewPendingPromptSource) ListPendingPrompts(sessionID string) ([]workflowview.PendingPromptSnapshot, error) {
	if s.prompts == nil {
		return nil, nil
	}
	items := s.prompts.ListPendingPrompts(sessionID)
	out := make([]workflowview.PendingPromptSnapshot, 0, len(items))
	for _, item := range items {
		recommendedOptionIndex, err := promptcontrol.DecodeLegacyRecommendedOptionIndex(
			item.Request.RecommendedOptionIndex,
			len(item.Request.Suggestions),
		)
		if err != nil {
			return nil, fmt.Errorf("session %q pending prompt %q: %w", sessionID, item.Request.ID, err)
		}
		decisions := make([]clientui.ApprovalDecision, 0, len(item.Request.ApprovalOptions))
		for _, option := range item.Request.ApprovalOptions {
			decisions = append(decisions, clientui.ApprovalDecision(option.Decision))
		}
		out = append(out, workflowview.PendingPromptSnapshot{
			ID:                     item.Request.ID,
			CreatedAt:              item.CreatedAt,
			Question:               item.Request.Question,
			Suggestions:            append([]string(nil), item.Request.Suggestions...),
			RecommendedOptionIndex: recommendedOptionIndex,
			Approval:               item.Request.Approval,
			ApprovalDecisions:      decisions,
		})
	}
	return out, nil
}
