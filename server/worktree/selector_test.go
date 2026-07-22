package worktree

import (
	"errors"
	"testing"

	"core/shared/serverapi"
)

func TestTopologySelectorPrecedenceAndRoundTrip(t *testing.T) {
	branch := "feature"
	entries := []serverapi.WorktreeTopologyEntry{
		{
			Variant: serverapi.WorktreeTopologyVariantRegistered,
			Registered: &serverapi.WorktreeRegisteredFacts{
				Git:  serverapi.WorktreeGitFacts{CanonicalRoot: "/repo/one/feature", HeadObject: "one", BranchName: &branch, PathAvailable: true},
				Kent: serverapi.WorktreeKentFacts{WorktreeID: "id-one", CanonicalRoot: "/repo/one/feature", DisplayName: "one"},
			},
		},
		{
			Variant: serverapi.WorktreeTopologyVariantMissing,
			Missing: &serverapi.WorktreeMissingFacts{
				Kent: serverapi.WorktreeKentFacts{WorktreeID: "feature", CanonicalRoot: "/repo/two/missing", DisplayName: "two"},
			},
		},
		{
			Variant: serverapi.WorktreeTopologyVariantExternal,
			External: &serverapi.WorktreeExternalFacts{
				Git: serverapi.WorktreeGitFacts{CanonicalRoot: "/repo/three/feature", HeadObject: "three", PathAvailable: true},
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
	entries := []serverapi.WorktreeTopologyEntry{
		externalTopologyEntry("/repo/one/feature"),
		externalTopologyEntry("/repo/two/feature"),
	}
	_, err := resolveTopologySelector(entries, "feature")
	var ambiguous *serverapi.WorktreeSelectorError
	if !errors.As(err, &ambiguous) || ambiguous.Kind != serverapi.WorktreeSelectorErrorKindAmbiguous || len(ambiguous.Candidates) != 2 {
		t.Fatalf("ambiguous path error = %T %+v", err, ambiguous)
	}
	match, err := resolveTopologySelector(entries, "one/feature")
	if err != nil || match.index != 0 {
		t.Fatalf("unique component path resolved %+v err=%v", match, err)
	}
}

func externalTopologyEntry(root string) serverapi.WorktreeTopologyEntry {
	return serverapi.WorktreeTopologyEntry{
		Variant: serverapi.WorktreeTopologyVariantExternal,
		External: &serverapi.WorktreeExternalFacts{
			Git: serverapi.WorktreeGitFacts{CanonicalRoot: root, HeadObject: root, PathAvailable: true},
		},
	}
}
