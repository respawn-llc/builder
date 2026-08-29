package sessionruntime

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"core/server/runtime"
	"core/server/runtimeactivity"
	"core/server/runtimewire"
	"core/server/session"
	"core/server/tools"
	shelltool "core/server/tools/shell"
	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/worktreecontract"
)

type ActiveRuntimeMaintenance struct {
	PreviousFilesystemContext tools.FilesystemContext
	Replace                   func(tools.FilesystemContext) error
	steerSessionRebindFailure func(session.SessionRebindReminder) (session.CommitReceipt, error)
	retire                    bool
}

func (m *ActiveRuntimeMaintenance) SteerSessionRebindFailure(reminder session.SessionRebindReminder) (session.CommitReceipt, error) {
	if m == nil || m.steerSessionRebindFailure == nil {
		return session.CommitReceipt{}, errors.New("active runtime Session rebind failure steering is unavailable")
	}
	return m.steerSessionRebindFailure(reminder)
}

func (m *ActiveRuntimeMaintenance) RetireRuntime() {
	if m != nil {
		m.retire = true
	}
}

func (m *ActiveRuntimeMaintenance) RetirementScheduled() bool {
	return m != nil && m.retire
}

func (a *Authority) SyncExecutionTarget(ctx context.Context, sessionID string, target clientui.SessionExecutionTarget, reminder *session.WorktreeReminderState) error {
	id, err := runtimeids.ParseSessionID(strings.TrimSpace(sessionID))
	if err != nil {
		return err
	}
	normalizedTarget, normalizedReminder, err := normalizeTarget(target, reminder)
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
			retire, syncErr = syncResourceExecutionTarget(resource, engine, normalizedTarget, normalizedReminder)
			return syncErr
		})
		return retire, err
	})
}

func (a *Authority) RunWorktreeTransition(
	ctx context.Context,
	sessionID string,
	transition clientui.WorktreeTransitionKind,
	fn func(
		context.Context,
		func(func() error) error,
		func(context.Context, clientui.SessionExecutionTarget, *session.WorktreeReminderState) error,
		func(clientui.WorktreeTransitionOutcome) error,
	) error,
) error {
	if fn == nil {
		return nil
	}
	switch transition {
	case clientui.WorktreeTransitionEnter, clientui.WorktreeTransitionLeave:
	default:
		return errors.New("worktree transition kind is invalid")
	}
	id, err := runtimeids.ParseSessionID(strings.TrimSpace(sessionID))
	if err != nil {
		return err
	}
	return a.withMaintenanceResource(ctx, id, func(runCtx context.Context, store *session.Store, resource *agentResource, engine *runtime.Engine) (bool, error) {
		if resource == nil {
			return false, fn(
				runCtx,
				func(apply func() error) error { return apply() },
				func(syncCtx context.Context, target clientui.SessionExecutionTarget, reminder *session.WorktreeReminderState) error {
					if err := context.Cause(syncCtx); err != nil {
						return err
					}
					_, normalizedReminder, err := normalizeTarget(target, reminder)
					if err != nil || normalizedReminder == nil {
						return err
					}
					return store.SetWorktreeReminderState(normalizedReminder)
				},
				func(clientui.WorktreeTransitionOutcome) error { return nil },
			)
		}
		retire := false
		err := engine.RunWorktreeTransition(runCtx, func() error {
			active := true
			defer func() { active = false }()
			return fn(
				runCtx,
				func(apply func() error) error { return apply() },
				func(syncCtx context.Context, target clientui.SessionExecutionTarget, reminder *session.WorktreeReminderState) error {
					return engine.ApplyWorktreeTransitionTerminal(syncCtx, func(operationCtx context.Context) error {
						if err := context.Cause(operationCtx); err != nil {
							return err
						}
						if !active {
							return errors.New("worktree transition target synchronizer is no longer active")
						}
						normalizedTarget, normalizedReminder, err := normalizeTarget(target, reminder)
						if err != nil {
							return err
						}
						var syncErr error
						retire, syncErr = syncResourceExecutionTarget(resource, engine, normalizedTarget, normalizedReminder)
						return syncErr
					})
				},
				func(outcome clientui.WorktreeTransitionOutcome) error {
					if !active {
						return errors.New("worktree transition failure synchronizer is no longer active")
					}
					return engine.SteerWorktreeTransitionFailure(outcome)
				},
			)
		})
		if errors.Is(err, runtime.ErrReviewerActive) ||
			errors.Is(err, runtime.ErrWorktreeDeleteBlockedByQueuedWork) {
			err = errors.Join(worktreecontract.ErrWorktreeBlocked, err)
		}
		return retire, err
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
			var runErr error
			retire, runErr = runActiveRuntimeMaintenance(resource, engine, func(maintenance *ActiveRuntimeMaintenance) error {
				return fn(runCtx, store, maintenance)
			})
			return runErr
		})
		return retire, err
	})
}

