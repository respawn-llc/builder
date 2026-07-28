package sessionruntime

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"core/internal/testharness/testsetup"
	"core/server/llm"
	"core/server/metadata"
	runtimepkg "core/server/runtime"
	"core/server/runtimewire"
	"core/server/session"
	"core/server/session/sessiontest"
	shelltool "core/server/tools/shell"
	"core/server/tools/shell/postprocess"
	"core/shared/config"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/sessioncontract"
	"core/shared/textutil"
	"core/shared/toolspec"
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
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("done"), Phase: textutil.Value(llm.MessagePhaseFinal)},
		Usage:     llm.Usage{WindowTokens: 200000},
	}, nil
}

func TestAppendRecoveredWarningIfNeededPersistsOnce(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	warning := "generated warning"
	if err := fixture.store.EnsureDurable(); err != nil {
		t.Fatalf("EnsureDurable: %v", err)
	}
	fixture.api = NewAPI(fixture.metadata, nil, fixture.authority, APIOptions{
		RecoveredWarningProvider: func() (string, bool, error) { return warning, true, nil },
	})
	if err := appendRecoveredWarning(fixture.store, fixture.api.recoveredWarningProvider); err != nil {
		t.Fatalf("append warning: %v", err)
	}
	if err := appendRecoveredWarning(fixture.store, fixture.api.recoveredWarningProvider); err != nil {
		t.Fatalf("append duplicate warning: %v", err)
	}
	count := recoveredWarningEntryCount(t, fixture.store, warning)
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
	if err := appendRecoveredWarning(reopened, fixture.api.recoveredWarningProvider); err != nil {
		t.Fatalf("append warning after reopen: %v", err)
	}
	reopenedCount := recoveredWarningEntryCount(t, reopened, warning)
	if reopenedCount != 1 {
		t.Fatalf("reopened warning count = %d, want 1", reopenedCount)
	}
}

func TestAppendRecoveredWarningCommitsMarkerWithEventBeforeObserverFailure(t *testing.T) {
	persistence := sessiontest.NewPersistence()
	gate := sessiontest.NewPersistenceGate(persistence)
	projectSessionsDir := t.TempDir()
	workspaceRoot := t.TempDir()
	store, err := session.Create(
		projectSessionsDir,
		filepath.Base(projectSessionsDir),
		workspaceRoot,
		sessioncontract.SessionCategoryMain,
		session.WithPersistenceObserver(gate),
		session.WithPersistedSessionResolver(persistence),
	)
	if err != nil {
		t.Fatalf("create gated session: %v", err)
	}
	warning := "generated warning"
	observerErr := errors.New("warning metadata observer failed")
	gate.FailWhen(func(snapshot session.PersistedStoreSnapshot) bool {
		return snapshot.Meta.GeneratedRecoveredWarningIssued
	}, observerErr)

	if err := appendRecoveredWarning(
		store,
		func() (string, bool, error) { return warning, true, nil },
	); !errors.Is(err, observerErr) {
		t.Fatalf("append warning error = %v, want %v", err, observerErr)
	}
	if !store.Meta().GeneratedRecoveredWarningIssued {
		t.Fatal("committed warning append did not retain its metadata marker")
	}

	reopened, err := session.OpenByID(
		projectSessionsDir,
		store.Meta().SessionID,
		persistence.Options()...,
	)
	if err != nil {
		t.Fatalf("reopen committed warning: %v", err)
	}
	if !reopened.Meta().GeneratedRecoveredWarningIssued {
		t.Fatal("reopened warning lost its atomic metadata marker")
	}
	if err := appendRecoveredWarning(
		reopened,
		func() (string, bool, error) { return warning, true, nil },
	); err != nil {
		t.Fatalf("retry warning after reopen: %v", err)
	}
	if count := recoveredWarningEntryCount(t, reopened, warning); count != 1 {
		t.Fatalf("warning count after committed retry = %d, want 1", count)
	}
}

