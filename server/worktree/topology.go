package worktree

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"core/server/metadata"
	"core/shared/clientui"
	"core/shared/serverapi"
)

func (s *Service) projectTopology(ctx context.Context, workspaceID string, workspaceRoot string) ([]serverapi.WorktreeTopologyEntry, error) {
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

func projectTopologyEntries(gitEntries []GitWorktree, records []metadata.WorktreeRecord) ([]serverapi.WorktreeTopologyEntry, error) {
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
	out := make([]serverapi.WorktreeTopologyEntry, 0, len(gitEntries)+len(records))
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
			out = append(out, serverapi.WorktreeTopologyEntry{Variant: serverapi.WorktreeTopologyVariantRegistered, Registered: &serverapi.WorktreeRegisteredFacts{Git: gitFacts, Kent: kentFactsFromRecord(record)}})
			continue
		}
		out = append(out, serverapi.WorktreeTopologyEntry{Variant: serverapi.WorktreeTopologyVariantExternal, External: &serverapi.WorktreeExternalFacts{Git: gitFacts}})
	}
	for _, record := range records {
		if _, missing := byRoot[strings.TrimSpace(record.CanonicalRoot)]; !missing {
			continue
		}
		out = append(out, serverapi.WorktreeTopologyEntry{Variant: serverapi.WorktreeTopologyVariantMissing, Missing: &serverapi.WorktreeMissingFacts{Kent: kentFactsFromRecord(record)}})
	}
	return out, nil
}

func projectWorktreeList(entries []serverapi.WorktreeTopologyEntry, target clientui.SessionExecutionTarget) ([]serverapi.WorktreeListEntry, error) {
	out := make([]serverapi.WorktreeListEntry, 0, len(entries))
	for index, topology := range entries {
		selector, err := topologySelectorFor(entries, index)
		if err != nil {
			return nil, err
		}
		entry := serverapi.WorktreeListEntry{
			Topology: topology,
			Projection: serverapi.WorktreeListProjection{
				Selector:  selector,
				IsCurrent: topologyIsCurrent(topology, target),
			},
		}
		if err := entry.Validate(); err != nil {
			return nil, fmt.Errorf("project worktree list entry %d: %w", index, err)
		}
		out = append(out, entry)
	}
	return out, nil
}

func topologyIsCurrent(entry serverapi.WorktreeTopologyEntry, target clientui.SessionExecutionTarget) bool {
	if target.Worktree == nil {
		switch entry.Variant {
		case serverapi.WorktreeTopologyVariantRegistered:
			return entry.Registered.Git.IsMain
		case serverapi.WorktreeTopologyVariantExternal:
			return entry.External.Git.IsMain
		default:
			return false
		}
	}
	worktreeID := topologyWorktreeID(entry)
	return worktreeID != nil && strings.TrimSpace(*worktreeID) == strings.TrimSpace(target.Worktree.ID)
}

func (s *Service) ResolveWorktreeSelector(ctx context.Context, req serverapi.WorktreeSelectorPreviewRequest) (serverapi.WorktreeSelectorPreviewResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.WorktreeSelectorPreviewResponse{}, err
	}
	workspaceCtx, err := s.resolveSessionWorkspaceContext(ctx, req.SessionID)
	if err != nil {
		return serverapi.WorktreeSelectorPreviewResponse{}, err
	}
	entries, err := s.projectTopology(ctx, workspaceCtx.workspaceID, workspaceCtx.workspaceRoot)
	if err != nil {
		return serverapi.WorktreeSelectorPreviewResponse{}, err
	}
	match, err := resolveTopologySelector(entries, req.Selector)
	if err != nil {
		return serverapi.WorktreeSelectorPreviewResponse{}, err
	}
	selector, err := topologySelectorFor(entries, match.index)
	if err != nil {
		return serverapi.WorktreeSelectorPreviewResponse{}, err
	}
	return serverapi.WorktreeSelectorPreviewResponse{Worktree: match.entry, Selector: selector}, nil
}

func gitFactsFromEntry(entry GitWorktree) serverapi.WorktreeGitFacts {
	facts := serverapi.WorktreeGitFacts{
		CanonicalRoot: strings.TrimSpace(entry.Root),
		HeadObject:    strings.TrimSpace(entry.HeadOID),
		Detached:      entry.Detached,
		Bare:          entry.Bare,
		IsMain:        entry.IsMain,
		PathAvailable: PathAvailability(entry.Root) == serverapi.WorktreePathAvailabilityAvailable,
	}
	if value := strings.TrimSpace(entry.BranchRef); value != "" {
		facts.BranchRef = &value
	}
	if value := strings.TrimSpace(entry.BranchName); value != "" {
		facts.BranchName = &value
	}
	if value := strings.TrimSpace(entry.LockedReason); value != "" {
		facts.LockedReason = &value
	}
	if value := strings.TrimSpace(entry.PrunableReason); value != "" {
		facts.PrunableReason = &value
	}
	return facts
}

func kentFactsFromRecord(record metadata.WorktreeRecord) serverapi.WorktreeKentFacts {
	facts := serverapi.WorktreeKentFacts{
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

func registeredTopologyEntry(item syncedWorktree) serverapi.WorktreeTopologyEntry {
	return serverapi.WorktreeTopologyEntry{
		Variant: serverapi.WorktreeTopologyVariantRegistered,
		Registered: &serverapi.WorktreeRegisteredFacts{
			Git:  gitFactsFromEntry(item.git),
			Kent: kentFactsFromRecord(item.record),
		},
	}
}

func topologyEntryByWorktreeID(entries []serverapi.WorktreeTopologyEntry, worktreeID string) (serverapi.WorktreeTopologyEntry, bool) {
	for _, entry := range entries {
		id := topologyWorktreeID(entry)
		if id != nil && strings.TrimSpace(*id) == strings.TrimSpace(worktreeID) {
			return entry, true
		}
	}
	return serverapi.WorktreeTopologyEntry{}, false
}
