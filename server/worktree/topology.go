package worktree

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"core/server/metadata"
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
	byRoot := make(map[string]metadata.WorktreeRecord, len(records))
	for _, record := range records {
		root := strings.TrimSpace(record.CanonicalRoot)
		if _, exists := byRoot[root]; exists {
			return nil, fmt.Errorf("duplicate Kent worktree root %q", root)
		}
		byRoot[root] = record
	}
	out := make([]serverapi.WorktreeTopologyEntry, 0, len(gitEntries)+len(records))
	for _, gitEntry := range gitEntries {
		root := strings.TrimSpace(gitEntry.Root)
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
	return serverapi.WorktreeGitFacts{CanonicalRoot: entry.Root, HeadObject: entry.HeadOID, Detached: entry.Detached, Bare: entry.Bare, IsMain: entry.IsMain, PathAvailable: pathAvailability(entry.Root) == "available"}
}

func kentFactsFromRecord(record metadata.WorktreeRecord) serverapi.WorktreeKentFacts {
	return serverapi.WorktreeKentFacts{WorktreeID: record.ID, CanonicalRoot: record.CanonicalRoot, DisplayName: record.DisplayName, Managed: record.Managed, CreatedBranch: record.CreatedBranch}
}
