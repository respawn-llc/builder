package sessionruntime

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"core/server/runtime"
	"core/server/runtimeactivity"
	"core/server/session"
	shelltool "core/server/tools/shell"
	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

type ActiveRuntimeMaintenance struct {
	PreviousWorkdir string
	Rebind          func(string) error
}

func (a *Authority) SyncExecutionTarget(ctx context.Context, sessionID string, target clientui.SessionExecutionTarget, reminder *session.WorktreeReminderState) error {
	id, err := runtimeids.ParseSessionID(strings.TrimSpace(sessionID))
	if err != nil {
		return err
	}
	workdir, normalizedReminder, err := normalizeTarget(target, reminder)
	if err != nil {
		return err
	}
	return a.withMaintenanceResource(ctx, id, func(runCtx context.Context, store *session.Store, resource *agentResource, engine *runtime.Engine) (bool, error) {
		if resource == nil {
			if normalizedReminder == nil {
				return false, nil
			}
			return false, store.SetWorktreeReminderState(normalizedReminder)
		}
		retire := false
		err := engine.RunWhenIdleBeforeQueuedUserWork(runCtx, runtime.ActiveKindRuntimeMaintenance, func() error {
			var syncErr error
			retire, syncErr = syncResourceExecutionTarget(resource, engine, workdir, normalizedReminder)
			return syncErr
		})
		return retire, err
	})
}

func (a *Authority) RunWorktreeTransition(
	ctx context.Context,
	sessionID string,
	origin *serverapi.RuntimeStepOrigin,
	fn func(context.Context, func(func() error) error, func(context.Context, clientui.SessionExecutionTarget, *session.WorktreeReminderState) error) error,
) error {
	if fn == nil {
		return nil
	}
	id, err := runtimeids.ParseSessionID(strings.TrimSpace(sessionID))
	if err != nil {
		return err
	}
	return a.withMaintenanceResource(ctx, id, func(runCtx context.Context, store *session.Store, resource *agentResource, engine *runtime.Engine) (bool, error) {
		if resource == nil {
			if origin != nil {
				return false, serverapi.NewWorktreeImmediateTransitionError(
					serverapi.WorktreeImmediateTransitionOriginInactive,
					runtimeUnavailableErr(id.String()),
				)
			}
			return false, fn(runCtx, func(apply func() error) error { return apply() }, func(syncCtx context.Context, target clientui.SessionExecutionTarget, reminder *session.WorktreeReminderState) error {
				if err := context.Cause(syncCtx); err != nil {
					return err
				}
				_, normalizedReminder, err := normalizeTarget(target, reminder)
				if err != nil || normalizedReminder == nil {
					return err
				}
				return store.SetWorktreeReminderState(normalizedReminder)
			})
		}
		if origin == nil {
			var retire bool
			err := engine.RunWhenIdleBeforeQueuedUserWork(runCtx, runtime.ActiveKindRuntimeMaintenance, func() error {
				active := true
				defer func() { active = false }()
				return fn(runCtx, func(apply func() error) error { return apply() }, func(_ context.Context, target clientui.SessionExecutionTarget, reminder *session.WorktreeReminderState) error {
					if !active {
						return errors.New("worktree transition target synchronizer is no longer active")
					}
					workdir, normalizedReminder, err := normalizeTarget(target, reminder)
					if err != nil {
						return err
					}
					var syncErr error
					retire, syncErr = syncResourceExecutionTarget(resource, engine, workdir, normalizedReminder)
					return syncErr
				})
			})
			return retire, err
		}
		activeStep := runtimeactivity.ActiveStepFromProvider(engine)
		if activeStep == nil || activeStep.RunID != origin.RunID || activeStep.StepID != origin.StepID {
			return false, serverapi.NewWorktreeImmediateTransitionError(
				serverapi.WorktreeImmediateTransitionOriginInactive,
				runtime.ErrActiveStepInactive,
			)
		}
		active := true
		defer func() { active = false }()
		authority := func(apply func() error) error {
			if !active {
				return runtime.ErrActiveStepInactive
			}
			return engine.ApplyForActiveStep(origin.StepID, apply)
		}
		retire := false
		err := fn(runCtx, authority, func(_ context.Context, target clientui.SessionExecutionTarget, reminder *session.WorktreeReminderState) error {
			if !active {
				return runtime.ErrActiveStepInactive
			}
			workdir, normalizedReminder, err := normalizeTarget(target, reminder)
			if err != nil {
				return err
			}
			var syncErr error
			retire, syncErr = syncResourceExecutionTarget(resource, engine, workdir, normalizedReminder)
			return syncErr
		})
		if err != nil {
			kind := serverapi.WorktreeImmediateTransitionApplyFailed
			if errors.Is(err, runtime.ErrActiveStepInactive) {
				kind = serverapi.WorktreeImmediateTransitionOriginInactive
			}
			return retire, serverapi.NewWorktreeImmediateTransitionError(kind, err)
		}
		return retire, nil
	})
}

