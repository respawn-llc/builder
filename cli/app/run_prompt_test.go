package app

import (
	"context"
	"core/cli/app/internal/daemonlaunch"
	"core/cli/app/internal/remoteattach"
	"core/server/auth"
	"core/server/authservice"
	"core/server/launch"
	"core/server/runprompt"
	"core/server/session"
	serverstartup "core/server/startup"
	askquestion "core/server/tools"
	"core/shared/client"
	"core/shared/clientui"
	"core/shared/config"
	"core/shared/protocol"
	"core/shared/serverapi"
	"core/shared/sessioncontract"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type memoryAuthHandler struct {
	state     auth.State
	lookupEnv func(string) string
}

func readyMemoryAuthHandler() memoryAuthHandler {
	return apiKeyMemoryAuthHandler("in-memory-test-key")
}

func apiKeyMemoryAuthHandler(key string) memoryAuthHandler {
	state := apiKeyMemoryAuthState(key)
	state.UpdatedAt = time.Now().UTC()
	return memoryAuthHandler{state: state}
}

func apiKeyMemoryAuthHandlerWithoutTimestamp(key string) memoryAuthHandler {
	return memoryAuthHandler{state: apiKeyMemoryAuthState(key)}
}

func apiKeyMemoryAuthState(key string) auth.State {
	return auth.State{
		Scope: auth.ScopeGlobal,
		Method: auth.Method{
			Type:   auth.MethodAPIKey,
			APIKey: &auth.APIKeyMethod{Key: key},
		},
	}
}

func saveReadyAppAuthState(t *testing.T, workspace string) {
	t.Helper()
	cfg := loadAppTestConfig(t, workspace, config.LoadOptions{})
	store := auth.NewFileStore(config.GlobalAuthConfigPath(cfg))
	if err := store.Save(context.Background(), readyMemoryAuthHandler().state); err != nil {
		t.Fatalf("save auth state: %v", err)
	}
}

func TestLoadRemoteAttachConfigUsesSessionWorkspaceWhenWorkspaceImplicit(t *testing.T) {
	home := newAppTestHome(t)
	workspace := t.TempDir()
	worktree := filepath.Join(home, config.ConfigDirName, "worktrees", "project", "feature")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}
	configureAppTestServerPort(t)
	cfg := loadAppTestConfig(t, workspace, config.LoadOptions{})
	store := createAuthoritativeAppSession(t, cfg.PersistenceRoot, cfg.WorkspaceRoot)

	got, err := loadRemoteAttachConfig(Options{
		WorkspaceRoot: worktree,
		SessionID:     store.Meta().SessionID,
	})
	if err != nil {
		t.Fatalf("loadRemoteAttachConfig: %v", err)
	}
	gotCanonical, err := config.CanonicalWorkspaceRoot(got.WorkspaceRoot)
	if err != nil {
		t.Fatalf("canonical got workspace: %v", err)
	}
	wantCanonical, err := config.CanonicalWorkspaceRoot(cfg.WorkspaceRoot)
	if err != nil {
		t.Fatalf("canonical want workspace: %v", err)
	}
	if gotCanonical != wantCanonical {
		t.Fatalf("workspace root = %q, want session workspace %q", got.WorkspaceRoot, cfg.WorkspaceRoot)
	}
}

func TestLoadRemoteAttachConfigRejectsStaleWorkspaceContextSession(t *testing.T) {
	newAppTestHome(t)
	workspace := t.TempDir()
	configureAppTestServerPort(t)
	_ = loadAppTestConfig(t, workspace, config.LoadOptions{})

	_, err := loadRemoteAttachConfig(Options{
		WorkspaceRoot:             workspace,
		WorkspaceContextSessionID: "stale-env-session",
	})
	if !errors.Is(err, sessioncontract.ErrSessionNotFound) {
		t.Fatalf("error = %v, want missing session rejection", err)
	}
}

func TestLoadRemoteAttachConfigKeepsExplicitSessionLookupStrict(t *testing.T) {
	newAppTestHome(t)
	workspace := t.TempDir()
	configureAppTestServerPort(t)

	_, err := loadRemoteAttachConfig(Options{
		WorkspaceRoot: workspace,
		SessionID:     "missing-explicit-session",
	})
	if err == nil {
		t.Fatal("expected stale explicit session id to fail")
	}
}

func TestRunPromptFromWorktreeUsesKentSessionWorkspaceContext(t *testing.T) {
	home := newAppTestHome(t)
	workspace := t.TempDir()
	worktree := filepath.Join(home, config.ConfigDirName, "worktrees", "project", "feature")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}
	configureAppTestServerPort(t)
	cfg := loadAppTestConfig(t, workspace, config.LoadOptions{})
	parent := createAuthoritativeAppSession(t, cfg.PersistenceRoot, cfg.WorkspaceRoot)
	saveReadyAppAuthState(t, workspace)

	fakeResponses, hits := newFakeResponsesServer(t, []string{"worktree reply"})
	defer fakeResponses.Close()

	stopServer := startStandingRunPromptServer(t, workspace, fakeResponses.URL)
	defer stopServer()

	result, err := RunPrompt(context.Background(), Options{
		WorkspaceRoot:             worktree,
		WorkspaceContextSessionID: parent.Meta().SessionID,
		Model:                     "gpt-5",
		OpenAIBaseURL:             fakeResponses.URL,
		OpenAIBaseURLExplicit:     true,
	}, "hello from worktree", 0, nil)
	if err != nil {
		t.Fatalf("RunPrompt: %v", err)
	}
	if result.Result != "worktree reply" {
		t.Fatalf("result = %q, want worktree reply", result.Result)
	}
	if result.SessionID == parent.Meta().SessionID {
		t.Fatal("expected worktree run to create a child run instead of continuing parent session")
	}
	if hits.Load() != 1 {
		t.Fatalf("expected one llm call, got %d", hits.Load())
	}
}

