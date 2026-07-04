package startup

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"core/server/auth"
	"core/server/authservice"
	corepkg "core/server/core"
	"core/server/metadata"
	rpccontract "core/shared/apicontract"
	"core/shared/client"
	"core/shared/config"
	"core/shared/protocol"
	"core/shared/serverapi"
)

type envAuthHandler struct {
	lookupEnv func(string) string
}

func (h envAuthHandler) WrapStore(base auth.Store) auth.Store {
	return authservice.WrapStoreWithEnvAPIKeyOverride(base, h.LookupEnv)
}

func (envAuthHandler) NeedsInteraction(req authservice.FlowInteractionRequest) bool {
	return !req.Gate.Ready
}

func (envAuthHandler) Interact(context.Context, authservice.FlowInteractionRequest) (authservice.FlowInteractionOutcome, error) {
	return authservice.FlowInteractionOutcome{}, auth.ErrAuthNotConfigured
}

func (h envAuthHandler) LookupEnv(key string) string {
	if h.lookupEnv != nil {
		return h.lookupEnv(key)
	}
	return testAuthLookupEnv(key)
}

func testAuthLookupEnv(key string) string {
	if key == "OPENAI_API_KEY" {
		return "in-memory-test-key"
	}
	return ""
}

var noopOnboarding = OnboardingHandler(func(_ context.Context, req OnboardingRequest) (config.App, error) {
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

var serveTestPortReservations = struct {
	sync.Mutex
	listeners map[string]net.Listener
}{listeners: map[string]net.Listener{}}

func reserveServeTestPort(t *testing.T, listener net.Listener) {
	t.Helper()
	addr := listener.Addr().String()
	serveTestPortReservations.Lock()
	if existing := serveTestPortReservations.listeners[addr]; existing != nil {
		_ = existing.Close()
	}
	serveTestPortReservations.listeners[addr] = listener
	serveTestPortReservations.Unlock()
	t.Cleanup(func() { releaseServeTestPort(addr) })
}

func releaseServeTestPort(addr string) {
	serveTestPortReservations.Lock()
	listener := serveTestPortReservations.listeners[addr]
	delete(serveTestPortReservations.listeners, addr)
	serveTestPortReservations.Unlock()
	if listener != nil {
		_ = listener.Close()
	}
}

func releaseServeTestPortForConfig(cfg config.App) {
	releaseServeTestPort(net.JoinHostPort(cfg.Settings.ServerHost, strconv.Itoa(cfg.Settings.ServerPort)))
}

func registerServeWorkspace(t *testing.T, workspace string) {
	t.Helper()
	configureServeTestServerPort(t)
	cfg, err := config.Load(workspace, config.LoadOptions{})
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	if _, _, err := config.WriteDefaultSettingsFileAt(cfg.Source.HomeSettingsPath); err != nil {
		t.Fatalf("write test settings: %v", err)
	}
	if _, err := metadata.RegisterBinding(context.Background(), cfg.PersistenceRoot, cfg.WorkspaceRoot); err != nil {
		t.Fatalf("RegisterBinding: %v", err)
	}
}

func newServeWorkspace(t *testing.T) string {
	t.Helper()
	workspace := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	registerServeWorkspace(t, workspace)
	return workspace
}

func startServeTestServer(t *testing.T, request Request, authHandler envAuthHandler, onboarding OnboardingHandler) *ServeServer {
	t.Helper()
	server, err := StartServeServer(context.Background(), request, authHandler, onboarding)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })
	return server
}

func configureServeTestServerPort(t *testing.T) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve server port: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	reserveServeTestPort(t, listener)
	t.Setenv("KENT_SERVER_HOST", "127.0.0.1")
	t.Setenv("KENT_SERVER_PORT", strconv.Itoa(port))
}

