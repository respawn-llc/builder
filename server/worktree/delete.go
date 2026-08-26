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
	"core/server/sessionruntime"
	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/worktreecontract"
)

func (s *Service) DeleteWorktree(ctx context.Context, req worktreecontract.DeleteRequest) (worktreecontract.DeleteResult, error) {
	if err := req.Validate(); err != nil {
		return worktreecontract.DeleteResult{}, err
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
		return worktreecontract.DeleteResult{
			Kind:      worktreecontract.DeleteResultKindScheduled,
			Scheduled: &ack,
		}, nil
	}
	release, workspaceCtx, err := s.beginWorkspaceMutation(ctx, req.SessionID)
	if err != nil {
		return worktreecontract.DeleteResult{}, err
	}
	topology, err := s.projectTopology(ctx, workspaceCtx.workspaceID, workspaceCtx.workspaceRoot)
	if err != nil {
		release()
		return worktreecontract.DeleteResult{}, err
	}
	match, err := resolveTopologySelector(topology, req.Selector)
	if err != nil {
		release()
		return worktreecontract.DeleteResult{}, err
	}
	if _, err := match.entry.DeletionSelector(); err != nil {
		release()
		return worktreecontract.DeleteResult{}, err
	}
	if topologyIsCurrent(match.entry, workspaceCtx.target) {
		if err := s.ensureDeleteFolderRemovalAuthorized(ctx, match.entry, req.ForceFolderRemoval); err != nil {
			release()
			return worktreecontract.DeleteResult{}, err
		}
		deleteTarget, err := scheduledKentWorktreeTargetFromEntry(match.entry)
		if err != nil {
			release()
			return worktreecontract.DeleteResult{}, err
		}
		release()
		ack, err := s.scheduleWorktreeTransition(ctx, transitionRequest, func(runCtx context.Context, _ transitionAuthority, sync transitionTargetSync) error {
			_, err := s.executeScheduledDelete(runCtx, req, deleteTarget, sync)
			return err
		}, req.TransitionHeader.Origin)
		if err != nil {
			return worktreecontract.DeleteResult{}, err
		}
		return worktreecontract.DeleteResult{
			Kind:      worktreecontract.DeleteResultKindScheduled,
			Scheduled: &ack,
		}, nil
	}
	defer release()
	completed, err := s.executeDeleteLocked(ctx, workspaceCtx, match.entry, req, nil)
	if err != nil {
		return worktreecontract.DeleteResult{}, err
	}
	return worktreecontract.DeleteResult{
		Kind:      worktreecontract.DeleteResultKindCompleted,
		Completed: &completed,
	}, nil
}

func (s *Service) executeScheduledDelete(
	ctx context.Context,
	req worktreecontract.DeleteRequest,
	deleteTarget scheduledWorktreeTarget,
	sync transitionTargetSync,
) (worktreecontract.DeleteCompletedResult, error) {
	release, workspaceCtx, err := s.beginWorkspaceMutation(ctx, req.SessionID)
	if err != nil {
		return worktreecontract.DeleteCompletedResult{}, err
	}
	defer release()
	topology, err := s.projectTopology(ctx, workspaceCtx.workspaceID, workspaceCtx.workspaceRoot)
	if err != nil {
		return worktreecontract.DeleteCompletedResult{}, err
	}
	entry, err := deleteTarget.resolve(topology)
	if err != nil {
		return worktreecontract.DeleteCompletedResult{}, err
	}
	return s.executeDeleteLocked(ctx, workspaceCtx, entry, req, sync)
}

