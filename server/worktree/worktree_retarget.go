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

type worktreeSessionRetargetCompensation struct {
	service    *Service
	pending    []pendingWorktreeSessionRetarget
	targetSync worktreeSessionTargetSync
}

func applyWorktreeTargetMutation[T any](
	write func() error,
	finish func() (T, error),
	rollback func() error,
) (T, error) {
	var zero T
	if write == nil || finish == nil || rollback == nil {
		return zero, worktreeUnappliedTechnical(errors.New("Worktree target mutation callbacks are required"))
	}
	if err := write(); err != nil {
		return zero, worktreeUnappliedTechnical(err)
	}
	value, finishErr := finish()
	if finishErr == nil {
		return value, finishErr
	}
	finishIndeterminate := isWorktreeIndeterminate(finishErr)
	finishUnapplied := isWorktreeUnapplied(finishErr)
	if isWorktreeApplied(finishErr) && !finishIndeterminate && !finishUnapplied {
		return value, finishErr
	}
	rollbackErr := rollback()
	if (rollbackErr == nil || isWorktreeApplied(rollbackErr)) &&
		finishUnapplied &&
		!finishIndeterminate {
		return value, worktreeUnappliedWithDiagnostic(
			finishErr,
			errors.Join(finishErr, worktreeAppliedDiagnostic(rollbackErr)),
		)
	}
	return value, worktreeIndeterminate(errors.Join(finishErr, rollbackErr))
}

func (compensation worktreeSessionRetargetCompensation) rollback(ctx context.Context) error {
	if len(compensation.pending) == 0 {
		return nil
	}
	if compensation.service == nil {
		return errors.New("worktree session retarget compensation service is required")
	}
	return compensation.service.rollbackRetargetedSessions(ctx, compensation.pending, compensation.targetSync)
}

func (s *Service) retargetSessionsFromWorktree(
	ctx context.Context,
	workspaceID string,
	workspaceRoot string,
	worktree metadata.WorktreeRecord,
	options worktreeSessionRetargetOptions,
) (worktreeSessionRetargetCompensation, error) {
	if s == nil || s.metadata == nil || s.authority == nil || s.publisher == nil {
		return worktreeSessionRetargetCompensation{}, errors.New("worktree service dependencies are required")
	}
	trimmedWorkspaceID := strings.TrimSpace(workspaceID)
	trimmedWorkspaceRoot := strings.TrimSpace(workspaceRoot)
	trimmedWorktreeID := strings.TrimSpace(worktree.ID)
	if trimmedWorkspaceID == "" || trimmedWorkspaceRoot == "" || trimmedWorktreeID == "" {
		return worktreeSessionRetargetCompensation{}, nil
	}
	blockers, err := s.metadata.ListSessionsTargetingWorktree(ctx, trimmedWorktreeID)
	if err != nil {
		return worktreeSessionRetargetCompensation{}, err
	}
	reminderFactory := options.reminder
	if reminderFactory == nil {
		reminderFactory = worktreeReminderStateForExitedWorktree
	}
	targetSync := options.sync
	if targetSync == nil {
		targetSync = s.syncExecutionTarget
	}
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
				return worktreeSessionRetargetCompensation{}, errors.Join(errors.Join(collected...), s.rollbackRetargetedSessions(ctx, pending, targetSync))
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
				return worktreeSessionRetargetCompensation{}, errors.Join(errors.Join(collected...), s.rollbackRetargetedSessions(ctx, pending, targetSync))
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
				return worktreeSessionRetargetCompensation{}, errors.Join(errors.Join(collected...), s.rollbackRetargetedSessions(ctx, pending, targetSync))
			}
			continue
		}
		reminder, err := reminderFactory(worktree, nextTarget)
		if err != nil {
			appendErr(item.sessionID, err)
			if options.rollbackOnError {
				return worktreeSessionRetargetCompensation{}, errors.Join(errors.Join(collected...), s.rollbackRetargetedSessions(ctx, pending, targetSync))
			}
			continue
		}
		if err := targetSync(ctx, item.sessionID, nextTarget, &reminder); err != nil {
			appendErr(item.sessionID, err)
			if options.rollbackOnError {
				return worktreeSessionRetargetCompensation{}, errors.Join(errors.Join(collected...), s.rollbackRetargetedSessions(ctx, pending, targetSync))
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
	if err := errors.Join(collected...); err != nil {
		return worktreeSessionRetargetCompensation{}, err
	}
	return worktreeSessionRetargetCompensation{
		service:    s,
		pending:    pending,
		targetSync: targetSync,
	}, nil
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
		if err := s.authority.ClearWorktreeReminder(rollbackCtx, sessionID); err != nil {
			collected = append(collected, fmt.Errorf("rollback session %q worktree reminder: %w", sessionID, err))
		}
		if err := targetSync(rollbackCtx, sessionID, item.previousTarget, nil); err != nil {
			collected = append(collected, fmt.Errorf("rollback session %q runtime target: %w", sessionID, err))
		}
		cancel()
	}
	return errors.Join(collected...)
}

