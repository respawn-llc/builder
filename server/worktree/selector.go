package worktree

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"core/shared/serverapi"
)

type topologySelectorMatch struct {
	index int
	entry serverapi.WorktreeTopologyEntry
}

type scheduledWorktreeTarget struct {
	worktreeID    *string
	canonicalRoot string
}

func scheduledWorktreeTargetFromEntry(entry serverapi.WorktreeTopologyEntry) (scheduledWorktreeTarget, error) {
	root := strings.TrimSpace(topologyRoot(entry))
	if root == "" {
		return scheduledWorktreeTarget{}, errors.New("scheduled worktree target requires a canonical root")
	}
	target := scheduledWorktreeTarget{canonicalRoot: root}
	if id := topologyWorktreeID(entry); id != nil {
		value := strings.TrimSpace(*id)
		if value == "" {
			return scheduledWorktreeTarget{}, errors.New("scheduled worktree target has an invalid Kent worktree id")
		}
		target.worktreeID = &value
	}
	return target, nil
}

func scheduledKentWorktreeTargetFromEntry(entry serverapi.WorktreeTopologyEntry) (scheduledWorktreeTarget, error) {
	target, err := scheduledWorktreeTargetFromEntry(entry)
	if err != nil {
		return scheduledWorktreeTarget{}, err
	}
	if target.worktreeID == nil {
		return scheduledWorktreeTarget{}, errors.New("scheduled worktree target requires a Kent worktree id")
	}
	return target, nil
}

func (target scheduledWorktreeTarget) resolve(topology []serverapi.WorktreeTopologyEntry) (serverapi.WorktreeTopologyEntry, error) {
	root := strings.TrimSpace(target.canonicalRoot)
	if root == "" {
		return serverapi.WorktreeTopologyEntry{}, errors.New("scheduled worktree target identity is invalid")
	}
	if target.worktreeID != nil {
		worktreeID := strings.TrimSpace(*target.worktreeID)
		if worktreeID == "" {
			return serverapi.WorktreeTopologyEntry{}, errors.New("scheduled worktree target identity is invalid")
		}
		entry, found := topologyEntryByWorktreeID(topology, worktreeID)
		if !found {
			return serverapi.WorktreeTopologyEntry{}, errors.Join(
				serverapi.ErrWorktreeNotFound,
				fmt.Errorf("scheduled worktree target %q is no longer present", worktreeID),
			)
		}
		if filepath.Clean(topologyRoot(entry)) != filepath.Clean(root) {
			return serverapi.WorktreeTopologyEntry{}, errors.Join(
				serverapi.ErrWorktreeNotFound,
				fmt.Errorf("scheduled worktree target %q changed root", worktreeID),
			)
		}
		return entry, nil
	}
	for _, entry := range topology {
		if filepath.Clean(topologyRoot(entry)) == filepath.Clean(root) {
			return entry, nil
		}
	}
	return serverapi.WorktreeTopologyEntry{}, errors.Join(
		serverapi.ErrWorktreeNotFound,
		fmt.Errorf("scheduled worktree target %q is no longer present", root),
	)
}

func resolveTopologySelector(entries []serverapi.WorktreeTopologyEntry, selector string) (topologySelectorMatch, error) {
	selector = strings.TrimSpace(selector)
	if selector == "" {
		return topologySelectorMatch{}, &serverapi.WorktreeSelectorError{Kind: serverapi.WorktreeSelectorErrorKindNotFound, Input: selector}
	}
	for _, matches := range [][]topologySelectorMatch{
		matchTopologyWorktreeID(entries, selector),
		matchTopologyBranch(entries, selector),
		matchTopologyDisplayName(entries, selector),
		matchTopologyPath(entries, selector),
	} {
		if len(matches) == 0 {
			continue
		}
		if len(matches) == 1 {
			return matches[0], nil
		}
		return topologySelectorMatch{}, topologyAmbiguity(selector, matches)
	}
	return topologySelectorMatch{}, &serverapi.WorktreeSelectorError{Kind: serverapi.WorktreeSelectorErrorKindNotFound, Input: selector}
}

func topologySelectorFor(entries []serverapi.WorktreeTopologyEntry, index int) (string, error) {
	if index < 0 || index >= len(entries) {
		return "", &serverapi.WorktreeSelectorError{Kind: serverapi.WorktreeSelectorErrorKindNotFound, Input: ""}
	}
	entry := entries[index]
	candidates := make([]string, 0, 5)
	if branch := topologyBranch(entry); branch != nil {
		candidates = append(candidates, *branch)
	}
	if displayName := topologyDisplayName(entry); displayName != nil {
		candidates = append(candidates, *displayName)
	}
	if worktreeID := topologyWorktreeID(entry); worktreeID != nil {
		candidates = append(candidates, *worktreeID)
	}
	root := topologyRoot(entry)
	candidates = append(candidates, shortestUniquePathSuffix(entries, index), root)
	for _, candidate := range candidates {
		if strings.TrimSpace(candidate) == "" {
			continue
		}
		match, err := resolveTopologySelector(entries, candidate)
		if err == nil && match.index == index {
			return candidate, nil
		}
	}
	return "", &serverapi.WorktreeSelectorError{Kind: serverapi.WorktreeSelectorErrorKindUnavailable, Input: root}
}

