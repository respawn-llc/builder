package worktreeui

import (
	"errors"
	"testing"

	worktreepb "core/shared/protoapi/gen/kent/api/worktree"
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

func TestProjectItemKeepsMainWorkspacePresentationSeparateFromGitMainMarker(t *testing.T) {
	mainWorkspace := &worktreepb.ListEntry{
		Topology: &worktreepb.TopologyEntry{
			Topology: &worktreepb.TopologyEntry_MainWorkspace{
				MainWorkspace: &worktreepb.MainWorkspaceFacts{
					Git: &worktreepb.GitFacts{
						CanonicalRoot:  "/repo/linked",
						HeadObject:     "deadbeef",
						IsMainWorktree: false,
						PathAvailable:  true,
					},
				},
			},
		},
		Projection: &worktreepb.ListProjection{Selector: "linked", IsCurrent: true},
	}
	gitMain := &worktreepb.ListEntry{
		Topology: &worktreepb.TopologyEntry{
			Topology: &worktreepb.TopologyEntry_External{
				External: &worktreepb.ExternalFacts{
					Git: &worktreepb.GitFacts{
						CanonicalRoot:  "/repo/main",
						HeadObject:     "deadbeef",
						IsMainWorktree: true,
						PathAvailable:  true,
					},
				},
			},
		},
		Projection: &worktreepb.ListProjection{
			Selector: "main",
			Switch: &worktreepb.SwitchOperation{
				Kind:     worktreepb.SwitchOperationKind_WORKTREE_SWITCH_OPERATION_ENTER,
				Selector: stringPtr("main"),
			},
		},
	}
	items, err := ProjectItems([]*worktreepb.ListEntry{mainWorkspace, gitMain})
	if err != nil {
		t.Fatalf("ProjectItems: %v", err)
	}
	if !items[0].IsMainWorkspace {
		t.Fatalf("Main Workspace item = %+v", items[0])
	}
	if items[1].IsMainWorkspace {
		t.Fatalf("Git main item = %+v", items[1])
	}
	if got := DeleteActions(items[0]); len(got) != 1 || got[0] != DeleteActionCancel {
		t.Fatalf("Main Workspace delete actions = %+v, want cancel only", got)
	}
	if got := DeleteActions(items[1]); len(got) != 1 || got[0] != DeleteActionCancel {
		t.Fatalf("Git main delete actions = %+v, want cancel only", got)
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
	entry := &worktreepb.ListEntry{
		Topology: &worktreepb.TopologyEntry{
			Topology: &worktreepb.TopologyEntry_Registered{
				Registered: &worktreepb.RegisteredFacts{
					Git: &worktreepb.GitFacts{
						CanonicalRoot: root,
						HeadObject:    "deadbeef",
						BranchRef:     textutil.OptionalTrimmedString("refs/heads/" + branch),
						BranchName:    &branchValue,
						PathAvailable: true,
					},
					Kent: &worktreepb.KentFacts{
						WorktreeId:    id,
						CanonicalRoot: root,
						DisplayName:   name,
						Managed:       true,
						CreatedBranch: !main,
					},
				},
			},
		},
		Projection: &worktreepb.ListProjection{
			Selector: branch, IsCurrent: current,
			DeletePreview: func() *worktreepb.DeletePreviewOperation {
				if main {
					return nil
				}
				return &worktreepb.DeletePreviewOperation{Selector: id}
			}(),
		},
	}
	item, err := ProjectItem(entry)
	if err != nil {
		t.Fatalf("ProjectItem: %v", err)
	}
	return item
}

func stringPtr(value string) *string {
	return &value
}
