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
	claim, err := s.activeRuntimeClaim(ctx, trimmedSessionID)
	if err != nil {
		return err
	}
	if claim != nil {
		if err := s.WithRuntimeEngine(ctx, trimmedSessionID, func(engine *runtime.Engine) error {
			return engine.RunWhenIdle(ctx, func() error {
				current := s.runtimes.RuntimeClaimFor(trimmedSessionID)
				if current == nil || !current.IsCurrent() {
					return errors.Join(ErrAcquiredRuntimeOvertaken, fmt.Errorf("session %q runtime was replaced during execution target sync", trimmedSessionID))
				}
				return current.Rebind(trimmedWorkdir)
			})
		}); err != nil {
			return err
		}
	}
	return s.persistWorktreeReminderState(ctx, trimmedSessionID, normalizedReminder)
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

func (s *Service) activeRuntimeClaim(ctx context.Context, sessionID string) (*registry.RuntimeClaim, error) {
	if s.runtimes == nil {
		return nil, nil
	}
	claim := s.runtimes.RuntimeClaimFor(strings.TrimSpace(sessionID))
	if claim == nil {
		return nil, nil
	}
	if _, err := claim.AwaitReady(ctx); err != nil {
		return nil, err
	}
	if !claim.IsCurrent() {
		return nil, nil
	}
	if err := claim.ActivationErr(); err != nil {
		return nil, err
	}
	return claim, nil
}

func (s *Service) resolveExecutionTarget(ctx context.Context, sessionID string) (clientui.SessionExecutionTarget, error) {
	if s == nil || s.metadataStore == nil {
		return clientui.SessionExecutionTarget{}, fmt.Errorf("metadata store is required")
	}
	return s.metadataStore.ResolveSessionExecutionTarget(ctx, sessionID)
}
