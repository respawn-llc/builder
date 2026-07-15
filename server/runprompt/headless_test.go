package runprompt

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	modelstub "core/internal/testharness/pty/blackbox"
	"core/internal/testharness/testsetup"
	"core/server/auth"
	"core/server/launch"
	"core/server/llm"
	"core/server/registry"
	"core/server/requestmemo"
	"core/server/runlog"
	"core/server/runtime"
	"core/server/session"
	"core/server/session/sessiontest"
	"core/server/sessionlaunch"
	"core/server/sessionruntime"
	shelltool "core/server/tools/shell"
	"core/server/tools/shell/postprocess"
	"core/shared/clientui"
	"core/shared/config"
	"core/shared/serverapi"
	"core/shared/sessionenv"
	"core/shared/toolspec"
)

type stubRunPromptService struct {
	mu       sync.Mutex
	calls    int
	run      func(context.Context, serverapi.RunPromptRequest, serverapi.RunPromptProgressSink) (serverapi.RunPromptResponse, error)
	callHook func()
}

func TestRunPromptProgressFromRuntimeEventPublishesUserVisibleEvents(t *testing.T) {
	tests := []struct {
		name      string
		event     runtime.Event
		wantKind  serverapi.RunPromptProgressKind
		wantText  string
		wantPhase clientui.MessagePhase
	}{
		{
			name: "assistant commentary",
			event: runtime.Event{
				Kind: runtime.EventAssistantMessage,
				Message: llm.Message{
					Role:    llm.RoleAssistant,
					Phase:   llm.MessagePhaseCommentary,
					Content: "I am checking the runtime.",
				},
			},
			wantKind:  serverapi.RunPromptProgressKindAssistantMessage,
			wantText:  "I am checking the runtime.",
			wantPhase: clientui.MessagePhaseCommentary,
		},
		{
			name:     "compaction started",
			event:    runtime.Event{Kind: runtime.EventCompactionStarted},
			wantKind: serverapi.RunPromptProgressKindCompactionStarted,
		},
		{
			name: "compaction failed",
			event: runtime.Event{
				Kind:       runtime.EventCompactionFailed,
				Compaction: &runtime.CompactionStatus{Error: "provider rejected compaction"},
			},
			wantKind: serverapi.RunPromptProgressKindCompactionFailed,
			wantText: "provider rejected compaction",
		},
		{
			name: "steering accepted",
			event: runtime.Event{
				Kind: runtime.EventQueuedUserMessageStatus,
				QueuedUserMessageStatus: &runtime.QueuedUserMessageStatusEvent{
					Status:      runtime.QueuedUserMessageAccepted,
					RestoreText: "use the safer migration",
				},
			},
			wantKind: serverapi.RunPromptProgressKindSteeredMessage,
			wantText: "use the safer migration",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ok := RunPromptProgressFromRuntimeEvent(test.event)
			if !ok {
				t.Fatal("expected user-visible progress event")
			}
			if got.Kind != test.wantKind {
				t.Fatalf("kind = %q, want %q", got.Kind, test.wantKind)
			}
			switch test.wantKind {
			case serverapi.RunPromptProgressKindAssistantMessage:
				if got.AssistantMessage == nil {
					t.Fatal("assistant message payload is absent")
				}
				if got.AssistantMessage.Content != test.wantText || got.AssistantMessage.Phase != test.wantPhase {
					t.Fatalf("assistant message = %+v", got.AssistantMessage)
				}
			case serverapi.RunPromptProgressKindCompactionFailed:
				if got.Failure == nil || got.Failure.Error == nil || *got.Failure.Error != test.wantText {
					t.Fatalf("failure = %+v", got.Failure)
				}
			case serverapi.RunPromptProgressKindSteeredMessage:
				if got.SteeredMessage == nil || got.SteeredMessage.Content != test.wantText {
					t.Fatalf("steered message = %+v", got.SteeredMessage)
				}
			}
		})
	}
}

