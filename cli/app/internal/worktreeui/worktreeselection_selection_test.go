package worktreeui

import (
	"testing"

	"core/shared/serverapi"
)

func TestSelectionUsesCreateRowAndStableIdentities(t *testing.T) {
	entries := []Item{
		testWorktreeItem(t, "wt-1", "one", "/wt/one", "one", false, false),
		testWorktreeItem(t, "wt-2", "two", "/wt/two", "two", false, false),
	}
	if got := Clamp(-1, entries); got != 0 {
		t.Fatalf("negative selection = %d", got)
	}
	if got := Clamp(9, entries); got != 2 {
		t.Fatalf("overflow selection = %d", got)
	}
	item, ok := SelectedWorktree(entries, 2)
	if !ok || WorktreeID(item) != "wt-2" {
		t.Fatalf("selected = %+v ok=%v", item, ok)
	}
	if got, err := SelectedIdentity(entries, 0); err != nil || got.Kind != SelectionIdentityKindCreateRow {
		t.Fatalf("create selection identity = %+v, %v", got, err)
	}
	selectedIdentity, err := SelectionIdentityForItem(entries[1])
	if err != nil {
		t.Fatalf("SelectionIdentityForItem: %v", err)
	}
	if got, err := Restore(entries, 0, selectedIdentity); err != nil || got != 2 {
		t.Fatalf("restored selection = %d, %v", got, err)
	}
}

func TestSelectionRestoresRegisteredWorktreeByKentIDWhenSelectorChanges(t *testing.T) {
	selected := testWorktreeItem(t, "wt-1", "one", "/wt/one", "old-selector", false, false)
	selectedIdentity, err := SelectedIdentity([]Item{selected}, 1)
	if err != nil {
		t.Fatalf("SelectedIdentity: %v", err)
	}
	if selectedIdentity.Kind != SelectionIdentityKindKentWorktree || selectedIdentity.Value != "wt-1" {
		t.Fatalf("selected identity = %+v, want Kent worktree wt-1", selectedIdentity)
	}

	other := testWorktreeItem(t, "wt-2", "two", "/wt/two", "old-selector", false, false)
	refreshed := testWorktreeItem(t, "wt-1", "renamed", "/wt/one", "new-selector", false, false)
	if got, err := Restore([]Item{other, refreshed}, 1, selectedIdentity); err != nil || got != 2 {
		t.Fatalf("restored selection = %d, %v, want 2", got, err)
	}
}

func TestSelectionIdentityRejectsInvalidPresentKentID(t *testing.T) {
	invalidID := " "
	if _, err := SelectionIdentityForItem(Item{
		WorktreeID:    &invalidID,
		CanonicalRoot: "/wt/feature",
	}); err == nil {
		t.Fatal("expected present invalid Kent ID to fail instead of falling back to root")
	}
}

func TestStableMutationSelectorUsesExternalCanonicalRoot(t *testing.T) {
	item, err := ProjectItem(serverapi.WorktreeListEntry{
		Topology: serverapi.WorktreeTopologyEntry{
			Variant: serverapi.WorktreeTopologyVariantExternal,
			External: &serverapi.WorktreeExternalFacts{
				Git: serverapi.WorktreeGitFacts{
					CanonicalRoot: "/wt/external",
					HeadObject:    "deadbeef",
					Detached:      true,
					PathAvailable: true,
				},
			},
		},
		Projection: serverapi.WorktreeListProjection{Selector: "external"},
	})
	if err != nil {
		t.Fatalf("ProjectItem: %v", err)
	}

	selector, err := StableMutationSelector(item)
	if err != nil {
		t.Fatalf("StableMutationSelector: %v", err)
	}
	if selector != "/wt/external" {
		t.Fatalf("selector = %q, want external canonical root", selector)
	}
}
