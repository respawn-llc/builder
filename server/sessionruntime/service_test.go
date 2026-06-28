package sessionruntime

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"core/server/llm"
	"core/server/metadata"
	"core/server/registry"
	runtimepkg "core/server/runtime"
	"core/server/session"
	"core/server/tools"
	"core/shared/clientui"
	"core/shared/config"
	"core/shared/serverapi"
	"core/shared/toolspec"
	"core/shared/transcript"
)

type sessionRuntimeTestLLMClient struct {
	responses []llm.Response
}

func (c *sessionRuntimeTestLLMClient) Generate(_ context.Context, _ llm.Request) (llm.Response, error) {
	if len(c.responses) == 0 {
		return llm.Response{}, nil
	}
	resp := c.responses[0]
	c.responses = c.responses[1:]
	return resp, nil
}

type blockingLLMClient struct {
	entered     chan struct{}
	enteredOnce sync.Once
	release     chan struct{}
}

func (c *blockingLLMClient) Generate(_ context.Context, _ llm.Request) (llm.Response, error) {
	c.enteredOnce.Do(func() { close(c.entered) })
	<-c.release
	return llm.Response{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: "done", Phase: llm.MessagePhaseFinal},
		Usage:     llm.Usage{WindowTokens: 200000},
	}, nil
}

type sessionRuntimeTestTool struct {
	name toolspec.ID
}

func (t sessionRuntimeTestTool) Call(_ context.Context, c tools.Call) (tools.Result, error) {
	out, _ := json.Marshal(map[string]string{"tool": string(t.name)})
	return tools.Result{CallID: c.ID, Name: c.Name, Output: out}, nil
}

type patchDetailCapture struct {
	mu    sync.Mutex
	value string
}

func (c *patchDetailCapture) Set(value string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.value = value
}

func (c *patchDetailCapture) Get() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.value
}

func startRegisteredActiveRun(t *testing.T, fixture sessionRuntimeFixture, reg *registry.RuntimeRegistry) func() {
	t.Helper()
	client := &blockingLLMClient{entered: make(chan struct{}), release: make(chan struct{})}
	engine, err := runtimepkg.New(fixture.store, client, tools.NewRegistry(), runtimepkg.Config{Model: "gpt-5"})
	if err != nil {
		t.Fatalf("runtime.New: %v", err)
	}
	claim, _, _ := reg.AcquireRuntimeClaim(fixture.store.Meta().SessionID, "")
	claim.Resolve(engine, nil, nil)
	t.Cleanup(func() { _ = engine.Close() })
	done := make(chan error, 1)
	go func() {
		_, err := engine.SubmitUserMessage(context.Background(), "run")
		done <- err
	}()
	select {
	case <-client.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for active run to start")
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			close(client.release)
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Error("timed out waiting for active run to finish")
			}
		})
	}
}

func TestAppendRecoveredWarningIfNeededPersistsOnce(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	warning := "generated warning"
	if err := fixture.store.EnsureDurable(); err != nil {
		t.Fatalf("EnsureDurable: %v", err)
	}
	fixture.service.WithGeneratedRecoveredWarning(warning)
	if err := fixture.service.appendRecoveredWarningIfNeeded(fixture.store); err != nil {
		t.Fatalf("append warning: %v", err)
	}
	if err := fixture.service.appendRecoveredWarningIfNeeded(fixture.store); err != nil {
		t.Fatalf("append duplicate warning: %v", err)
	}
	count := 0
	if err := fixture.store.WalkEvents(func(evt session.Event) error {
		if evt.Kind != "local_entry" {
			return nil
		}
		var entry recoveredWarningEntry
		if err := json.Unmarshal(evt.Payload, &entry); err != nil {
			return err
		}
		if entry.Role == "warning" && entry.Text == warning {
			count++
		}
		return nil
	}); err != nil {
		t.Fatalf("walk events: %v", err)
	}
	if count != 1 {
		t.Fatalf("warning count = %d, want 1", count)
	}
	if !fixture.store.Meta().GeneratedRecoveredWarningIssued {
		t.Fatal("expected generated recovered warning marker to be persisted")
	}
	reopened, err := session.OpenByID(fixture.config.PersistenceRoot, fixture.store.Meta().SessionID, fixture.metadata.AuthoritativeSessionStoreOptions()...)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	if !reopened.Meta().GeneratedRecoveredWarningIssued {
		t.Fatal("expected generated recovered warning marker to survive reopen")
	}
	if err := fixture.service.appendRecoveredWarningIfNeeded(reopened); err != nil {
		t.Fatalf("append warning after reopen: %v", err)
	}
	reopenedCount := 0
	if err := reopened.WalkEvents(func(evt session.Event) error {
		if evt.Kind != "local_entry" {
			return nil
		}
		var entry recoveredWarningEntry
		if err := json.Unmarshal(evt.Payload, &entry); err != nil {
			return err
		}
		if entry.Role == "warning" && entry.Text == warning {
			reopenedCount++
		}
		return nil
	}); err != nil {
		t.Fatalf("walk reopened events: %v", err)
	}
	if reopenedCount != 1 {
		t.Fatalf("reopened warning count = %d, want 1", reopenedCount)
	}
}

