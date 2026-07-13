package worktree

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"core/server/metadata"
	"core/server/session"
	"core/shared/clientui"
	"core/shared/serverapi"
)

func (s *Service) DeleteWorktree(ctx context.Context, req serverapi.WorktreeDeleteRequest) (serverapi.WorktreeDeleteResult, error) {
	if err := req.Validate(); err != nil {
		return serverapi.WorktreeDeleteResult{}, err
	}
	transitionRequest := worktreeTransitionRequest{
		operationID: req.OperationID,
		sessionID:   strings.TrimSpace(req.SessionID),
		kind:        clientui.WorktreeTransitionDelete,
		selector:    strings.TrimSpace(req.Selector),
		force:       req.ForceFolderRemoval,
		cleanup:     req.BranchCleanupPolicy,
	}
	if ack, ok := s.replayPendingWorktreeTransition(transitionRequest); ok {
		return serverapi.WorktreeDeleteResult{
			Kind:      serverapi.WorktreeDeleteResultKindScheduled,
			Scheduled: &ack,
		}, nil
	}
	release, workspaceCtx, err := s.beginWorkspaceMutation(ctx, req.SessionID)
	if err != nil {
		return serverapi.WorktreeDeleteResult{}, err
	}
	topology, err := s.projectTopology(ctx, workspaceCtx.workspaceID, workspaceCtx.workspaceRoot)
	if err != nil {
		release()
		return serverapi.WorktreeDeleteResult{}, err
	}
	match, err := resolveTopologySelector(topology, req.Selector)
	if err != nil {
		release()
		return serverapi.WorktreeDeleteResult{}, err
	}
	if topologyIsMain(match.entry) {
		release()
		return serverapi.WorktreeDeleteResult{}, fmt.Errorf("cannot delete main workspace worktree: %w", serverapi.ErrWorktreeBlocked)
	}
	if topologyIsCurrent(match.entry, workspaceCtx.target) {
		deleteTarget, err := scheduledKentWorktreeTargetFromEntry(match.entry)
		if err != nil {
			release()
			return serverapi.WorktreeDeleteResult{}, err
		}
		release()
		ack, err := s.scheduleWorktreeTransition(ctx, transitionRequest, func(runCtx context.Context, sync transitionTargetSync) error {
			_, err := s.executeScheduledDelete(runCtx, req, deleteTarget, sync)
			return err
		})
		if err != nil {
			return serverapi.WorktreeDeleteResult{}, err
		}
		return serverapi.WorktreeDeleteResult{
			Kind:      serverapi.WorktreeDeleteResultKindScheduled,
			Scheduled: &ack,
		}, nil
	}
	defer release()
	completed, err := s.executeDeleteLocked(ctx, workspaceCtx, match.entry, req, nil)
	if err != nil {
		return serverapi.WorktreeDeleteResult{}, err
	}
	return serverapi.WorktreeDeleteResult{
		Kind:      serverapi.WorktreeDeleteResultKindCompleted,
		Completed: &completed,
	}, nil
}

func (s *Service) executeScheduledDelete(
	ctx context.Context,
	req serverapi.WorktreeDeleteRequest,
	deleteTarget scheduledWorktreeTarget,
	sync transitionTargetSync,
) (serverapi.WorktreeDeleteCompletedResult, error) {
	release, workspaceCtx, err := s.beginWorkspaceMutation(ctx, req.SessionID)
	if err != nil {
		return serverapi.WorktreeDeleteCompletedResult{}, err
	}
	defer release()
	topology, err := s.projectTopology(ctx, workspaceCtx.workspaceID, workspaceCtx.workspaceRoot)
	if err != nil {
		return serverapi.WorktreeDeleteCompletedResult{}, err
	}
	entry, err := deleteTarget.resolve(topology)
	if err != nil {
		return serverapi.WorktreeDeleteCompletedResult{}, err
	}
	return s.executeDeleteLocked(ctx, workspaceCtx, entry, req, sync)
}

