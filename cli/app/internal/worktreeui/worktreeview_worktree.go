package worktreeui

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"unicode"

	worktreepb "core/shared/protoapi/gen/kent/api/worktree"
	"core/shared/textutil"
	"core/shared/worktreecontract"
)

var ErrMainWorkspaceNotDeletable = errors.New("main workspace is not deletable")

type Item struct {
	Entry           *worktreepb.ListEntry
	DisplayName     string
	CanonicalRoot   string
	WorktreeID      *string
	BranchName      *string
	Detached        bool
	IsMainWorkspace bool
	IsCurrent       bool
	Managed         bool
	CreatedBranch   bool
}

func ProjectItems(entries []*worktreepb.ListEntry) ([]Item, error) {
	out := make([]Item, 0, len(entries))
	for _, entry := range entries {
		item, err := ProjectItem(entry)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, nil
}

func ProjectItem(entry *worktreepb.ListEntry) (Item, error) {
	if entry == nil || entry.Topology == nil || entry.Projection == nil {
		return Item{}, errors.New("worktree list entry is incomplete")
	}
	item := Item{Entry: entry, IsCurrent: entry.Projection.IsCurrent}
	switch {
	case entry.Topology.GetMainWorkspace() != nil:
		git := entry.Topology.GetMainWorkspace().Git
		item.DisplayName = "main"
		item.CanonicalRoot = git.CanonicalRoot
		item.BranchName = git.BranchName
		item.Detached = git.Detached
		item.IsMainWorkspace = true
	case entry.Topology.GetRegistered() != nil:
		git := entry.Topology.GetRegistered().Git
		kent := entry.Topology.GetRegistered().Kent
		item.DisplayName = kent.DisplayName
		item.CanonicalRoot = git.CanonicalRoot
		item.WorktreeID = textutil.OptionalTrimmedString(kent.WorktreeId)
		item.BranchName = git.BranchName
		item.Detached = git.Detached
		item.Managed = kent.Managed
		item.CreatedBranch = kent.CreatedBranch
	case entry.Topology.GetExternal() != nil:
		git := entry.Topology.GetExternal().Git
		item.DisplayName = filepath.Base(git.CanonicalRoot)
		item.CanonicalRoot = git.CanonicalRoot
		item.BranchName = git.BranchName
		item.Detached = git.Detached
	case entry.Topology.GetMissing() != nil:
		kent := entry.Topology.GetMissing().Kent
		item.DisplayName = kent.DisplayName
		item.CanonicalRoot = kent.CanonicalRoot
		item.WorktreeID = textutil.OptionalTrimmedString(kent.WorktreeId)
		item.Managed = kent.Managed
		item.CreatedBranch = kent.CreatedBranch
	default:
		return Item{}, errors.New("unsupported worktree topology variant")
	}
	return item, nil
}

func ProjectSelectorPreview(response *worktreepb.SelectorResolveSuccess) (Item, error) {
	if response == nil {
		return Item{}, errors.New("worktree selector response is empty")
	}
	return ProjectItem(response.Worktree)
}

func WorktreeID(item Item) string {
	if item.WorktreeID == nil {
		return ""
	}
	return *item.WorktreeID
}

func BranchName(item Item) string {
	if item.BranchName == nil {
		return ""
	}
	return *item.BranchName
}

func DisplayName(item Item) string {
	return item.DisplayName
}

func SanitizeBranchSuggestion(raw string) string {
	trimmed := strings.TrimSpace(strings.ToLower(raw))
	if trimmed == "" {
		return ""
	}
	var builder strings.Builder
	lastDash := false
	for _, r := range trimmed {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			builder.WriteRune(r)
			lastDash = false
		case r == '/' || r == '-' || r == '_':
			if builder.Len() == 0 || lastDash {
				continue
			}
			builder.WriteRune('-')
			lastDash = true
		default:
			if builder.Len() == 0 || lastDash {
				continue
			}
			builder.WriteRune('-')
			lastDash = true
		}
	}
	result := strings.Trim(builder.String(), "-/")
	for strings.Contains(result, "--") {
		result = strings.ReplaceAll(result, "--", "-")
	}
	return result
}

func DeleteCanAutoDeleteBranch(item Item) bool {
	return item.Managed && item.CreatedBranch && item.BranchName != nil
}

func CanDelete(item Item) bool {
	return item.Entry != nil &&
		item.Entry.Projection != nil &&
		item.Entry.Projection.DeletePreview != nil
}

func ValidateDeletionTarget(item Item) error {
	if CanDelete(item) {
		return nil
	}
	if item.Entry != nil && item.Entry.Topology != nil && item.Entry.Topology.GetMainWorkspace() != nil {
		return ErrMainWorkspaceNotDeletable
	}
	return worktreecontract.ErrWorktreeBlocked
}

func ResolveCurrentDeletionTarget(entries []Item) (Item, error) {
	for _, item := range entries {
		if item.IsCurrent {
			if err := ValidateDeletionTarget(item); err != nil {
				return Item{}, fmt.Errorf("%w; choose another worktree", err)
			}
			return item, nil
		}
	}
	return Item{}, worktreecontract.ErrWorktreeNotFound
}
