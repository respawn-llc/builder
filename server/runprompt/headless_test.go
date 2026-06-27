package runprompt

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"core/server/auth"
	"core/server/launch"
	"core/server/registry"
	"core/server/requestmemo"
	"core/server/session"
	"core/server/sessionlaunch"
	"core/shared/clientui"
	"core/shared/config"
	"core/shared/serverapi"
	"core/shared/toolspec"
)

type stubRunPromptService struct {
	mu       sync.Mutex
	calls    int
	run      func(context.Context, serverapi.RunPromptRequest, serverapi.RunPromptProgressSink) (serverapi.RunPromptResponse, error)
	callHook func()
}

func (s *stubRunPromptService) RunPrompt(ctx context.Context, req serverapi.RunPromptRequest, progress serverapi.RunPromptProgressSink) (serverapi.RunPromptResponse, error) {
	s.mu.Lock()
	s.calls++
	hook := s.callHook
	run := s.run
	s.mu.Unlock()
	if hook != nil {
		hook()
	}
	if run == nil {
		return serverapi.RunPromptResponse{}, nil
	}
	return run(ctx, req, progress)
}

func (s *stubRunPromptService) CallCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func newTestHeadlessSessionLaunch(cfg config.App, containerDir string, authManager *auth.Manager) *sessionlaunch.Service {
	return sessionlaunch.NewService(launch.Planner{
		Config:       cfg,
		ContainerDir: containerDir,
	}, registry.NewSessionStoreRegistry()).WithAuthStateReader(authManager)
}

func TestHeadlessRuntimeWorkdirUsesInheritedWorktreeReminderCWD(t *testing.T) {
	store, err := session.Create(t.TempDir(), "workspace", "/tmp/workspace")
	if err != nil {
		t.Fatalf("session.Create: %v", err)
	}
	if err := store.SetWorktreeReminderState(&session.WorktreeReminderState{
		Mode:          session.WorktreeReminderModeEnter,
		WorktreePath:  "/tmp/worktree",
		WorkspaceRoot: "/tmp/workspace",
		EffectiveCwd:  "/tmp/worktree/pkg",
	}); err != nil {
		t.Fatalf("SetWorktreeReminderState: %v", err)
	}

	got := headlessRuntimeWorkdir(launch.SessionPlan{Store: store, WorkspaceRoot: "/tmp/workspace"})
	if got != "/tmp/worktree/pkg" {
		t.Fatalf("headless runtime workdir = %q, want /tmp/worktree/pkg", got)
	}
}

func TestHeadlessRuntimeWorkdirFallsBackToInheritedWorktreePath(t *testing.T) {
	store, err := session.Create(t.TempDir(), "workspace", "/tmp/workspace")
	if err != nil {
		t.Fatalf("session.Create: %v", err)
	}
	if err := store.SetWorktreeReminderState(&session.WorktreeReminderState{
		Mode:          session.WorktreeReminderModeEnter,
		WorktreePath:  "/tmp/worktree",
		WorkspaceRoot: "/tmp/workspace",
	}); err != nil {
		t.Fatalf("SetWorktreeReminderState: %v", err)
	}

	got := headlessRuntimeWorkdir(launch.SessionPlan{Store: store, WorkspaceRoot: "/tmp/workspace"})
	if got != "/tmp/worktree" {
		t.Fatalf("headless runtime workdir = %q, want /tmp/worktree", got)
	}
}

