package app

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"sync"

	"core/cli/app/commands"
	"core/cli/app/internal/status"
	"core/server/auth"
	"core/server/authservice"
	"core/server/launch"
	"core/server/metadata"
	"core/server/projectview"
	"core/server/registry"
	"core/server/runtime"
	"core/server/session"
	"core/server/session/sessiontest"
	"core/server/sessionlaunch"
	"core/server/sessionruntime"
	"core/server/sessionservice"
	shelltool "core/server/tools/shell"
	"core/shared/apicontract"
	"core/shared/clientui"
	"core/shared/config"
	"core/shared/serverapi"
)

type testEmbeddedServer struct {
	cfg                  config.App
	containerDir         string
	oauthOpts            auth.OpenAIOAuthOptions
	authManager          *auth.Manager
	fastModeState        *runtime.FastModeState
	background           *shelltool.Manager
	runPromptClient      apicontract.RunPromptService
	projectID            string
	boundWorkspaceID     string
	askViewClient        apicontract.AskViewService
	approvalViewClient   apicontract.ApprovalViewService
	attentionClient      apicontract.AttentionNotificationService
	promptControlClient  apicontract.PromptControlService
	projectViewClient    apicontract.ProjectViewService
	processControlClient apicontract.ProcessControlService
	processOutputClient  apicontract.ProcessOutputService
	processViewClient    apicontract.ProcessViewService
	runtimeControlClient apicontract.RuntimeControlService
	sessionLaunch        apicontract.SessionLaunchService
	sessionLifecycle     apicontract.SessionLifecycleService
	sessionRuntime       apicontract.SessionRuntimeService
	sessionTranscript    apicontract.SessionTranscriptService
	sessionViewClient    apicontract.SessionViewService
	runtimeAuthority     *sessionruntime.Authority
	sessionPersistence   *sessiontest.Persistence
	metadataOnce         sync.Once
	metadataStore        *metadata.Store
	metadataBindingData  metadata.Binding
	metadataBindingOK    bool
	prepareRuntime       func(ctx context.Context, plan sessionLaunchPlan, diagnosticWriter io.Writer, startLogLine string) (*runtimeLaunchPlan, error)
	reauthenticate       func(ctx context.Context, interactor authInteractor) error
}

type recordingSessionRuntimeClient struct {
	activate func(context.Context, serverapi.SessionRuntimeActivateRequest) (serverapi.SessionRuntimeActivateResponse, error)
	release  func(context.Context, serverapi.SessionRuntimeReleaseRequest) (serverapi.SessionRuntimeReleaseResponse, error)
}

func sessionRuntimeActivateResponse(sessionID string, generation uint64) serverapi.SessionRuntimeActivateResponse {
	return serverapi.SessionRuntimeActivateResponse{
		Attachment: serverapi.SessionRuntimeAttachment{
			SessionID:  sessionID,
			Generation: generation,
		},
	}
}

func (c *recordingSessionRuntimeClient) ActivateSessionRuntime(ctx context.Context, req serverapi.SessionRuntimeActivateRequest) (serverapi.SessionRuntimeActivateResponse, error) {
	if c.activate != nil {
		return c.activate(ctx, req)
	}
	return sessionRuntimeActivateResponse(req.SessionID, 1), nil
}

func (c *recordingSessionRuntimeClient) ReleaseSessionRuntime(ctx context.Context, req serverapi.SessionRuntimeReleaseRequest) (serverapi.SessionRuntimeReleaseResponse, error) {
	if c.release != nil {
		return c.release(ctx, req)
	}
	return serverapi.SessionRuntimeReleaseResponse{}, nil
}

func (s *testEmbeddedServer) Close() error {
	if s == nil || s.metadataStore == nil {
		return nil
	}
	err := s.metadataStore.Close()
	s.metadataStore = nil
	return err
}

func (s *testEmbeddedServer) OwnsServer() bool { return true }

func (s *testEmbeddedServer) Config() config.App { return s.cfg }

func (s *testEmbeddedServer) workspaceRetargetContext() *sessionWorkspaceRetargetContext {
	return &sessionWorkspaceRetargetContext{
		workspaceRoot: s.cfg.WorkspaceRoot,
		theme:         s.cfg.Settings.Theme,
	}
}

func (s *testEmbeddedServer) PresentationTheme() string { return "dark" }

