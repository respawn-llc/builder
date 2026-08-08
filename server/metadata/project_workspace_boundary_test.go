package metadata

import (
	"fmt"
	"testing"
)

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

func TestProjectWorkspaceBoundaryRejectsOverLimitAndLateInvalidEntries(t *testing.T) {
	roots := make([]ProjectWorkspace, ProjectWorkspaceCollectionLimit+1)
	for index := range roots {
		roots[index] = ProjectWorkspace{CanonicalRoot: "workspace-" + string(rune('a'+index%26)) + "-" + string(rune(index))}
	}
	if err := (ProjectWorkspaceBoundary{ProjectID: "project", Workspaces: roots}).Validate(); err == nil {
		t.Fatal("over-limit boundary was accepted")
	}

	lateInvalid := make([]ProjectWorkspace, ProjectWorkspaceCollectionLimit+1)
	for index := 0; index < ProjectWorkspaceCollectionLimit; index++ {
		lateInvalid[index] = ProjectWorkspace{CanonicalRoot: "valid-" + string(rune(index))}
	}
	if err := (ProjectWorkspaceBoundary{ProjectID: "project", Workspaces: append(lateInvalid[:ProjectWorkspaceCollectionLimit], ProjectWorkspace{})}).Validate(); err == nil {
		t.Fatal("boundary with invalid entry after the first 500 was accepted")
	}
}

func TestProjectWorkspaceBoundaryWithWorkspaceKeepsExactlyNewestBoundedEntries(t *testing.T) {
	workspaces := make([]ProjectWorkspace, ProjectWorkspaceCollectionLimit)
	for index := range workspaces {
		workspaces[index] = ProjectWorkspace{CanonicalRoot: fmt.Sprintf("workspace-%d", index)}
	}
	boundary := ProjectWorkspaceBoundary{ProjectID: "project", Workspaces: workspaces}
	next, added, err := boundary.WithWorkspace(ProjectWorkspace{CanonicalRoot: "newest"})
	if err != nil {
		t.Fatalf("WithWorkspace: %v", err)
	}
	if !added || len(next.Workspaces) != ProjectWorkspaceCollectionLimit {
		t.Fatalf("bounded insertion = len %d, added=%t, want len %d", len(next.Workspaces), added, ProjectWorkspaceCollectionLimit)
	}
	if next.Workspaces[0].CanonicalRoot != "newest" || next.Workspaces[ProjectWorkspaceCollectionLimit-1].CanonicalRoot != "workspace-498" {
		t.Fatalf("bounded insertion order = first %q, last %q", next.Workspaces[0].CanonicalRoot, next.Workspaces[ProjectWorkspaceCollectionLimit-1].CanonicalRoot)
	}
}
