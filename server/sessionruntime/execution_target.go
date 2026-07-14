package sessionruntime

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"core/server/registry"
	"core/server/runtime"
	"core/server/session"
	"core/shared/clientui"
	"core/shared/serverapi"
)

func (s *Service) SyncExecutionTarget(ctx context.Context, sessionID string, target clientui.SessionExecutionTarget, reminder *session.WorktreeReminderState) error {
	trimmedSessionID := strings.TrimSpace(sessionID)
	trimmedWorkdir := strings.TrimSpace(target.EffectiveWorkdir)
	if trimmedSessionID == "" {
		return errors.New("session id is required")
	}
	if trimmedWorkdir == "" {
		return errors.New("execution target effective workdir is required")
	}
	var normalizedReminder *session.WorktreeReminderState
	if reminder != nil {
		normalized, err := session.NormalizeWorktreeReminderState(*reminder)
		if err != nil {
			return err
		}
		normalizedReminder = &normalized
	}
	for {
		guard, err := s.activeRuntimeGuard(ctx, trimmedSessionID)
		if errors.Is(err, ErrAcquiredRuntimeOvertaken) {
			continue
		}
		if err != nil {
			return err
		}
		if guard == nil {
			return s.persistWorktreeReminderState(ctx, trimmedSessionID, normalizedReminder)
		}
		if err := s.syncActiveExecutionTarget(ctx, trimmedSessionID, trimmedWorkdir, guard, normalizedReminder); err != nil {
			return err
		}
		if s.runtimes != nil {
			s.runtimes.PublishSessionIdentity(trimmedSessionID, &target)
		}
		return nil
	}
}

func (s *Service) RunWorktreeTransition(
	ctx context.Context,
	sessionID string,
	fn func(context.Context, func(context.Context, clientui.SessionExecutionTarget, *session.WorktreeReminderState) error) error,
) error {
	return s.runWorktreeTransition(ctx, sessionID, false, fn)
}

// RunWorktreeTransitionImmediately applies a transition while the caller's
// tool command is still active. The caller must not acknowledge that command
// until this returns, so the next tool lookup observes the retargeted binding.
func (s *Service) RunWorktreeTransitionImmediately(
	ctx context.Context,
	sessionID string,
	fn func(context.Context, func(context.Context, clientui.SessionExecutionTarget, *session.WorktreeReminderState) error) error,
) error {
	return s.runWorktreeTransition(ctx, sessionID, true, fn)
}

func (s *Service) runWorktreeTransition(
	ctx context.Context,
	sessionID string,
	immediate bool,
	fn func(context.Context, func(context.Context, clientui.SessionExecutionTarget, *session.WorktreeReminderState) error) error,
) error {
	if fn == nil {
		return nil
	}
	trimmedSessionID := strings.TrimSpace(sessionID)
	if trimmedSessionID == "" {
		return errors.New("session id is required")
	}
	for {
		guard, err := s.activeRuntimeGuard(ctx, trimmedSessionID)
		if errors.Is(err, ErrAcquiredRuntimeOvertaken) {
			continue
		}
		if err != nil {
			return err
		}
		if guard == nil {
			return fn(ctx, func(syncCtx context.Context, target clientui.SessionExecutionTarget, reminder *session.WorktreeReminderState) error {
				if err := s.syncInactiveExecutionTarget(syncCtx, trimmedSessionID, target, reminder); err != nil {
					return err
				}
				if s.runtimes != nil {
					s.runtimes.PublishSessionIdentity(trimmedSessionID, &target)
				}
				return nil
			})
		}
		defer guard.Release()
		engine := guard.Engine()
		if engine == nil {
			return runtimeUnavailableErr(trimmedSessionID)
		}
		if immediate {
			return fn(ctx, func(syncCtx context.Context, target clientui.SessionExecutionTarget, reminder *session.WorktreeReminderState) error {
				return s.syncGuardedExecutionTarget(syncCtx, trimmedSessionID, target, guard, reminder)
			})
		}
		return engine.RunWhenIdleBeforeQueuedUserWork(ctx, runtime.ActiveKindRuntimeMaintenance, func() error {
			return fn(ctx, func(syncCtx context.Context, target clientui.SessionExecutionTarget, reminder *session.WorktreeReminderState) error {
				if err := s.syncGuardedExecutionTarget(syncCtx, trimmedSessionID, target, guard, reminder); err != nil {
					return err
				}
				if s.runtimes != nil {
					s.runtimes.PublishSessionIdentity(trimmedSessionID, &target)
				}
				return nil
			})
		})
	}
}

func (s *Service) PublishWorktreeTransitionOutcome(sessionID string, outcome clientui.WorktreeTransitionOutcome) {
	if s == nil || s.runtimes == nil {
		return
	}
	s.runtimes.PublishWorktreeTransitionOutcome(strings.TrimSpace(sessionID), outcome)
}

func (s *Service) SteerWorktreeTransitionFailure(ctx context.Context, sessionID string, outcome clientui.WorktreeTransitionOutcome) error {
	trimmedSessionID := strings.TrimSpace(sessionID)
	if trimmedSessionID == "" {
		return errors.New("session id is required")
	}
	for {
		guard, err := s.activeRuntimeGuard(ctx, trimmedSessionID)
		if errors.Is(err, ErrAcquiredRuntimeOvertaken) {
			continue
		}
		if err != nil || guard == nil {
			return err
		}
		defer guard.Release()
		engine := guard.Engine()
		if engine == nil {
			return runtimeUnavailableErr(trimmedSessionID)
		}
		return engine.SteerWorktreeTransitionFailure(outcome)
	}
}

