package worktree

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"core/server/metadata"
	"core/server/session"
	"core/shared/clientui"
)

type worktreeSessionRetargetFilter func(metadata.WorktreeSessionBlocker) bool

type worktreeReminderFactory func(metadata.WorktreeRecord, clientui.SessionExecutionTarget) (session.WorktreeReminderState, error)

type worktreeSessionTargetSync func(context.Context, string, clientui.SessionExecutionTarget, *session.WorktreeReminderState) error

type worktreeSessionRetargetOptions struct {
	filter   worktreeSessionRetargetFilter
	reminder worktreeReminderFactory
	sync     worktreeSessionTargetSync
}

func (s *Service) retargetSessionsFromWorktree(
	ctx context.Context,
	workspaceID string,
	workspaceRoot string,
	worktree metadata.WorktreeRecord,
	options worktreeSessionRetargetOptions,
) error {
	if s == nil || s.metadata == nil || s.authority == nil || s.publisher == nil {
		return errors.New("worktree service dependencies are required")
	}
	trimmedWorkspaceID := strings.TrimSpace(workspaceID)
	trimmedWorkspaceRoot := strings.TrimSpace(workspaceRoot)
	trimmedWorktreeID := strings.TrimSpace(worktree.ID)
	if trimmedWorkspaceID == "" || trimmedWorkspaceRoot == "" || trimmedWorktreeID == "" {
		return nil
	}
	blockers, err := s.metadata.ListSessionsTargetingWorktree(ctx, trimmedWorktreeID)
	if err != nil {
		return err
	}
	reminderFactory := options.reminder
	if reminderFactory == nil {
		reminderFactory = worktreeReminderStateForExitedWorktree
	}
	targetSync := options.sync
	if targetSync == nil {
		targetSync = s.syncExecutionTarget
	}
	collected := make([]error, 0)
	appendErr := func(sessionID string, err error) {
		collected = append(collected, fmt.Errorf("retarget session %q from worktree %q: %w", strings.TrimSpace(sessionID), trimmedWorktreeID, err))
	}
	for _, blocker := range blockers {
		if options.filter != nil && !options.filter(blocker) {
			continue
		}
		previousTarget, err := s.metadata.ResolveSessionExecutionTarget(ctx, blocker.SessionID)
		if err != nil {
			appendErr(blocker.SessionID, err)
			continue
		}
		cwdRelpath := clampCwdRelpath(previousTarget.CwdRelpath, trimmedWorkspaceRoot)
		if err := s.metadata.UpdateSessionExecutionTarget(ctx, metadata.SessionExecutionTargetUpdate{
			SessionID:  blocker.SessionID,
			Workspace:  &metadata.SessionExecutionTargetUpdateWorkspace{ID: trimmedWorkspaceID},
			Worktree:   nil,
			CwdRelpath: cwdRelpath,
		}); err != nil {
			appendErr(blocker.SessionID, err)
			continue
		}
		nextTarget, err := s.metadata.ResolveSessionExecutionTarget(ctx, blocker.SessionID)
		if err != nil {
			appendErr(blocker.SessionID, err)
			continue
		}
		reminder, err := reminderFactory(worktree, nextTarget)
		if err != nil {
			appendErr(blocker.SessionID, err)
			continue
		}
		if err := targetSync(ctx, blocker.SessionID, nextTarget, &reminder); err != nil {
			appendErr(blocker.SessionID, err)
		}
	}
	return errors.Join(collected...)
}

func (s *Service) switchSessionTarget(ctx context.Context, workspaceCtx sessionWorkspaceContext, previous *syncedWorktree, next syncedWorktree) (clientui.SessionExecutionTarget, error) {
	return s.switchSessionTargetWithSync(ctx, workspaceCtx, previous, next, func(syncCtx context.Context, target clientui.SessionExecutionTarget, reminder *session.WorktreeReminderState) error {
		return s.syncExecutionTarget(syncCtx, workspaceCtx.sessionID, target, reminder)
	})
}

func (s *Service) switchSessionTargetWithSync(
	ctx context.Context,
	workspaceCtx sessionWorkspaceContext,
	previous *syncedWorktree,
	next syncedWorktree,
	sync transitionTargetSync,
) (clientui.SessionExecutionTarget, error) {
	if sync == nil {
		return clientui.SessionExecutionTarget{}, errors.New("execution target synchronizer is required")
	}
	sessionIDs, err := parseSessionStartAdmissionIDs([]string{workspaceCtx.sessionID})
	if err != nil {
		return clientui.SessionExecutionTarget{}, err
	}
	release, err := s.acquireSessionStartAdmission(ctx, sessionIDs, sessionStartAdmissionWait)
	if err != nil {
		return clientui.SessionExecutionTarget{}, err
	}
	defer releaseSessionStarts(release)
	ctx = authorizeSessionMaintenance(ctx, release)
	nextWorktreeID := strings.TrimSpace(next.record.ID)
	nextBaseRoot := strings.TrimSpace(next.record.CanonicalRoot)
	var nextWorktree *metadata.SessionExecutionTargetUpdateWorktree
	if next.git.IsMain {
		nextBaseRoot = workspaceCtx.workspaceRoot
	} else {
		nextWorktree = &metadata.SessionExecutionTargetUpdateWorktree{ID: nextWorktreeID}
	}
	previousTarget := workspaceCtx.target
	if err := validatePresentExecutionTargetWorktreeID(previousTarget); err != nil {
		return clientui.SessionExecutionTarget{}, err
	}
	cwdRelpath := clampCwdRelpath(previousTarget.CwdRelpath, nextBaseRoot)
	if err := s.metadata.UpdateSessionExecutionTarget(ctx, metadata.SessionExecutionTargetUpdate{
		SessionID:  workspaceCtx.sessionID,
		Workspace:  &metadata.SessionExecutionTargetUpdateWorkspace{ID: workspaceCtx.workspaceID},
		Worktree:   nextWorktree,
		CwdRelpath: cwdRelpath,
	}); err != nil {
		return clientui.SessionExecutionTarget{}, err
	}
	nextTarget, err := s.metadata.ResolveSessionExecutionTarget(ctx, workspaceCtx.sessionID)
	if err != nil {
		return clientui.SessionExecutionTarget{}, err
	}
	reminder, ok, err := worktreeReminderStateForTransition(previous, previousTarget, next, nextTarget)
	if err != nil {
		return clientui.SessionExecutionTarget{}, err
	}
	if ok {
		if err := sync(ctx, nextTarget, &reminder); err != nil {
			return clientui.SessionExecutionTarget{}, err
		}
		return nextTarget, nil
	}
	if err := sync(ctx, nextTarget, nil); err != nil {
		return clientui.SessionExecutionTarget{}, err
	}
	return nextTarget, nil
}