func (s *testEmbeddedServer) ClientPromptRoots() (commands.ClientPromptRoots, error) {
	return commands.NewClientPromptRoots()
}

func (s *testEmbeddedServer) BindProjectWorkspace(_ context.Context, projectID string, workspaceID string) (interactiveSessionServer, error) {
	if s == nil {
		return nil, errors.New("test embedded server is required")
	}
	clone := &testEmbeddedServer{
		cfg:                  s.cfg,
		containerDir:         s.containerDir,
		oauthOpts:            s.oauthOpts,
		authManager:          s.authManager,
		fastModeState:        s.fastModeState,
		background:           s.background,
		runPromptClient:      s.runPromptClient,
		projectID:            strings.TrimSpace(projectID),
		boundWorkspaceID:     s.boundWorkspaceID,
		askViewClient:        s.askViewClient,
		approvalViewClient:   s.approvalViewClient,
		attentionClient:      s.attentionClient,
		promptControlClient:  s.promptControlClient,
		projectViewClient:    s.projectViewClient,
		processControlClient: s.processControlClient,
		processOutputClient:  s.processOutputClient,
		processViewClient:    s.processViewClient,
		runtimeControlClient: s.runtimeControlClient,
		sessionLaunch:        s.sessionLaunch,
		sessionLifecycle:     s.sessionLifecycle,
		sessionRuntime:       s.sessionRuntime,
		sessionTranscript:    s.sessionTranscript,
		sessionViewClient:    s.sessionViewClient,
		runtimeAuthority:     s.runtimeAuthority,
		sessionPersistence:   s.sessionPersistence,
		metadataStore:        s.metadataStore,
		metadataBindingData:  s.metadataBindingData,
		metadataBindingOK:    s.metadataBindingOK,
		prepareRuntime:       s.prepareRuntime,
		reauthenticate:       s.reauthenticate,
	}
	clone.boundWorkspaceID = strings.TrimSpace(workspaceID)
	return clone, nil
}

func (s *testEmbeddedServer) ProjectID() string {
	if strings.TrimSpace(s.projectID) != "" {
		return s.projectID
	}
	binding, err := metadata.ResolveBinding(context.Background(), s.cfg.PersistenceRoot, s.cfg.WorkspaceRoot)
	if err != nil {
		return ""
	}
	return binding.ProjectID
}

func (s *testEmbeddedServer) metadataBinding() (*metadata.Store, metadata.Binding, bool) {
	if strings.TrimSpace(s.cfg.PersistenceRoot) == "" || strings.TrimSpace(s.cfg.WorkspaceRoot) == "" {
		return nil, metadata.Binding{}, false
	}
	s.metadataOnce.Do(func() {
		store, err := metadata.Open(s.cfg.PersistenceRoot)
		if err != nil {
			return
		}
		binding, err := store.EnsureWorkspaceBinding(context.Background(), s.cfg.WorkspaceRoot)
		if err != nil {
			_ = store.Close()
			return
		}
		s.metadataStore = store
		s.metadataBindingData = binding
		s.metadataBindingOK = true
	})
	if !s.metadataBindingOK || s.metadataStore == nil {
		return nil, metadata.Binding{}, false
	}
	return s.metadataStore, s.metadataBindingData, true
}

func (s *testEmbeddedServer) ProjectViewClient() apicontract.ProjectViewService {
	if s.projectViewClient != nil {
		return s.projectViewClient
	}
	if metadataStore, binding, ok := s.metadataBinding(); ok {
		service, err := projectview.NewMetadataService(metadataStore, binding.ProjectID)
		if err == nil {
			return service
		}
	}
	if strings.TrimSpace(s.cfg.PersistenceRoot) == "" {
		return nil
	}
	store, err := metadata.Open(s.cfg.PersistenceRoot)
	if err != nil {
		return nil
	}
	s.metadataStore = store
	service, err := projectview.NewMetadataService(store, "")
	if err != nil {
		_ = store.Close()
		return nil
	}
	return service
}

func (s *testEmbeddedServer) ServerStatusClient() apicontract.ServerStatusService {
	return nil
}

func (s *testEmbeddedServer) AskViewClient() apicontract.AskViewService { return s.askViewClient }

func (s *testEmbeddedServer) ApprovalViewClient() apicontract.ApprovalViewService {
	return s.approvalViewClient
}

