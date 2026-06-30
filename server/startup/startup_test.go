package startup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"core/server/auth"
	"core/server/authservice"
	corepkg "core/server/core"
	"core/server/metadata"
	"core/shared/config"
	"core/shared/serverapi"
)

func registerStartupWorkspace(t *testing.T, workspace string) {
	t.Helper()
	cfg, err := config.Load(workspace, config.LoadOptions{})
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	if _, err := metadata.RegisterBinding(context.Background(), cfg.PersistenceRoot, cfg.WorkspaceRoot); err != nil {
		t.Fatalf("RegisterBinding: %v", err)
	}
}

type stubAuthHandler struct {
	lookupEnv func(string) string
	needs     func(authservice.FlowInteractionRequest) bool
	interact  func(context.Context, authservice.FlowInteractionRequest) error
}

func (h stubAuthHandler) WrapStore(base auth.Store) auth.Store {
	return base
}

func (h stubAuthHandler) NeedsInteraction(req authservice.FlowInteractionRequest) bool {
	if h.needs == nil {
		return false
	}
	return h.needs(req)
}

func (h stubAuthHandler) Interact(ctx context.Context, req authservice.FlowInteractionRequest) (authservice.FlowInteractionOutcome, error) {
	if h.interact == nil {
		return authservice.FlowInteractionOutcome{}, nil
	}
	return authservice.FlowInteractionOutcome{}, h.interact(ctx, req)
}

func (h stubAuthHandler) LookupEnv(key string) string {
	if h.lookupEnv == nil {
		return ""
	}
	return h.lookupEnv(key)
}

type stubAuthState struct {
	cfg       config.App
	oauthOpts auth.OpenAIOAuthOptions
	mgr       *auth.Manager
}

func (s stubAuthState) Config() config.App                    { return s.cfg }
func (s stubAuthState) OAuthOptions() auth.OpenAIOAuthOptions { return s.oauthOpts }
func (s stubAuthState) AuthManager() *auth.Manager            { return s.mgr }

func TestEnsureReadyUsesAuthHandlerLookupEnv(t *testing.T) {
	mgr := auth.NewManager(auth.NewMemoryStore(auth.EmptyState()), nil, time.Now)
	sawInteraction := false
	err := EnsureReady(context.Background(), stubAuthState{
		cfg: config.App{Settings: config.Settings{
			Theme: "dark",
		}},
		oauthOpts: auth.OpenAIOAuthOptions{ClientID: "client-test"},
		mgr:       mgr,
	}, stubAuthHandler{
		lookupEnv: func(key string) string {
			if key == "OPENAI_API_KEY" {
				return "sk-env"
			}
			return ""
		},
		needs: func(req authservice.FlowInteractionRequest) bool {
			sawInteraction = true
			if !req.HasEnvAPIKey {
				t.Fatal("expected lookup env api key to be reflected in interaction request")
			}
			if req.Theme != "dark" {
				t.Fatalf("theme = %q, want dark", req.Theme)
			}
			return true
		},
		interact: func(context.Context, authservice.FlowInteractionRequest) error {
			return auth.ErrAuthNotConfigured
		},
	})
	if !errors.Is(err, auth.ErrAuthNotConfigured) {
		t.Fatalf("expected auth not configured, got %v", err)
	}
	if !sawInteraction {
		t.Fatal("expected ensure ready to invoke auth interaction")
	}
}

func TestEnsureReadyPromptsDuringExplicitReauthWhenStartupAuthIsOptional(t *testing.T) {
	mgr := auth.NewManager(auth.NewMemoryStore(auth.EmptyState()), nil, time.Now)
	called := false
	err := EnsureReady(context.Background(), stubAuthState{
		cfg: config.App{Settings: config.Settings{
			Theme:         "dark",
			OpenAIBaseURL: "http://127.0.0.1:8080/v1",
		}},
		mgr: mgr,
	}, stubAuthHandler{
		needs: func(req authservice.FlowInteractionRequest) bool {
			return !called && req.PromptOptional && !req.Gate.Ready
		},
		interact: func(context.Context, authservice.FlowInteractionRequest) error {
			called = true
			return nil
		},
	})
	if err != nil {
		t.Fatalf("ensure ready: %v", err)
	}
	if !called {
		t.Fatal("expected explicit reauth to prompt even when startup auth is optional")
	}
}

func TestEnsureReadyRequiresAuthManager(t *testing.T) {
	err := EnsureReady(context.Background(), stubAuthState{}, stubAuthHandler{})
	if err == nil || !errors.Is(err, errAuthManagerRequired) {
		t.Fatalf("expected missing auth manager error, got %v", err)
	}
}

