package runtimecontrol

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"core/server/launch"
	"core/server/llm"
	"core/server/metadata"
	"core/server/runtime"
	"core/server/runtimewire"
	"core/server/session"
	"core/server/session/sessiontest"
	"core/server/sessionruntime"
	"core/server/tools"
	"core/shared/config"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/sessioncontract"
	"core/shared/textutil"
)

func TestServicePersistsEveryChatControlWithoutCreatingRuntime(t *testing.T) {
	fixture := newChatControlFixture(t, session.ChatSettings{
		Supervisor:     "all",
		Thinking:       "high",
		Fast:           true,
		Questions:      true,
		AutoCompaction: true,
	})
	sessionID := fixture.store.Meta().SessionID

	reviewer, err := fixture.service.SetReviewerEnabled(t.Context(), serverapi.RuntimeSetReviewerEnabledRequest{
		ClientRequestID: "dormant-supervisor",
		SessionID:       sessionID,
		Enabled:         false,
	})
	if err != nil {
		t.Fatalf("SetReviewerEnabled: %v", err)
	}
	if reviewer.Mode != "off" || !reviewer.Changed {
		t.Fatalf("reviewer response = %+v, want changed off", reviewer)
	}
	if err := fixture.service.SetThinkingLevel(t.Context(), serverapi.RuntimeSetThinkingLevelRequest{
		ClientRequestID: "dormant-thinking",
		SessionID:       sessionID,
		Level:           "low",
	}); err != nil {
		t.Fatalf("SetThinkingLevel: %v", err)
	}
	fast, err := fixture.service.SetFastModeEnabled(t.Context(), serverapi.RuntimeSetFastModeEnabledRequest{
		ClientRequestID: "dormant-fast",
		SessionID:       sessionID,
		Enabled:         false,
	})
	if err != nil {
		t.Fatalf("SetFastModeEnabled: %v", err)
	}
	if !fast.Changed {
		t.Fatal("Fast response did not report a change")
	}
	questions, err := fixture.service.SetQuestionsEnabled(t.Context(), serverapi.RuntimeSetQuestionsEnabledRequest{
		ClientRequestID: "dormant-questions",
		SessionID:       sessionID,
		Enabled:         false,
	})
	if err != nil {
		t.Fatalf("SetQuestionsEnabled: %v", err)
	}
	if !questions.Changed || questions.Enabled {
		t.Fatalf("Questions response = %+v, want changed false", questions)
	}
	autoCompaction, err := fixture.service.SetAutoCompactionEnabled(t.Context(), serverapi.RuntimeSetAutoCompactionEnabledRequest{
		ClientRequestID: "dormant-auto-compaction",
		SessionID:       sessionID,
		Enabled:         false,
	})
	if err != nil {
		t.Fatalf("SetAutoCompactionEnabled: %v", err)
	}
	if !autoCompaction.Changed || autoCompaction.Enabled {
		t.Fatalf("Auto-compaction response = %+v, want changed false", autoCompaction)
	}

	assertPersistedChatSettings(t, fixture.persistence, sessionID, session.ChatSettings{
		Supervisor:     "off",
		Thinking:       "low",
		Fast:           false,
		Questions:      false,
		AutoCompaction: false,
	})
	id := mustChatControlSessionID(t, sessionID)
	if err := fixture.authority.WithCurrentRuntime(t.Context(), id, func(context.Context, *runtime.Engine) error {
		t.Fatal("dormant controls unexpectedly created an Engine")
		return nil
	}); !errors.Is(err, serverapi.ErrRuntimeUnavailable) {
		t.Fatalf("dormant runtime lookup error = %v, want unavailable", err)
	}
}

