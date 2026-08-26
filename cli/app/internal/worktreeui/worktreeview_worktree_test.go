package worktreeui

import (
	"errors"
	"testing"

	"core/shared/textutil"
	"core/shared/worktreecontract"
)

func TestProjectItemPreservesServerSelectorAndTopologyFacts(t *testing.T) {
	item := testWorktreeItem(t, "wt-1", "feature", "/wt/feature", "feature", false, true)
	if item.Entry.Projection.Selector != "feature" || WorktreeID(item) != "wt-1" || BranchName(item) != "feature" || !item.IsCurrent {
		t.Fatalf("item = %+v", item)
	}
}

func TestResolveCurrentDeletionTargetRejectsMainWorkspace(t *testing.T) {
	item := testWorktreeItem(t, "wt-main", "main", "/repo", "main", true, true)
	_, err := ResolveCurrentDeletionTarget([]Item{item})
	if err == nil || !errors.Is(err, ErrMainWorkspaceNotDeletable) {
		t.Fatalf("expected main workspace rejection, got %v", err)
	}
}

func TestResolveCurrentDeletionTargetFallsBackToNotFound(t *testing.T) {
	_, err := ResolveCurrentDeletionTarget(nil)
	if !errors.Is(err, worktreecontract.ErrWorktreeNotFound) {
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
	entry := worktreecontract.ListEntry{
		Topology: worktreecontract.TopologyEntry{
			Variant: worktreecontract.TopologyVariantRegistered,
			Registered: &worktreecontract.RegisteredFacts{
				Git: worktreecontract.GitFacts{
					CanonicalRoot: root,
					HeadObject:    "deadbeef",
					BranchRef:     textutil.OptionalTrimmedString("refs/heads/" + branch),
					BranchName:    &branchValue,
					IsMain:        main,
					PathAvailable: true,
				},
				Kent: worktreecontract.KentFacts{
					WorktreeID:    id,
					CanonicalRoot: root,
					DisplayName:   name,
					Managed:       true,
					CreatedBranch: !main,
				},
			},
		},
		Projection: worktreecontract.ListProjection{Selector: branch, IsCurrent: current},
	}
	item, err := ProjectItem(entry)
	if err != nil {
		t.Fatalf("ProjectItem: %v", err)
	}
	return item
}
