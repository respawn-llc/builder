package sessionlaunch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"core/server/launch"
	"core/server/metadata"
	"core/server/session"
	"core/shared/config"
	"core/shared/serverapi"
	"core/shared/sessioncontract"
	"core/shared/toolspec"
)

func TestServiceMaterializesCompleteResolvedWorkspaceChatDraft(t *testing.T) {
	ctx := context.Background()
	service, metadataStore, cfg, binding := newWorkspaceChatMaterializationService(t)
	cfg.Settings.ThinkingLevel = "  provider-specific-depth  "
	cfg.Settings.EnabledTools = map[toolspec.ID]bool{toolspec.ToolAskQuestion: true}
	service.planner.Config = cfg
	draft := metadata.WorkspaceChatDraftDocument{
		Message:        "unsent composer",
		Agent:          "default",
		Supervisor:     "all",
		Thinking:       "provider-specific-depth",
		Fast:           false,
		Questions:      false,
		AutoCompaction: false,
	}
	if err := metadataStore.ReplaceWorkspaceChatDraft(ctx, binding.WorkspaceID, &draft); err != nil {
		t.Fatalf("ReplaceWorkspaceChatDraft: %v", err)
	}

	sessionID, err := service.materializeWorkspaceChatSession(ctx)
	if err != nil {
		t.Fatalf("MaterializeWorkspaceChatSession: %v", err)
	}
	if !sessionID.IsCanonicalUUIDv4() {
		t.Fatalf("materialized Session identity = %q, want canonical UUIDv4", sessionID.String())
	}
	record, err := metadataStore.ResolvePersistedSession(ctx, sessionID.String())
	if err != nil {
		t.Fatalf("ResolvePersistedSession: %v", err)
	}
	state, err := session.ChatDraftStateFromMeta(*record.Meta)
	if err != nil {
		t.Fatalf("ChatDraftStateFromMeta: %v", err)
	}
	if state.Message != draft.Message ||
		state.Agent != draft.Agent ||
		state.Settings == nil ||
		state.Settings.Supervisor == nil || *state.Settings.Supervisor != draft.Supervisor ||
		state.Settings.Thinking == nil || *state.Settings.Thinking != draft.Thinking ||
		state.Settings.Fast == nil || *state.Settings.Fast != draft.Fast ||
		state.Settings.Questions == nil || *state.Settings.Questions != draft.Questions ||
		state.Settings.AutoCompaction == nil || *state.Settings.AutoCompaction != draft.AutoCompaction {
		t.Fatalf("materialized Chat draft = %+v, want %+v", state, draft)
	}
	if record.Meta.Name != "" ||
		record.Meta.FirstPromptPreview != "" ||
		record.Meta.ModelRequestCount != 0 ||
		record.Meta.Locked != nil {
		t.Fatalf("materialized Session has accepted-turn facts: %+v", record.Meta)
	}
	if stored, err := metadataStore.ReadWorkspaceChatDraft(ctx, binding.WorkspaceID); err != nil || stored != nil {
		t.Fatalf("workspace draft after materialization = %+v, err=%v", stored, err)
	}
}