func (s *Service) syncActiveExecutionTarget(ctx context.Context, sessionID string, workdir string, guard registry.RuntimeGuard, reminder *session.WorktreeReminderState) error {
	defer guard.Release()
	engine := guard.Engine()
	if engine == nil {
		return runtimeUnavailableErr(sessionID)
	}
	return engine.RunWhenIdleBeforeQueuedUserWork(ctx, runtime.ActiveKindRuntimeMaintenance, func() error {
		target := clientui.SessionExecutionTarget{EffectiveWorkdir: workdir}
		return s.syncGuardedExecutionTarget(ctx, sessionID, target, guard, reminder)
	})
}

func (s *Service) syncInactiveExecutionTarget(ctx context.Context, sessionID string, target clientui.SessionExecutionTarget, reminder *session.WorktreeReminderState) error {
	if strings.TrimSpace(target.EffectiveWorkdir) == "" {
		return errors.New("execution target effective workdir is required")
	}
	if err := s.persistWorktreeReminderState(ctx, sessionID, reminder); err != nil {
		return err
	}
	return nil
}

func (s *Service) syncGuardedExecutionTarget(
	_ context.Context,
	sessionID string,
	target clientui.SessionExecutionTarget,
	guard registry.RuntimeGuard,
	reminder *session.WorktreeReminderState,
) error {
	workdir := strings.TrimSpace(target.EffectiveWorkdir)
	if workdir == "" {
		return errors.New("execution target effective workdir is required")
	}
	var normalizedReminder *session.WorktreeReminderState
	if reminder != nil {
		normalized, err := session.NormalizeWorktreeReminderState(*reminder)
		if err != nil {
			return err
		}
		normalizedReminder = &normalized
	}
	engine := guard.Engine()
	if engine == nil {
		return runtimeUnavailableErr(sessionID)
	}
	previousWorkdir := engine.TranscriptWorkingDir()
	previousReminder := engine.WorktreeReminderState()
	if err := guard.Rebind(workdir); err != nil {
		return err
	}
	if err := engine.SetWorktreeReminderState(normalizedReminder); err != nil {
		rollbackErr := rollbackActiveExecutionTarget(engine, guard, previousWorkdir, previousReminder)
		if rollbackErr != nil {
			return errors.Join(err, rollbackErr, guard.Retire(runtime.QueuedUserMessageFailureRuntimeUnavailable))
		}
		return errors.Join(err, rollbackErr)
	}
	return nil
}

func (s *Service) ClearWorktreeReminder(ctx context.Context, sessionID string) error {
	store, err := s.resolveStore(ctx, strings.TrimSpace(sessionID))
	if err != nil {
		return err
	}
	return store.SetWorktreeReminderState(nil)
}

func (s *Service) persistWorktreeReminderState(ctx context.Context, sessionID string, reminder *session.WorktreeReminderState) error {
	if reminder == nil {
		return nil
	}
	store, err := s.resolveStore(ctx, strings.TrimSpace(sessionID))
	if err != nil {
		return err
	}
	return store.SetWorktreeReminderState(reminder)
}

func (s *Service) resolveStore(ctx context.Context, sessionID string) (*session.Store, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.sessionStores != nil {
		store, err := s.sessionStores.ResolveStore(ctx, sessionID)
		if err != nil {
			return nil, err
		}
		if store != nil {
			return store, nil
		}
	}
	store, err := session.OpenByID(s.persistenceRoot, sessionID, s.storeOptions...)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.sessionStores != nil {
		s.sessionStores.RegisterStore(store)
	}
	return store, nil
}

func rollbackActiveExecutionTarget(engine *runtime.Engine, guard registry.RuntimeGuard, workdir string, reminder *session.WorktreeReminderState) error {
	var collected []error
	if strings.TrimSpace(workdir) != "" {
		if err := guard.Rebind(workdir); err != nil {
			collected = append(collected, fmt.Errorf("rollback runtime workdir: %w", err))
		}
	}
	if err := engine.SetWorktreeReminderState(reminder); err != nil {
		collected = append(collected, fmt.Errorf("rollback worktree reminder: %w", err))
	}
	return errors.Join(collected...)
}

func (s *Service) activeRuntimeGuard(ctx context.Context, sessionID string) (registry.RuntimeGuard, error) {
	if s.runtimes == nil {
		return nil, nil
	}
	guard, err := s.runtimes.BeginRuntimeGuard(ctx, strings.TrimSpace(sessionID))
	if err != nil {
		if errors.Is(err, registry.ErrRuntimeGuardOvertaken) {
			return nil, ErrAcquiredRuntimeOvertaken
		}
		if errors.Is(err, serverapi.ErrRuntimeUnavailable) {
			return nil, nil
		}
		return nil, err
	}
	if guard == nil {
		return nil, nil
	}
	return guard, nil
}

func (s *Service) resolveExecutionTarget(ctx context.Context, sessionID string) (clientui.SessionExecutionTarget, error) {
	if s == nil || s.metadataStore == nil {
		return clientui.SessionExecutionTarget{}, fmt.Errorf("metadata store is required")
	}
	return s.metadataStore.ResolveSessionExecutionTarget(ctx, sessionID)
}
