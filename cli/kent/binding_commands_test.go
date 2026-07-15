package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"core/server/auth"
	"core/server/authservice"
	"core/server/metadata"
	"core/server/session"
	serverstartup "core/server/startup"
	"core/shared/apicontract"
	"core/shared/client"
	"core/shared/config"
	"core/shared/protocol"
	"core/shared/serverapi"
)

type bindingCommandTimeoutProjectViewStub struct {
	resolveProjectPath func(context.Context, serverapi.ProjectResolvePathRequest) (serverapi.ProjectResolvePathResponse, error)
	listProjects       func(context.Context, serverapi.ProjectListRequest) (serverapi.ProjectListResponse, error)
	createProject      func(context.Context, serverapi.ProjectCreateRequest) (serverapi.ProjectCreateResponse, error)
	attachWorkspace    func(context.Context, serverapi.ProjectAttachWorkspaceRequest) (serverapi.ProjectAttachWorkspaceResponse, error)
	rebindWorkspace    func(context.Context, serverapi.ProjectRebindWorkspaceRequest) (serverapi.ProjectRebindWorkspaceResponse, error)
}

type bindingCommandTimeoutSessionLifecycleStub struct {
	retargetSessionWorkspace func(context.Context, serverapi.SessionRetargetWorkspaceRequest) (serverapi.SessionRetargetWorkspaceResponse, error)
}

func (bindingCommandTimeoutSessionLifecycleStub) Close() error {
	return nil
}

func (s bindingCommandTimeoutProjectViewStub) ListProjects(ctx context.Context, req serverapi.ProjectListRequest) (serverapi.ProjectListResponse, error) {
	if s.listProjects == nil {
		return serverapi.ProjectListResponse{}, errors.New("unexpected ListProjects call")
	}
	return s.listProjects(ctx, req)
}

func (bindingCommandTimeoutProjectViewStub) ListProjectHome(context.Context, serverapi.ProjectHomeListRequest) (serverapi.ProjectHomeListResponse, error) {
	return serverapi.ProjectHomeListResponse{}, errors.New("unexpected ListProjectHome call")
}

func (s bindingCommandTimeoutProjectViewStub) ResolveProjectPath(ctx context.Context, req serverapi.ProjectResolvePathRequest) (serverapi.ProjectResolvePathResponse, error) {
	if s.resolveProjectPath == nil {
		return serverapi.ProjectResolvePathResponse{}, errors.New("unexpected ResolveProjectPath call")
	}
	return s.resolveProjectPath(ctx, req)
}

func (bindingCommandTimeoutProjectViewStub) PlanWorkspaceBinding(context.Context, serverapi.ProjectBindingPlanRequest) (serverapi.ProjectBindingPlanResponse, error) {
	return serverapi.ProjectBindingPlanResponse{}, errors.New("unexpected PlanWorkspaceBinding call")
}

func (s bindingCommandTimeoutProjectViewStub) CreateProject(ctx context.Context, req serverapi.ProjectCreateRequest) (serverapi.ProjectCreateResponse, error) {
	if s.createProject == nil {
		return serverapi.ProjectCreateResponse{}, errors.New("unexpected CreateProject call")
	}
	return s.createProject(ctx, req)
}

func (bindingCommandTimeoutProjectViewStub) ListProjectWorkspaces(context.Context, serverapi.ProjectWorkspaceListRequest) (serverapi.ProjectWorkspaceListResponse, error) {
	return serverapi.ProjectWorkspaceListResponse{}, errors.New("unexpected ListProjectWorkspaces call")
}

func (s bindingCommandTimeoutProjectViewStub) AttachWorkspaceToProject(ctx context.Context, req serverapi.ProjectAttachWorkspaceRequest) (serverapi.ProjectAttachWorkspaceResponse, error) {
	if s.attachWorkspace == nil {
		return serverapi.ProjectAttachWorkspaceResponse{}, errors.New("unexpected AttachWorkspaceToProject call")
	}
	return s.attachWorkspace(ctx, req)
}