func TestAppendRecoveredWarningIfNeededIgnoresProviderError(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	fixture.service.WithGeneratedRecoveredWarningProvider(func() (string, bool, error) {
		return "", false, errors.New("recovered dir unreadable")
	})
	if err := fixture.service.appendRecoveredWarningIfNeeded(fixture.store); err != nil {
		t.Fatalf("expected warning lookup errors to be non-fatal, got %v", err)
	}
}

func TestSyncExecutionTargetPersistsReminderWithoutActiveRuntime(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)

	err := fixture.service.SyncExecutionTarget(context.Background(), fixture.store.Meta().SessionID, clientui.SessionExecutionTarget{
		WorkspaceRoot:    " /tmp/workspace ",
		EffectiveWorkdir: " /tmp/workspace ",
	}, &session.WorktreeReminderState{
		Mode:          session.WorktreeReminderModeExit,
		Branch:        " feature/worktree ",
		WorktreePath:  " /tmp/worktree-a ",
		WorkspaceRoot: " /tmp/workspace ",
		EffectiveCwd:  " /tmp/workspace ",
	})
	if err != nil {
		t.Fatalf("SyncExecutionTarget: %v", err)
	}

	resolved, err := fixture.service.resolveStore(context.Background(), fixture.store.Meta().SessionID)
	if err != nil {
		t.Fatalf("resolveStore: %v", err)
	}
	state := resolved.Meta().WorktreeReminder
	if state == nil {
		t.Fatal("expected persisted worktree reminder state")
	}
	if state.Mode != session.WorktreeReminderModeExit {
		t.Fatalf("mode = %q, want exit", state.Mode)
	}
	if state.Branch != "feature/worktree" {
		t.Fatalf("branch = %q, want feature/worktree", state.Branch)
	}
	if state.WorktreePath != "/tmp/worktree-a" {
		t.Fatalf("worktree path = %q, want /tmp/worktree-a", state.WorktreePath)
	}
	if state.EffectiveCwd != "/tmp/workspace" {
		t.Fatalf("effective cwd = %q, want /tmp/workspace", state.EffectiveCwd)
	}
}

func TestRuntimeRebindDoesNotAdvanceTranscriptWorkdirWhenLocalRebindFails(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	patchText := "*** Begin Patch\n*** Add File: probe.txt\n+hello\n*** End Patch\n"
	client := &sessionRuntimeTestLLMClient{responses: []llm.Response{
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "patching", Phase: llm.MessagePhaseCommentary},
			ToolCalls: []llm.ToolCall{{ID: "call-patch", Name: string(toolspec.ToolPatch), Input: json.RawMessage(`{"patch":` + strconv.Quote(patchText) + `}`)}},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "done", Phase: llm.MessagePhaseFinal},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
	}}
	var detail patchDetailCapture
	engine, err := runtimepkg.New(fixture.store, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolPatch, Handler: sessionRuntimeTestTool{name: toolspec.ToolPatch}}), runtimepkg.Config{
		Model:                "gpt-5",
		TranscriptWorkingDir: "/old-worktree",
		OnEvent: func(evt runtimepkg.Event) {
			if evt.Kind != runtimepkg.EventToolCallStarted || evt.ToolCall == nil {
				return
			}
			meta, ok := transcript.DecodeToolCallMeta(evt.ToolCall.Presentation)
			if ok {
				detail.Set(meta.PatchDetail)
			}
		},
	})
	if err != nil {
		t.Fatalf("runtime.New: %v", err)
	}
	defer func() { _ = engine.Close() }()
	rebindErr := runtimeRebindFunc(func(string) error { return errors.New("local rebind failed") }, engine)("/new-worktree")
	if rebindErr == nil || !strings.Contains(rebindErr.Error(), "local rebind failed") {
		t.Fatalf("runtimeRebindFunc error = %v, want local rebind failed", rebindErr)
	}
	if _, err := engine.SubmitUserMessage(context.Background(), "apply patch"); err != nil {
		t.Fatalf("SubmitUserMessage: %v", err)
	}
	gotDetail := detail.Get()
	if !strings.Contains(gotDetail, "/old-worktree/probe.txt") {
		t.Fatalf("expected patch detail to keep old workdir, got %q", gotDetail)
	}
	if strings.Contains(gotDetail, "/new-worktree/probe.txt") {
		t.Fatalf("did not expect failed rebind workdir in patch detail, got %q", gotDetail)
	}
}