func (a *Authority) RunSessionMaintenanceIfIdle(
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
		if hasBlockingRuntimeActivity(resource) {
			return false, ErrRuntimeActivityBusy
		}
		var retire bool
		started, runErr := engine.RunIfIdleBeforeQueuedUserWork(runCtx, runtime.ActiveKindRuntimeMaintenance, func() error {
			var maintenanceErr error
			retire, maintenanceErr = runActiveRuntimeMaintenance(resource, engine, func(maintenance *ActiveRuntimeMaintenance) error {
				return fn(runCtx, store, maintenance)
			})
			return maintenanceErr
		})
		if !started && errors.Is(runErr, runtime.ErrAgentBusy) {
			return false, ErrRuntimeActivityBusy
		}
		return retire, runErr
	})
}

func (a *Authority) RunSessionMaintenanceAtStepBoundary(
	ctx context.Context,
	sessionID string,
	origin serverapi.RuntimeStepOrigin,
	onScheduled func(),
	fn func(context.Context, *session.Store, *ActiveRuntimeMaintenance) error,
) error {
	if fn == nil {
		return nil
	}
	id, err := runtimeids.ParseSessionID(strings.TrimSpace(sessionID))
	if err != nil {
		return err
	}
	return a.withExactStepBoundaryMaintenanceResource(ctx, id, func(runCtx context.Context, store *session.Store, resource *agentResource, engine *runtime.Engine) (bool, error) {
		if resource == nil {
			return false, runtime.ErrActiveStepInactive
		}
		activeStep := runtimeactivity.ActiveStepFromProvider(engine)
		if activeStep == nil || activeStep.RunID != origin.RunID || activeStep.StepID != origin.StepID {
			return false, runtime.ErrActiveStepInactive
		}
		var retire bool
		err := engine.RunExecutionTargetTransition(runCtx, onScheduled, func() error {
			var runErr error
			retire, runErr = runActiveRuntimeMaintenance(resource, engine, func(maintenance *ActiveRuntimeMaintenance) error {
				return fn(runCtx, store, maintenance)
			})
			return runErr
		})
		return retire, err
	})
}

