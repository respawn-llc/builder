package app

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"core/internal/testharness/testsetup"
	"core/server/auth"
	"core/server/authservice"
	"core/server/runprompt"
	serverstartup "core/server/startup"
	askquestion "core/server/tools"
	"core/shared/config"
	"core/shared/protocol"
	"core/shared/sessioncontract"

	"golang.org/x/net/websocket"
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
	req.Config.Source.CreatedDefaultConfig = created
	req.Config.Source.SettingsPath = path
	req.Config.Source.SettingsFileExists = true
	return req.Config, nil
})

func waitForConfiguredRunPromptDaemon(t *testing.T, workspace string) {
	t.Helper()
	loadCfg := loadAppTestConfig(t, workspace, config.LoadOptions{})
	healthURL := config.ServerHTTPBaseURL(loadCfg) + protocol.HealthPath
	client := &http.Client{Timeout: 250 * time.Millisecond}
	testsetup.RequireUntil(t, time.Now().Add(5*time.Second), 10*time.Millisecond, func() bool {
		resp, err := client.Get(healthURL)
		if err == nil {
			_ = resp.Body.Close()
			return resp.StatusCode == http.StatusOK
		}
		return false
	}, "configured daemon did not become healthy at %s", healthURL)
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

func publishConfiguredRemoteForWorkspace(t *testing.T, workspace string, caps protocol.CapabilityFlags) func() {
	t.Helper()
	identity := protocol.ServerIdentity{
		ProtocolVersion: protocol.Version,
		ServerID:        "stale-daemon",
		PID:             222,
		Capabilities:    caps,
	}
	server := httptest.NewServer(websocket.Handler(func(ws *websocket.Conn) {
		defer func() { _ = ws.Close() }()
		var req protocol.Request
		if err := websocket.JSON.Receive(ws, &req); err != nil {
			return
		}
		if req.Method != protocol.MethodHandshake {
			_ = websocket.JSON.Send(ws, protocol.NewErrorResponse(req.ID, protocol.ErrCodeInvalidRequest, "handshake required"))
			return
		}
		if err := websocket.JSON.Send(ws, protocol.NewSuccessResponse(req.ID, protocol.HandshakeResponse{Identity: identity})); err != nil {
			return
		}
		for {
			if err := websocket.JSON.Receive(ws, &req); err != nil {
				return
			}
			_ = websocket.JSON.Send(ws, protocol.NewErrorResponse(req.ID, protocol.ErrCodeMethodNotFound, "method not found"))
		}
	}))
	host, port, err := net.SplitHostPort(strings.TrimPrefix(server.URL, "http://"))
	if err != nil {
		server.Close()
		t.Fatalf("SplitHostPort: %v", err)
	}
	t.Setenv("KENT_SERVER_HOST", host)
	t.Setenv("KENT_SERVER_PORT", port)
	return server.Close
}
