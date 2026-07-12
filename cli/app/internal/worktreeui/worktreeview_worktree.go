package worktreeui

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"unicode"

	"core/shared/serverapi"
)

var ErrMainWorkspaceNotDeletable = errors.New("main workspace is not deletable")

type Item struct {
	Entry         serverapi.WorktreeListEntry
	DisplayName   string
	CanonicalRoot string
	WorktreeID    *string
	BranchName    *string
	Detached      bool
	IsMain        bool
	IsCurrent     bool
	Managed       bool
	CreatedBranch bool
}

func ProjectItems(entries []serverapi.WorktreeListEntry) ([]Item, error) {
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

func ProjectItem(entry serverapi.WorktreeListEntry) (Item, error) {
	if err := entry.Validate(); err != nil {
		return Item{}, err
	}
	item := Item{Entry: entry, IsCurrent: entry.Projection.IsCurrent}
	switch entry.Topology.Variant {
	case serverapi.WorktreeTopologyVariantRegistered:
		git := entry.Topology.Registered.Git
		kent := entry.Topology.Registered.Kent
		item.DisplayName = kent.DisplayName
		item.CanonicalRoot = git.CanonicalRoot
		item.WorktreeID = stringPointer(kent.WorktreeID)
		item.BranchName = git.BranchName
		item.Detached = git.Detached
		item.IsMain = git.IsMain
		item.Managed = kent.Managed
		item.CreatedBranch = kent.CreatedBranch
	case serverapi.WorktreeTopologyVariantExternal:
		git := entry.Topology.External.Git
		item.DisplayName = filepath.Base(git.CanonicalRoot)
		item.CanonicalRoot = git.CanonicalRoot
		item.BranchName = git.BranchName
		item.Detached = git.Detached
		item.IsMain = git.IsMain
	case serverapi.WorktreeTopologyVariantMissing:
		kent := entry.Topology.Missing.Kent
		item.DisplayName = kent.DisplayName
		item.CanonicalRoot = kent.CanonicalRoot
		item.WorktreeID = stringPointer(kent.WorktreeID)
		item.Managed = kent.Managed
		item.CreatedBranch = kent.CreatedBranch
	default:
		return Item{}, fmt.Errorf("unsupported worktree topology variant %q", entry.Topology.Variant)
	}
	return item, nil
}

func stringPointer(value string) *string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil
	}
	return &trimmed
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

func ResolveDeletionTarget(entries []Item, token string) (Item, error) {
	trimmedToken := strings.TrimSpace(token)
	if trimmedToken != "" {
		return ResolveToken(entries, trimmedToken)
	}
	for _, item := range entries {
		if item.IsCurrent {
			if item.IsMain {
				return Item{}, fmt.Errorf("%w; choose another worktree", ErrMainWorkspaceNotDeletable)
			}
			return item, nil
		}
	}
	return Item{}, serverapi.ErrWorktreeNotFound
}

func ResolveToken(entries []Item, token string) (Item, error) {
	trimmedToken := strings.TrimSpace(token)
	if trimmedToken == "" {
		return Item{}, serverapi.ErrWorktreeNotFound
	}
	for _, item := range entries {
		if item.Entry.Projection.Selector == trimmedToken {
			return item, nil
		}
	}
	return Item{}, fmt.Errorf("worktree selector %q is not present in the current list", trimmedToken)
}
