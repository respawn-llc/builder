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
	"core/server/runtimewire"
	"core/server/session"
	"core/server/tools"
	shelltool "core/server/tools/shell"
	"core/server/tools/shell/postprocess"
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
	claim, _, _ := reg.AcquireRuntimeClaim(fixture.store.Meta().SessionID, "test-owner")
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

func TestHasBlockingRuntimeActivity(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	reg := registry.NewRuntimeRegistry()
	fixture.service.runtimes = reg
	if active, err := fixture.service.HasBlockingRuntimeActivity(context.Background(), fixture.store.Meta().SessionID); err != nil || active {
		t.Fatalf("HasBlockingRuntimeActivity before run = (%v, %v), want (false, nil)", active, err)
	}
	release := startRegisteredActiveRun(t, fixture, reg)
	defer release()
	active, err := fixture.service.HasBlockingRuntimeActivity(context.Background(), fixture.store.Meta().SessionID)
	if err != nil {
		t.Fatalf("HasBlockingRuntimeActivity: %v", err)
	}
	if !active {
		t.Fatal("HasBlockingRuntimeActivity = false, want true while run active")
	}
	release()
	if active, err := fixture.service.HasBlockingRuntimeActivity(context.Background(), fixture.store.Meta().SessionID); err != nil || active {
		t.Fatalf("HasBlockingRuntimeActivity after run = (%v, %v), want (false, nil)", active, err)
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
		OwnerID:         "owner-a",
	})
	if !errors.Is(err, serverapi.ErrSessionIDNotSingle) {
		t.Fatalf("expected path-like session id rejection, got %v", err)
	}
}

func TestActivateSessionRuntimeRejectsMissingOwnerID(t *testing.T) {
	svc := &Service{}
	_, err := svc.ActivateSessionRuntime(context.Background(), serverapi.SessionRuntimeActivateRequest{
		ClientRequestID: "req-1",
		SessionID:       "session-1",
	})
	if !errors.Is(err, registry.ErrRuntimeOwnerIDRequired) {
		t.Fatalf("expected runtime owner id rejection, got %v", err)
	}
}

func TestServicePassesRuntimeClientFactoryIntoInteractiveRuntime(t *testing.T) {
	fixture := newSessionRuntimeFixtureWithRegistry(t)
	calls := 0
	factory := runtimewire.RuntimeClientFactoryFunc(func(_ context.Context, req runtimewire.RuntimeClientRequest) (llm.Client, error) {
		calls++
		if req.Purpose != runtimewire.RuntimeClientPurposeMain {
			t.Fatalf("factory purpose = %v, want main", req.Purpose)
		}
		return &sessionRuntimeTestLLMClient{responses: []llm.Response{{Assistant: llm.Message{Role: llm.RoleAssistant, Content: "ok", Phase: llm.MessagePhaseFinal}, Usage: llm.Usage{WindowTokens: 200000}}}}, nil
	})
	fixture.service = NewServiceWithOptions(
		fixture.config.PersistenceRoot,
		fixture.metadata,
		nil,
		nil,
		nil,
		nil,
		registry.NewRuntimeRegistry(),
		registry.NewSessionStoreRegistry(),
		ServiceOptions{RuntimeClientFactory: factory},
		fixture.metadata.AuthoritativeSessionStoreOptions()...,
	)

	_, err := fixture.service.ActivateSessionRuntime(context.Background(), serverapi.SessionRuntimeActivateRequest{
		ClientRequestID: "activate-factory",
		SessionID:       fixture.store.Meta().SessionID,
		OwnerID:         "owner",
		ActiveSettings: config.Settings{
			Model:              "gpt-5",
			ModelContextWindow: 200000,
			Reviewer:           config.ReviewerSettings{Frequency: "off"},
			Timeouts:           config.Timeouts{ModelRequestSeconds: 1},
			Shell:              config.ShellSettings{PostprocessingMode: config.ShellPostprocessingModeBuiltin},
		},
		EnabledToolIDs: []string{string(toolspec.ToolExecCommand)},
		Source:         config.SourceReport{Sources: map[string]string{}},
	})
	if err != nil {
		t.Fatalf("ActivateSessionRuntime: %v", err)
	}
	if calls != 1 {
		t.Fatalf("factory calls = %d, want 1", calls)
	}
	_, _ = fixture.service.ReleaseSessionRuntime(context.Background(), serverapi.SessionRuntimeReleaseRequest{
		ClientRequestID: "release-factory",
		SessionID:       fixture.store.Meta().SessionID,
		OwnerID:         "owner",
		DropOwner:       true,
		ClosePolicy:     serverapi.SessionRuntimeReleaseClosePolicyDetachOnly,
	})
}

