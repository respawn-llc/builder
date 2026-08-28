package session

import (
	"errors"
	"os"
	"testing"

	"core/shared/textutil"
)

func TestMutateChatSettingsUpdatesOneControlWithoutChangingTheAggregate(t *testing.T) {
	store, observer := newChatSettingsMutationStore(t, ChatDraftState{
		Agent:    "worker",
		Settings: completeChatSettingsOverrides("edits", "custom-depth", false, true, true),
	})
	tests := []struct {
		name   string
		target ChatSettingsState
	}{
		{name: "Supervisor", target: ChatSettingsState{Agent: "worker", Settings: completeChatSettingsOverrides("all", "custom-depth", false, true, true)}},
		{name: "Thinking", target: ChatSettingsState{Agent: "worker", Settings: completeChatSettingsOverrides("all", "provider-specific", false, true, true)}},
		{name: "Fast", target: ChatSettingsState{Agent: "worker", Settings: completeChatSettingsOverrides("all", "provider-specific", true, true, true)}},
		{name: "Questions", target: ChatSettingsState{Agent: "worker", Settings: completeChatSettingsOverrides("all", "provider-specific", true, false, true)}},
		{name: "Auto-compaction", target: ChatSettingsState{Agent: "worker", Settings: completeChatSettingsOverrides("all", "provider-specific", true, false, false)}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			observer.called = false
			result, err := store.CommitChatSettingsState(test.target)
			if err != nil {
				t.Fatalf("CommitChatSettingsState: %v", err)
			}
			if !result.Changed || !result.Committed || !observer.called {
				t.Fatalf("mutation result = %+v, observer called=%v", result, observer.called)
			}
		})
	}
}

func TestMutateChatSettingsSelectsDifferentAgentWithCompleteBaselineAtomically(t *testing.T) {
	store, observer := newChatSettingsMutationStore(t, ChatDraftState{
		Agent:    "worker",
		Settings: completeChatSettingsOverrides("edits", "medium", false, true, true),
	})
	if err := store.SetContinuationContext(ContinuationContext{
		AgentRole:     textutil.Value("worker"),
		OpenAIBaseURL: textutil.Value("https://old-agent.example/v1"),
	}); err != nil {
		t.Fatalf("seed continuation: %v", err)
	}
	observer.called = false
	target := ChatSettingsState{Agent: "reviewer", Settings: completeChatSettingsOverrides("all", "  provider-specific-depth  ", true, false, false)}
	result, err := store.CommitChatSettingsState(target)
	if err != nil {
		t.Fatalf("CommitChatSettingsState: %v", err)
	}
	if !result.Changed || !result.Committed || !observer.called {
		t.Fatalf("mutation result = %+v, observer called=%v", result, observer.called)
	}
	for name, meta := range map[string]Meta{
		"store":    store.Meta(),
		"observer": observer.snapshot.Meta,
	} {
		if meta.Continuation == nil || meta.Continuation.OpenAIBaseURL != nil {
			t.Fatalf("%s continuation = %+v, want selected Agent with no previous base URL", name, meta.Continuation)
		}
	}
}

func TestMutateChatSettingsSelectingCurrentAgentIsNoWriteNoOp(t *testing.T) {
	store, observer := newChatSettingsMutationStore(t, ChatDraftState{
		Agent:    "worker",
		Settings: completeChatSettingsOverrides("edits", "medium", false, true, true),
	})
	before := store.Meta()
	observer.called = false
	result, err := store.CommitChatSettingsState(ChatSettingsState{Agent: "worker", Settings: completeChatSettingsOverrides("edits", "medium", false, true, true)})
	if err != nil {
		t.Fatalf("CommitChatSettingsState: %v", err)
	}
	if result.Changed || result.Committed || observer.called {
		t.Fatalf("same-Agent result = %+v, observer called=%v", result, observer.called)
	}
	after := store.Meta()
	if !after.UpdatedAt.Equal(before.UpdatedAt) {
		t.Fatalf("same-Agent selection changed UpdatedAt: %v -> %v", before.UpdatedAt, after.UpdatedAt)
	}
}

func TestMutateChatSettingsRepairsUnavailableUnlockedAgentToDefaultBaseline(t *testing.T) {
	store, _ := newChatSettingsMutationStore(t, ChatDraftState{
		Agent:    "removed-agent",
		Settings: completeChatSettingsOverrides("all", "custom-depth", true, false, false),
	})
	_, err := store.CommitChatSettingsState(ChatSettingsState{Agent: "default", Settings: completeChatSettingsOverrides("edits", "provider-default", false, true, true)})
	if err != nil {
		t.Fatalf("repair unavailable Agent: %v", err)
	}
}

func TestMutateChatSettingsPreservesLockedUnavailableAgent(t *testing.T) {
	store, observer := newChatSettingsMutationStore(t, ChatDraftState{
		Agent:    "removed-agent",
		Settings: completeChatSettingsOverrides("all", "custom-depth", true, false, false),
	})
	if err := store.MarkModelDispatchLocked(LockedContract{Model: "gpt-5"}); err != nil {
		t.Fatalf("MarkModelDispatchLocked: %v", err)
	}
	observer.called = false
	result, err := store.CommitChatSettingsState(ChatSettingsState{Agent: "default", Settings: completeChatSettingsOverrides("edits", "medium", false, true, true)})
	if !errors.Is(err, ErrChatAgentLocked) {
		t.Fatalf("locked Agent mutation error = %v, want ErrChatAgentLocked", err)
	}
	if result.Changed || result.Committed || observer.called {
		t.Fatalf("locked Agent result = %+v, observer called=%v", result, observer.called)
	}
}

func TestMutateChatSettingsObserverFailurePublishesOnlyCompleteAggregate(t *testing.T) {
	store, observer := newChatSettingsMutationStore(t, ChatDraftState{
		Agent:    "worker",
		Settings: completeChatSettingsOverrides("edits", "medium", false, true, true),
	})
	observer.called = false
	observer.err = os.ErrPermission
	result, err := store.CommitChatSettingsState(ChatSettingsState{Agent: "reviewer", Settings: completeChatSettingsOverrides("all", "provider-specific", true, false, false)})
	if err == nil || !errors.Is(err, os.ErrPermission) || !result.Committed || !result.Changed {
		t.Fatalf("observer failure result = %+v, err=%v", result, err)
	}
}

func newChatSettingsMutationStore(t *testing.T, state ChatDraftState) (*Store, *recordingPersistenceObserver) {
	t.Helper()
	observer := &recordingPersistenceObserver{}
	store, err := NewLazy(
		t.TempDir(),
		"workspace-x",
		t.TempDir(),
		testSessionCategory,
		WithPersistenceObserver(observer),
	)
	if err != nil {
		t.Fatalf("NewLazy: %v", err)
	}
	if err := InitializeChatDraft(store, state); err != nil {
		t.Fatalf("InitializeChatDraft: %v", err)
	}
	if err := store.EnsureDurable(); err != nil {
		t.Fatalf("EnsureDurable: %v", err)
	}
	return store, observer
}

func completeChatSettingsOverrides(
	supervisor string,
	thinking string,
	fast bool,
	questions bool,
	autoCompaction bool,
) *ChatSettingsOverrides {
	return &ChatSettingsOverrides{
		Supervisor:     textutil.Value(supervisor),
		Thinking:       textutil.Value(thinking),
		Fast:           textutil.Value(fast),
		Questions:      textutil.Value(questions),
		AutoCompaction: textutil.Value(autoCompaction),
	}
}