func (s *Service) switchSessionTarget(ctx context.Context, workspaceCtx sessionWorkspaceContext, previous *syncedWorktree, next syncedWorktree) (clientui.SessionExecutionTarget, error) {
	target, err := s.switchSessionTargetWithSync(ctx, workspaceCtx, previous, next, nil, func(syncCtx context.Context, target clientui.SessionExecutionTarget, reminder *session.WorktreeReminderState) error {
		return s.syncExecutionTarget(syncCtx, workspaceCtx.sessionID, target, reminder)
	})
	return target, err
}

func (s *Service) switchSessionTargetWithSync(
	ctx context.Context,
	workspaceCtx sessionWorkspaceContext,
	previous *syncedWorktree,
	next syncedWorktree,
	stepAuthority transitionAuthority,
	sync transitionTargetSync,
) (clientui.SessionExecutionTarget, error) {
	if sync == nil {
		return clientui.SessionExecutionTarget{}, worktreeUnappliedTechnical(errors.New("execution target synchronizer is required"))
	}
	if stepAuthority == nil {
		sessionIDs, err := parseSessionStartAdmissionIDs([]string{workspaceCtx.sessionID})
		if err != nil {
			return clientui.SessionExecutionTarget{}, worktreeUnappliedTechnical(err)
		}
		release, err := s.acquireSessionStartAdmission(ctx, sessionIDs, sessionStartAdmissionWait)
		if err != nil {
			return clientui.SessionExecutionTarget{}, worktreeUnappliedTechnical(err)
		}
		defer releaseSessionStarts(release)
		ctx = authorizeSessionMaintenance(ctx, release)
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
		return clientui.SessionExecutionTarget{}, worktreeUnappliedTechnical(err)
	}
	cwdRelpath := clampCwdRelpath(previousTarget.CwdRelpath, nextBaseRoot)
	return applyWorktreeTargetMutation(
		func() error {
			return s.metadata.UpdateSessionExecutionTarget(ctx, metadata.SessionExecutionTargetUpdate{
				SessionID:  workspaceCtx.sessionID,
				Workspace:  &metadata.SessionExecutionTargetUpdateWorkspace{ID: workspaceCtx.workspaceID},
				Worktree:   nextWorktree,
				CwdRelpath: cwdRelpath,
			})
		},
		func() (clientui.SessionExecutionTarget, error) {
			nextTarget, err := s.metadata.ResolveSessionExecutionTarget(ctx, workspaceCtx.sessionID)
			if err != nil {
				return clientui.SessionExecutionTarget{}, worktreeUnappliedTechnical(err)
			}
			reminder, ok, err := worktreeReminderStateForTransition(previous, previousTarget, next, nextTarget)
			if err != nil {
				return clientui.SessionExecutionTarget{}, worktreeUnappliedTechnical(err)
			}
			if ok {
				return nextTarget, worktreeUnappliedTechnicalUnlessClassified(sync(ctx, nextTarget, &reminder))
			}
			return nextTarget, worktreeUnappliedTechnicalUnlessClassified(sync(ctx, nextTarget, nil))
		},
		func() error {
			return s.rollbackSessionTargetWithSync(ctx, workspaceCtx, previousTarget, sync)
		},
	)
}

func (s *Service) rollbackSessionTargetWithSync(
	ctx context.Context,
	workspaceCtx sessionWorkspaceContext,
	previousTarget clientui.SessionExecutionTarget,
	sync transitionTargetSync,
) error {
	rollbackCtx, cancel := liveRollbackContext(ctx)
	defer cancel()
	var collected []error
	indeterminate := false
	if err := s.metadata.UpdateSessionExecutionTarget(rollbackCtx, metadata.SessionExecutionTargetUpdateFromReadModel(workspaceCtx.sessionID, previousTarget)); err != nil {
		collected = append(collected, fmt.Errorf("rollback execution target: %w", err))
		indeterminate = true
	}
	if err := s.authority.ClearWorktreeReminder(rollbackCtx, workspaceCtx.sessionID); err != nil {
		collected = append(collected, fmt.Errorf("rollback worktree reminder: %w", err))
		indeterminate = true
	}
	if strings.TrimSpace(previousTarget.EffectiveWorkdir) != "" {
		if err := sync(rollbackCtx, previousTarget, nil); err != nil {
			collected = append(collected, fmt.Errorf("rollback runtime target: %w", err))
			if !isWorktreeApplied(err) {
				indeterminate = true
			}
		}
	}
	err := errors.Join(collected...)
	if indeterminate {
		return worktreeIndeterminate(err)
	}
	return worktreeApplied(err)
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
