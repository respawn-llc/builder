package session

import (
	"encoding/json"
	"testing"

	"core/shared/config"
	"core/shared/textutil"
)

func TestNormalizeChatSettingsOverridesValidatesPresentValuesAndClones(t *testing.T) {
	supervisor := "edits"
	thinking := "  custom-depth  "
	fast := true
	questions := false
	autoCompaction := true
	input := &ChatSettingsOverrides{
		Supervisor:     &supervisor,
		Thinking:       &thinking,
		Fast:           &fast,
		Questions:      &questions,
		AutoCompaction: &autoCompaction,
	}

	normalized, err := NormalizeChatSettingsOverrides(input)
	if err != nil {
		t.Fatalf("NormalizeChatSettingsOverrides: %v", err)
	}
	if normalized == input ||
		normalized.Supervisor == input.Supervisor ||
		normalized.Thinking == input.Thinking ||
		normalized.Fast == input.Fast ||
		normalized.Questions == input.Questions ||
		normalized.AutoCompaction == input.AutoCompaction {
		t.Fatal("normalized overrides alias caller-owned storage")
	}
	if *normalized.Supervisor != "edits" ||
		*normalized.Thinking != "custom-depth" ||
		!*normalized.Fast ||
		*normalized.Questions ||
		!*normalized.AutoCompaction {
		t.Fatalf("normalized overrides = %+v", normalized)
	}

	supervisor = "off"
	thinking = "changed"
	fast = false
	questions = true
	autoCompaction = false
	if *normalized.Supervisor != "edits" ||
		*normalized.Thinking != "custom-depth" ||
		!*normalized.Fast ||
		*normalized.Questions ||
		!*normalized.AutoCompaction {
		t.Fatalf("caller mutation changed normalized overrides: %+v", normalized)
	}
}

func TestChatSettingsOverridesRejectInvalidPresentStringsWithoutThinkingAllowlist(t *testing.T) {
	for name, overrides := range map[string]ChatSettingsOverrides{
		"blank supervisor":   {Supervisor: textutil.Value("")},
		"unknown supervisor": {Supervisor: textutil.Value("sometimes")},
		"blank thinking":     {Thinking: textutil.Value(" \t ")},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NormalizeChatSettingsOverrides(&overrides); err == nil {
				t.Fatalf("NormalizeChatSettingsOverrides accepted %+v", overrides)
			}
		})
	}

	for _, thinking := range []string{"ultra", "provider-specific-depth"} {
		normalized, err := NormalizeChatSettingsOverrides(&ChatSettingsOverrides{
			Thinking: textutil.Value(thinking),
		})
		if err != nil {
			t.Fatalf("custom Thinking %q rejected: %v", thinking, err)
		}
		if normalized.Thinking == nil || *normalized.Thinking != thinking {
			t.Fatalf("normalized Thinking = %v, want %q", normalized.Thinking, thinking)
		}
	}

	var decoded ChatSettingsOverrides
	if err := json.Unmarshal([]byte(`{"thinking":"custom","unknown":true}`), &decoded); err == nil {
		t.Fatal("Chat settings overrides accepted an unknown persisted field")
	}
}

func TestResolveEffectiveChatSettingsUsesOverrideCurrentDefaultPrecedence(t *testing.T) {
	defaults := ChatSettings{
		Supervisor:     "edits",
		Thinking:       "medium",
		Fast:           true,
		Questions:      true,
		AutoCompaction: true,
	}
	current := &ChatSettingsOverrides{
		Supervisor: textutil.Value("all"),
		Fast:       textutil.Value(false),
		Questions:  textutil.Value(false),
	}
	overrides := &ChatSettingsOverrides{
		Thinking:       textutil.Value("  custom-depth  "),
		AutoCompaction: textutil.Value(false),
	}

	effective, err := ResolveEffectiveChatSettings(overrides, current, defaults)
	if err != nil {
		t.Fatalf("ResolveEffectiveChatSettings: %v", err)
	}
	want := (ChatSettings{
		Supervisor:     "all",
		Thinking:       "custom-depth",
		Fast:           false,
		Questions:      false,
		AutoCompaction: false,
	})
	if effective != want {
		t.Fatalf("effective settings = %+v, want %+v", effective, want)
	}
}

func TestInitializeChatDraftTransfersCompleteAggregateAndRoundTrips(t *testing.T) {
	store := newSessionTestLazyStore(t)
	state := ChatDraftState{
		Message: "unsent composer text",
		Agent:   "worker",
		Settings: &ChatSettingsOverrides{
			Supervisor:     textutil.Value("all"),
			Thinking:       textutil.Value("  provider-specific-depth  "),
			Fast:           textutil.Value(true),
			Questions:      textutil.Value(false),
			AutoCompaction: textutil.Value(false),
		},
	}
	if err := InitializeChatDraft(store, state); err != nil {
		t.Fatalf("InitializeChatDraft: %v", err)
	}
	assertChatDraftState(t, store.Meta(), ChatDraftState{
		Message: "unsent composer text",
		Agent:   "worker",
		Settings: &ChatSettingsOverrides{
			Supervisor:     textutil.Value("all"),
			Thinking:       textutil.Value("provider-specific-depth"),
			Fast:           textutil.Value(true),
			Questions:      textutil.Value(false),
			AutoCompaction: textutil.Value(false),
		},
	})
	meta := store.Meta()
	if meta.Name != "" || meta.FirstPromptPreview != "" || meta.Locked != nil || meta.ModelRequestCount != 0 {
		t.Fatalf("initialized metadata includes accepted-prompt facts: %+v", meta)
	}
	if err := store.EnsureDurable(); err != nil {
		t.Fatalf("EnsureDurable: %v", err)
	}
	reopened, err := openSessionTestStore(store)
	if err != nil {
		t.Fatalf("open initialized Session: %v", err)
	}
	assertChatDraftState(t, reopened.Meta(), ChatDraftState{
		Message: "unsent composer text",
		Agent:   "worker",
		Settings: &ChatSettingsOverrides{
			Supervisor:     textutil.Value("all"),
			Thinking:       textutil.Value("provider-specific-depth"),
			Fast:           textutil.Value(true),
			Questions:      textutil.Value(false),
			AutoCompaction: textutil.Value(false),
		},
	})
}