func TestRunPromptRejectsStaleWorkspaceContextSession(t *testing.T) {
	_, workspace := newRegisteredAppWorkspace(t)
	saveReadyAppAuthState(t, workspace)

	fakeResponses, hits := newFakeResponsesServer(t, []string{"workspace reply"})
	defer fakeResponses.Close()

	_, err := RunPrompt(context.Background(), Options{
		WorkspaceRoot:             workspace,
		WorkspaceContextSessionID: "stale-env-session",
		Model:                     "gpt-5",
		OpenAIBaseURL:             fakeResponses.URL,
		OpenAIBaseURLExplicit:     true,
	}, "hello from stale context", 0, nil)
	if !errors.Is(err, sessioncontract.ErrSessionNotFound) {
		t.Fatalf("error = %v, want missing session rejection", err)
	}
	if hits.Load() != 0 {
		t.Fatalf("expected no llm calls, got %d", hits.Load())
	}
}

type headlessProjectViewStubService struct {
	listProjectsResp serverapi.ProjectListResponse
	listProjectsErr  error
	overviews        map[string]serverapi.ProjectGetOverviewResponse
	overviewErr      error
}

type configuredProjectViewRemoteStub struct {
	identity           protocol.ServerIdentity
	resolveProjectPath func(context.Context, serverapi.ProjectResolvePathRequest) (serverapi.ProjectResolvePathResponse, error)
	listProjects       func(context.Context, serverapi.ProjectListRequest) (serverapi.ProjectListResponse, error)
	getProjectOverview func(context.Context, serverapi.ProjectGetOverviewRequest) (serverapi.ProjectGetOverviewResponse, error)
	closed             atomic.Bool
}

func (s *configuredProjectViewRemoteStub) Close() error {
	if s != nil {
		s.closed.Store(true)
	}
	return nil
}

func (s *configuredProjectViewRemoteStub) Identity() protocol.ServerIdentity {
	if s == nil {
		return protocol.ServerIdentity{}
	}
	return s.identity
}

func (s *configuredProjectViewRemoteStub) RequireRoot(rootID string) error {
	if s == nil {
		return errors.New("remote client is required")
	}
	if rootID != "" && s.identity.PersistenceRootID != rootID {
		return errors.New("project view root mismatch")
	}
	return nil
}

func (s *configuredProjectViewRemoteStub) ListProjects(ctx context.Context, req serverapi.ProjectListRequest) (serverapi.ProjectListResponse, error) {
	if s != nil && s.listProjects != nil {
		return s.listProjects(ctx, req)
	}
	return serverapi.ProjectListResponse{}, errors.New("unexpected ListProjects call")
}

func (*configuredProjectViewRemoteStub) ListProjectHome(context.Context, serverapi.ProjectHomeListRequest) (serverapi.ProjectHomeListResponse, error) {
	return serverapi.ProjectHomeListResponse{}, errors.New("unexpected ListProjectHome call")
}

func (s *configuredProjectViewRemoteStub) ResolveProjectPath(ctx context.Context, req serverapi.ProjectResolvePathRequest) (serverapi.ProjectResolvePathResponse, error) {
	if s != nil && s.resolveProjectPath != nil {
		return s.resolveProjectPath(ctx, req)
	}
	return serverapi.ProjectResolvePathResponse{}, errors.New("unexpected ResolveProjectPath call")
}

func (s *configuredProjectViewRemoteStub) PlanWorkspaceBinding(ctx context.Context, req serverapi.ProjectBindingPlanRequest) (serverapi.ProjectBindingPlanResponse, error) {
	return testPlanHeadlessWorkspaceBinding(ctx, s, req)
}

func (*configuredProjectViewRemoteStub) CreateProject(context.Context, serverapi.ProjectCreateRequest) (serverapi.ProjectCreateResponse, error) {
	return serverapi.ProjectCreateResponse{}, errors.New("unexpected CreateProject call")
}

func (*configuredProjectViewRemoteStub) ListProjectWorkspaces(context.Context, serverapi.ProjectWorkspaceListRequest) (serverapi.ProjectWorkspaceListResponse, error) {
	return serverapi.ProjectWorkspaceListResponse{}, errors.New("unexpected ListProjectWorkspaces call")
}

func (*configuredProjectViewRemoteStub) AttachWorkspaceToProject(context.Context, serverapi.ProjectAttachWorkspaceRequest) (serverapi.ProjectAttachWorkspaceResponse, error) {
	return serverapi.ProjectAttachWorkspaceResponse{}, errors.New("unexpected AttachWorkspaceToProject call")
}

func (*configuredProjectViewRemoteStub) RebindWorkspace(context.Context, serverapi.ProjectRebindWorkspaceRequest) (serverapi.ProjectRebindWorkspaceResponse, error) {
	return serverapi.ProjectRebindWorkspaceResponse{}, errors.New("unexpected RebindWorkspace call")
}

