package core

import (
	"sync"

	"core/server/authservice"
	serverbootstrap "core/server/bootstrap"
	"core/server/metadata"

	"core/server/processview"
	"core/server/promptcontrol"
	"core/server/registry"
	"core/server/runtime"
	"core/server/runtimecontrol"
	"core/server/runtimewire"
	"core/server/serverstatus"
	"core/server/sessionlaunch"
	"core/server/sessionruntime"
	"core/server/sessionservice"
	"core/server/sessionview"
	"core/server/sleepguard"
	shelltool "core/server/tools/shell"

	"core/server/workflowrunner"
	"core/server/workflowsvc"
	"core/server/worktree"
	"core/shared/client"
	"core/shared/config"
)

type Bundles struct {
	Auth        *AuthBundle
	cleanup     []lifecycleResource
	Persistence *PersistenceBundle
	Processes   *ProcessBundle
	Projects    *ProjectBundle
	Prompts     *PromptBundle
	Runtime     *RuntimeBundle
	Sessions    *SessionBundle
	Updates     *UpdateBundle
	Workflows   *WorkflowBundle
	Worktrees   *WorktreeBundle
}

type AuthBundle struct {
	support       serverbootstrap.AuthSupport
	authBootstrap client.AuthBootstrapClient
	authStatus    client.AuthStatusClient
	serverStatus  client.ServerStatusClient
	authRequired  bool
}

type PersistenceBundle struct {
	rootLock      *RootLockLease
	metadataStore *metadata.Store
	sessionStores *registry.SessionStoreRegistry
}

type ProcessBundle struct {
	processControls client.ProcessControlClient
	processOutput   client.ProcessOutputClient
	processViews    client.ProcessViewClient
}

type ProjectBundle struct {
	cfg          config.App
	containerDir string
	projectID    string
	projectViews client.ProjectViewClient
}

type PromptBundle struct {
	askViews       client.AskViewClient
	approvalViews  client.ApprovalViewClient
	promptControl  client.PromptControlClient
	promptActivity client.PromptActivityClient
}

type RuntimeBundle struct {
	fastModeState    *runtime.FastModeState
	background       *shelltool.Manager
	backgroundRouter *runtimewire.BackgroundEventRouter
	runtimeRegistry  *registry.RuntimeRegistry
	runtimeControls  client.RuntimeControlClient
	sessionRuntime   client.SessionRuntimeClient
	sessionActivity  client.SessionActivityClient

	sessionRuntimeService *sessionruntime.Service
}

type SessionBundle struct {
	mu               sync.Mutex
	runPromptMu      sync.Mutex
	sessionLaunchMap map[string]client.SessionLaunchClient
	sessionServices  map[string]*sessionlaunch.Service
	runPromptMap     map[string]client.RunPromptClient
	sessionLaunch    client.SessionLaunchClient
	sessionViews     client.SessionViewClient
	sessionLifecycle client.SessionLifecycleClient
	runPrompt        client.RunPromptClient
}

type UpdateBundle struct {
	updateStatus *serverstatus.UpdateStatusService
}

type WorktreeBundle struct {
	worktrees client.WorktreeClient
}

type WorkflowBundle struct {
	workflows client.WorkflowClient
	scheduler *workflowrunner.SchedulerService
}

func (s *Core) safeBundles() *Bundles {
	if s == nil {
		return (&Bundles{}).withDefaults()
	}
	return s.bundles.withDefaults()
}

func (b *Bundles) withDefaults() *Bundles {
	if b == nil {
		return (&Bundles{}).withDefaults()
	}
	withDefaults := *b
	if withDefaults.Auth == nil {
		withDefaults.Auth = &AuthBundle{}
	}
	if withDefaults.Persistence == nil {
		withDefaults.Persistence = &PersistenceBundle{}
	}
	if withDefaults.Processes == nil {
		withDefaults.Processes = &ProcessBundle{}
	}
	if withDefaults.Projects == nil {
		withDefaults.Projects = &ProjectBundle{}
	}
	if withDefaults.Prompts == nil {
		withDefaults.Prompts = &PromptBundle{}
	}
	if withDefaults.Runtime == nil {
		withDefaults.Runtime = &RuntimeBundle{}
	}
	if withDefaults.Sessions == nil {
		withDefaults.Sessions = emptySessionBundle()
	}
	if withDefaults.Updates == nil {
		withDefaults.Updates = &UpdateBundle{}
	}
	if withDefaults.Workflows == nil {
		withDefaults.Workflows = &WorkflowBundle{}
	}
	if withDefaults.Worktrees == nil {
		withDefaults.Worktrees = &WorktreeBundle{}
	}
	return &withDefaults
}