func (a *Authority) RunSessionMaintenance(
	ctx context.Context,
	sessionID string,
	fn func(context.Context, *session.Store, *ActiveRuntimeMaintenance) error,
) error {
	if fn == nil {
		return nil
	}
	id, err := runtimeids.ParseSessionID(strings.TrimSpace(sessionID))
	if err != nil {
		return err
	}
	return a.withMaintenanceResource(ctx, id, func(runCtx context.Context, store *session.Store, resource *agentResource, engine *runtime.Engine) (bool, error) {
		if resource == nil {
			return false, fn(runCtx, store, nil)
		}
		var retire bool
		err := engine.RunWhenIdleBeforeQueuedUserWork(runCtx, runtime.ActiveKindRuntimeMaintenance, func() error {
			previousWorkdir := strings.TrimSpace(engine.TranscriptWorkingDir())
			currentWorkdir := previousWorkdir
			active := true
			maintenance := &ActiveRuntimeMaintenance{
				PreviousWorkdir: previousWorkdir,
				Rebind: func(workdir string) error {
					if !active {
						return errors.New("active runtime maintenance rebind is no longer active")
					}
					normalized := strings.TrimSpace(workdir)
					if normalized == "" {
						return errors.New("runtime workdir is required")
					}
					if err := rebindResource(resource, engine, normalized); err != nil {
						return err
					}
					currentWorkdir = normalized
					return nil
				},
			}
			callbackErr := fn(runCtx, store, maintenance)
			active = false
			if callbackErr == nil || currentWorkdir == previousWorkdir {
				return callbackErr
			}
			rollbackErr := rebindResource(resource, engine, previousWorkdir)
			if rollbackErr != nil {
				retire = true
				engine.FailQueuedUserMessages(runtime.QueuedUserMessageFailureRuntimeUnavailable)
				rollbackErr = fmt.Errorf("rollback runtime workdir: %w", rollbackErr)
			}
			return errors.Join(callbackErr, rollbackErr)
		})
		return retire, err
	})
}

func (a *Authority) ClearWorktreeReminder(ctx context.Context, sessionID string) error {
	id, err := runtimeids.ParseSessionID(strings.TrimSpace(sessionID))
	if err != nil {
		return err
	}
	return a.withMaintenanceResource(ctx, id, func(_ context.Context, store *session.Store, _ *agentResource, _ *runtime.Engine) (bool, error) {
		return false, store.SetWorktreeReminderState(nil)
	})
}

func (a *Authority) SteerWorktreeTransitionFailure(ctx context.Context, sessionID string, outcome clientui.WorktreeTransitionOutcome) error {
	id, err := runtimeids.ParseSessionID(strings.TrimSpace(sessionID))
	if err != nil {
		return err
	}
	return a.WithCurrentRuntime(ctx, id, func(_ context.Context, engine *runtime.Engine) error {
		return engine.SteerWorktreeTransitionFailure(outcome)
	})
}

func (a *Authority) HasBlockingRuntimeActivity(ctx context.Context, sessionID string) (bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := context.Cause(ctx); err != nil {
		return false, err
	}
	id, err := runtimeids.ParseSessionID(strings.TrimSpace(sessionID))
	if err != nil {
		return false, err
	}
	_, active := a.SessionExecution(id)
	return active, nil
}