func TestActivateSessionRuntimeUsesActiveShellPostprocessingWithSuppliedManager(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	bootstrapRunner, err := postprocess.NewRunner(postprocess.Settings{
		Mode: config.ShellPostprocessingModeNone,
	})
	if err != nil {
		t.Fatalf("new bootstrap shell postprocessor: %v", err)
	}
	background, err := shelltool.NewManager(
		shelltool.WithMinimumExecToBgTime(time.Second),
		shelltool.WithPostprocessor(bootstrapRunner),
	)
	if err != nil {
		t.Fatalf("new background shell manager: %v", err)
	}
	t.Cleanup(func() { _ = background.Close() })
	backgroundRouter := runtimewire.NewBackgroundEventRouter(
		background,
		16_000,
		shelltool.BackgroundOutputDefault,
	)
	runtimes := registry.NewRuntimeRegistry()
	client := &sessionRuntimeTestLLMClient{responses: []llm.Response{
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "running", Phase: llm.MessagePhaseCommentary},
			ToolCalls: []llm.ToolCall{{
				ID:    "call-active-shell",
				Name:  string(toolspec.ToolExecCommand),
				Input: json.RawMessage(`{"cmd":"printf '\\033[31mactive\\033[0m'; sleep 2","shell":"/bin/sh","login":false,"yield_time_ms":200}`),
			}},
			Usage: llm.Usage{WindowTokens: 200000},
		},
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "done", Phase: llm.MessagePhaseFinal},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
	}}
	fixture.service = NewServiceWithOptions(
		fixture.config.PersistenceRoot,
		fixture.metadata,
		nil,
		nil,
		background,
		backgroundRouter,
		runtimes,
		registry.NewSessionStoreRegistry(),
		ServiceOptions{
			RuntimeClientFactory: runtimewire.RuntimeClientFactoryFunc(func(context.Context, runtimewire.RuntimeClientRequest) (llm.Client, error) {
				return client, nil
			}),
		},
		fixture.metadata.AuthoritativeSessionStoreOptions()...,
	)
	sessionID := fixture.store.Meta().SessionID
	t.Cleanup(func() {
		_, _ = fixture.service.ReleaseSessionRuntime(context.Background(), serverapi.SessionRuntimeReleaseRequest{
			ClientRequestID: "release-active-shell",
			SessionID:       sessionID,
			OwnerID:         "interactive-owner",
			DropOwner:       true,
			ClosePolicy:     serverapi.SessionRuntimeReleaseClosePolicyDetachOnly,
		})
	})

	if fixture.service.background != background {
		t.Fatal("interactive runtime did not retain the supplied global shell manager")
	}
	_, err = fixture.service.ActivateSessionRuntime(context.Background(), serverapi.SessionRuntimeActivateRequest{
		ClientRequestID: "activate-active-shell",
		SessionID:       sessionID,
		OwnerID:         "interactive-owner",
		ActiveSettings: config.Settings{
			Model:                  "gpt-5",
			ModelContextWindow:     200000,
			MinimumExecToBgSeconds: 1,
			ShellOutputMaxChars:    16_000,
			Reviewer:               config.ReviewerSettings{Frequency: "off"},
			Timeouts:               config.Timeouts{ModelRequestSeconds: 1},
			Shell:                  config.ShellSettings{PostprocessingMode: config.ShellPostprocessingModeBuiltin},
		},
		EnabledToolIDs: []string{string(toolspec.ToolExecCommand)},
		Source:         config.SourceReport{Sources: map[string]string{}},
	})
	if err != nil {
		t.Fatalf("ActivateSessionRuntime: %v", err)
	}

	err = fixture.service.WithRuntimeEngine(context.Background(), sessionID, func(engine *runtimepkg.Engine) error {
		_, submitErr := engine.SubmitUserMessage(context.Background(), "run active shell")
		return submitErr
	})
	if err != nil {
		t.Fatalf("SubmitUserMessage through activated runtime: %v", err)
	}

	window, err := fixture.store.ReadRecentEvents(128)
	if err != nil {
		t.Fatalf("ReadRecentEvents: %v", err)
	}
	var toolResult string
	for _, event := range window.Events {
		if event.Kind != "message" {
			continue
		}
		var message llm.Message
		if err := json.Unmarshal(event.Payload, &message); err != nil {
			t.Fatalf("decode message event: %v", err)
		}
		if message.Role != llm.RoleTool || message.ToolCallID != "call-active-shell" {
			continue
		}
		if err := json.Unmarshal([]byte(message.Content), &toolResult); err != nil {
			t.Fatalf("decode exec_command result: %v", err)
		}
		break
	}
	if toolResult == "" {
		t.Fatal("activated runtime transcript missing exec_command result")
	}
	if !strings.Contains(toolResult, "active") {
		t.Fatalf("exec_command output = %q, want active output from request shell settings", toolResult)
	}
	if strings.Contains(toolResult, "\x1b[") {
		t.Fatalf("exec_command output retained ANSI despite active builtin postprocessing: %q", toolResult)
	}

	processes := background.List()
	if len(processes) != 1 {
		t.Fatalf("supplied manager process count = %d, want 1 active background process", len(processes))
	}
	if processes[0].OwnerSessionID != sessionID {
		t.Fatalf("background process owner session = %q, want activated session %q", processes[0].OwnerSessionID, sessionID)
	}
}

