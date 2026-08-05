package metadata

import "testing"

func TestProjectWorkspaceBoundaryWithWorkspaceKeepsNewestBoundedMembership(t *testing.T) {
	boundary := ProjectWorkspaceBoundary{ProjectID: "project", Workspaces: []ProjectWorkspace{
		{CanonicalRoot: "newer"}, {CanonicalRoot: "older"},
	}}
	next, added, err := boundary.WithWorkspace(ProjectWorkspace{CanonicalRoot: "newest"})
	if err != nil || !added || len(next.Workspaces) != 3 || next.Workspaces[0].CanonicalRoot != "newest" {
		t.Fatalf("bounded membership = %+v, added=%t, error=%v", next, added, err)
	}
	duplicate, added, err := next.WithWorkspace(next.Workspaces[1])
	if err != nil || added || len(duplicate.Workspaces) != 3 {
		t.Fatalf("duplicate membership changed boundary = %+v, added=%t, error=%v", duplicate, added, err)
	}
}
