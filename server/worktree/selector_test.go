package worktree

import (
	"core/shared/worktreecontract"
	"errors"
	"testing"
)

func TestTopologySelectorPrecedenceAndRoundTrip(t *testing.T) {
	branch := "feature"
	entries := []worktreecontract.TopologyEntry{
		{
			Variant: worktreecontract.TopologyVariantRegistered,
			Registered: &worktreecontract.RegisteredFacts{
				Git:  worktreecontract.GitFacts{CanonicalRoot: "/repo/one/feature", HeadObject: "one", BranchName: &branch, PathAvailable: true},
				Kent: worktreecontract.KentFacts{WorktreeID: "id-one", CanonicalRoot: "/repo/one/feature", DisplayName: "one"},
			},
		},
		{
			Variant: worktreecontract.TopologyVariantMissing,
			Missing: &worktreecontract.MissingFacts{
				Kent: worktreecontract.KentFacts{WorktreeID: "feature", CanonicalRoot: "/repo/two/missing", DisplayName: "two"},
			},
		},
		{
			Variant: worktreecontract.TopologyVariantExternal,
			External: &worktreecontract.ExternalFacts{
				Git: worktreecontract.GitFacts{CanonicalRoot: "/repo/three/feature", HeadObject: "three", PathAvailable: true},
			},
		},
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
	entries := []worktreecontract.TopologyEntry{
		externalTopologyEntry("/repo/one/feature"),
		externalTopologyEntry("/repo/two/feature"),
	}
	_, err := resolveTopologySelector(entries, "feature")
	var ambiguous *worktreecontract.SelectorError
	if !errors.As(err, &ambiguous) || ambiguous.Kind != worktreecontract.SelectorErrorKindAmbiguous || len(ambiguous.Candidates) != 2 {
		t.Fatalf("ambiguous path error = %T %+v", err, ambiguous)
	}
	match, err := resolveTopologySelector(entries, "one/feature")
	if err != nil || match.index != 0 {
		t.Fatalf("unique component path resolved %+v err=%v", match, err)
	}
}

func externalTopologyEntry(root string) worktreecontract.TopologyEntry {
	return worktreecontract.TopologyEntry{
		Variant: worktreecontract.TopologyVariantExternal,
		External: &worktreecontract.ExternalFacts{
			Git: worktreecontract.GitFacts{CanonicalRoot: root, HeadObject: root, PathAvailable: true},
		},
	}
}
