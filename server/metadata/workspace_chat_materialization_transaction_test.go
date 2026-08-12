package metadata

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"core/server/session"
	"core/shared/config"
	"core/shared/serverapi"
	"core/shared/sessioncontract"
	"core/shared/textutil"
)

func TestWorkspaceChatMaterializationCommitsVisibleSessionAndDraftRemovalTogether(t *testing.T) {
	ctx := context.Background()
	store, cfg, binding := newMetadataTestStore(t)
	draft := testDraft()
	if err := store.ReplaceWorkspaceChatDraft(ctx, binding.WorkspaceID, &draft); err != nil {
		t.Fatalf("ReplaceWorkspaceChatDraft: %v", err)
	}

	chat := newWorkspaceChatMaterializationSession(t, store, cfg, binding, ChatMaterializationFixture{
		Message:        draft.Message,
		Agent:          draft.Agent,
		Supervisor:     draft.Supervisor,
		Thinking:       draft.Thinking,
		Fast:           draft.Fast,
		Questions:      draft.Questions,
		AutoCompaction: draft.AutoCompaction,
	})
	if err := chat.EnsureDurable(); err != nil {
		t.Fatalf("EnsureDurable: %v", err)
	}

	if storedDraft, err := store.ReadWorkspaceChatDraft(ctx, binding.WorkspaceID); err != nil || storedDraft != nil {
		t.Fatalf("workspace draft after commit = %+v, err=%v", storedDraft, err)
	}
	record, err := store.ResolvePersistedSession(ctx, chat.Meta().SessionID)
	if err != nil {
		t.Fatalf("ResolvePersistedSession: %v", err)
	}
	if record.Meta.InputDraft != draft.Message {
		t.Fatalf("materialized composer draft = %q, want %q", record.Meta.InputDraft, draft.Message)
	}
	page := materializedSessionPage(t, store, binding.ProjectID)
	requireSessionPageIDs(t, page, chat.Meta().SessionID)
}

func TestWorkspaceChatMaterializationRejectsMismatchedProjectWorkspaceBinding(t *testing.T) {
	ctx := context.Background()
	store, cfg, binding := newMetadataTestStore(t)
	otherRoot := t.TempDir()
	other, err := store.AttachWorkspaceToProject(ctx, binding.ProjectID, otherRoot)
	if err != nil {
		t.Fatalf("AttachWorkspaceToProject: %v", err)
	}
	draft := testDraft()
	if err := store.ReplaceWorkspaceChatDraft(ctx, binding.WorkspaceID, &draft); err != nil {
		t.Fatalf("ReplaceWorkspaceChatDraft: %v", err)
	}

	chat := newWorkspaceChatMaterializationSessionForRoot(
		t,
		store,
		cfg,
		binding,
		other.CanonicalRoot,
		ChatMaterializationFixture{
			Agent:          "default",
			Supervisor:     "edits",
			Thinking:       "medium",
			Fast:           false,
			Questions:      true,
			AutoCompaction: true,
		},
	)
	if err := chat.EnsureDurable(); !errors.Is(err, serverapi.ErrWorkspaceNotRegistered) {
		t.Fatalf("EnsureDurable error = %v, want workspace binding rejection", err)
	}
	if storedDraft, err := store.ReadWorkspaceChatDraft(ctx, binding.WorkspaceID); err != nil || storedDraft == nil {
		t.Fatalf("workspace draft after rejected binding = %+v, err=%v", storedDraft, err)
	}
	if _, err := store.ResolvePersistedSession(ctx, chat.Meta().SessionID); !errors.Is(err, session.ErrSessionNotFound) {
		t.Fatalf("ResolvePersistedSession error = %v, want Session absent", err)
	}
	if page := materializedSessionPage(t, store, binding.ProjectID); len(page.Sessions) != 0 {
		t.Fatalf("visible Sessions after rejected binding = %+v", page.Sessions)
	}
}

