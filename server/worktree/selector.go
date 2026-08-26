package worktree

import (
	"core/shared/worktreecontract"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

type topologySelectorMatch struct {
	index int
	entry worktreecontract.TopologyEntry
}

type scheduledWorktreeTarget struct {
	worktreeID    *string
	canonicalRoot string
}

func scheduledWorktreeTargetFromEntry(entry worktreecontract.TopologyEntry) (scheduledWorktreeTarget, error) {
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

func scheduledKentWorktreeTargetFromEntry(entry worktreecontract.TopologyEntry) (scheduledWorktreeTarget, error) {
	target, err := scheduledWorktreeTargetFromEntry(entry)
	if err != nil {
		return scheduledWorktreeTarget{}, err
	}
	if target.worktreeID == nil {
		return scheduledWorktreeTarget{}, errors.New("scheduled worktree target requires a Kent worktree id")
	}
	return target, nil
}

func (target scheduledWorktreeTarget) resolve(topology []worktreecontract.TopologyEntry) (worktreecontract.TopologyEntry, error) {
	root := strings.TrimSpace(target.canonicalRoot)
	if root == "" {
		return worktreecontract.TopologyEntry{}, errors.New("scheduled worktree target identity is invalid")
	}
	if target.worktreeID != nil {
		worktreeID := strings.TrimSpace(*target.worktreeID)
		if worktreeID == "" {
			return worktreecontract.TopologyEntry{}, errors.New("scheduled worktree target identity is invalid")
		}
		entry, found := topologyEntryByWorktreeID(topology, worktreeID)
		if !found {
			return worktreecontract.TopologyEntry{}, errors.Join(
				worktreecontract.ErrWorktreeNotFound,
				fmt.Errorf("scheduled worktree target %q is no longer present", worktreeID),
			)
		}
		if filepath.Clean(topologyRoot(entry)) != filepath.Clean(root) {
			return worktreecontract.TopologyEntry{}, errors.Join(
				worktreecontract.ErrWorktreeNotFound,
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
	return worktreecontract.TopologyEntry{}, errors.Join(
		worktreecontract.ErrWorktreeNotFound,
		fmt.Errorf("scheduled worktree target %q is no longer present", root),
	)
}

func resolveTopologySelector(entries []worktreecontract.TopologyEntry, selector string) (topologySelectorMatch, error) {
	selector = strings.TrimSpace(selector)
	if selector == "" {
		return topologySelectorMatch{}, &worktreecontract.SelectorError{Kind: worktreecontract.SelectorErrorKindNotFound, Input: selector}
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
	return topologySelectorMatch{}, &worktreecontract.SelectorError{Kind: worktreecontract.SelectorErrorKindNotFound, Input: selector}
}

func topologySelectorFor(entries []worktreecontract.TopologyEntry, index int) (string, error) {
	if index < 0 || index >= len(entries) {
		return "", &worktreecontract.SelectorError{Kind: worktreecontract.SelectorErrorKindNotFound, Input: ""}
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
	return "", &worktreecontract.SelectorError{Kind: worktreecontract.SelectorErrorKindUnavailable, Input: root}
}

func matchTopologyWorktreeID(entries []worktreecontract.TopologyEntry, selector string) []topologySelectorMatch {
	return filterTopology(entries, func(entry worktreecontract.TopologyEntry) bool {
		id := topologyWorktreeID(entry)
		return id != nil && *id == selector
	})
}

func matchTopologyBranch(entries []worktreecontract.TopologyEntry, selector string) []topologySelectorMatch {
	return filterTopology(entries, func(entry worktreecontract.TopologyEntry) bool {
		branch := topologyBranch(entry)
		return branch != nil && *branch == selector
	})
}

func matchTopologyDisplayName(entries []worktreecontract.TopologyEntry, selector string) []topologySelectorMatch {
	return filterTopology(entries, func(entry worktreecontract.TopologyEntry) bool {
		name := topologyDisplayName(entry)
		return name != nil && *name == selector
	})
}

func matchTopologyPath(entries []worktreecontract.TopologyEntry, selector string) []topologySelectorMatch {
	absolute := filepath.IsAbs(selector)
	selectorComponents := cleanPathComponents(selector)
	return filterTopology(entries, func(entry worktreecontract.TopologyEntry) bool {
		root := topologyRoot(entry)
		if absolute {
			return filepath.Clean(root) == filepath.Clean(selector)
		}
		return pathHasComponentSuffix(cleanPathComponents(root), selectorComponents)
	})
}

func filterTopology(entries []worktreecontract.TopologyEntry, matches func(worktreecontract.TopologyEntry) bool) []topologySelectorMatch {
	out := make([]topologySelectorMatch, 0, 1)
	for index, entry := range entries {
		if matches(entry) {
			out = append(out, topologySelectorMatch{index: index, entry: entry})
		}
	}
	return out
}

func topologyAmbiguity(input string, matches []topologySelectorMatch) error {
	candidates := make([]worktreecontract.SelectorCandidate, 0, len(matches))
	for _, match := range matches {
		candidates = append(candidates, worktreecontract.SelectorCandidate{
			Variant:          match.entry.Variant,
			Selector:         topologyRoot(match.entry),
			BranchName:       topologyBranch(match.entry),
			DisplayName:      topologyDisplayName(match.entry),
			FallbackIdentity: topologyRoot(match.entry),
		})
	}
	return &worktreecontract.SelectorError{Kind: worktreecontract.SelectorErrorKindAmbiguous, Input: input, Candidates: candidates}
}

func topologyRoot(entry worktreecontract.TopologyEntry) string {
	switch entry.Variant {
	case worktreecontract.TopologyVariantRegistered:
		return entry.Registered.Git.CanonicalRoot
	case worktreecontract.TopologyVariantExternal:
		return entry.External.Git.CanonicalRoot
	case worktreecontract.TopologyVariantMissing:
		return entry.Missing.Kent.CanonicalRoot
	default:
		return ""
	}
}

func topologyWorktreeID(entry worktreecontract.TopologyEntry) *string {
	switch entry.Variant {
	case worktreecontract.TopologyVariantRegistered:
		value := entry.Registered.Kent.WorktreeID
		return &value
	case worktreecontract.TopologyVariantMissing:
		value := entry.Missing.Kent.WorktreeID
		return &value
	default:
		return nil
	}
}

func topologyBranch(entry worktreecontract.TopologyEntry) *string {
	switch entry.Variant {
	case worktreecontract.TopologyVariantRegistered:
		return entry.Registered.Git.BranchName
	case worktreecontract.TopologyVariantExternal:
		return entry.External.Git.BranchName
	default:
		return nil
	}
}

func topologyDisplayName(entry worktreecontract.TopologyEntry) *string {
	switch entry.Variant {
	case worktreecontract.TopologyVariantRegistered:
		value := entry.Registered.Kent.DisplayName
		return &value
	case worktreecontract.TopologyVariantMissing:
		value := entry.Missing.Kent.DisplayName
		return &value
	case worktreecontract.TopologyVariantExternal:
		value := filepath.Base(entry.External.Git.CanonicalRoot)
		return &value
	default:
		return nil
	}
}

func shortestUniquePathSuffix(entries []worktreecontract.TopologyEntry, index int) string {
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
