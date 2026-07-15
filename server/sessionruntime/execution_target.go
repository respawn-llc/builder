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

func (s *Service) RunWorktreeTransitionAtStepBoundary(
	ctx context.Context,
	sessionID string,
	origin serverapi.RuntimeStepOrigin,
	fn func(context.Context, func(context.Context, clientui.SessionExecutionTarget, *session.WorktreeReminderState) error) error,
	complete func(error),
) error {
	if fn == nil {
		return nil
	}
	if err := origin.Validate(); err != nil {
		return err
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
			return runtimeUnavailableErr(trimmedSessionID)
		}
		engine := guard.Engine()
		if engine == nil {
			guard.Release()
			return runtimeUnavailableErr(trimmedSessionID)
		}
		effect := &worktreeTransitionBoundaryEffect{
			apply: func(stepCtx context.Context) error {
				defer guard.Release()
				return fn(stepCtx, func(syncCtx context.Context, target clientui.SessionExecutionTarget, reminder *session.WorktreeReminderState) error {
					if err := s.syncGuardedExecutionTarget(syncCtx, trimmedSessionID, target, guard, reminder); err != nil {
						return err
					}
					if s.runtimes != nil {
						s.runtimes.PublishSessionIdentity(trimmedSessionID, &target)
					}
					return nil
				})
			},
			complete: complete,
			cancel:   guard.Release,
		}
		if err := engine.QueueActiveStepEffect(origin.RunID, origin.StepID, effect); err != nil {
			guard.Release()
			return err
		}
		return nil
	}
}

type worktreeTransitionBoundaryEffect struct {
	apply    func(context.Context) error
	complete func(error)
	cancel   func()
}

func (effect *worktreeTransitionBoundaryEffect) Apply(ctx context.Context) error {
	if effect == nil || effect.apply == nil {
		return errors.New("worktree transition boundary effect is required")
	}
	err := effect.apply(ctx)
	if effect.complete != nil {
		effect.complete(err)
	}
	return err
}

func (effect *worktreeTransitionBoundaryEffect) Cancel(cause error) {
	if effect != nil && effect.cancel != nil {
		effect.cancel()
	}
	if effect != nil && effect.complete != nil {
		effect.complete(cause)
	}
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
	return s.runSessionMaintenance(ctx, sessionID, func(runCtx context.Context, _ *session.Store, guard registry.RuntimeGuard, _ *runtime.Engine) error {
		trimmedSessionID := strings.TrimSpace(sessionID)
		if guard == nil {
			return fn(runCtx, func(syncCtx context.Context, target clientui.SessionExecutionTarget, reminder *session.WorktreeReminderState) error {
				if err := s.syncInactiveExecutionTarget(syncCtx, trimmedSessionID, target, reminder); err != nil {
					return err
				}
				if s.runtimes != nil {
					s.runtimes.PublishSessionIdentity(trimmedSessionID, &target)
				}
				return nil
			})
		}
		return fn(runCtx, func(syncCtx context.Context, target clientui.SessionExecutionTarget, reminder *session.WorktreeReminderState) error {
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

func (s *Service) RunSessionMaintenance(
	ctx context.Context,
	sessionID string,
	fn func(context.Context, *session.Store, *ActiveRuntimeMaintenance) error,
) error {
	if fn == nil {
		return nil
	}
	return s.runSessionMaintenance(ctx, sessionID, func(runCtx context.Context, store *session.Store, guard registry.RuntimeGuard, engine *runtime.Engine) error {
		if guard == nil {
			return fn(runCtx, store, nil)
		}
		activeRuntime := &ActiveRuntimeMaintenance{
			PreviousWorkdir: strings.TrimSpace(engine.TranscriptWorkingDir()),
			Rebind:          guard.Rebind,
		}
		if err := activeRuntime.Validate(); err != nil {
			return err
		}
		return fn(runCtx, store, activeRuntime)
	})
}

type ActiveRuntimeMaintenance struct {
	PreviousWorkdir string
	Rebind          func(string) error
}

func (m *ActiveRuntimeMaintenance) Validate() error {
	if m == nil {
		return errors.New("active runtime maintenance is required")
	}
	if strings.TrimSpace(m.PreviousWorkdir) == "" {
		return errors.New("active runtime working directory is required")
	}
	if m.Rebind == nil {
		return errors.New("active runtime rebind operation is required")
	}
	return nil
}

func (s *Service) runSessionMaintenance(
	ctx context.Context,
	sessionID string,
	fn func(context.Context, *session.Store, registry.RuntimeGuard, *runtime.Engine) error,
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
			store, err := s.resolveStore(ctx, trimmedSessionID)
			if err != nil {
				return err
			}
			return fn(ctx, store, nil, nil)
		}
		defer guard.Release()
		engine := guard.Engine()
		if engine == nil {
			return runtimeUnavailableErr(trimmedSessionID)
		}
		return engine.RunWhenIdleBeforeQueuedUserWork(ctx, runtime.ActiveKindRuntimeMaintenance, func() error {
			store, err := s.resolveStore(ctx, trimmedSessionID)
			if err != nil {
				return err
			}
			return fn(ctx, store, guard, engine)
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
