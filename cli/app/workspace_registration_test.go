package app

import (
	"context"
	"errors"
	"io"
	"net"
	"path/filepath"
	"strconv"
	"sync"
	"testing"

	"core/server/llm"
	"core/server/metadata"
	"core/server/projectview"
	"core/server/runtime"
	"core/server/session"
	serverstartup "core/server/startup"
	"core/server/tools"
	"core/shared/client"
	"core/shared/config"
)

func registerAppWorkspace(t *testing.T, workspace string) {
	t.Helper()
	configureAppTestServerPort(t)
	cfg, err := config.Load(workspace, config.LoadOptions{})
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	_ = mustRegisterAppBinding(t, cfg.PersistenceRoot, cfg.WorkspaceRoot)
}

func newAppTestHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	return home
}

func newRegisteredAppWorkspace(t *testing.T) (home string, workspace string) {
	t.Helper()
	home = newAppTestHome(t)
	workspace = t.TempDir()
	registerAppWorkspace(t, workspace)
	return home, workspace
}

func loadAppTestConfig(t *testing.T, workspace string, opts config.LoadOptions) config.App {
	t.Helper()
	cfg, err := config.Load(workspace, opts)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	return cfg
}

func newAppMetadataProjectViewClient(t *testing.T, cfg config.App) client.ProjectViewClient {
	t.Helper()
	store, err := metadata.Open(cfg.PersistenceRoot)
	if err != nil {
		t.Fatalf("metadata.Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	service, err := projectview.NewMetadataService(store, "")
	if err != nil {
		t.Fatalf("NewMetadataService: %v", err)
	}
	return client.NewLoopbackProjectViewClient(service)
}

// startStandingRunPromptServer starts an in-process standing serve server for
// the workspace and waits until it is reachable for headless run-prompt attach.
// kent run is a pure client and never starts its own server, so integration
// tests that drive RunPrompt end-to-end must provide a server to attach to.
// Callers defer the returned cleanup, which stops serving and closes the server.
func startStandingRunPromptServer(t *testing.T, workspace, openAIBaseURL string) func() {
	return startStandingRunPromptServerWithAuth(t, workspace, openAIBaseURL, apiKeyMemoryAuthHandler("test-key"))
}

func startStandingRunPromptServerWithAuth(t *testing.T, workspace, openAIBaseURL string, authHandler serverstartup.AuthHandler) func() {
	t.Helper()
	releasePortProbe := reserveAppDirectServePort(t)
	srv, err := serverstartup.StartServeServer(context.Background(), serverstartup.Request{
		WorkspaceRoot:         workspace,
		WorkspaceRootExplicit: true,
		Model:                 "gpt-5",
		OpenAIBaseURL:         openAIBaseURL,
		OpenAIBaseURLExplicit: openAIBaseURL != "",
	}, authHandler, autoOnboarding)
	if err != nil {
		t.Fatalf("StartServeServer: %v", err)
	}
	stop := serveAppServerAfter(t, srv, releasePortProbe)
	cleanup := func() {
		stop()
		_ = srv.Close()
	}
	needsCleanup := true
	defer func() {
		if needsCleanup {
			cleanup()
		}
	}()
	waitForConfiguredRunPromptDaemon(t, workspace)
	needsCleanup = false
	return cleanup
}

func serveAppServer(t *testing.T, srv *serverstartup.ServeServer) func() {
	return serveAppServerAfter(t, srv, func() {})
}

func serveAppServerAfter(t *testing.T, srv *serverstartup.ServeServer, beforeServe func()) func() {
	t.Helper()
	serveCtx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		beforeServe()
		errCh <- srv.Serve(serveCtx)
	}()
	return func() {
		cancel()
		if serveErr := <-errCh; !errors.Is(serveErr, context.Canceled) {
			t.Fatalf("Serve error = %v, want context canceled", serveErr)
		}
	}
}

func reserveAppDirectServePort(t *testing.T) func() {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve direct serve port: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	t.Setenv("KENT_SERVER_HOST", "127.0.0.1")
	t.Setenv("KENT_SERVER_PORT", strconv.Itoa(port))
	var once sync.Once
	release := func() {
		once.Do(func() { _ = listener.Close() })
	}
	t.Cleanup(release)
	return release
}

func prepareAppRuntimePlan(t *testing.T, server launchPlannerServer, req sessionLaunchRequest, diagnosticWriter io.Writer, startLogLine string) (sessionLaunchPlan, *runtimeLaunchPlan) {
	t.Helper()
	planner := newSessionLaunchPlanner(server)
	plan, err := planner.PlanSession(context.Background(), req)
	if err != nil {
		t.Fatalf("PlanSession: %v", err)
	}
	runtimePlan, err := planner.PrepareRuntime(context.Background(), plan, diagnosticWriter, startLogLine)
	if err != nil {
		t.Fatalf("PrepareRuntime: %v", err)
	}
	return plan, runtimePlan
}

func prepareAppRuntimePlanWithOpenAIBaseURL(t *testing.T, server launchPlannerServer, req sessionLaunchRequest, openAIBaseURL string, diagnosticWriter io.Writer, startLogLine string) (sessionLaunchPlan, *runtimeLaunchPlan) {
	t.Helper()
	planner := newSessionLaunchPlanner(server)
	plan, err := planner.PlanSession(context.Background(), req)
	if err != nil {
		t.Fatalf("PlanSession: %v", err)
	}
	plan.ActiveSettings.OpenAIBaseURL = openAIBaseURL
	runtimePlan, err := planner.PrepareRuntime(context.Background(), plan, diagnosticWriter, startLogLine)
	if err != nil {
		t.Fatalf("PrepareRuntime: %v", err)
	}
	return plan, runtimePlan
}

func newAppRuntimeEngine(t *testing.T, client llm.Client, cfg runtime.Config, handlers ...tools.HandlerRegistration) (*session.Store, *runtime.Engine) {
	t.Helper()
	store := createAppRuntimeSession(t)
	return store, newAppRuntimeEngineWithStore(t, store, client, cfg, handlers...)
}

func createAppRuntimeSession(t *testing.T) *session.Store {
	t.Helper()
	dir := t.TempDir()
	return createAppRuntimeSessionAt(t, dir, "ws", dir)
}

func createAppRuntimeSessionAt(t *testing.T, root string, workspaceContainerName string, workspaceRoot string) *session.Store {
	t.Helper()
	store, err := session.Create(root, workspaceContainerName, workspaceRoot)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	return store
}

func newAppRuntimeEngineWithStore(t *testing.T, store *session.Store, client llm.Client, cfg runtime.Config, handlers ...tools.HandlerRegistration) *runtime.Engine {
	t.Helper()
	if cfg.Model == "" {
		cfg.Model = "gpt-5"
	}
	eng, err := runtime.New(store, client, tools.NewRegistry(handlers...), cfg)
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}
	return eng
}

