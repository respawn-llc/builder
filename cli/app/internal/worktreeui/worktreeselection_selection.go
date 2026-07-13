package worktreeui

import (
	"errors"
	"strings"
)

var errInvalidSelectionIdentity = errors.New("worktree selection identity is invalid")

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

func SelectionIdentityForItem(item Item) (SelectionIdentity, error) {
	if item.WorktreeID != nil {
		worktreeID := strings.TrimSpace(*item.WorktreeID)
		if worktreeID == "" {
			return SelectionIdentity{}, errInvalidSelectionIdentity
		}
		return SelectionIdentity{Kind: SelectionIdentityKindKentWorktree, Value: worktreeID}, nil
	}
	root := strings.TrimSpace(item.CanonicalRoot)
	if root == "" {
		return SelectionIdentity{}, errInvalidSelectionIdentity
	}
	return SelectionIdentity{Kind: SelectionIdentityKindGitRoot, Value: root}, nil
}

func StableMutationSelector(item Item) (string, error) {
	identity, err := SelectionIdentityForItem(item)
	if err != nil {
		return "", err
	}
	switch identity.Kind {
	case SelectionIdentityKindKentWorktree, SelectionIdentityKindGitRoot:
	default:
		return "", errInvalidSelectionIdentity
	}
	return identity.Value, nil
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

func SelectedIdentity(entries []Item, selection int) (SelectionIdentity, error) {
	if item, ok := SelectedWorktree(entries, selection); ok {
		return SelectionIdentityForItem(item)
	}
	return SelectionIdentity{Kind: SelectionIdentityKindCreateRow}, nil
}

func FindByIdentity(entries []Item, identity SelectionIdentity) (Item, int, bool, error) {
	if identity.Kind == SelectionIdentityKindCreateRow {
		return Item{}, 0, false, nil
	}
	if (identity.Kind != SelectionIdentityKindKentWorktree &&
		identity.Kind != SelectionIdentityKindGitRoot) ||
		strings.TrimSpace(identity.Value) == "" {
		return Item{}, 0, false, errInvalidSelectionIdentity
	}
	for idx, item := range entries {
		itemIdentity, err := SelectionIdentityForItem(item)
		if err != nil {
			return Item{}, 0, false, err
		}
		if itemIdentity == identity {
			return item, idx, true, nil
		}
	}
	return Item{}, 0, false, nil
}

func Restore(entries []Item, currentSelection int, selectedIdentity SelectionIdentity) (int, error) {
	if selectedIdentity.Kind == SelectionIdentityKindCreateRow {
		return 0, nil
	}
	if _, idx, ok, err := FindByIdentity(entries, selectedIdentity); err != nil {
		return 0, err
	} else if ok {
		return idx + 1, nil
	}
	if len(entries) == 0 {
		return 0, nil
	}
	return Clamp(currentSelection, entries), nil
}
