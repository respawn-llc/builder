package testsetup

import (
	"context"
	"testing"
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