func (s *Service) executeDeleteLocked(
	ctx context.Context,
	workspaceCtx sessionWorkspaceContext,
	entry worktreecontract.TopologyEntry,
	req worktreecontract.DeleteRequest,
	currentSync transitionTargetSync,
) (worktreecontract.DeleteCompletedResult, error) {
	if _, err := entry.DeletionSelector(); err != nil {
		return worktreecontract.DeleteCompletedResult{}, err
	}
	target, record, err := s.deleteTarget(ctx, workspaceCtx, entry)
	if err != nil {
		return worktreecontract.DeleteCompletedResult{}, err
	}
	retainRecord, err := s.retainManagedTaskWorktreeRecord(ctx, record)
	if err != nil {
		return worktreecontract.DeleteCompletedResult{}, err
	}
	var targetRoot *string
	if target != nil {
		targetRoot = &target.record.CanonicalRoot
	} else if record != nil {
		targetRoot = &record.CanonicalRoot
	}
	currentSessionID, err := deleteActivityCurrentSessionID(workspaceCtx.sessionID)
	if err != nil {
		return worktreecontract.DeleteCompletedResult{}, err
	}
	activityLease, err := s.acquireDeleteTargetActivity(ctx, currentSessionID, record, targetRoot)
	if err != nil {
		return worktreecontract.DeleteCompletedResult{}, err
	}
	defer activityLease.Close()
	mutationCtx := activityLease.Context()
	if err := s.ensureDeleteFolderRemovalAuthorized(ctx, entry, req.ForceFolderRemoval); err != nil {
		return worktreecontract.DeleteCompletedResult{}, err
	}
	retargetCompensation := worktreeSessionRetargetCompensation{}
	if record != nil {
		retargetCompensation, err = s.retargetDeleteSessions(mutationCtx, workspaceCtx, *record, currentSync)
		if err != nil {
			return worktreecontract.DeleteCompletedResult{}, err
		}
	}
	leftoverRoot := missingLeftoverRoot(entry)
	if target != nil {
		var err error
		if req.ForceFolderRemoval && entry.Variant == worktreecontract.TopologyVariantRegistered &&
			entry.Registered != nil && entry.Registered.Git.PrunableReason != nil {
			err = s.git.ForceRemovePrunableWorktree(ctx, workspaceCtx.workspaceRoot, target.record.CanonicalRoot)
		} else {
			err = s.git.Remove(ctx, workspaceCtx.workspaceRoot, target.record.CanonicalRoot, req.ForceFolderRemoval)
		}
		if err != nil {
			var recoveryError *PrunableWorktreeRecoveryError
			if errors.As(err, &recoveryError) && recoveryError.Destructive {
				return worktreecontract.DeleteCompletedResult{}, err
			}
			return worktreecontract.DeleteCompletedResult{}, errors.Join(err, retargetCompensation.rollback(mutationCtx))
		}
	}
	if record != nil && !retainRecord {
		if err := s.metadata.DeleteWorktreeRecordByID(ctx, record.ID); err != nil {
			return worktreecontract.DeleteCompletedResult{}, err
		}
	}
	cleanup := s.cleanupDeletedBranch(ctx, workspaceCtx.workspaceRoot, entry, record, req.BranchCleanupPolicy)
	result := worktreecontract.DeleteCompletedResult{Cleanup: cleanup, LeftoverRoot: leftoverRoot}
	if err := result.Validate(); err != nil {
		return worktreecontract.DeleteCompletedResult{}, err
	}
	return result, nil
}

func (s *Service) ensureDeleteFolderRemovalAuthorized(
	ctx context.Context,
	entry worktreecontract.TopologyEntry,
	forceFolderRemoval bool,
) error {
	dirtyState, err := s.evaluateDeleteCleanliness(ctx, entry)
	if err != nil {
		return err
	}
	if dirtyState.Kind != worktreecontract.DirtyStateClean && !forceFolderRemoval {
		return &worktreecontract.DeletePreconditionError{DirtyState: dirtyState}
	}
	return nil
}

