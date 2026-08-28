package sessionruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
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
	"core/server/tools"
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
	mu        sync.Mutex
	requests  []llm.Request
	final     chan struct{}
	finalOnce sync.Once
}

func (c *sessionRuntimeTestLLMClient) Generate(_ context.Context, request llm.Request) (llm.Response, error) {
	c.mu.Lock()
	c.requests = append(c.requests, llm.Request{Items: llm.CloneResponseItems(request.Items)})
	if len(c.responses) == 0 {
		c.mu.Unlock()
		return llm.Response{}, nil
	}
	resp := c.responses[0]
	c.responses = c.responses[1:]
	c.mu.Unlock()
	if resp.Assistant.Phase != nil && *resp.Assistant.Phase == llm.MessagePhaseFinal && c.final != nil {
		c.finalOnce.Do(func() { close(c.final) })
	}
	return resp, nil
}

func (c *sessionRuntimeTestLLMClient) requestSnapshot() []llm.Request {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]llm.Request, len(c.requests))
	for index := range c.requests {
		out[index] = c.requests[index]
		out[index].Items = llm.CloneResponseItems(c.requests[index].Items)
	}
	return out
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
	fixture.api = NewAPI(fixture.metadata, fixture.authority, APIOptions{
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
	fixture.api = NewAPI(fixture.metadata, fixture.authority, APIOptions{
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
		ClientRequestID:       "req-1",
		SessionID:             "session-1",
		QuestionsEnabled:      textutil.Value(true),
		AutoCompactionEnabled: textutil.Value(true),
	})
	if !errors.Is(err, ErrRuntimeOwnerIDRequired) {
		t.Fatalf("expected runtime owner id rejection, got %v", err)
	}
}

func TestServicePassesRuntimeClientFactoryIntoInteractiveRuntime(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	if err := fixture.store.MarkModelDispatchLocked(session.LockedContract{Model: "gpt-5", ContextWindow: 20, ContextPercent: 95}); err != nil {
		t.Fatalf("lock Session context window: %v", err)
	}
	calls := 0
	factory := runtimewire.RuntimeClientFactoryFunc(func(_ context.Context, req runtimewire.RuntimeClientRequest) (llm.Client, error) {
		calls++
		if req.Purpose != runtimewire.RuntimeClientPurposeMain {
			t.Fatalf("factory purpose = %v, want main", req.Purpose)
		}
		if req.ActiveSettings.ModelContextWindow != 20 {
			t.Fatalf("factory context window = %d, want locked Session window 20", req.ActiveSettings.ModelContextWindow)
		}
		return &sessionRuntimeTestLLMClient{responses: []llm.Response{{Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("ok"), Phase: textutil.Value(llm.MessagePhaseFinal)}, Usage: llm.Usage{WindowTokens: 200000}}}}, nil
	})
	fixture.api = NewAPI(fixture.metadata, fixture.authority, APIOptions{RuntimeClientFactory: factory})
	settings := config.DefaultOnboardingSettings()
	settings.Model = "gpt-5"
	settings.ModelContextWindow = 40
	settings.CompactionMode = config.CompactionModeNative
	settings.Reviewer.Frequency = "off"
	activation, err := fixture.api.ActivateSessionRuntime(context.Background(), serverapi.SessionRuntimeActivateRequest{
		ClientRequestID:       "activate-factory",
		SessionID:             fixture.store.Meta().SessionID,
		OwnerID:               "owner",
		QuestionsEnabled:      textutil.Value(true),
		AutoCompactionEnabled: textutil.Value(true),
		ActiveSettings:        settings,
		EnabledToolIDs:        []string{string(toolspec.ToolExecCommand)},
		Source:                config.SourceReport{Sources: map[string]string{}},
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

func TestSessionFastModeRemainsEngineLocalAcrossActivationMutationAndReopen(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	second, err := session.Create(
		filepath.Dir(fixture.store.Dir()),
		filepath.Base(filepath.Dir(fixture.store.Dir())),
		fixture.config.WorkspaceRoot,
		sessioncontract.SessionCategoryMain,
		fixture.metadata.AuthoritativeSessionStoreOptions()...,
	)
	if err != nil {
		t.Fatalf("create second Session: %v", err)
	}
	sessiontest.CommitChatSettingsTestState(t, fixture.store, func(settings *session.ChatSettingsOverrides) { settings.Fast = textutil.Value(true) })
	sessiontest.CommitChatSettingsTestState(t, second, func(settings *session.ChatSettingsOverrides) { settings.Fast = textutil.Value(false) })
	factory := runtimewire.RuntimeClientFactoryFunc(func(context.Context, runtimewire.RuntimeClientRequest) (llm.Client, error) {
		return &sessionRuntimeTestLLMClient{}, nil
	})
	fixture.api = NewAPI(fixture.metadata, fixture.authority, APIOptions{
		RuntimeClientFactory: factory,
	})
	firstSettings := sessionRuntimeFastSettings(true)
	secondSettings := sessionRuntimeFastSettings(false)
	first := activateSessionRuntimeForFastTest(t, fixture.api, fixture.store.Meta().SessionID, "fast-first", firstSettings)
	secondAttachment := activateSessionRuntimeForFastTest(t, fixture.api, second.Meta().SessionID, "fast-second", secondSettings)

	firstEngine := currentSessionRuntimeEngine(t, fixture.authority, fixture.store.Meta().SessionID)
	secondEngine := currentSessionRuntimeEngine(t, fixture.authority, second.Meta().SessionID)
	if !firstEngine.FastModeEnabled() {
		t.Error("first Session Fast = false, want true")
	}
	if secondEngine.FastModeEnabled() {
		t.Error("second Session Fast = true, want false")
	}
	if _, err := firstEngine.SetFastModeEnabled(true); err != nil {
		t.Fatalf("set first Fast: %v", err)
	}
	if secondEngine.FastModeEnabled() {
		t.Error("mutating first Session changed second Session Fast")
	}

	releaseSessionRuntimeForFastTest(t, fixture.api, first, "fast-first")
	releaseSessionRuntimeForFastTest(t, fixture.api, secondAttachment, "fast-second")
	activateSessionRuntimeForFastTest(t, fixture.api, fixture.store.Meta().SessionID, "fast-first-reopen", firstSettings)
	activateSessionRuntimeForFastTest(t, fixture.api, second.Meta().SessionID, "fast-second-reopen", secondSettings)
	if !currentSessionRuntimeEngine(t, fixture.authority, fixture.store.Meta().SessionID).FastModeEnabled() {
		t.Error("reopened first Session Fast = false, want true")
	}
	if currentSessionRuntimeEngine(t, fixture.authority, second.Meta().SessionID).FastModeEnabled() {
		t.Error("reopened second Session Fast = true, want false")
	}
}

func TestActivateSessionRuntimeUsesTypedQuestionAndAutoCompactionSettings(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	fixture.api = NewAPI(fixture.metadata, fixture.authority, APIOptions{
		RuntimeClientFactory: runtimewire.RuntimeClientFactoryFunc(func(context.Context, runtimewire.RuntimeClientRequest) (llm.Client, error) {
			return &sessionRuntimeTestLLMClient{}, nil
		}),
	})
	settings := sessionRuntimeFastSettings(false)
	response, err := fixture.api.ActivateSessionRuntime(t.Context(), serverapi.SessionRuntimeActivateRequest{
		ClientRequestID:       "typed-session-settings",
		SessionID:             fixture.store.Meta().SessionID,
		OwnerID:               "typed-session-settings",
		ActiveSettings:        settings,
		QuestionsEnabled:      textutil.Value(false),
		AutoCompactionEnabled: textutil.Value(false),
		Source:                config.SourceReport{Sources: map[string]string{}},
	})
	if err != nil {
		t.Fatalf("ActivateSessionRuntime: %v", err)
	}
	engine := currentSessionRuntimeEngine(t, fixture.authority, fixture.store.Meta().SessionID)
	if engine.QuestionsEnabled() || engine.AutoCompactionEnabled() {
		t.Fatalf("runtime settings = questions %t auto-compaction %t, want false/false", engine.QuestionsEnabled(), engine.AutoCompactionEnabled())
	}
	releaseSessionRuntimeForFastTest(t, fixture.api, response.Attachment, "typed-session-settings")
}

func TestActivateSessionRuntimeCommitsPlannedAgentSelection(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	fixture.api = NewAPI(fixture.metadata, fixture.authority, APIOptions{
		RuntimeClientFactory: runtimewire.RuntimeClientFactoryFunc(func(context.Context, runtimewire.RuntimeClientRequest) (llm.Client, error) {
			return &sessionRuntimeTestLLMClient{}, nil
		}),
	})
	settings := sessionRuntimeFastSettings(true)
	settings.Reviewer.Frequency = "all"
	settings.ThinkingLevel = "high"
	response, err := fixture.api.ActivateSessionRuntime(t.Context(), serverapi.SessionRuntimeActivateRequest{
		ClientRequestID:       "planned-agent-selection",
		SessionID:             fixture.store.Meta().SessionID,
		OwnerID:               "planned-agent-selection",
		ActiveSettings:        settings,
		QuestionsEnabled:      textutil.Value(false),
		AutoCompactionEnabled: textutil.Value(false),
		AgentSelection: &serverapi.SessionRuntimeAgentSelection{
			Agent: "worker",
			Baseline: serverapi.SessionRuntimeChatSettings{
				Supervisor:     "all",
				Thinking:       "high",
				Fast:           true,
				Questions:      false,
				AutoCompaction: false,
			},
		},
		Source: config.SourceReport{Sources: map[string]string{}},
	})
	if err != nil {
		t.Fatalf("ActivateSessionRuntime: %v", err)
	}
	persisted, err := fixture.metadata.ResolvePersistedSession(t.Context(), fixture.store.Meta().SessionID)
	if err != nil {
		t.Fatalf("resolve persisted Session: %v", err)
	}
	state, err := session.ChatSettingsStateFromMeta(*persisted.Meta)
	if err != nil {
		t.Fatalf("resolve persisted Chat settings: %v", err)
	}
	if state.Agent != "worker" ||
		state.Settings == nil ||
		state.Settings.Supervisor == nil || *state.Settings.Supervisor != "all" ||
		state.Settings.Thinking == nil || *state.Settings.Thinking != "high" ||
		state.Settings.Fast == nil || !*state.Settings.Fast ||
		state.Settings.Questions == nil || *state.Settings.Questions ||
		state.Settings.AutoCompaction == nil || *state.Settings.AutoCompaction {
		t.Fatalf("persisted Chat settings = %+v, want complete worker selection", state)
	}
	releaseSessionRuntimeForFastTest(t, fixture.api, response.Attachment, "planned-agent-selection")
}

func TestActivateSessionRuntimeReplacesReadyRuntimeAfterAgentSelection(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	fixture.api = NewAPI(fixture.metadata, fixture.authority, APIOptions{
		RuntimeClientFactory: runtimewire.RuntimeClientFactoryFunc(func(context.Context, runtimewire.RuntimeClientRequest) (llm.Client, error) {
			return &sessionRuntimeTestLLMClient{}, nil
		}),
	})
	first := activateSessionRuntimeForFastTest(
		t,
		fixture.api,
		fixture.store.Meta().SessionID,
		"first-agent",
		sessionRuntimeFastSettings(false),
	)

	settings := sessionRuntimeFastSettings(true)
	second, err := fixture.api.ActivateSessionRuntime(t.Context(), serverapi.SessionRuntimeActivateRequest{
		ClientRequestID:       "replacement-agent",
		SessionID:             fixture.store.Meta().SessionID,
		OwnerID:               "replacement-agent",
		ActiveSettings:        settings,
		QuestionsEnabled:      textutil.Value(true),
		AutoCompactionEnabled: textutil.Value(true),
		AgentSelection: &serverapi.SessionRuntimeAgentSelection{
			Agent: "worker",
			Baseline: serverapi.SessionRuntimeChatSettings{
				Supervisor:     settings.Reviewer.Frequency,
				Thinking:       settings.ThinkingLevel,
				Fast:           true,
				Questions:      true,
				AutoCompaction: true,
			},
		},
		Source: config.SourceReport{Sources: map[string]string{}},
	})
	if err != nil {
		t.Fatalf("ActivateSessionRuntime: %v", err)
	}
	if second.Attachment.Generation == first.Generation {
		t.Fatalf("replacement generation = %d, want a generation after %d", second.Attachment.Generation, first.Generation)
	}
	if !currentSessionRuntimeEngine(t, fixture.authority, fixture.store.Meta().SessionID).FastModeEnabled() {
		t.Fatal("replacement runtime Fast = false, want selected Agent baseline true")
	}

	releaseSessionRuntimeForFastTest(t, fixture.api, first, "first-agent")
	releaseSessionRuntimeForFastTest(t, fixture.api, second.Attachment, "replacement-agent")
}

func TestActivateSessionRuntimeUsesLatestPersistedQuestionAndAutoCompactionSettings(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	fixture.api = NewAPI(fixture.metadata, fixture.authority, APIOptions{
		RuntimeClientFactory: runtimewire.RuntimeClientFactoryFunc(func(context.Context, runtimewire.RuntimeClientRequest) (llm.Client, error) {
			return &sessionRuntimeTestLLMClient{}, nil
		}),
	})
	sessiontest.CommitChatSettingsTestState(t, fixture.store, func(settings *session.ChatSettingsOverrides) {
		settings.Questions, settings.AutoCompaction = textutil.Value(false), textutil.Value(false)
	})

	response, err := fixture.api.ActivateSessionRuntime(t.Context(), serverapi.SessionRuntimeActivateRequest{
		ClientRequestID:       "stale-planned-session-settings",
		SessionID:             fixture.store.Meta().SessionID,
		OwnerID:               "stale-planned-session-settings",
		ActiveSettings:        sessionRuntimeFastSettings(false),
		QuestionsEnabled:      textutil.Value(true),
		AutoCompactionEnabled: textutil.Value(true),
		Source:                config.SourceReport{Sources: map[string]string{}},
	})
	if err != nil {
		t.Fatalf("ActivateSessionRuntime: %v", err)
	}
	engine := currentSessionRuntimeEngine(t, fixture.authority, fixture.store.Meta().SessionID)
	if engine.QuestionsEnabled() || engine.AutoCompactionEnabled() {
		t.Fatalf("runtime settings = questions %t auto-compaction %t, want latest persisted false/false", engine.QuestionsEnabled(), engine.AutoCompactionEnabled())
	}
	releaseSessionRuntimeForFastTest(t, fixture.api, response.Attachment, "stale-planned-session-settings")
}

func TestActivateSessionRuntimeUsesLatestPersistedCompleteChatSettings(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	fixture.api = NewAPI(fixture.metadata, fixture.authority, APIOptions{
		RuntimeClientFactory: runtimewire.RuntimeClientFactoryFunc(func(context.Context, runtimewire.RuntimeClientRequest) (llm.Client, error) {
			return &sessionRuntimeTestLLMClient{}, nil
		}),
	})
	sessiontest.CommitChatSettingsTestState(t, fixture.store, func(settings *session.ChatSettingsOverrides) {
		settings.Supervisor, settings.Thinking, settings.Fast, settings.Questions, settings.AutoCompaction = textutil.Value("all"), textutil.Value("high"), textutil.Value(true), textutil.Value(false), textutil.Value(false)
	})
	stale := sessionRuntimeFastSettings(false)
	stale.Reviewer.Frequency = "off"
	stale.ThinkingLevel = "low"

	response, err := fixture.api.ActivateSessionRuntime(t.Context(), serverapi.SessionRuntimeActivateRequest{
		ClientRequestID:       "stale-complete-session-settings",
		SessionID:             fixture.store.Meta().SessionID,
		OwnerID:               "stale-complete-session-settings",
		ActiveSettings:        stale,
		QuestionsEnabled:      textutil.Value(true),
		AutoCompactionEnabled: textutil.Value(true),
		Source:                config.SourceReport{Sources: map[string]string{}},
	})
	if err != nil {
		t.Fatalf("ActivateSessionRuntime: %v", err)
	}
	engine := currentSessionRuntimeEngine(t, fixture.authority, fixture.store.Meta().SessionID)
	if engine.ReviewerFrequency() != "all" ||
		engine.ThinkingLevel() != "high" ||
		!engine.FastModeEnabled() ||
		engine.QuestionsEnabled() ||
		engine.AutoCompactionEnabled() {
		t.Fatalf(
			"runtime settings = supervisor %q thinking %q fast %t questions %t auto-compaction %t",
			engine.ReviewerFrequency(),
			engine.ThinkingLevel(),
			engine.FastModeEnabled(),
			engine.QuestionsEnabled(),
			engine.AutoCompactionEnabled(),
		)
	}
	releaseSessionRuntimeForFastTest(t, fixture.api, response.Attachment, "stale-complete-session-settings")
}

func TestActivateSessionRuntimePreservesExplicitThinkingOverPersistedSetting(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	fixture.api = NewAPI(fixture.metadata, fixture.authority, APIOptions{
		RuntimeClientFactory: runtimewire.RuntimeClientFactoryFunc(func(context.Context, runtimewire.RuntimeClientRequest) (llm.Client, error) {
			return &sessionRuntimeTestLLMClient{}, nil
		}),
	})
	sessiontest.CommitChatSettingsTestState(t, fixture.store, func(settings *session.ChatSettingsOverrides) { settings.Thinking = textutil.Value("low") })
	settings := sessionRuntimeFastSettings(false)
	settings.ThinkingLevel = "high"

	response, err := fixture.api.ActivateSessionRuntime(t.Context(), serverapi.SessionRuntimeActivateRequest{
		ClientRequestID:          "explicit-thinking-activation",
		SessionID:                fixture.store.Meta().SessionID,
		OwnerID:                  "explicit-thinking-activation",
		ActiveSettings:           settings,
		QuestionsEnabled:         textutil.Value(true),
		AutoCompactionEnabled:    textutil.Value(true),
		ThinkingOverrideExplicit: true,
		Source:                   config.SourceReport{Sources: map[string]string{}},
	})
	if err != nil {
		t.Fatalf("ActivateSessionRuntime: %v", err)
	}
	engine := currentSessionRuntimeEngine(t, fixture.authority, fixture.store.Meta().SessionID)
	if engine.ThinkingLevel() != "high" {
		t.Fatalf("runtime Thinking = %q, want explicit high", engine.ThinkingLevel())
	}
	releaseSessionRuntimeForFastTest(t, fixture.api, response.Attachment, "explicit-thinking-activation")
}

func sessionRuntimeFastSettings(enabled bool) config.Settings {
	settings := config.DefaultOnboardingSettings()
	settings.Model = "gpt-5"
	settings.PriorityRequestMode = enabled
	settings.ProviderCapabilities = config.ProviderCapabilitiesOverride{
		ProviderID:           "openai",
		SupportsResponsesAPI: true,
		IsOpenAIFirstParty:   true,
	}
	settings.Reviewer.Frequency = "off"
	return settings
}

func activateSessionRuntimeForFastTest(t *testing.T, api *API, sessionID string, owner string, settings config.Settings) serverapi.SessionRuntimeAttachment {
	t.Helper()
	response, err := api.ActivateSessionRuntime(t.Context(), serverapi.SessionRuntimeActivateRequest{
		ClientRequestID:       owner,
		SessionID:             sessionID,
		OwnerID:               owner,
		ActiveSettings:        settings,
		QuestionsEnabled:      textutil.Value(true),
		AutoCompactionEnabled: textutil.Value(true),
		Source:                config.SourceReport{Sources: map[string]string{}},
	})
	if err != nil {
		t.Fatalf("ActivateSessionRuntime %s: %v", owner, err)
	}
	return response.Attachment
}

func releaseSessionRuntimeForFastTest(t *testing.T, api *API, attachment serverapi.SessionRuntimeAttachment, owner string) {
	t.Helper()
	if _, err := api.ReleaseSessionRuntime(t.Context(), serverapi.SessionRuntimeReleaseRequest{
		ClientRequestID: owner + "-release",
		Attachment:      attachment,
		OwnerID:         owner,
		DropOwner:       true,
		ClosePolicy:     serverapi.SessionRuntimeReleaseClosePolicyCloseIfIdle,
	}); err != nil {
		t.Fatalf("ReleaseSessionRuntime %s: %v", owner, err)
	}
}

func currentSessionRuntimeEngine(t *testing.T, authority *Authority, rawSessionID string) *runtimepkg.Engine {
	t.Helper()
	sessionID, err := runtimeids.ParseSessionID(rawSessionID)
	if err != nil {
		t.Fatalf("ParseSessionID: %v", err)
	}
	var engine *runtimepkg.Engine
	if err := authority.WithCurrentRuntime(t.Context(), sessionID, func(_ context.Context, current *runtimepkg.Engine) error {
		engine = current
		return nil
	}); err != nil {
		t.Fatalf("WithCurrentRuntime: %v", err)
	}
	return engine
}

func TestActivateSessionRuntimeAllowsNativeEditInSiblingWorkspace(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	sibling := t.TempDir()
	target := filepath.Join(sibling, "interactive.txt")
	if err := os.WriteFile(target, []byte("before\n"), 0o644); err != nil {
		t.Fatalf("write sibling fixture: %v", err)
	}
	binding, err := fixture.metadata.ResolveSessionNavigationBinding(context.Background(), fixture.store.Meta().SessionID)
	if err != nil {
		t.Fatalf("ResolveSessionNavigationBinding: %v", err)
	}
	if _, err := fixture.metadata.AttachWorkspaceToProject(context.Background(), binding.ProjectID, sibling); err != nil {
		t.Fatalf("AttachWorkspaceToProject: %v", err)
	}
	client := &sessionRuntimeTestLLMClient{responses: []llm.Response{
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("editing"), Phase: textutil.Value(llm.MessagePhaseCommentary)},
			ToolCalls: []llm.ToolCall{{
				ID:    "call-interactive-edit",
				Name:  string(toolspec.ToolEdit),
				Input: json.RawMessage(`{"path":"` + target + `","old_string":"before","new_string":"interactive"}`),
			}},
			Usage: llm.Usage{WindowTokens: 200000},
		},
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("done"), Phase: textutil.Value(llm.MessagePhaseFinal)},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
	}}
	fixture.api = NewAPI(fixture.metadata, fixture.authority, APIOptions{
		RuntimeClientFactory: runtimewire.RuntimeClientFactoryFunc(func(context.Context, runtimewire.RuntimeClientRequest) (llm.Client, error) {
			return client, nil
		}),
	})
	activation, err := fixture.api.ActivateSessionRuntime(context.Background(), serverapi.SessionRuntimeActivateRequest{
		ClientRequestID:       "activate-sibling-edit",
		SessionID:             fixture.store.Meta().SessionID,
		OwnerID:               "interactive-owner",
		QuestionsEnabled:      textutil.Value(true),
		AutoCompactionEnabled: textutil.Value(true),
		ActiveSettings: config.Settings{
			Model:              "gpt-5",
			ThinkingLevel:      "medium",
			ModelContextWindow: 200000,
			AllowNonCwdEdits:   false,
			Reviewer:           config.ReviewerSettings{Frequency: "off"},
			Timeouts:           config.Timeouts{ModelRequestSeconds: 1},
			Shell:              config.ShellSettings{PostprocessingMode: config.ShellPostprocessingModeBuiltin},
		},
		EnabledToolIDs: []string{string(toolspec.ToolEdit)},
		Source:         config.SourceReport{Sources: map[string]string{}},
	})
	if err != nil {
		t.Fatalf("ActivateSessionRuntime: %v", err)
	}
	t.Cleanup(func() {
		_, _ = fixture.api.ReleaseSessionRuntime(context.Background(), serverapi.SessionRuntimeReleaseRequest{
			ClientRequestID: "release-sibling-edit",
			Attachment:      activation.Attachment,
			OwnerID:         "interactive-owner",
			DropOwner:       true,
			ClosePolicy:     serverapi.SessionRuntimeReleaseClosePolicyDetachOnly,
		})
	})
	sessionID, err := runtimeids.ParseSessionID(fixture.store.Meta().SessionID)
	if err != nil {
		t.Fatalf("ParseSessionID: %v", err)
	}
	if err := fixture.authority.WithCurrentRuntime(context.Background(), sessionID, func(_ context.Context, engine *runtimepkg.Engine) error {
		_, err := engine.SubmitUserMessage(context.Background(), "edit the sibling Workspace")
		return err
	}); err != nil {
		t.Fatalf("SubmitUserMessage through activated runtime: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if data, readErr := os.ReadFile(target); readErr == nil && string(data) == "interactive\n" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	data, readErr := os.ReadFile(target)
	t.Fatalf("interactive sibling edit data = %q, read error = %v", data, readErr)
}

func TestActivateSessionRuntimeDeniesEditInForeignManagedWorktree(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	binding, err := fixture.metadata.ResolveSessionNavigationBinding(context.Background(), fixture.store.Meta().SessionID)
	if err != nil {
		t.Fatalf("ResolveSessionNavigationBinding: %v", err)
	}
	managedBase := t.TempDir()
	currentRoot := filepath.Join(managedBase, "current")
	foreignRoot := filepath.Join(managedBase, "foreign")
	missingRoot := filepath.Join(managedBase, "missing")
	for _, root := range []string{currentRoot, foreignRoot} {
		if err := os.MkdirAll(root, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", root, err)
		}
	}
	currentRoot, err = config.CanonicalWorkspaceRoot(currentRoot)
	if err != nil {
		t.Fatalf("canonical current worktree: %v", err)
	}
	foreignRoot, err = config.CanonicalWorkspaceRoot(foreignRoot)
	if err != nil {
		t.Fatalf("canonical foreign worktree: %v", err)
	}
	if err := fixture.metadata.UpsertWorktreeRecord(context.Background(), metadata.WorktreeRecord{
		ID: "interactive-current", WorkspaceID: binding.WorkspaceID, CanonicalRoot: currentRoot,
		DisplayName: "current", Availability: "available", Managed: true, GitMetadataJSON: `{}`,
	}); err != nil {
		t.Fatalf("UpsertWorktreeRecord current: %v", err)
	}
	if err := fixture.metadata.UpsertWorktreeRecord(context.Background(), metadata.WorktreeRecord{
		ID: "interactive-missing", WorkspaceID: binding.WorkspaceID, CanonicalRoot: missingRoot,
		DisplayName: "missing", Availability: "missing", Managed: true, GitMetadataJSON: `{}`,
	}); err != nil {
		t.Fatalf("UpsertWorktreeRecord missing: %v", err)
	}
	if err := fixture.metadata.UpdateSessionExecutionTarget(context.Background(), metadata.SessionExecutionTargetUpdate{
		SessionID:  fixture.store.Meta().SessionID,
		Workspace:  &metadata.SessionExecutionTargetUpdateWorkspace{ID: binding.WorkspaceID},
		Worktree:   &metadata.SessionExecutionTargetUpdateWorktree{ID: "interactive-current"},
		CwdRelpath: ".",
	}); err != nil {
		t.Fatalf("UpdateSessionExecutionTarget: %v", err)
	}
	foreignBinding, err := fixture.metadata.AttachWorkspaceToProject(context.Background(), binding.ProjectID, foreignRoot)
	if err != nil {
		t.Fatalf("AttachWorkspaceToProject foreign: %v", err)
	}
	if err := fixture.metadata.UpsertWorktreeRecord(context.Background(), metadata.WorktreeRecord{
		ID: "interactive-foreign-workspace", WorkspaceID: foreignBinding.WorkspaceID, CanonicalRoot: foreignRoot,
		DisplayName: "foreign-workspace", Availability: "available", Managed: true, GitMetadataJSON: `{}`,
	}); err != nil {
		t.Fatalf("UpsertWorktreeRecord foreign workspace: %v", err)
	}
	for index := 0; index < metadata.ProjectWorkspaceCollectionLimit; index++ {
		if _, err := fixture.metadata.AttachWorkspaceToProject(context.Background(), binding.ProjectID, t.TempDir()); err != nil {
			t.Fatalf("AttachWorkspaceToProject filler %d: %v", index, err)
		}
	}
	boundary, err := fixture.metadata.ResolveProjectWorkspaceBoundary(context.Background(), binding.ProjectID)
	if err != nil {
		t.Fatalf("ResolveProjectWorkspaceBoundary: %v", err)
	}
	if len(boundary.Workspaces) != metadata.ProjectWorkspaceCollectionLimit {
		t.Fatalf("project workspace boundary count = %d, want %d", len(boundary.Workspaces), metadata.ProjectWorkspaceCollectionLimit)
	}
	for _, workspace := range boundary.Workspaces {
		if workspace.CanonicalRoot == foreignRoot {
			t.Fatal("foreign managed Worktree Workspace was not omitted from bounded Project collection")
		}
	}
	target := filepath.Join(foreignRoot, "foreign.txt")
	if err := os.WriteFile(target, []byte("before\n"), 0o644); err != nil {
		t.Fatalf("write foreign fixture: %v", err)
	}
	client := &sessionRuntimeTestLLMClient{final: make(chan struct{}), responses: []llm.Response{
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("editing"), Phase: textutil.Value(llm.MessagePhaseCommentary)},
			ToolCalls: []llm.ToolCall{{ID: "call-foreign-edit", Name: string(toolspec.ToolEdit), Input: json.RawMessage(`{"path":"` + target + `","old_string":"before","new_string":"forbidden-direct"}`)}},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
		{Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("done"), Phase: textutil.Value(llm.MessagePhaseFinal)}, Usage: llm.Usage{WindowTokens: 200000}},
	}}
	fixture.api = NewAPI(fixture.metadata, fixture.authority, APIOptions{
		ManagedWorktreeBaseDir: managedBase,
		RuntimeClientFactory:   runtimewire.RuntimeClientFactoryFunc(func(context.Context, runtimewire.RuntimeClientRequest) (llm.Client, error) { return client, nil }),
	})
	activation, err := fixture.api.ActivateSessionRuntime(context.Background(), serverapi.SessionRuntimeActivateRequest{
		ClientRequestID: "activate-foreign-edit", SessionID: fixture.store.Meta().SessionID, OwnerID: "interactive-owner",
		QuestionsEnabled: textutil.Value(true), AutoCompactionEnabled: textutil.Value(true),
		ActiveSettings: config.Settings{
			Model: "gpt-5", ThinkingLevel: "medium", ModelContextWindow: 200000,
			Reviewer: config.ReviewerSettings{Frequency: "off"}, Timeouts: config.Timeouts{ModelRequestSeconds: 1},
			Shell: config.ShellSettings{PostprocessingMode: config.ShellPostprocessingModeBuiltin},
		},
		EnabledToolIDs: []string{string(toolspec.ToolEdit)}, Source: config.SourceReport{Sources: map[string]string{}},
	})
	if err != nil {
		t.Fatalf("ActivateSessionRuntime: %v", err)
	}
	t.Cleanup(func() {
		_, _ = fixture.api.ReleaseSessionRuntime(context.Background(), serverapi.SessionRuntimeReleaseRequest{
			ClientRequestID: "release-foreign-edit", Attachment: activation.Attachment, OwnerID: "interactive-owner", DropOwner: true,
			ClosePolicy: serverapi.SessionRuntimeReleaseClosePolicyDetachOnly,
		})
	})
	sessionID, err := runtimeids.ParseSessionID(fixture.store.Meta().SessionID)
	if err != nil {
		t.Fatalf("ParseSessionID: %v", err)
	}
	if err := fixture.authority.WithCurrentRuntime(context.Background(), sessionID, func(_ context.Context, engine *runtimepkg.Engine) error {
		_, err := engine.SubmitUserMessage(context.Background(), "edit the foreign worktree")
		return err
	}); err != nil {
		t.Fatalf("SubmitUserMessage: %v", err)
	}
	select {
	case <-client.final:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for final model response")
	}
	denied := false
	for _, request := range client.requestSnapshot() {
		for _, item := range request.Items {
			if item.Type != llm.ResponseItemTypeFunctionCallOutput {
				continue
			}
			var output struct {
				Error string `json:"error"`
			}
			if err := json.Unmarshal(item.Output, &output); err == nil && output.Error == tools.ForeignManagedWorktreeEditDeniedMessage {
				denied = true
			}
		}
	}
	if !denied {
		requests := client.requestSnapshot()
		var items []string
		for _, request := range requests {
			for _, item := range request.Items {
				items = append(items, fmt.Sprintf("type=%s call_id=%v name=%v output=%s", item.Type, item.CallID, item.Name, item.Output))
			}
		}
		t.Fatalf("foreign edit tool result was not the typed denial: items=%v", items)
	}
	if data, err := os.ReadFile(target); err != nil || string(data) != "before\n" {
		t.Fatalf("foreign managed edit data = %q, error = %v", data, err)
	}
}