func (s *testEmbeddedServer) PromptControlClient() apicontract.PromptControlService {
	return s.promptControlClient
}

func (s *testEmbeddedServer) OAuthOptions() auth.OpenAIOAuthOptions { return s.oauthOpts }

func (s *testEmbeddedServer) AuthManager() *auth.Manager { return s.authManager }

func (s *testEmbeddedServer) AuthStateResolver() status.AuthStateResolver {
	return status.NormalizeAuthStateResolver(s.authManager)
}

func (s *testEmbeddedServer) AuthStatePath() string {
	if s.authManager == nil {
		return ""
	}
	return config.GlobalAuthConfigPath(s.cfg)
}

func (s *testEmbeddedServer) AuthStatusClient() apicontract.AuthStatusService {
	return nil
}

func (s *testEmbeddedServer) FastModeState() *runtime.FastModeState { return s.fastModeState }

func (s *testEmbeddedServer) Background() *shelltool.Manager { return s.background }

func (s *testEmbeddedServer) RunPromptClient() apicontract.RunPromptService { return s.runPromptClient }

func (s *testEmbeddedServer) ProcessControlClient() apicontract.ProcessControlService {
	return s.processControlClient
}

func (s *testEmbeddedServer) ProcessOutputClient() apicontract.ProcessOutputService {
	return s.processOutputClient
}

func (s *testEmbeddedServer) ProcessViewClient() apicontract.ProcessViewService {
	return s.processViewClient
}

func (s *testEmbeddedServer) RuntimeControlClient() apicontract.RuntimeControlService {
	if s.runtimeControlClient != nil {
		return s.runtimeControlClient
	}
	return newUnavailableRuntimeControlService()
}

func (s *testEmbeddedServer) sessionAuthority(storeOptions ...session.StoreOption) *sessionruntime.Authority {
	if s.runtimeAuthority == nil {
		s.runtimeAuthority = sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{
			PersistenceRoot: s.cfg.PersistenceRoot,
			StoreOptions:    storeOptions,
		})
	}
	return s.runtimeAuthority
}

type testSessionProcessSource struct {
	manager *shelltool.Manager
}

func (s testSessionProcessSource) List() []shelltool.Snapshot {
	if s.manager == nil {
		return nil
	}
	return s.manager.List()
}

func (s *testEmbeddedServer) sessionWorkspaceRetargeter(metadataStore *metadata.Store) *sessionservice.SessionWorkspaceRetargeter {
	runtimes := registry.NewRuntimeRegistry()
	return sessionservice.NewSessionWorkspaceRetargeter(
		metadataStore,
		s.sessionAuthority(metadataStore.AuthoritativeSessionStoreOptions()...),
		runtimes,
		testSessionProcessSource{manager: s.background},
	)
}

func (s *testEmbeddedServer) SessionLaunchClient() apicontract.SessionLaunchService {
	if s.sessionLaunch != nil {
		return s.sessionLaunch
	}
	if metadataStore, binding, ok := s.metadataBinding(); ok {
		service := sessionlaunch.NewService(launch.Planner{
			Config:            s.cfg,
			ContainerDir:      filepath.Join(filepath.Join(s.cfg.PersistenceRoot, "projects"), binding.ProjectID, "sessions"),
			StoreOptions:      metadataStore.AuthoritativeSessionStoreOptions(),
			PersistedSessions: metadataStore,
		})
		return service
	}
	var storeOptions []session.StoreOption
	if s.sessionPersistence != nil {
		storeOptions = s.sessionPersistence.Options()
	}
	service := sessionlaunch.NewService(launch.Planner{
		Config:            s.cfg,
		ContainerDir:      s.containerDir,
		StoreOptions:      storeOptions,
		PersistedSessions: s.sessionPersistence,
	})
	return service
}

