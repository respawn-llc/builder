package app

import (
	"context"
	"core/cli/app/internal/status"
	"core/server/auth"
	"core/server/authservice"
	"core/server/launch"
	"core/server/metadata"
	"core/server/projectview"
	"core/server/registry"
	"core/server/runtime"
	"core/server/runtimecontrol"
	"core/server/sessionlaunch"
	"core/server/sessionservice"
	serverstartup "core/server/startup"
	shelltool "core/server/tools/shell"
	rpccontract "core/shared/apicontract"
	"core/shared/client"
	"core/shared/clientui"
	"core/shared/config"
	"core/shared/serverapi"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

type testEmbeddedServer struct {
	cfg                  config.App
	containerDir         string
	oauthOpts            auth.OpenAIOAuthOptions
	authManager          *auth.Manager
	fastModeState        *runtime.FastModeState
	background           *shelltool.Manager
	backgroundRouter     serverstartup.BackgroundRouter
	runPromptClient      client.RunPromptClient
	projectID            string
	boundWorkspaceID     string
	askViewClient        client.AskViewClient
	approvalViewClient   client.ApprovalViewClient
	promptControlClient  client.PromptControlClient
	promptActivityClient client.PromptActivityClient
	projectViewClient    client.ProjectViewClient
	processControlClient client.ProcessControlClient
	processOutputClient  client.ProcessOutputClient
	processViewClient    client.ProcessViewClient
	runtimeControlClient client.RuntimeControlClient
	sessionLaunch        client.SessionLaunchClient
	sessionActivity      client.SessionActivityClient
	sessionLifecycle     client.SessionLifecycleClient
	sessionRuntime       client.SessionRuntimeClient
	sessionViewClient    client.SessionViewClient
	sessionStores        *registry.SessionStoreRegistry
	metadataOnce         sync.Once
	metadataStore        *metadata.Store
	metadataBindingData  metadata.Binding
	metadataBindingOK    bool
	prepareRuntime       func(ctx context.Context, plan sessionLaunchPlan, diagnosticWriter io.Writer, startLogLine string) (*runtimeLaunchPlan, error)
	reauthenticate       func(ctx context.Context, interactor authInteractor) error
}

type noOpSessionActivitySubscription struct{}

func (noOpSessionActivitySubscription) Next(context.Context) (clientui.Event, error) {
	return clientui.Event{}, io.EOF
}

func (noOpSessionActivitySubscription) Close() error { return nil }

type recordingSessionRuntimeClient struct {
	activate func(context.Context, serverapi.SessionRuntimeActivateRequest) (serverapi.SessionRuntimeActivateResponse, error)
	release  func(context.Context, serverapi.SessionRuntimeReleaseRequest) (serverapi.SessionRuntimeReleaseResponse, error)
}

func (c *recordingSessionRuntimeClient) ActivateSessionRuntime(ctx context.Context, req serverapi.SessionRuntimeActivateRequest) (serverapi.SessionRuntimeActivateResponse, error) {
	if c.activate != nil {
		return c.activate(ctx, req)
	}
	return serverapi.SessionRuntimeActivateResponse{}, nil
}

func (c *recordingSessionRuntimeClient) ReleaseSessionRuntime(ctx context.Context, req serverapi.SessionRuntimeReleaseRequest) (serverapi.SessionRuntimeReleaseResponse, error) {
	if c.release != nil {
		return c.release(ctx, req)
	}
	return serverapi.SessionRuntimeReleaseResponse{}, nil
}

type recordingSessionActivityClient struct {
	subscribe func(context.Context, serverapi.SessionActivitySubscribeRequest) (serverapi.SessionActivitySubscription, error)
}

func (c *recordingSessionActivityClient) SubscribeSessionActivity(ctx context.Context, req serverapi.SessionActivitySubscribeRequest) (serverapi.SessionActivitySubscription, error) {
	if c.subscribe != nil {
		return c.subscribe(ctx, req)
	}
	return noOpSessionActivitySubscription{}, nil
}

type recordingPromptActivityClient struct {
	subscribe func(context.Context, serverapi.PromptActivitySubscribeRequest) (serverapi.PromptActivitySubscription, error)
}

func (c *recordingPromptActivityClient) SubscribePromptActivity(ctx context.Context, req serverapi.PromptActivitySubscribeRequest) (serverapi.PromptActivitySubscription, error) {
	if c.subscribe != nil {
		return c.subscribe(ctx, req)
	}
	return nil, nil
}

type stubEmbeddedProcessViewClient struct {
	listResp serverapi.ProcessListResponse
	getResp  serverapi.ProcessGetResponse
	err      error
}

type stubEmbeddedProcessControlClient struct {
	inlineResp serverapi.ProcessInlineOutputResponse
	err        error
	killed     []string
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
		backgroundRouter:     s.backgroundRouter,
		runPromptClient:      s.runPromptClient,
		projectID:            strings.TrimSpace(projectID),
		boundWorkspaceID:     s.boundWorkspaceID,
		askViewClient:        s.askViewClient,
		approvalViewClient:   s.approvalViewClient,
		promptControlClient:  s.promptControlClient,
		promptActivityClient: s.promptActivityClient,
		projectViewClient:    s.projectViewClient,
		processControlClient: s.processControlClient,
		processOutputClient:  s.processOutputClient,
		processViewClient:    s.processViewClient,
		runtimeControlClient: s.runtimeControlClient,
		sessionLaunch:        s.sessionLaunch,
		sessionActivity:      s.sessionActivity,
		sessionLifecycle:     s.sessionLifecycle,
		sessionRuntime:       s.sessionRuntime,
		sessionViewClient:    s.sessionViewClient,
		sessionStores:        s.sessionStores,
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

func (s *testEmbeddedServer) ProjectViewClient() client.ProjectViewClient {
	if s.projectViewClient != nil {
		return s.projectViewClient
	}
	if metadataStore, binding, ok := s.metadataBinding(); ok {
		service, err := projectview.NewMetadataService(metadataStore, binding.ProjectID)
		if err == nil {
			return client.NewLoopbackProjectViewClient(service)
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
	return client.NewLoopbackProjectViewClient(service)
}

func (s *testEmbeddedServer) AskViewClient() client.AskViewClient { return s.askViewClient }

func (s *testEmbeddedServer) ApprovalViewClient() client.ApprovalViewClient {
	return s.approvalViewClient
}

func (s *testEmbeddedServer) PromptControlClient() client.PromptControlClient {
	return s.promptControlClient
}

func (s *testEmbeddedServer) PromptActivityClient() client.PromptActivityClient {
	return s.promptActivityClient
}

func (s *testEmbeddedServer) ContainerDir() string { return s.containerDir }

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

func (s *testEmbeddedServer) AuthStatusClient() client.AuthStatusClient {
	return nil
}

func (s *testEmbeddedServer) FastModeState() *runtime.FastModeState { return s.fastModeState }

func (s *testEmbeddedServer) Background() *shelltool.Manager { return s.background }

func (s *testEmbeddedServer) BackgroundRouter() serverstartup.BackgroundRouter {
	return s.backgroundRouter
}

func (s *testEmbeddedServer) RunPromptClient() client.RunPromptClient { return s.runPromptClient }

func (s *testEmbeddedServer) ProcessControlClient() client.ProcessControlClient {
	return s.processControlClient
}

func (s *testEmbeddedServer) ProcessOutputClient() client.ProcessOutputClient {
	return s.processOutputClient
}

func (s *testEmbeddedServer) ProcessViewClient() client.ProcessViewClient {
	return s.processViewClient
}

func (s *testEmbeddedServer) RuntimeControlClient() client.RuntimeControlClient {
	if s.runtimeControlClient != nil {
		return s.runtimeControlClient
	}
	registry := registry.NewRuntimeRegistry()
	return client.NewLoopbackRuntimeControlClient(runtimecontrol.NewService(registry))
}

func (s *testEmbeddedServer) sessionStoreRegistry() *registry.SessionStoreRegistry {
	if s.sessionStores == nil {
		s.sessionStores = registry.NewSessionStoreRegistry()
	}
	return s.sessionStores
}

func (s *testEmbeddedServer) SessionLaunchClient() client.SessionLaunchClient {
	if s.sessionLaunch != nil {
		return s.sessionLaunch
	}
	if metadataStore, binding, ok := s.metadataBinding(); ok {
		service := sessionlaunch.NewService(launch.Planner{
			Config:       s.cfg,
			ContainerDir: filepath.Join(filepath.Join(s.cfg.PersistenceRoot, "projects"), binding.ProjectID, "sessions"),
			StoreOptions: metadataStore.AuthoritativeSessionStoreOptions(),
		}, s.sessionStoreRegistry())
		return client.NewLoopbackSessionLaunchClient(service)
	}
	service := sessionlaunch.NewService(launch.Planner{Config: s.cfg, ContainerDir: s.containerDir}, s.sessionStoreRegistry())
	return client.NewLoopbackSessionLaunchClient(service)
}

func (s *testEmbeddedServer) SessionActivityClient() client.SessionActivityClient {
	return s.sessionActivity
}

func (s *testEmbeddedServer) SessionLifecycleClient() client.SessionLifecycleClient {
	if s.sessionLifecycle != nil {
		return s.sessionLifecycle
	}
	if metadataStore, binding, ok := s.metadataBinding(); ok {
		service := sessionservice.NewSessionLifecycleService(
			filepath.Join(filepath.Join(s.cfg.PersistenceRoot, "projects"), binding.ProjectID, "sessions"),
			s.sessionStoreRegistry(),
			s.authManager,
			metadataStore.AuthoritativeSessionStoreOptions()...,
		).WithPersistenceRoot(s.cfg.PersistenceRoot)
		return client.NewLoopbackSessionLifecycleClient(service)
	}
	containerDir := strings.TrimSpace(s.containerDir)
	if containerDir == "" {
		projectID := strings.TrimSpace(s.ProjectID())
		if projectID == "" {
			projectID = "test-project"
		}
		containerDir = filepath.Join(filepath.Join(s.cfg.PersistenceRoot, "projects"), projectID, "sessions")
	}
	service := sessionservice.NewSessionLifecycleService(containerDir, s.sessionStoreRegistry(), s.authManager).WithPersistenceRoot(s.cfg.PersistenceRoot)
	return client.NewLoopbackSessionLifecycleClient(service)
}

func (s *testEmbeddedServer) SessionRuntimeClient() client.SessionRuntimeClient {
	return s.sessionRuntime
}

func (s *testEmbeddedServer) SessionViewClient() client.SessionViewClient {
	return s.sessionViewClient
}

func (s *testEmbeddedServer) WorktreeClient() client.WorktreeClient {
	return nil
}

func (s *testEmbeddedServer) RuntimeAttachmentClients() runtimeAttachmentClients {
	return runtimeAttachmentClients{
		ApprovalViews:   s.approvalViewClient,
		AskViews:        s.askViewClient,
		ProcessControls: s.processControlClient,
		ProcessOutput:   s.processOutputClient,
		ProcessViews:    s.processViewClient,
		PromptActivity:  s.promptActivityClient,
		PromptControl:   s.promptControlClient,
		RuntimeControls: s.RuntimeControlClient(),
		SessionActivity: s.sessionActivity,
		SessionRuntime:  s.sessionRuntime,
		SessionViews:    s.sessionViewClient,
		Worktrees:       s.WorktreeClient(),
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
	service := authservice.NewBootstrapService(s.authManager, s.oauthOpts, s.cfg.Settings, rpccontract.AllowedPreAuthMethods())
	remote := client.NewLoopbackAuthBootstrapClient(service)
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
	service := authservice.NewBootstrapService(s.authManager, s.oauthOpts, s.cfg.Settings, rpccontract.AllowedPreAuthMethods())
	return ensureRemoteAuthReady(ctx, client.NewLoopbackAuthBootstrapClient(service), s.cfg.Settings, interactor, interactiveAuth)
}

func (s *stubEmbeddedProcessViewClient) ListProcesses(context.Context, serverapi.ProcessListRequest) (serverapi.ProcessListResponse, error) {
	if s.err != nil {
		return serverapi.ProcessListResponse{}, s.err
	}
	return s.listResp, nil
}

func (s *stubEmbeddedProcessViewClient) GetProcess(context.Context, serverapi.ProcessGetRequest) (serverapi.ProcessGetResponse, error) {
	if s.err != nil {
		return serverapi.ProcessGetResponse{}, s.err
	}
	return s.getResp, nil
}

func (s *stubEmbeddedProcessControlClient) KillProcess(_ context.Context, req serverapi.ProcessKillRequest) (serverapi.ProcessKillResponse, error) {
	if s.err != nil {
		return serverapi.ProcessKillResponse{}, s.err
	}
	s.killed = append(s.killed, req.ProcessID)
	return serverapi.ProcessKillResponse{}, nil
}

func (s *stubEmbeddedProcessControlClient) GetInlineOutput(context.Context, serverapi.ProcessInlineOutputRequest) (serverapi.ProcessInlineOutputResponse, error) {
	if s.err != nil {
		return serverapi.ProcessInlineOutputResponse{}, s.err
	}
	return s.inlineResp, nil
}

func TestEmbeddedAppServerPrepareRuntimeRegistersRuntimeForSessionViews(t *testing.T) {
	_, workspace := newRegisteredAppWorkspace(t)

	server, err := startEmbeddedServer(context.Background(), Options{WorkspaceRoot: workspace}, readyMemoryAuthHandler(), false)
	if err != nil {
		t.Fatalf("start embedded server: %v", err)
	}
	defer func() { _ = server.Close() }()

	plan, runtimePlan := prepareAppRuntimePlan(t, server, sessionLaunchRequest{Mode: launchModeInteractive}, io.Discard, "test prepare runtime")
	defer runtimePlan.Close()
	if err := runtimePlan.Wiring.runtimeControls.SetThinkingLevel(context.Background(), serverapi.RuntimeSetThinkingLevelRequest{ClientRequestID: uuid.NewString(), SessionID: plan.SessionID, Level: "high"}); err != nil {
		t.Fatalf("set thinking level: %v", err)
	}

	resp, err := server.SessionViewClient().GetSessionMainView(context.Background(), serverapi.SessionMainViewRequest{SessionID: plan.SessionID})
	if err != nil {
		t.Fatalf("get session main view while runtime attached: %v", err)
	}
	if resp.MainView.Session.SessionID != plan.SessionID {
		t.Fatalf("session id = %q, want %q", resp.MainView.Session.SessionID, plan.SessionID)
	}
	if resp.MainView.Status.ThinkingLevel != "high" {
		t.Fatalf("thinking level = %q, want high", resp.MainView.Status.ThinkingLevel)
	}
}

func TestEmbeddedAppServerPrepareRuntimeWiresProcessReadsForUIHydration(t *testing.T) {
	_, workspace := newRegisteredAppWorkspace(t)

	server, err := startEmbeddedServer(context.Background(), Options{WorkspaceRoot: workspace}, readyMemoryAuthHandler(), false)
	if err != nil {
		t.Fatalf("start embedded server: %v", err)
	}
	defer func() { _ = server.Close() }()

	plan, runtimePlan := prepareAppRuntimePlan(t, server, sessionLaunchRequest{Mode: launchModeInteractive}, io.Discard, "test prepare runtime process reads")
	defer runtimePlan.Close()
	if runtimePlan.Wiring.processViews == nil {
		t.Fatal("expected PrepareRuntime to wire process view client")
	}

	manager := server.inner.Background()
	if manager == nil {
		t.Fatal("expected server background manager")
	}
	manager.SetMinimumExecToBgTime(fastBackgroundTestYield)
	res, err := manager.Start(context.Background(), shelltool.ExecRequest{
		Command:        []string{"sh", "-c", "printf 'local\n'; sleep 1"},
		DisplayCommand: "local-process",
		OwnerSessionID: plan.SessionID,
		OwnerRunID:     "local-run",
		OwnerStepID:    "local-step",
		Workdir:        workspace,
		YieldTime:      fastBackgroundTestYield,
	})
	if err != nil {
		t.Fatalf("start background process: %v", err)
	}
	if !res.Backgrounded {
		t.Fatal("expected backgrounded local process")
	}

	runtimePlan.Wiring.processViews = &stubEmbeddedProcessViewClient{listResp: serverapi.ProcessListResponse{Processes: []clientui.BackgroundProcess{{
		ID:             "remote-proc",
		OwnerSessionID: plan.SessionID,
		OwnerRunID:     "remote-run",
		OwnerStepID:    "remote-step",
		Command:        "remote-process",
	}}}}

	processClient := newUIProcessClientWithReads(runtimePlan.Wiring.processViews, runtimePlan.Wiring.processControls)
	got, err := processClient.ListProcesses(context.Background())
	if err != nil {
		t.Fatalf("ListProcesses: %v", err)
	}
	if len(got) != 1 || got[0].ID != "remote-proc" || got[0].OwnerRunID != "remote-run" || got[0].OwnerStepID != "remote-step" {
		t.Fatalf("expected shared process reads to win over local manager snapshot, got %+v", got)
	}
}

func TestEmbeddedAppServerPrepareRuntimeExposesPendingAsksAndApprovals(t *testing.T) {
	_, workspace := newRegisteredAppWorkspace(t)

	server, err := startEmbeddedServer(context.Background(), Options{WorkspaceRoot: workspace}, readyMemoryAuthHandler(), false)
	if err != nil {
		t.Fatalf("start embedded server: %v", err)
	}
	defer func() { _ = server.Close() }()

	_, runtimePlan := prepareAppRuntimePlan(t, server, sessionLaunchRequest{Mode: launchModeInteractive}, io.Discard, "test prepare runtime pending prompts")
	defer runtimePlan.Close()
	if runtimePlan.Wiring.askViews == nil || runtimePlan.Wiring.approvalViews == nil || runtimePlan.Wiring.promptControl == nil {
		t.Fatal("expected PrepareRuntime to wire shared prompt clients")
	}
}

func waitForPendingAskResources(t *testing.T, client client.AskViewClient, sessionID string, want int) []clientui.PendingAsk {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := client.ListPendingAsksBySession(context.Background(), serverapi.AskListPendingBySessionRequest{SessionID: sessionID})
		if err != nil {
			t.Fatalf("ListPendingAsksBySession: %v", err)
		}
		if len(resp.Asks) == want {
			return resp.Asks
		}
		time.Sleep(10 * time.Millisecond)
	}
	resp, err := client.ListPendingAsksBySession(context.Background(), serverapi.AskListPendingBySessionRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("ListPendingAsksBySession final: %v", err)
	}
	t.Fatalf("timed out waiting for %d pending asks, got %+v", want, resp.Asks)
	return nil
}

func waitForPendingApprovalResources(t *testing.T, client client.ApprovalViewClient, sessionID string, want int) []clientui.PendingApproval {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := client.ListPendingApprovalsBySession(context.Background(), serverapi.ApprovalListPendingBySessionRequest{SessionID: sessionID})
		if err != nil {
			t.Fatalf("ListPendingApprovalsBySession: %v", err)
		}
		if len(resp.Approvals) == want {
			return resp.Approvals
		}
		time.Sleep(10 * time.Millisecond)
	}
	resp, err := client.ListPendingApprovalsBySession(context.Background(), serverapi.ApprovalListPendingBySessionRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("ListPendingApprovalsBySession final: %v", err)
	}
	t.Fatalf("timed out waiting for %d pending approvals, got %+v", want, resp.Approvals)
	return nil
}

func TestEmbeddedAppServerPrepareRuntimeWiresSessionActivityForSharedClients(t *testing.T) {
	_, workspace := newRegisteredAppWorkspace(t)

	server, err := startEmbeddedServer(context.Background(), Options{WorkspaceRoot: workspace}, readyMemoryAuthHandler(), false)
	if err != nil {
		t.Fatalf("start embedded server: %v", err)
	}
	defer func() { _ = server.Close() }()

	plan, runtimePlan := prepareAppRuntimePlan(t, server, sessionLaunchRequest{Mode: launchModeInteractive}, io.Discard, "test prepare runtime session activity")
	defer runtimePlan.Close()

	reads := server.SessionViewClient()
	if reads == nil {
		t.Fatal("expected session view client")
	}
	hydrated, err := reads.GetSessionMainView(context.Background(), serverapi.SessionMainViewRequest{SessionID: plan.SessionID})
	if err != nil {
		t.Fatalf("GetSessionMainView: %v", err)
	}
	if hydrated.MainView.Session.SessionID != plan.SessionID {
		t.Fatalf("unexpected hydrated session: %+v", hydrated.MainView.Session)
	}

	activity := server.inner.SessionActivityClient()
	if activity == nil {
		t.Fatal("expected session activity client")
	}
	first, err := activity.SubscribeSessionActivity(context.Background(), serverapi.SessionActivitySubscribeRequest{SessionID: plan.SessionID})
	if err != nil {
		t.Fatalf("SubscribeSessionActivity first: %v", err)
	}
	defer func() { _ = first.Close() }()
	second, err := activity.SubscribeSessionActivity(context.Background(), serverapi.SessionActivitySubscribeRequest{SessionID: plan.SessionID})
	if err != nil {
		t.Fatalf("SubscribeSessionActivity second: %v", err)
	}
	defer func() { _ = second.Close() }()

	runtimePlan.Wiring.runtimeClient.AppendCommittedEntry("user", "hello from client one")

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	firstEvt, err := first.Next(ctx)
	if err != nil {
		t.Fatalf("first.Next: %v", err)
	}
	secondEvt, err := second.Next(ctx)
	if err != nil {
		t.Fatalf("second.Next: %v", err)
	}
	if firstEvt.Kind != clientui.EventLocalEntryAdded || secondEvt.Kind != clientui.EventLocalEntryAdded {
		t.Fatalf("unexpected activity events: first=%+v second=%+v", firstEvt, secondEvt)
	}
	if len(firstEvt.TranscriptEntries) != 1 || firstEvt.TranscriptEntries[0].Text != "hello from client one" {
		t.Fatalf("unexpected first local entry event: %+v", firstEvt)
	}
	if len(secondEvt.TranscriptEntries) != 1 || secondEvt.TranscriptEntries[0].Text != "hello from client one" {
		t.Fatalf("unexpected second local entry event: %+v", secondEvt)
	}

	if _, err := reads.GetSessionMainView(context.Background(), serverapi.SessionMainViewRequest{SessionID: plan.SessionID}); err != nil {
		t.Fatalf("GetSessionMainView refreshed: %v", err)
	}
	page, err := reads.GetSessionTranscriptPage(context.Background(), serverapi.SessionTranscriptPageRequest{SessionID: plan.SessionID})
	if err != nil {
		t.Fatalf("GetSessionTranscriptPage refreshed: %v", err)
	}
	if len(page.Transcript.Entries) == 0 {
		t.Fatalf("expected hydrated transcript entries after activity: %+v", page.Transcript)
	}
	last := page.Transcript.Entries[len(page.Transcript.Entries)-1]
	if last.Text != "hello from client one" {
		t.Fatalf("unexpected hydrated entry: %+v", last)
	}
}

func TestEmbeddedAppServerPrepareRuntimeIsolatesSessionActivityBetweenSessions(t *testing.T) {
	_, workspace := newRegisteredAppWorkspace(t)

	server, err := startEmbeddedServer(context.Background(), Options{WorkspaceRoot: workspace}, readyMemoryAuthHandler(), false)
	if err != nil {
		t.Fatalf("start embedded server: %v", err)
	}
	defer func() { _ = server.Close() }()

	planA, runtimePlanA := prepareAppRuntimePlan(t, server, sessionLaunchRequest{Mode: launchModeInteractive}, io.Discard, "test prepare runtime session activity A")
	defer runtimePlanA.Close()

	planB, runtimePlanB := prepareAppRuntimePlan(t, server, sessionLaunchRequest{Mode: launchModeInteractive}, io.Discard, "test prepare runtime session activity B")
	defer runtimePlanB.Close()

	activity := server.inner.SessionActivityClient()
	if activity == nil {
		t.Fatal("expected session activity client")
	}
	subA, err := activity.SubscribeSessionActivity(context.Background(), serverapi.SessionActivitySubscribeRequest{SessionID: planA.SessionID})
	if err != nil {
		t.Fatalf("SubscribeSessionActivity A: %v", err)
	}
	defer func() { _ = subA.Close() }()
	subB, err := activity.SubscribeSessionActivity(context.Background(), serverapi.SessionActivitySubscribeRequest{SessionID: planB.SessionID})
	if err != nil {
		t.Fatalf("SubscribeSessionActivity B: %v", err)
	}
	defer func() { _ = subB.Close() }()

	runtimePlanA.Wiring.runtimeClient.AppendCommittedEntry("user", "session-a-only")

	ctxA, cancelA := context.WithTimeout(context.Background(), time.Second)
	defer cancelA()
	evtA, err := subA.Next(ctxA)
	if err != nil {
		t.Fatalf("subA.Next: %v", err)
	}
	if evtA.Kind != clientui.EventLocalEntryAdded {
		t.Fatalf("unexpected session A event: %+v", evtA)
	}
	if len(evtA.TranscriptEntries) != 1 || evtA.TranscriptEntries[0].Text != "session-a-only" {
		t.Fatalf("unexpected session A local entry payload: %+v", evtA)
	}
	ctxB, cancelB := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancelB()
	if evtB, err := subB.Next(ctxB); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected session B stream to stay idle, got evt=%+v err=%v", evtB, err)
	}

	reads := server.SessionViewClient()
	if reads == nil {
		t.Fatal("expected session view client")
	}
	pageA, err := reads.GetSessionTranscriptPage(context.Background(), serverapi.SessionTranscriptPageRequest{SessionID: planA.SessionID})
	if err != nil {
		t.Fatalf("GetSessionTranscriptPage A: %v", err)
	}
	if !transcriptPageContainsText(pageA.Transcript, "session-a-only") {
		t.Fatalf("expected session A transcript to contain appended entry, got %+v", pageA.Transcript)
	}
	pageB, err := reads.GetSessionTranscriptPage(context.Background(), serverapi.SessionTranscriptPageRequest{SessionID: planB.SessionID})
	if err != nil {
		t.Fatalf("GetSessionTranscriptPage B: %v", err)
	}
	if transcriptPageContainsText(pageB.Transcript, "session-a-only") {
		t.Fatalf("session B transcript leaked session A entry: %+v", pageB.Transcript)
	}

	runtimePlanB.Wiring.runtimeClient.AppendCommittedEntry("assistant", "session-b-only")

	ctxB2, cancelB2 := context.WithTimeout(context.Background(), time.Second)
	defer cancelB2()
	evtB, err := subB.Next(ctxB2)
	if err != nil {
		t.Fatalf("subB.Next after session B append: %v", err)
	}
	if evtB.Kind != clientui.EventLocalEntryAdded {
		t.Fatalf("unexpected session B event: %+v", evtB)
	}
	if len(evtB.TranscriptEntries) != 1 || evtB.TranscriptEntries[0].Text != "session-b-only" {
		t.Fatalf("unexpected session B local entry payload: %+v", evtB)
	}
	ctxA2, cancelA2 := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancelA2()
	if evtA2, err := subA.Next(ctxA2); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected session A stream to stay idle after session B append, got evt=%+v err=%v", evtA2, err)
	}

	pageB, err = reads.GetSessionTranscriptPage(context.Background(), serverapi.SessionTranscriptPageRequest{SessionID: planB.SessionID})
	if err != nil {
		t.Fatalf("GetSessionTranscriptPage B after append: %v", err)
	}
	if !transcriptPageContainsText(pageB.Transcript, "session-b-only") {
		t.Fatalf("expected session B transcript to contain appended entry, got %+v", pageB.Transcript)
	}
	pageA, err = reads.GetSessionTranscriptPage(context.Background(), serverapi.SessionTranscriptPageRequest{SessionID: planA.SessionID})
	if err != nil {
		t.Fatalf("GetSessionTranscriptPage A after session B append: %v", err)
	}
	if transcriptPageContainsText(pageA.Transcript, "session-b-only") {
		t.Fatalf("session A transcript leaked session B entry: %+v", pageA.Transcript)
	}
}

func TestEmbeddedAppServerRoutesBackgroundCompletionToOwningSessionOnly(t *testing.T) {
	_, workspace := newRegisteredAppWorkspace(t)

	server, err := startEmbeddedServer(context.Background(), Options{WorkspaceRoot: workspace}, readyMemoryAuthHandler(), false)
	if err != nil {
		t.Fatalf("start embedded server: %v", err)
	}
	defer func() { _ = server.Close() }()

	planA, runtimePlanA := prepareAppRuntimePlan(t, server, sessionLaunchRequest{Mode: launchModeInteractive}, io.Discard, "test background completion isolation A")
	defer runtimePlanA.Close()

	planB, runtimePlanB := prepareAppRuntimePlan(t, server, sessionLaunchRequest{Mode: launchModeInteractive}, io.Discard, "test background completion isolation B")
	defer runtimePlanB.Close()

	activity := server.inner.SessionActivityClient()
	if activity == nil {
		t.Fatal("expected session activity client")
	}
	subA, err := activity.SubscribeSessionActivity(context.Background(), serverapi.SessionActivitySubscribeRequest{SessionID: planA.SessionID})
	if err != nil {
		t.Fatalf("SubscribeSessionActivity A: %v", err)
	}
	defer func() { _ = subA.Close() }()
	subB, err := activity.SubscribeSessionActivity(context.Background(), serverapi.SessionActivitySubscribeRequest{SessionID: planB.SessionID})
	if err != nil {
		t.Fatalf("SubscribeSessionActivity B: %v", err)
	}
	defer func() { _ = subB.Close() }()

	processID := "bg-owned-a"
	server.inner.BackgroundRouter().Handle(shelltool.Event{
		Type:             shelltool.EventCompleted,
		NoticeSuppressed: true,
		Snapshot: shelltool.Snapshot{
			ID:             processID,
			OwnerSessionID: planA.SessionID,
			State:          "completed",
			Command:        "sleep 1; printf done",
			Workdir:        workspace,
			LogPath:        "/tmp/bg-owned-a.log",
		},
		Preview: "done",
	})

	evtA := waitForSessionActivityEvent(t, subA, 5*time.Second, func(evt clientui.Event) bool {
		return evt.Kind == clientui.EventBackgroundUpdated && evt.Background != nil && evt.Background.ID == processID && evt.Background.Type == "completed"
	})
	if evtA.Background == nil || evtA.Background.ID != processID {
		t.Fatalf("unexpected session A background event: %+v", evtA.Background)
	}

	ctxB, cancelB := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancelB()
	if evtB, err := subB.Next(ctxB); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected session B stream to stay idle for session A background completion, got evt=%+v err=%v", evtB, err)
	}
}

func transcriptPageContainsText(page clientui.TranscriptPage, want string) bool {
	for _, entry := range page.Entries {
		if entry.Text == want {
			return true
		}
	}
	return false
}

func waitForSessionActivityEvent(t *testing.T, sub serverapi.SessionActivitySubscription, timeout time.Duration, match func(clientui.Event) bool) clientui.Event {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Until(deadline))
		evt, err := sub.Next(ctx)
		cancel()
		if err != nil {
			t.Fatalf("session activity Next: %v", err)
		}
		if match == nil || match(evt) {
			return evt
		}
	}
	t.Fatal("timed out waiting for matching session activity event")
	return clientui.Event{}
}
