package startup

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"core/prompts"
	"core/server/auth"
	"core/server/authservice"
	serverbootstrap "core/server/bootstrap"
	"core/server/llm"
	"core/server/metadata"
	"core/server/session"
	"core/server/session/sessiontest"
	shelltool "core/server/tools/shell"
	"core/shared/clientui"
	"core/shared/config"
	"core/shared/serverapi"
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
		workspace,
		metadataStore.AuthoritativeSessionStoreOptions()...,
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
	generatedCalls := 0
	restoreGeneratedSync := serverbootstrap.SetGeneratedSyncForTest(func(ctx context.Context, opts prompts.GeneratedSyncOptions) (prompts.GeneratedSyncResult, error) {
		generatedCalls++
		return prompts.GeneratedSync(ctx, opts)
	})
	defer restoreGeneratedSync()
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
	if generatedCalls != 1 {
		t.Fatalf("generated sync calls = %d, want 1", generatedCalls)
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

func TestRunPromptClientRunsLoopbackThroughEmbeddedServer(t *testing.T) {
	workspace := newRegisteredEmbeddedWorkspace(t)

	responseServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if handleTestOpenAIInputTokenCount(w, r, 11) {
			return
		}
		if r.URL.Path != "/responses" {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		if got := strings.TrimSpace(r.Header.Get("Authorization")); got == "" {
			t.Fatal("expected authorization header")
		}
		writeTestOpenAICompletedResponseStream(w, "hello from embedded", 11, 7)
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

func TestRunPromptClientPublishesHeadlessSessionActivity(t *testing.T) {
	workspace := newRegisteredEmbeddedWorkspace(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	releaseResponse := make(chan struct{})
	responseServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if handleTestOpenAIInputTokenCount(w, r, 11) {
			return
		}
		if r.URL.Path != "/responses" {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		select {
		case <-releaseResponse:
		case <-r.Context().Done():
			return
		case <-ctx.Done():
			return
		}
		writeTestOpenAICompletedResponseStream(w, "hello from headless activity", 11, 7)
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

	store := createEmbeddedProjectSession(t, server, workspace)
	if err := store.SetName("headless activity"); err != nil {
		t.Fatalf("set session name: %v", err)
	}
	_ = openEmbeddedSessionByID(t, server, store.Meta().SessionID)

	responseCh := make(chan serverapi.RunPromptResponse, 1)
	errCh := make(chan error, 1)
	go func() {
		resp, err := server.RunPromptClient().RunPrompt(ctx, serverapi.RunPromptRequest{
			ClientRequestID:   "embedded-run-activity-1",
			SelectedSessionID: store.Meta().SessionID,
			Prompt:            "hello from user",
		}, nil)
		if err != nil {
			errCh <- err
			return
		}
		responseCh <- resp
	}()

	activity := server.SessionActivityClient()
	if activity == nil {
		t.Fatal("expected session activity client")
	}
	var sub serverapi.SessionActivitySubscription
	var err error
	for {
		sub, err = activity.SubscribeSessionActivity(ctx, serverapi.SessionActivitySubscribeRequest{SessionID: store.Meta().SessionID})
		if err == nil {
			break
		}
		select {
		case runErr := <-errCh:
			t.Fatalf("RunPrompt early failure: %v", runErr)
		case <-ctx.Done():
			t.Fatal("timed out waiting for headless session activity subscription")
		case <-time.After(10 * time.Millisecond):
		}
	}
	defer func() { _ = sub.Close() }()

	close(releaseResponse)
	evt, err := sub.Next(ctx)
	if err != nil {
		t.Fatalf("headless session activity Next: %v", err)
	}
	if evt.Kind == "" {
		t.Fatalf("expected a projected headless activity event, got %+v", evt)
	}

	select {
	case runErr := <-errCh:
		t.Fatalf("RunPrompt: %v", runErr)
	case resp := <-responseCh:
		if resp.Result != "hello from headless activity" {
			t.Fatalf("unexpected result: %+v", resp)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for headless run prompt response")
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
	if _, err := store.AppendRunStarted(session.RunRecord{RunID: "run-1", StepID: "step-1", StartedAt: time.Now().UTC().Add(-time.Minute)}); err != nil {
		t.Fatalf("append run start: %v", err)
	}

	sessionPath := filepath.Join(store.Dir(), "session.json")
	eventsPath := filepath.Join(store.Dir(), "events.jsonl")
	if _, err := os.Stat(sessionPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected session metadata file to be absent after 4B cutover, got err=%v", err)
	}
	beforeEvents, err := os.ReadFile(eventsPath)
	if err != nil {
		t.Fatalf("read events file before: %v", err)
	}

	resp, err := server.SessionViewClient().GetSessionMainView(context.Background(), serverapi.SessionMainViewRequest{SessionID: store.Meta().SessionID})
	if err != nil {
		t.Fatalf("get session main view: %v", err)
	}
	if resp.MainView.Session.SessionName != "incident triage" || resp.MainView.ActiveRun == nil || resp.MainView.ActiveRun.RunID != "run-1" {
		t.Fatalf("unexpected main view: %+v", resp.MainView)
	}

	if _, err := os.Stat(sessionPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected session metadata file to remain absent after dormant read, got err=%v", err)
	}
	afterEvents, err := os.ReadFile(eventsPath)
	if err != nil {
		t.Fatalf("read events file after: %v", err)
	}
	if string(beforeEvents) != string(afterEvents) {
		t.Fatalf("events file mutated during dormant read")
	}
}

func TestProjectViewClientListsCurrentProjectAndSessions(t *testing.T) {
	workspace := newRegisteredEmbeddedWorkspace(t)

	server := startReadyEmbeddedServer(t, serverbootstrap.Request{
		WorkspaceRoot: workspace,
	})

	first := createEmbeddedProjectSession(t, server, workspace)
	if err := first.SetName("first"); err != nil {
		t.Fatalf("persist first session meta: %v", err)
	}
	second := createEmbeddedProjectSession(t, server, workspace)
	if err := second.SetName("second"); err != nil {
		t.Fatalf("persist second session meta: %v", err)
	}

	projects, err := server.ProjectViewClient().ListProjects(context.Background(), serverapi.ProjectListRequest{})
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if len(projects.Projects) != 1 {
		t.Fatalf("expected one project, got %+v", projects)
	}
	if projects.Projects[0].ProjectID != server.ProjectID() {
		t.Fatalf("unexpected project id: %+v", projects.Projects[0])
	}
	if projects.Projects[0].SessionCount != 2 {
		t.Fatalf("unexpected project session count: %+v", projects.Projects[0])
	}

	sessions, err := server.ProjectViewClient().ListSessionsByProject(context.Background(), serverapi.SessionListByProjectRequest{ProjectID: server.ProjectID()})
	if err != nil {
		t.Fatalf("ListSessionsByProject: %v", err)
	}
	if len(sessions.Sessions) != 2 {
		t.Fatalf("expected two sessions, got %+v", sessions)
	}
	if sessions.Sessions[0].SessionID != second.Meta().SessionID {
		t.Fatalf("expected most recent session first, got %+v", sessions.Sessions)
	}
}

func TestProcessViewClientListsBackgroundProcessesWithRunOwnership(t *testing.T) {
	workspace := newRegisteredEmbeddedWorkspace(t)

	server := startReadyEmbeddedServer(t, serverbootstrap.Request{
		WorkspaceRoot: workspace,
	})
	server.Background().SetMinimumExecToBgTime(250 * time.Millisecond)

	result, err := server.Background().Start(context.Background(), shelltool.ExecRequest{
		Command:        []string{"sh", "-c", "printf 'process\n'; sleep 1"},
		DisplayCommand: "embedded-process",
		OwnerSessionID: "session-1",
		OwnerRunID:     "run-1",
		OwnerStepID:    "step-1",
		Workdir:        workspace,
		YieldTime:      250 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("start background process: %v", err)
	}
	if !result.Backgrounded {
		t.Fatal("expected backgrounded process")
	}

	resp, err := server.ProcessViewClient().ListProcesses(context.Background(), serverapi.ProcessListRequest{OwnerSessionID: "session-1", OwnerRunID: "run-1"})
	if err != nil {
		t.Fatalf("ListProcesses: %v", err)
	}
	if len(resp.Processes) != 1 {
		t.Fatalf("expected one process, got %+v", resp.Processes)
	}
	if resp.Processes[0].OwnerRunID != "run-1" || resp.Processes[0].OwnerStepID != "step-1" {
		t.Fatalf("unexpected process ownership: %+v", resp.Processes[0])
	}
}

func TestProcessOutputClientStreamsBackgroundProcessOutput(t *testing.T) {
	workspace := newRegisteredEmbeddedWorkspace(t)

	server := startReadyEmbeddedServer(t, serverbootstrap.Request{
		WorkspaceRoot: workspace,
	})
	server.Background().SetMinimumExecToBgTime(250 * time.Millisecond)

	result, err := server.Background().Start(context.Background(), shelltool.ExecRequest{
		Command:        []string{"sh", "-c", "printf 'first\\n'; sleep 0.4; printf 'second\\n'"},
		DisplayCommand: "embedded-process-output",
		OwnerSessionID: "session-1",
		OwnerRunID:     "run-1",
		OwnerStepID:    "step-1",
		Workdir:        workspace,
		YieldTime:      250 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("start background process: %v", err)
	}
	if !result.Backgrounded {
		t.Fatal("expected backgrounded process")
	}
	processResp, err := server.ProcessViewClient().GetProcess(context.Background(), serverapi.ProcessGetRequest{ProcessID: result.SessionID})
	if err != nil {
		t.Fatalf("GetProcess: %v", err)
	}
	if processResp.Process == nil || !processResp.Process.OutputAvailable || processResp.Process.OutputRetainedFromBytes != 0 || processResp.Process.OutputRetainedToBytes <= 0 {
		t.Fatalf("expected retained output metadata, got %+v", processResp.Process)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	sub, err := server.ProcessOutputClient().SubscribeProcessOutput(ctx, serverapi.ProcessOutputSubscribeRequest{ProcessID: result.SessionID})
	if err != nil {
		t.Fatalf("SubscribeProcessOutput: %v", err)
	}
	defer func() { _ = sub.Close() }()

	chunks := make([]clientui.ProcessOutputChunk, 0, 2)
	for {
		chunk, err := sub.Next(ctx)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		if chunk.ProcessID != result.SessionID {
			t.Fatalf("unexpected chunk process id: %+v", chunk)
		}
		if len(chunks) > 0 {
			previous := chunks[len(chunks)-1]
			if chunk.OffsetBytes != previous.NextOffsetBytes || chunk.NextOffsetBytes <= chunk.OffsetBytes {
				t.Fatalf("unexpected chunk offsets previous=%+v current=%+v", previous, chunk)
			}
		}
		chunks = append(chunks, chunk)
	}
	combined := strings.Builder{}
	for _, chunk := range chunks {
		combined.WriteString(chunk.Text)
	}
	combinedText := combined.String()
	firstIndex := strings.Index(combinedText, "first")
	secondIndex := strings.Index(combinedText, "second")
	if len(chunks) == 0 || firstIndex < 0 || secondIndex < firstIndex {
		t.Fatalf("expected streamed process output to contain first and second, chunks=%+v", chunks)
	}
}

type fakeEmbeddedClient struct{}

func (*fakeEmbeddedClient) Generate(context.Context, llm.Request) (llm.Response, error) {
	return llm.Response{}, nil
}

func (*fakeEmbeddedClient) ProviderCapabilities(context.Context) (llm.ProviderCapabilities, error) {
	return llm.ProviderCapabilities{ProviderID: "openai", SupportsResponsesAPI: true, IsOpenAIFirstParty: true}, nil
}
