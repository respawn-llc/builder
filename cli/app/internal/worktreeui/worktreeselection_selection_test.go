package worktreeui

import "testing"

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
	if got := SelectedIdentity(entries, 0); got.Kind != SelectionIdentityKindCreateRow {
		t.Fatalf("create selection identity = %+v", got)
	}
	if got := Restore(entries, 0, SelectionIdentityForItem(entries[1])); got != 2 {
		t.Fatalf("restored selection = %d", got)
	}
}

func TestSelectionRestoresRegisteredWorktreeByKentIDWhenSelectorChanges(t *testing.T) {
	selected := testWorktreeItem(t, "wt-1", "one", "/wt/one", "old-selector", false, false)
	selectedIdentity := SelectedIdentity([]Item{selected}, 1)
	if selectedIdentity.Kind != SelectionIdentityKindKentWorktree || selectedIdentity.Value != "wt-1" {
		t.Fatalf("selected identity = %+v, want Kent worktree wt-1", selectedIdentity)
	}

	other := testWorktreeItem(t, "wt-2", "two", "/wt/two", "old-selector", false, false)
	refreshed := testWorktreeItem(t, "wt-1", "renamed", "/wt/one", "new-selector", false, false)
	if got := Restore([]Item{other, refreshed}, 1, selectedIdentity); got != 2 {
		t.Fatalf("restored selection = %d, want 2", got)
	}
}