func TestServiceMaterializationFailurePreservesDraftAndCleansFilesystemArtifact(t *testing.T) {
	ctx := context.Background()
	service, metadataStore, _, binding := newWorkspaceChatMaterializationService(t)
	draft := metadata.WorkspaceChatDraftDocument{
		Message:        "preserve",
		Agent:          "default",
		Supervisor:     "edits",
		Thinking:       "medium",
		AutoCompaction: true,
	}
	if err := metadataStore.ReplaceWorkspaceChatDraft(ctx, binding.WorkspaceID, &draft); err != nil {
		t.Fatalf("ReplaceWorkspaceChatDraft: %v", err)
	}
	service.WithWorkspaceChatMaterializationStoreOptions(
		session.WithPersistenceObserver(failingMaterializationObserver{}),
		session.WithPersistedSessionResolver(metadataStore),
	)

	if _, err := service.materializeWorkspaceChatSession(ctx); err == nil {
		t.Fatal("MaterializeWorkspaceChatSession succeeded despite injected metadata failure")
	}
	if stored, err := metadataStore.ReadWorkspaceChatDraft(ctx, binding.WorkspaceID); err != nil || stored == nil || *stored != draft {
		t.Fatalf("workspace draft after failed materialization = %+v, err=%v", stored, err)
	}
	entries, err := os.ReadDir(service.planner.ContainerDir)
	if err != nil {
		t.Fatalf("ReadDir Session container: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("failed materialization left Session artifacts: %+v", entries)
	}
	page, err := metadataStore.ListSessionPage(ctx, serverapi.SessionPageRequest{
		ProjectID: binding.ProjectID,
		Category:  sessioncontract.SessionCategoryMain,
		PageSize:  10,
		Position:  serverapi.NewestSessionPagePosition(),
	})
	if err != nil {
		t.Fatalf("ListSessionPage: %v", err)
	}
	if len(page.Sessions) != 0 {
		t.Fatalf("failed materialization exposed Sessions: %+v", page.Sessions)
	}
}

type failingMaterializationObserver struct{}

func (failingMaterializationObserver) ObservePersistedStore(context.Context, session.PersistedStoreSnapshot) error {
	return errors.New("injected materialization failure")
}

func TestServiceTreatsSeparatelyReceivedMaterializationRequestsIndependently(t *testing.T) {
	ctx := context.Background()
	service, metadataStore, _, binding := newWorkspaceChatMaterializationService(t)
	first, err := service.materializeWorkspaceChatSession(ctx)
	if err != nil {
		t.Fatalf("first MaterializeWorkspaceChatSession: %v", err)
	}
	second, err := service.materializeWorkspaceChatSession(ctx)
	if err != nil {
		t.Fatalf("second MaterializeWorkspaceChatSession: %v", err)
	}
	if first == second {
		t.Fatalf("separate materialization requests returned one Session identity %q", first.String())
	}
	page, err := metadataStore.ListSessionPage(ctx, serverapi.SessionPageRequest{
		ProjectID: binding.ProjectID,
		Category:  sessioncontract.SessionCategoryMain,
		PageSize:  10,
		Position:  serverapi.NewestSessionPagePosition(),
	})
	if err != nil {
		t.Fatalf("ListSessionPage: %v", err)
	}
	if len(page.Sessions) != 2 {
		t.Fatalf("visible Sessions = %+v, want two independent materializations", page.Sessions)
	}
}

func TestServiceResolutionFailureCreatesNoSession(t *testing.T) {
	ctx := context.Background()
	service, metadataStore, _, binding := newWorkspaceChatMaterializationService(t)
	wantErr := errors.New("reload failed")
	service.planner.ReloadConfig = func() (config.App, error) {
		return config.App{}, wantErr
	}
	if _, err := service.materializeWorkspaceChatSession(ctx); !errors.Is(err, wantErr) {
		t.Fatalf("MaterializeWorkspaceChatSession error = %v, want %v", err, wantErr)
	}
	page, err := metadataStore.ListSessionPage(ctx, serverapi.SessionPageRequest{
		ProjectID: binding.ProjectID,
		Category:  sessioncontract.SessionCategoryMain,
		PageSize:  10,
		Position:  serverapi.NewestSessionPagePosition(),
	})
	if err != nil {
		t.Fatalf("ListSessionPage: %v", err)
	}
	if len(page.Sessions) != 0 {
		t.Fatalf("resolution failure exposed Sessions: %+v", page.Sessions)
	}
}

func newWorkspaceChatMaterializationService(t *testing.T) (*Service, *metadata.Store, config.App, metadata.Binding) {
	t.Helper()
	workspace := t.TempDir()
	persistenceRoot := t.TempDir()
	cfg, err := config.Load(workspace, config.LoadOptions{ConfigRoot: persistenceRoot})
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	cfg.Settings.Model = "gpt-5.6-sol"
	cfg.Settings.ThinkingLevel = "medium"
	cfg.Settings.Reviewer.Frequency = "edits"
	cfg.Settings.Reviewer.Model = cfg.Settings.Model
	cfg.Settings.Reviewer.ThinkingLevel = cfg.Settings.ThinkingLevel
	metadataStore, err := metadata.Open(cfg.PersistenceRoot)
	if err != nil {
		t.Fatalf("metadata.Open: %v", err)
	}
	t.Cleanup(func() { _ = metadataStore.Close() })
	binding, err := metadataStore.RegisterWorkspaceBinding(context.Background(), cfg.WorkspaceRoot)
	if err != nil {
		t.Fatalf("RegisterWorkspaceBinding: %v", err)
	}
	containerDir := filepath.Join(cfg.PersistenceRoot, "projects", binding.ProjectID, "sessions")
	if err := os.MkdirAll(containerDir, 0o755); err != nil {
		t.Fatalf("MkdirAll Session container: %v", err)
	}
	service := NewService(launch.Planner{
		Config:                   cfg,
		ContainerDir:             containerDir,
		StoreOptions:             metadataStore.AuthoritativeSessionStoreOptions(),
		PersistedSessions:        metadataStore,
		ProjectWorkspaceBoundary: metadataStore,
	}).
		WithWorkspaceChatDraft(NewWorkspaceChatDraftOwner(metadataStore), binding.WorkspaceID, nil).
		WithWorkspaceChatMaterializationStoreOptions(metadataStore.WorkspaceChatMaterializationStoreOptions(binding.WorkspaceID)...)
	return service, metadataStore, cfg, binding
}