func TestServicePersistsLiveChatControlsAndReopenedEngineObservesThem(t *testing.T) {
	initial := session.ChatSettings{
		Supervisor:     "off",
		Thinking:       "low",
		Fast:           false,
		Questions:      true,
		AutoCompaction: true,
	}
	fixture := newChatControlFixture(t, initial)
	other := fixture.createSession(t, initial)
	currentAttachment, current := fixture.openRuntime(t, fixture.store, initial, "current")
	_, otherEngine := fixture.openRuntime(t, other, initial, "other")
	sessionID := fixture.store.Meta().SessionID

	if _, err := fixture.service.SetReviewerEnabled(t.Context(), serverapi.RuntimeSetReviewerEnabledRequest{
		ClientRequestID: "live-supervisor",
		SessionID:       sessionID,
		Enabled:         true,
	}); err != nil {
		t.Fatalf("SetReviewerEnabled: %v", err)
	}
	if err := fixture.service.SetThinkingLevel(t.Context(), serverapi.RuntimeSetThinkingLevelRequest{
		ClientRequestID: "live-thinking",
		SessionID:       sessionID,
		Level:           "high",
	}); err != nil {
		t.Fatalf("SetThinkingLevel: %v", err)
	}
	if _, err := fixture.service.SetFastModeEnabled(t.Context(), serverapi.RuntimeSetFastModeEnabledRequest{
		ClientRequestID: "live-fast",
		SessionID:       sessionID,
		Enabled:         true,
	}); err != nil {
		t.Fatalf("SetFastModeEnabled: %v", err)
	}
	if _, err := fixture.service.SetQuestionsEnabled(t.Context(), serverapi.RuntimeSetQuestionsEnabledRequest{
		ClientRequestID: "live-questions",
		SessionID:       sessionID,
		Enabled:         false,
	}); err != nil {
		t.Fatalf("SetQuestionsEnabled: %v", err)
	}
	if _, err := fixture.service.SetAutoCompactionEnabled(t.Context(), serverapi.RuntimeSetAutoCompactionEnabledRequest{
		ClientRequestID: "live-auto-compaction",
		SessionID:       sessionID,
		Enabled:         false,
	}); err != nil {
		t.Fatalf("SetAutoCompactionEnabled: %v", err)
	}

	want := session.ChatSettings{
		Supervisor:     "edits",
		Thinking:       "high",
		Fast:           true,
		Questions:      false,
		AutoCompaction: false,
	}
	assertEngineChatSettings(t, current, want)
	assertEngineChatSettings(t, otherEngine, initial)
	assertPersistedChatSettings(t, fixture.persistence, sessionID, want)

	if _, err := currentAttachment.Release(t.Context(), sessionruntime.RuntimeReleaseClose); err != nil {
		t.Fatalf("release current runtime: %v", err)
	}
	_, reopened := fixture.openRuntime(t, fixture.store, want, "reopened")
	assertEngineChatSettings(t, reopened, want)
}

func TestServiceRejectsInvalidThinkingWithoutPersistingDormantOrLiveOverride(t *testing.T) {
	initial := session.ChatSettings{
		Supervisor:     "off",
		Thinking:       "high",
		Fast:           false,
		Questions:      true,
		AutoCompaction: true,
	}
	for _, live := range []bool{false, true} {
		t.Run(map[bool]string{false: "dormant", true: "live"}[live], func(t *testing.T) {
			fixture := newChatControlFixture(t, initial)
			var engine *runtime.Engine
			if live {
				_, engine = fixture.openRuntime(t, fixture.store, initial, "thinking-validation")
			}
			err := fixture.service.SetThinkingLevel(t.Context(), serverapi.RuntimeSetThinkingLevelRequest{
				ClientRequestID: "invalid-thinking",
				SessionID:       fixture.store.Meta().SessionID,
				Level:           "provider-specific-depth",
			})
			if err == nil {
				t.Fatal("SetThinkingLevel accepted an unsupported public control value")
			}
			assertPersistedChatSettings(t, fixture.persistence, fixture.store.Meta().SessionID, initial)
			if engine != nil && engine.ThinkingLevel() != initial.Thinking {
				t.Fatalf("live Thinking = %q, want unchanged %q", engine.ThinkingLevel(), initial.Thinking)
			}
		})
	}
}