func TestRunPromptProgressFromRuntimeEventDropsOperationalSpam(t *testing.T) {
	for _, event := range []runtime.Event{
		{Kind: runtime.EventToolCallStarted},
		{Kind: runtime.EventToolCallCompleted},
		{Kind: runtime.EventReviewerCompleted},
		{
			Kind: runtime.EventQueuedUserMessageStatus,
			QueuedUserMessageStatus: &runtime.QueuedUserMessageStatusEvent{
				Status: runtime.QueuedUserMessageSubmitted,
			},
		},
	} {
		if got, ok := RunPromptProgressFromRuntimeEvent(event); ok {
			t.Fatalf("unexpected progress event for %q: %+v", event.Kind, got)
		}
	}
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

func newTestHeadlessSessionLaunch(cfg config.App, containerDir string, authManager *auth.Manager, persistences ...*sessiontest.Persistence) *sessionlaunch.Service {
	persistence := sessiontest.NewPersistence()
	if len(persistences) > 0 && persistences[0] != nil {
		persistence = persistences[0]
	}
	return sessionlaunch.NewService(launch.Planner{
		Config:       cfg,
		ContainerDir: containerDir,
		StoreOptions: persistence.Options(),
	}, registry.NewSessionStoreRegistry()).WithAuthStateReader(authManager)
}

func newTestHeadlessSessionRuntime(root string, authManager *auth.Manager, runtimes *registry.RuntimeRegistry) *sessionruntime.Service {
	return sessionruntime.NewService(root, nil, authManager, nil, nil, nil, runtimes, nil)
}

func TestHeadlessRuntimeWorkdirUsesInheritedWorktreeReminderCWD(t *testing.T) {
	persistence := sessiontest.NewPersistence()
	store, err := session.Create(t.TempDir(), "workspace", "/tmp/workspace", persistence.Options()...)
	if err != nil {
		t.Fatalf("session.Create: %v", err)
	}
	if err := store.SetWorktreeReminderState(&session.WorktreeReminderState{
		Mode: session.WorktreeReminderModeEnter,
		WorktreeContext: session.WorktreeContext{
			WorktreePath:  "/tmp/worktree",
			WorkspaceRoot: "/tmp/workspace",
			EffectiveCwd:  "/tmp/worktree/pkg",
		},
	}); err != nil {
		t.Fatalf("SetWorktreeReminderState: %v", err)
	}

	got := headlessRuntimeWorkdir(launch.SessionPlan{Store: store, WorkspaceRoot: "/tmp/workspace"})
	if got != "/tmp/worktree/pkg" {
		t.Fatalf("headless runtime workdir = %q, want /tmp/worktree/pkg", got)
	}
}

func TestHeadlessRuntimeWorkdirRejectsReminderWithoutEffectiveCWD(t *testing.T) {
	persistence := sessiontest.NewPersistence()
	store, err := session.Create(t.TempDir(), "workspace", "/tmp/workspace", persistence.Options()...)
	if err != nil {
		t.Fatalf("session.Create: %v", err)
	}
	err = store.SetWorktreeReminderState(&session.WorktreeReminderState{
		Mode: session.WorktreeReminderModeEnter,
		WorktreeContext: session.WorktreeContext{
			WorktreePath:  "/tmp/worktree",
			WorkspaceRoot: "/tmp/workspace",
		},
	})
	if err == nil {
		t.Fatal("expected missing effective cwd to be rejected")
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

func TestInProcessRunPromptClientUsesSelectedSessionContinuationContext(t *testing.T) {
	root := t.TempDir()
	containerDir := filepath.Join(root, "projects", "project-a", "sessions")
	persistence := sessiontest.NewPersistence()
	store, err := session.Create(containerDir, "workspace-a", "/tmp/workspace-a", persistence.Options()...)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := store.EnsureDurable(); err != nil {
		t.Fatalf("EnsureDurable: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if modelstub.HandleInputTokenCount(w, r, 1) {
			return
		}
		if r.URL.Path != "/responses" {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got == "" {
			t.Fatal("expected authorization header")
		}
		modelstub.WriteCompletedResponseStream(w, "from persisted continuation", 1, 1)
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
			Shell:         config.ShellSettings{PostprocessingMode: config.ShellPostprocessingModeBuiltin},
		},
	}
	runtimes := registry.NewRuntimeRegistry()
	client := NewInProcessRunPromptClient(HeadlessBootstrap{
		SessionLaunch:   newTestHeadlessSessionLaunch(cfg, containerDir, authManager, persistence),
		AuthManager:     authManager,
		RuntimeRegistry: runtimes,
		SessionRuntime:  newTestHeadlessSessionRuntime(root, authManager, runtimes),
	})

	var progresses []serverapi.RunPromptProgress
	response, err := client.RunPrompt(context.Background(), serverapi.RunPromptRequest{
		ClientRequestID:   "continuation-direct-1",
		SelectedSessionID: store.Meta().SessionID,
		Prompt:            "hello",
	}, serverapi.RunPromptProgressFunc(func(progress serverapi.RunPromptProgress) {
		progresses = append(progresses, progress)
	}))
	if err != nil {
		t.Fatalf("RunPrompt: %v", err)
	}
	if response.SessionID != store.Meta().SessionID {
		t.Fatalf("session id = %q, want %q", response.SessionID, store.Meta().SessionID)
	}
	if response.Result != "from persisted continuation" {
		t.Fatalf("result = %q, want from persisted continuation", response.Result)
	}
	for _, progress := range progresses {
		if progress.Kind == serverapi.RunPromptProgressKindSessionStarted {
			t.Fatalf("continued run announced a new session: %+v", progress)
		}
	}
	if got := store.Meta().Continuation; got == nil || got.OpenAIBaseURL != server.URL {
		t.Fatalf("expected persisted continuation preserved, got %+v", got)
	}
}

func TestInProcessRunPromptClientUsesActiveShellPostprocessorWithSuppliedBackgroundManager(t *testing.T) {
	root := t.TempDir()
	containerDir := filepath.Join(root, "projects", "project-a", "sessions")
	persistence := sessiontest.NewPersistence()
	store, err := session.Create(containerDir, "workspace-a", t.TempDir(), persistence.Options()...)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := store.EnsureDurable(); err != nil {
		t.Fatalf("EnsureDurable: %v", err)
	}

	bootstrapHook := writeRunPromptHook(t, `printf '{"processed":true,"replaced_output":"BOOTSTRAP"}'`)
	effectiveHook := writeRunPromptHook(t, fmt.Sprintf(
		`printf '{"processed":true,"replaced_output":"EFFECTIVE:%%s"}' "$%s"`,
		sessionenv.SessionIDEnv,
	))
	bootstrapRunner := mustRunPromptPostprocessor(t, config.ShellPostprocessingModeUser, &bootstrapHook)
	background, err := shelltool.NewManager(shelltool.WithPostprocessor(bootstrapRunner))
	if err != nil {
		t.Fatalf("new supplied background manager: %v", err)
	}
	t.Cleanup(func() { _ = background.Close() })

	toolOutput := make(chan string, 1)
	var responseCount int
	var responseMu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if modelstub.HandleInputTokenCount(w, r, 1) {
			return
		}
		if r.URL.Path != "/responses" {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}

		responseMu.Lock()
		responseCount++
		currentResponse := responseCount
		responseMu.Unlock()

		switch currentResponse {
		case 1:
			args, marshalErr := json.Marshal(map[string]any{
				"cmd":           "printf raw",
				"shell":         "/bin/sh",
				"login":         false,
				"yield_time_ms": 1000,
			})
			if marshalErr != nil {
				t.Fatalf("marshal exec_command arguments: %v", marshalErr)
			}
			writeRunPromptExecCommandResponse(w, args)
		case 2:
			defer r.Body.Close()
			var payload map[string]any
			if decodeErr := json.NewDecoder(r.Body).Decode(&payload); decodeErr != nil {
				t.Fatalf("decode second provider request: %v", decodeErr)
			}
			output, ok := findRunPromptFunctionCallOutput(payload)
			if !ok {
				t.Fatalf("second provider request has no function_call_output: %#v", payload)
			}
			toolOutput <- output
			modelstub.WriteCompletedResponseStream(w, "done", 1, 1)
		default:
			t.Fatalf("unexpected provider response request %d", currentResponse)
		}
	}))
	defer server.Close()

	authManager := auth.NewManager(auth.NewMemoryStore(auth.State{
		Method: auth.Method{Type: auth.MethodAPIKey, APIKey: &auth.APIKeyMethod{Key: "test-key"}},
	}), nil, time.Now)
	cfg := config.App{
		WorkspaceRoot:   store.Meta().WorkspaceRoot,
		PersistenceRoot: root,
		Settings: config.Settings{
			Model:               "gpt-5",
			OpenAIBaseURL:       server.URL,
			ShellOutputMaxChars: 16_000,
			EnabledTools:        map[toolspec.ID]bool{toolspec.ToolExecCommand: true},
			Shell: config.ShellSettings{
				PostprocessingMode: config.ShellPostprocessingModeUser,
				PostprocessHook:    &effectiveHook,
			},
		},
	}
	runtimes := registry.NewRuntimeRegistry()
	client := NewInProcessRunPromptClient(HeadlessBootstrap{
		SessionLaunch:   newTestHeadlessSessionLaunch(cfg, containerDir, authManager, persistence),
		AuthManager:     authManager,
		Background:      background,
		RuntimeRegistry: runtimes,
		SessionRuntime:  newTestHeadlessSessionRuntime(root, authManager, runtimes),
	})

	response, err := client.RunPrompt(context.Background(), serverapi.RunPromptRequest{
		ClientRequestID:   "active-shell-policy-1",
		SelectedSessionID: store.Meta().SessionID,
		Prompt:            "run the shell probe",
	}, nil)
	if err != nil {
		t.Fatalf("RunPrompt: %v", err)
	}
	if response.Result != "done" {
		t.Fatalf("result = %q, want done", response.Result)
	}
	select {
	case output := <-toolOutput:
		want := "EFFECTIVE:" + store.Meta().SessionID
		if output != want {
			t.Fatalf("exec_command output = %q, want %q", output, want)
		}
		if strings.Contains(output, "BOOTSTRAP") {
			t.Fatalf("exec_command used bootstrap shell postprocessing: %q", output)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for exec_command output")
	}
}

func writeRunPromptHook(t *testing.T, body string) string {
	t.Helper()
	script := "#!/bin/sh\n" + body + "\n"
	return testsetup.WriteExecutable(t, "postprocess-hook.sh", script)
}

func mustRunPromptPostprocessor(t *testing.T, mode config.ShellPostprocessingMode, hookPath *string) *postprocess.Runner {
	t.Helper()
	runner, err := postprocess.NewRunner(postprocess.Settings{
		Mode:     mode,
		HookPath: hookPath,
	})
	if err != nil {
		t.Fatalf("new run prompt postprocessor: %v", err)
	}
	return runner
}

func writeRunPromptExecCommandResponse(w http.ResponseWriter, args json.RawMessage) {
	writeSSEJSON(w, map[string]any{
		"type": "response.output_item.added",
		"item": map[string]any{
			"id":        "fc-runprompt-1",
			"type":      "function_call",
			"name":      string(toolspec.ToolExecCommand),
			"call_id":   "call-runprompt-1",
			"arguments": "",
		},
	})
	writeSSEJSON(w, map[string]any{
		"type":    "response.function_call_arguments.delta",
		"item_id": "fc-runprompt-1",
		"delta":   string(args),
	})
	writeSSEJSON(w, map[string]any{
		"type": "response.completed",
		"response": map[string]any{
			"usage": map[string]any{
				"input_tokens":  1,
				"output_tokens": 1,
				"total_tokens":  2,
			},
			"output": []any{
				map[string]any{
					"type":      "function_call",
					"id":        "fc-runprompt-1",
					"name":      string(toolspec.ToolExecCommand),
					"call_id":   "call-runprompt-1",
					"arguments": string(args),
				},
			},
		},
	})
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
}

func writeSSEJSON(w http.ResponseWriter, value any) {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(fmt.Sprintf("marshal run prompt SSE event: %v", err))
	}
	_, _ = fmt.Fprintf(w, "data: %s\n\n", encoded)
}

