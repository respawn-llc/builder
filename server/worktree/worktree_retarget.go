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
	filter          worktreeSessionRetargetFilter
	reminder        worktreeReminderFactory
	sync            worktreeSessionTargetSync
	rollbackOnError bool
}

type pendingWorktreeSessionRetarget struct {
	sessionID      string
	previousTarget clientui.SessionExecutionTarget
}

func (s *Service) retargetActiveSessionsFromDeletedWorktree(ctx context.Context, workspaceID string, workspaceRoot string, worktree metadata.WorktreeRecord, currentSessionID string) error {
	trimmedCurrentSessionID := strings.TrimSpace(currentSessionID)
	return s.retargetSessionsFromWorktree(ctx, workspaceID, workspaceRoot, worktree, worktreeSessionRetargetOptions{
		filter: func(blocker metadata.WorktreeSessionBlocker) bool {
			sessionID := strings.TrimSpace(blocker.SessionID)
			if sessionID == "" || sessionID == trimmedCurrentSessionID {
				return false
			}
			return s.active != nil && s.active.IsSessionRuntimeActive(sessionID)
		},
		reminder:        worktreeReminderStateForExitedWorktree,
		rollbackOnError: true,
	})
}

func (s *Service) retargetSessionsFromWorktree(ctx context.Context, workspaceID string, workspaceRoot string, worktree metadata.WorktreeRecord, options worktreeSessionRetargetOptions) error {
	if s == nil || s.metadata == nil || s.runtime == nil {
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
		targetSync = s.runtime.SyncExecutionTarget
	}
	targetSessionIDs := make([]string, 0, len(blockers))
	for _, blocker := range blockers {
		sessionID := strings.TrimSpace(blocker.SessionID)
		if sessionID == "" {
			continue
		}
		if options.filter != nil && !options.filter(blocker) {
			continue
		}
		targetSessionIDs = append(targetSessionIDs, sessionID)
	}
	releaseRuns := func() {}
	if s.active != nil && len(targetSessionIDs) > 0 {
		releaseRuns = s.active.BlockSessionRuns(targetSessionIDs)
	}
	defer releaseRuns()
	pending := make([]pendingWorktreeSessionRetarget, 0, len(blockers))
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
			if options.rollbackOnError {
				return errors.Join(errors.Join(collected...), s.rollbackRetargetedSessions(ctx, pending, targetSync))
			}
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
			if options.rollbackOnError {
				return errors.Join(errors.Join(collected...), s.rollbackRetargetedSessions(ctx, pending, targetSync))
			}
			continue
		}
		pending = append(pending, pendingWorktreeSessionRetarget{sessionID: blocker.SessionID, previousTarget: previousTarget})
	}
	for _, item := range pending {
		nextTarget, err := s.metadata.ResolveSessionExecutionTarget(ctx, item.sessionID)
		if err != nil {
			appendErr(item.sessionID, err)
			if options.rollbackOnError {
				return errors.Join(errors.Join(collected...), s.rollbackRetargetedSessions(ctx, pending, targetSync))
			}
			continue
		}
		reminder, err := reminderFactory(worktree, nextTarget)
		if err != nil {
			appendErr(item.sessionID, err)
			if options.rollbackOnError {
				return errors.Join(errors.Join(collected...), s.rollbackRetargetedSessions(ctx, pending, targetSync))
			}
			continue
		}
		if err := targetSync(ctx, item.sessionID, nextTarget, &reminder); err != nil {
			appendErr(item.sessionID, err)
			if options.rollbackOnError {
				return errors.Join(errors.Join(collected...), s.rollbackRetargetedSessions(ctx, pending, targetSync))
			}
			rollbackCtx, cancel := liveRollbackContext(ctx)
			rollbackErr := s.metadata.UpdateSessionExecutionTarget(rollbackCtx, metadata.SessionExecutionTargetUpdateFromReadModel(item.sessionID, item.previousTarget))
			cancel()
			if rollbackErr != nil {
				appendErr(item.sessionID, errors.Join(err, fmt.Errorf("rollback execution target after runtime sync failure: %w", rollbackErr)))
				continue
			}
			continue
		}
	}
	return errors.Join(collected...)
}