func (s bindingCommandTimeoutProjectViewStub) RebindWorkspace(ctx context.Context, req serverapi.ProjectRebindWorkspaceRequest) (serverapi.ProjectRebindWorkspaceResponse, error) {
	if s.rebindWorkspace == nil {
		return serverapi.ProjectRebindWorkspaceResponse{}, errors.New("unexpected RebindWorkspace call")
	}
	return s.rebindWorkspace(ctx, req)
}

func (bindingCommandTimeoutProjectViewStub) ListSessionsByProject(context.Context, serverapi.SessionListByProjectRequest) (serverapi.SessionListByProjectResponse, error) {
	return serverapi.SessionListByProjectResponse{}, nil
}

func (bindingCommandTimeoutProjectViewStub) GetProjectOverview(context.Context, serverapi.ProjectGetOverviewRequest) (serverapi.ProjectGetOverviewResponse, error) {
	return serverapi.ProjectGetOverviewResponse{}, errors.New("unexpected GetProjectOverview call")
}

func (bindingCommandTimeoutSessionLifecycleStub) GetInitialInput(context.Context, serverapi.SessionInitialInputRequest) (serverapi.SessionInitialInputResponse, error) {
	return serverapi.SessionInitialInputResponse{}, errors.New("unexpected GetInitialInput call")
}

func (bindingCommandTimeoutSessionLifecycleStub) PersistInputDraft(context.Context, serverapi.SessionPersistInputDraftRequest) (serverapi.SessionPersistInputDraftResponse, error) {
	return serverapi.SessionPersistInputDraftResponse{}, errors.New("unexpected PersistInputDraft call")
}

func (s bindingCommandTimeoutSessionLifecycleStub) RetargetSessionWorkspace(ctx context.Context, req serverapi.SessionRetargetWorkspaceRequest) (serverapi.SessionRetargetWorkspaceResponse, error) {
	if s.retargetSessionWorkspace == nil {
		return serverapi.SessionRetargetWorkspaceResponse{}, errors.New("unexpected RetargetSessionWorkspace call")
	}
	return s.retargetSessionWorkspace(ctx, req)
}

func (bindingCommandTimeoutSessionLifecycleStub) ResolveTransition(context.Context, serverapi.SessionResolveTransitionRequest) (serverapi.SessionResolveTransitionResponse, error) {
	return serverapi.SessionResolveTransitionResponse{}, errors.New("unexpected ResolveTransition call")
}

type bindingCommandMemoryAuthHandler struct {
	state auth.State
}

func (h bindingCommandMemoryAuthHandler) WrapStore(auth.Store) auth.Store {
	return auth.NewMemoryStore(h.state)
}

func (bindingCommandMemoryAuthHandler) NeedsInteraction(req authservice.FlowInteractionRequest) bool {
	return !req.Gate.Ready
}

func (bindingCommandMemoryAuthHandler) Interact(context.Context, authservice.FlowInteractionRequest) (authservice.FlowInteractionOutcome, error) {
	return authservice.FlowInteractionOutcome{}, auth.ErrAuthNotConfigured
}

func (bindingCommandMemoryAuthHandler) LookupEnv(string) string {
	return ""
}

