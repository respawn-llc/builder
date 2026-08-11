package metadata

import (
	"context"
	"path/filepath"
	"testing"

	"core/server/session"
	"core/shared/sessioncontract"
)

func TestSessionChatSettingsRoundTripThroughMetadataDocument(t *testing.T) {
	store, cfg, binding := newMetadataTestStore(t)
	sessionDir := filepath.Join(cfg.PersistenceRoot, "projects", binding.ProjectID, "sessions")
	chat, err := session.NewLazy(
		sessionDir,
		binding.WorkspaceName,
		cfg.WorkspaceRoot,
		sessioncontract.SessionCategoryMain,
		store.AuthoritativeSessionStoreOptions()...,
	)
	if err != nil {
		t.Fatalf("NewLazy: %v", err)
	}
	supervisor := "all"
	thinking := "  provider-specific-depth  "
	fast := true
	questions := false
	autoCompaction := false
	if err := session.InitializeChatDraft(chat, session.ChatDraftState{
		Message: "unsent composer text",
		Agent:   "worker",
		Settings: &session.ChatSettingsOverrides{
			Supervisor:     &supervisor,
			Thinking:       &thinking,
			Fast:           &fast,
			Questions:      &questions,
			AutoCompaction: &autoCompaction,
		},
	}); err != nil {
		t.Fatalf("InitializeChatDraft: %v", err)
	}
	if err := chat.EnsureDurable(); err != nil {
		t.Fatalf("EnsureDurable: %v", err)
	}

	record, err := store.ResolvePersistedSession(context.Background(), chat.Meta().SessionID)
	if err != nil {
		t.Fatalf("ResolvePersistedSession: %v", err)
	}
	state, err := session.ChatDraftStateFromMeta(*record.Meta)
	if err != nil {
		t.Fatalf("ChatDraftStateFromMeta: %v", err)
	}
	if state.Message != "unsent composer text" ||
		state.Agent != "worker" ||
		state.Settings == nil ||
		state.Settings.Supervisor == nil || *state.Settings.Supervisor != "all" ||
		state.Settings.Thinking == nil || *state.Settings.Thinking != "provider-specific-depth" ||
		state.Settings.Fast == nil || !*state.Settings.Fast ||
		state.Settings.Questions == nil || *state.Settings.Questions ||
		state.Settings.AutoCompaction == nil || *state.Settings.AutoCompaction {
		t.Fatalf("resolved Chat draft state = %+v", state)
	}
}

func TestExistingMetadataSessionWithoutChatOverridesRemainsReadable(t *testing.T) {
	store, cfg, binding := newMetadataTestStore(t)
	chat := createMetadataTestSession(t, store, cfg, binding)
	record, err := store.ResolvePersistedSession(context.Background(), chat.Meta().SessionID)
	if err != nil {
		t.Fatalf("ResolvePersistedSession: %v", err)
	}
	state, err := session.ChatDraftStateFromMeta(*record.Meta)
	if err != nil {
		t.Fatalf("ChatDraftStateFromMeta: %v", err)
	}
	if state.Settings != nil {
		t.Fatalf("existing Session settings = %+v, want absent overrides", state.Settings)
	}
}