func TestServiceRejectsThinkingUnsupportedBySelectedAgentWithoutPersistingDormantOrLiveOverride(t *testing.T) {
	initial := session.ChatSettings{
		Supervisor:     "off",
		Thinking:       "high",
		Fast:           false,
		Questions:      true,
		AutoCompaction: true,
	}
	for _, live := range []bool{false, true} {
		t.Run(map[bool]string{false: "dormant", true: "live"}[live], func(t *testing.T) {
			fixture := newChatControlFixture(t, initial)
			var engine *runtime.Engine
			if live {
				_, engine = fixture.openRuntime(t, fixture.store, initial, "thinking-capability-validation")
			}
			err := fixture.service.SetThinkingLevel(t.Context(), serverapi.RuntimeSetThinkingLevelRequest{
				ClientRequestID: "unsupported-thinking",
				SessionID:       fixture.store.Meta().SessionID,
				Level:           "ultra",
			})
			if err == nil {
				t.Fatal("SetThinkingLevel accepted a value unsupported by the selected Agent")
			}
			assertPersistedChatSettings(t, fixture.persistence, fixture.store.Meta().SessionID, initial)
			if engine != nil && engine.ThinkingLevel() != initial.Thinking {
				t.Fatalf("live Thinking = %q, want unchanged %q", engine.ThinkingLevel(), initial.Thinking)
			}
		})
	}
}

func TestServiceAcceptsSelectedAgentNonEnumeratedThinkingValue(t *testing.T) {
	initial := session.ChatSettings{
		Supervisor:     "off",
		Thinking:       "provider-specific-depth",
		Questions:      true,
		AutoCompaction: true,
	}
	fixture := newChatControlFixture(t, initial)
	fixture.service.WithChatSettingsPreparationResolver(staticChatSettingsPreparationResolver{
		prepared: launch.PreparedChatSettings{
			Baseline:                initial,
			SupportedThinkingValues: []string{"provider-specific-depth", "provider-specific-wide"},
			FastAvailable:           true,
			QuestionsAvailable:      true,
		},
	})
	_, engine := fixture.openRuntime(t, fixture.store, initial, "non-enumerated-thinking")

	if err := fixture.service.SetThinkingLevel(t.Context(), serverapi.RuntimeSetThinkingLevelRequest{
		ClientRequestID: "non-enumerated-thinking",
		SessionID:       fixture.store.Meta().SessionID,
		Level:           " provider-specific-wide ",
	}); err != nil {
		t.Fatalf("SetThinkingLevel: %v", err)
	}

	want := initial
	want.Thinking = "provider-specific-wide"
	assertPersistedChatSettings(t, fixture.persistence, fixture.store.Meta().SessionID, want)
	assertEngineChatSettings(t, engine, want)
}

func TestServiceSupervisorEnableUsesSelectedAgentBaselineWithoutHiddenResumeMode(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		baseline string
		want     string
	}{
		{name: "off", baseline: "off", want: "edits"},
		{name: "after edits", baseline: "edits", want: "edits"},
		{name: "always", baseline: "all", want: "all"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			initial := session.ChatSettings{
				Supervisor:     testCase.baseline,
				Thinking:       "medium",
				Questions:      true,
				AutoCompaction: true,
			}
			fixture := newChatControlFixture(t, initial)
			sessionID := fixture.store.Meta().SessionID

			if _, err := fixture.service.SetReviewerEnabled(t.Context(), serverapi.RuntimeSetReviewerEnabledRequest{
				ClientRequestID: "disable",
				SessionID:       sessionID,
				Enabled:         false,
			}); err != nil {
				t.Fatalf("disable Supervisor: %v", err)
			}
			if _, err := fixture.service.SetReviewerEnabled(t.Context(), serverapi.RuntimeSetReviewerEnabledRequest{
				ClientRequestID: "enable",
				SessionID:       sessionID,
				Enabled:         true,
			}); err != nil {
				t.Fatalf("enable Supervisor: %v", err)
			}
			assertPersistedChatSettings(t, fixture.persistence, sessionID, session.ChatSettings{
				Supervisor:     testCase.want,
				Thinking:       "medium",
				Questions:      true,
				AutoCompaction: true,
			})
		})
	}
}