func (a *Authority) routeBackgroundEvent(event shelltool.Event) {
	correlation := event.Snapshot.ExecutionCorrelation
	if correlation == nil {
		return
	}
	if err := correlation.Validate(); err != nil {
		panic(fmt.Sprintf("route background event with invalid execution correlation: process_id=%q error=%v", event.Snapshot.ID, err))
	}
	if event.Snapshot.ActivityID.Version() != 4 {
		panic(fmt.Sprintf("route background event with invalid activity id: process_id=%q activity_id=%q", event.Snapshot.ID, event.Snapshot.ActivityID))
	}
	sessionID, err := runtimeids.ParseSessionID(strings.TrimSpace(event.Snapshot.OwnerSessionID))
	if err != nil {
		panic(fmt.Sprintf("route background event with invalid owner session: process_id=%q session_id=%q error=%v", event.Snapshot.ID, event.Snapshot.OwnerSessionID, err))
	}
	a.mu.Lock()
	resource := a.resources[sessionID]
	a.mu.Unlock()
	if resource == nil || resource.ref.Generation() != correlation.ResourceGeneration() {
		return
	}
	routeErr := resource.withEngine(context.Background(), resource.ref, func(_ context.Context, engine *runtime.Engine) error {
		summary := shelltool.BackgroundNoticeSummary{}
		if event.Type == shelltool.EventCompleted || event.Type == shelltool.EventKilled {
			var summaryErr error
			summary, summaryErr = shelltool.SummarizeBackgroundEvent(event, shelltool.BackgroundNoticeOptions{
				MaxChars:          resource.backgroundLimit,
				SuccessOutputMode: resource.backgroundMode,
			})
			if summaryErr != nil {
				if resource.logger != nil {
					resource.logger.Logf("runtime.background.summary.failed process_id=%s error=%q", event.Snapshot.ID, summaryErr.Error())
				}
				summary = shelltool.InvariantFailureBackgroundNotice(event, summaryErr)
			}
		}
		eventType := runtime.BackgroundShellEventBackgrounded
		switch event.Type {
		case shelltool.EventCompleted:
			eventType = runtime.BackgroundShellEventCompleted
		case shelltool.EventKilled:
			eventType = runtime.BackgroundShellEventKilled
		}
		preview, previewRemoved := summary.RuntimePreview()
		engine.HandleBackgroundShellUpdate(runtime.BackgroundShellEvent{
			Type:              eventType,
			ID:                event.Snapshot.ID,
			ActivityID:        event.Snapshot.ActivityID,
			OwnerRunID:        event.Snapshot.OwnerRunID,
			OwnerStepID:       event.Snapshot.OwnerStepID,
			State:             event.Snapshot.State,
			Command:           event.Snapshot.Command,
			Workdir:           event.Snapshot.Workdir,
			LogPath:           event.Snapshot.LogPath,
			NoticeText:        summary.DetailText,
			CompactText:       summary.CondensedText,
			Preview:           preview,
			PreviewRemoved:    previewRemoved,
			ExitCode:          event.Snapshot.ExitCode,
			UserRequestedKill: event.Snapshot.KillRequested,
			NoticeSuppressed:  event.NoticeSuppressed,
		}, !event.NoticeSuppressed)
		return nil
	})
	if routeErr != nil && !errors.Is(routeErr, serverapi.ErrRuntimeUnavailable) && resource.logger != nil {
		resource.logger.Logf("runtime.background.route.failed process_id=%s error=%q", event.Snapshot.ID, routeErr.Error())
	}
}