func TestWorkspaceChatMaterializationTransactionFailurePreservesDraftAndOmitsSession(t *testing.T) {
	ctx := context.Background()
	store, cfg, binding := newMetadataTestStore(t)
	draft := testDraft()
	if err := store.ReplaceWorkspaceChatDraft(ctx, binding.WorkspaceID, &draft); err != nil {
		t.Fatalf("ReplaceWorkspaceChatDraft: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `
		CREATE TRIGGER fail_workspace_chat_materialization
		BEFORE UPDATE OF chat_draft_json ON workspaces
		WHEN OLD.id = '`+binding.WorkspaceID+`' AND NEW.chat_draft_json IS NULL
		BEGIN
			SELECT RAISE(ABORT, 'injected draft clear failure');
		END
	`); err != nil {
		t.Fatalf("create failure trigger: %v", err)
	}

	chat := newWorkspaceChatMaterializationSession(t, store, cfg, binding, ChatMaterializationFixture{
		Agent:          "default",
		Supervisor:     "edits",
		Thinking:       "medium",
		Fast:           false,
		Questions:      true,
		AutoCompaction: true,
	})
	if err := chat.EnsureDurable(); err == nil {
		t.Fatal("EnsureDurable succeeded despite injected transaction failure")
	}
	if storedDraft, err := store.ReadWorkspaceChatDraft(ctx, binding.WorkspaceID); err != nil || storedDraft == nil || *storedDraft != draft {
		t.Fatalf("workspace draft after rollback = %+v, err=%v", storedDraft, err)
	}
	if _, err := store.ResolvePersistedSession(ctx, chat.Meta().SessionID); !errors.Is(err, session.ErrSessionNotFound) {
		t.Fatalf("ResolvePersistedSession error = %v, want Session absent", err)
	}
	if page := materializedSessionPage(t, store, binding.ProjectID); len(page.Sessions) != 0 {
		t.Fatalf("visible Sessions after rollback = %+v", page.Sessions)
	}
}

func TestBlankWorkspaceChatMaterializationStaysVisibleAfterOrdinaryReimport(t *testing.T) {
	ctx := context.Background()
	store, cfg, binding := newMetadataTestStore(t)
	chat := newWorkspaceChatMaterializationSession(t, store, cfg, binding, ChatMaterializationFixture{
		Agent:          "default",
		Supervisor:     "edits",
		Thinking:       "medium",
		Fast:           false,
		Questions:      true,
		AutoCompaction: true,
	})
	if err := chat.EnsureDurable(); err != nil {
		t.Fatalf("EnsureDurable: %v", err)
	}
	requireSessionPageIDs(t, materializedSessionPage(t, store, binding.ProjectID), chat.Meta().SessionID)

	if err := store.ImportSessionSnapshot(ctx, session.PersistedStoreSnapshot{
		SessionDir: chat.Dir(),
		Meta:       chat.Meta(),
	}); err != nil {
		t.Fatalf("ordinary ImportSessionSnapshot: %v", err)
	}
	requireSessionPageIDs(t, materializedSessionPage(t, store, binding.ProjectID), chat.Meta().SessionID)
}

func TestOrdinaryBlankSessionImportsRemainHidden(t *testing.T) {
	ctx := context.Background()
	store, cfg, binding := newMetadataTestStore(t)
	sessionDir := filepath.Join(cfg.PersistenceRoot, "projects", binding.ProjectID, "sessions")
	chat, err := session.Create(
		sessionDir,
		binding.WorkspaceName,
		binding.CanonicalRoot,
		sessioncontract.SessionCategoryMain,
		store.AuthoritativeSessionStoreOptions()...,
	)
	if err != nil {
		t.Fatalf("session.Create: %v", err)
	}
	if page := materializedSessionPage(t, store, binding.ProjectID); len(page.Sessions) != 0 {
		t.Fatalf("ordinary blank Session became visible: %+v", page.Sessions)
	}
	if err := store.ImportSessionSnapshot(ctx, session.PersistedStoreSnapshot{
		SessionDir: chat.Dir(),
		Meta:       chat.Meta(),
	}); err != nil {
		t.Fatalf("ordinary ImportSessionSnapshot: %v", err)
	}
	if page := materializedSessionPage(t, store, binding.ProjectID); len(page.Sessions) != 0 {
		t.Fatalf("ordinary blank Session became visible after reimport: %+v", page.Sessions)
	}
}

type ChatMaterializationFixture struct {
	Message        string
	Agent          string
	Supervisor     string
	Thinking       string
	Fast           bool
	Questions      bool
	AutoCompaction bool
}

func newWorkspaceChatMaterializationSession(
	t *testing.T,
	store *Store,
	cfg config.App,
	binding Binding,
	fixture ChatMaterializationFixture,
) *session.Store {
	t.Helper()
	return newWorkspaceChatMaterializationSessionForRoot(
		t,
		store,
		cfg,
		binding,
		binding.CanonicalRoot,
		fixture,
	)
}

func newWorkspaceChatMaterializationSessionForRoot(
	t *testing.T,
	store *Store,
	cfg config.App,
	binding Binding,
	workspaceRoot string,
	fixture ChatMaterializationFixture,
) *session.Store {
	t.Helper()
	sessionDir := filepath.Join(cfg.PersistenceRoot, "projects", binding.ProjectID, "sessions")
	chat, err := session.NewLazy(
		sessionDir,
		binding.WorkspaceName,
		workspaceRoot,
		sessioncontract.SessionCategoryMain,
		store.WorkspaceChatMaterializationStoreOptions(binding.WorkspaceID)...,
	)
	if err != nil {
		t.Fatalf("session.NewLazy: %v", err)
	}
	if err := session.InitializeChatDraft(chat, session.ChatDraftState{
		Message: fixture.Message,
		Agent:   fixture.Agent,
		Settings: &session.ChatSettingsOverrides{
			Supervisor:     textutil.Value(fixture.Supervisor),
			Thinking:       textutil.Value(fixture.Thinking),
			Fast:           textutil.Value(fixture.Fast),
			Questions:      textutil.Value(fixture.Questions),
			AutoCompaction: textutil.Value(fixture.AutoCompaction),
		},
	}); err != nil {
		t.Fatalf("InitializeChatDraft: %v", err)
	}
	return chat
}

func materializedSessionPage(t *testing.T, store *Store, projectID string) serverapi.SessionPageResponse {
	t.Helper()
	page, err := store.ListSessionPage(t.Context(), serverapi.SessionPageRequest{
		ProjectID: projectID,
		Category:  sessioncontract.SessionCategoryMain,
		PageSize:  10,
		Position:  serverapi.NewestSessionPagePosition(),
	})
	if err != nil {
		t.Fatalf("ListSessionPage: %v", err)
	}
	return page
}