func configureAppTestServerPort(t *testing.T) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve server port: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatalf("release server port probe: %v", err)
	}
	t.Setenv("KENT_SERVER_HOST", "127.0.0.1")
	t.Setenv("KENT_SERVER_PORT", strconv.Itoa(port))
}

func mustRegisterAppBinding(t *testing.T, persistenceRoot string, workspaceRoot string) metadata.Binding {
	t.Helper()
	binding, err := metadata.RegisterBinding(context.Background(), persistenceRoot, workspaceRoot)
	if err != nil {
		t.Fatalf("RegisterBinding: %v", err)
	}
	return binding
}

func createAuthoritativeAppSession(t *testing.T, persistenceRoot string, workspaceRoot string) *session.Store {
	t.Helper()
	binding := mustRegisterAppBinding(t, persistenceRoot, workspaceRoot)
	metadataStore, err := metadata.Open(persistenceRoot)
	if err != nil {
		t.Fatalf("metadata.Open: %v", err)
	}
	// Keep the metadata store alive for the lifetime of the session store so
	// persistence observer writes continue to succeed during the test.
	store, err := session.Create(
		filepath.Join(filepath.Join(config.App{PersistenceRoot: persistenceRoot}.PersistenceRoot, "projects"), binding.ProjectID, "sessions"),
		filepath.Base(filepath.Clean(workspaceRoot)),
		workspaceRoot,
		metadataStore.AuthoritativeSessionStoreOptions()...,
	)
	if err != nil {
		_ = metadataStore.Close()
		t.Fatalf("session.Create: %v", err)
	}
	if err := store.EnsureDurable(); err != nil {
		_ = metadataStore.Close()
		t.Fatalf("EnsureDurable: %v", err)
	}
	t.Cleanup(func() { _ = metadataStore.Close() })
	return store
}

func openAuthoritativeAppSession(t *testing.T, persistenceRoot string, sessionID string) *session.Store {
	t.Helper()
	metadataStore, err := metadata.Open(persistenceRoot)
	if err != nil {
		t.Fatalf("metadata.Open: %v", err)
	}
	store, err := session.OpenByID(persistenceRoot, sessionID, metadataStore.AuthoritativeSessionStoreOptions()...)
	if err != nil {
		_ = metadataStore.Close()
		t.Fatalf("session.OpenByID: %v", err)
	}
	t.Cleanup(func() { _ = metadataStore.Close() })
	return store
}