var bindingCommandAutoOnboarding = serverstartup.OnboardingHandler(func(_ context.Context, req serverstartup.OnboardingRequest) (config.App, error) {
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

func configureBindingCommandTestServerPort(t *testing.T) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	reserveBindingCommandTestPort(t, listener)
	t.Setenv("KENT_SERVER_PORT", fmt.Sprintf("%d", port))
}

var bindingCommandTestPortReservations = struct {
	sync.Mutex
	listeners map[string]net.Listener
}{listeners: map[string]net.Listener{}}

func reserveBindingCommandTestPort(t *testing.T, listener net.Listener) {
	t.Helper()
	addr := listener.Addr().String()
	bindingCommandTestPortReservations.Lock()
	if existing := bindingCommandTestPortReservations.listeners[addr]; existing != nil {
		_ = existing.Close()
	}
	bindingCommandTestPortReservations.listeners[addr] = listener
	bindingCommandTestPortReservations.Unlock()
	t.Cleanup(func() { releaseBindingCommandTestPort(addr) })
}

func releaseBindingCommandTestPort(addr string) {
	bindingCommandTestPortReservations.Lock()
	listener := bindingCommandTestPortReservations.listeners[addr]
	delete(bindingCommandTestPortReservations.listeners, addr)
	bindingCommandTestPortReservations.Unlock()
	if listener != nil {
		_ = listener.Close()
	}
}

func releaseBindingCommandTestPortForConfig(cfg config.App) {
	releaseBindingCommandTestPort(net.JoinHostPort(cfg.Settings.ServerHost, strconv.Itoa(cfg.Settings.ServerPort)))
}

func registerBindingCommandWorkspace(t *testing.T, workspace string) metadata.Binding {
	t.Helper()
	setBindingCommandTestHome(t)
	configureBindingCommandTestServerPort(t)
	cfg, err := config.Load(workspace, config.LoadOptions{})
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	binding, err := metadata.RegisterBinding(context.Background(), cfg.PersistenceRoot, cfg.WorkspaceRoot)
	if err != nil {
		t.Fatalf("RegisterBinding: %v", err)
	}
	return binding
}

func newBindingCommandSession(t *testing.T, workspace string) (*metadata.Store, metadata.Binding, *session.Store) {
	t.Helper()
	setBindingCommandTestHome(t)
	cfg, err := config.Load(workspace, config.LoadOptions{})
	if err != nil {
		t.Fatalf("config.Load oldWorkspace: %v", err)
	}
	store, err := metadata.Open(cfg.PersistenceRoot)
	if err != nil {
		t.Fatalf("metadata.Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	binding, err := store.RegisterWorkspaceBinding(context.Background(), cfg.WorkspaceRoot)
	if err != nil {
		t.Fatalf("RegisterBinding oldWorkspace: %v", err)
	}
	sess, err := session.Create(
		filepath.Join(filepath.Join(cfg.PersistenceRoot, "projects"), binding.ProjectID, "sessions"),
		filepath.Base(cfg.WorkspaceRoot),
		cfg.WorkspaceRoot,
		store.AuthoritativeSessionStoreOptions()...,
	)
	if err != nil {
		t.Fatalf("session.Create: %v", err)
	}
	return store, binding, sess
}

func resetBindingCommandRetargetHooks(t *testing.T) {
	t.Helper()
	originalOpener := bindingCommandRemoteOpener
	originalRetargeter := bindingCommandSessionRetargeter
	t.Cleanup(func() {
		bindingCommandRemoteOpener = originalOpener
		bindingCommandSessionRetargeter = originalRetargeter
	})
	setBindingCommandTestHome(t)
}

func setBindingCommandTestHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("KENT_PERSISTENCE_ROOT", filepath.Join(home, ".kent"))
	return home
}

func newBindingCommandWorkspaceConfig(t *testing.T) (string, config.App) {
	t.Helper()
	workspace := t.TempDir()
	cfg, err := config.Load(workspace, config.LoadOptions{})
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	return workspace, cfg
}

func startBindingCommandServer(t *testing.T, workspace string) func() {
	t.Helper()
	cfg, err := config.Load(workspace, config.LoadOptions{})
	if err != nil {
		t.Fatalf("config.Load server workspace: %v", err)
	}
	if !cfg.Source.SettingsFileExists {
		if _, _, err := config.WriteDefaultSettingsFileAt(cfg.Source.HomeSettingsPath); err != nil {
			t.Fatalf("write test settings: %v", err)
		}
	}
	srv, err := serverstartup.StartServeServer(context.Background(), serverstartup.Request{WorkspaceRoot: workspace, WorkspaceRootExplicit: true, Model: "gpt-5"}, bindingCommandMemoryAuthHandler{state: auth.State{
		Scope:     auth.ScopeGlobal,
		Method:    auth.Method{Type: auth.MethodAPIKey, APIKey: &auth.APIKeyMethod{Key: "test-key"}},
		UpdatedAt: time.Now().UTC(),
	}}, bindingCommandAutoOnboarding)
	if err != nil {
		t.Fatalf("serve.Start: %v", err)
	}
	serveCtx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		releaseBindingCommandTestPortForConfig(srv.Config())
		errCh <- srv.Serve(serveCtx)
	}()
	waitForBindingCommandServer(t, workspace)
	return func() {
		cancel()
		if err := <-errCh; err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("Serve error: %v", err)
		}
		_ = srv.Close()
	}
}

