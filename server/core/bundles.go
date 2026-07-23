package core

import (
	"context"
	"sync"

	"core/server/authservice"
	serverbootstrap "core/server/bootstrap"
	"core/server/capabilityfacts"
	"core/server/metadata"

	"core/server/processview"
	"core/server/promptcontrol"
	"core/server/registry"
	"core/server/runtime"
	"core/server/runtimecontrol"
	"core/server/serverstatus"
	"core/server/sessionlaunch"
	"core/server/sessionruntime"
	"core/server/sessionservice"
	"core/server/sessionview"
	"core/server/sleepguard"
	shelltool "core/server/tools/shell"

	"core/server/workflowexecution"
	"core/server/workflowrunner"
	"core/server/workflowsvc"
	"core/server/worktree"
	"core/shared/apicontract"
	"core/shared/config"
)

type Bundles struct {
	Auth        *AuthBundle
	Capability  *CapabilityBundle
	cleanup     []lifecycleResource
	Persistence *PersistenceBundle
	Processes   *ProcessBundle
	Projects    *ProjectBundle
	Prompts     *PromptBundle
	Runtime     *RuntimeBundle
	Sessions    *SessionBundle
	Workflows   *WorkflowBundle
	Worktrees   *WorktreeBundle
}

type AuthBundle struct {
	support       serverbootstrap.AuthSupport
	authBootstrap apicontract.AuthBootstrapService
	authStatus    apicontract.AuthStatusService
	serverStatus  apicontract.ServerStatusService
	authRequired  bool
}

type CapabilityBundle struct {
	facts apicontract.CapabilityFactsService
}

type PersistenceBundle struct {
	rootLock      *RootLockLease
	metadataStore *metadata.Store
}

type ProcessBundle struct {
	processControls apicontract.ProcessControlService
	processOutput   apicontract.ProcessOutputService
	processViews    apicontract.ProcessViewService
}

type ProjectBundle struct {
	cfg          config.App
	projectID    string
	projectViews apicontract.ProjectViewService
}

type PromptBundle struct {
	askViews               apicontract.AskViewService
	approvalViews          apicontract.ApprovalViewService
	promptControl          apicontract.PromptControlService
	attentionNotifications apicontract.AttentionNotificationService
}

type RuntimeBundle struct {
	fastModeState       *runtime.FastModeState
	background          *shelltool.Manager
	runtimeRegistry     *registry.RuntimeRegistry
	runtimeAuthority    *sessionruntime.Authority
	runtimeControls     apicontract.RuntimeControlService
	runtimeLiveControls apicontract.RuntimeLiveControlService
	sessionRuntime      apicontract.SessionRuntimeService
	sessionTranscript   apicontract.SessionTranscriptService
}

type SessionBundle struct {
	mu               sync.Mutex
	runPromptMu      sync.Mutex
	sessionLaunchMap map[string]apicontract.SessionLaunchService
	sessionServices  map[string]*sessionlaunch.Service
	runPromptMap     map[string]apicontract.RunPromptService
	sessionLaunch    apicontract.SessionLaunchService
	sessionViews     apicontract.SessionViewService
	sessionLifecycle apicontract.SessionLifecycleService
	runPrompt        apicontract.RunPromptService
}

type WorktreeBundle struct {
	worktrees apicontract.WorktreeService
}