func (s *Service) syncExecutionTarget(ctx context.Context, sessionID string, target clientui.SessionExecutionTarget, reminder *session.WorktreeReminderState) error {
	if err := s.authority.SyncExecutionTarget(ctx, sessionID, target, reminder); err != nil {
		return err
	}
	if err := s.publisher.PublishSessionIdentity(sessionID); err != nil {
		return fmt.Errorf("publish session identity: %w", err)
	}
	return nil
}

func liveRollbackContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		return context.WithTimeout(context.Background(), rollbackSessionTargetTimeout)
	}
	return context.WithTimeout(context.WithoutCancel(ctx), rollbackSessionTargetTimeout)
}

func worktreeReminderStateForTransition(previous *syncedWorktree, previousTarget clientui.SessionExecutionTarget, next syncedWorktree, nextTarget clientui.SessionExecutionTarget) (session.WorktreeReminderState, bool, error) {
	if err := validatePresentExecutionTargetWorktreeID(previousTarget); err != nil {
		return session.WorktreeReminderState{}, false, err
	}
	if next.git.IsMain {
		if previous == nil || previousTarget.Worktree == nil || previous.git.IsMain {
			return session.WorktreeReminderState{}, false, nil
		}
		return session.WorktreeReminderState{
			Mode: session.WorktreeReminderModeExit,
			WorktreeContext: session.WorktreeContext{
				Branch:        optionalWorktreeBranchName(previous.git.Branch),
				WorktreePath:  strings.TrimSpace(previous.record.CanonicalRoot),
				WorkspaceRoot: strings.TrimSpace(nextTarget.WorkspaceRoot),
				EffectiveCwd:  strings.TrimSpace(nextTarget.EffectiveWorkdir),
			},
		}, true, nil
	}
	return session.WorktreeReminderState{
		Mode: session.WorktreeReminderModeEnter,
		WorktreeContext: session.WorktreeContext{
			Branch:        optionalWorktreeBranchName(next.git.Branch),
			WorktreePath:  strings.TrimSpace(next.record.CanonicalRoot),
			WorkspaceRoot: strings.TrimSpace(nextTarget.WorkspaceRoot),
			EffectiveCwd:  strings.TrimSpace(nextTarget.EffectiveWorkdir),
		},
	}, true, nil
}

func worktreeReminderStateForExitedWorktree(worktree metadata.WorktreeRecord, nextTarget clientui.SessionExecutionTarget) (session.WorktreeReminderState, error) {
	gitMetadata, err := worktreeGitMetadataFromRecord(worktree)
	if err != nil {
		return session.WorktreeReminderState{}, err
	}
	return session.WorktreeReminderState{
		Mode: session.WorktreeReminderModeExit,
		WorktreeContext: session.WorktreeContext{
			Branch:        optionalWorktreeBranchName(gitMetadata.Branch),
			WorktreePath:  strings.TrimSpace(worktree.CanonicalRoot),
			WorkspaceRoot: strings.TrimSpace(nextTarget.WorkspaceRoot),
			EffectiveCwd:  strings.TrimSpace(nextTarget.EffectiveWorkdir),
		},
	}, nil
}

func optionalWorktreeBranchName(branch *localBranch) *string {
	if branch == nil {
		return nil
	}
	name := branch.Name()
	return &name
}

func worktreeGitMetadataFromRecord(worktree metadata.WorktreeRecord) (GitWorktree, error) {
	metadataJSON := strings.TrimSpace(worktree.GitMetadataJSON)
	if metadataJSON == "" {
		return GitWorktree{}, nil
	}
	var persisted persistedGitWorktree
	if err := json.Unmarshal([]byte(metadataJSON), &persisted); err != nil {
		return GitWorktree{}, fmt.Errorf("decode git worktree metadata: %w", err)
	}
	branch, err := optionalLocalBranch(persisted.BranchRef, persisted.BranchName)
	if err != nil {
		return GitWorktree{}, fmt.Errorf("decode git worktree metadata: %w", err)
	}
	decoded := GitWorktree{
		Root:           worktree.CanonicalRoot,
		HeadOID:        persisted.HeadOID,
		RecordedBranch: branch,
		Detached:       persisted.Detached,
		Bare:           persisted.Bare,
		LockedReason:   persisted.LockedReason,
		PrunableReason: persisted.PrunableReason,
		IsMain:         worktree.IsMain,
	}
	if !persisted.Detached {
		decoded.Branch = branch
	}
	if err := decoded.validateHead(); err != nil {
		return GitWorktree{}, fmt.Errorf("decode git worktree metadata: %w", err)
	}
	return decoded, nil
}