func runActiveRuntimeMaintenance(
	resource *agentResource,
	engine *runtime.Engine,
	fn func(*ActiveRuntimeMaintenance) error,
) (bool, error) {
	previousContext := tools.FilesystemContext{}
	if resource.localTools != nil {
		previousContext = resource.localTools.FilesystemContext()
	}
	currentContext := previousContext.Clone()
	active := true
	maintenance := &ActiveRuntimeMaintenance{
		PreviousFilesystemContext: previousContext,
		Replace: func(next tools.FilesystemContext) error {
			if !active {
				return errors.New("active runtime maintenance rebind is no longer active")
			}
			if err := rebindResourceContext(resource, engine, next); err != nil {
				return err
			}
			currentContext = next.Clone()
			return nil
		},
		steerSessionRebindFailure: engine.SteerSessionRebindFailure,
	}
	callbackErr := fn(maintenance)
	active = false
	retire := maintenance.retire
	if callbackErr == nil || currentContext.Equal(previousContext) {
		return retire, callbackErr
	}
	rollbackErr := rebindResourceContext(resource, engine, previousContext)
	if rollbackErr != nil {
		retire = true
		engine.FailQueuedUserMessages(runtime.QueuedUserMessageFailureRuntimeUnavailable)
		rollbackErr = fmt.Errorf("rollback runtime filesystem context: %w", rollbackErr)
	}
	return retire, errors.Join(callbackErr, rollbackErr)
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
	if a == nil {
		return false, nil
	}
	a.mu.Lock()
	resource := a.resources[id]
	a.mu.Unlock()
	if resource == nil {
		return false, nil
	}
	return hasBlockingRuntimeActivity(resource), nil
}

func hasBlockingRuntimeActivity(resource *agentResource) bool {
	resource.mu.Lock()
	state := resource.state
	current := resource.current
	steps := resource.steps
	engine := resource.engine
	resource.mu.Unlock()
	active := state != AgentResourceReady || current != nil
	if engine == nil {
		return active || steps != 0
	}
	snapshot := engine.ActiveRun()
	maintenanceStep := steps != 0 && snapshot != nil && snapshot.ActiveKind == runtime.ActiveKindRuntimeMaintenance
	active = active || (steps != 0 && !maintenanceStep)
	if !active {
		liveRun := engine.HasActiveLiveRunGroup()
		if snapshot != nil && snapshot.ActiveKind == runtime.ActiveKindRuntimeMaintenance {
			liveRun = false
		}
		active = liveRun ||
			engine.HasPendingRuntimeOperations() ||
			engine.HasQueuedUserWork() ||
			engine.HasScheduledQueuedUserWork() ||
			engine.CurrentNodeExecutionConfigured() ||
			engine.ReviewerActive()
	}
	return active
}

func (a *Authority) RetireIdleRuntime(ctx context.Context, sessionID string) (bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	id, err := runtimeids.ParseSessionID(strings.TrimSpace(sessionID))
	if err != nil {
		return false, err
	}
	gate := a.gateFor(id)
	if err := gate.lock.LockContext(ctx); err != nil {
		return false, err
	}
	defer gate.lock.Unlock()
	a.mu.Lock()
	resource := a.resources[id]
	a.mu.Unlock()
	if resource == nil {
		return true, nil
	}
	resource.mu.Lock()
	if resource.state != AgentResourceReady ||
		resource.current != nil ||
		resource.pins != 0 ||
		resource.callbacks != 0 ||
		resource.steps != 0 ||
		resource.engine == nil ||
		!resource.engine.BeginRetirement() {
		resource.mu.Unlock()
		return false, nil
	}
	closed, err := a.closeAdmittedResourceLocked(ctx, resource)
	return closed, err
}

