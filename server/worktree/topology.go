package worktree

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"core/server/metadata"
	"core/shared/clientui"
	"core/shared/config"
	worktreepb "core/shared/protoapi/gen/kent/api/worktree"
	"core/shared/worktreecontract"
)

func (s *Service) projectTopology(ctx context.Context, workspaceID string, workspaceRoot string) ([]*worktreepb.TopologyEntry, error) {
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
	return projectTopologyEntries(workspaceRoot, gitEntries, records)
}

func projectTopologyEntries(workspaceRoot string, gitEntries []GitWorktree, records []metadata.WorktreeRecord) ([]*worktreepb.TopologyEntry, error) {
	workspaceRoot = strings.TrimSpace(workspaceRoot)
	if workspaceRoot == "" {
		return nil, errors.New("workspace root must not be blank")
	}
	canonicalWorkspaceRoot, err := config.CanonicalWorkspaceRoot(workspaceRoot)
	if err != nil {
		return nil, err
	}
	byRoot := make(map[string]metadata.WorktreeRecord, len(records))
	for _, record := range records {
		rawRoot := strings.TrimSpace(record.CanonicalRoot)
		if rawRoot == "" {
			return nil, fmt.Errorf("Kent worktree %q has no canonical root", strings.TrimSpace(record.ID))
		}
		root, err := config.CanonicalWorkspaceRoot(rawRoot)
		if err != nil {
			return nil, err
		}
		record.CanonicalRoot = root
		if _, exists := byRoot[root]; exists {
			return nil, fmt.Errorf("duplicate Kent worktree root %q", root)
		}
		byRoot[root] = record
	}
	out := make([]*worktreepb.TopologyEntry, 0, len(gitEntries)+len(records))
	gitRoots := make(map[string]struct{}, len(gitEntries))
	for _, gitEntry := range gitEntries {
		rawRoot := strings.TrimSpace(gitEntry.Root)
		if rawRoot == "" {
			return nil, errors.New("Git worktree has no canonical root")
		}
		root, err := config.CanonicalWorkspaceRoot(rawRoot)
		if err != nil {
			return nil, err
		}
		if _, exists := gitRoots[root]; exists {
			return nil, fmt.Errorf("duplicate Git worktree root %q", root)
		}
		gitEntry.Root = root
		gitRoots[root] = struct{}{}
		record, registered := byRoot[root]
		delete(byRoot, root)
		gitFacts := gitFactsFromEntry(gitEntry)
		if root == canonicalWorkspaceRoot {
			out = append(out, &worktreepb.TopologyEntry{Topology: &worktreepb.TopologyEntry_MainWorkspace{
				MainWorkspace: &worktreepb.MainWorkspaceFacts{Git: gitFacts},
			}})
			continue
		}
		if registered {
			out = append(out, registeredTopologyEntry(syncedWorktree{record: record, git: gitEntry}))
			continue
		}
		out = append(out, &worktreepb.TopologyEntry{Topology: &worktreepb.TopologyEntry_External{
			External: &worktreepb.ExternalFacts{Git: gitFacts},
		}})
	}
	for _, record := range records {
		if _, missing := byRoot[strings.TrimSpace(record.CanonicalRoot)]; !missing {
			continue
		}
		out = append(out, &worktreepb.TopologyEntry{Topology: &worktreepb.TopologyEntry_Missing{
			Missing: &worktreepb.MissingFacts{Kent: kentFactsFromRecord(record)},
		}})
	}
	return out, nil
}