func (s *configuredProjectViewRemoteStub) GetProjectOverview(ctx context.Context, req serverapi.ProjectGetOverviewRequest) (serverapi.ProjectGetOverviewResponse, error) {
	if s != nil && s.getProjectOverview != nil {
		return s.getProjectOverview(ctx, req)
	}
	return serverapi.ProjectGetOverviewResponse{}, errors.New("unexpected GetProjectOverview call")
}

func (*configuredProjectViewRemoteStub) ListSessionsByProject(context.Context, serverapi.SessionListByProjectRequest) (serverapi.SessionListByProjectResponse, error) {
	return serverapi.SessionListByProjectResponse{}, nil
}

func (s headlessProjectViewStubService) ListProjects(context.Context, serverapi.ProjectListRequest) (serverapi.ProjectListResponse, error) {
	return s.listProjectsResp, s.listProjectsErr
}

func (headlessProjectViewStubService) ListProjectHome(context.Context, serverapi.ProjectHomeListRequest) (serverapi.ProjectHomeListResponse, error) {
	return serverapi.ProjectHomeListResponse{}, errors.New("unexpected ListProjectHome call")
}

func (headlessProjectViewStubService) ResolveProjectPath(context.Context, serverapi.ProjectResolvePathRequest) (serverapi.ProjectResolvePathResponse, error) {
	return serverapi.ProjectResolvePathResponse{}, errors.New("unexpected ResolveProjectPath call")
}

func (s headlessProjectViewStubService) PlanWorkspaceBinding(ctx context.Context, req serverapi.ProjectBindingPlanRequest) (serverapi.ProjectBindingPlanResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.ProjectBindingPlanResponse{}, err
	}
	selection, found, err := testSelectSingleRemoteWorkspace(ctx, s)
	if err != nil {
		return serverapi.ProjectBindingPlanResponse{}, err
	}
	if !found {
		return serverapi.ProjectBindingPlanResponse{Kind: serverapi.ProjectBindingPlanKindHeadlessRemoteAmbiguous}, nil
	}
	return serverapi.ProjectBindingPlanResponse{
		Kind:      serverapi.ProjectBindingPlanKindHeadlessRemoteSelected,
		Workspace: &selection,
	}, nil
}

func (headlessProjectViewStubService) CreateProject(context.Context, serverapi.ProjectCreateRequest) (serverapi.ProjectCreateResponse, error) {
	return serverapi.ProjectCreateResponse{}, errors.New("unexpected CreateProject call")
}

func (headlessProjectViewStubService) ListProjectWorkspaces(context.Context, serverapi.ProjectWorkspaceListRequest) (serverapi.ProjectWorkspaceListResponse, error) {
	return serverapi.ProjectWorkspaceListResponse{}, errors.New("unexpected ListProjectWorkspaces call")
}

func (headlessProjectViewStubService) AttachWorkspaceToProject(context.Context, serverapi.ProjectAttachWorkspaceRequest) (serverapi.ProjectAttachWorkspaceResponse, error) {
	return serverapi.ProjectAttachWorkspaceResponse{}, errors.New("unexpected AttachWorkspaceToProject call")
}

func (headlessProjectViewStubService) RebindWorkspace(context.Context, serverapi.ProjectRebindWorkspaceRequest) (serverapi.ProjectRebindWorkspaceResponse, error) {
	return serverapi.ProjectRebindWorkspaceResponse{}, errors.New("unexpected RebindWorkspace call")
}

func (s headlessProjectViewStubService) GetProjectOverview(_ context.Context, req serverapi.ProjectGetOverviewRequest) (serverapi.ProjectGetOverviewResponse, error) {
	if s.overviewErr != nil {
		return serverapi.ProjectGetOverviewResponse{}, s.overviewErr
	}
	resp, ok := s.overviews[req.ProjectID]
	if !ok {
		return serverapi.ProjectGetOverviewResponse{}, errors.New("missing overview")
	}
	return resp, nil
}

func (headlessProjectViewStubService) ListSessionsByProject(context.Context, serverapi.SessionListByProjectRequest) (serverapi.SessionListByProjectResponse, error) {
	return serverapi.SessionListByProjectResponse{}, nil
}

func testPlanHeadlessWorkspaceBinding(ctx context.Context, projectViews client.ProjectViewClient, req serverapi.ProjectBindingPlanRequest) (serverapi.ProjectBindingPlanResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.ProjectBindingPlanResponse{}, err
	}
	resolved, err := projectViews.ResolveProjectPath(ctx, serverapi.ProjectResolvePathRequest{Path: req.Path})
	if err != nil {
		return serverapi.ProjectBindingPlanResponse{}, err
	}
	resp := serverapi.ProjectBindingPlanResponse{
		CanonicalRoot:    resolved.CanonicalRoot,
		PathAvailability: resolved.PathAvailability,
		Binding:          resolved.Binding,
	}
	if resolved.Binding != nil {
		resp.Kind = serverapi.ProjectBindingPlanKindBound
		return resp, nil
	}
	if resolved.PathAvailability == clientui.ProjectAvailabilityAvailable {
		resp.Kind = serverapi.ProjectBindingPlanKindLocalUnbound
		return resp, nil
	}
	selection, found, err := testSelectSingleRemoteWorkspace(ctx, projectViews)
	if err != nil {
		return serverapi.ProjectBindingPlanResponse{}, err
	}
	if !found {
		resp.Kind = serverapi.ProjectBindingPlanKindHeadlessRemoteAmbiguous
		return resp, nil
	}
	resp.Kind = serverapi.ProjectBindingPlanKindHeadlessRemoteSelected
	resp.Workspace = &selection
	return resp, nil
}