func TestMemoizingPromptServiceDedupesSuccessfulRetry(t *testing.T) {
	inner := &stubRunPromptService{run: func(_ context.Context, req serverapi.RunPromptRequest, _ serverapi.RunPromptProgressSink) (serverapi.RunPromptResponse, error) {
		return serverapi.RunPromptResponse{SessionID: req.SelectedSessionID, Result: "ok"}, nil
	}}
	service := &memoizingPromptService{
		inner: inner,
		runs:  requestmemo.New[runPromptMemoRequest, serverapi.RunPromptResponse](),
	}
	req := serverapi.RunPromptRequest{ClientRequestID: "req-1", SelectedSessionID: "session-1", Prompt: "hello"}

	first, err := service.RunPrompt(context.Background(), req, nil)
	if err != nil {
		t.Fatalf("RunPrompt first: %v", err)
	}
	second, err := service.RunPrompt(context.Background(), req, nil)
	if err != nil {
		t.Fatalf("RunPrompt replay: %v", err)
	}
	if first.Result != "ok" || second.Result != "ok" {
		t.Fatalf("responses = (%+v, %+v), want both ok", first, second)
	}
	if inner.CallCount() != 1 {
		t.Fatalf("inner call count = %d, want 1", inner.CallCount())
	}
}

func TestMemoizingPromptServiceRejectsClientRequestIDPayloadMismatch(t *testing.T) {
	inner := &stubRunPromptService{run: func(_ context.Context, req serverapi.RunPromptRequest, _ serverapi.RunPromptProgressSink) (serverapi.RunPromptResponse, error) {
		return serverapi.RunPromptResponse{SessionID: req.SelectedSessionID, Result: "ok"}, nil
	}}
	service := &memoizingPromptService{
		inner: inner,
		runs:  requestmemo.New[runPromptMemoRequest, serverapi.RunPromptResponse](),
	}
	first := serverapi.RunPromptRequest{ClientRequestID: "req-1", SelectedSessionID: "session-1", Prompt: "hello"}
	if _, err := service.RunPrompt(context.Background(), first, nil); err != nil {
		t.Fatalf("RunPrompt first: %v", err)
	}
	second := first
	second.Prompt = "different"
	if _, err := service.RunPrompt(context.Background(), second, nil); !errors.Is(err, requestmemo.ErrClientRequestIDReused) {
		t.Fatalf("RunPrompt mismatch error = %v, want request id payload mismatch", err)
	}
	if inner.CallCount() != 1 {
		t.Fatalf("inner call count = %d, want 1", inner.CallCount())
	}
}

func TestLoopbackRunPromptClientUsesSelectedSessionContinuationContext(t *testing.T) {
	root := t.TempDir()
	containerDir := filepath.Join(root, "projects", "project-a", "sessions")
	store, err := session.Create(containerDir, "workspace-a", "/tmp/workspace-a")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := store.EnsureDurable(); err != nil {
		t.Fatalf("EnsureDurable: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if handleTestOpenAIInputTokenCount(w, r, 1) {
			return
		}
		if r.URL.Path != "/responses" {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got == "" {
			t.Fatal("expected authorization header")
		}
		writeTestOpenAICompletedResponseStream(w, "from persisted continuation", 1, 1)
	}))
	defer server.Close()

	if err := store.SetContinuationContext(session.ContinuationContext{OpenAIBaseURL: server.URL}); err != nil {
		t.Fatalf("set continuation context: %v", err)
	}

	authManager := auth.NewManager(auth.NewMemoryStore(auth.State{
		Method: auth.Method{Type: auth.MethodAPIKey, APIKey: &auth.APIKeyMethod{Key: "test-key"}},
	}), nil, time.Now)

	cfg := config.App{
		WorkspaceRoot:   "/tmp/workspace-a",
		PersistenceRoot: root,
		Settings: config.Settings{
			Model:         "gpt-5",
			OpenAIBaseURL: "http://wrong.invalid",
		},
	}
	client := NewLoopbackRunPromptClient(HeadlessBootstrap{
		SessionLaunch: newTestHeadlessSessionLaunch(cfg, containerDir, authManager),
		AuthManager:   authManager,
	})

	response, err := client.RunPrompt(context.Background(), serverapi.RunPromptRequest{
		ClientRequestID:   "continuation-direct-1",
		SelectedSessionID: store.Meta().SessionID,
		Prompt:            "hello",
	}, nil)
	if err != nil {
		t.Fatalf("RunPrompt: %v", err)
	}
	if response.SessionID != store.Meta().SessionID {
		t.Fatalf("session id = %q, want %q", response.SessionID, store.Meta().SessionID)
	}
	if response.Result != "from persisted continuation" {
		t.Fatalf("result = %q, want from persisted continuation", response.Result)
	}
	if got := store.Meta().Continuation; got == nil || got.OpenAIBaseURL != server.URL {
		t.Fatalf("expected persisted continuation preserved, got %+v", got)
	}
}

