package worktreeui

import (
	"errors"
	"testing"

	"core/shared/serverapi"
)

func TestProjectItemPreservesServerSelectorAndTopologyFacts(t *testing.T) {
	item := testWorktreeItem(t, "wt-1", "feature", "/wt/feature", "feature", false, true)
	if item.Entry.Projection.Selector != "feature" || WorktreeID(item) != "wt-1" || BranchName(item) != "feature" || !item.IsCurrent {
		t.Fatalf("item = %+v", item)
	}
}

func TestResolveTokenUsesOnlyServerProjectedSelector(t *testing.T) {
	item := testWorktreeItem(t, "wt-1", "feature", "/wt/feature", "feature", false, false)
	if _, err := ResolveToken([]Item{item}, "wt-1"); err == nil {
		t.Fatal("client-local ID lookup succeeded, want server-projected selector only")
	}
	resolved, err := ResolveToken([]Item{item}, "feature")
	if err != nil || WorktreeID(resolved) != "wt-1" {
		t.Fatalf("ResolveToken = %+v, %v", resolved, err)
	}
}

func TestResolveDeletionTargetRejectsCurrentMainWorkspace(t *testing.T) {
	item := testWorktreeItem(t, "wt-main", "main", "/repo", "main", true, true)
	_, err := ResolveDeletionTarget([]Item{item}, "")
	if err == nil || !errors.Is(err, ErrMainWorkspaceNotDeletable) {
		t.Fatalf("expected main workspace rejection, got %v", err)
	}
}

func TestResolveDeletionTargetFallsBackToNotFound(t *testing.T) {
	_, err := ResolveDeletionTarget(nil, "")
	if !errors.Is(err, serverapi.ErrWorktreeNotFound) {
		t.Fatalf("expected worktree not found, got %v", err)
	}
}

func TestSanitizeBranchSuggestion(t *testing.T) {
	if got := SanitizeBranchSuggestion(" Fix: My Feature!! "); got != "fix-my-feature" {
		t.Fatalf("suggestion = %q, want fix-my-feature", got)
	}
}

func testWorktreeItem(t *testing.T, id, name, root, branch string, main, current bool) Item {
	t.Helper()
	branchValue := branch
	entry := serverapi.WorktreeListEntry{
		Topology: serverapi.WorktreeTopologyEntry{
			Variant: serverapi.WorktreeTopologyVariantRegistered,
			Registered: &serverapi.WorktreeRegisteredFacts{
				Git: serverapi.WorktreeGitFacts{
					CanonicalRoot: root,
					HeadObject:    "deadbeef",
					BranchRef:     stringPointer("refs/heads/" + branch),
					BranchName:    &branchValue,
					IsMain:        main,
					PathAvailable: true,
				},
				Kent: serverapi.WorktreeKentFacts{
					WorktreeID:    id,
					CanonicalRoot: root,
					DisplayName:   name,
					Managed:       true,
					CreatedBranch: !main,
				},
			},
		},
		Projection: serverapi.WorktreeListProjection{Selector: branch, IsCurrent: current},
	}
	item, err := ProjectItem(entry)
	if err != nil {
		t.Fatalf("ProjectItem: %v", err)
	}
	return item
}