func TestActivateSessionRuntimeRejectsManagedWorktreeOutsideServerNamespace(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	binding, err := fixture.metadata.ResolveSessionNavigationBinding(context.Background(), fixture.store.Meta().SessionID)
	if err != nil {
		t.Fatalf("ResolveSessionNavigationBinding: %v", err)
	}
	legacyRoot := t.TempDir()
	if err := fixture.metadata.UpsertWorktreeRecord(context.Background(), metadata.WorktreeRecord{
		ID: "interactive-legacy-outside-namespace", WorkspaceID: binding.WorkspaceID, CanonicalRoot: legacyRoot,
		DisplayName: "legacy", Availability: "available", Managed: true, GitMetadataJSON: `{}`,
	}); err != nil {
		t.Fatalf("UpsertWorktreeRecord legacy: %v", err)
	}
	if err := fixture.metadata.UpdateSessionExecutionTarget(context.Background(), metadata.SessionExecutionTargetUpdate{
		SessionID:  fixture.store.Meta().SessionID,
		Workspace:  &metadata.SessionExecutionTargetUpdateWorkspace{ID: binding.WorkspaceID},
		Worktree:   &metadata.SessionExecutionTargetUpdateWorktree{ID: "interactive-legacy-outside-namespace"},
		CwdRelpath: ".",
	}); err != nil {
		t.Fatalf("UpdateSessionExecutionTarget: %v", err)
	}
	_, err = NewAPI(fixture.metadata, fixture.authority, APIOptions{
		ManagedWorktreeBaseDir: t.TempDir(),
	}).ActivateSessionRuntime(context.Background(), serverapi.SessionRuntimeActivateRequest{
		ClientRequestID:       "activate-legacy-outside-namespace",
		SessionID:             fixture.store.Meta().SessionID,
		OwnerID:               "interactive-owner",
		QuestionsEnabled:      textutil.Value(true),
		AutoCompactionEnabled: textutil.Value(true),
		ActiveSettings: config.Settings{
			Model: "gpt-5", ThinkingLevel: "medium", ModelContextWindow: 200000,
			Reviewer: config.ReviewerSettings{Frequency: "off"},
			Timeouts: config.Timeouts{ModelRequestSeconds: 1},
			Shell:    config.ShellSettings{PostprocessingMode: config.ShellPostprocessingModeBuiltin},
		},
		EnabledToolIDs: []string{string(toolspec.ToolEdit)},
		Source:         config.SourceReport{Sources: map[string]string{}},
	})
	if err == nil {
		t.Fatal("ActivateSessionRuntime accepted a managed Worktree outside the server namespace")
	}
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
	fixture.api = NewAPI(fixture.metadata, authority, APIOptions{
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
		ClientRequestID:       "activate-active-shell",
		SessionID:             sessionID,
		OwnerID:               "interactive-owner",
		QuestionsEnabled:      textutil.Value(true),
		AutoCompactionEnabled: textutil.Value(true),
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
	api := NewAPI(metadataStore, authority, APIOptions{})
	return sessionRuntimeFixture{config: appCfg, metadata: metadataStore, store: store, api: api, authority: authority}
}