type WorkflowBundle struct {
	workflows apicontract.WorkflowService
	scheduler *workflowexecution.SchedulerService
	fatal     *workflowexecution.FatalSignal
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
	if withDefaults.Capability == nil {
		withDefaults.Capability = &CapabilityBundle{}
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
	if withDefaults.Prompts.attentionNotifications == nil {
		withDefaults.Prompts.attentionNotifications = unavailableAttentionNotificationClient{}
	}
	if withDefaults.Runtime == nil {
		withDefaults.Runtime = &RuntimeBundle{}
	}
	if withDefaults.Sessions == nil {
		withDefaults.Sessions = emptySessionBundle()
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
		sessionLaunchMap: make(map[string]apicontract.SessionLaunchService),
		sessionServices:  make(map[string]*sessionlaunch.Service),
		runPromptMap:     make(map[string]apicontract.RunPromptService),
	}
}

type bundleCompositionInput struct {
	cfg                     config.App
	authSupport             serverbootstrap.AuthSupport
	capabilityFactsService  *capabilityfacts.Service
	runtimeSupport          serverbootstrap.RuntimeSupport
	rootLease               *RootLockLease
	metadataStore           *metadata.Store
	runtimeRegistry         *registry.RuntimeRegistry
	runtimeAuthority        *sessionruntime.Authority
	projectViews            apicontract.ProjectViewService
	authBootstrapService    *authservice.BootstrapService
	authStatusService       *authservice.StatusService
	askService              *promptcontrol.AskViewService
	approvalService         *promptcontrol.ApprovalViewService
	processService          *processview.ProcessViewService
	processOutputService    *processview.ProcessOutputService
	promptControlService    *promptcontrol.PromptControlService
	attentionService        apicontract.AttentionNotificationService
	runtimeControlService   *runtimecontrol.Service
	serverStatusService     *serverstatus.ServerStatusService
	sessionRuntimeAPI       *sessionruntime.API
	sessionViewService      *sessionview.Service
	sessionLifecycleService *sessionservice.SessionLifecycleService
	updateStatusService     *serverstatus.UpdateStatusService
	workflowService         *workflowsvc.Service
	workflowScheduler       *workflowexecution.SchedulerService
	workflowFatal           *workflowexecution.FatalSignal
	workflowRuntimeStarter  *workflowrunner.Starter
	worktreeService         *worktree.Service
	sleepManager            *sleepguard.Manager
}

func composeBundles(in bundleCompositionInput) *Bundles {
	return &Bundles{
		Auth:       newAuthBundle(in.authSupport, in.authBootstrapService, in.authStatusService, in.serverStatusService, authservice.StartupAuthRequired(in.cfg.Settings)),
		Capability: newCapabilityBundle(in.capabilityFactsService),
		cleanup: []lifecycleResource{
			{name: "persistence root lock", close: in.rootLease.Close},
			{name: "metadata store", close: in.metadataStore.Close},
			{name: "background manager", close: in.runtimeSupport.Background.Close},
			{name: "update status service", close: in.updateStatusService.Close},
			{name: "worktree transitions", close: func() error {
				if in.worktreeService == nil {
					return nil
				}
				return in.worktreeService.Close()
			}},
			{name: "workflow runtime starter", close: func() error {
				if in.workflowRuntimeStarter == nil {
					return nil
				}
				return in.workflowRuntimeStarter.Close()
			}},
			{name: "session runtime authority", close: func() error {
				if in.runtimeAuthority == nil {
					return nil
				}
				return in.runtimeAuthority.Close(context.Background())
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
		Persistence: newPersistenceBundle(in.rootLease, in.metadataStore),
		Processes:   newProcessBundle(in.processService, in.processOutputService),
		Projects:    newProjectBundle(in.cfg, in.projectViews),
		Prompts:     newPromptBundle(in.askService, in.approvalService, in.promptControlService, in.attentionService),
		Runtime:     newRuntimeBundle(in.runtimeSupport, in.runtimeRegistry, in.runtimeAuthority, in.runtimeControlService, in.sessionRuntimeAPI),
		Sessions:    newSessionBundle(in.sessionViewService, in.sessionLifecycleService),
		Workflows:   newWorkflowBundle(in.workflowService, in.workflowScheduler, in.workflowFatal),
		Worktrees:   &WorktreeBundle{worktrees: in.worktreeService},
	}
}

func newAuthBundle(authSupport serverbootstrap.AuthSupport, bootstrapService *authservice.BootstrapService, statusService *authservice.StatusService, serverStatusService *serverstatus.ServerStatusService, authRequired bool) *AuthBundle {
	return &AuthBundle{
		support:       authSupport,
		authBootstrap: bootstrapService,
		authStatus:    statusService,
		serverStatus:  serverStatusService,
		authRequired:  authRequired,
	}
}

func newCapabilityBundle(factsService *capabilityfacts.Service) *CapabilityBundle {
	return &CapabilityBundle{facts: factsService}
}

func newPersistenceBundle(rootLease *RootLockLease, metadataStore *metadata.Store) *PersistenceBundle {
	return &PersistenceBundle{
		rootLock:      rootLease,
		metadataStore: metadataStore,
	}
}

func newProcessBundle(processService *processview.ProcessViewService, processOutputService *processview.ProcessOutputService) *ProcessBundle {
	return &ProcessBundle{
		processControls: processService,
		processOutput:   processOutputService,
		processViews:    processService,
	}
}

func newProjectBundle(cfg config.App, projectViews apicontract.ProjectViewService) *ProjectBundle {
	return &ProjectBundle{
		cfg:          cfg,
		projectViews: projectViews,
	}
}

func newPromptBundle(askService *promptcontrol.AskViewService, approvalService *promptcontrol.ApprovalViewService, promptControlService *promptcontrol.PromptControlService, attentionService apicontract.AttentionNotificationService) *PromptBundle {
	if attentionService == nil {
		attentionService = unavailableAttentionNotificationClient{}
	}
	return &PromptBundle{
		askViews:               askService,
		approvalViews:          approvalService,
		promptControl:          promptControlService,
		attentionNotifications: attentionService,
	}
}

func newRuntimeBundle(runtimeSupport serverbootstrap.RuntimeSupport, runtimeRegistry *registry.RuntimeRegistry, runtimeAuthority *sessionruntime.Authority, runtimeControlService *runtimecontrol.Service, sessionRuntimeAPI *sessionruntime.API) *RuntimeBundle {
	return &RuntimeBundle{
		fastModeState:       runtimeSupport.FastModeState,
		background:          runtimeSupport.Background,
		runtimeRegistry:     runtimeRegistry,
		runtimeAuthority:    runtimeAuthority,
		runtimeControls:     runtimeControlService,
		runtimeLiveControls: runtimeControlService,
		sessionRuntime:      sessionRuntimeAPI,
		sessionTranscript:   runtimeRegistry,
	}
}

func newWorkflowBundle(workflowService *workflowsvc.Service, scheduler *workflowexecution.SchedulerService, fatal *workflowexecution.FatalSignal) *WorkflowBundle {
	return &WorkflowBundle{workflows: workflowService, scheduler: scheduler, fatal: fatal}
}

func newSessionBundle(sessionViewService *sessionview.Service, sessionLifecycleService *sessionservice.SessionLifecycleService) *SessionBundle {
	return &SessionBundle{
		sessionLaunchMap: make(map[string]apicontract.SessionLaunchService),
		sessionServices:  make(map[string]*sessionlaunch.Service),
		runPromptMap:     make(map[string]apicontract.RunPromptService),
		sessionLaunch:    unregisteredSessionLaunchClient{},
		sessionViews:     sessionViewService,
		sessionLifecycle: sessionLifecycleService,
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