func (s *Service) rollbackRetargetedSessions(
	ctx context.Context,
	pending []pendingWorktreeSessionRetarget,
	targetSync worktreeSessionTargetSync,
) error {
	if len(pending) == 0 {
		return nil
	}
	if targetSync == nil {
		return errors.New("worktree session target synchronizer is required")
	}
	collected := make([]error, 0)
	for i := len(pending) - 1; i >= 0; i-- {
		item := pending[i]
		sessionID := strings.TrimSpace(item.sessionID)
		rollbackCtx, cancel := liveRollbackContext(ctx)
		if err := s.metadata.UpdateSessionExecutionTarget(rollbackCtx, metadata.SessionExecutionTargetUpdateFromReadModel(sessionID, item.previousTarget)); err != nil {
			collected = append(collected, fmt.Errorf("rollback session %q execution target: %w", sessionID, err))
			cancel()
			continue
		}
		if err := s.runtime.ClearWorktreeReminder(rollbackCtx, sessionID); err != nil {
			collected = append(collected, fmt.Errorf("rollback session %q worktree reminder: %w", sessionID, err))
		}
		if err := targetSync(rollbackCtx, sessionID, item.previousTarget, nil); err != nil {
			collected = append(collected, fmt.Errorf("rollback session %q runtime target: %w", sessionID, err))
		}
		cancel()
	}
	return errors.Join(collected...)
}

func worktreeHasStableIdentity(entry GitWorktree) bool {
	return strings.TrimSpace(entry.BranchRef) != "" || strings.TrimSpace(entry.HeadOID) != "" || entry.Detached || entry.IsMain || entry.Bare
}

func (s *Service) switchSessionTarget(ctx context.Context, workspaceCtx sessionWorkspaceContext, previous *syncedWorktree, next syncedWorktree) (clientui.SessionExecutionTarget, error) {
	return s.switchSessionTargetWithSync(ctx, workspaceCtx, previous, next, func(syncCtx context.Context, target clientui.SessionExecutionTarget, reminder *session.WorktreeReminderState) error {
		return s.runtime.SyncExecutionTarget(syncCtx, workspaceCtx.sessionID, target, reminder)
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
	if s.active != nil {
		if sessionID := strings.TrimSpace(workspaceCtx.sessionID); sessionID != "" {
			release := s.active.BlockSessionRuns([]string{sessionID})
			defer release()
		}
	}
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
		s.rollbackSessionTargetWithSync(ctx, workspaceCtx, previousTarget, sync)
		return clientui.SessionExecutionTarget{}, err
	}
	reminder, ok, err := worktreeReminderStateForTransition(previous, previousTarget, next, nextTarget)
	if err != nil {
		s.rollbackSessionTargetWithSync(ctx, workspaceCtx, previousTarget, sync)
		return clientui.SessionExecutionTarget{}, err
	}
	if ok {
		if err := sync(ctx, nextTarget, &reminder); err != nil {
			s.rollbackSessionTargetWithSync(ctx, workspaceCtx, previousTarget, sync)
			return clientui.SessionExecutionTarget{}, err
		}
		return nextTarget, nil
	}
	if err := sync(ctx, nextTarget, nil); err != nil {
		s.rollbackSessionTargetWithSync(ctx, workspaceCtx, previousTarget, sync)
		return clientui.SessionExecutionTarget{}, err
	}
	return nextTarget, nil
}

func (s *Service) applySessionTarget(ctx context.Context, sessionID string, target clientui.SessionExecutionTarget, reminder *session.WorktreeReminderState) error {
	return s.runtime.SyncExecutionTarget(ctx, sessionID, target, reminder)
}

func (s *Service) rollbackSessionTarget(ctx context.Context, workspaceCtx sessionWorkspaceContext, previousTarget clientui.SessionExecutionTarget) {
	s.rollbackSessionTargetWithSync(ctx, workspaceCtx, previousTarget, func(syncCtx context.Context, target clientui.SessionExecutionTarget, reminder *session.WorktreeReminderState) error {
		return s.runtime.SyncExecutionTarget(syncCtx, workspaceCtx.sessionID, target, reminder)
	})
}

func (s *Service) rollbackSessionTargetWithSync(
	ctx context.Context,
	workspaceCtx sessionWorkspaceContext,
	previousTarget clientui.SessionExecutionTarget,
	sync transitionTargetSync,
) {
	rollbackCtx, cancel := liveRollbackContext(ctx)
	defer cancel()
	_ = s.metadata.UpdateSessionExecutionTarget(rollbackCtx, metadata.SessionExecutionTargetUpdateFromReadModel(workspaceCtx.sessionID, previousTarget))
	_ = s.runtime.ClearWorktreeReminder(rollbackCtx, workspaceCtx.sessionID)
	if strings.TrimSpace(previousTarget.EffectiveWorkdir) != "" {
		_ = sync(rollbackCtx, previousTarget, nil)
	}
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
			Mode:          session.WorktreeReminderModeExit,
			Branch:        strings.TrimSpace(previous.git.BranchName),
			WorktreePath:  strings.TrimSpace(previous.record.CanonicalRoot),
			WorkspaceRoot: strings.TrimSpace(nextTarget.WorkspaceRoot),
			EffectiveCwd:  strings.TrimSpace(nextTarget.EffectiveWorkdir),
		}, true, nil
	}
	return session.WorktreeReminderState{
		Mode:          session.WorktreeReminderModeEnter,
		Branch:        strings.TrimSpace(next.git.BranchName),
		WorktreePath:  strings.TrimSpace(next.record.CanonicalRoot),
		WorkspaceRoot: strings.TrimSpace(nextTarget.WorkspaceRoot),
		EffectiveCwd:  strings.TrimSpace(nextTarget.EffectiveWorkdir),
	}, true, nil
}

