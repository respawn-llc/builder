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
	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

func (s *Service) DeleteWorktree(ctx context.Context, req serverapi.WorktreeDeleteRequest) (serverapi.WorktreeDeleteResult, error) {
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
	if _, err := match.entry.DeletionSelector(); err != nil {
		release()
		return serverapi.WorktreeDeleteResult{}, err
	}
	defer release()
	result, err := s.executeDeleteLocked(ctx, workspaceCtx, match.entry, req)
	if err != nil {
		return serverapi.WorktreeDeleteResult{}, err
	}
	return result, nil
}

func (s *Service) executeDeleteLocked(
	ctx context.Context,
	workspaceCtx sessionWorkspaceContext,
	entry serverapi.WorktreeTopologyEntry,
	req serverapi.WorktreeDeleteRequest,
) (serverapi.WorktreeDeleteResult, error) {
	if _, err := entry.DeletionSelector(); err != nil {
		return serverapi.WorktreeDeleteResult{}, err
	}
	target, record, err := s.deleteTarget(ctx, workspaceCtx, entry)
	if err != nil {
		return serverapi.WorktreeDeleteResult{}, err
	}
	retainRecord, err := s.retainManagedTaskWorktreeRecord(ctx, record)
	if err != nil {
		return serverapi.WorktreeDeleteResult{}, err
	}
	var targetRoot *string
	if target != nil {
		targetRoot = &target.record.CanonicalRoot
	} else if record != nil {
		targetRoot = &record.CanonicalRoot
	}
	activityLease, err := s.acquireDeleteTargetActivity(ctx, record, targetRoot)
	if err != nil {
		return serverapi.WorktreeDeleteResult{}, err
	}
	defer activityLease.Close()
	mutationCtx := activityLease.Context()
	if err := s.ensureDeleteFolderRemovalAuthorized(ctx, entry, req.ForceFolderRemoval); err != nil {
		return serverapi.WorktreeDeleteResult{}, err
	}
	retargetCompensation := worktreeSessionRetargetCompensation{}
	if record != nil {
		retargetCompensation, err = s.retargetDeleteSessions(mutationCtx, workspaceCtx, *record)
		if err != nil {
			return serverapi.WorktreeDeleteResult{}, err
		}
	}
	leftoverRoot := missingLeftoverRoot(entry)
	if target != nil {
		var err error
		if req.ForceFolderRemoval && entry.Variant == serverapi.WorktreeTopologyVariantRegistered &&
			entry.Registered != nil && entry.Registered.Git.PrunableReason != nil {
			err = s.git.ForceRemovePrunableWorktree(ctx, workspaceCtx.workspaceRoot, target.record.CanonicalRoot)
		} else {
			err = s.git.Remove(ctx, workspaceCtx.workspaceRoot, target.record.CanonicalRoot, req.ForceFolderRemoval)
		}
		if err != nil {
			var recoveryError *PrunableWorktreeRecoveryError
			if errors.As(err, &recoveryError) && recoveryError.Destructive {
				return serverapi.WorktreeDeleteResult{}, err
			}
			return serverapi.WorktreeDeleteResult{}, errors.Join(err, retargetCompensation.rollback(mutationCtx))
		}
	}
	if record != nil && !retainRecord {
		if err := s.metadata.DeleteWorktreeRecordByID(ctx, record.ID); err != nil {
			return serverapi.WorktreeDeleteResult{}, err
		}
	}
	cleanup := s.cleanupDeletedBranch(ctx, workspaceCtx.workspaceRoot, entry, record, req.BranchCleanupPolicy)
	result := serverapi.WorktreeDeleteResult{Cleanup: cleanup, LeftoverRoot: leftoverRoot}
	if err := result.Validate(); err != nil {
		return serverapi.WorktreeDeleteResult{}, err
	}
	return result, nil
}

func (s *Service) ensureDeleteFolderRemovalAuthorized(
	ctx context.Context,
	entry serverapi.WorktreeTopologyEntry,
	forceFolderRemoval bool,
) error {
	dirtyState, err := s.evaluateDeleteCleanliness(ctx, entry)
	if err != nil {
		return err
	}
	if dirtyState.Kind != clientui.WorktreeDirtyStateClean && !forceFolderRemoval {
		return &serverapi.WorktreeDeletePreconditionError{DirtyState: dirtyState}
	}
	return nil
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
		gitEntry, err := gitWorktreeFromFacts(entry.Registered.Git)
		if err != nil {
			return nil, nil, err
		}
		target := syncedWorktree{record: record, git: gitEntry}
		return &target, &record, nil
	case serverapi.WorktreeTopologyVariantExternal:
		gitEntry, err := gitWorktreeFromFacts(entry.External.Git)
		if err != nil {
			return nil, nil, err
		}
		target := syncedWorktree{
			record: metadata.WorktreeRecord{
				WorkspaceID:   workspaceCtx.workspaceID,
				CanonicalRoot: entry.External.Git.CanonicalRoot,
				DisplayName:   filepath.Base(entry.External.Git.CanonicalRoot),
			},
			git: gitEntry,
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
					return deleteTargetActivityLease{}, errors.Join(serverapi.ErrWorktreeBlocked, err)
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
			return deleteTargetActivityLease{}, errors.Join(serverapi.ErrWorktreeBlocked, fmt.Errorf("worktree has active background processes: %s", strings.Join(processBlockers, ", ")))
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
	return errors.Join(serverapi.ErrWorktreeBlocked, fmt.Errorf("worktree is still targeted by active runs: %s", strings.Join(names, ", ")))
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
	branchName, named := worktreeNamedBranch(gitEntry)
	if !named {
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
	case serverapi.WorktreeBranchCleanupModeDeleteForce:
	default:
		panic(fmt.Sprintf("invalid branch cleanup policy %q", policy))
	}
	force := policy == serverapi.WorktreeBranchCleanupModeDeleteForce
	if err := s.git.deleteBranch(ctx, workspaceRoot, branchName, force); err != nil {
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
		live, err := gitWorktreeFromFacts(entry.Registered.Git)
		if err != nil {
			return GitWorktree{}, nil, err
		}
		return live, &live, nil
	case serverapi.WorktreeTopologyVariantExternal:
		live, err := gitWorktreeFromFacts(entry.External.Git)
		if err != nil {
			return GitWorktree{}, nil, err
		}
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
