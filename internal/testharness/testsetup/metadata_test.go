package testsetup

import (
	"context"
	"testing"

	"core/server/metadata"
)

func TestOpenStoreMaterializesIsolatedCurrentStores(t *testing.T) {
	first := OpenStore(t, t.TempDir())
	workspaceRoot := t.TempDir()
	if _, err := first.RegisterWorkspaceBinding(context.Background(), workspaceRoot); err != nil {
		t.Fatalf("RegisterWorkspaceBinding: %v", err)
	}

	second := OpenStore(t, t.TempDir())
	projects, err := second.ListProjects(context.Background())
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if len(projects) != 0 {
		t.Fatalf("second store projects = %+v, want isolated empty store", projects)
	}
}

func TestPrepareMetadataPersistenceRootIsIdempotent(t *testing.T) {
	persistenceRoot := t.TempDir()
	PrepareMetadataPersistenceRoot(t, persistenceRoot)
	PrepareMetadataPersistenceRoot(t, persistenceRoot)

	store, err := metadata.Open(persistenceRoot)
	if err != nil {
		t.Fatalf("open prepared metadata store: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close prepared metadata store: %v", err)
	}
}