func (s *Service) executeDeleteLocked(
	ctx context.Context,
	workspaceCtx sessionWorkspaceContext,
	entry serverapi.WorktreeTopologyEntry,
	req serverapi.WorktreeDeleteRequest,
	currentSync transitionTargetSync,
) (serverapi.WorktreeDeleteCompletedResult, error) {
	if topologyIsMain(entry) {
		return serverapi.WorktreeDeleteCompletedResult{}, fmt.Errorf("cannot delete main workspace worktree: %w", serverapi.ErrWorktreeBlocked)
	}
	target, record, err := s.deleteTarget(ctx, workspaceCtx, entry)
	if err != nil {
		return serverapi.WorktreeDeleteCompletedResult{}, err
	}
	if record != nil {
		if err := s.ensureNoManagedTaskBlockers(ctx, record.ID); err != nil {
			return serverapi.WorktreeDeleteCompletedResult{}, err
		}
	}
	releaseRuns, blockers, err := s.freezeDeleteTargetSessions(ctx, workspaceCtx.sessionID, record)
	if err != nil {
		return serverapi.WorktreeDeleteCompletedResult{}, err
	}
	defer releaseRuns()
	if len(blockers) > 0 {
		return serverapi.WorktreeDeleteCompletedResult{}, activeDeleteBlockerError(blockers)
	}
	if target != nil {
		if processBlockers := s.backgroundProcessBlockers(target.record.CanonicalRoot); len(processBlockers) > 0 {
			return serverapi.WorktreeDeleteCompletedResult{}, errors.Join(serverapi.ErrWorktreeBlocked, fmt.Errorf("worktree has active background processes: %s", strings.Join(processBlockers, ", ")))
		}
		dirtyState, err := s.git.ProbeDirtyState(ctx, target.record.CanonicalRoot)
		if err != nil {
			return serverapi.WorktreeDeleteCompletedResult{}, err
		}
		if dirtyState.Kind != serverapi.WorktreeDirtyStateClean && !req.ForceFolderRemoval {
			return serverapi.WorktreeDeleteCompletedResult{}, &serverapi.WorktreeDeletePreconditionError{DirtyState: dirtyState}
		}
	}
	if record != nil {
		if err := s.retargetDeleteSessions(ctx, workspaceCtx, *record, currentSync); err != nil {
			return serverapi.WorktreeDeleteCompletedResult{}, err
		}
	}
	leftoverRoot := missingLeftoverRoot(entry)
	if target != nil {
		if err := s.git.Remove(ctx, workspaceCtx.workspaceRoot, target.record.CanonicalRoot, req.ForceFolderRemoval); err != nil {
			return serverapi.WorktreeDeleteCompletedResult{}, err
		}
	}
	if record != nil {
		if err := s.metadata.DeleteWorktreeRecordByID(ctx, record.ID); err != nil {
			return serverapi.WorktreeDeleteCompletedResult{}, err
		}
	}
	cleanup := s.cleanupDeletedBranch(ctx, workspaceCtx.workspaceRoot, entry, record, req.BranchCleanupPolicy)
	result := serverapi.WorktreeDeleteCompletedResult{Cleanup: cleanup, LeftoverRoot: leftoverRoot}
	if err := result.Validate(); err != nil {
		return serverapi.WorktreeDeleteCompletedResult{}, err
	}
	return result, nil
}

func (s *Service) deleteTarget(
	ctx context.Context,
	workspaceCtx sessionWorkspaceContext,
	entry serverapi.WorktreeTopologyEntry,
) (*syncedWorktree, *metadata.WorktreeRecord, error) {
	switch entry.Variant {
	case serverapi.WorktreeTopologyVariantRegistered:
		record, err := s.metadata.GetWorktreeRecordByID(ctx, entry.Registered.Kent.WorktreeID)
		if err != nil {
			return nil, nil, err
		}
		target := syncedWorktree{record: record, git: gitWorktreeFromFacts(entry.Registered.Git)}
		return &target, &record, nil
	case serverapi.WorktreeTopologyVariantExternal:
		target := syncedWorktree{
			record: metadata.WorktreeRecord{
				WorkspaceID:   workspaceCtx.workspaceID,
				CanonicalRoot: entry.External.Git.CanonicalRoot,
				DisplayName:   filepath.Base(entry.External.Git.CanonicalRoot),
			},
			git: gitWorktreeFromFacts(entry.External.Git),
		}
		return &target, nil, nil
	case serverapi.WorktreeTopologyVariantMissing:
		record, err := s.metadata.GetWorktreeRecordByID(ctx, entry.Missing.Kent.WorktreeID)
		if err != nil {
			return nil, nil, err
		}
		return nil, &record, nil
	default:
		return nil, nil, errors.New("worktree topology variant is invalid")
	}
}

