package worktreeui

const CreateRowID = "__create__"

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

func SelectedID(entries []Item, selection int) string {
	if item, ok := SelectedWorktree(entries, selection); ok {
		return item.Entry.Projection.Selector
	}
	return CreateRowID
}

func Restore(entries []Item, currentSelection int, selectedID string) int {
	trimmed := selectedID
	if trimmed == "" || trimmed == CreateRowID {
		return 0
	}
	for idx, item := range entries {
		if item.Entry.Projection.Selector == trimmed {
			return idx + 1
		}
	}
	if len(entries) == 0 {
		return 0
	}
	return Clamp(currentSelection, entries)
}