func (a *Authority) routeBackgroundEvent(event shelltool.Event) bool {
	correlation := event.Snapshot.ExecutionCorrelation
	if correlation == nil {
		return false
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
	if event.Type == shelltool.EventCompleted || event.Type == shelltool.EventKilled {
		return a.routeTerminalBackgroundEvent(sessionID, event)
	}
	a.mu.Lock()
	resource := a.resources[sessionID]
	a.mu.Unlock()
	if resource == nil || resource.ref.Generation() != correlation.ResourceGeneration() {
		return false
	}
	delivered, routeErr := a.deliverBackgroundEvent(resource, event)
	if routeErr != nil && !errors.Is(routeErr, serverapi.ErrRuntimeUnavailable) && resource.logger != nil {
		resource.logger.Logf("runtime.background.route.failed process_id=%s error=%q", event.Snapshot.ID, routeErr.Error())
	}
	return delivered
}

func (a *Authority) routeTerminalBackgroundEvent(sessionID runtimeids.SessionID, event shelltool.Event) bool {
	for {
		a.mu.Lock()
		if a.closed {
			a.mu.Unlock()
			return false
		}
		resource := a.resources[sessionID]
		if resource == nil {
			a.mu.Unlock()
			return false
		}
		a.mu.Unlock()

		delivered, routeErr := a.deliverBackgroundEvent(resource, event)
		if delivered {
			if routeErr != nil && resource.logger != nil {
				resource.logger.Logf("runtime.background.route.failed process_id=%s error=%q", event.Snapshot.ID, routeErr.Error())
			}
			return true
		}
		if !errors.Is(routeErr, serverapi.ErrRuntimeUnavailable) {
			if resource.logger != nil {
				resource.logger.Logf("runtime.background.route.failed process_id=%s error=%q", event.Snapshot.ID, routeErr.Error())
			}
			return false
		}

		a.mu.Lock()
		if a.closed {
			a.mu.Unlock()
			return false
		}
		if a.resources[sessionID] != resource {
			a.mu.Unlock()
			continue
		}
		a.mu.Unlock()
		return false
	}
}

func (a *Authority) deliverBackgroundEvent(resource *agentResource, event shelltool.Event) (bool, error) {
	backgroundEvent := runtimeBackgroundShellEvent(resource, event)
	terminal := event.Type == shelltool.EventCompleted || event.Type == shelltool.EventKilled
	queueNotice := terminal && !event.NoticeSuppressed
	resource.mu.Lock()
	current := resource.current
	resource.mu.Unlock()
	if queueNotice && current == nil {
		delivered := false
		err := resource.withEngine(context.Background(), resource.ref, func(_ context.Context, engine *runtime.Engine) error {
			if recordErr := engine.RecordBackgroundShellUpdate(backgroundEvent); recordErr != nil {
				return recordErr
			}
			delivered = true
			return nil
		})
		if err != nil || !delivered {
			return delivered, err
		}
		if resourceSessionHasWorkflowContract(resource) {
			return true, nil
		} else {
			a.startBackgroundContinuation(resource, backgroundEvent)
			return true, nil
		}
	}
	delivered := false
	err := resource.withEngine(context.Background(), resource.ref, func(_ context.Context, engine *runtime.Engine) error {
		engine.HandleBackgroundShellUpdate(backgroundEvent, queueNotice)
		delivered = true
		return nil
	})
	return delivered, err
}

func (a *Authority) startBackgroundContinuation(resource *agentResource, event runtime.BackgroundShellEvent) {
	// Retried terminal events can arrive while OpenRuntime still holds the
	// Session admission gate. Defer only admission; model work starts inside the
	// Agent Execution Scope created below.
	a.launchLifecycleTask(func(ctx context.Context) {
		descriptor, err := session.NewOpenSessionDescriptor(resource.ref.SessionID())
		if err == nil {
			_, err = a.StartAgentExecution(ctx, AgentExecutionRequest{
				Descriptor: descriptor,
				Resource:   CurrentAgentResource{},
				Runner: func(ctx context.Context, _ ExecutionScope, bridge AgentRuntimeBridge) error {
					return bridge.WithEngine(ctx, func(engineCtx context.Context, engine *runtime.Engine) error {
						return engine.RunBackgroundShellContinuation(engineCtx, event)
					})
				},
			})
		}
		if err == nil {
			return
		}
		if errors.Is(err, ErrSessionRunActive) {
			err = a.WithCurrentRuntime(ctx, resource.ref.SessionID(), func(_ context.Context, engine *runtime.Engine) error {
				engine.QueueBackgroundShellContinuation(event)
				return nil
			})
			if err == nil {
				return
			}
		}
		if backgroundContinuationLifecycleStopped(err) {
			if resource.logger != nil {
				resource.logger.Logf("runtime.background.continuation.start.skipped process_id=%s error=%q", event.ID, err.Error())
			}
			return
		}
		fallbackErr := a.WithCurrentRuntime(ctx, resource.ref.SessionID(), func(_ context.Context, engine *runtime.Engine) error {
			return engine.SteerBackgroundContinuationFailure(err)
		})
		err = errors.Join(err, fallbackErr)
		if resource.logger != nil {
			resource.logger.Logf("runtime.background.continuation.start.failed process_id=%s error=%q", event.ID, err.Error())
		}
	})
}

func backgroundContinuationLifecycleStopped(err error) bool {
	return errors.Is(err, context.Canceled) ||
		errors.Is(err, ErrAuthorityClosed) ||
		errors.Is(err, serverapi.ErrRuntimeUnavailable)
}

func resourceSessionHasWorkflowContract(resource *agentResource) bool {
	if resource == nil || resource.store == nil {
		return false
	}
	locked := resource.store.Meta().Locked
	return locked != nil && locked.WorkflowCompletionMode != nil
}

func runtimeBackgroundShellEvent(resource *agentResource, event shelltool.Event) runtime.BackgroundShellEvent {
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
	return runtime.BackgroundShellEvent{
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

type maintenanceAdmission uint8

const (
	maintenanceAdmissionAuthorized maintenanceAdmission = iota + 1
	maintenanceAdmissionExactStepBoundary
)

func (a *Authority) withMaintenanceResource(ctx context.Context, sessionID runtimeids.SessionID, callback maintenanceCallback) error {
	return a.withMaintenanceResourceAdmission(ctx, sessionID, maintenanceAdmissionAuthorized, false, callback)
}

func (a *Authority) withExactStepBoundaryMaintenanceResource(ctx context.Context, sessionID runtimeids.SessionID, callback maintenanceCallback) error {
	return a.withMaintenanceResourceAdmission(ctx, sessionID, maintenanceAdmissionExactStepBoundary, false, callback)
}

func (a *Authority) withMaintenanceResourceAdmission(
	ctx context.Context,
	sessionID runtimeids.SessionID,
	admission maintenanceAdmission,
	serializeCallback bool,
	callback maintenanceCallback,
) error {
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
	gate.lock.Lock()
	if block := gate.unauthorizedMaintenanceBlock(ctx); block != nil &&
		(admission != maintenanceAdmissionExactStepBoundary || block.reason != SessionStartBlockMaintenance) {
		gate.lock.Unlock()
		return errors.Join(
			ErrSessionStartsBlocked,
			fmt.Errorf("session %s maintenance is blocked by session start block %d", sessionID, block.reason),
		)
	}
	a.mu.Lock()
	resource := a.resources[sessionID]
	a.mu.Unlock()
	if resource == nil {
		defer gate.lock.Unlock()
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
	engine, err := resource.beginEngineCallbackUnderAdmission()
	if err != nil {
		gate.lock.Unlock()
		return err
	}
	store := resource.store
	if !serializeCallback {
		gate.lock.Unlock()
	}
	retire := false
	err = func() error {
		defer resource.releaseCallbackCount()
		var callbackErr error
		retire, callbackErr = callback(ctx, store, resource, engine)
		return callbackErr
	}()
	if serializeCallback {
		gate.lock.Unlock()
	}
	if retire {
		err = errors.Join(err, a.retireExactResource(ctx, resource))
	} else {
		err = errors.Join(err, a.closeRetiringResource(context.Background(), resource))
	}
	return err
}

func (a *Authority) retireExactResource(ctx context.Context, resource *agentResource) error {
	sessionID := resource.ref.SessionID()
	gate := a.gateFor(sessionID)
	gate.lock.Lock()
	defer gate.lock.Unlock()
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

func normalizeTarget(target clientui.SessionExecutionTarget, reminder *session.WorktreeReminderState) (clientui.SessionExecutionTarget, *session.WorktreeReminderState, error) {
	normalizedTarget := clientui.NormalizeSessionExecutionTarget(target)
	if normalizedTarget.EffectiveWorkdir == "" {
		return clientui.SessionExecutionTarget{}, nil, errors.New("execution target effective workdir is required")
	}
	if reminder == nil {
		return normalizedTarget, nil, nil
	}
	normalized, err := session.NormalizeWorktreeReminderState(*reminder)
	if err != nil {
		return clientui.SessionExecutionTarget{}, nil, err
	}
	return normalizedTarget, &normalized, nil
}

func syncResourceExecutionTarget(resource *agentResource, engine *runtime.Engine, target clientui.SessionExecutionTarget, reminder *session.WorktreeReminderState) (bool, error) {
	previousReminder := engine.WorktreeReminderState()
	previousContext := tools.FilesystemContext{}
	if resource.localTools != nil {
		previousContext = resource.localTools.FilesystemContext()
	}
	if err := rebindResourceExecutionTarget(resource, engine, target); err != nil {
		return false, err
	}
	if err := engine.SetWorktreeReminderState(reminder); err != nil {
		rollbackErr := rollbackResourceExecutionTarget(resource, engine, previousContext, previousReminder)
		if rollbackErr != nil {
			engine.FailQueuedUserMessages(runtime.QueuedUserMessageFailureRuntimeUnavailable)
			return true, errors.Join(err, rollbackErr)
		}
		return false, err
	}
	return false, nil
}

func rebindResourceContext(resource *agentResource, engine *runtime.Engine, context tools.FilesystemContext) error {
	if resource == nil || engine == nil {
		return errors.New("active runtime resource is required")
	}
	if resource.localTools != nil {
		if err := resource.localTools.ReplaceFilesystemContext(context); err != nil {
			return err
		}
	}
	engine.SetTranscriptWorkingDir(context.Access.WorkingDirectory.LexicalPath)
	return nil
}

func rebindResourceExecutionTarget(resource *agentResource, engine *runtime.Engine, target clientui.SessionExecutionTarget) error {
	if resource == nil || engine == nil {
		return errors.New("active runtime resource is required")
	}
	if resource.localTools != nil {
		var currentWorktreeRoot *string
		if target.Worktree != nil {
			root := target.Worktree.Root
			currentWorktreeRoot = &root
		}
		current := resource.localTools.FilesystemContext()
		targetRoot := target.WorkspaceRoot
		if currentWorktreeRoot != nil {
			targetRoot = *currentWorktreeRoot
		}
		managed := current.ManagedWorktree
		if managed != nil {
			var err error
			managed, err = managed.WithCurrentWorktreeRoot(currentWorktreeRoot)
			if err != nil {
				return err
			}
		}
		next, err := runtimewire.WithExecutionTarget(current, target.EffectiveWorkdir, targetRoot, managed)
		if err != nil {
			return err
		}
		if err := resource.localTools.ReplaceFilesystemContext(next); err != nil {
			return err
		}
	}
	engine.SetTranscriptWorkingDir(target.EffectiveWorkdir)
	return nil
}

func rollbackResourceExecutionTarget(resource *agentResource, engine *runtime.Engine, context tools.FilesystemContext, reminder *session.WorktreeReminderState) error {
	var collected []error
	if strings.TrimSpace(context.Access.WorkingDirectory.LexicalPath) != "" {
		if err := rebindResourceContext(resource, engine, context); err != nil {
			collected = append(collected, fmt.Errorf("rollback runtime workdir: %w", err))
		}
		engine.SetTranscriptWorkingDir(context.Access.WorkingDirectory.LexicalPath)
	}
	if err := engine.SetWorktreeReminderState(reminder); err != nil {
		collected = append(collected, fmt.Errorf("rollback worktree reminder: %w", err))
	}
	return errors.Join(collected...)
}
