package worktree

import (
	worktreepb "core/shared/protoapi/gen/kent/api/worktree"
	"core/shared/worktreecontract"
	"errors"
	"testing"
)

func TestTopologySelectorPrecedenceAndRoundTrip(t *testing.T) {
	branch := "feature"
	entries := []*worktreepb.TopologyEntry{
		{Topology: &worktreepb.TopologyEntry_Registered{Registered: &worktreepb.RegisteredFacts{
			Git:  &worktreepb.GitFacts{CanonicalRoot: "/repo/one/feature", HeadObject: "one", BranchName: &branch, PathAvailable: true},
			Kent: &worktreepb.KentFacts{WorktreeId: "id-one", CanonicalRoot: "/repo/one/feature", DisplayName: "one"},
		}}},
		{Topology: &worktreepb.TopologyEntry_Missing{Missing: &worktreepb.MissingFacts{
			Kent: &worktreepb.KentFacts{WorktreeId: "feature", CanonicalRoot: "/repo/two/missing", DisplayName: "two"},
		}}},
		{Topology: &worktreepb.TopologyEntry_External{External: &worktreepb.ExternalFacts{
			Git: &worktreepb.GitFacts{CanonicalRoot: "/repo/three/feature", HeadObject: "three", PathAvailable: true},
		}}},
	}
	match, err := resolveTopologySelector(entries, "feature")
	if err != nil {
		t.Fatalf("Resolve UUID collision: %v", err)
	}
	if match.index != 1 {
		t.Fatalf("selector precedence resolved index %d, want UUID row 1", match.index)
	}
	for index := range entries {
		selector, err := topologySelectorFor(entries, index)
		if err != nil {
			t.Fatalf("selector for row %d: %v", index, err)
		}
		match, err := resolveTopologySelector(entries, selector)
		if err != nil || match.index != index {
			t.Fatalf("round trip row %d selector %q resolved %d err=%v", index, selector, match.index, err)
		}
	}
}

func TestTopologySelectorReportsAmbiguousPath(t *testing.T) {
	entries := []*worktreepb.TopologyEntry{
		externalTopologyEntry("/repo/one/feature"),
		externalTopologyEntry("/repo/two/feature"),
	}
	_, err := resolveTopologySelector(entries, "feature")
	var ambiguous *worktreecontract.SelectorError
	if !errors.As(err, &ambiguous) ||
		ambiguous.Details.Kind != worktreepb.SelectorErrorKind_WORKTREE_SELECTOR_ERROR_KIND_AMBIGUOUS ||
		len(ambiguous.Details.Candidates) != 2 {
		t.Fatalf("ambiguous path error = %T %+v", err, ambiguous)
	}
	match, err := resolveTopologySelector(entries, "one/feature")
	if err != nil || match.index != 0 {
		t.Fatalf("unique component path resolved %+v err=%v", match, err)
	}
}

func externalTopologyEntry(root string) *worktreepb.TopologyEntry {
	return &worktreepb.TopologyEntry{Topology: &worktreepb.TopologyEntry_External{
		External: &worktreepb.ExternalFacts{
			Git: &worktreepb.GitFacts{CanonicalRoot: root, HeadObject: root, PathAvailable: true},
		},
	}}
}
