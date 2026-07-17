package startup

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	modelstub "core/internal/testharness/pty/blackbox"
	"core/server/auth"
	"core/server/authservice"
	"core/server/llm"
	"core/server/metadata"
	"core/server/session"
	"core/server/session/sessiontest"
	"core/shared/client"
	"core/shared/clientui"
	"core/shared/config"
	"core/shared/protocol"
	"core/shared/serverapi"
	"core/shared/sessioncontract"
)

func registerEmbeddedWorkspace(t *testing.T, workspace string) {
	t.Helper()
	cfg, err := config.Load(workspace, config.LoadOptions{})
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	if _, err := metadata.RegisterBinding(context.Background(), cfg.PersistenceRoot, cfg.WorkspaceRoot); err != nil {
		t.Fatalf("RegisterBinding: %v", err)
	}
}

func newRegisteredEmbeddedWorkspace(t *testing.T) string {
	t.Helper()
	workspace := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	registerEmbeddedWorkspace(t, workspace)
	return workspace
}

func startReadyEmbeddedServer(t *testing.T, req Request) *EmbeddedServer {
	t.Helper()
	server, err := StartWithOptions(context.Background(), req, startupEnvAuthHandler{}, startupNoopOnboarding, Options{})
	if err != nil {
		t.Fatalf("start embedded server: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })
	return server
}

func createEmbeddedProjectSession(t *testing.T, server *EmbeddedServer, workspace string) *session.Store {
	t.Helper()
	metadataStore, err := metadata.Open(server.Config().PersistenceRoot)
	if err != nil {
		t.Fatalf("metadata.Open: %v", err)
	}
	t.Cleanup(func() { _ = metadataStore.Close() })
	// Keep the metadata store alive for the lifetime of the session store so
	// persistence observer writes continue to succeed during the test.
	store, err := session.Create(
		filepath.Join(filepath.Join(server.Config().PersistenceRoot, "projects"), server.ProjectID(), "sessions"),
		filepath.Base(filepath.Clean(workspace)),
		workspace, sessioncontract.SessionCategoryMain, metadataStore.AuthoritativeSessionStoreOptions()...,
	)
	if err != nil {
		t.Fatalf("create project session: %v", err)
	}
	if err := metadataStore.ImportSessionSnapshot(context.Background(), session.PersistedStoreSnapshot{
		SessionDir: store.Dir(),
		Meta:       store.Meta(),
	}); err != nil {
		t.Fatalf("import project session snapshot: %v", err)
	}
	return store
}

func openEmbeddedSessionByID(t *testing.T, server *EmbeddedServer, sessionID string) *session.Store {
	t.Helper()
	metadataStore, err := metadata.Open(server.Config().PersistenceRoot)
	if err != nil {
		t.Fatalf("metadata.Open: %v", err)
	}
	t.Cleanup(func() { _ = metadataStore.Close() })
	store, err := session.OpenByID(server.Config().PersistenceRoot, sessionID, metadataStore.AuthoritativeSessionStoreOptions()...)
	if err != nil {
		t.Fatalf("open session by id: %v", err)
	}
	return store
}

func TestStartWithOptionsMissingConfigExposesBootstrapSurface(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	configureServeTestServerPort(t)
	workspace := t.TempDir()
	registerEmbeddedWorkspace(t, workspace)

	server, err := StartWithOptions(context.Background(), Request{
		WorkspaceRoot:         workspace,
		WorkspaceRootExplicit: true,
	}, startupEnvAuthHandler{}, nil, Options{})
	if err != nil {
		t.Fatalf("StartWithOptions: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })
	if server.Core != nil {
		t.Fatal("expected missing-config embedded startup to defer configured core construction")
	}
	settingsPath := filepath.Join(home, config.ConfigDirName, "config.toml")
	if _, statErr := os.Stat(settingsPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("settings file should remain absent before finalize, stat err=%v", statErr)
	}
	releaseServeTestPortForConfig(server.Config())
	if err := server.ServeBackground(); err != nil {
		t.Fatalf("ServeBackground: %v", err)
	}

	remote := dialEmbeddedRemote(t, server.Config())
	defer func() { _ = remote.Close() }()
	if identity := remote.Identity(); identity.ProtocolVersion != protocol.Version || !identity.Capabilities.OnboardingFinalize {
		t.Fatalf("unexpected bootstrap identity: %+v", identity)
	}
	_, err = remote.ListProjects(context.Background(), serverapi.ProjectListRequest{})
	if !errors.Is(err, serverapi.ErrServerNotReadyOnboardingRequired) {
		t.Fatalf("ListProjects error = %v, want onboarding_required", err)
	}
}

func dialEmbeddedRemote(t *testing.T, cfg config.App) *client.Remote {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var (
		remote *client.Remote
		err    error
	)
	for {
		remote, err = client.DialConfiguredRemote(context.Background(), cfg)
		if err == nil {
			return remote
		}
		if time.Now().After(deadline) {
			t.Fatalf("DialConfiguredRemote: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestRunPromptClientRunsLoopbackThroughEmbeddedServer(t *testing.T) {
	workspace := newRegisteredEmbeddedWorkspace(t)

	responseServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if modelstub.HandleInputTokenCount(w, r, 11) {
			return
		}
		if r.URL.Path != "/responses" {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		if got := strings.TrimSpace(r.Header.Get("Authorization")); got == "" {
			t.Fatal("expected authorization header")
		}
		modelstub.WriteCompletedResponseStream(w, "hello from embedded", 11, 7)
	}))
	defer responseServer.Close()

	server := startReadyEmbeddedServer(t, Request{
		WorkspaceRoot:         workspace,
		WorkspaceRootExplicit: true,
		OpenAIBaseURL:         responseServer.URL,
		OpenAIBaseURLExplicit: true,
		LoadOptions: config.LoadOptions{
			Model: "gpt-5",
		},
	})

	response, err := server.RunPromptClient().RunPrompt(context.Background(), serverapi.RunPromptRequest{
		ClientRequestID: "embedded-run-1",
		Intent:          serverapi.CreateNewSessionLaunchIntent(nil),
		Prompt:          "hello from user",
	}, nil)
	if err != nil {
		t.Fatalf("run prompt via embedded server: %v", err)
	}
	if strings.TrimSpace(response.SessionID) == "" {
		t.Fatal("expected session id")
	}
	if response.Result != "hello from embedded" {
		t.Fatalf("response result = %q", response.Result)
	}

	store := openEmbeddedSessionByID(t, server, response.SessionID)
	if store.Meta().Continuation == nil || store.Meta().Continuation.OpenAIBaseURL != responseServer.URL {
		t.Fatalf("unexpected continuation context: %+v", store.Meta().Continuation)
	}
	events, err := sessiontest.CollectEvents(store)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	var sawUser bool
	var sawAssistant bool
	for _, evt := range events {
		if evt.Kind != "message" {
			continue
		}
		var msg llm.Message
		if err := json.Unmarshal(evt.Payload, &msg); err != nil {
			t.Fatalf("unmarshal message: %v", err)
		}
		if msg.Role == llm.RoleUser && msg.Content == "hello from user" {
			sawUser = true
		}
		if msg.Role == llm.RoleAssistant && msg.Content == "hello from embedded" {
			sawAssistant = true
		}
	}
	if !sawUser || !sawAssistant {
		t.Fatalf("expected persisted user and assistant messages, sawUser=%t sawAssistant=%t", sawUser, sawAssistant)
	}
}

func TestStartWithOptionsPropagatesAuthFailureBeforeOnboarding(t *testing.T) {
	workspace := newRegisteredEmbeddedWorkspace(t)
	authInteractionCalled := false
	authHandler := stubAuthHandler{
		lookupEnv: os.Getenv,
		needs: func(req authservice.FlowInteractionRequest) bool {
			return !req.Gate.Ready
		},
		interact: func(context.Context, authservice.FlowInteractionRequest) error {
			authInteractionCalled = true
			return auth.ErrAuthNotConfigured
		},
	}
	onboardingCalled := false
	onboarding := OnboardingHandler(func(_ context.Context, req OnboardingRequest) (config.App, error) {
		onboardingCalled = true
		return req.Config, nil
	})

	_, err := StartWithOptions(context.Background(), Request{WorkspaceRoot: workspace}, authHandler, onboarding, Options{})
	if !errors.Is(err, auth.ErrAuthNotConfigured) {
		t.Fatalf("expected auth not configured, got %v", err)
	}
	if !authInteractionCalled {
		t.Fatal("expected auth handler interaction")
	}
	if onboardingCalled {
		t.Fatal("did not expect onboarding after auth failure")
	}
}

func TestSessionViewClientReadsDormantSessionByIDWithoutMutatingFiles(t *testing.T) {
	workspace := newRegisteredEmbeddedWorkspace(t)

	server := startReadyEmbeddedServer(t, Request{
		WorkspaceRoot: workspace,
	})

	store := createEmbeddedProjectSession(t, server, workspace)
	if err := store.SetName("incident triage"); err != nil {
		t.Fatalf("set name: %v", err)
	}
	if _, _, err := store.AppendEvent("step-1", "message", llm.Message{Role: llm.RoleUser, Content: "hello"}); err != nil {
		t.Fatalf("append user message: %v", err)
	}

	eventsPath := filepath.Join(store.Dir(), "events.jsonl")
	beforeEvents, err := os.ReadFile(eventsPath)
	if err != nil {
		t.Fatalf("read events file before: %v", err)
	}

	resp, err := server.SessionViewClient().GetSessionMainView(context.Background(), serverapi.SessionMainViewRequest{SessionID: store.Meta().SessionID})
	if err != nil {
		t.Fatalf("get session main view: %v", err)
	}
	if resp.MainView.Session.SessionName != "incident triage" || resp.MainView.Activity.State != clientui.RuntimeActivityUnavailable {
		t.Fatalf("unexpected main view: %+v", resp.MainView)
	}

	afterEvents, err := os.ReadFile(eventsPath)
	if err != nil {
		t.Fatalf("read events file after: %v", err)
	}
	if string(beforeEvents) != string(afterEvents) {
		t.Fatalf("events file mutated during dormant read")
	}
}
