package metadata

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"core/shared/serverapi"
)

func testDraft() WorkspaceChatDraftDocument {
	return WorkspaceChatDraftDocument{Message: "verbatim\nmessage", Agent: "default", Supervisor: "edits", Thinking: "medium", Fast: true, Questions: false, AutoCompaction: true}
}
func TestWorkspaceChatDraftDocumentStrictRoundTrip(t *testing.T) {
	want := testDraft(); body, err := json.Marshal(want); if err != nil { t.Fatal(err) }
	var got WorkspaceChatDraftDocument; if err := json.Unmarshal(body, &got); err != nil || got != want { t.Fatalf("got=%+v err=%v", got, err) }
	for _, raw := range []string{`{"message":"","agent":"default","supervisor":"edits","thinking":"medium","fast":false,"questions":false,"auto_compaction":true,"extra":true}`, `{"message":"","agent":"","supervisor":"edits","thinking":"medium","fast":false,"questions":false,"auto_compaction":true}`, `{"message":"","agent":"default","supervisor":"bad","thinking":"medium","fast":false,"questions":false,"auto_compaction":true}`} {
		var invalid WorkspaceChatDraftDocument; if json.Unmarshal([]byte(raw), &invalid) == nil { t.Fatal("invalid draft accepted") }
	}
}
func TestWorkspaceChatDraftPersistence(t *testing.T) {
	store, cfg, first := newMetadataTestStore(t); second, err := store.RegisterWorkspaceBinding(context.Background(), t.TempDir()); if err != nil { t.Fatal(err) }
	draft := testDraft(); if err := store.ReplaceWorkspaceChatDraft(context.Background(), first.WorkspaceID, &draft); err != nil { t.Fatal(err) }
	got, err := store.ReadWorkspaceChatDraft(context.Background(), first.WorkspaceID); if err != nil || got == nil || *got != draft { t.Fatalf("first=%+v err=%v", got, err) }
	other, err := store.ReadWorkspaceChatDraft(context.Background(), second.WorkspaceID); if err != nil || other != nil { t.Fatalf("second=%+v err=%v", other, err) }
	if err := store.Close(); err != nil { t.Fatal(err) }; reopened, err := Open(cfg.PersistenceRoot); if err != nil { t.Fatal(err) }; t.Cleanup(func() { _ = reopened.Close() })
	got, err = reopened.ReadWorkspaceChatDraft(context.Background(), first.WorkspaceID); if err != nil || got == nil || *got != draft { t.Fatalf("restart=%+v err=%v", got, err) }
	if err := reopened.ReplaceWorkspaceChatDraft(context.Background(), first.WorkspaceID, nil); err != nil { t.Fatal(err) }; got, err = reopened.ReadWorkspaceChatDraft(context.Background(), first.WorkspaceID); if err != nil || got != nil { t.Fatalf("clear=%+v err=%v", got, err) }
}
func TestWorkspaceChatDraftCorruptAndDetach(t *testing.T) {
	store, _, binding := newMetadataTestStore(t); if _, err := store.db.ExecContext(context.Background(), `UPDATE workspaces SET chat_draft_json = ? WHERE id = ?`, `{"message":"broken"}`, binding.WorkspaceID); err != nil { t.Fatal(err) }; if _, err := store.ReadWorkspaceChatDraft(context.Background(), binding.WorkspaceID); err == nil { t.Fatal("corrupt accepted") }
	attached, err := store.AttachWorkspaceToProject(context.Background(), binding.ProjectID, t.TempDir()); if err != nil { t.Fatal(err) }; draft := testDraft(); if err := store.ReplaceWorkspaceChatDraft(context.Background(), attached.WorkspaceID, &draft); err != nil { t.Fatal(err) }
	blockers, err := store.UnlinkProjectWorkspace(context.Background(), binding.ProjectID, attached.WorkspaceID); if err != nil || len(blockers) != 0 { t.Fatalf("blockers=%+v err=%v", blockers, err) }; if _, err := store.ReadWorkspaceChatDraft(context.Background(), attached.WorkspaceID); !errors.Is(err, serverapi.ErrWorkspaceNotRegistered) { t.Fatal(err) }
}