func TestLoopbackRunPromptClientRejectsSelectedSessionWithGoal(t *testing.T) {
	root := t.TempDir()
	containerDir := filepath.Join(root, "projects", "project-a", "sessions")
	store, err := session.Create(containerDir, "workspace-a", "/tmp/workspace-a")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := store.SetGoal("ship feature", session.GoalActorUser); err != nil {
		t.Fatalf("set goal: %v", err)
	}
	if err := store.EnsureDurable(); err != nil {
		t.Fatalf("EnsureDurable: %v", err)
	}

	cfg := config.App{
		WorkspaceRoot:   "/tmp/workspace-a",
		PersistenceRoot: root,
		Settings:        config.Settings{Model: "gpt-5"},
	}
	client := NewLoopbackRunPromptClient(HeadlessBootstrap{
		SessionLaunch: newTestHeadlessSessionLaunch(cfg, containerDir, nil),
	})

	_, err = client.RunPrompt(context.Background(), serverapi.RunPromptRequest{
		ClientRequestID:   "goal-reject-1",
		SelectedSessionID: store.Meta().SessionID,
		Prompt:            "continue",
	}, nil)
	if !errors.Is(err, ErrHeadlessGoalSession) {
		t.Fatalf("RunPrompt error = %v, want ErrHeadlessGoalSession", err)
	}
}

func TestLoopbackRunPromptClientUnregistersRuntimeAfterCompletion(t *testing.T) {
	root := t.TempDir()
	containerDir := filepath.Join(root, "projects", "project-a", "sessions")
	store, err := session.Create(containerDir, "workspace-a", "/tmp/workspace-a")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := store.EnsureDurable(); err != nil {
		t.Fatalf("EnsureDurable: %v", err)
	}

	started := make(chan struct{})
	release := make(chan struct{})
	var startedOnce sync.Once
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if handleTestOpenAIInputTokenCount(w, r, 1) {
			return
		}
		if r.URL.Path != "/responses" {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		startedOnce.Do(func() { close(started) })
		<-release
		writeTestOpenAICompletedResponseStream(w, "done", 1, 1)
	}))
	defer server.Close()

	runtimes := registry.NewRuntimeRegistry()
	authManager := auth.NewManager(auth.NewMemoryStore(auth.State{
		Method: auth.Method{Type: auth.MethodAPIKey, APIKey: &auth.APIKeyMethod{Key: "test-key"}},
	}), nil, time.Now)
	cfg := config.App{
		WorkspaceRoot:   "/tmp/workspace-a",
		PersistenceRoot: root,
		Settings: config.Settings{
			Model:         "gpt-5",
			OpenAIBaseURL: server.URL,
		},
	}
	client := NewLoopbackRunPromptClient(HeadlessBootstrap{
		SessionLaunch:   newTestHeadlessSessionLaunch(cfg, containerDir, authManager),
		AuthManager:     authManager,
		RuntimeRegistry: runtimes,
	})

	done := make(chan error, 1)
	go func() {
		_, err := client.RunPrompt(context.Background(), serverapi.RunPromptRequest{
			ClientRequestID:   "runtime-cleanup-1",
			SelectedSessionID: store.Meta().SessionID,
			Prompt:            "hello",
		}, nil)
		done <- err
	}()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for /responses request")
	}
	if !runtimes.IsSessionRuntimeActive(store.Meta().SessionID) {
		t.Fatalf("expected run prompt runtime active while request is in flight")
	}
	status := runtimes.ExternalRuntimeStatus(store.Meta().SessionID)
	if status.State != clientui.ExternalRuntimeStateOwnerRunning || !status.QueueAccepting {
		t.Fatalf("external runtime status while headless request is in flight = %+v, want owner_running queue-accepting", status)
	}
	releaseOnce.Do(func() { close(release) })
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RunPrompt: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RunPrompt did not finish")
	}
	if runtimes.IsSessionRuntimeActive(store.Meta().SessionID) {
		t.Fatalf("expected run prompt runtime to unregister after completion")
	}
}