func ParseSessionIDs(raw []string) ([]runtimeids.SessionID, error) {
	ids := make([]runtimeids.SessionID, 0, len(raw))
	seen := make(map[runtimeids.SessionID]struct{}, len(raw))
	for _, value := range raw {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		id, err := runtimeids.ParseSessionID(trimmed)
		if err != nil {
			return nil, err
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return ids, nil
}

type maintenanceCallback func(context.Context, *session.Store, *agentResource, *runtime.Engine) (retire bool, err error)

func (a *Authority) withMaintenanceResource(ctx context.Context, sessionID runtimeids.SessionID, callback maintenanceCallback) error {
	if a == nil {
		return errors.New("session runtime authority is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := context.Cause(ctx); err != nil {
		return err
	}
	gate := a.gateFor(sessionID)
	gate.mu.Lock()
	defer gate.mu.Unlock()
	a.mu.Lock()
	resource := a.resources[sessionID]
	a.mu.Unlock()
	if resource == nil {
		descriptor, err := session.NewOpenSessionDescriptor(sessionID)
		if err != nil {
			return err
		}
		store, err := session.MaterializeSessionDescriptor(a.options.persistenceRoot, descriptor, a.options.storeOptions...)
		if err == nil {
			_, err = callback(ctx, store, nil, nil)
		}
		return err
	}
	retire := false
	err := resource.withEngine(ctx, resource.ref, func(runCtx context.Context, engine *runtime.Engine) (err error) {
		retire, err = callback(runCtx, resource.store, resource, engine)
		return
	})
	if retire {
		err = errors.Join(err, a.retireExactResource(ctx, resource))
	}
	return err
}

func (a *Authority) retireExactResource(ctx context.Context, resource *agentResource) error {
	sessionID := resource.ref.SessionID()
	a.mu.Lock()
	if a.resources[sessionID] != resource {
		a.mu.Unlock()
		return nil
	}
	delete(a.resources, sessionID)
	a.mu.Unlock()

	resource.engine.FailQueuedUserMessages(runtime.QueuedUserMessageFailureRuntimeUnavailable)
	return resource.closeResource(ctx)
}

func normalizeTarget(target clientui.SessionExecutionTarget, reminder *session.WorktreeReminderState) (string, *session.WorktreeReminderState, error) {
	workdir := strings.TrimSpace(target.EffectiveWorkdir)
	if workdir == "" {
		return "", nil, errors.New("execution target effective workdir is required")
	}
	if reminder == nil {
		return workdir, nil, nil
	}
	normalized, err := session.NormalizeWorktreeReminderState(*reminder)
	if err != nil {
		return "", nil, err
	}
	return workdir, &normalized, nil
}

func syncResourceExecutionTarget(resource *agentResource, engine *runtime.Engine, workdir string, reminder *session.WorktreeReminderState) (bool, error) {
	previousWorkdir := strings.TrimSpace(engine.TranscriptWorkingDir())
	previousReminder := engine.WorktreeReminderState()
	if err := rebindResource(resource, engine, workdir); err != nil {
		return false, err
	}
	if err := engine.SetWorktreeReminderState(reminder); err != nil {
		rollbackErr := rollbackResourceExecutionTarget(resource, engine, previousWorkdir, previousReminder)
		if rollbackErr != nil {
			engine.FailQueuedUserMessages(runtime.QueuedUserMessageFailureRuntimeUnavailable)
			return true, errors.Join(err, rollbackErr)
		}
		return false, err
	}
	return false, nil
}

func rebindResource(resource *agentResource, engine *runtime.Engine, workdir string) error {
	if resource == nil || engine == nil {
		return errors.New("active runtime resource is required")
	}
	if resource.localTools != nil {
		if err := resource.localTools.Rebind(workdir); err != nil {
			return err
		}
	}
	engine.SetTranscriptWorkingDir(workdir)
	return nil
}

func rollbackResourceExecutionTarget(resource *agentResource, engine *runtime.Engine, workdir string, reminder *session.WorktreeReminderState) error {
	var collected []error
	if strings.TrimSpace(workdir) != "" {
		if err := rebindResource(resource, engine, workdir); err != nil {
			collected = append(collected, fmt.Errorf("rollback runtime workdir: %w", err))
		}
	}
	if err := engine.SetWorktreeReminderState(reminder); err != nil {
		collected = append(collected, fmt.Errorf("rollback worktree reminder: %w", err))
	}
	return errors.Join(collected...)
}
