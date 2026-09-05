package main

import (
	"testing"

	worktreepb "core/shared/protoapi/gen/kent/api/worktree"
)

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