func worktreeReminderStateForExitedWorktree(worktree metadata.WorktreeRecord, nextTarget clientui.SessionExecutionTarget) (session.WorktreeReminderState, error) {
	gitMetadata, err := worktreeGitMetadataFromRecord(worktree)
	if err != nil {
		return session.WorktreeReminderState{}, err
	}
	branchName := strings.TrimSpace(gitMetadata.BranchName)
	if branchName == "" {
		branchName = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(gitMetadata.BranchRef), "refs/heads/"))
	}
	return session.WorktreeReminderState{
		Mode:          session.WorktreeReminderModeExit,
		Branch:        branchName,
		WorktreePath:  strings.TrimSpace(worktree.CanonicalRoot),
		WorkspaceRoot: strings.TrimSpace(nextTarget.WorkspaceRoot),
		EffectiveCwd:  strings.TrimSpace(nextTarget.EffectiveWorkdir),
	}, nil
}

func worktreeGitMetadataFromRecord(worktree metadata.WorktreeRecord) (GitWorktree, error) {
	metadataJSON := strings.TrimSpace(worktree.GitMetadataJSON)
	if metadataJSON == "" {
		return GitWorktree{}, nil
	}
	var persisted struct {
		HeadOID        string `json:"head_oid"`
		BranchRef      string `json:"branch_ref"`
		BranchName     string `json:"branch_name"`
		Detached       bool   `json:"detached"`
		Bare           bool   `json:"bare"`
		LockedReason   string `json:"locked_reason"`
		PrunableReason string `json:"prunable_reason"`
	}
	if err := json.Unmarshal([]byte(metadataJSON), &persisted); err != nil {
		return GitWorktree{}, fmt.Errorf("decode git worktree metadata: %w", err)
	}
	return GitWorktree{
		Root:           worktree.CanonicalRoot,
		HeadOID:        persisted.HeadOID,
		BranchRef:      persisted.BranchRef,
		BranchName:     persisted.BranchName,
		Detached:       persisted.Detached,
		Bare:           persisted.Bare,
		LockedReason:   persisted.LockedReason,
		PrunableReason: persisted.PrunableReason,
		IsMain:         worktree.IsMain,
	}, nil
}