func matchTopologyWorktreeID(entries []serverapi.WorktreeTopologyEntry, selector string) []topologySelectorMatch {
	return filterTopology(entries, func(entry serverapi.WorktreeTopologyEntry) bool {
		id := topologyWorktreeID(entry)
		return id != nil && *id == selector
	})
}

func matchTopologyBranch(entries []serverapi.WorktreeTopologyEntry, selector string) []topologySelectorMatch {
	return filterTopology(entries, func(entry serverapi.WorktreeTopologyEntry) bool {
		branch := topologyBranch(entry)
		return branch != nil && *branch == selector
	})
}

func matchTopologyDisplayName(entries []serverapi.WorktreeTopologyEntry, selector string) []topologySelectorMatch {
	return filterTopology(entries, func(entry serverapi.WorktreeTopologyEntry) bool {
		name := topologyDisplayName(entry)
		return name != nil && *name == selector
	})
}

func matchTopologyPath(entries []serverapi.WorktreeTopologyEntry, selector string) []topologySelectorMatch {
	absolute := filepath.IsAbs(selector)
	selectorComponents := cleanPathComponents(selector)
	return filterTopology(entries, func(entry serverapi.WorktreeTopologyEntry) bool {
		root := topologyRoot(entry)
		if absolute {
			return filepath.Clean(root) == filepath.Clean(selector)
		}
		return pathHasComponentSuffix(cleanPathComponents(root), selectorComponents)
	})
}

func filterTopology(entries []serverapi.WorktreeTopologyEntry, matches func(serverapi.WorktreeTopologyEntry) bool) []topologySelectorMatch {
	out := make([]topologySelectorMatch, 0, 1)
	for index, entry := range entries {
		if matches(entry) {
			out = append(out, topologySelectorMatch{index: index, entry: entry})
		}
	}
	return out
}

func topologyAmbiguity(input string, matches []topologySelectorMatch) error {
	candidates := make([]serverapi.WorktreeSelectorCandidate, 0, len(matches))
	for _, match := range matches {
		candidates = append(candidates, serverapi.WorktreeSelectorCandidate{
			Variant:          match.entry.Variant,
			Selector:         topologyRoot(match.entry),
			BranchName:       topologyBranch(match.entry),
			DisplayName:      topologyDisplayName(match.entry),
			FallbackIdentity: topologyRoot(match.entry),
		})
	}
	return &serverapi.WorktreeSelectorError{Kind: serverapi.WorktreeSelectorErrorKindAmbiguous, Input: input, Candidates: candidates}
}

func topologyRoot(entry serverapi.WorktreeTopologyEntry) string {
	switch entry.Variant {
	case serverapi.WorktreeTopologyVariantRegistered:
		return entry.Registered.Git.CanonicalRoot
	case serverapi.WorktreeTopologyVariantExternal:
		return entry.External.Git.CanonicalRoot
	case serverapi.WorktreeTopologyVariantMissing:
		return entry.Missing.Kent.CanonicalRoot
	default:
		return ""
	}
}

func topologyWorktreeID(entry serverapi.WorktreeTopologyEntry) *string {
	switch entry.Variant {
	case serverapi.WorktreeTopologyVariantRegistered:
		value := entry.Registered.Kent.WorktreeID
		return &value
	case serverapi.WorktreeTopologyVariantMissing:
		value := entry.Missing.Kent.WorktreeID
		return &value
	default:
		return nil
	}
}

func topologyBranch(entry serverapi.WorktreeTopologyEntry) *string {
	switch entry.Variant {
	case serverapi.WorktreeTopologyVariantRegistered:
		return entry.Registered.Git.BranchName
	case serverapi.WorktreeTopologyVariantExternal:
		return entry.External.Git.BranchName
	default:
		return nil
	}
}

func topologyDisplayName(entry serverapi.WorktreeTopologyEntry) *string {
	switch entry.Variant {
	case serverapi.WorktreeTopologyVariantRegistered:
		value := entry.Registered.Kent.DisplayName
		return &value
	case serverapi.WorktreeTopologyVariantMissing:
		value := entry.Missing.Kent.DisplayName
		return &value
	case serverapi.WorktreeTopologyVariantExternal:
		value := filepath.Base(entry.External.Git.CanonicalRoot)
		return &value
	default:
		return nil
	}
}

func shortestUniquePathSuffix(entries []serverapi.WorktreeTopologyEntry, index int) string {
	components := cleanPathComponents(topologyRoot(entries[index]))
	for start := len(components) - 1; start >= 0; start-- {
		candidate := strings.Join(components[start:], string(filepath.Separator))
		matches := matchTopologyPath(entries, candidate)
		if len(matches) == 1 && matches[0].index == index {
			return candidate
		}
	}
	return topologyRoot(entries[index])
}

func cleanPathComponents(path string) []string {
	clean := filepath.Clean(path)
	volume := filepath.VolumeName(clean)
	clean = strings.TrimPrefix(clean, volume)
	clean = strings.TrimPrefix(clean, string(filepath.Separator))
	if clean == "." || clean == "" {
		return nil
	}
	return strings.Split(clean, string(filepath.Separator))
}

func pathHasComponentSuffix(path []string, suffix []string) bool {
	if len(suffix) == 0 || len(suffix) > len(path) {
		return false
	}
	start := len(path) - len(suffix)
	for index := range suffix {
		if path[start+index] != suffix[index] {
			return false
		}
	}
	return true
}