func TestAppendRecoveredWarningIfNeededSurfacesProviderError(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	providerErr := errors.New("recovered dir unreadable")
	fixture.api = NewAPI(fixture.metadata, nil, fixture.authority, APIOptions{
		RecoveredWarningProvider: func() (string, bool, error) {
			return "", false, providerErr
		},
	})
	if err := appendRecoveredWarning(fixture.store, fixture.api.recoveredWarningProvider); !errors.Is(err, providerErr) {
		t.Fatalf("warning lookup error = %v, want %v", err, providerErr)
	}
}

func TestActivateSessionRuntimeRejectsPathLikeSessionID(t *testing.T) {
	svc := &API{}
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
	svc := &API{}
	_, err := svc.ActivateSessionRuntime(context.Background(), serverapi.SessionRuntimeActivateRequest{
		ClientRequestID: "req-1",
		SessionID:       "session-1",
	})
	if !errors.Is(err, ErrRuntimeOwnerIDRequired) {
		t.Fatalf("expected runtime owner id rejection, got %v", err)
	}
}

func TestServicePassesRuntimeClientFactoryIntoInteractiveRuntime(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	calls := 0
	factory := runtimewire.RuntimeClientFactoryFunc(func(_ context.Context, req runtimewire.RuntimeClientRequest) (llm.Client, error) {
		calls++
		if req.Purpose != runtimewire.RuntimeClientPurposeMain {
			t.Fatalf("factory purpose = %v, want main", req.Purpose)
		}
		return &sessionRuntimeTestLLMClient{responses: []llm.Response{{Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("ok"), Phase: textutil.Value(llm.MessagePhaseFinal)}, Usage: llm.Usage{WindowTokens: 200000}}}}, nil
	})
	fixture.api = NewAPI(fixture.metadata, nil, fixture.authority, APIOptions{RuntimeClientFactory: factory})

	activation, err := fixture.api.ActivateSessionRuntime(context.Background(), serverapi.SessionRuntimeActivateRequest{
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
	_, _ = fixture.api.ReleaseSessionRuntime(context.Background(), serverapi.SessionRuntimeReleaseRequest{
		ClientRequestID: "release-factory",
		Attachment:      activation.Attachment,
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
	authority := NewAuthority(AuthorityOptions{
		PersistenceRoot: fixture.config.PersistenceRoot,
		Background:      background,
		StoreOptions:    fixture.metadata.AuthoritativeSessionStoreOptions(),
	})
	t.Cleanup(func() {
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close active-shell runtime authority: %v", err)
		}
	})
	client := &sessionRuntimeTestLLMClient{responses: []llm.Response{
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("running"), Phase: textutil.Value(llm.MessagePhaseCommentary)},
			ToolCalls: []llm.ToolCall{{
				ID:    "call-active-shell",
				Name:  string(toolspec.ToolExecCommand),
				Input: json.RawMessage(`{"cmd":"printf '\\033[31mactive\\033[0m'; sleep 2","shell":"/bin/sh","login":false,"yield_time_ms":200}`),
			}},
			Usage: llm.Usage{WindowTokens: 200000},
		},
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("done"), Phase: textutil.Value(llm.MessagePhaseFinal)},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
	}}
	fixture.api = NewAPI(fixture.metadata, nil, authority, APIOptions{
		RuntimeClientFactory: runtimewire.RuntimeClientFactoryFunc(func(context.Context, runtimewire.RuntimeClientRequest) (llm.Client, error) {
			return client, nil
		}),
	})
	sessionID := fixture.store.Meta().SessionID
	var attachment serverapi.SessionRuntimeAttachment
	t.Cleanup(func() {
		if attachment.Validate() != nil {
			return
		}
		_, _ = fixture.api.ReleaseSessionRuntime(context.Background(), serverapi.SessionRuntimeReleaseRequest{
			ClientRequestID: "release-active-shell",
			Attachment:      attachment,
			OwnerID:         "interactive-owner",
			DropOwner:       true,
		})
	})

	activation, err := fixture.api.ActivateSessionRuntime(context.Background(), serverapi.SessionRuntimeActivateRequest{
		ClientRequestID: "activate-active-shell",
		SessionID:       sessionID,
		OwnerID:         "interactive-owner",
		ActiveSettings: config.Settings{
			Model:                  "gpt-5",
			ThinkingLevel:          "medium",
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
	attachment = activation.Attachment

	id, err := runtimeids.ParseSessionID(sessionID)
	if err != nil {
		t.Fatalf("ParseSessionID: %v", err)
	}
	err = authority.WithCurrentRuntime(context.Background(), id, func(_ context.Context, engine *runtimepkg.Engine) error {
		_, submitErr := engine.SubmitUserMessage(context.Background(), "run active shell")
		return submitErr
	})
	if err != nil {
		t.Fatalf("SubmitUserMessage through activated runtime: %v", err)
	}

	eventLog, err := fixture.store.MaterializeEventLog()
	if err != nil {
		t.Fatalf("MaterializeEventLog: %v", err)
	}
	window, err := eventLog.ReadRecentRecords(128)
	if err != nil {
		t.Fatalf("ReadRecentRecords: %v", err)
	}
	var toolResult string
	for _, record := range window.Records {
		payload, payloadErr := record.Payload()
		if payloadErr != nil {
			t.Fatalf("read event payload: %v", payloadErr)
		}
		message, ok := payload.(session.MessageRecord)
		if !ok {
			continue
		}
		if message.Role != session.MessageRoleTool ||
			message.ToolCallID == nil || *message.ToolCallID != "call-active-shell" ||
			message.Content == nil {
			continue
		}
		if err := json.Unmarshal([]byte(*message.Content), &toolResult); err != nil {
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
	svc := &API{}
	_, err := svc.ReleaseSessionRuntime(context.Background(), serverapi.SessionRuntimeReleaseRequest{
		ClientRequestID: "req-1",
		Attachment: serverapi.SessionRuntimeAttachment{
			SessionID:  "sessions/workspace-a/session-1",
			Generation: 1,
		},
		OwnerID: "owner-a",
	})
	if !errors.Is(err, serverapi.ErrSessionIDNotSingle) {
		t.Fatalf("expected path-like session id rejection, got %v", err)
	}
}

func TestReleaseSessionRuntimeRejectsMissingOwnerID(t *testing.T) {
	svc := &API{}
	_, err := svc.ReleaseSessionRuntime(context.Background(), serverapi.SessionRuntimeReleaseRequest{
		ClientRequestID: "req-1",
		Attachment: serverapi.SessionRuntimeAttachment{
			SessionID:  "session-1",
			Generation: 1,
		},
		DropOwner: true,
	})
	if !errors.Is(err, ErrRuntimeOwnerIDRequired) {
		t.Fatalf("expected runtime owner id rejection, got %v", err)
	}
}

type sessionRuntimeFixture struct {
	config    config.App
	metadata  *metadata.Store
	store     *session.Store
	api       *API
	authority *Authority
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
	metadataStore := testsetup.OpenStore(t, appCfg.PersistenceRoot)
	binding, err := metadataStore.RegisterWorkspaceBinding(context.Background(), appCfg.WorkspaceRoot)
	if err != nil {
		t.Fatalf("RegisterWorkspaceBinding: %v", err)
	}
	projectSessionsDir := filepath.Join(filepath.Join(appCfg.PersistenceRoot, "projects"), binding.ProjectID, "sessions")
	store, err := session.Create(projectSessionsDir, filepath.Base(projectSessionsDir), appCfg.WorkspaceRoot, sessioncontract.SessionCategoryMain, metadataStore.AuthoritativeSessionStoreOptions()...)
	if err != nil {
		t.Fatalf("session.Create: %v", err)
	}
	if err := store.SetName("session-a"); err != nil {
		t.Fatalf("SetName: %v", err)
	}
	authority := NewAuthority(AuthorityOptions{
		PersistenceRoot: appCfg.PersistenceRoot,
		StoreOptions:    metadataStore.AuthoritativeSessionStoreOptions(),
	})
	t.Cleanup(func() {
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close runtime authority: %v", err)
		}
	})
	api := NewAPI(metadataStore, nil, authority, APIOptions{})
	return sessionRuntimeFixture{config: appCfg, metadata: metadataStore, store: store, api: api, authority: authority}
}