func findRunPromptFunctionCallOutput(payload map[string]any) (string, bool) {
	items, ok := payload["input"].([]any)
	if !ok {
		return "", false
	}
	for _, rawItem := range items {
		item, ok := rawItem.(map[string]any)
		if !ok || item["type"] != "function_call_output" {
			continue
		}
		output, ok := item["output"].(string)
		if !ok {
			return "", false
		}
		return output, true
	}
	return "", false
}

func TestInProcessRunPromptClientRejectsSelectedSessionWithGoal(t *testing.T) {
	root := t.TempDir()
	containerDir := filepath.Join(root, "projects", "project-a", "sessions")
	persistence := sessiontest.NewPersistence()
	store, err := session.Create(containerDir, "workspace-a", "/tmp/workspace-a", persistence.Options()...)
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
	client := NewInProcessRunPromptClient(HeadlessBootstrap{
		SessionLaunch: newTestHeadlessSessionLaunch(cfg, containerDir, nil, persistence),
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

func TestInProcessRunPromptClientUnregistersRuntimeAfterCompletion(t *testing.T) {
	root := t.TempDir()
	containerDir := filepath.Join(root, "projects", "project-a", "sessions")
	persistence := sessiontest.NewPersistence()
	store, err := session.Create(containerDir, "workspace-a", "/tmp/workspace-a", persistence.Options()...)
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
		if modelstub.HandleInputTokenCount(w, r, 1) {
			return
		}
		if r.URL.Path != "/responses" {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		startedOnce.Do(func() { close(started) })
		<-release
		modelstub.WriteCompletedResponseStream(w, "done", 1, 1)
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
			Shell:         config.ShellSettings{PostprocessingMode: config.ShellPostprocessingModeBuiltin},
		},
	}
	client := NewInProcessRunPromptClient(HeadlessBootstrap{
		SessionLaunch:   newTestHeadlessSessionLaunch(cfg, containerDir, authManager, persistence),
		AuthManager:     authManager,
		RuntimeRegistry: runtimes,
		SessionRuntime:  newTestHeadlessSessionRuntime(root, authManager, runtimes),
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
	activity, err := runtimes.RuntimeActivity(store.Meta().SessionID)
	if err != nil {
		t.Fatalf("RuntimeActivity: %v", err)
	}
	if !activity.ActiveForControl() {
		t.Fatal("expected headless runtime to record an active server-side run while request is in flight")
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
	persistence := sessiontest.NewPersistence()
	store, err := session.Create(containerDir, "workspace-a", "/tmp/workspace-a", persistence.Options()...)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := store.MarkModelDispatchLocked(session.LockedContract{Model: "locked-model", EnabledTools: []string{string(toolspec.ToolExecCommand)}}); err != nil {
		t.Fatalf("mark model dispatch locked: %v", err)
	}

	requestBodies := make(chan map[string]any, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if modelstub.HandleInputTokenCount(w, r, 1) {
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
		modelstub.WriteCompletedResponseStream(w, "locked response", 1, 1)
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
	runtimes := registry.NewRuntimeRegistry()
	client := NewInProcessRunPromptClient(HeadlessBootstrap{
		SessionLaunch:   newTestHeadlessSessionLaunch(cfg, containerDir, authManager, persistence),
		AuthManager:     authManager,
		RuntimeRegistry: runtimes,
		SessionRuntime:  newTestHeadlessSessionRuntime(root, authManager, runtimes),
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
	runLog, err := os.ReadFile(filepath.Join(store.Dir(), runlog.RunLogFileName))
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
