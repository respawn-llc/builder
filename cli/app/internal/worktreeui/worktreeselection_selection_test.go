package worktreeui

import "testing"

func TestSelectionUsesCreateRowAndServerSelectors(t *testing.T) {
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
	if got := SelectedID(entries, 0); got != CreateRowID {
		t.Fatalf("create selection id = %q", got)
	}
	if got := Restore(entries, 0, "two"); got != 2 {
		t.Fatalf("restored selection = %d", got)
	}
}