func emptySessionBundle() *SessionBundle {
	return &SessionBundle{
		sessionLaunchMap: make(map[string]client.SessionLaunchClient),
		sessionServices:  make(map[string]*sessionlaunch.Service),
		runPromptMap:     make(map[string]client.RunPromptClient),
	}
}

type bundleCompositionInput struct {
	cfg                     config.App
	containerDir            string
	authSupport             serverbootstrap.AuthSupport
	runtimeSupport          serverbootstrap.RuntimeSupport
	rootLease               *RootLockLease
	metadataStore           *metadata.Store
	sessionStoreRegistry    *registry.SessionStoreRegistry
	runtimeRegistry         *registry.RuntimeRegistry
	projectViews            client.ProjectViewClient
	authBootstrapService    *authservice.BootstrapService
	authStatusService       *authservice.StatusService
	askService              *promptcontrol.AskViewService
	approvalService         *promptcontrol.ApprovalViewService
	processService          *processview.ProcessViewService
	processOutputService    *processview.ProcessOutputService
	promptControlService    *promptcontrol.PromptControlService
	promptActivityService   *promptcontrol.PromptActivityService
	runtimeControlService   *runtimecontrol.Service
	serverStatusService     *serverstatus.ServerStatusService
	sessionRuntimeService   *sessionruntime.Service
	sessionViewService      *sessionview.Service
	sessionLifecycleService *sessionservice.SessionLifecycleService
	sessionActivityService  *sessionservice.SessionActivityService
	updateStatusService     *serverstatus.UpdateStatusService
	workflowService         *workflowsvc.Service
	workflowScheduler       *workflowrunner.SchedulerService
	workflowRuntimeStarter  *workflowrunner.Starter
	worktreeService         *worktree.Service
	sleepManager            *sleepguard.Manager
}

func composeBundles(in bundleCompositionInput) *Bundles {
	return &Bundles{
		Auth: newAuthBundle(in.authSupport, in.authBootstrapService, in.authStatusService, in.serverStatusService, authservice.StartupAuthRequired(in.cfg.Settings)),
		cleanup: []lifecycleResource{
			{name: "persistence root lock", close: in.rootLease.Close},
			{name: "metadata store", close: in.metadataStore.Close},
			{name: "background manager", close: in.runtimeSupport.Background.Close},
			{name: "workflow runtime starter", close: func() error {
				if in.workflowRuntimeStarter == nil {
					return nil
				}
				return in.workflowRuntimeStarter.Close()
			}},
			{name: "workflow scheduler", close: func() error {
				if in.workflowScheduler == nil {
					return nil
				}
				return in.workflowScheduler.Close()
			}},
			{name: "sleep manager", close: func() error {
				if in.sleepManager != nil {
					in.sleepManager.Close()
				}
				return nil
			}},
		},
		Persistence: newPersistenceBundle(in.rootLease, in.metadataStore, in.sessionStoreRegistry),
		Processes:   newProcessBundle(in.processService, in.processOutputService),
		Projects:    newProjectBundle(in.cfg, in.containerDir, in.projectViews),
		Prompts:     newPromptBundle(in.askService, in.approvalService, in.promptControlService, in.promptActivityService),
		Runtime:     newRuntimeBundle(in.runtimeSupport, in.runtimeRegistry, in.runtimeControlService, in.sessionRuntimeService, in.sessionActivityService),
		Sessions:    newSessionBundle(in.sessionViewService, in.sessionLifecycleService),
		Updates:     &UpdateBundle{updateStatus: in.updateStatusService},
		Workflows:   newWorkflowBundle(in.workflowService, in.workflowScheduler),
		Worktrees:   &WorktreeBundle{worktrees: client.NewLoopbackWorktreeClient(in.worktreeService)},
	}
}

