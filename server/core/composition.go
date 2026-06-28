package core

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"core/prompts"
	"core/server/authservice"
	serverbootstrap "core/server/bootstrap"
	"core/server/metadata"

	"core/server/processview"
	"core/server/projectview"
	"core/server/promptcontrol"
	"core/server/registry"
	"core/server/runtime"
	"core/server/runtimecontrol"
	"core/server/serverstatus"
	"core/server/sessionruntime"
	"core/server/sessionservice"
	"core/server/sessionview"
	"core/server/sleepguard"

	"core/server/workflow"
	"core/server/workflowrunner"
	"core/server/workflowstore"
	"core/server/workflowsvc"
	"core/server/workflowview"
	"core/server/worktree"
	rpccontract "core/shared/apicontract"
	"core/shared/client"
	"core/shared/config"
	"core/shared/serverapi"
)

func New(cfg config.App, authSupport serverbootstrap.AuthSupport, runtimeSupport serverbootstrap.RuntimeSupport) (*Core, error) {
	return NewWithContext(context.Background(), cfg, authSupport, runtimeSupport)
}

func NewWithContext(ctx context.Context, cfg config.App, authSupport serverbootstrap.AuthSupport, runtimeSupport serverbootstrap.RuntimeSupport) (*Core, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	rootLease, err := AcquireRootLock(cfg.PersistenceRoot)
	if err != nil {
		return nil, fmt.Errorf("persistence bundle: root lock: %w", err)
	}
	generatedSupport, err := serverbootstrap.BuildGeneratedSupport(ctx, cfg.PersistenceRoot)
	if err != nil {
		_ = rootLease.Close()
		return nil, fmt.Errorf("persistence bundle: generated support: %w", err)
	}
	runtimeSupport.Generated = generatedSupport
	containerDir := ""
	metadataStore, err := metadata.Open(cfg.PersistenceRoot)
	if err != nil {
		_ = rootLease.Close()
		return nil, fmt.Errorf("persistence bundle: metadata store: %w", err)
	}
	if err := validateAuthBundleSupport(authSupport); err != nil {
		_ = rootLease.Close()
		_ = metadataStore.Close()
		return nil, err
	}
	if err := validateRuntimeBundleSupport(runtimeSupport); err != nil {
		_ = rootLease.Close()
		_ = metadataStore.Close()
		return nil, err
	}
	storeOptions := metadataStore.AuthoritativeSessionStoreOptions()
	runtimeRegistry := registry.NewRuntimeRegistry()
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
		_ = rootLease.Close()
		_ = metadataStore.Close()
		return nil, fmt.Errorf("projects bundle: metadata service: %w", err)
	}
	askService := promptcontrol.NewAskViewService(runtimeRegistry)
	approvalService := promptcontrol.NewApprovalViewService(runtimeRegistry)
	processService := processview.NewProcessViewService(runtimeSupport.Background)
	processOutputService := processview.NewProcessOutputService(runtimeSupport.Background, runtimeSupport.Background)
	sessionRuntimeService := sessionruntime.NewService(cfg.PersistenceRoot, metadataStore, authSupport.AuthManager, runtimeSupport.FastModeState, runtimeSupport.Background, runtimeSupport.BackgroundRouter, runtimeRegistry, sessionStoreRegistry, storeOptions...).
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
	sessionStoreResolver := registry.NewGlobalPersistenceSessionResolver(cfg.PersistenceRoot, storeOptions...)
	promptControlService := promptcontrol.NewPromptControlService(runtimeRegistry)
	promptActivityService := promptcontrol.NewPromptActivityService(runtimeRegistry)
	runtimeControlService := runtimecontrol.NewService(runtimeRegistry).WithPromptHistoryStore(metadataStore).WithWorkflowSessionResolver(sessionStoreResolver)
	worktreeService := worktree.NewService(metadataStore, nil, runtimeRegistry, sessionRuntimeService, runtimeSupport.Background, runtimeControlService, worktree.ServiceOptions{BaseDir: cfg.Settings.Worktrees.BaseDir, SetupScript: cfg.Settings.Worktrees.SetupScript})
	projectViews := client.NewLoopbackProjectViewClient(projectService)
	authBootstrapService := authservice.NewBootstrapService(authSupport.AuthManager, authSupport.OAuthOptions, cfg.Settings, rpccontract.AllowedPreAuthMethods())
	authStatusService := authservice.NewStatusService(authSupport.AuthManager, cfg.Settings)
	serverStatusService := serverstatus.NewServerStatusService(authSupport.AuthManager, cfg)
	updateStatusService := serverstatus.NewUpdateStatusService(config.Version)
	sessionViewService := sessionview.NewService(sessionStoreResolver, runtimeRegistry, metadataStore).WithCacheWarningMode(cfg.Settings.CacheWarningMode).WithUpdateStatusProvider(updateStatusService)
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
		_ = rootLease.Close()
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
	workflowViewService, err := workflowview.New(metadataStore, workflowview.WithSessionTranscriptProvider(sessionViewService))
	if err != nil {
		cleanupNewFailure()
		return nil, fmt.Errorf("workflow bundle: view: %w", err)
	}
	workflowRuntimeStarter, err = workflowrunner.NewStarter(cfg, metadataStore, workflowStore, authSupport.AuthManager, runtimeSupport.Background, runtimeRegistry, workflowrunner.StarterOptions{Worktrees: taskWorktreeEnsurer{service: worktreeService}, SessionRuntime: sessionRuntimeService})
	if err != nil {
		cleanupNewFailure()
		return nil, fmt.Errorf("workflow bundle: runtime starter: %w", err)
	}
	workflowScheduler, err = workflowrunner.NewSchedulerService(workflowStore, workflowRuntimeStarter, workflowrunner.SchedulerConfig{Concurrency: cfg.Settings.Workflow.Concurrency}, workflowrunner.WithSchedulerPendingAskResolver(runtimePendingAskResolver{prompts: runtimeRegistry}))
	if err != nil {
		cleanupNewFailure()
		return nil, fmt.Errorf("workflow bundle: scheduler: %w", err)
	}
	workflowRuntimeStarter.SetRuntimeFinished(workflowScheduler.RuntimeFinished)
	workflowService, err := workflowsvc.New(workflowStore, workflowViewService, workflowRoleResolver, workflowsvc.WithTaskWorktreeEnsurer(taskWorktreeEnsurer{service: worktreeService}), workflowsvc.WithTaskWorktreeDeleter(taskWorktreeDeleter{service: worktreeService}), workflowsvc.WithTaskRuntimeCanceler(workflowRuntimeStarter), workflowsvc.WithSchedulerNotifier(workflowScheduler), workflowsvc.WithPromptResponder(runtimeRegistry))
	if err != nil {
		cleanupNewFailure()
		return nil, fmt.Errorf("workflow bundle: service: %w", err)
	}
	core := &Core{bundles: composeBundles(bundleCompositionInput{
		cfg:                     cfg,
		containerDir:            containerDir,
		authSupport:             authSupport,
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

func (r runtimePendingAskResolver) CanRehydrate(_ context.Context, sessionID string, _ workflow.RunID, askID string) (bool, error) {
	if r.prompts == nil || strings.TrimSpace(sessionID) == "" || strings.TrimSpace(askID) == "" {
		return false, nil
	}
	for _, item := range r.prompts.ListPendingPrompts(sessionID) {
		if item.Request.ID == askID && !item.Request.Approval {
			return true, nil
		}
	}
	return false, nil
}