func TestReleaseSessionRuntimeRejectsPathLikeSessionID(t *testing.T) {
	svc := &Service{}
	_, err := svc.ReleaseSessionRuntime(context.Background(), serverapi.SessionRuntimeReleaseRequest{
		ClientRequestID: "req-1",
		SessionID:       "sessions/workspace-a/session-1",
		OwnerID:         "owner-a",
	})
	if !errors.Is(err, serverapi.ErrSessionIDNotSingle) {
		t.Fatalf("expected path-like session id rejection, got %v", err)
	}
}

func TestReleaseSessionRuntimeRejectsMissingOwnerID(t *testing.T) {
	svc := &Service{}
	_, err := svc.ReleaseSessionRuntime(context.Background(), serverapi.SessionRuntimeReleaseRequest{
		ClientRequestID: "req-1",
		SessionID:       "session-1",
		OnlyIfIdle:      true,
		DropOwner:       true,
	})
	if !errors.Is(err, registry.ErrRuntimeOwnerIDRequired) {
		t.Fatalf("expected runtime owner id rejection, got %v", err)
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

func newSessionRuntimeFixtureWithRegistry(t *testing.T) sessionRuntimeFixture {
	t.Helper()
	fixture := newSessionRuntimeFixture(t)
	fixture.service = NewService(fixture.config.PersistenceRoot, fixture.metadata, nil, nil, nil, nil, registry.NewRuntimeRegistry(), registry.NewSessionStoreRegistry(), fixture.metadata.AuthoritativeSessionStoreOptions()...)
	return fixture
}
