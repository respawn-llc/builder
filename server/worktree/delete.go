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
	"core/server/sessionruntime"
	worktreepb "core/shared/protoapi/gen/kent/api/worktree"
	"core/shared/runtimeids"
	"core/shared/worktreecontract"
)

func (s *Service) DeleteWorktree(ctx context.Context, req *worktreepb.DeleteRequest) (*worktreepb.DeleteSuccess, error) {
	release, workspaceCtx, err := s.beginWorkspaceMutation(ctx, req.SessionId)
	if err != nil {
		return nil, err
	}
	topology, err := s.projectTopology(ctx, workspaceCtx.workspaceID, workspaceCtx.workspaceRoot)
	if err != nil {
		release()
		return nil, err
	}
	match, err := resolveTopologySelector(topology, req.Selector)
	if err != nil {
		release()
		return nil, err
	}
	if _, err := deletionSelector(match); err != nil {
		release()
		return nil, err
	}
	defer release()
	result, err := s.executeDeleteLocked(ctx, workspaceCtx, match.entry, req)
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (s *Service) executeDeleteLocked(
	ctx context.Context,
	workspaceCtx sessionWorkspaceContext,
	entry *worktreepb.TopologyEntry,
	req *worktreepb.DeleteRequest,
) (*worktreepb.DeleteSuccess, error) {
	target, record, err := s.deleteTarget(ctx, workspaceCtx, entry)
	if err != nil {
		return nil, err
	}
	retainRecord, err := s.retainManagedTaskWorktreeRecord(ctx, record)
	if err != nil {
		return nil, err
	}
	var targetRoot *string
	if target != nil {
		targetRoot = &target.record.CanonicalRoot
	} else if record != nil {
		targetRoot = &record.CanonicalRoot
	}
	activityLease, err := s.acquireDeleteTargetActivity(ctx, record, targetRoot)
	if err != nil {
		return nil, err
	}
	defer activityLease.Close()
	mutationCtx := activityLease.Context()
	if err := s.ensureDeleteFolderRemovalAuthorized(ctx, entry, req.ForceFolderRemoval); err != nil {
		return nil, err
	}
	retargetCompensation := worktreeSessionRetargetCompensation{}
	if record != nil {
		retargetCompensation, err = s.retargetDeleteSessions(mutationCtx, workspaceCtx, *record)
		if err != nil {
			return nil, err
		}
	}
	leftoverRoot := missingLeftoverRoot(entry)
	if target != nil {
		var err error
		if req.ForceFolderRemoval && entry.GetRegistered() != nil &&
			entry.GetRegistered().GetGit().PrunableReason != nil {
			err = s.git.ForceRemovePrunableWorktree(ctx, workspaceCtx.workspaceRoot, target.record.CanonicalRoot)
		} else {
			err = s.git.Remove(ctx, workspaceCtx.workspaceRoot, target.record.CanonicalRoot, req.ForceFolderRemoval)
		}
		if err != nil {
			var recoveryError *PrunableWorktreeRecoveryError
			if errors.As(err, &recoveryError) && recoveryError.Destructive {
				return nil, err
			}
			return nil, errors.Join(err, retargetCompensation.rollback(mutationCtx))
		}
	}
	if record != nil && !retainRecord {
		if err := s.metadata.DeleteWorktreeRecordByID(ctx, record.ID); err != nil {
			return nil, err
		}
	}
	cleanup := s.cleanupDeletedBranch(ctx, workspaceCtx.workspaceRoot, entry, record, req.BranchCleanupPolicy)
	return &worktreepb.DeleteSuccess{Cleanup: cleanup, LeftoverRoot: leftoverRoot}, nil
}

func (s *Service) ensureDeleteFolderRemovalAuthorized(
	ctx context.Context,
	entry *worktreepb.TopologyEntry,
	forceFolderRemoval bool,
) error {
	dirtyState, err := s.evaluateDeleteCleanliness(ctx, entry)
	if err != nil {
		return err
	}
	if dirtyState.Kind != worktreepb.DirtyStateKind_DIRTY_STATE_CLEAN && !forceFolderRemoval {
		return worktreecontract.NewDeletePreconditionError(dirtyState)
	}
	return nil
}

func (s *Service) deleteTarget(
	ctx context.Context,
	workspaceCtx sessionWorkspaceContext,
	entry *worktreepb.TopologyEntry,
) (*syncedWorktree, *metadata.WorktreeRecord, error) {
	switch {
	case entry.GetRegistered() != nil:
		record, err := s.metadata.GetWorktreeRecordByID(ctx, entry.GetRegistered().GetKent().GetWorktreeId())
		if err != nil {
			return nil, nil, err
		}
		gitEntry, err := gitWorktreeFromFacts(entry.GetRegistered().GetGit())
		if err != nil {
			return nil, nil, err
		}
		target := syncedWorktree{record: record, git: gitEntry}
		return &target, &record, nil
	case entry.GetExternal() != nil:
		gitEntry, err := gitWorktreeFromFacts(entry.GetExternal().GetGit())
		if err != nil {
			return nil, nil, err
		}
		target := syncedWorktree{
			record: metadata.WorktreeRecord{
				WorkspaceID:   workspaceCtx.workspaceID,
				CanonicalRoot: entry.GetExternal().GetGit().GetCanonicalRoot(),
				DisplayName:   filepath.Base(entry.GetExternal().GetGit().GetCanonicalRoot()),
			},
			git: gitEntry,
		}
		return &target, nil, nil
	case entry.GetMissing() != nil:
		record, err := s.metadata.GetWorktreeRecordByID(ctx, entry.GetMissing().GetKent().GetWorktreeId())
		if err != nil {
			return nil, nil, err
		}
		return nil, &record, nil
	default:
		return nil, nil, errors.New("worktree topology variant is invalid")
	}
}

func (s *Service) retainManagedTaskWorktreeRecord(ctx context.Context, record *metadata.WorktreeRecord) (bool, error) {
	if record == nil {
		return false, nil
	}
	taskManagers, err := s.metadata.Queries().CountNonTerminalTasksByManagedWorktree(ctx, sql.NullString{
		String: strings.TrimSpace(record.ID),
		Valid:  strings.TrimSpace(record.ID) != "",
	})
	if err != nil {
		return false, err
	}
	return taskManagers > 0, nil
}

func (s *Service) acquireDeleteTargetActivity(
	ctx context.Context,
	record *metadata.WorktreeRecord,
	worktreeRoot *string,
) (deleteTargetActivityLease, error) {
	lease := deleteTargetActivityLease{ctx: ctx, close: func() {}}
	if worktreeRoot != nil && strings.TrimSpace(*worktreeRoot) == "" {
		return deleteTargetActivityLease{}, errors.New("delete target root must not be blank when present")
	}
	if record != nil {
		sessions, err := s.metadata.ListSessionsTargetingWorktree(ctx, record.ID)
		if err != nil {
			return deleteTargetActivityLease{}, err
		}
		type targetSession struct {
			id      runtimeids.SessionID
			blocker metadata.WorktreeSessionBlocker
		}
		targets := make([]targetSession, 0, len(sessions))
		for _, target := range sessions {
			sessionID, err := runtimeids.ParseSessionID(target.SessionID)
			if err != nil {
				return deleteTargetActivityLease{}, fmt.Errorf("parse worktree-targeting session id %q: %w", target.SessionID, err)
			}
			targets = append(targets, targetSession{id: sessionID, blocker: target})
		}
		if len(targets) > 0 {
			sessionIDs := make([]runtimeids.SessionID, 0, len(targets))
			for _, target := range targets {
				sessionIDs = append(sessionIDs, target.id)
			}
			startBlock, err := s.acquireSessionStartAdmission(ctx, sessionIDs, sessionStartAdmissionTry)
			if err != nil {
				if errors.Is(err, sessionruntime.ErrSessionStartAdmissionBusy) {
					return deleteTargetActivityLease{}, errors.Join(worktreecontract.ErrWorktreeBlocked, err)
				}
				return deleteTargetActivityLease{}, err
			}
			lease.close = func() { releaseSessionStarts(startBlock) }
			lease.ctx = authorizeSessionMaintenance(ctx, startBlock)
		}
		activeBlockers := make([]metadata.WorktreeSessionBlocker, 0, len(targets))
		for _, target := range targets {
			active, err := s.authority.HasBlockingRuntimeActivity(ctx, target.id.String())
			if err != nil {
				lease.Close()
				return deleteTargetActivityLease{}, err
			}
			if active {
				activeBlockers = append(activeBlockers, target.blocker)
				continue
			}
			retired, err := s.authority.RetireIdleRuntime(lease.ctx, target.id.String())
			if err != nil {
				lease.Close()
				return deleteTargetActivityLease{}, err
			}
			if !retired {
				activeBlockers = append(activeBlockers, target.blocker)
			}
		}
		if len(activeBlockers) > 0 {
			lease.Close()
			return deleteTargetActivityLease{}, activeDeleteBlockerError(activeBlockers)
		}
	}
	if worktreeRoot != nil {
		if processBlockers := s.backgroundProcessBlockers(*worktreeRoot); len(processBlockers) > 0 {
			lease.Close()
			return deleteTargetActivityLease{}, errors.Join(worktreecontract.ErrWorktreeBlocked, fmt.Errorf("worktree has active background processes: %s", strings.Join(processBlockers, ", ")))
		}
	}
	return lease, nil
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
	return errors.Join(worktreecontract.ErrWorktreeBlocked, fmt.Errorf("worktree is still targeted by active runs: %s", strings.Join(names, ", ")))
}

func (s *Service) retargetDeleteSessions(
	ctx context.Context,
	workspaceCtx sessionWorkspaceContext,
	record metadata.WorktreeRecord,
) (worktreeSessionRetargetCompensation, error) {
	return s.retargetSessionsFromWorktree(
		ctx,
		workspaceCtx.workspaceID,
		workspaceCtx.workspaceRoot,
		record,
		worktreeSessionRetargetOptions{
			reminder:        worktreeReminderStateForExitedWorktree,
			sync:            s.syncExecutionTarget,
			rollbackOnError: true,
		},
	)
}

func missingLeftoverRoot(entry *worktreepb.TopologyEntry) *string {
	if entry.GetMissing() == nil {
		return nil
	}
	root := strings.TrimSpace(entry.GetMissing().GetKent().GetCanonicalRoot())
	if _, err := os.Stat(root); err != nil {
		return nil
	}
	return &root
}

func (s *Service) cleanupDeletedBranch(
	ctx context.Context,
	workspaceRoot string,
	entry *worktreepb.TopologyEntry,
	record *metadata.WorktreeRecord,
	policy worktreepb.BranchCleanupMode,
) *worktreepb.BranchCleanupOutcome {
	gitEntry, live, err := branchCleanupGitEntry(entry, record)
	if err != nil {
		return &worktreepb.BranchCleanupOutcome{
			Kind: worktreepb.BranchCleanupOutcomeKind_WORKTREE_BRANCH_CLEANUP_OUTCOME_NOT_APPLICABLE,
		}
	}
	branchName, named := worktreeNamedBranch(gitEntry)
	if !named {
		return &worktreepb.BranchCleanupOutcome{
			Kind: worktreepb.BranchCleanupOutcomeKind_WORKTREE_BRANCH_CLEANUP_OUTCOME_NOT_APPLICABLE,
		}
	}
	switch policy {
	case worktreepb.BranchCleanupMode_WORKTREE_BRANCH_CLEANUP_MODE_RETAIN:
		return &worktreepb.BranchCleanupOutcome{
			Kind: worktreepb.BranchCleanupOutcomeKind_WORKTREE_BRANCH_CLEANUP_OUTCOME_NOT_REQUESTED,
		}
	case worktreepb.BranchCleanupMode_WORKTREE_BRANCH_CLEANUP_MODE_AUTO_IF_KENT_CREATED:
		if record == nil {
			diagnostic := "Kent cannot prove this worktree created the branch"
			return &worktreepb.BranchCleanupOutcome{
				Kind:       worktreepb.BranchCleanupOutcomeKind_WORKTREE_BRANCH_CLEANUP_OUTCOME_RETAINED,
				BranchName: &branchName,
				Diagnostic: &diagnostic,
			}
		}
		createdBranch, proven, err := kentCreatedBranchForCleanup(*record, live)
		if err != nil {
			diagnostic := err.Error()
			return &worktreepb.BranchCleanupOutcome{
				Kind:       worktreepb.BranchCleanupOutcomeKind_WORKTREE_BRANCH_CLEANUP_OUTCOME_RETAINED,
				BranchName: &branchName,
				Diagnostic: &diagnostic,
			}
		}
		if !proven {
			diagnostic := "Kent cannot prove this worktree created the branch"
			return &worktreepb.BranchCleanupOutcome{
				Kind:       worktreepb.BranchCleanupOutcomeKind_WORKTREE_BRANCH_CLEANUP_OUTCOME_RETAINED,
				BranchName: &branchName,
				Diagnostic: &diagnostic,
			}
		}
		branchName = createdBranch
	case worktreepb.BranchCleanupMode_WORKTREE_BRANCH_CLEANUP_MODE_DELETE_SAFE:
	case worktreepb.BranchCleanupMode_WORKTREE_BRANCH_CLEANUP_MODE_DELETE_FORCE:
	default:
		panic(fmt.Sprintf("invalid branch cleanup policy %q", policy))
	}
	force := policy == worktreepb.BranchCleanupMode_WORKTREE_BRANCH_CLEANUP_MODE_DELETE_FORCE
	if err := s.git.deleteBranch(ctx, workspaceRoot, branchName, force); err != nil {
		diagnostic := err.Error()
		return &worktreepb.BranchCleanupOutcome{
			Kind:       worktreepb.BranchCleanupOutcomeKind_WORKTREE_BRANCH_CLEANUP_OUTCOME_RETAINED,
			BranchName: &branchName,
			Diagnostic: &diagnostic,
		}
	}
	return &worktreepb.BranchCleanupOutcome{
		Kind:       worktreepb.BranchCleanupOutcomeKind_WORKTREE_BRANCH_CLEANUP_OUTCOME_DELETED,
		BranchName: &branchName,
	}
}

func branchCleanupGitEntry(
	entry *worktreepb.TopologyEntry,
	record *metadata.WorktreeRecord,
) (GitWorktree, *GitWorktree, error) {
	switch {
	case entry.GetRegistered() != nil:
		live, err := gitWorktreeFromFacts(entry.GetRegistered().GetGit())
		if err != nil {
			return GitWorktree{}, nil, err
		}
		return live, &live, nil
	case entry.GetExternal() != nil:
		live, err := gitWorktreeFromFacts(entry.GetExternal().GetGit())
		if err != nil {
			return GitWorktree{}, nil, err
		}
		return live, &live, nil
	case entry.GetMissing() != nil:
		if record == nil {
			return GitWorktree{}, nil, nil
		}
		persisted, err := worktreeGitMetadataFromRecord(*record)
		return persisted, nil, err
	default:
		return GitWorktree{}, nil, errors.New("worktree topology variant is invalid")
	}
}