func testSelectSingleRemoteWorkspace(ctx context.Context, projectViews client.ProjectViewClient) (serverapi.ProjectWorkspacePlanSelected, bool, error) {
	projects, err := projectViews.ListProjects(ctx, serverapi.ProjectListRequest{})
	if err != nil {
		return serverapi.ProjectWorkspacePlanSelected{}, false, err
	}
	selection := serverapi.ProjectWorkspacePlanSelected{}
	count := 0
	for _, project := range projects.Projects {
		overview, err := projectViews.GetProjectOverview(ctx, serverapi.ProjectGetOverviewRequest{ProjectID: project.ProjectID})
		if err != nil {
			return serverapi.ProjectWorkspacePlanSelected{}, false, err
		}
		for _, workspace := range overview.Overview.Workspaces {
			availability := strings.TrimSpace(string(workspace.Availability))
			if availability != "" && workspace.Availability != clientui.ProjectAvailabilityAvailable {
				continue
			}
			count++
			selection = serverapi.ProjectWorkspacePlanSelected{ProjectID: project.ProjectID, WorkspaceID: workspace.WorkspaceID}
			if count > 1 {
				return serverapi.ProjectWorkspacePlanSelected{}, false, nil
			}
		}
	}
	if count == 0 {
		return serverapi.ProjectWorkspacePlanSelected{}, false, nil
	}
	return selection, true, nil
}

func (h memoryAuthHandler) WrapStore(auth.Store) auth.Store {
	return auth.NewMemoryStore(h.state)
}

func (memoryAuthHandler) NeedsInteraction(req authservice.FlowInteractionRequest) bool {
	return !req.Gate.Ready
}

func (memoryAuthHandler) Interact(context.Context, authservice.FlowInteractionRequest) (authservice.FlowInteractionOutcome, error) {
	return authservice.FlowInteractionOutcome{}, auth.ErrAuthNotConfigured
}

func (h memoryAuthHandler) LookupEnv(key string) string {
	if h.lookupEnv != nil {
		return h.lookupEnv(key)
	}
	return ""
}

var autoOnboarding = serverstartup.OnboardingHandler(func(_ context.Context, req serverstartup.OnboardingRequest) (config.App, error) {
	path, created, err := config.WriteDefaultSettingsFile()
	if err != nil {
		return config.App{}, err
	}
	reloaded, err := req.ReloadConfig()
	if err != nil {
		return config.App{}, err
	}
	reloaded.Source.CreatedDefaultConfig = created
	reloaded.Source.SettingsPath = path
	reloaded.Source.SettingsFileExists = true
	return reloaded, nil
})

