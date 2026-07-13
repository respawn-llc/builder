package worktreeui

type SelectionIdentityKind uint8

const (
	SelectionIdentityKindCreateRow SelectionIdentityKind = iota
	SelectionIdentityKindKentWorktree
	SelectionIdentityKindGitRoot
)

type SelectionIdentity struct {
	Kind  SelectionIdentityKind
	Value string
}

func SelectionIdentityForItem(item Item) SelectionIdentity {
	if worktreeID := WorktreeID(item); worktreeID != "" {
		return SelectionIdentity{Kind: SelectionIdentityKindKentWorktree, Value: worktreeID}
	}
	return SelectionIdentity{Kind: SelectionIdentityKindGitRoot, Value: item.CanonicalRoot}
}

func RowCount(entries []Item) int {
	return len(entries) + 1
}

func Clamp(selection int, entries []Item) int {
	rowCount := RowCount(entries)
	if rowCount <= 0 {
		return 0
	}
	if selection < 0 {
		return 0
	}
	if selection >= rowCount {
		return rowCount - 1
	}
	return selection
}

func SelectedWorktree(entries []Item, selection int) (Item, bool) {
	if selection <= 0 {
		return Item{}, false
	}
	index := selection - 1
	if index < 0 || index >= len(entries) {
		return Item{}, false
	}
	return entries[index], true
}

func SelectedIdentity(entries []Item, selection int) SelectionIdentity {
	if item, ok := SelectedWorktree(entries, selection); ok {
		return SelectionIdentityForItem(item)
	}
	return SelectionIdentity{Kind: SelectionIdentityKindCreateRow}
}

func FindByIdentity(entries []Item, identity SelectionIdentity) (Item, int, bool) {
	if identity.Kind == SelectionIdentityKindCreateRow {
		return Item{}, 0, false
	}
	for idx, item := range entries {
		if SelectionIdentityForItem(item) == identity {
			return item, idx, true
		}
	}
	return Item{}, 0, false
}

func Restore(entries []Item, currentSelection int, selectedIdentity SelectionIdentity) int {
	if selectedIdentity.Kind == SelectionIdentityKindCreateRow {
		return 0
	}
	if _, idx, ok := FindByIdentity(entries, selectedIdentity); ok {
		return idx + 1
	}
	if len(entries) == 0 {
		return 0
	}
	return Clamp(currentSelection, entries)
}
