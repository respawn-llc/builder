package main

import (
	"path/filepath"
	"testing"

	worktreepb "core/shared/protoapi/gen/kent/api/worktree"
)

func TestWorktreeTransitionResolutionPathUsesAbsoluteSelector(t *testing.T) {
	workspaceRoot := filepath.Join(t.TempDir(), "current-workspace")
	targetWorktree := filepath.Join(t.TempDir(), "target-worktree")
	if got := worktreeTransitionResolutionPath(targetWorktree, workspaceRoot); got != targetWorktree {
		t.Fatalf("absolute selector resolution path = %q, want %q", got, targetWorktree)
	}
	if got := worktreeTransitionResolutionPath("feature/branch", workspaceRoot); got != workspaceRoot {
		t.Fatalf("concise selector resolution path = %q, want current Workspace %q", got, workspaceRoot)
	}
}

func TestWorktreeTopologyVariantJSONIncludesMainWorkspace(t *testing.T) {
	topology := &worktreepb.TopologyEntry{
		Topology: &worktreepb.TopologyEntry_MainWorkspace{
			MainWorkspace: &worktreepb.MainWorkspaceFacts{
				Git: &worktreepb.GitFacts{
					CanonicalRoot: "/repo",
					HeadObject:    "head",
				},
			},
		},
	}
	if got, err := worktreeTopologyVariantJSON(topology); err != nil || got != "main_workspace" {
		t.Fatalf("worktreeTopologyVariantJSON = %q, %v; want main_workspace", got, err)
	}
}