func waitForBindingCommandServer(t *testing.T, workspace string) {
	t.Helper()
	cfg, err := config.Load(workspace, config.LoadOptions{})
	if err != nil {
		t.Fatalf("config.Load health workspace: %v", err)
	}
	healthURL := config.ServerHTTPBaseURL(cfg) + protocol.HealthPath
	client := &http.Client{Timeout: 250 * time.Millisecond}
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp, err := client.Get(healthURL)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("binding command test server did not become healthy at %s", healthURL)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestProjectSubcommandPrintsBoundProjectID(t *testing.T) {
	workspace := t.TempDir()
	binding := registerBindingCommandWorkspace(t, workspace)
	cleanup := startBindingCommandServer(t, workspace)
	defer cleanup()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := projectSubcommand([]string{workspace}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit code = %d, want 0 stderr=%q", code, stderr.String())
	}
	if got := stdout.String(); got != binding.ProjectID+"\n" {
		t.Fatalf("stdout = %q, want %q", got, binding.ProjectID+"\n")
	}
}

func TestProjectSubcommandTreatsNestedDirectoryAsUnregistered(t *testing.T) {
	workspace := t.TempDir()
	nested := filepath.Join(workspace, "subdir")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("MkdirAll nested: %v", err)
	}
	registerBindingCommandWorkspace(t, workspace)
	cleanup := startBindingCommandServer(t, workspace)
	defer cleanup()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := projectSubcommand([]string{nested}, &stdout, &stderr); code != 1 {
		t.Fatalf("exit code = %d, want 1 stderr=%q", code, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	if got := stderr.String(); !bytes.Contains([]byte(got), []byte("workspace is not registered")) {
		t.Fatalf("stderr = %q, want unregistered error", got)
	}
}

func TestProjectIDForPathUsesTargetPathServerConfig(t *testing.T) {
	originalOpener := bindingCommandRemoteOpener
	originalResolver := bindingCommandWorkspaceResolver
	t.Cleanup(func() {
		bindingCommandRemoteOpener = originalOpener
		bindingCommandWorkspaceResolver = originalResolver
	})

	target := t.TempDir()
	normalizedTarget, err := normalizeBindingCommandPath(target)
	if err != nil {
		t.Fatalf("normalizeBindingCommandPath: %v", err)
	}
	calledPath := ""
	bindingCommandRemoteOpener = func(ctx context.Context, path string) (config.App, *client.Remote, error) {
		calledPath = path
		return config.App{}, &client.Remote{}, nil
	}
	bindingCommandWorkspaceResolver = func(ctx context.Context, projectViews apicontract.ProjectViewService, workspaceRoot string) (serverapi.ProjectBinding, error) {
		if workspaceRoot != normalizedTarget {
			t.Fatalf("workspace root = %q, want %q", workspaceRoot, normalizedTarget)
		}
		return serverapi.ProjectBinding{ProjectID: "project-target"}, nil
	}

	projectID, err := projectIDForPath(context.Background(), target)
	if err != nil {
		t.Fatalf("projectIDForPath target: %v", err)
	}
	if calledPath != normalizedTarget {
		t.Fatalf("openBindingCommandRemote path = %q, want %q", calledPath, normalizedTarget)
	}
	if projectID != "project-target" {
		t.Fatalf("project id = %q, want project-target", projectID)
	}
}

func TestAttachSubcommandPathFirstBindsTargetToCurrentProject(t *testing.T) {
	source := t.TempDir()
	target := t.TempDir()
	binding := registerBindingCommandWorkspace(t, source)
	cleanup := startBindingCommandServer(t, source)
	defer cleanup()

	previousWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(source); err != nil {
		t.Fatalf("Chdir source: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(previousWD) })

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := attachSubcommand([]string{target}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit code = %d, want 0 stderr=%q", code, stderr.String())
	}
	if got := stdout.String(); got != binding.ProjectID+"\n" {
		t.Fatalf("stdout = %q, want %q", got, binding.ProjectID+"\n")
	}

	targetCfg, err := config.Load(target, config.LoadOptions{})
	if err != nil {
		t.Fatalf("config.Load target: %v", err)
	}
	resolved, err := metadata.ResolveBinding(context.Background(), targetCfg.PersistenceRoot, targetCfg.WorkspaceRoot)
	if err != nil {
		t.Fatalf("ResolveBinding target: %v", err)
	}
	if resolved.ProjectID != binding.ProjectID {
		t.Fatalf("target project id = %q, want %q", resolved.ProjectID, binding.ProjectID)
	}
}

func TestAttachSubcommandExplicitProjectOverridesCurrentWorkspace(t *testing.T) {
	source := t.TempDir()
	target := t.TempDir()
	working := t.TempDir()
	binding := registerBindingCommandWorkspace(t, source)
	cleanup := startBindingCommandServer(t, source)
	defer cleanup()

	previousWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(working); err != nil {
		t.Fatalf("Chdir working: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(previousWD) })

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := attachSubcommand([]string{"--project", binding.ProjectID, target}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit code = %d, want 0 stderr=%q", code, stderr.String())
	}
	if got := stdout.String(); got != binding.ProjectID+"\n" {
		t.Fatalf("stdout = %q, want %q", got, binding.ProjectID+"\n")
	}
}

func TestAttachSubcommandWithoutProjectGuidanceFailsWhenCurrentWorkspaceUnregistered(t *testing.T) {
	setBindingCommandTestHome(t)
	working := t.TempDir()
	target := t.TempDir()
	configureBindingCommandTestServerPort(t)
	cleanup := startBindingCommandServer(t, working)
	defer cleanup()

	previousWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(working); err != nil {
		t.Fatalf("Chdir working: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(previousWD) })

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := attachSubcommand([]string{target}, &stdout, &stderr); code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	if got := stderr.String(); got == "" || !bytes.Contains([]byte(got), []byte("kent project")) || !bytes.Contains([]byte(got), []byte("--project <project-id>")) {
		t.Fatalf("stderr = %q, want recovery guidance", got)
	}
}

func TestAttachSubcommandRejectsUnknownExplicitProjectIDCleanly(t *testing.T) {
	setBindingCommandTestHome(t)
	target := t.TempDir()
	configureBindingCommandTestServerPort(t)
	cleanup := startBindingCommandServer(t, target)
	defer cleanup()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := attachSubcommand([]string{"--project", "project-missing", target}, &stdout, &stderr); code != 1 {
		t.Fatalf("exit code = %d, want 1 stderr=%q", code, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	if got := stderr.String(); !bytes.Contains([]byte(got), []byte("does not exist in this Kent state")) || !bytes.Contains([]byte(got), []byte("project-missing")) {
		t.Fatalf("stderr = %q, want missing project guidance", got)
	}
}

func TestRebindSubcommandRetargetsSessionWorkspace(t *testing.T) {
	oldWorkspace := t.TempDir()
	newWorkspace := t.TempDir()
	store, binding, sess := newBindingCommandSession(t, oldWorkspace)
	configureBindingCommandTestServerPort(t)
	cleanup := startBindingCommandServer(t, oldWorkspace)
	defer cleanup()
	if err := sess.SetName("incident triage"); err != nil {
		t.Fatalf("SetName: %v", err)
	}
	if err := sess.SetName("incident triage"); err != nil {
		t.Fatalf("SetName: %v", err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := rebindSubcommand([]string{sess.Meta().SessionID, newWorkspace}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit code = %d, want 0 stderr=%q", code, stderr.String())
	}
	resolvedBinding, err := store.EnsureWorkspaceBinding(context.Background(), newWorkspace)
	if err != nil {
		t.Fatalf("EnsureWorkspaceBinding newWorkspace: %v", err)
	}
	if got := stdout.String(); got != resolvedBinding.WorkspaceID+"\n" {
		t.Fatalf("stdout = %q, want %q", got, resolvedBinding.WorkspaceID+"\n")
	}
	if resolvedBinding.ProjectID != binding.ProjectID {
		t.Fatalf("new workspace project id = %q, want %q", resolvedBinding.ProjectID, binding.ProjectID)
	}
	target, err := store.ResolveSessionExecutionTarget(context.Background(), sess.Meta().SessionID)
	if err != nil {
		t.Fatalf("ResolveSessionExecutionTarget: %v", err)
	}
	if target.WorkspaceID != resolvedBinding.WorkspaceID {
		t.Fatalf("target workspace id = %q, want %q", target.WorkspaceID, resolvedBinding.WorkspaceID)
	}
	if target.WorkspaceRoot != resolvedBinding.CanonicalRoot {
		t.Fatalf("target workspace root = %q, want %q", target.WorkspaceRoot, resolvedBinding.CanonicalRoot)
	}
}

func TestRebindSubcommandRejectsInvalidInputs(t *testing.T) {
	oldWorkspace := t.TempDir()
	otherWorkspace := t.TempDir()
	targetWorkspace := t.TempDir()
	missingWorkspace := filepath.Join(t.TempDir(), "missing")
	store, _, sess := newBindingCommandSession(t, oldWorkspace)
	configureBindingCommandTestServerPort(t)
	cleanup := startBindingCommandServer(t, oldWorkspace)
	defer cleanup()
	if err := sess.SetName("incident triage"); err != nil {
		t.Fatalf("SetName: %v", err)
	}
	otherCfg, err := config.Load(otherWorkspace, config.LoadOptions{})
	if err != nil {
		t.Fatalf("config.Load otherWorkspace: %v", err)
	}
	_, err = store.RegisterWorkspaceBinding(context.Background(), otherCfg.WorkspaceRoot)
	if err != nil {
		t.Fatalf("RegisterBinding otherWorkspace: %v", err)
	}

	assertRebindError := func(args []string, want string) {
		t.Helper()
		var stdout bytes.Buffer
		var stderr bytes.Buffer
		if code := rebindSubcommand(args, &stdout, &stderr); code != 1 {
			t.Fatalf("exit code = %d, want 1 stderr=%q", code, stderr.String())
		}
		if stdout.Len() != 0 {
			t.Fatalf("stdout = %q, want empty", stdout.String())
		}
		if got := stderr.String(); !bytes.Contains([]byte(got), []byte(want)) {
			t.Fatalf("stderr = %q, want %q", got, want)
		}
	}

	assertRebindError([]string{"session-missing", targetWorkspace}, session.ErrSessionNotFound.Error())
	assertRebindError([]string{sess.Meta().SessionID, missingWorkspace}, missingWorkspace)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := rebindSubcommand([]string{sess.Meta().SessionID}, &stdout, &stderr); code != 2 {
		t.Fatalf("exit code = %d, want 2 stderr=%q", code, stderr.String())
	}
	if got := stderr.String(); !bytes.Contains([]byte(got), []byte("rebind requires <session-id> and <new-path>")) {
		t.Fatalf("stderr = %q, want usage guidance", got)
	}
}

func TestRebindSubcommandRejectsBlankProjectBeforeOpeningServer(t *testing.T) {
	resetBindingCommandRetargetHooks(t)
	opened := false
	bindingCommandRemoteOpener = func(context.Context, string) (config.App, *client.Remote, error) {
		opened = true
		return config.App{}, nil, errors.New("unexpected server open")
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := rebindSubcommand([]string{"--project", " ", "session-123", t.TempDir()}, &stdout, &stderr); code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if opened {
		t.Fatal("blank project flag opened the server")
	}
	if stdout.Len() != 0 || stderr.Len() == 0 {
		t.Fatalf("stdout = %q stderr = %q, want empty stdout and validation error", stdout.String(), stderr.String())
	}
}

func TestRetargetSessionWorkspaceUsesDaemonAndPassesTargetProject(t *testing.T) {
	resetBindingCommandRetargetHooks(t)
	newWorkspace, newCfg := newBindingCommandWorkspaceConfig(t)
	bindingCommandRemoteOpener = func(context.Context, string) (config.App, *client.Remote, error) {
		return newCfg, &client.Remote{}, nil
	}
	remoteCalls := 0
	const sessionID = "session-123"
	const projectID = "project-target"
	bindingCommandSessionRetargeter = func(ctx context.Context, lifecycle apicontract.SessionLifecycleService, gotSessionID string, workspaceRoot string, gotProjectID *string) (serverapi.SessionRetargetWorkspaceResponse, error) {
		if gotSessionID != sessionID {
			t.Fatalf("session id = %q, want %q", gotSessionID, sessionID)
		}
		if workspaceRoot != newCfg.WorkspaceRoot {
			t.Fatalf("workspace root = %q, want %q", workspaceRoot, newCfg.WorkspaceRoot)
		}
		if gotProjectID == nil || *gotProjectID != projectID {
			t.Fatalf("project id = %v, want %q", gotProjectID, projectID)
		}
		remoteCalls++
		return serverapi.SessionRetargetWorkspaceResponse{Binding: serverapi.ProjectBinding{WorkspaceID: "workspace-remote"}}, nil
	}

	targetProjectID := projectID
	response, err := retargetSessionWorkspaceResponse(context.Background(), sessionID, newWorkspace, &targetProjectID)
	if err != nil {
		t.Fatalf("retargetSessionWorkspaceResponse: %v", err)
	}
	if response.Binding.WorkspaceID != "workspace-remote" {
		t.Fatalf("binding workspace id = %q, want %q", response.Binding.WorkspaceID, "workspace-remote")
	}
	if remoteCalls != 1 {
		t.Fatalf("remote calls = %d, want 1", remoteCalls)
	}
}

func TestRebindSubcommandReportsAutoAttachedWorkspace(t *testing.T) {
	resetBindingCommandRetargetHooks(t)
	targetPath, targetCfg := newBindingCommandWorkspaceConfig(t)
	bindingCommandRemoteOpener = func(context.Context, string) (config.App, *client.Remote, error) {
		return targetCfg, &client.Remote{}, nil
	}
	const (
		sessionID = "session-123"
		projectID = "project-target"
	)
	bindingCommandSessionRetargeter = func(_ context.Context, _ apicontract.SessionLifecycleService, gotSessionID string, workspaceRoot string, gotProjectID *string) (serverapi.SessionRetargetWorkspaceResponse, error) {
		if gotSessionID != sessionID || workspaceRoot != targetCfg.WorkspaceRoot || gotProjectID == nil || *gotProjectID != projectID {
			t.Fatalf("retarget request = session:%q workspace:%q project:%v", gotSessionID, workspaceRoot, gotProjectID)
		}
		return serverapi.SessionRetargetWorkspaceResponse{
			Binding: serverapi.ProjectBinding{
				ProjectID:     projectID,
				ProjectName:   "Target",
				WorkspaceID:   "workspace-created",
				CanonicalRoot: targetCfg.WorkspaceRoot,
			},
			WorkspaceBindingCreated: true,
		}, nil
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := rebindSubcommand([]string{"--project", projectID, sessionID, targetPath}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit code = %d, want 0 stderr=%q", code, stderr.String())
	}
	if stdout.String() != "workspace-created\n" {
		t.Fatalf("stdout = %q, want workspace id", stdout.String())
	}
	if stderr.Len() == 0 {
		t.Fatal("auto-attached workspace did not produce an operator notice")
	}
}

func TestRebindSubcommandFormatsCrossProjectChoicesAsExecutableCommands(t *testing.T) {
	resetBindingCommandRetargetHooks(t)
	targetPath := filepath.Join(t.TempDir(), "target workspace's copy")
	if err := os.MkdirAll(targetPath, 0o755); err != nil {
		t.Fatalf("MkdirAll target path: %v", err)
	}
	targetCfg, err := config.Load(targetPath, config.LoadOptions{})
	if err != nil {
		t.Fatalf("config.Load target: %v", err)
	}
	bindingCommandRemoteOpener = func(context.Context, string) (config.App, *client.Remote, error) {
		return targetCfg, &client.Remote{}, nil
	}
	const (
		sessionID       = "session-123"
		sourceProjectID = "project-source"
		targetProjectID = "project-target"
	)
	retargetErr := &serverapi.SessionRetargetError{
		Reason:        serverapi.SessionRetargetTargetProjectRequired,
		SessionID:     sessionID,
		SourceProject: serverapi.ProjectReference{ID: sourceProjectID, Name: "Source"},
		TargetRoot:    targetCfg.WorkspaceRoot,
		CandidateProjects: []serverapi.ProjectReference{{
			ID:   targetProjectID,
			Name: "Target",
		}},
	}
	bindingCommandSessionRetargeter = func(context.Context, apicontract.SessionLifecycleService, string, string, *string) (serverapi.SessionRetargetWorkspaceResponse, error) {
		return serverapi.SessionRetargetWorkspaceResponse{}, retargetErr
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := rebindSubcommand([]string{sessionID, targetPath}, &stdout, &stderr); code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	if stderr.Len() == 0 {
		t.Fatal("cross-project choices did not produce operator guidance")
	}
	guidance := buildSessionRetargetCommandGuidance(targetPath, retargetErr)
	if len(guidance.Candidates) != 1 ||
		!slices.Equal(guidance.Candidates[0].Tokens, []string{config.Command, "rebind", "--project", targetProjectID, sessionID, targetPath}) ||
		!slices.Equal(guidance.AttachToSource, []string{config.Command, "attach", "--project", sourceProjectID, targetPath}) ||
		!slices.Equal(guidance.RebindIntoSource, []string{config.Command, "rebind", sessionID, targetPath}) {
		t.Fatalf("guidance commands = %+v", guidance)
	}
}

func TestRebindSubcommandRejectsExplicitProjectForForeignBoundTarget(t *testing.T) {
	resetBindingCommandRetargetHooks(t)
	targetPath, targetCfg := newBindingCommandWorkspaceConfig(t)
	bindingCommandRemoteOpener = func(context.Context, string) (config.App, *client.Remote, error) {
		return targetCfg, &client.Remote{}, nil
	}
	const (
		sessionID          = "session-123"
		requestedProjectID = "project-requested"
		existingProjectID  = "project-existing"
	)
	retargetErr := &serverapi.SessionRetargetError{
		Reason:        serverapi.SessionRetargetTargetProjectConflict,
		SessionID:     sessionID,
		SourceProject: serverapi.ProjectReference{ID: "project-source", Name: "Source"},
		TargetRoot:    targetCfg.WorkspaceRoot,
		CandidateProjects: []serverapi.ProjectReference{{
			ID:   existingProjectID,
			Name: "Existing",
		}},
	}
	bindingCommandSessionRetargeter = func(_ context.Context, _ apicontract.SessionLifecycleService, gotSessionID string, workspaceRoot string, gotProjectID *string) (serverapi.SessionRetargetWorkspaceResponse, error) {
		if gotSessionID != sessionID || workspaceRoot != targetCfg.WorkspaceRoot || gotProjectID == nil || *gotProjectID != requestedProjectID {
			t.Fatalf("retarget request = session:%q workspace:%q project:%v", gotSessionID, workspaceRoot, gotProjectID)
		}
		return serverapi.SessionRetargetWorkspaceResponse{}, retargetErr
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := rebindSubcommand([]string{"--project", requestedProjectID, sessionID, targetPath}, &stdout, &stderr); code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	if stderr.Len() == 0 {
		t.Fatal("foreign binding conflict did not produce operator guidance")
	}
	guidance := buildSessionRetargetCommandGuidance(targetPath, retargetErr)
	if len(guidance.Candidates) != 1 ||
		!slices.Equal(guidance.Candidates[0].Tokens, []string{config.Command, "rebind", "--project", existingProjectID, sessionID, targetPath}) ||
		guidance.AttachToSource != nil ||
		guidance.RebindIntoSource != nil {
		t.Fatalf("guidance commands = %+v", guidance)
	}
}

func TestRetargetSessionWorkspaceReturnsDaemonOpenFailure(t *testing.T) {
	resetBindingCommandRetargetHooks(t)
	newWorkspace := t.TempDir()
	want := &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connect refused")}
	bindingCommandRemoteOpener = func(context.Context, string) (config.App, *client.Remote, error) {
		return config.App{}, nil, want
	}
	_, err := retargetSessionWorkspaceResponse(context.Background(), "session-123", newWorkspace, nil)
	if !errors.Is(err, want) {
		t.Fatalf("retargetSessionWorkspaceResponse error = %v, want daemon open failure", err)
	}
}
