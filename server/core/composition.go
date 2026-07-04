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
	"core/shared/client"
	"core/shared/clientui"
	"core/shared/config"
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
	containerDir := ""
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
	sessionStoreRegistry := registry.NewSessionStoreRegistry()
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
	sessionRuntimeService := sessionruntime.NewServiceWithOptions(cfg.PersistenceRoot, metadataStore, authSupport.AuthManager, runtimeSupport.FastModeState, runtimeSupport.Background, runtimeSupport.BackgroundRouter, runtimeRegistry, sessionStoreRegistry, sessionruntime.ServiceOptions{RuntimeClientFactory: opts.RuntimeClientFactory}, storeOptions...).
		WithGeneratedRecoveredWarningProvider(func() (string, bool, error) {
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
		})
	projectService.WithRuntimeActivitySources(runtimeRegistry, sessionRuntimeService)
	sessionStoreResolver := registry.NewGlobalPersistenceSessionResolver(cfg.PersistenceRoot, storeOptions...)
	promptControlService := promptcontrol.NewPromptControlService(runtimeRegistry)
	promptActivityService := promptcontrol.NewPromptActivityService(runtimeRegistry)
	runtimeOperations := runtimeops.NewCoordinator()
	runtimeRegistry.WithOperationCoordinator(runtimeOperations)
	runtimeRegistry.WithExecutionTargetResolver(metadataStore.ResolveSessionExecutionTarget)
	runtimeControlService := runtimecontrol.NewService(runtimeRegistry).WithOperationCoordinator(runtimeOperations).WithPromptHistoryStore(metadataStore).WithWorkflowSessionResolver(sessionStoreResolver)
	worktreeService := worktree.NewService(metadataStore, nil, runtimeRegistry, sessionRuntimeService, runtimeSupport.Background, runtimeControlService, worktree.ServiceOptions{BaseDir: cfg.Settings.Worktrees.BaseDir, SetupScript: cfg.Settings.Worktrees.SetupScript})
	projectViews := client.NewLoopbackProjectViewClient(projectService)
	authBootstrapService := authservice.NewBootstrapService(authSupport.AuthManager, authSupport.OAuthOptions, cfg.Settings, rpccontract.AllowedPreAuthMethods())
	authStatusService := authservice.NewStatusService(authSupport.AuthManager, cfg.Settings)
	serverStatusService := serverstatus.NewServerStatusService(authSupport.AuthManager, cfg)
	updateStatusService := serverstatus.NewUpdateStatusService(config.Version)
	sessionViewService := sessionview.NewService(sessionStoreResolver, runtimeRegistry, metadataStore).WithOperationCoordinator(runtimeOperations).WithCacheWarningMode(cfg.Settings.CacheWarningMode).WithUpdateStatusProvider(updateStatusService)
	sessionLifecycleService := sessionservice.NewGlobalSessionLifecycleService(cfg.PersistenceRoot, sessionStoreRegistry, authSupport.AuthManager, storeOptions...)
	sessionActivityService := sessionservice.NewSessionActivityService(runtimeRegistry)
	var workflowRuntimeStarter *workflowrunner.Starter
	var workflowScheduler *workflowrunner.SchedulerService
	cleanupNewFailure := func() {
		sleepManager.Close()
		if workflowScheduler != nil {
			_ = workflowScheduler.Close()
		}
		if workflowRuntimeStarter != nil {
			_ = workflowRuntimeStarter.Close()
		}
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
	workflowViewService, err := workflowview.New(metadataStore, workflowview.WithSessionTranscriptProvider(sessionViewService), workflowview.WithPendingPromptSource(workflowViewPendingPromptSource{prompts: runtimeRegistry}))
	if err != nil {
		cleanupNewFailure()
		return nil, fmt.Errorf("workflow bundle: view: %w", err)
	}
	workflowAttentionFinalizer := workflowattention.NewFinalizer(workflowApprovalProjection{store: workflowStore, view: workflowViewService, roleResolver: workflowRoleResolver}, attentionBroker)
	workflowRuntimeStarter, err = workflowrunner.NewStarter(cfg, metadataStore, workflowStore, authSupport.AuthManager, runtimeSupport.Background, runtimeRegistry, workflowrunner.StarterOptions{RuntimeClientFactory: opts.RuntimeClientFactory, Worktrees: taskWorktreeEnsurer{service: worktreeService}, SessionRuntime: sessionRuntimeService, AttentionFinalizer: workflowAttentionFinalizer})
	if err != nil {
		cleanupNewFailure()
		return nil, fmt.Errorf("workflow bundle: runtime starter: %w", err)
	}
	workflowScheduler, err = workflowrunner.NewSchedulerService(workflowStore, workflowRuntimeStarter, workflowrunner.SchedulerConfig{Concurrency: cfg.Settings.Workflow.Concurrency}, workflowrunner.WithSchedulerPendingAskResolver(runtimePendingAskResolver{prompts: runtimeRegistry}), workflowrunner.WithSchedulerAttentionFinalizer(workflowAttentionFinalizer))
	if err != nil {
		cleanupNewFailure()
		return nil, fmt.Errorf("workflow bundle: scheduler: %w", err)
	}
	workflowRuntimeStarter.SetRuntimeFinished(workflowScheduler.RuntimeFinished)
	workflowService, err := workflowsvc.New(workflowStore, workflowViewService, workflowRoleResolver, workflowsvc.WithTaskWorktreeEnsurer(taskWorktreeEnsurer{service: worktreeService}), workflowsvc.WithTaskWorktreeDeleter(taskWorktreeDeleter{service: worktreeService}), workflowsvc.WithTaskRuntimeCanceler(workflowRuntimeStarter), workflowsvc.WithSchedulerNotifier(workflowScheduler), workflowsvc.WithPromptResponder(runtimeRegistry), workflowsvc.WithWorkflowAttentionFinalizer(workflowAttentionFinalizer))
	if err != nil {
		cleanupNewFailure()
		return nil, fmt.Errorf("workflow bundle: service: %w", err)
	}
	core := &Core{bundles: composeBundles(bundleCompositionInput{
		cfg:                     cfg,
		containerDir:            containerDir,
		authSupport:             authSupport,
		capabilityFactsService:  capabilityFactsService,
		runtimeSupport:          runtimeSupport,
		rootLease:               rootLease,
		metadataStore:           metadataStore,
		sessionStoreRegistry:    sessionStoreRegistry,
		runtimeRegistry:         runtimeRegistry,
		projectViews:            projectViews,
		authBootstrapService:    authBootstrapService,
		authStatusService:       authStatusService,
		askService:              askService,
		approvalService:         approvalService,
		processService:          processService,
		processOutputService:    processOutputService,
		promptControlService:    promptControlService,
		promptActivityService:   promptActivityService,
		attentionService:        client.NewLoopbackAttentionNotificationClient(runtimeRegistry),
		runtimeControlService:   runtimeControlService,
		serverStatusService:     serverStatusService,
		sessionRuntimeService:   sessionRuntimeService,
		sessionViewService:      sessionViewService,
		sessionLifecycleService: sessionLifecycleService,
		sessionActivityService:  sessionActivityService,
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
			core.bundles.Projects.containerDir = filepath.Join(filepath.Join(cfg.PersistenceRoot, "projects"), binding.ProjectID, "sessions")
			if err := os.MkdirAll(core.bundles.Projects.containerDir, 0o755); err != nil {
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
	updateStatusService.Start()
	return core, nil
}

type taskWorktreeEnsurer struct {
	service *worktree.Service
}

func (e taskWorktreeEnsurer) EnsureTaskWorktree(ctx context.Context, taskID string) error {
	if e.service == nil {
		return nil
	}
	_, err := e.service.EnsureTaskWorktree(ctx, worktree.EnsureTaskWorktreeRequest{TaskID: taskID})
	return err
}

type workflowApprovalProjection struct {
	store        *workflowstore.Store
	view         *workflowview.Service
	roleResolver workflow.RoleResolver
}

func (p workflowApprovalProjection) ApprovalProjection(ctx context.Context, transitionID workflow.TransitionID) (workflowattention.ApprovalProjection, bool, error) {
	if p.store == nil || p.view == nil {
		return workflowattention.ApprovalProjection{}, false, nil
	}
	taskID, _, _, err := p.store.TaskIdentityForTransition(ctx, transitionID)
	if err != nil {
		return workflowattention.ApprovalProjection{}, false, err
	}
	attention, err := p.view.ListTaskAttention(ctx, serverapi.WorkflowTaskAttentionListRequest{TaskID: taskID}, p.roleResolver)
	if err != nil {
		return workflowattention.ApprovalProjection{}, false, err
	}
	for _, item := range attention.Items {
		if item.Kind != "approval" || item.TaskTransitionID != string(transitionID) {
			continue
		}
		if strings.TrimSpace(item.RunID) == "" || strings.TrimSpace(item.SessionID) == "" {
			break
		}
		return workflowattention.ApprovalProjection{
			TransitionID:     transitionID,
			ProjectID:        item.ProjectID,
			WorkflowID:       item.WorkflowID,
			TaskID:           workflow.TaskID(item.TaskID),
			TaskShortID:      item.TaskShortID,
			TaskTitle:        item.TaskTitle,
			RunID:            item.RunID,
			SessionID:        item.SessionID,
			Message:          item.Message,
			OccurredAtUnixMs: item.OccurredAtUnixMs,
		}, true, nil
	}
	transition, err := p.store.ApprovalTransitionProjection(ctx, transitionID)
	if err != nil {
		return workflowattention.ApprovalProjection{}, false, err
	}
	return workflowattention.ApprovalProjection{
		TransitionID:     transition.TransitionID,
		ProjectID:        transition.ProjectID,
		WorkflowID:       transition.WorkflowID,
		TaskID:           transition.TaskID,
		TaskShortID:      transition.TaskShortID,
		TaskTitle:        transition.TaskTitle,
		RunID:            string(transition.SourceRunID),
		SessionID:        transition.SessionID,
		Message:          "action required",
		OccurredAtUnixMs: transition.OccurredAtUnixMs,
	}, true, nil
}

func (p workflowApprovalProjection) InterruptedRunProjection(ctx context.Context, runID workflow.RunID) (workflowattention.InterruptedRunProjection, bool, error) {
	if p.store == nil || p.view == nil || runID == "" {
		return workflowattention.InterruptedRunProjection{}, false, nil
	}
	run, err := p.store.GetRun(ctx, runID)
	if err != nil {
		return workflowattention.InterruptedRunProjection{}, false, err
	}
	if !workflowattention.ShouldNotifyInterruptedRun(run.InterruptionReason) {
		return workflowattention.InterruptedRunProjection{}, false, nil
	}
	if projection, ok, err := p.interruptedRunAttentionProjection(ctx, run); err != nil || ok {
		return projection, ok, err
	}
	input, err := p.store.GetRunStartContext(ctx, runID)
	if err != nil {
		return workflowattention.InterruptedRunProjection{}, false, err
	}
	return workflowattention.InterruptedRunProjection{
		ProjectID:        input.Task.ProjectID,
		WorkflowID:       string(input.Task.WorkflowID),
		TaskID:           input.Task.ID,
		TaskShortID:      input.Task.ShortID,
		TaskTitle:        input.Task.Title,
		RunID:            run.ID,
		SessionID:        run.SessionID,
		Reason:           run.InterruptionReason,
		OccurredAtUnixMs: run.InterruptedAt,
	}, true, nil
}

func (p workflowApprovalProjection) interruptedRunAttentionProjection(ctx context.Context, run workflowstore.RunRecord) (workflowattention.InterruptedRunProjection, bool, error) {
	attention, err := p.view.ListTaskAttention(ctx, serverapi.WorkflowTaskAttentionListRequest{TaskID: string(run.TaskID)}, p.roleResolver)
	if err != nil {
		return workflowattention.InterruptedRunProjection{}, false, err
	}
	for _, item := range attention.Items {
		if item.Kind != "interrupted_run" || item.RunID != string(run.ID) {
			continue
		}
		return workflowattention.InterruptedRunProjection{
			ProjectID:        item.ProjectID,
			WorkflowID:       item.WorkflowID,
			TaskID:           workflow.TaskID(item.TaskID),
			TaskShortID:      item.TaskShortID,
			TaskTitle:        item.TaskTitle,
			RunID:            run.ID,
			SessionID:        item.SessionID,
			Message:          item.Message,
			Reason:           run.InterruptionReason,
			OccurredAtUnixMs: item.OccurredAtUnixMs,
		}, true, nil
	}
	return workflowattention.InterruptedRunProjection{}, false, nil
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
	if !req.Approval || req.AttentionTarget == nil {
		return false
	}
	if req.AttentionTarget.Kind != clientui.AttentionNotificationTargetWorkflowTask || req.AttentionTarget.Focus == nil {
		return false
	}
	if req.AttentionTarget.Focus.Kind != clientui.AttentionNotificationFocusQuestion {
		return false
	}
	for _, focusedAskID := range req.AttentionTarget.Focus.AskIDs {
		if strings.TrimSpace(focusedAskID) == strings.TrimSpace(askID) {
			return true
		}
	}
	return false
}