func newAuthBundle(authSupport serverbootstrap.AuthSupport, bootstrapService *authservice.BootstrapService, statusService *authservice.StatusService, serverStatusService *serverstatus.ServerStatusService, authRequired bool) *AuthBundle {
	return &AuthBundle{
		support:       authSupport,
		authBootstrap: client.NewLoopbackAuthBootstrapClient(bootstrapService),
		authStatus:    client.NewLoopbackAuthStatusClient(statusService),
		serverStatus:  client.NewLoopbackServerStatusClient(serverStatusService),
		authRequired:  authRequired,
	}
}

func newPersistenceBundle(rootLease *RootLockLease, metadataStore *metadata.Store, sessionStoreRegistry *registry.SessionStoreRegistry) *PersistenceBundle {
	return &PersistenceBundle{
		rootLock:      rootLease,
		metadataStore: metadataStore,
		sessionStores: sessionStoreRegistry,
	}
}

func newProcessBundle(processService *processview.ProcessViewService, processOutputService *processview.ProcessOutputService) *ProcessBundle {
	return &ProcessBundle{
		processControls: client.NewLoopbackProcessControlClient(processService),
		processOutput:   client.NewLoopbackProcessOutputClient(processOutputService),
		processViews:    client.NewLoopbackProcessViewClient(processService),
	}
}

func newProjectBundle(cfg config.App, containerDir string, projectViews client.ProjectViewClient) *ProjectBundle {
	return &ProjectBundle{
		cfg:          cfg,
		containerDir: containerDir,
		projectViews: projectViews,
	}
}

func newPromptBundle(askService *promptcontrol.AskViewService, approvalService *promptcontrol.ApprovalViewService, promptControlService *promptcontrol.PromptControlService, promptActivityService *promptcontrol.PromptActivityService) *PromptBundle {
	return &PromptBundle{
		askViews:       client.NewLoopbackAskViewClient(askService),
		approvalViews:  client.NewLoopbackApprovalViewClient(approvalService),
		promptControl:  client.NewLoopbackPromptControlClient(promptControlService),
		promptActivity: client.NewLoopbackPromptActivityClient(promptActivityService),
	}
}

func newRuntimeBundle(runtimeSupport serverbootstrap.RuntimeSupport, runtimeRegistry *registry.RuntimeRegistry, runtimeControlService *runtimecontrol.Service, sessionRuntimeService *sessionruntime.Service, sessionActivityService *sessionservice.SessionActivityService) *RuntimeBundle {
	return &RuntimeBundle{
		fastModeState:    runtimeSupport.FastModeState,
		background:       runtimeSupport.Background,
		backgroundRouter: runtimeSupport.BackgroundRouter,
		runtimeRegistry:  runtimeRegistry,
		runtimeControls:  client.NewLoopbackRuntimeControlClient(runtimeControlService),
		sessionRuntime:   client.NewLoopbackSessionRuntimeClient(sessionRuntimeService),
		sessionActivity:  client.NewLoopbackSessionActivityClient(sessionActivityService),

		sessionRuntimeService: sessionRuntimeService,
	}
}

func newWorkflowBundle(workflowService *workflowsvc.Service, scheduler *workflowrunner.SchedulerService) *WorkflowBundle {
	return &WorkflowBundle{workflows: client.NewLoopbackWorkflowClient(workflowService), scheduler: scheduler}
}

func newSessionBundle(sessionViewService *sessionview.Service, sessionLifecycleService *sessionservice.SessionLifecycleService) *SessionBundle {
	return &SessionBundle{
		sessionLaunchMap: make(map[string]client.SessionLaunchClient),
		sessionServices:  make(map[string]*sessionlaunch.Service),
		runPromptMap:     make(map[string]client.RunPromptClient),
		sessionLaunch:    unregisteredSessionLaunchClient{},
		sessionViews:     client.NewLoopbackSessionViewClient(sessionViewService),
		sessionLifecycle: client.NewLoopbackSessionLifecycleClient(sessionLifecycleService),
		runPrompt:        unregisteredRunPromptClient{},
	}
}

func validateAuthBundleSupport(authSupport serverbootstrap.AuthSupport) error {
	if authSupport.AuthManager == nil {
		return bundleResourceRequiredError("auth", "auth manager")
	}
	return nil
}

func validateRuntimeBundleSupport(runtimeSupport serverbootstrap.RuntimeSupport) error {
	if runtimeSupport.Background == nil {
		return bundleResourceRequiredError("runtime", "background manager")
	}
	return nil
}
