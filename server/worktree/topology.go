package worktree

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"core/server/metadata"
	"core/shared/clientui"
	"core/shared/worktreecontract"
)

func (s *Service) projectTopology(ctx context.Context, workspaceID string, workspaceRoot string) ([]worktreecontract.TopologyEntry, error) {
	if s == nil || s.metadata == nil || s.git == nil {
		return nil, errors.New("worktree service dependencies are required")
	}
	gitEntries, err := s.git.List(ctx, workspaceRoot)
	if err != nil {
		return nil, err
	}
	records, err := s.metadata.ListWorktreeRecordsByWorkspaceID(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	return projectTopologyEntries(gitEntries, records)
}

func projectTopologyEntries(gitEntries []GitWorktree, records []metadata.WorktreeRecord) ([]worktreecontract.TopologyEntry, error) {
	byRoot := make(map[string]metadata.WorktreeRecord, len(records))
	for _, record := range records {
		root := strings.TrimSpace(record.CanonicalRoot)
		if root == "" {
			return nil, fmt.Errorf("Kent worktree %q has no canonical root", strings.TrimSpace(record.ID))
		}
		if _, exists := byRoot[root]; exists {
			return nil, fmt.Errorf("duplicate Kent worktree root %q", root)
		}
		byRoot[root] = record
	}
	out := make([]worktreecontract.TopologyEntry, 0, len(gitEntries)+len(records))
	gitRoots := make(map[string]struct{}, len(gitEntries))
	for _, gitEntry := range gitEntries {
		root := strings.TrimSpace(gitEntry.Root)
		if root == "" {
			return nil, errors.New("Git worktree has no canonical root")
		}
		if _, exists := gitRoots[root]; exists {
			return nil, fmt.Errorf("duplicate Git worktree root %q", root)
		}
		gitRoots[root] = struct{}{}
		record, registered := byRoot[root]
		delete(byRoot, root)
		gitFacts := gitFactsFromEntry(gitEntry)
		if registered {
			out = append(out, worktreecontract.TopologyEntry{Variant: worktreecontract.TopologyVariantRegistered, Registered: &worktreecontract.RegisteredFacts{Git: gitFacts, Kent: kentFactsFromRecord(record)}})
			continue
		}
		out = append(out, worktreecontract.TopologyEntry{Variant: worktreecontract.TopologyVariantExternal, External: &worktreecontract.ExternalFacts{Git: gitFacts}})
	}
	for _, record := range records {
		if _, missing := byRoot[strings.TrimSpace(record.CanonicalRoot)]; !missing {
			continue
		}
		out = append(out, worktreecontract.TopologyEntry{Variant: worktreecontract.TopologyVariantMissing, Missing: &worktreecontract.MissingFacts{Kent: kentFactsFromRecord(record)}})
	}
	return out, nil
}

func projectWorktreeList(entries []worktreecontract.TopologyEntry, target *clientui.SessionExecutionTarget) ([]worktreecontract.ListEntry, error) {
	out := make([]worktreecontract.ListEntry, 0, len(entries))
	for index, topology := range entries {
		selector, err := topologySelectorFor(entries, index)
		if err != nil {
			return nil, err
		}
		entry, err := worktreecontract.ProjectListEntry(
			topology,
			selector,
			target != nil && topologyIsCurrent(topology, *target),
			target != nil,
		)
		if err != nil {
			return nil, fmt.Errorf("project worktree list entry %d: %w", index, err)
		}
		out = append(out, entry)
	}
	return out, nil
}

func topologyIsCurrent(entry worktreecontract.TopologyEntry, target clientui.SessionExecutionTarget) bool {
	if target.Worktree == nil {
		switch entry.Variant {
		case worktreecontract.TopologyVariantRegistered:
			return entry.Registered.Git.IsMain
		case worktreecontract.TopologyVariantExternal:
			return entry.External.Git.IsMain
		default:
			return false
		}
	}
	worktreeID := topologyWorktreeID(entry)
	return worktreeID != nil && strings.TrimSpace(*worktreeID) == strings.TrimSpace(target.Worktree.ID)
}

func (s *Service) ResolveWorktreeSelector(ctx context.Context, req worktreecontract.SelectorResolveRequest) (worktreecontract.SelectorResolveResponse, error) {
	resolution, err := s.resolveWorktreeSelector(ctx, req.SessionID, req.Selector)
	if err != nil {
		return worktreecontract.SelectorResolveResponse{}, err
	}
	projected, err := projectWorktreeList(resolution.entries, &resolution.target)
	if err != nil {
		return worktreecontract.SelectorResolveResponse{}, err
	}
	return worktreecontract.SelectorResolveResponse{Worktree: projected[resolution.match.index]}, nil
}

type worktreeSelectorResolution struct {
	entries []worktreecontract.TopologyEntry
	match   topologySelectorMatch
	target  clientui.SessionExecutionTarget
}

func (s *Service) resolveWorktreeSelector(ctx context.Context, sessionID string, selector string) (worktreeSelectorResolution, error) {
	workspaceCtx, err := s.resolveSessionWorkspaceContext(ctx, sessionID)
	if err != nil {
		return worktreeSelectorResolution{}, err
	}
	entries, err := s.projectTopology(ctx, workspaceCtx.workspaceID, workspaceCtx.workspaceRoot)
	if err != nil {
		return worktreeSelectorResolution{}, err
	}
	match, err := resolveTopologySelector(entries, selector)
	if err != nil {
		return worktreeSelectorResolution{}, err
	}
	return worktreeSelectorResolution{entries: entries, match: match, target: workspaceCtx.target}, nil
}

func (s *Service) PreviewWorktreeDelete(ctx context.Context, req worktreecontract.DeletePreviewRequest) (worktreecontract.DeletePreviewResponse, error) {
	resolution, err := s.resolveWorktreeSelector(ctx, req.SessionID, req.Selector)
	if err != nil {
		return worktreecontract.DeletePreviewResponse{}, err
	}
	deletionSelector, err := resolution.match.entry.DeletionSelector()
	if err != nil {
		return worktreecontract.DeletePreviewResponse{}, err
	}
	cleanliness, err := s.evaluateDeleteCleanliness(ctx, resolution.match.entry)
	if err != nil {
		return worktreecontract.DeletePreviewResponse{}, err
	}
	response := worktreecontract.DeletePreviewResponse{
		Worktree:         resolution.match.entry,
		DeletionSelector: deletionSelector,
		Cleanliness:      cleanliness,
	}
	if err := response.Validate(); err != nil {
		return worktreecontract.DeletePreviewResponse{}, err
	}
	return response, nil
}

func gitFactsFromEntry(entry GitWorktree) worktreecontract.GitFacts {
	facts := worktreecontract.GitFacts{
		CanonicalRoot: strings.TrimSpace(entry.Root),
		HeadObject:    strings.TrimSpace(entry.HeadOID),
		Detached:      entry.Detached,
		Bare:          entry.Bare,
		IsMain:        entry.IsMain,
		PathAvailable: PathAvailability(entry.Root) == worktreecontract.PathAvailabilityAvailable,
	}
	if entry.Branch != nil {
		branchRef := entry.Branch.Ref()
		branchName := entry.Branch.Name()
		facts.BranchRef = &branchRef
		facts.BranchName = &branchName
	}
	if value := strings.TrimSpace(entry.LockedReason); value != "" {
		facts.LockedReason = &value
	}
	if value := strings.TrimSpace(entry.PrunableReason); value != "" {
		facts.PrunableReason = &value
	}
	return facts
}

func kentFactsFromRecord(record metadata.WorktreeRecord) worktreecontract.KentFacts {
	facts := worktreecontract.KentFacts{
		WorktreeID:    strings.TrimSpace(record.ID),
		CanonicalRoot: strings.TrimSpace(record.CanonicalRoot),
		DisplayName:   strings.TrimSpace(record.DisplayName),
		Managed:       record.Managed,
		CreatedBranch: record.CreatedBranch,
	}
	if value := strings.TrimSpace(record.OriginSessionID); value != "" {
		facts.OriginSessionID = &value
	}
	return facts
}

func registeredTopologyEntry(item syncedWorktree) worktreecontract.TopologyEntry {
	return worktreecontract.TopologyEntry{
		Variant: worktreecontract.TopologyVariantRegistered,
		Registered: &worktreecontract.RegisteredFacts{
			Git:  gitFactsFromEntry(item.git),
			Kent: kentFactsFromRecord(item.record),
		},
	}
}

func topologyEntryByWorktreeID(entries []worktreecontract.TopologyEntry, worktreeID string) (worktreecontract.TopologyEntry, bool) {
	for _, entry := range entries {
		id := topologyWorktreeID(entry)
		if id != nil && strings.TrimSpace(*id) == strings.TrimSpace(worktreeID) {
			return entry, true
		}
	}
	return worktreecontract.TopologyEntry{}, false
}