func projectWorktreeList(entries []*worktreepb.TopologyEntry, target *clientui.SessionExecutionTarget) ([]*worktreepb.ListEntry, error) {
	out := make([]*worktreepb.ListEntry, 0, len(entries))
	for index, topology := range entries {
		selector, err := topologySelectorFor(entries, index)
		if err != nil {
			return nil, err
		}
		entry, err := projectListEntry(
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

func projectListEntry(topology *worktreepb.TopologyEntry, selector string, isCurrent bool, sessionScoped bool) (*worktreepb.ListEntry, error) {
	projection := &worktreepb.ListProjection{Selector: selector, IsCurrent: isCurrent}
	var git *worktreepb.GitFacts
	switch {
	case topology.GetMainWorkspace() != nil:
		git = topology.GetMainWorkspace().GetGit()
	case topology.GetRegistered() != nil:
		git = topology.GetRegistered().GetGit()
	case topology.GetExternal() != nil:
		git = topology.GetExternal().GetGit()
		if git != nil && git.BranchName == nil {
			fallback := filepath.Base(git.CanonicalRoot)
			projection.FallbackIdentity = &fallback
		}
	case topology.GetMissing() == nil:
		return nil, errors.New("worktree topology variant is invalid")
	}
	if !sessionScoped {
		projection.IsCurrent = false
		return &worktreepb.ListEntry{Topology: topology, Projection: projection}, nil
	}
	if git != nil && !isCurrent && git.PathAvailable {
		projection.Switch = &worktreepb.SwitchOperation{
			Kind: worktreepb.SwitchOperationKind_WORKTREE_SWITCH_OPERATION_LEAVE_MAIN,
		}
		if topology.GetMainWorkspace() == nil {
			projection.Switch.Kind = worktreepb.SwitchOperationKind_WORKTREE_SWITCH_OPERATION_ENTER
			projection.Switch.Selector = &projection.Selector
		}
	}
	deletionSelector, err := deletionSelector(topology)
	switch {
	case err == nil:
		projection.DeletePreview = &worktreepb.DeletePreviewOperation{Selector: deletionSelector}
	case !errors.Is(err, worktreecontract.ErrWorktreeBlocked):
		return nil, err
	}
	return &worktreepb.ListEntry{Topology: topology, Projection: projection}, nil
}

func topologyIsCurrent(entry *worktreepb.TopologyEntry, target clientui.SessionExecutionTarget) bool {
	if target.Worktree == nil {
		return entry.GetMainWorkspace() != nil
	}
	worktreeID := topologyWorktreeID(entry)
	return worktreeID != nil && strings.TrimSpace(*worktreeID) == strings.TrimSpace(target.Worktree.ID)
}

func (s *Service) ResolveWorktreeSelector(ctx context.Context, req *worktreepb.SelectorResolveRequest) (*worktreepb.SelectorResolveSuccess, error) {
	resolution, err := s.resolveWorktreeSelector(ctx, req.SessionId, req.Selector)
	if err != nil {
		return nil, err
	}
	projected, err := projectWorktreeList(resolution.entries, &resolution.target)
	if err != nil {
		return nil, err
	}
	return &worktreepb.SelectorResolveSuccess{Worktree: projected[resolution.match.index]}, nil
}

type worktreeSelectorResolution struct {
	entries []*worktreepb.TopologyEntry
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

func (s *Service) PreviewWorktreeDelete(ctx context.Context, req *worktreepb.DeletePreviewRequest) (*worktreepb.DeletePreviewSuccess, error) {
	resolution, err := s.resolveWorktreeSelector(ctx, req.SessionId, req.Selector)
	if err != nil {
		return nil, err
	}
	selector, err := deletionSelector(resolution.match.entry)
	if err != nil {
		return nil, err
	}
	cleanliness, err := s.evaluateDeleteCleanliness(ctx, resolution.match.entry)
	if err != nil {
		return nil, err
	}
	return &worktreepb.DeletePreviewSuccess{
		Worktree:         resolution.match.entry,
		DeletionSelector: selector,
		Cleanliness:      cleanliness,
	}, nil
}

func deletionSelector(entry *worktreepb.TopologyEntry) (string, error) {
	switch {
	case entry == nil:
		return "", errors.New("worktree topology entry is required")
	case entry.GetMainWorkspace() != nil:
		return "", worktreecontract.ErrWorktreeBlocked
	case entry.GetRegistered() != nil:
		if entry.GetRegistered().GetGit().GetIsMainWorktree() {
			return "", worktreecontract.ErrWorktreeBlocked
		}
		return entry.GetRegistered().GetKent().GetWorktreeId(), nil
	case entry.GetExternal() != nil:
		if entry.GetExternal().GetGit().GetIsMainWorktree() {
			return "", worktreecontract.ErrWorktreeBlocked
		}
		return entry.GetExternal().GetGit().GetCanonicalRoot(), nil
	case entry.GetMissing() != nil:
		return entry.GetMissing().GetKent().GetWorktreeId(), nil
	default:
		return "", errors.New("worktree topology variant is invalid")
	}
}

func gitFactsFromEntry(entry GitWorktree) *worktreepb.GitFacts {
	facts := &worktreepb.GitFacts{
		CanonicalRoot:  strings.TrimSpace(entry.Root),
		HeadObject:     strings.TrimSpace(entry.HeadOID),
		Detached:       entry.Detached,
		Bare:           entry.Bare,
		IsMainWorktree: entry.IsMainWorktree,
		PathAvailable:  PathAvailability(entry.Root) == pathAvailabilityAvailable,
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

func kentFactsFromRecord(record metadata.WorktreeRecord) *worktreepb.KentFacts {
	facts := &worktreepb.KentFacts{
		WorktreeId:    strings.TrimSpace(record.ID),
		CanonicalRoot: strings.TrimSpace(record.CanonicalRoot),
		DisplayName:   strings.TrimSpace(record.DisplayName),
		Managed:       record.Managed,
		CreatedBranch: record.CreatedBranch,
	}
	if value := strings.TrimSpace(record.OriginSessionID); value != "" {
		facts.OriginSessionId = &value
	}
	return facts
}

func registeredTopologyEntry(item syncedWorktree) *worktreepb.TopologyEntry {
	return &worktreepb.TopologyEntry{Topology: &worktreepb.TopologyEntry_Registered{
		Registered: &worktreepb.RegisteredFacts{
			Git:  gitFactsFromEntry(item.git),
			Kent: kentFactsFromRecord(item.record),
		},
	}}
}

func topologyEntryByWorktreeID(entries []*worktreepb.TopologyEntry, worktreeID string) (*worktreepb.TopologyEntry, bool) {
	for _, entry := range entries {
		id := topologyWorktreeID(entry)
		if id != nil && strings.TrimSpace(*id) == strings.TrimSpace(worktreeID) {
			return entry, true
		}
	}
	return nil, false
}