func TestHasActiveRun(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	reg := registry.NewRuntimeRegistry()
	fixture.service.runtimes = reg
	if active, err := fixture.service.HasActiveRun(context.Background(), fixture.store.Meta().SessionID); err != nil || active {
		t.Fatalf("HasActiveRun before run = (%v, %v), want (false, nil)", active, err)
	}
	release := startRegisteredActiveRun(t, fixture, reg)
	defer release()
	active, err := fixture.service.HasActiveRun(context.Background(), fixture.store.Meta().SessionID)
	if err != nil {
		t.Fatalf("HasActiveRun: %v", err)
	}
	if !active {
		t.Fatal("HasActiveRun = false, want true while run active")
	}
	release()
	if active, err := fixture.service.HasActiveRun(context.Background(), fixture.store.Meta().SessionID); err != nil || active {
		t.Fatalf("HasActiveRun after run = (%v, %v), want (false, nil)", active, err)
	}
}

func TestResolveStoreFallsBackThroughMetadataAuthority(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	resolved, err := fixture.service.resolveStore(context.Background(), fixture.store.Meta().SessionID)
	if err != nil {
		t.Fatalf("resolveStore: %v", err)
	}
	if resolved.Meta().SessionID != fixture.store.Meta().SessionID {
		t.Fatalf("resolved session id = %q, want %q", resolved.Meta().SessionID, fixture.store.Meta().SessionID)
	}
}

func TestResolveStoreRejectsUnknownSession(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	_, err := fixture.service.resolveStore(context.Background(), "session-missing")
	if err == nil {
		t.Fatal("expected resolveStore to reject unknown session")
	}
}

func TestActivateSessionRuntimeRejectsPathLikeSessionID(t *testing.T) {
	svc := &Service{}
	_, err := svc.ActivateSessionRuntime(context.Background(), serverapi.SessionRuntimeActivateRequest{
		ClientRequestID: "req-1",
		SessionID:       "../session-1",
	})
	if !errors.Is(err, serverapi.ErrSessionIDNotSingle) {
		t.Fatalf("expected path-like session id rejection, got %v", err)
	}
}

func TestReleaseSessionRuntimeRejectsPathLikeSessionID(t *testing.T) {
	svc := &Service{}
	_, err := svc.ReleaseSessionRuntime(context.Background(), serverapi.SessionRuntimeReleaseRequest{
		ClientRequestID: "req-1",
		SessionID:       "sessions/workspace-a/session-1",
	})
	if !errors.Is(err, serverapi.ErrSessionIDNotSingle) {
		t.Fatalf("expected path-like session id rejection, got %v", err)
	}
}

type sessionRuntimeFixture struct {
	config   config.App
	metadata *metadata.Store
	store    *session.Store
	service  *Service
}

func newSessionRuntimeFixture(t *testing.T) sessionRuntimeFixture {
	t.Helper()
	home := t.TempDir()
	workspace := t.TempDir()
	t.Setenv("HOME", home)
	appCfg, err := config.Load(workspace, config.LoadOptions{})
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	metadataStore, err := metadata.Open(appCfg.PersistenceRoot)
	if err != nil {
		t.Fatalf("metadata.Open: %v", err)
	}
	t.Cleanup(func() { _ = metadataStore.Close() })
	binding, err := metadataStore.RegisterWorkspaceBinding(context.Background(), appCfg.WorkspaceRoot)
	if err != nil {
		t.Fatalf("RegisterWorkspaceBinding: %v", err)
	}
	projectSessionsDir := filepath.Join(filepath.Join(appCfg.PersistenceRoot, "projects"), binding.ProjectID, "sessions")
	store, err := session.Create(projectSessionsDir, filepath.Base(projectSessionsDir), appCfg.WorkspaceRoot, metadataStore.AuthoritativeSessionStoreOptions()...)
	if err != nil {
		t.Fatalf("session.Create: %v", err)
	}
	if err := store.SetName("session-a"); err != nil {
		t.Fatalf("SetName: %v", err)
	}
	service := NewService(appCfg.PersistenceRoot, metadataStore, nil, nil, nil, nil, nil, registry.NewSessionStoreRegistry(), metadataStore.AuthoritativeSessionStoreOptions()...)
	return sessionRuntimeFixture{config: appCfg, metadata: metadataStore, store: store, service: service}
}
