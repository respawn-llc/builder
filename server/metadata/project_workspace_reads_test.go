package metadata

import (
	"context"
	"errors"
	"testing"

	"core/shared/serverapi"
)

func TestGetProjectWorkspaceCatalogRowSelectsExactAttachedWorkspace(t *testing.T) {
	store, _, source := newMetadataTestStore(t)
	attached, err := store.AttachWorkspaceToProject(context.Background(), source.ProjectID, t.TempDir())
	if err != nil {
		t.Fatalf("attach Workspace: %v", err)
	}
	selector, err := serverapi.NewProjectWorkspaceSelectorForID(attached.WorkspaceID)
	if err != nil {
		t.Fatalf("create ID selector: %v", err)
	}
	row, err := store.GetProjectWorkspaceCatalogRow(context.Background(), source.ProjectID, selector)
	if err != nil {
		t.Fatalf("get exact Project Workspace: %v", err)
	}
	if row.WorkspaceID != attached.WorkspaceID || row.CanonicalRoot != attached.CanonicalRoot || row.IsDefault {
		t.Fatalf("exact Project Workspace = %+v", row)
	}
}

func TestGetProjectWorkspaceCatalogRowDistinguishesMissingProjectFromNotAttachedWorkspace(t *testing.T) {
	store, _, source := newMetadataTestStore(t)
	selector, err := serverapi.NewProjectWorkspaceSelectorForID(source.WorkspaceID)
	if err != nil {
		t.Fatalf("create Workspace selector: %v", err)
	}
	if _, err := store.GetProjectWorkspaceCatalogRow(context.Background(), "missing-project", selector); !errors.Is(err, serverapi.ErrProjectNotFound) {
		t.Fatalf("missing Project error = %v, want ErrProjectNotFound", err)
	}

	other, err := store.CreateProjectForWorkspace(context.Background(), t.TempDir(), "Other")
	if err != nil {
		t.Fatalf("create other Project: %v", err)
	}
	foreignSelector, err := serverapi.NewProjectWorkspaceSelectorForID(other.WorkspaceID)
	if err != nil {
		t.Fatalf("create foreign Workspace selector: %v", err)
	}
	if _, err := store.GetProjectWorkspaceCatalogRow(context.Background(), source.ProjectID, foreignSelector); !errors.Is(err, serverapi.ErrWorkspaceNotRegistered) {
		t.Fatalf("not-attached Workspace error = %v, want ErrWorkspaceNotRegistered", err)
	}
}