func TestBuildRequestMapsStartupOptionsAndLookupEnv(t *testing.T) {
	handler := stubAuthHandler{
		lookupEnv: func(key string) string {
			if key == "KENT_LOOKUP_TEST" {
				return "lookup-value"
			}
			return ""
		},
	}
	req := buildRequest(Request{
		WorkspaceRoot:         "/tmp/workspace",
		WorkspaceRootExplicit: true,
		SessionID:             "session-123",
		Model:                 "gpt-5",
		ProviderOverride:      "openai",
		ThinkingLevel:         "high",
		Theme:                 "dark",
		ModelTimeoutSeconds:   45,
		Tools:                 "shell,patch",
		OpenAIBaseURL:         "http://example.test/v1",
		OpenAIBaseURLExplicit: true,
	}, handler)

	if req.WorkspaceRoot != "/tmp/workspace" || !req.WorkspaceRootExplicit {
		t.Fatalf("unexpected workspace mapping: %+v", req)
	}
	if req.SessionID != "session-123" {
		t.Fatalf("session id = %q, want session-123", req.SessionID)
	}
	if req.OpenAIBaseURL != "http://example.test/v1" || !req.OpenAIBaseURLExplicit {
		t.Fatalf("unexpected base url mapping: %+v", req)
	}
	if req.LoadOptions.Model != "gpt-5" || req.LoadOptions.ProviderOverride != "openai" || req.LoadOptions.ThinkingLevel != "high" {
		t.Fatalf("unexpected model/provider/thinking mapping: %+v", req.LoadOptions)
	}
	if req.LoadOptions.Theme != "dark" || req.LoadOptions.ModelTimeoutSeconds != 45 {
		t.Fatalf("unexpected theme/timeout mapping: %+v", req.LoadOptions)
	}
	if req.LoadOptions.Tools != "shell,patch" {
		t.Fatalf("tools = %q, want shell,patch", req.LoadOptions.Tools)
	}
	if got := req.LookupEnv("KENT_LOOKUP_TEST"); got != "lookup-value" {
		t.Fatalf("lookup env returned %q, want lookup-value", got)
	}
}

func TestLookupEnvFallsBackToProcessEnvWhenHandlerMissing(t *testing.T) {
	t.Setenv("KENT_LOOKUP_ENV_FALLBACK", "fallback-value")
	req := buildRequest(Request{}, nil)
	if got := req.LookupEnv("KENT_LOOKUP_ENV_FALLBACK"); got != "fallback-value" {
		t.Fatalf("lookup env fallback = %q, want fallback-value", got)
	}
}

type startupEnvAuthHandler struct{}

func (startupEnvAuthHandler) WrapStore(base auth.Store) auth.Store {
	return authservice.WrapStoreWithEnvAPIKeyOverride(base, startupTestAuthLookupEnv)
}

func (startupEnvAuthHandler) NeedsInteraction(req authservice.FlowInteractionRequest) bool {
	return req.AuthRequired && !req.Gate.Ready
}

func (startupEnvAuthHandler) Interact(context.Context, authservice.FlowInteractionRequest) (authservice.FlowInteractionOutcome, error) {
	return authservice.FlowInteractionOutcome{}, auth.ErrAuthNotConfigured
}

func (startupEnvAuthHandler) LookupEnv(key string) string {
	return startupTestAuthLookupEnv(key)
}

func startupTestAuthLookupEnv(key string) string {
	if key == "OPENAI_API_KEY" {
		return "in-memory-test-key"
	}
	return ""
}