func (s *testEmbeddedServer) SessionLifecycleClient() apicontract.SessionLifecycleService {
	if s.sessionLifecycle != nil {
		return s.sessionLifecycle
	}
	if metadataStore, binding, ok := s.metadataBinding(); ok {
		service := sessionservice.NewSessionLifecycleService(
			filepath.Join(filepath.Join(s.cfg.PersistenceRoot, "projects"), binding.ProjectID, "sessions"),
			s.sessionAuthority(metadataStore.AuthoritativeSessionStoreOptions()...),
			s.authManager,
		).WithPersistenceRoot(s.cfg.PersistenceRoot).
			WithWorkspaceRetargeter(s.sessionWorkspaceRetargeter(metadataStore)).
			WithNavigationTargetResolver(metadataStore)
		return service
	}
	containerDir := strings.TrimSpace(s.containerDir)
	if containerDir == "" {
		projectID := strings.TrimSpace(s.ProjectID())
		if projectID == "" {
			projectID = "test-project"
		}
		containerDir = filepath.Join(filepath.Join(s.cfg.PersistenceRoot, "projects"), projectID, "sessions")
	}
	var storeOptions []session.StoreOption
	if s.sessionPersistence != nil {
		storeOptions = s.sessionPersistence.Options()
	}
	service := sessionservice.NewSessionLifecycleService(containerDir, s.sessionAuthority(storeOptions...), s.authManager).
		WithPersistenceRoot(s.cfg.PersistenceRoot)
	return service
}

func (s *testEmbeddedServer) SessionRuntimeClient() apicontract.SessionRuntimeService {
	return s.sessionRuntime
}

func (s *testEmbeddedServer) SessionViewClient() apicontract.SessionViewService {
	if s.sessionViewClient != nil {
		return s.sessionViewClient
	}
	return stubSessionViewClient{getSessionMainView: func(_ context.Context, req serverapi.SessionMainViewRequest) (serverapi.SessionMainViewResponse, error) {
		root := strings.TrimSpace(s.cfg.WorkspaceRoot)
		return serverapi.SessionMainViewResponse{MainView: clientui.RuntimeMainView{Session: clientui.RuntimeSessionView{
			SessionID: req.SessionID,
			ExecutionTarget: clientui.SessionExecutionTarget{
				WorkspaceRoot:         root,
				WorkspaceAvailability: clientui.ProjectAvailabilityAvailable,
				EffectiveWorkdir:      root,
			},
		}}}, nil
	}}
}

func (s *testEmbeddedServer) WorktreeClient() apicontract.WorktreeService {
	return nil
}

func (s *testEmbeddedServer) RuntimeAttachmentClients() runtimeAttachmentClients {
	return runtimeAttachmentClients{
		ProcessControls:   s.processControlClient,
		ProcessOutput:     s.processOutputClient,
		ProcessViews:      s.processViewClient,
		PromptControl:     s.promptControlClient,
		RuntimeControls:   s.RuntimeControlClient(),
		SessionRuntime:    s.sessionRuntime,
		SessionTranscript: s.sessionTranscript,
		SessionViews:      s.sessionViewClient,
		Worktrees:         s.WorktreeClient(),
	}
}

func (s *testEmbeddedServer) PrepareRuntime(ctx context.Context, plan sessionLaunchPlan, diagnosticWriter io.Writer, startLogLine string) (*runtimeLaunchPlan, error) {
	if s.prepareRuntime != nil {
		return s.prepareRuntime(ctx, plan, diagnosticWriter, startLogLine)
	}
	return nil, errors.New("test embedded server prepare runtime not configured")
}

func (s *testEmbeddedServer) Reauthenticate(ctx context.Context, interactor authInteractor, interactiveAuth bool) error {
	if s.reauthenticate != nil {
		return s.reauthenticate(ctx, interactor)
	}
	service := authservice.NewBootstrapService(s.authManager, s.oauthOpts, s.cfg.Settings, apicontract.AllowedPreAuthMethods())
	remote := service
	status, err := remote.GetAuthBootstrapStatus(ctx, serverapi.AuthGetBootstrapStatusRequest{})
	if err != nil {
		return err
	}
	if interactive, ok := interactor.(*interactiveAuthInteractor); ok {
		return interactive.completeRemoteAuthBootstrap(ctx, remote, s.cfg.Settings, status, true)
	}
	return ensureRemoteAuthReady(ctx, remote, s.cfg.Settings, interactor, interactiveAuth)
}

func (s *testEmbeddedServer) EnsureAuthReady(ctx context.Context, interactor authInteractor, interactiveAuth bool) error {
	service := authservice.NewBootstrapService(s.authManager, s.oauthOpts, s.cfg.Settings, apicontract.AllowedPreAuthMethods())
	return ensureRemoteAuthReady(ctx, service, s.cfg.Settings, interactor, interactiveAuth)
}
