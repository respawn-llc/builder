package metadata

import (
	"context"
	"testing"
)

func testDraft() WorkspaceChatDraftDocument {
	return WorkspaceChatDraftDocument{Message: "verbatim\nmessage", Agent: "default", Supervisor: "edits", Thinking: "medium", Fast: true, Questions: false, AutoCompaction: true}
}
func TestWorkspaceChatDraftPersistence(t *testing.T) {
	store, cfg, first := newMetadataTestStore(t)
	second, err := store.RegisterWorkspaceBinding(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	draft := testDraft()
	if err := store.ReplaceWorkspaceChatDraft(context.Background(), first.WorkspaceID, &draft); err != nil {
		t.Fatal(err)
	}
	got, err := store.ReadWorkspaceChatDraft(context.Background(), first.WorkspaceID)
	if err != nil || got == nil || *got != draft {
		t.Fatalf("first=%+v err=%v", got, err)
	}
	other, err := store.ReadWorkspaceChatDraft(context.Background(), second.WorkspaceID)
	if err != nil || other != nil {
		t.Fatalf("second=%+v err=%v", other, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(cfg.PersistenceRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	got, err = reopened.ReadWorkspaceChatDraft(context.Background(), first.WorkspaceID)
	if err != nil || got == nil || *got != draft {
		t.Fatalf("restart=%+v err=%v", got, err)
	}
	if err := reopened.ReplaceWorkspaceChatDraft(context.Background(), first.WorkspaceID, nil); err != nil {
		t.Fatal(err)
	}
	got, err = reopened.ReadWorkspaceChatDraft(context.Background(), first.WorkspaceID)
	if err != nil || got != nil {
		t.Fatalf("clear=%+v err=%v", got, err)
	}
}