func TestStartBuildsStandaloneServerFromCoreStartup(t *testing.T) {
	workspace := newServeWorkspace(t)

	request := Request{WorkspaceRoot: workspace, WorkspaceRootExplicit: true}
	authHandler := envAuthHandler{}
	onboarding := noopOnboarding

	appCore, err := StartCore(context.Background(), request, authHandler, onboarding)
	if err != nil {
		t.Fatalf("StartCore: %v", err)
	}
	coreProjectID := appCore.ProjectID()
	coreProjects, err := appCore.ProjectViewClient().ListProjects(context.Background(), serverapi.ProjectListRequest{})
	if err != nil {
		t.Fatalf("core ListProjects: %v", err)
	}
	if err := appCore.Close(); err != nil {
		t.Fatalf("appCore.Close: %v", err)
	}

	server := startServeTestServer(t, request, authHandler, onboarding)

	if server.Core == nil {
		t.Fatal("expected standalone server to expose core")
	}
	if server.ProjectID() != coreProjectID {
		t.Fatalf("project id mismatch: server=%q core=%q", server.ProjectID(), coreProjectID)
	}
	if server.ProjectViewClient() == nil || server.SessionViewClient() == nil || server.ProcessViewClient() == nil || server.ProcessOutputClient() == nil || server.RunPromptClient() == nil {
		t.Fatal("expected standalone server to expose core-backed clients")
	}
	serverProjects, err := server.ProjectViewClient().ListProjects(context.Background(), serverapi.ProjectListRequest{})
	if err != nil {
		t.Fatalf("server ListProjects: %v", err)
	}
	if len(coreProjects.Projects) != 1 || len(serverProjects.Projects) != 1 {
		t.Fatalf("unexpected project counts core=%d server=%d", len(coreProjects.Projects), len(serverProjects.Projects))
	}
	if coreProjects.Projects[0].ProjectID != serverProjects.Projects[0].ProjectID {
		t.Fatalf("project listing mismatch core=%+v server=%+v", coreProjects.Projects[0], serverProjects.Projects[0])
	}
}

func TestServerIdentityCapabilitiesFollowRouteContracts(t *testing.T) {
	capabilities := newServerIdentity(config.App{}).Capabilities
	if !capabilities.JSONRPCWebSocket ||
		!capabilities.AuthBootstrap ||
		!capabilities.ProjectAttach ||
		!capabilities.SessionAttach ||
		!capabilities.HealthEndpoint ||
		!capabilities.ReadinessEndpoint ||
		!capabilities.RunPrompt ||
		!capabilities.SessionPlan ||
		!capabilities.SessionLifecycle ||
		!capabilities.SessionTranscript ||
		!capabilities.SessionRuntime ||
		!capabilities.RuntimeControl ||
		!capabilities.RuntimeLiveControl ||
		!capabilities.PromptControl ||
		!capabilities.PromptActivity ||
		!capabilities.SessionActivity ||
		!capabilities.ProcessOutput ||
		!capabilities.AttentionNotifications ||
		!capabilities.OnboardingFinalize {
		t.Fatalf("current route contracts produced incomplete server capabilities: %+v", capabilities)
	}
}

func TestServerCapabilityFlagsReflectMissingRoutes(t *testing.T) {
	capabilities := serverCapabilityFlags([]rpccontract.Route{
		{Method: protocol.MethodHandshake, Dependency: rpccontract.DependencyProtocol},
		{Method: protocol.MethodAttachProject, Dependency: rpccontract.DependencyProtocolAttach},
		{Method: protocol.MethodAttachSession, Dependency: rpccontract.DependencyProtocolAttach},
		{Dependency: rpccontract.DependencyRunPrompt},
	})

	if !capabilities.JSONRPCWebSocket || !capabilities.ProjectAttach || !capabilities.SessionAttach || !capabilities.RunPrompt {
		t.Fatalf("expected supplied routes to enable matching capabilities: %+v", capabilities)
	}
	if !capabilities.HealthEndpoint || !capabilities.ReadinessEndpoint {
		t.Fatalf("health/readiness endpoints are mux capabilities, got %+v", capabilities)
	}
	if capabilities.AuthBootstrap ||
		capabilities.RuntimeLiveControl ||
		capabilities.AttentionNotifications {
		t.Fatalf("capabilities must not be true without their routes/dependencies: %+v", capabilities)
	}
}

