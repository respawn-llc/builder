package worktree

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	worktreepb "core/shared/protoapi/gen/kent/api/worktree"
	"core/shared/worktreecontract"
)

type topologySelectorMatch struct {
	index int
	entry *worktreepb.TopologyEntry
}

type scheduledWorktreeTarget struct {
	worktreeID    *string
	canonicalRoot string
}

func scheduledWorktreeTargetFromEntry(entry *worktreepb.TopologyEntry) (scheduledWorktreeTarget, error) {
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

func scheduledKentWorktreeTargetFromEntry(entry *worktreepb.TopologyEntry) (scheduledWorktreeTarget, error) {
	target, err := scheduledWorktreeTargetFromEntry(entry)
	if err != nil {
		return scheduledWorktreeTarget{}, err
	}
	if target.worktreeID == nil {
		return scheduledWorktreeTarget{}, errors.New("scheduled worktree target requires a Kent worktree id")
	}
	return target, nil
}

func (target scheduledWorktreeTarget) resolve(topology []*worktreepb.TopologyEntry) (*worktreepb.TopologyEntry, error) {
	root := strings.TrimSpace(target.canonicalRoot)
	if root == "" {
		return nil, errors.New("scheduled worktree target identity is invalid")
	}
	if target.worktreeID != nil {
		worktreeID := strings.TrimSpace(*target.worktreeID)
		if worktreeID == "" {
			return nil, errors.New("scheduled worktree target identity is invalid")
		}
		entry, found := topologyEntryByWorktreeID(topology, worktreeID)
		if !found {
			return nil, errors.Join(
				worktreecontract.ErrWorktreeNotFound,
				fmt.Errorf("scheduled worktree target %q is no longer present", worktreeID),
			)
		}
		if filepath.Clean(topologyRoot(entry)) != filepath.Clean(root) {
			return nil, errors.Join(
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
	return nil, errors.Join(
		worktreecontract.ErrWorktreeNotFound,
		fmt.Errorf("scheduled worktree target %q is no longer present", root),
	)
}

func resolveTopologySelector(entries []*worktreepb.TopologyEntry, selector string) (topologySelectorMatch, error) {
	selector = strings.TrimSpace(selector)
	if selector == "" {
		return topologySelectorMatch{}, worktreecontract.NewSelectorError(
			worktreepb.SelectorErrorKind_WORKTREE_SELECTOR_ERROR_KIND_NOT_FOUND,
			selector,
			nil,
		)
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
	return topologySelectorMatch{}, worktreecontract.NewSelectorError(
		worktreepb.SelectorErrorKind_WORKTREE_SELECTOR_ERROR_KIND_NOT_FOUND,
		selector,
		nil,
	)
}

func topologySelectorFor(entries []*worktreepb.TopologyEntry, index int) (string, error) {
	if index < 0 || index >= len(entries) {
		return "", worktreecontract.NewSelectorError(
			worktreepb.SelectorErrorKind_WORKTREE_SELECTOR_ERROR_KIND_NOT_FOUND,
			"",
			nil,
		)
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
	return "", worktreecontract.NewSelectorError(
		worktreepb.SelectorErrorKind_WORKTREE_SELECTOR_ERROR_KIND_UNAVAILABLE,
		root,
		nil,
	)
}

func matchTopologyWorktreeID(entries []*worktreepb.TopologyEntry, selector string) []topologySelectorMatch {
	return filterTopology(entries, func(entry *worktreepb.TopologyEntry) bool {
		id := topologyWorktreeID(entry)
		return id != nil && *id == selector
	})
}

func matchTopologyBranch(entries []*worktreepb.TopologyEntry, selector string) []topologySelectorMatch {
	return filterTopology(entries, func(entry *worktreepb.TopologyEntry) bool {
		branch := topologyBranch(entry)
		return branch != nil && *branch == selector
	})
}

func matchTopologyDisplayName(entries []*worktreepb.TopologyEntry, selector string) []topologySelectorMatch {
	return filterTopology(entries, func(entry *worktreepb.TopologyEntry) bool {
		name := topologyDisplayName(entry)
		return name != nil && *name == selector
	})
}

func matchTopologyPath(entries []*worktreepb.TopologyEntry, selector string) []topologySelectorMatch {
	absolute := filepath.IsAbs(selector)
	selectorComponents := cleanPathComponents(selector)
	return filterTopology(entries, func(entry *worktreepb.TopologyEntry) bool {
		root := topologyRoot(entry)
		if absolute {
			return filepath.Clean(root) == filepath.Clean(selector)
		}
		return pathHasComponentSuffix(cleanPathComponents(root), selectorComponents)
	})
}

func filterTopology(entries []*worktreepb.TopologyEntry, matches func(*worktreepb.TopologyEntry) bool) []topologySelectorMatch {
	out := make([]topologySelectorMatch, 0, 1)
	for index, entry := range entries {
		if matches(entry) {
			out = append(out, topologySelectorMatch{index: index, entry: entry})
		}
	}
	return out
}

func topologyAmbiguity(input string, matches []topologySelectorMatch) error {
	candidates := make([]*worktreepb.SelectorCandidate, 0, len(matches))
	for _, match := range matches {
		candidates = append(candidates, &worktreepb.SelectorCandidate{
			Variant:          topologyVariant(match.entry),
			Selector:         topologyRoot(match.entry),
			BranchName:       topologyBranch(match.entry),
			DisplayName:      topologyDisplayName(match.entry),
			FallbackIdentity: topologyRoot(match.entry),
		})
	}
	return worktreecontract.NewSelectorError(
		worktreepb.SelectorErrorKind_WORKTREE_SELECTOR_ERROR_KIND_AMBIGUOUS,
		input,
		candidates,
	)
}

func topologyVariant(entry *worktreepb.TopologyEntry) worktreepb.TopologyVariant {
	switch {
	case entry == nil:
		return worktreepb.TopologyVariant_WORKTREE_TOPOLOGY_VARIANT_UNSPECIFIED
	case entry.GetRegistered() != nil:
		return worktreepb.TopologyVariant_WORKTREE_TOPOLOGY_VARIANT_REGISTERED
	case entry.GetExternal() != nil:
		return worktreepb.TopologyVariant_WORKTREE_TOPOLOGY_VARIANT_EXTERNAL
	case entry.GetMissing() != nil:
		return worktreepb.TopologyVariant_WORKTREE_TOPOLOGY_VARIANT_MISSING
	default:
		return worktreepb.TopologyVariant_WORKTREE_TOPOLOGY_VARIANT_UNSPECIFIED
	}
}

func topologyRoot(entry *worktreepb.TopologyEntry) string {
	switch {
	case entry == nil:
		return ""
	case entry.GetRegistered() != nil:
		return entry.GetRegistered().GetGit().GetCanonicalRoot()
	case entry.GetExternal() != nil:
		return entry.GetExternal().GetGit().GetCanonicalRoot()
	case entry.GetMissing() != nil:
		return entry.GetMissing().GetKent().GetCanonicalRoot()
	default:
		return ""
	}
}

func topologyWorktreeID(entry *worktreepb.TopologyEntry) *string {
	switch {
	case entry == nil:
		return nil
	case entry.GetRegistered() != nil:
		value := entry.GetRegistered().GetKent().GetWorktreeId()
		return &value
	case entry.GetMissing() != nil:
		value := entry.GetMissing().GetKent().GetWorktreeId()
		return &value
	default:
		return nil
	}
}

func topologyBranch(entry *worktreepb.TopologyEntry) *string {
	switch {
	case entry == nil:
		return nil
	case entry.GetRegistered() != nil:
		return entry.GetRegistered().GetGit().BranchName
	case entry.GetExternal() != nil:
		return entry.GetExternal().GetGit().BranchName
	default:
		return nil
	}
}

func topologyDisplayName(entry *worktreepb.TopologyEntry) *string {
	switch {
	case entry == nil:
		return nil
	case entry.GetRegistered() != nil:
		value := entry.GetRegistered().GetKent().GetDisplayName()
		return &value
	case entry.GetMissing() != nil:
		value := entry.GetMissing().GetKent().GetDisplayName()
		return &value
	case entry.GetExternal() != nil:
		value := filepath.Base(entry.GetExternal().GetGit().GetCanonicalRoot())
		return &value
	default:
		return nil
	}
}

func shortestUniquePathSuffix(entries []*worktreepb.TopologyEntry, index int) string {
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
