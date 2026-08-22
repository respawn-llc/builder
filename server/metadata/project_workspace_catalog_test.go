package metadata

import (
	"context"
	"errors"
	"testing"

	"core/shared/serverapi"
)

func TestListProjectWorkspaceCatalogPageOrderingAndFormerLimit(t *testing.T) {
	store, _, source := newMetadataTestStore(t)
	ctx := context.Background()
	var oldest Binding
	const formerLimit = 500
	for index := 0; index < formerLimit; index++ {
		binding, err := store.AttachWorkspaceToProject(ctx, source.ProjectID, t.TempDir())
		if err != nil {
			t.Fatalf("attach %d: %v", index, err)
		}
		if index == 0 {
			oldest = binding
		}
	}
	if _, err := store.db.ExecContext(ctx, "UPDATE workspaces SET created_at_unix_ms = 1 WHERE id = ?", oldest.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	first, err := store.ListProjectWorkspaceCatalogPage(ctx, source.ProjectID, 0, MaxProjectWorkspacePageSize)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Workspaces) != MaxProjectWorkspacePageSize || !first.Workspaces[0].IsDefault ||
		first.NextOffset == nil || *first.NextOffset != MaxProjectWorkspacePageSize {
		t.Fatalf("first page = %+v", first)
	}
	last, err := store.ListProjectWorkspaceCatalogPage(ctx, source.ProjectID, formerLimit, MaxProjectWorkspacePageSize)
	if err != nil {
		t.Fatal(err)
	}
	if len(last.Workspaces) != 1 || last.Workspaces[0].WorkspaceID != oldest.WorkspaceID || last.NextOffset != nil {
		t.Fatalf("last page = %+v", last)
	}
}

func TestListProjectWorkspaceCatalogPageUsesWorkspaceIDForTimestampTies(t *testing.T) {
	store, _, source := newMetadataTestStore(t)
	ctx := context.Background()
	first, err := store.AttachWorkspaceToProject(ctx, source.ProjectID, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.AttachWorkspaceToProject(ctx, source.ProjectID, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, "UPDATE workspaces SET created_at_unix_ms = 1, id = ? WHERE id = ?", "workspace-z-tie", first.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, "UPDATE workspaces SET created_at_unix_ms = 1, id = ? WHERE id = ?", "workspace-a-tie", second.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	page, err := store.ListProjectWorkspaceCatalogPage(ctx, source.ProjectID, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if got := []string{page.Workspaces[1].WorkspaceID, page.Workspaces[2].WorkspaceID}; got[0] != "workspace-a-tie" || got[1] != "workspace-z-tie" {
		t.Fatalf("tied Workspace order = %v", got)
	}
}

func TestListProjectWorkspaceCatalogPageDistinguishesMissingProjectAndPastEnd(t *testing.T) {
	store, _, source := newMetadataTestStore(t)
	if _, err := store.ListProjectWorkspaceCatalogPage(context.Background(), "missing-project", 0, 10); !errors.Is(err, serverapi.ErrProjectNotFound) {
		t.Fatalf("missing Project error = %v", err)
	}
	page, err := store.ListProjectWorkspaceCatalogPage(context.Background(), source.ProjectID, 10_000, 10)
	if err != nil || len(page.Workspaces) != 0 || page.NextOffset != nil {
		t.Fatalf("past-end page = %+v, error = %v", page, err)
	}
}