func TestHeadlessRunPromptOverridesRespectLockedModelContract(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	root := t.TempDir()
	containerDir := filepath.Join(root, "projects", "project-a", "sessions")
	store, err := session.Create(containerDir, "workspace-a", "/tmp/workspace-a")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := store.MarkModelDispatchLocked(session.LockedContract{Model: "locked-model", EnabledTools: []string{string(toolspec.ToolExecCommand)}}); err != nil {
		t.Fatalf("mark model dispatch locked: %v", err)
	}

	requestBodies := make(chan map[string]any, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if handleTestOpenAIInputTokenCount(w, r, 1) {
			return
		}
		if r.URL.Path != "/responses" {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		defer r.Body.Close()
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request payload: %v", err)
		}
		requestBodies <- payload
		writeTestOpenAICompletedResponseStream(w, "locked response", 1, 1)
	}))
	defer server.Close()

	authManager := auth.NewManager(auth.NewMemoryStore(auth.State{
		Method: auth.Method{Type: auth.MethodAPIKey, APIKey: &auth.APIKeyMethod{Key: "test-key"}},
	}), nil, time.Now)

	cfg, err := config.Load("/tmp/workspace-a", config.LoadOptions{})
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	cfg.PersistenceRoot = root
	cfg.Settings.Model = "base-model"
	cfg.Settings.OpenAIBaseURL = server.URL
	cfg.Settings.EnabledTools = map[toolspec.ID]bool{toolspec.ToolPatch: true}
	client := NewLoopbackRunPromptClient(HeadlessBootstrap{
		SessionLaunch: newTestHeadlessSessionLaunch(cfg, containerDir, authManager),
		AuthManager:   authManager,
	})

	response, err := client.RunPrompt(context.Background(), serverapi.RunPromptRequest{
		ClientRequestID:   "locked-direct-1",
		SelectedSessionID: store.Meta().SessionID,
		Prompt:            "hello",
		Overrides: serverapi.RunPromptOverrides{
			Model: "override-model",
			Tools: "patch",
		},
	}, nil)
	if err != nil {
		t.Fatalf("RunPrompt: %v", err)
	}
	if response.Result != "locked response" {
		t.Fatalf("result = %q, want locked response", response.Result)
	}
	runLog, err := os.ReadFile(filepath.Join(store.Dir(), RunLogFileName))
	if err != nil {
		t.Fatalf("read run log: %v", err)
	}
	if !strings.Contains(string(runLog), "model=locked-model") {
		t.Fatalf("expected run log to preserve locked model, got %q", string(runLog))
	}
	if strings.Contains(string(runLog), "model=override-model") {
		t.Fatalf("did not expect run log to use override model, got %q", string(runLog))
	}
	select {
	case payload := <-requestBodies:
		toolsPayload, ok := payload["tools"].([]any)
		if !ok || len(toolsPayload) != 1 {
			t.Fatalf("expected one locked tool in request payload, got %#v", payload["tools"])
		}
		toolPayload, ok := toolsPayload[0].(map[string]any)
		if !ok {
			t.Fatalf("unexpected tool payload: %#v", toolsPayload[0])
		}
		if got := toolPayload["name"]; got != string(toolspec.ToolExecCommand) {
			t.Fatalf("expected locked shell tool, got %#v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for provider request payload")
	}
}