func TestServiceRepeatedSupervisorEnablePreservesCurrentMode(t *testing.T) {
	initial := session.ChatSettings{
		Supervisor:     "all",
		Thinking:       "medium",
		Questions:      true,
		AutoCompaction: true,
	}
	for _, live := range []bool{false, true} {
		t.Run(map[bool]string{false: "dormant", true: "live"}[live], func(t *testing.T) {
			fixture := newChatControlFixture(t, initial)
			fixture.service.WithChatSettingsPreparationResolver(staticChatSettingsPreparationResolver{
				prepared: launch.PreparedChatSettings{
					Baseline:                session.ChatSettings{Supervisor: "edits", Thinking: "medium", Questions: true, AutoCompaction: true},
					SupportedThinkingValues: []string{"low", "medium", "high"},
					FastAvailable:           true,
					QuestionsAvailable:      true,
				},
			})
			var engine *runtime.Engine
			if live {
				_, engine = fixture.openRuntime(t, fixture.store, initial, "repeated-supervisor-enable")
			}

			response, err := fixture.service.SetReviewerEnabled(t.Context(), serverapi.RuntimeSetReviewerEnabledRequest{
				ClientRequestID: "repeated-enable",
				SessionID:       fixture.store.Meta().SessionID,
				Enabled:         true,
			})
			if err != nil {
				t.Fatalf("SetReviewerEnabled: %v", err)
			}
			if response.Changed || response.Mode != "all" {
				t.Fatalf("response = %+v, want unchanged all", response)
			}
			assertPersistedChatSettings(t, fixture.persistence, fixture.store.Meta().SessionID, initial)
			if engine != nil {
				assertEngineChatSettings(t, engine, initial)
			}
		})
	}
}

type chatControlFixture struct {
	authority   *sessionruntime.Authority
	persistence *sessiontest.Persistence
	service     *Service
	container   string
	workspace   string
	store       *session.Store
}

func newChatControlFixture(t *testing.T, settings session.ChatSettings) *chatControlFixture {
	t.Helper()
	persistence := sessiontest.NewPersistence()
	fixture := &chatControlFixture{
		persistence: persistence,
		container:   t.TempDir(),
		workspace:   t.TempDir(),
	}
	fixture.store = fixture.createSession(t, settings)
	fixture.authority = sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{
		PersistenceRoot: t.TempDir(),
		StoreOptions:    persistence.Options(),
	})
	t.Cleanup(func() {
		if err := fixture.authority.Close(context.Background()); err != nil {
			t.Errorf("close authority: %v", err)
		}
	})
	fixture.service = NewService(fixture.authority).WithPersistedSessionResolver(persistence)
	fixture.service.WithChatSettingsPreparationResolver(staticChatSettingsPreparationResolver{
		prepared: launch.PreparedChatSettings{
			Baseline:                settings,
			SupportedThinkingValues: []string{"low", "medium", "high"},
			FastAvailable:           true,
			QuestionsAvailable:      true,
		},
	})
	return fixture
}

func (f *chatControlFixture) createSession(t *testing.T, settings session.ChatSettings) *session.Store {
	t.Helper()
	store, err := session.NewLazy(
		f.container,
		filepath.Base(f.container),
		f.workspace,
		sessioncontract.SessionCategoryMain,
		f.persistence.Options()...,
	)
	if err != nil {
		t.Fatalf("NewLazy: %v", err)
	}
	if err := session.InitializeCreationContext(store, nil, session.SessionCreationSourceIndependent, session.ChildContextOptions{}); err != nil {
		t.Fatalf("InitializeCreationContext: %v", err)
	}
	if err := session.InitializeChatDraft(store, session.ChatDraftState{
		Agent: config.DefaultSubagentRole,
		Settings: &session.ChatSettingsOverrides{
			Supervisor:     textutil.Value(settings.Supervisor),
			Thinking:       textutil.Value(settings.Thinking),
			Fast:           textutil.Value(settings.Fast),
			Questions:      textutil.Value(settings.Questions),
			AutoCompaction: textutil.Value(settings.AutoCompaction),
		},
	}); err != nil {
		t.Fatalf("InitializeChatDraft: %v", err)
	}
	if err := store.EnsureDurable(); err != nil {
		t.Fatalf("EnsureDurable: %v", err)
	}
	return store
}