var startupNoopOnboarding = OnboardingHandler(func(_ context.Context, req OnboardingRequest) (config.App, error) {
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

func TestStartWrapsCoreWithSameClientAssembly(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	t.Setenv("HOME", home)

	request := Request{WorkspaceRoot: workspace, WorkspaceRootExplicit: true}
	authHandler := startupEnvAuthHandler{}
	onboarding := startupNoopOnboarding
	registerStartupWorkspace(t, workspace)

	appCore, err := StartCore(context.Background(), request, authHandler, onboarding)
	if err != nil {
		t.Fatalf("StartCore: %v", err)
	}
	generatedSkillsRoot := filepath.Join(home, config.ConfigDirName, ".generated", "skills")
	if entries, err := os.ReadDir(generatedSkillsRoot); err != nil {
		t.Fatalf("expected StartCore to seed generated skills through bootstrap: %v", err)
	} else if len(entries) == 0 {
		t.Fatal("expected StartCore to seed at least one generated skill")
	}

	wrapped := &EmbeddedServer{Core: appCore}
	if wrapped.ProjectViewClient() != appCore.ProjectViewClient() {
		t.Fatal("expected embedded wrapper to expose core project client")
	}
	if wrapped.SessionViewClient() != appCore.SessionViewClient() {
		t.Fatal("expected embedded wrapper to expose core session client")
	}
	if wrapped.ProcessViewClient() != appCore.ProcessViewClient() {
		t.Fatal("expected embedded wrapper to expose core process client")
	}
	if wrapped.ProcessOutputClient() != appCore.ProcessOutputClient() {
		t.Fatal("expected embedded wrapper to expose core process output client")
	}
	if wrapped.RunPromptClient() != appCore.RunPromptClient() {
		t.Fatal("expected embedded wrapper to expose core run prompt client")
	}
	coreProjectID := appCore.ProjectID()
	coreProjects, err := appCore.ProjectViewClient().ListProjects(context.Background(), serverapi.ProjectListRequest{})
	if err != nil {
		t.Fatalf("core ListProjects: %v", err)
	}
	if err := appCore.Close(); err != nil {
		t.Fatalf("appCore.Close: %v", err)
	}

	started, err := Start(context.Background(), request, authHandler, onboarding)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = started.Close() }()
	if started.Core == nil {
		t.Fatal("expected embedded server to carry core")
	}
	if started.ProjectID() != coreProjectID {
		t.Fatalf("project id mismatch: started=%q core=%q", started.ProjectID(), coreProjectID)
	}
	startedProjects, err := started.ProjectViewClient().ListProjects(context.Background(), serverapi.ProjectListRequest{})
	if err != nil {
		t.Fatalf("started ListProjects: %v", err)
	}
	if len(coreProjects.Projects) != 1 || len(startedProjects.Projects) != 1 {
		t.Fatalf("unexpected project counts core=%d started=%d", len(coreProjects.Projects), len(startedProjects.Projects))
	}
	if coreProjects.Projects[0].ProjectID != startedProjects.Projects[0].ProjectID {
		t.Fatalf("project listing mismatch core=%+v started=%+v", coreProjects.Projects[0], startedProjects.Projects[0])
	}
}

func TestHeadlessHandlersStartCoreWithoutCLIFrontendDependencies(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	t.Setenv("HOME", home)

	authHandler, onboardingHandler := NewHeadlessHandlers(startupTestAuthLookupEnv)
	registerStartupWorkspace(t, workspace)
	appCore, err := StartCore(context.Background(), Request{WorkspaceRoot: workspace, WorkspaceRootExplicit: true}, authHandler, onboardingHandler)
	if err != nil {
		t.Fatalf("StartCore: %v", err)
	}
	defer func() { _ = appCore.Close() }()

	if appCore.Config().WorkspaceRoot != workspace {
		t.Fatalf("workspace root = %q, want %q", appCore.Config().WorkspaceRoot, workspace)
	}
	if !appCore.Config().Source.SettingsFileExists {
		t.Fatal("expected headless startup onboarding to ensure settings file exists")
	}
	if appCore.Config().Source.SettingsPath == "" {
		t.Fatal("expected settings path to be populated after headless onboarding")
	}
	if _, err := os.Stat(appCore.Config().Source.SettingsPath); err != nil {
		t.Fatalf("expected settings file to exist: %v", err)
	}
}

func TestStartCoreRejectsSecondOwnerForSamePersistenceRoot(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	t.Setenv("HOME", home)

	authHandler, onboardingHandler := NewHeadlessHandlers(startupTestAuthLookupEnv)
	registerStartupWorkspace(t, workspace)
	first, err := StartCore(context.Background(), Request{WorkspaceRoot: workspace, WorkspaceRootExplicit: true}, authHandler, onboardingHandler)
	if err != nil {
		t.Fatalf("StartCore first: %v", err)
	}
	defer func() { _ = first.Close() }()

	_, err = StartCore(context.Background(), Request{WorkspaceRoot: workspace, WorkspaceRootExplicit: true}, authHandler, onboardingHandler)
	if !errors.Is(err, corepkg.ErrPersistenceRootBusy) {
		t.Fatalf("StartCore second error = %v, want ErrPersistenceRootBusy", err)
	}
}

func TestHeadlessHandlersFailFastWithoutCredentials(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("OPENAI_API_KEY", "")

	authHandler, onboardingHandler := NewHeadlessHandlers(nil)
	_, err := StartCore(context.Background(), Request{WorkspaceRoot: workspace, WorkspaceRootExplicit: true}, authHandler, onboardingHandler)
	if !errors.Is(err, auth.ErrAuthNotConfigured) {
		t.Fatalf("expected auth not configured, got %v", err)
	}
}

func TestHeadlessHandlersAllowExplicitOpenAIBaseURLWithoutCredentials(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("OPENAI_API_KEY", "")

	authHandler, onboardingHandler := NewHeadlessHandlers(nil)
	registerStartupWorkspace(t, workspace)
	appCore, err := StartCore(context.Background(), Request{
		WorkspaceRoot:         workspace,
		WorkspaceRootExplicit: true,
		OpenAIBaseURL:         "http://127.0.0.1:8080/v1",
		OpenAIBaseURLExplicit: true,
	}, authHandler, onboardingHandler)
	if err != nil {
		t.Fatalf("StartCore: %v", err)
	}
	defer func() { _ = appCore.Close() }()

	if appCore.Config().Settings.OpenAIBaseURL != "http://127.0.0.1:8080/v1" {
		t.Fatalf("openai base url = %q", appCore.Config().Settings.OpenAIBaseURL)
	}
}