func TestServerCapabilityFlagsRequireAttachRouteDependencyPerMethod(t *testing.T) {
	capabilities := serverCapabilityFlags([]rpccontract.Route{
		{Method: protocol.MethodHandshake, Dependency: rpccontract.DependencyProtocol},
		{Method: protocol.MethodAttachProject, Dependency: rpccontract.DependencyProtocol},
		{Method: protocol.MethodAttachSession, Dependency: rpccontract.DependencyProtocolAttach},
	})

	if capabilities.ProjectAttach {
		t.Fatalf("ProjectAttach advertised without protocol_attach dependency: %+v", capabilities)
	}
	if !capabilities.SessionAttach {
		t.Fatalf("SessionAttach not advertised with protocol_attach dependency: %+v", capabilities)
	}
}

func TestServerCapabilityFlagsAdvertiseRuntimeLiveControlWhenHandlersAreExecutable(t *testing.T) {
	capabilities := serverCapabilityFlags(rpccontract.Routes())
	if !capabilities.RuntimeLiveControl {
		t.Fatalf("RuntimeLiveControl not advertised after executable live handlers are wired: %+v", capabilities)
	}
}

func TestStartRejectsSecondOwnerForSamePersistenceRoot(t *testing.T) {
	workspace := newServeWorkspace(t)

	request := Request{WorkspaceRoot: workspace, WorkspaceRootExplicit: true}
	authHandler := envAuthHandler{}
	onboarding := noopOnboarding

	_ = startServeTestServer(t, request, authHandler, onboarding)

	_, err := StartServeServer(context.Background(), request, authHandler, onboarding)
	if !errors.Is(err, corepkg.ErrPersistenceRootBusy) {
		t.Fatalf("Start second error = %v, want ErrPersistenceRootBusy", err)
	}
}