func topologyIsMain(entry serverapi.WorktreeTopologyEntry) bool {
	switch entry.Variant {
	case serverapi.WorktreeTopologyVariantRegistered:
		return entry.Registered.Git.IsMain
	case serverapi.WorktreeTopologyVariantExternal:
		return entry.External.Git.IsMain
	default:
		return false
	}
}

func (s *Service) ensureNoManagedTaskBlockers(ctx context.Context, worktreeID string) error {
	taskBlockers, err := s.metadata.Queries().CountNonTerminalTasksByManagedWorktree(ctx, sql.NullString{String: strings.TrimSpace(worktreeID), Valid: true})
	if err != nil {
		return err
	}
	if taskBlockers > 0 {
		return errors.Join(serverapi.ErrWorktreeBlocked, fmt.Errorf("worktree is still managed by %d non-terminal workflow task(s)", taskBlockers))
	}
	return nil
}

func (s *Service) freezeDeleteTargetSessions(
	ctx context.Context,
	currentSessionID string,
	record *metadata.WorktreeRecord,
) (func(), []metadata.WorktreeSessionBlocker, error) {
	if record == nil {
		return func() {}, nil, nil
	}
	sessions, err := s.metadata.ListSessionsTargetingWorktree(ctx, record.ID)
	if err != nil {
		return func() {}, nil, err
	}
	sessionIDs := make([]string, 0, len(sessions))
	for _, target := range sessions {
		if id := strings.TrimSpace(target.SessionID); id != "" {
			sessionIDs = append(sessionIDs, id)
		}
	}
	release := func() {}
	if s.active != nil && len(sessionIDs) > 0 {
		release = s.active.BlockSessionRuns(sessionIDs)
	}
	activeBlockers := make([]metadata.WorktreeSessionBlocker, 0)
	for _, target := range sessions {
		sessionID := strings.TrimSpace(target.SessionID)
		if sessionID == "" || sessionID == strings.TrimSpace(currentSessionID) {
			continue
		}
		active, err := s.runtime.HasBlockingRuntimeActivity(ctx, sessionID)
		if err != nil {
			release()
			return func() {}, nil, err
		}
		if active {
			activeBlockers = append(activeBlockers, target)
		}
	}
	return release, activeBlockers, nil
}

func activeDeleteBlockerError(blockers []metadata.WorktreeSessionBlocker) error {
	sort.Slice(blockers, func(i int, j int) bool {
		return blockers[i].UpdatedAt.After(blockers[j].UpdatedAt)
	})
	names := make([]string, 0, len(blockers))
	for _, blocker := range blockers {
		name := strings.TrimSpace(blocker.SessionName)
		if name == "" {
			name = strings.TrimSpace(blocker.SessionID)
		}
		names = append(names, name)
	}
	return errors.Join(serverapi.ErrWorktreeBlocked, fmt.Errorf("worktree is still targeted by active runs: %s", strings.Join(names, ", ")))
}

func (s *Service) retargetDeleteSessions(
	ctx context.Context,
	workspaceCtx sessionWorkspaceContext,
	record metadata.WorktreeRecord,
	currentSync transitionTargetSync,
) error {
	return s.retargetSessionsFromWorktree(
		ctx,
		workspaceCtx.workspaceID,
		workspaceCtx.workspaceRoot,
		record,
		worktreeSessionRetargetOptions{
			reminder: worktreeReminderStateForExitedWorktree,
			sync: func(
				syncCtx context.Context,
				sessionID string,
				target clientui.SessionExecutionTarget,
				reminder *session.WorktreeReminderState,
			) error {
				return s.syncDeleteSession(syncCtx, workspaceCtx.sessionID, sessionID, target, reminder, currentSync)
			},
			rollbackOnError: true,
		},
	)
}

func (s *Service) syncDeleteSession(
	ctx context.Context,
	currentSessionID string,
	sessionID string,
	target clientui.SessionExecutionTarget,
	reminder *session.WorktreeReminderState,
	currentSync transitionTargetSync,
) error {
	if strings.TrimSpace(sessionID) == strings.TrimSpace(currentSessionID) && currentSync != nil {
		return currentSync(ctx, target, reminder)
	}
	return s.runtime.SyncExecutionTarget(ctx, sessionID, target, reminder)
}