func waitForConfiguredRunPromptDaemon(t *testing.T, workspace string) {
	t.Helper()
	loadCfg := loadAppTestConfig(t, workspace, config.LoadOptions{})
	healthURL := config.ServerHTTPBaseURL(loadCfg) + protocol.HealthPath
	deadline := time.Now().Add(5 * time.Second)
	client := &http.Client{Timeout: 250 * time.Millisecond}
	for {
		resp, err := client.Get(healthURL)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("configured daemon did not become healthy at %s", healthURL)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestEnsureSubagentSessionNameSetsDefault(t *testing.T) {
	containerDir := t.TempDir()
	store, err := session.NewLazy(containerDir, "workspace-x", "/tmp/workspace")
	if err != nil {
		t.Fatalf("new lazy session: %v", err)
	}

	if err := launch.EnsureSubagentSessionName(store); err != nil {
		t.Fatalf("ensure subagent session name: %v", err)
	}

	meta := store.Meta()
	want := meta.SessionID + " " + subagentSessionSuffix
	if meta.Name != want {
		t.Fatalf("session name = %q, want %q", meta.Name, want)
	}
}

func TestEnsureSubagentSessionNamePreservesExistingName(t *testing.T) {
	containerDir := t.TempDir()
	store, err := session.NewLazy(containerDir, "workspace-x", "/tmp/workspace")
	if err != nil {
		t.Fatalf("new lazy session: %v", err)
	}
	if err := store.SetName("incident triage"); err != nil {
		t.Fatalf("set name: %v", err)
	}

	if err := launch.EnsureSubagentSessionName(store); err != nil {
		t.Fatalf("ensure subagent session name: %v", err)
	}

	if got := store.Meta().Name; got != "incident triage" {
		t.Fatalf("session name = %q, want incident triage", got)
	}
}

func TestRunPromptAskHandlerReturnsError(t *testing.T) {
	_, err := runprompt.RunPromptAskHandler(askquestion.AskQuestionRequest{Question: "Need approval?"})
	if !errors.Is(err, runprompt.ErrHeadlessAskUnsupported) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunPromptUsesConfiguredDaemonWithoutLocalAuth(t *testing.T) {
	_, workspace := newRegisteredAppWorkspace(t)
	saveReadyAppAuthState(t, workspace)

	fakeResponses, hits := newFakeResponsesServer(t, []string{"daemon reply"})
	defer fakeResponses.Close()

	srv, err := serverstartup.StartServeServer(context.Background(), serverstartup.Request{
		WorkspaceRoot:         workspace,
		WorkspaceRootExplicit: true,
		Model:                 "gpt-5",
		OpenAIBaseURL:         fakeResponses.URL,
		OpenAIBaseURLExplicit: true,
	}, apiKeyMemoryAuthHandler("test-key"), autoOnboarding)
	if err != nil {
		t.Fatalf("serve.Start: %v", err)
	}
	defer func() { _ = srv.Close() }()

	stopServing := serveAppServer(t, srv)
	defer stopServing()

	waitForConfiguredRunPromptDaemon(t, workspace)

	result, err := RunPrompt(context.Background(), Options{WorkspaceRoot: workspace, WorkspaceRootExplicit: true}, "hello through daemon", 0, nil)
	if err != nil {
		t.Fatalf("RunPrompt: %v", err)
	}
	if result.Result != "daemon reply" {
		t.Fatalf("result = %q, want %q", result.Result, "daemon reply")
	}
	if hits.Load() != 1 {
		t.Fatalf("expected daemon-backed llm call once, got %d", hits.Load())
	}

}

func TestRunPromptWithIncompatibleServerReportsIncompatibleError(t *testing.T) {
	_, workspace := newRegisteredAppWorkspace(t)
	saveReadyAppAuthState(t, workspace)

	// A reachable but capability-incompatible server (e.g. an older build without
	// RunPrompt) must surface the distinct "incompatible server" error directing
	// the operator to restart/upgrade it, not the generic "no server" error that
	// would invite starting a conflicting second server.
	cleanup := publishConfiguredRemoteForWorkspace(t, workspace, protocol.CapabilityFlags{
		JSONRPCWebSocket: true,
		ProjectAttach:    true,
		SessionAttach:    true,
		SessionPlan:      true,
		SessionLifecycle: true,
		SessionRuntime:   true,
		RuntimeControl:   true,
		PromptControl:    true,
		PromptActivity:   true,
		SessionActivity:  true,
	})
	defer cleanup()

	_, err := RunPrompt(context.Background(), Options{
		WorkspaceRoot:         workspace,
		WorkspaceRootExplicit: true,
		Model:                 "gpt-5",
	}, "hello", 0, nil)
	if !errors.Is(err, errRunServerIncompatible) {
		t.Fatalf("RunPrompt error = %v, want errRunServerIncompatible", err)
	}
}

func TestOwnedDaemonCloseFallsBackToKillWhenInterruptFails(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("sleep helper is unix-only")
	}
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	errCh := make(chan error, 1)
	go func() {
		errCh <- cmd.Wait()
	}()
	killed := false
	closeFn := daemonlaunch.NewOwnedProcessClose[*client.Remote](nil, nil, cmd, errCh, daemonlaunch.Controls{
		Terminate: func(process *os.Process) error {
			return errors.New("interrupt unsupported")
		},
		Kill: func(process *os.Process) error {
			killed = true
			if process == nil {
				return nil
			}
			return process.Kill()
		},
	})
	if err := closeFn(); err != nil {
		t.Fatalf("closeFn: %v", err)
	}
	if !killed {
		t.Fatal("expected owned daemon close to fall back to kill")
	}
}

func TestRunPromptUsesInvocationOverridesWhenAttachingToConfiguredDaemon(t *testing.T) {
	_, workspace := newRegisteredAppWorkspace(t)

	defaultResponses, defaultHits := newFakeResponsesServer(t, []string{"daemon default"})
	defer defaultResponses.Close()
	overrideResponses, overrideHits := newFakeResponsesServer(t, []string{"override reply"})
	defer overrideResponses.Close()

	srv, err := serverstartup.StartServeServer(context.Background(), serverstartup.Request{
		WorkspaceRoot:         workspace,
		WorkspaceRootExplicit: true,
		Model:                 "gpt-5",
		OpenAIBaseURL:         defaultResponses.URL,
		OpenAIBaseURLExplicit: true,
	}, apiKeyMemoryAuthHandler("test-key"), autoOnboarding)
	if err != nil {
		t.Fatalf("serve.Start: %v", err)
	}
	defer func() { _ = srv.Close() }()

	stopServing := serveAppServer(t, srv)
	defer stopServing()

	waitForConfiguredRunPromptDaemon(t, workspace)

	result, err := RunPrompt(context.Background(), Options{
		WorkspaceRoot:         workspace,
		WorkspaceRootExplicit: true,
		Model:                 "gpt-5",
		OpenAIBaseURL:         overrideResponses.URL,
		OpenAIBaseURLExplicit: true,
	}, "hello through override", 0, nil)
	if err != nil {
		t.Fatalf("RunPrompt: %v", err)
	}
	if result.Result != "override reply" {
		t.Fatalf("result = %q, want %q", result.Result, "override reply")
	}
	if overrideHits.Load() != 1 {
		t.Fatalf("expected override llm call once, got %d", overrideHits.Load())
	}
	if defaultHits.Load() != 0 {
		t.Fatalf("expected daemon default llm endpoint unused, got %d", defaultHits.Load())
	}

}

func TestTryDialMatchingConfiguredRemoteRejectsServerThatDoesNotMatchSpawnedPID(t *testing.T) {
	_, workspace := newRegisteredAppWorkspace(t)
	cleanup := publishConfiguredRemoteForWorkspace(t, workspace, protocol.CapabilityFlags{RunPrompt: true, AuthBootstrap: true, ProjectAttach: true})
	defer cleanup()
	if remote, ok := tryDialMatchingConfiguredRemoteWithRequirement(context.Background(), Options{WorkspaceRoot: workspace, WorkspaceRootExplicit: true}, remoteattach.SupportsRunPrompt, func(identity protocol.ServerIdentity) bool {
		return identity.PID == 111
	}, true); ok || remote != nil {
		t.Fatalf("expected mismatched pid server to be rejected, got remote=%v ok=%t", remote, ok)
	}
}

func TestTryDialMatchingConfiguredRemoteSkipsUnregisteredWorkspace(t *testing.T) {
	newAppTestHome(t)
	workspace := t.TempDir()
	configureAppTestServerPort(t)
	cleanup := publishConfiguredRemoteForWorkspace(t, workspace, protocol.CapabilityFlags{RunPrompt: true, AuthBootstrap: true, ProjectAttach: true})
	defer cleanup()
	if remote, ok := tryDialMatchingConfiguredRemoteWithRequirement(context.Background(), Options{WorkspaceRoot: workspace, WorkspaceRootExplicit: true}, remoteattach.SupportsRunPrompt, nil, true); ok || remote != nil {
		t.Fatalf("expected unregistered workspace to skip configured remote attach, got remote=%v ok=%t", remote, ok)
	}
}

func TestStartRunPromptClientWithoutServerRequiresRunningServer(t *testing.T) {
	newAppTestHome(t)
	workspace := t.TempDir()
	configureAppTestServerPort(t)
	saveReadyAppAuthState(t, workspace)

	// kent run is a pure client: with no server running it cannot start one of
	// its own, so it must fail with the "server required" error.
	runClient, closeFn, err := startRunPromptClient(context.Background(), Options{WorkspaceRoot: workspace, WorkspaceRootExplicit: true})
	if !errors.Is(err, errRunRequiresServer) {
		t.Fatalf("startRunPromptClient error = %v, want errRunRequiresServer", err)
	}
	if runClient != nil {
		t.Fatalf("expected no run client, got %v", runClient)
	}
	if closeFn != nil {
		t.Fatal("expected no close function when startup fails")
	}
}

func TestHeadlessProjectBindingPlanChoosesOnlyWorkspace(t *testing.T) {
	client := client.NewLoopbackProjectViewClient(headlessProjectViewStubService{
		listProjectsResp: serverapi.ProjectListResponse{Projects: []clientui.ProjectSummary{{ProjectID: "project-1"}}},
		overviews: map[string]serverapi.ProjectGetOverviewResponse{
			"project-1": {Overview: clientui.ProjectOverview{Workspaces: []clientui.ProjectWorkspaceSummary{{WorkspaceID: "workspace-1"}}}},
		},
	})

	plan, err := client.PlanWorkspaceBinding(context.Background(), serverapi.ProjectBindingPlanRequest{Path: "/client/missing", Mode: serverapi.ProjectBindingPlanModeHeadless})
	if err != nil {
		t.Fatalf("PlanWorkspaceBinding: %v", err)
	}
	if plan.Kind != serverapi.ProjectBindingPlanKindHeadlessRemoteSelected || plan.Workspace == nil {
		t.Fatalf("expected single workspace selection, got %+v", plan)
	}
	if plan.Workspace.ProjectID != "project-1" || plan.Workspace.WorkspaceID != "workspace-1" {
		t.Fatalf("unexpected selection: %+v", plan.Workspace)
	}
}

func TestHeadlessProjectBindingPlanIgnoresUnavailableWorkspaces(t *testing.T) {
	client := client.NewLoopbackProjectViewClient(headlessProjectViewStubService{
		listProjectsResp: serverapi.ProjectListResponse{Projects: []clientui.ProjectSummary{{ProjectID: "project-1"}}},
		overviews: map[string]serverapi.ProjectGetOverviewResponse{
			"project-1": {Overview: clientui.ProjectOverview{Workspaces: []clientui.ProjectWorkspaceSummary{
				{WorkspaceID: "workspace-missing", Availability: clientui.ProjectAvailabilityMissing},
				{WorkspaceID: "workspace-1", Availability: clientui.ProjectAvailabilityAvailable},
				{WorkspaceID: "workspace-inaccessible", Availability: clientui.ProjectAvailabilityInaccessible},
			}}},
		},
	})

	plan, err := client.PlanWorkspaceBinding(context.Background(), serverapi.ProjectBindingPlanRequest{Path: "/client/missing", Mode: serverapi.ProjectBindingPlanModeHeadless})
	if err != nil {
		t.Fatalf("PlanWorkspaceBinding: %v", err)
	}
	if plan.Kind != serverapi.ProjectBindingPlanKindHeadlessRemoteSelected || plan.Workspace == nil {
		t.Fatalf("expected single available workspace selection, got %+v", plan)
	}
	if plan.Workspace.ProjectID != "project-1" || plan.Workspace.WorkspaceID != "workspace-1" {
		t.Fatalf("unexpected selection: %+v", plan.Workspace)
	}
}

func TestTryDialConfiguredRunPromptRemoteUsesFreshDialTimeoutAfterWorkspaceDiscovery(t *testing.T) {
	newAppTestHome(t)
	workspace := t.TempDir()

	originalProjectViewsDial := dialConfiguredProjectViewRemote
	originalRemoteDial := dialConfiguredRemote
	originalAttachTimeout := configuredRemoteAttachTimeout
	originalDiscoveryTimeout := configuredRemoteWorkspaceDiscoveryTimeout
	t.Cleanup(func() {
		dialConfiguredProjectViewRemote = originalProjectViewsDial
		dialConfiguredRemote = originalRemoteDial
		configuredRemoteAttachTimeout = originalAttachTimeout
		configuredRemoteWorkspaceDiscoveryTimeout = originalDiscoveryTimeout
	})

	configuredRemoteAttachTimeout = 20 * time.Millisecond
	configuredRemoteWorkspaceDiscoveryTimeout = 120 * time.Millisecond
	projectViews := &configuredProjectViewRemoteStub{
		identity: protocol.ServerIdentity{Capabilities: protocol.CapabilityFlags{RunPrompt: true, AuthBootstrap: true, ProjectAttach: true}},
		resolveProjectPath: func(context.Context, serverapi.ProjectResolvePathRequest) (serverapi.ProjectResolvePathResponse, error) {
			return serverapi.ProjectResolvePathResponse{PathAvailability: clientui.ProjectAvailabilityMissing}, nil
		},
		listProjects: func(context.Context, serverapi.ProjectListRequest) (serverapi.ProjectListResponse, error) {
			return serverapi.ProjectListResponse{Projects: []clientui.ProjectSummary{{ProjectID: "project-1"}}}, nil
		},
		getProjectOverview: func(ctx context.Context, req serverapi.ProjectGetOverviewRequest) (serverapi.ProjectGetOverviewResponse, error) {
			time.Sleep(configuredRemoteAttachTimeout + 10*time.Millisecond)
			if err := ctx.Err(); err != nil {
				return serverapi.ProjectGetOverviewResponse{}, err
			}
			return serverapi.ProjectGetOverviewResponse{Overview: clientui.ProjectOverview{Workspaces: []clientui.ProjectWorkspaceSummary{{WorkspaceID: "workspace-1"}}}}, nil
		},
	}
	dialConfiguredProjectViewRemote = func(context.Context, config.App) (configuredProjectViewRemote, error) {
		return projectViews, nil
	}
	var dialRemaining time.Duration
	dialConfiguredRemote = func(ctx context.Context, cfg config.App, projectID string, workspaceID string) (*client.Remote, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		deadline, ok := ctx.Deadline()
		if !ok {
			t.Fatal("expected dial context deadline")
		}
		dialRemaining = time.Until(deadline)
		if cfg.WorkspaceRoot != workspace {
			t.Fatalf("unexpected config workspace root: %s", cfg.WorkspaceRoot)
		}
		if projectID != "project-1" || workspaceID != "workspace-1" {
			t.Fatalf("unexpected workspace dial target: %s/%s", projectID, workspaceID)
		}
		return new(client.Remote), nil
	}

	remote, ok, err := tryDialMatchingConfiguredRunPromptRemote(context.Background(), Options{WorkspaceRoot: workspace, WorkspaceRootExplicit: true}, nil)
	if err != nil {
		t.Fatalf("tryDialMatchingConfiguredRunPromptRemote: %v", err)
	}
	if !ok {
		t.Fatal("expected configured remote attach to succeed")
	}
	if remote == nil {
		t.Fatal("expected remote client")
	}
	if !projectViews.closed.Load() {
		t.Fatal("expected project view remote to close after workspace selection")
	}
	if dialRemaining <= configuredRemoteAttachTimeout/2 {
		t.Fatalf("expected fresh attach timeout after workspace discovery, remaining=%v attach=%v", dialRemaining, configuredRemoteAttachTimeout)
	}
}

func TestTryDialMatchingConfiguredRunPromptRemoteUsesWorkspaceDiscoveryForAcceptedDaemon(t *testing.T) {
	newAppTestHome(t)
	workspace := t.TempDir()

	originalProjectViewsDial := dialConfiguredProjectViewRemote
	originalRemoteDial := dialConfiguredRemote
	t.Cleanup(func() {
		dialConfiguredProjectViewRemote = originalProjectViewsDial
		dialConfiguredRemote = originalRemoteDial
	})

	projectViews := &configuredProjectViewRemoteStub{
		identity: protocol.ServerIdentity{PID: 777, Capabilities: protocol.CapabilityFlags{RunPrompt: true, AuthBootstrap: true, ProjectAttach: true}},
		resolveProjectPath: func(context.Context, serverapi.ProjectResolvePathRequest) (serverapi.ProjectResolvePathResponse, error) {
			return serverapi.ProjectResolvePathResponse{PathAvailability: clientui.ProjectAvailabilityMissing}, nil
		},
		listProjects: func(context.Context, serverapi.ProjectListRequest) (serverapi.ProjectListResponse, error) {
			return serverapi.ProjectListResponse{Projects: []clientui.ProjectSummary{{ProjectID: "project-1"}}}, nil
		},
		getProjectOverview: func(context.Context, serverapi.ProjectGetOverviewRequest) (serverapi.ProjectGetOverviewResponse, error) {
			return serverapi.ProjectGetOverviewResponse{Overview: clientui.ProjectOverview{Workspaces: []clientui.ProjectWorkspaceSummary{{WorkspaceID: "workspace-1", Availability: clientui.ProjectAvailabilityAvailable}}}}, nil
		},
	}
	dialConfiguredProjectViewRemote = func(context.Context, config.App) (configuredProjectViewRemote, error) {
		return projectViews, nil
	}
	dialConfiguredRemote = func(ctx context.Context, cfg config.App, projectID string, workspaceID string) (*client.Remote, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if cfg.WorkspaceRoot != workspace {
			t.Fatalf("unexpected config workspace root: %s", cfg.WorkspaceRoot)
		}
		if projectID != "project-1" || workspaceID != "workspace-1" {
			t.Fatalf("unexpected workspace dial target: %s/%s", projectID, workspaceID)
		}
		return new(client.Remote), nil
	}

	remote, ok, err := tryDialMatchingConfiguredRunPromptRemote(context.Background(), Options{WorkspaceRoot: workspace, WorkspaceRootExplicit: true}, func(identity protocol.ServerIdentity) bool {
		return identity.PID == 777
	})
	if err != nil {
		t.Fatalf("tryDialMatchingConfiguredRunPromptRemote: %v", err)
	}
	if !ok {
		t.Fatal("expected launched daemon attach to succeed via workspace discovery")
	}
	if remote == nil {
		t.Fatal("expected remote client")
	}
	if !projectViews.closed.Load() {
		t.Fatal("expected project view remote to close after workspace discovery")
	}
}

func TestTryDialConfiguredRunPromptRemoteSkipsServerWithoutAuthBootstrapCapability(t *testing.T) {
	newAppTestHome(t)
	workspace := t.TempDir()

	originalProjectViewsDial := dialConfiguredProjectViewRemote
	t.Cleanup(func() { dialConfiguredProjectViewRemote = originalProjectViewsDial })

	projectViews := &configuredProjectViewRemoteStub{
		identity: protocol.ServerIdentity{Capabilities: protocol.CapabilityFlags{RunPrompt: true}},
	}
	dialConfiguredProjectViewRemote = func(context.Context, config.App) (configuredProjectViewRemote, error) {
		return projectViews, nil
	}

	remote, ok, err := tryDialMatchingConfiguredRunPromptRemote(context.Background(), Options{WorkspaceRoot: workspace, WorkspaceRootExplicit: true}, nil)
	if err != nil {
		t.Fatalf("tryDialMatchingConfiguredRunPromptRemote: %v", err)
	}
	if ok || remote != nil {
		t.Fatalf("expected configured remote without auth bootstrap to be skipped, got remote=%v ok=%t", remote, ok)
	}
	if !projectViews.closed.Load() {
		t.Fatal("expected incompatible project view remote to be closed")
	}
}

func TestTryDialConfiguredRunPromptRemoteSkipsServerWithoutProjectAttachCapability(t *testing.T) {
	newAppTestHome(t)
	workspace := t.TempDir()

	originalProjectViewsDial := dialConfiguredProjectViewRemote
	t.Cleanup(func() { dialConfiguredProjectViewRemote = originalProjectViewsDial })

	projectViews := &configuredProjectViewRemoteStub{
		identity: protocol.ServerIdentity{Capabilities: protocol.CapabilityFlags{RunPrompt: true, AuthBootstrap: true}},
	}
	dialConfiguredProjectViewRemote = func(context.Context, config.App) (configuredProjectViewRemote, error) {
		return projectViews, nil
	}

	remote, ok, err := tryDialMatchingConfiguredRunPromptRemote(context.Background(), Options{WorkspaceRoot: workspace, WorkspaceRootExplicit: true}, nil)
	if err != nil {
		t.Fatalf("tryDialMatchingConfiguredRunPromptRemote: %v", err)
	}
	if ok || remote != nil {
		t.Fatalf("expected configured remote without project attach to be skipped, got remote=%v ok=%t", remote, ok)
	}
	if !projectViews.closed.Load() {
		t.Fatal("expected incompatible project view remote to be closed")
	}
}

func TestServerAttachRemotePolicySetsRootIDOnlyForExplicitRoot(t *testing.T) {
	explicit := config.App{
		PersistenceRoot: filepath.Join(string(filepath.Separator), "tmp", "iso-root"),
		Source:          config.SourceReport{Sources: map[string]string{"persistence_root": "flag"}},
	}
	policy := serverAttachRemotePolicy(explicit, remoteattach.SupportsRunPrompt, true)
	if want := config.PersistenceRootHash(explicit.PersistenceRoot); policy.RootID != want {
		t.Fatalf("explicit-root policy RootID = %q, want %q", policy.RootID, want)
	}

	defaultRoot := config.App{
		PersistenceRoot: filepath.Join(string(filepath.Separator), "home", "u", ".kent"),
		Source:          config.SourceReport{Sources: map[string]string{"persistence_root": "default"}},
	}
	if policy := serverAttachRemotePolicy(defaultRoot, remoteattach.SupportsRunPrompt, true); policy.RootID != "" {
		t.Fatalf("default-root policy RootID = %q, want empty (no root validation)", policy.RootID)
	}

	// An explicit flag/env root that resolves to the default <home>/.kent must
	// not require a root id, so default-root attaches stay compatible with older
	// servers that report an empty id.
	resolvedDefault, err := config.NormalizePersistenceRoot(config.DefaultPersistence)
	if err != nil {
		t.Fatalf("normalize default root: %v", err)
	}
	explicitDefault := config.App{
		PersistenceRoot: resolvedDefault,
		Source:          config.SourceReport{Sources: map[string]string{"persistence_root": "flag"}},
	}
	if policy := serverAttachRemotePolicy(explicitDefault, remoteattach.SupportsRunPrompt, true); policy.RootID != "" {
		t.Fatalf("explicit-default-root policy RootID = %q, want empty (no root validation)", policy.RootID)
	}
}
