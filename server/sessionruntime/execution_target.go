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
		normalized, err := normalizeWorktreeReminderState(*reminder)
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
		return s.syncActiveExecutionTarget(ctx, trimmedSessionID, trimmedWorkdir, guard, normalizedReminder)
	}
}

func (s *Service) syncActiveExecutionTarget(ctx context.Context, sessionID string, workdir string, guard registry.RuntimeGuard, reminder *session.WorktreeReminderState) error {
	defer guard.Release()
	engine := guard.Engine()
	if engine == nil {
		return runtimeUnavailableErr(sessionID)
	}
	return engine.RunWhenIdleBeforeQueuedUserWork(ctx, runtime.ActiveKindRuntimeMaintenance, func() error {
		previousWorkdir := engine.TranscriptWorkingDir()
		previousReminder := engine.WorktreeReminderState()
		if err := guard.Rebind(workdir); err != nil {
			return err
		}
		if err := engine.SetWorktreeReminderState(reminder); err != nil {
			rollbackErr := rollbackActiveExecutionTarget(engine, guard, previousWorkdir, previousReminder)
			if rollbackErr != nil {
				return errors.Join(err, rollbackErr, guard.Retire(runtime.QueuedUserMessageFailureRuntimeUnavailable))
			}
			return errors.Join(err, rollbackErr)
		}
		return nil
	})
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

func normalizeWorktreeReminderState(state session.WorktreeReminderState) (session.WorktreeReminderState, error) {
	state.Mode = session.WorktreeReminderMode(strings.TrimSpace(string(state.Mode)))
	switch state.Mode {
	case session.WorktreeReminderModeEnter, session.WorktreeReminderModeExit:
	default:
		return session.WorktreeReminderState{}, errors.New("worktree reminder mode is required")
	}
	state.Branch = strings.TrimSpace(state.Branch)
	state.WorktreePath = strings.TrimSpace(state.WorktreePath)
	state.WorkspaceRoot = strings.TrimSpace(state.WorkspaceRoot)
	state.EffectiveCwd = strings.TrimSpace(state.EffectiveCwd)
	if state.WorkspaceRoot == "" {
		return session.WorktreeReminderState{}, errors.New("worktree reminder workspace root is required")
	}
	if state.EffectiveCwd == "" {
		return session.WorktreeReminderState{}, errors.New("worktree reminder effective cwd is required")
	}
	if state.Mode == session.WorktreeReminderModeEnter && state.WorktreePath == "" {
		return session.WorktreeReminderState{}, errors.New("worktree reminder worktree path is required for enter mode")
	}
	state.HasIssuedInGeneration = false
	state.IssuedCompactionCount = 0
	return state, nil
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