func (s *Service) deleteTarget(
	ctx context.Context,
	workspaceCtx sessionWorkspaceContext,
	entry worktreecontract.TopologyEntry,
) (*syncedWorktree, *metadata.WorktreeRecord, error) {
	switch entry.Variant {
	case worktreecontract.TopologyVariantRegistered:
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
	case worktreecontract.TopologyVariantExternal:
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
	case worktreecontract.TopologyVariantMissing:
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

func deleteActivityCurrentSessionID(raw string) (*runtimeids.SessionID, error) {
	sessionID, err := runtimeids.ParseSessionID(raw)
	if err != nil {
		return nil, fmt.Errorf("parse current delete session id: %w", err)
	}
	return &sessionID, nil
}

func (s *Service) acquireDeleteTargetActivity(
	ctx context.Context,
	currentSessionID *runtimeids.SessionID,
	record *metadata.WorktreeRecord,
	worktreeRoot *string,
) (deleteTargetActivityLease, error) {
	lease := deleteTargetActivityLease{ctx: ctx, close: func() {}}
	if currentSessionID != nil && currentSessionID.IsZero() {
		return deleteTargetActivityLease{}, errors.New("current delete session id must not be blank when present")
	}
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
			if currentSessionID != nil && sessionID == *currentSessionID {
				continue
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
	currentSync transitionTargetSync,
) (worktreeSessionRetargetCompensation, error) {
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
	return s.syncExecutionTarget(ctx, sessionID, target, reminder)
}

func missingLeftoverRoot(entry worktreecontract.TopologyEntry) *string {
	if entry.Variant != worktreecontract.TopologyVariantMissing {
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
	entry worktreecontract.TopologyEntry,
	record *metadata.WorktreeRecord,
	policy worktreecontract.BranchCleanupMode,
) worktreecontract.BranchCleanupOutcome {
	gitEntry, live, err := branchCleanupGitEntry(entry, record)
	if err != nil {
		return worktreecontract.BranchCleanupOutcome{Kind: worktreecontract.BranchCleanupOutcomeNotApplicable}
	}
	branchName, named := worktreeNamedBranch(gitEntry)
	if !named {
		return worktreecontract.BranchCleanupOutcome{Kind: worktreecontract.BranchCleanupOutcomeNotApplicable}
	}
	switch policy {
	case worktreecontract.BranchCleanupModeRetain:
		return worktreecontract.BranchCleanupOutcome{Kind: worktreecontract.BranchCleanupOutcomeNotRequested}
	case worktreecontract.BranchCleanupModeAutoIfKentCreated:
		if record == nil {
			diagnostic := "Kent cannot prove this worktree created the branch"
			return worktreecontract.BranchCleanupOutcome{
				Kind:       worktreecontract.BranchCleanupOutcomeRetained,
				BranchName: &branchName,
				Diagnostic: &diagnostic,
			}
		}
		createdBranch, proven, err := kentCreatedBranchForCleanup(*record, live)
		if err != nil {
			diagnostic := err.Error()
			return worktreecontract.BranchCleanupOutcome{
				Kind:       worktreecontract.BranchCleanupOutcomeRetained,
				BranchName: &branchName,
				Diagnostic: &diagnostic,
			}
		}
		if !proven {
			diagnostic := "Kent cannot prove this worktree created the branch"
			return worktreecontract.BranchCleanupOutcome{
				Kind:       worktreecontract.BranchCleanupOutcomeRetained,
				BranchName: &branchName,
				Diagnostic: &diagnostic,
			}
		}
		branchName = createdBranch
	case worktreecontract.BranchCleanupModeDeleteSafe:
	case worktreecontract.BranchCleanupModeDeleteForce:
	default:
		panic(fmt.Sprintf("invalid branch cleanup policy %q", policy))
	}
	force := policy == worktreecontract.BranchCleanupModeDeleteForce
	if err := s.git.deleteBranch(ctx, workspaceRoot, branchName, force); err != nil {
		diagnostic := err.Error()
		return worktreecontract.BranchCleanupOutcome{
			Kind:       worktreecontract.BranchCleanupOutcomeRetained,
			BranchName: &branchName,
			Diagnostic: &diagnostic,
		}
	}
	return worktreecontract.BranchCleanupOutcome{
		Kind:       worktreecontract.BranchCleanupOutcomeDeleted,
		BranchName: &branchName,
	}
}

func branchCleanupGitEntry(
	entry worktreecontract.TopologyEntry,
	record *metadata.WorktreeRecord,
) (GitWorktree, *GitWorktree, error) {
	switch entry.Variant {
	case worktreecontract.TopologyVariantRegistered:
		live, err := gitWorktreeFromFacts(entry.Registered.Git)
		if err != nil {
			return GitWorktree{}, nil, err
		}
		return live, &live, nil
	case worktreecontract.TopologyVariantExternal:
		live, err := gitWorktreeFromFacts(entry.External.Git)
		if err != nil {
			return GitWorktree{}, nil, err
		}
		return live, &live, nil
	case worktreecontract.TopologyVariantMissing:
		if record == nil {
			return GitWorktree{}, nil, nil
		}
		persisted, err := worktreeGitMetadataFromRecord(*record)
		return persisted, nil, err
	default:
		return GitWorktree{}, nil, errors.New("worktree topology variant is invalid")
	}
}