func TestServeWaitsForContextCancellation(t *testing.T) {
	server := &ServeServer{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := server.Serve(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Serve error = %v, want context canceled", err)
	}
}

func TestServeRequiresContext(t *testing.T) {
	server := &ServeServer{}
	if err := server.Serve(nil); err == nil || !errors.Is(err, errContextRequired) {
		t.Fatalf("Serve error = %v, want missing context error", err)
	}
}

func TestServeExposesConfiguredHealthEndpoints(t *testing.T) {
	workspace := newServeWorkspace(t)

	request := Request{WorkspaceRoot: workspace, WorkspaceRootExplicit: true}
	authHandler := envAuthHandler{}
	onboarding := noopOnboarding

	server := startServeTestServer(t, request, authHandler, onboarding)

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		releaseServeTestPortForConfig(server.Config())
		errCh <- server.Serve(ctx)
	}()

	loadCfg, err := config.Load(workspace, config.LoadOptions{})
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	healthURL := config.ServerHTTPBaseURL(loadCfg) + protocol.HealthPath
	readyURL := config.ServerHTTPBaseURL(loadCfg) + protocol.ReadinessPath
	deadline := time.Now().Add(5 * time.Second)
	var healthResp *http.Response
	for {
		healthResp, err = http.Get(healthURL)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("GET health: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	defer func() { _ = healthResp.Body.Close() }()
	if healthResp.StatusCode != http.StatusOK {
		t.Fatalf("health status = %d, want 200", healthResp.StatusCode)
	}
	var healthBody map[string]any
	if err := json.NewDecoder(healthResp.Body).Decode(&healthBody); err != nil {
		t.Fatalf("decode health body: %v", err)
	}
	if healthBody["status"] != "ok" {
		t.Fatalf("unexpected health body: %+v", healthBody)
	}

	readyResp, err := http.Get(readyURL)
	if err != nil {
		t.Fatalf("GET ready: %v", err)
	}
	defer func() { _ = readyResp.Body.Close() }()
	if readyResp.StatusCode != http.StatusOK {
		t.Fatalf("readiness status = %d, want 200", readyResp.StatusCode)
	}

	cancel()
	if serveErr := <-errCh; !errors.Is(serveErr, context.Canceled) {
		t.Fatalf("Serve error = %v, want context canceled", serveErr)
	}
}

func TestServeExposesDerivedLocalUnixSocketAndCleansStalePath(t *testing.T) {
	workspace := newServeWorkspace(t)

	request := Request{WorkspaceRoot: workspace, WorkspaceRootExplicit: true}
	authHandler := envAuthHandler{}
	onboarding := noopOnboarding

	loadCfg, err := config.Load(workspace, config.LoadOptions{})
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	socketPath, ok, err := config.ServerLocalRPCSocketPath(loadCfg)
	if err != nil {
		t.Fatalf("ServerLocalRPCSocketPath: %v", err)
	}
	if !ok {
		t.Skip("local unix sockets unsupported on this platform")
	}
	if err := os.MkdirAll(filepath.Dir(socketPath), 0o755); err != nil {
		t.Fatalf("MkdirAll socket dir: %v", err)
	}
	staleListener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen stale unix socket: %v", err)
	}
	if err := staleListener.Close(); err != nil {
		t.Fatalf("close stale unix socket: %v", err)
	}

	server := startServeTestServer(t, request, authHandler, onboarding)

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		releaseServeTestPortForConfig(server.Config())
		errCh <- server.Serve(ctx)
	}()
	defer func() {
		cancel()
		if serveErr := <-errCh; !errors.Is(serveErr, context.Canceled) {
			t.Fatalf("Serve error = %v, want context canceled", serveErr)
		}
	}()

	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(socketPath); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("unix socket path did not appear: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	for {
		conn, dialErr := net.DialTimeout("unix", socketPath, 100*time.Millisecond)
		if dialErr == nil {
			_ = conn.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("unix socket path did not become dialable: %v", dialErr)
		}
		time.Sleep(10 * time.Millisecond)
	}

	var localRemote *client.Remote
	for {
		localRemote, err = client.DialConfiguredRemote(context.Background(), loadCfg)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("DialConfiguredRemote: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if localRemote.Identity().ServerID == "" {
		t.Fatal("expected configured remote identity")
	}
	_ = localRemote.Close()

	tcpRemote, err := client.DialRemoteURL(context.Background(), config.ServerRPCURL(loadCfg))
	if err != nil {
		t.Fatalf("DialRemoteURL TCP: %v", err)
	}
	_ = tcpRemote.Close()
}

func TestEmbeddedServeBackgroundExposesAttachEndpointUntilClose(t *testing.T) {
	workspace := newServeWorkspace(t)

	server, err := Start(context.Background(), Request{WorkspaceRoot: workspace, WorkspaceRootExplicit: true}, envAuthHandler{}, noopOnboarding)
	if err != nil {
		t.Fatalf("Start embedded: %v", err)
	}
	releaseServeTestPortForConfig(server.Config())
	if err := server.ServeBackground(); err != nil {
		_ = server.Close()
		t.Fatalf("ServeBackground: %v", err)
	}

	loadCfg, err := config.Load(workspace, config.LoadOptions{})
	if err != nil {
		_ = server.Close()
		t.Fatalf("config.Load: %v", err)
	}

	// An external client can attach and the handshake reports an identity stamped
	// with the persistence-root id, which is exactly what kent run validates.
	var remote *client.Remote
	deadline := time.Now().Add(5 * time.Second)
	for {
		remote, err = client.DialConfiguredRemote(context.Background(), loadCfg)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			_ = server.Close()
			t.Fatalf("DialConfiguredRemote: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	identity := remote.Identity()
	_ = remote.Close()
	if identity.ServerID == "" {
		t.Fatal("expected embedded server identity over the attach endpoint")
	}
	if want := config.PersistenceRootHash(loadCfg.PersistenceRoot); identity.PersistenceRootID != want {
		t.Fatalf("identity PersistenceRootID = %q, want %q", identity.PersistenceRootID, want)
	}

	if err := server.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// After Close the control endpoint is torn down: a fresh dial must fail.
	closedDeadline := time.Now().Add(2 * time.Second)
	for {
		closedRemote, dialErr := client.DialRemoteURL(context.Background(), config.ServerRPCURL(loadCfg))
		if dialErr != nil {
			break
		}
		_ = closedRemote.Close()
		if time.Now().After(closedDeadline) {
			t.Fatal("embedded attach endpoint still reachable after Close")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestServeDegradesToTCPWhenDerivedLocalSocketFails(t *testing.T) {
	workspace := newServeWorkspace(t)

	request := Request{WorkspaceRoot: workspace, WorkspaceRootExplicit: true}
	authHandler := envAuthHandler{}
	onboarding := noopOnboarding

	originalLocalSocketListener := localSocketListener
	localSocketListener = func(config.App) (net.Listener, func(), bool, error) {
		return nil, nil, false, errors.New("uds setup failed")
	}
	t.Cleanup(func() { localSocketListener = originalLocalSocketListener })

	server := startServeTestServer(t, request, authHandler, onboarding)

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		releaseServeTestPortForConfig(server.Config())
		errCh <- server.Serve(ctx)
	}()
	defer func() {
		cancel()
		if serveErr := <-errCh; !errors.Is(serveErr, context.Canceled) {
			t.Fatalf("Serve error = %v, want context canceled", serveErr)
		}
	}()

	loadCfg, err := config.Load(workspace, config.LoadOptions{})
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	healthURL := config.ServerHTTPBaseURL(loadCfg) + protocol.HealthPath
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp, err := http.Get(healthURL)
		if err == nil {
			_ = resp.Body.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("GET health: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}

	tcpRemote, err := client.DialRemoteURL(context.Background(), config.ServerRPCURL(loadCfg))
	if err != nil {
		t.Fatalf("DialRemoteURL TCP: %v", err)
	}
	_ = tcpRemote.Close()
}

func TestServeStartsUnauthenticatedAndReportsBootstrapReadiness(t *testing.T) {
	workspace := newServeWorkspace(t)

	request := Request{WorkspaceRoot: workspace, WorkspaceRootExplicit: true, AllowUnauthenticated: true}
	authHandler := envAuthHandler{lookupEnv: func(string) string { return "" }}
	onboarding := noopOnboarding

	server := startServeTestServer(t, request, authHandler, onboarding)

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		releaseServeTestPortForConfig(server.Config())
		errCh <- server.Serve(ctx)
	}()

	loadCfg, err := config.Load(workspace, config.LoadOptions{})
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	healthURL := config.ServerHTTPBaseURL(loadCfg) + protocol.HealthPath
	readyURL := config.ServerHTTPBaseURL(loadCfg) + protocol.ReadinessPath
	deadline := time.Now().Add(5 * time.Second)
	var healthResp *http.Response
	for {
		healthResp, err = http.Get(healthURL)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("GET health: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	defer func() { _ = healthResp.Body.Close() }()
	if healthResp.StatusCode != http.StatusOK {
		t.Fatalf("health status = %d, want 200", healthResp.StatusCode)
	}
	var healthBody map[string]any
	if err := json.NewDecoder(healthResp.Body).Decode(&healthBody); err != nil {
		t.Fatalf("decode health body: %v", err)
	}
	if healthBody["auth_ready"] != false {
		t.Fatalf("expected auth_ready=false health payload, got %+v", healthBody)
	}

	readyResp, err := http.Get(readyURL)
	if err != nil {
		t.Fatalf("GET ready: %v", err)
	}
	defer func() { _ = readyResp.Body.Close() }()
	if readyResp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("readiness status = %d, want 503", readyResp.StatusCode)
	}
	var readyBody map[string]any
	if err := json.NewDecoder(readyResp.Body).Decode(&readyBody); err != nil {
		t.Fatalf("decode ready body: %v", err)
	}
	if readyBody["ready"] != false || readyBody["auth_ready"] != false || readyBody["transport_ready"] != true {
		t.Fatalf("unexpected readiness payload: %+v", readyBody)
	}

	cancel()
	if serveErr := <-errCh; !errors.Is(serveErr, context.Canceled) {
		t.Fatalf("Serve error = %v, want context canceled", serveErr)
	}
}

func TestMissingConfigServeStartsBootstrapSurfaceBeforeAuthReady(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	t.Setenv("HOME", home)
	configureServeTestServerPort(t)
	registerEmbeddedWorkspace(t, workspace)

	server := startServeTestServer(t, Request{WorkspaceRoot: workspace, WorkspaceRootExplicit: true}, envAuthHandler{lookupEnv: func(string) string { return "" }}, nil)
	if server.Core != nil || server.deps == nil {
		t.Fatal("expected missing-config serve startup surface without configured core")
	}
	readiness, err := server.deps.ServerStatusClient().GetServerReadiness(context.Background(), serverapi.ServerReadinessRequest{})
	if err != nil {
		t.Fatalf("GetServerReadiness: %v", err)
	}
	if readiness.Ready || len(readiness.Causes) == 0 {
		t.Fatalf("readiness = %+v, want not ready with onboarding cause", readiness)
	}
	cause := readiness.Causes[0]
	if cause.Code != string(serverapi.ServerNotReadyOnboardingRequired) || cause.Severity != "error" || cause.Summary != nil || cause.NextAction != nil {
		t.Fatalf("unexpected onboarding readiness cause: %+v", cause)
	}
	if _, statErr := os.Stat(filepath.Join(home, config.ConfigDirName, "config.toml")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("settings file should remain absent before finalize, stat err=%v", statErr)
	}
}

func TestStartupControlSurfaceRejectsConfigThatAppearsBeforeRootLock(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	t.Setenv("HOME", home)
	configureServeTestServerPort(t)
	loadCfg, err := config.Load(workspace, config.LoadOptions{})
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	if _, _, err := config.WriteDefaultSettingsFileAt(loadCfg.Source.HomeSettingsPath); err != nil {
		t.Fatalf("write settings: %v", err)
	}

	_, _, err = buildStartupControlSurface(context.Background(), buildRequest(Request{WorkspaceRoot: workspace, WorkspaceRootExplicit: true}, envAuthHandler{}), true, envAuthHandler{}, Options{})
	if !errors.Is(err, errStartupControlSurfaceNotRequired) {
		t.Fatalf("buildStartupControlSurface error = %v, want not required", err)
	}
}

func TestServeOnboardingHandlerReceivesCapabilityFactsClient(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	t.Setenv("HOME", home)
	configureServeTestServerPort(t)

	receivedFacts := false
	onboarding := OnboardingHandler(func(ctx context.Context, req OnboardingRequest) (config.App, error) {
		if req.CapabilityFactsClient == nil {
			t.Fatal("capability facts client was not threaded into serve onboarding")
		}
		if _, err := req.CapabilityFactsClient.GetCapabilityFacts(ctx, serverapi.CapabilityFactsRequest{}); err != nil {
			t.Fatalf("GetCapabilityFacts: %v", err)
		}
		receivedFacts = true
		return req.Config, ErrOnboardingRequired
	})

	server := startServeTestServer(t, Request{WorkspaceRoot: workspace, WorkspaceRootExplicit: true}, envAuthHandler{}, onboarding)
	defer func() { _ = server.Close() }()
	if !receivedFacts {
		t.Fatal("expected onboarding handler to receive capability facts")
	}
}

func TestConfiguredRemoteGetsServerReadinessWhenAuthMissing(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	t.Setenv("HOME", home)
	writeServeSettings(t, home, `
model = "gpt-5"

[subagents.coder]
model = "coder-model"

[subagents.blocked]
agent_callable = false
model = "blocked-model"
`)

	request := Request{WorkspaceRoot: workspace, WorkspaceRootExplicit: true, AllowUnauthenticated: true}
	authHandler := envAuthHandler{lookupEnv: func(string) string { return "" }}
	onboarding := noopOnboarding
	registerServeWorkspace(t, workspace)

	server := startServeTestServer(t, request, authHandler, onboarding)

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		releaseServeTestPortForConfig(server.Config())
		errCh <- server.Serve(ctx)
	}()
	defer func() {
		cancel()
		if serveErr := <-errCh; !errors.Is(serveErr, context.Canceled) {
			t.Fatalf("Serve error = %v, want context canceled", serveErr)
		}
	}()

	loadCfg, err := config.Load(workspace, config.LoadOptions{})
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	healthURL := config.ServerHTTPBaseURL(loadCfg) + protocol.HealthPath
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp, err := http.Get(healthURL)
		if err == nil {
			_ = resp.Body.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("GET health: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}

	remote, err := client.DialConfiguredRemote(context.Background(), loadCfg)
	if err != nil {
		t.Fatalf("DialConfiguredRemote: %v", err)
	}
	defer func() { _ = remote.Close() }()

	readiness, err := remote.GetServerReadiness(context.Background(), serverapi.ServerReadinessRequest{})
	if err != nil {
		t.Fatalf("GetServerReadiness: %v", err)
	}
	if readiness.Ready {
		t.Fatalf("ready = true, want false: %+v", readiness)
	}
	if readiness.ServerID == "" || readiness.ProtocolVersion != protocol.Version || readiness.ServerVersion == "" {
		t.Fatalf("missing readiness identity fields: %+v", readiness)
	}
	if readiness.AuthReady || !readiness.AuthRequired {
		t.Fatalf("auth flags = ready:%t required:%t, want ready:false required:true", readiness.AuthReady, readiness.AuthRequired)
	}
	if readiness.Endpoint == "" {
		t.Fatalf("expected endpoint in readiness response: %+v", readiness)
	}
	if len(readiness.Causes) != 1 {
		t.Fatalf("cause count = %d, want 1: %+v", len(readiness.Causes), readiness.Causes)
	}
	assertReadinessRoles(t, readiness.SubagentRoles, []string{"default", "fast", "blocked", "coder"})
	cause := readiness.Causes[0]
	if cause.Code != "server_not_ready" || cause.Severity != "error" || cause.Summary != nil || cause.NextAction != nil {
		t.Fatalf("unexpected generic readiness cause: %+v", cause)
	}
}

func TestMissingConfigFinalizeActivationFailureIsTypedAndRetryConflicts(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	t.Setenv("HOME", home)
	configureServeTestServerPort(t)

	server := startServeTestServer(t, Request{WorkspaceRoot: workspace, WorkspaceRootExplicit: true}, envAuthHandler{}, nil)
	if server.Core != nil || server.deps == nil {
		t.Fatal("expected missing-config serve startup surface")
	}
	metadataBlocker := filepath.Join(server.cfg.PersistenceRoot, "db")
	if err := os.WriteFile(metadataBlocker, []byte("block metadata open"), 0o644); err != nil {
		t.Fatalf("write metadata blocker: %v", err)
	}

	_, err := server.deps.OnboardingFinalizeClient().FinalizeOnboarding(context.Background(), serverapi.OnboardingFinalizeRequest{})
	if !errors.Is(err, serverapi.ErrServerNotReadyActivationFailed) {
		t.Fatalf("finalize error = %v, want activation_failed", err)
	}
	var readyErr *serverapi.ServerNotReadyError
	if !errors.As(err, &readyErr) {
		t.Fatalf("finalize error = %T %v, want ServerNotReadyError", err, err)
	}
	details := readyErr.Details.(serverapi.ServerNotReadyDetails)
	if !details.OnboardingCompleted || details.SettingsPath == nil || *details.SettingsPath == "" || details.Diagnostic == nil || *details.Diagnostic == "" {
		t.Fatalf("activation details = %+v", details)
	}
	if _, statErr := os.Stat(filepath.Join(home, config.ConfigDirName, "config.toml")); statErr != nil {
		t.Fatalf("config should remain written after activation failure: %v", statErr)
	}
	if state := server.deps.ServerReadinessState(); state.Ready || state.Reason == nil || *state.Reason != serverapi.ServerNotReadyActivationFailed || state.Diagnostic == nil || *state.Diagnostic == "" {
		t.Fatalf("readiness = %+v, want activation_failed diagnostic", state)
	}
	readiness, statusErr := server.deps.ServerStatusClient().GetServerReadiness(context.Background(), serverapi.ServerReadinessRequest{})
	if statusErr != nil {
		t.Fatalf("GetServerReadiness after activation failure: %v", statusErr)
	}
	if readiness.Ready || len(readiness.Causes) == 0 || readiness.Causes[0].DiagnosticID == "" {
		t.Fatalf("readiness response after activation failure = %+v", readiness)
	}
	_, retryErr := server.deps.OnboardingFinalizeClient().FinalizeOnboarding(context.Background(), serverapi.OnboardingFinalizeRequest{})
	if !errors.Is(retryErr, serverapi.ErrOnboardingFinalizeConfigAlreadyExists) {
		t.Fatalf("retry error = %v, want config_already_exists", retryErr)
	}
}

type finalizeServiceFunc func(context.Context, serverapi.OnboardingFinalizeRequest) (serverapi.OnboardingFinalizeResponse, error)

func (f finalizeServiceFunc) FinalizeOnboarding(ctx context.Context, req serverapi.OnboardingFinalizeRequest) (serverapi.OnboardingFinalizeResponse, error) {
	return f(ctx, req)
}

func TestStartupFinalizeActivationUsesServerOwnedContext(t *testing.T) {
	requestCtx, cancel := context.WithCancel(context.Background())
	cancel()
	activationCtxCanceled := true
	service := startupFinalizeService{
		service: finalizeServiceFunc(func(context.Context, serverapi.OnboardingFinalizeRequest) (serverapi.OnboardingFinalizeResponse, error) {
			return serverapi.OnboardingFinalizeResponse{Completed: true, SettingsPath: "/tmp/config.toml"}, nil
		}),
		activationContext: context.Background(),
		activate: func(ctx context.Context, _ serverapi.OnboardingFinalizeResponse) error {
			activationCtxCanceled = ctx.Err() != nil
			return nil
		},
	}
	if _, err := service.FinalizeOnboarding(requestCtx, serverapi.OnboardingFinalizeRequest{}); err != nil {
		t.Fatalf("FinalizeOnboarding: %v", err)
	}
	if activationCtxCanceled {
		t.Fatal("activation used canceled request context")
	}
}

func writeServeSettings(t *testing.T, home string, contents string) {
	t.Helper()
	settingsDir := filepath.Join(home, config.ConfigDirName)
	if err := os.MkdirAll(settingsDir, 0o755); err != nil {
		t.Fatalf("create settings dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(settingsDir, "config.toml"), []byte(contents), 0o644); err != nil {
		t.Fatalf("write settings: %v", err)
	}
}

func assertReadinessRoles(t *testing.T, roles []serverapi.SubagentRoleSummary, want []string) {
	t.Helper()
	got := make([]string, 0, len(roles))
	for _, role := range roles {
		got = append(got, role.Name)
	}
	if len(got) != len(want) {
		t.Fatalf("subagent roles = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("subagent roles = %+v, want %+v", got, want)
		}
	}
}

func TestServeFailsWhenConfiguredPortIsOccupied(t *testing.T) {
	workspace := newServeWorkspace(t)
	request := Request{WorkspaceRoot: workspace, WorkspaceRootExplicit: true}
	authHandler := envAuthHandler{}
	onboarding := noopOnboarding
	server := startServeTestServer(t, request, authHandler, onboarding)
	loadCfg, err := config.Load(workspace, config.LoadOptions{})
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	releaseServeTestPortForConfig(loadCfg)
	listener, err := net.Listen("tcp", net.JoinHostPort(loadCfg.Settings.ServerHost, strconv.Itoa(loadCfg.Settings.ServerPort)))
	if err != nil {
		t.Fatalf("occupy configured port: %v", err)
	}
	defer func() { _ = listener.Close() }()
	if err := server.Serve(context.Background()); err == nil {
		t.Fatal("expected serve to fail when configured port is occupied")
	}
}