func TestInitializeChatDraftRequiresFreshIndependentMainSessionAndCompleteSettings(t *testing.T) {
	valid := ChatDraftState{
		Agent: config.DefaultSubagentRole,
		Settings: &ChatSettingsOverrides{
			Supervisor:     textutil.Value("off"),
			Thinking:       textutil.Value("medium"),
			Fast:           textutil.Value(false),
			Questions:      textutil.Value(true),
			AutoCompaction: textutil.Value(true),
		},
	}
	for name, mutate := range map[string]func(*ChatDraftState){
		"missing settings":        func(state *ChatDraftState) { state.Settings = nil },
		"missing supervisor":      func(state *ChatDraftState) { state.Settings.Supervisor = nil },
		"missing thinking":        func(state *ChatDraftState) { state.Settings.Thinking = nil },
		"missing fast":            func(state *ChatDraftState) { state.Settings.Fast = nil },
		"missing questions":       func(state *ChatDraftState) { state.Settings.Questions = nil },
		"missing auto-compaction": func(state *ChatDraftState) { state.Settings.AutoCompaction = nil },
	} {
		t.Run(name, func(t *testing.T) {
			state := cloneChatDraftStateFixture(valid)
			mutate(&state)
			if err := InitializeChatDraft(newSessionTestLazyStore(t), state); err == nil {
				t.Fatal("InitializeChatDraft accepted incomplete transferred settings")
			}
		})
	}

	durable := newSessionTestStore(t)
	if err := InitializeChatDraft(durable, valid); err == nil {
		t.Fatal("InitializeChatDraft accepted a durable Session")
	}
}

func TestExistingSessionWithoutChatOverridesUsesCurrentAndProductDefaults(t *testing.T) {
	meta := Meta{}
	state, err := ChatDraftStateFromMeta(meta)
	if err != nil {
		t.Fatalf("ChatDraftStateFromMeta: %v", err)
	}
	if state.Agent != config.DefaultSubagentRole || state.Settings != nil {
		t.Fatalf("legacy Chat draft state = %+v, want default Agent and absent overrides", state)
	}

	effective, err := ResolveEffectiveChatSettings(nil, &ChatSettingsOverrides{
		Supervisor: textutil.Value("all"),
		Thinking:   textutil.Value("high"),
		Fast:       textutil.Value(false),
	}, ChatSettings{
		Supervisor:     "edits",
		Thinking:       "medium",
		Fast:           true,
		Questions:      true,
		AutoCompaction: true,
	})
	if err != nil {
		t.Fatalf("ResolveEffectiveChatSettings: %v", err)
	}
	want := (ChatSettings{
		Supervisor:     "all",
		Thinking:       "high",
		Fast:           false,
		Questions:      true,
		AutoCompaction: true,
	})
	if effective != want {
		t.Fatalf("legacy effective settings = %+v, want %+v", effective, want)
	}
}

func assertChatDraftState(t *testing.T, meta Meta, want ChatDraftState) {
	t.Helper()
	got, err := ChatDraftStateFromMeta(meta)
	if err != nil {
		t.Fatalf("ChatDraftStateFromMeta: %v", err)
	}
	if got.Message != want.Message || got.Agent != want.Agent {
		t.Fatalf("Chat draft = %+v, want %+v", got, want)
	}
	if got.Settings == nil || want.Settings == nil {
		if got.Settings != want.Settings {
			t.Fatalf("Chat settings = %+v, want %+v", got.Settings, want.Settings)
		}
		return
	}
	if *got.Settings.Supervisor != *want.Settings.Supervisor ||
		*got.Settings.Thinking != *want.Settings.Thinking ||
		*got.Settings.Fast != *want.Settings.Fast ||
		*got.Settings.Questions != *want.Settings.Questions ||
		*got.Settings.AutoCompaction != *want.Settings.AutoCompaction {
		t.Fatalf("Chat settings = %+v, want %+v", got.Settings, want.Settings)
	}
}

func cloneChatDraftStateFixture(input ChatDraftState) ChatDraftState {
	result := input
	if input.Settings != nil {
		result.Settings = &ChatSettingsOverrides{
			Supervisor:     textutil.Value(*input.Settings.Supervisor),
			Thinking:       textutil.Value(*input.Settings.Thinking),
			Fast:           textutil.Value(*input.Settings.Fast),
			Questions:      textutil.Value(*input.Settings.Questions),
			AutoCompaction: textutil.Value(*input.Settings.AutoCompaction),
		}
	}
	return result
}