func (f *chatControlFixture) openRuntime(
	t *testing.T,
	store *session.Store,
	settings session.ChatSettings,
	owner string,
) (sessionruntime.RuntimeAttachment, *runtime.Engine) {
	t.Helper()
	filesystemContext, err := runtimewire.NewFilesystemContext(
		f.workspace,
		f.workspace,
		metadata.ProjectWorkspaceBoundary{ProjectID: "chat-control-test"},
	)
	if err != nil {
		t.Fatalf("NewFilesystemContext: %v", err)
	}
	runtimeSettings := config.DefaultOnboardingSettings()
	runtimeSettings.Model = "gpt-5"
	runtimeSettings.ProviderOverride = "openai"
	runtimeSettings.ThinkingLevel = settings.Thinking
	runtimeSettings.PriorityRequestMode = settings.Fast
	runtimeSettings.Reviewer.Frequency = settings.Supervisor
	plan, err := sessionruntime.NewAgentRuntimePlan(sessionruntime.AgentRuntimePlanOptions{
		Settings:              runtimeSettings,
		FilesystemContext:     tools.FilesystemContext{Access: filesystemContext.Access},
		QuestionsEnabled:      textutil.Value(settings.Questions),
		AutoCompactionEnabled: textutil.Value(settings.AutoCompaction),
		Client:                &runtimeControlFakeClient{},
		ReviewerClientFactory: runtimewire.RuntimeClientFactoryFunc(func(context.Context, runtimewire.RuntimeClientRequest) (llm.Client, error) {
			return &runtimeControlFakeClient{}, nil
		}),
		ProviderCapabilitiesOverride: &runtimeControlOpenAICapabilities,
	})
	if err != nil {
		t.Fatalf("NewAgentRuntimePlan: %v", err)
	}
	id := mustChatControlSessionID(t, store.Meta().SessionID)
	attachment, err := f.authority.OpenRuntime(t.Context(), sessionruntime.RuntimeOpenRequest{
		SessionID: id,
		OwnerID:   owner,
		Runtime:   &plan,
	})
	if err != nil {
		t.Fatalf("OpenRuntime: %v", err)
	}
	var engine *runtime.Engine
	if err := f.authority.WithCurrentRuntime(t.Context(), id, func(_ context.Context, current *runtime.Engine) error {
		engine = current
		engine.SetQuestionsEnabled(settings.Questions)
		engine.SetAutoCompactionEnabled(settings.AutoCompaction)
		return nil
	}); err != nil {
		t.Fatalf("WithCurrentRuntime: %v", err)
	}
	return attachment, engine
}

func assertPersistedChatSettings(t *testing.T, persistence *sessiontest.Persistence, sessionID string, want session.ChatSettings) {
	t.Helper()
	record, err := persistence.ResolvePersistedSession(t.Context(), sessionID)
	if err != nil {
		t.Fatalf("ResolvePersistedSession: %v", err)
	}
	state, err := session.ChatSettingsStateFromMeta(*record.Meta)
	if err != nil {
		t.Fatalf("ChatSettingsStateFromMeta: %v", err)
	}
	got, err := session.ResolveEffectiveChatSettings(state.Settings, nil, want)
	if err != nil {
		t.Fatalf("ResolveEffectiveChatSettings: %v", err)
	}
	if got != want {
		t.Fatalf("persisted Chat settings = %+v, want %+v", got, want)
	}
}

func assertEngineChatSettings(t *testing.T, engine *runtime.Engine, want session.ChatSettings) {
	t.Helper()
	if got := engine.ReviewerFrequency(); got != want.Supervisor {
		t.Fatalf("Supervisor = %q, want %q", got, want.Supervisor)
	}
	if got := engine.ThinkingLevel(); got != want.Thinking {
		t.Fatalf("Thinking = %q, want %q", got, want.Thinking)
	}
	if got := engine.FastModeEnabled(); got != want.Fast {
		t.Fatalf("Fast = %t, want %t", got, want.Fast)
	}
	if got := engine.QuestionsEnabled(); got != want.Questions {
		t.Fatalf("Questions = %t, want %t", got, want.Questions)
	}
	if got := engine.AutoCompactionEnabled(); got != want.AutoCompaction {
		t.Fatalf("Auto-compaction = %t, want %t", got, want.AutoCompaction)
	}
}

func mustChatControlSessionID(t *testing.T, value string) runtimeids.SessionID {
	t.Helper()
	id, err := runtimeids.ParseSessionID(value)
	if err != nil {
		t.Fatalf("ParseSessionID: %v", err)
	}
	return id
}

type staticChatSettingsPreparationResolver struct {
	prepared launch.PreparedChatSettings
	err      error
}

func (r staticChatSettingsPreparationResolver) PrepareSessionChatSettings(context.Context, string, string) (launch.PreparedChatSettings, error) {
	return r.prepared, r.err
}
