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
	serverbootstrap "core/server/bootstrap"
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

type testAuthHandler struct {
	lookupEnv      func(string) string
	state          *auth.State
	interactCalled bool
}

func (h *testAuthHandler) WrapStore(base auth.Store) auth.Store {
	if h != nil && h.state != nil {
		return auth.NewMemoryStore(*h.state)
	}
	return authservice.WrapStoreWithEnvAPIKeyOverride(base, h.lookupEnv)
}

func (h *testAuthHandler) NeedsInteraction(req authservice.FlowInteractionRequest) bool {
	return !req.Gate.Ready
}

func (h *testAuthHandler) Interact(context.Context, authservice.FlowInteractionRequest) (authservice.FlowInteractionOutcome, error) {
	h.interactCalled = true
	return authservice.FlowInteractionOutcome{}, auth.ErrAuthNotConfigured
}

func readyEmbeddedAuthHandler() *testAuthHandler {
	state := auth.State{
		Scope: auth.ScopeGlobal,
		Method: auth.Method{
			Type:   auth.MethodAPIKey,
			APIKey: &auth.APIKeyMethod{Key: "in-memory-test-key"},
		},
		UpdatedAt: time.Now().UTC(),
	}
	return &testAuthHandler{state: &state}
}

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

func defaultEmbeddedOnboardingHandler(called *bool) EmbeddedOnboardingHandler {
	return func(_ context.Context, req EmbeddedOnboardingRequest) (config.App, error) {
		if called != nil {
			*called = true
		}
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
	}
}

func startReadyEmbeddedServer(t *testing.T, req serverbootstrap.Request) *EmbeddedServer {
	t.Helper()
	if req.LookupEnv == nil {
		req.LookupEnv = os.Getenv
	}
	server, err := StartEmbedded(context.Background(), req, EmbeddedStartHooks{
		Auth:       readyEmbeddedAuthHandler(),
		Onboarding: defaultEmbeddedOnboardingHandler(nil),
	})
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

func TestStartBuildsEmbeddedServerAndRunsOnboarding(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("KENT_OAUTH_ISSUER", "https://attacker.example")
	t.Setenv("KENT_OAUTH_CLIENT_ID", "client-test")

	workspace := t.TempDir()
	registerEmbeddedWorkspace(t, workspace)
	authHandler := readyEmbeddedAuthHandler()
	onboardingCalled := false
	onboarding := defaultEmbeddedOnboardingHandler(&onboardingCalled)

	server, err := StartEmbedded(context.Background(), serverbootstrap.Request{
		WorkspaceRoot: workspace,
		LookupEnv:     os.Getenv,
	}, EmbeddedStartHooks{Auth: authHandler, Onboarding: onboarding})
	if err != nil {
		t.Fatalf("start embedded server: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })
	generatedSkillsRoot := filepath.Join(home, config.ConfigDirName, ".generated", "skills")
	if entries, err := os.ReadDir(generatedSkillsRoot); err != nil {
		t.Fatalf("expected embedded startup to seed generated skills through bootstrap: %v", err)
	} else if len(entries) == 0 {
		t.Fatal("expected embedded startup to seed at least one generated skill")
	}

	if !onboardingCalled {
		t.Fatal("expected onboarding handler to run")
	}
	if got := server.OAuthOptions().Issuer; got != auth.DefaultOpenAIIssuer {
		t.Fatalf("oauth issuer = %q, want %q", got, auth.DefaultOpenAIIssuer)
	}
	if got := server.OAuthOptions().ClientID; got != "client-test" {
		t.Fatalf("oauth client id = %q", got)
	}
	wantContainerDir := filepath.Join(filepath.Join(server.Config().PersistenceRoot, "projects"), server.ProjectID(), "sessions")
	if server.ContainerDir() != wantContainerDir {
		t.Fatalf("container dir = %q, want %q", server.ContainerDir(), wantContainerDir)
	}
	if _, err := os.Stat(filepath.Join(server.ContainerDir())); err != nil {
		t.Fatalf("expected container dir to exist: %v", err)
	}
	if server.RunPromptClient() == nil {
		t.Fatal("expected run prompt client")
	}
}

func TestStartEmbeddedOnboardingReceivesCapabilityFactsClient(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	workspace := t.TempDir()
	registerEmbeddedWorkspace(t, workspace)

	seenFacts := false
	onboarding := EmbeddedOnboardingHandler(func(ctx context.Context, req EmbeddedOnboardingRequest) (config.App, error) {
		if req.CapabilityFactsClient == nil {
			t.Fatal("capability facts client was not threaded into embedded onboarding")
		}
		facts, err := req.CapabilityFactsClient.GetCapabilityFacts(ctx, serverapi.CapabilityFactsRequest{})
		if err != nil {
			t.Fatalf("GetCapabilityFacts: %v", err)
		}
		seenFacts = factsContainGeneratedSkillCandidate(facts)
		return defaultEmbeddedOnboardingHandler(nil)(ctx, req)
	})

	server, err := StartEmbedded(context.Background(), serverbootstrap.Request{
		WorkspaceRoot: workspace,
		LookupEnv:     os.Getenv,
	}, EmbeddedStartHooks{Auth: readyEmbeddedAuthHandler(), Onboarding: onboarding})
	if err != nil {
		t.Fatalf("start embedded server: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })
	if !seenFacts {
		t.Fatal("expected embedded generated skill facts before core startup")
	}
}

func TestStartEmbeddedMissingConfigExposesBootstrapSurface(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	configureServeTestServerPort(t)
	workspace := t.TempDir()
	registerEmbeddedWorkspace(t, workspace)

	server, err := StartEmbeddedWithOptions(context.Background(), serverbootstrap.Request{
		WorkspaceRoot:         workspace,
		WorkspaceRootExplicit: true,
		LookupEnv: func(key string) string {
			if key == "OPENAI_API_KEY" {
				return "in-memory-test-key"
			}
			return ""
		},
	}, EmbeddedStartHooks{
		Auth: readyEmbeddedAuthHandler(),
	}, Options{})
	if err != nil {
		t.Fatalf("StartEmbeddedWithOptions: %v", err)
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

	server := startReadyEmbeddedServer(t, serverbootstrap.Request{
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

func TestStartPropagatesAuthFailureBeforeOnboarding(t *testing.T) {
	workspace := newRegisteredEmbeddedWorkspace(t)
	authHandler := &testAuthHandler{lookupEnv: os.Getenv}
	onboardingCalled := false
	onboarding := EmbeddedOnboardingHandler(func(_ context.Context, req EmbeddedOnboardingRequest) (config.App, error) {
		onboardingCalled = true
		return req.Config, nil
	})

	_, err := StartEmbedded(context.Background(), serverbootstrap.Request{WorkspaceRoot: workspace, LookupEnv: os.Getenv}, EmbeddedStartHooks{Auth: authHandler, Onboarding: onboarding})
	if !errors.Is(err, auth.ErrAuthNotConfigured) {
		t.Fatalf("expected auth not configured, got %v", err)
	}
	if !authHandler.interactCalled {
		t.Fatal("expected auth handler interaction")
	}
	if onboardingCalled {
		t.Fatal("did not expect onboarding after auth failure")
	}
}

func TestSessionViewClientReadsDormantSessionByIDWithoutMutatingFiles(t *testing.T) {
	workspace := newRegisteredEmbeddedWorkspace(t)

	server := startReadyEmbeddedServer(t, serverbootstrap.Request{
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