func missingLeftoverRoot(entry serverapi.WorktreeTopologyEntry) *string {
	if entry.Variant != serverapi.WorktreeTopologyVariantMissing {
		return nil
	}
	root := strings.TrimSpace(entry.Missing.Kent.CanonicalRoot)
	if _, err := os.Stat(root); err != nil {
		return nil
	}
	return &root
}

func (s *Service) cleanupDeletedBranch(
	ctx context.Context,
	workspaceRoot string,
	entry serverapi.WorktreeTopologyEntry,
	record *metadata.WorktreeRecord,
	policy serverapi.WorktreeBranchCleanupMode,
) serverapi.WorktreeBranchCleanupOutcome {
	gitEntry, live, err := branchCleanupGitEntry(entry, record)
	if err != nil {
		return serverapi.WorktreeBranchCleanupOutcome{Kind: serverapi.WorktreeBranchCleanupOutcomeNotApplicable}
	}
	branchName := worktreeNamedBranch(gitEntry)
	if branchName == "" {
		return serverapi.WorktreeBranchCleanupOutcome{Kind: serverapi.WorktreeBranchCleanupOutcomeNotApplicable}
	}
	switch policy {
	case serverapi.WorktreeBranchCleanupModeRetain:
		return serverapi.WorktreeBranchCleanupOutcome{Kind: serverapi.WorktreeBranchCleanupOutcomeNotRequested}
	case serverapi.WorktreeBranchCleanupModeAutoIfKentCreated:
		if record == nil {
			diagnostic := "Kent cannot prove this worktree created the branch"
			return serverapi.WorktreeBranchCleanupOutcome{
				Kind:       serverapi.WorktreeBranchCleanupOutcomeRetained,
				BranchName: &branchName,
				Diagnostic: &diagnostic,
			}
		}
		createdBranch, proven, err := kentCreatedBranchForCleanup(*record, live)
		if err != nil {
			diagnostic := err.Error()
			return serverapi.WorktreeBranchCleanupOutcome{
				Kind:       serverapi.WorktreeBranchCleanupOutcomeRetained,
				BranchName: &branchName,
				Diagnostic: &diagnostic,
			}
		}
		if !proven {
			diagnostic := "Kent cannot prove this worktree created the branch"
			return serverapi.WorktreeBranchCleanupOutcome{
				Kind:       serverapi.WorktreeBranchCleanupOutcomeRetained,
				BranchName: &branchName,
				Diagnostic: &diagnostic,
			}
		}
		branchName = createdBranch
	case serverapi.WorktreeBranchCleanupModeDeleteSafe:
	default:
		panic(fmt.Sprintf("invalid branch cleanup policy %q", policy))
	}
	if err := s.git.deleteBranch(ctx, workspaceRoot, branchName, false); err != nil {
		diagnostic := err.Error()
		return serverapi.WorktreeBranchCleanupOutcome{
			Kind:       serverapi.WorktreeBranchCleanupOutcomeRetained,
			BranchName: &branchName,
			Diagnostic: &diagnostic,
		}
	}
	return serverapi.WorktreeBranchCleanupOutcome{
		Kind:       serverapi.WorktreeBranchCleanupOutcomeDeleted,
		BranchName: &branchName,
	}
}

func branchCleanupGitEntry(
	entry serverapi.WorktreeTopologyEntry,
	record *metadata.WorktreeRecord,
) (GitWorktree, *GitWorktree, error) {
	switch entry.Variant {
	case serverapi.WorktreeTopologyVariantRegistered:
		live := gitWorktreeFromFacts(entry.Registered.Git)
		return live, &live, nil
	case serverapi.WorktreeTopologyVariantExternal:
		live := gitWorktreeFromFacts(entry.External.Git)
		return live, &live, nil
	case serverapi.WorktreeTopologyVariantMissing:
		if record == nil {
			return GitWorktree{}, nil, nil
		}
		persisted, err := worktreeGitMetadataFromRecord(*record)
		return persisted, nil, err
	default:
		return GitWorktree{}, nil, errors.New("worktree topology variant is invalid")
	}
}
