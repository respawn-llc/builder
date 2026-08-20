package sessionruntime

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"core/server/runtime"
	"core/server/runtimewire"
	"core/server/session"
	"core/server/tools"
	shelltool "core/server/tools/shell"
	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

type ActiveRuntimeMaintenance struct {
	PreviousFilesystemContext tools.FilesystemContext
	Replace                   func(tools.FilesystemContext) error
}

type worktreeTransitionSubmission struct {
	operation       func(context.Context, func(clientui.SessionExecutionTarget, *session.WorktreeReminderState) error) error
	admissionFailed func(error)
}

func (a *Authority) SyncExecutionTarget(ctx context.Context, sessionID string, target clientui.SessionExecutionTarget, reminder *session.WorktreeReminderState) error {
	id, err := runtimeids.ParseSessionID(strings.TrimSpace(sessionID))
	if err != nil {
		return err
	}
	_, normalizedReminder, err := normalizeTarget(target, reminder)
	if err != nil {
		return err
	}
	return a.withMaintenanceResource(ctx, id, func(_ context.Context, store *session.Store, resource *agentResource, _ *runtime.Engine) (bool, error) {
		if resource == nil {
			if normalizedReminder == nil {
				return false, nil
			}
			return false, store.SetWorktreeReminderState(normalizedReminder)
		}
		return false, errors.New("active Runtime execution target changes require Runtime Steering")
	})
}

func (a *Authority) SubmitWorktreeTransition(
	sessionID string,
	operation func(context.Context, func(clientui.SessionExecutionTarget, *session.WorktreeReminderState) error) error,
	admissionFailed func(error),
) error {
	if operation == nil {
		return nil
	}
	id, err := runtimeids.ParseSessionID(strings.TrimSpace(sessionID))
	if err != nil {
		return err
	}
	a.mu.Lock()
	resource := a.resources[id]
	a.mu.Unlock()
	if resource == nil {
		return a.submitDormantWorktreeTransition(id, worktreeTransitionSubmission{
			operation:       operation,
			admissionFailed: admissionFailed,
		})
	}
	return resource.withEngine(context.Background(), resource.ref, func(_ context.Context, engine *runtime.Engine) error {
		return engine.SubmitWorktreeTransition(operation)
	})
}

func (a *Authority) submitDormantWorktreeTransition(
	sessionID runtimeids.SessionID,
	submission worktreeTransitionSubmission,
) error {
	gate := a.gateFor(sessionID)
	gate.worktreeMu.Lock()
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		gate.worktreeMu.Unlock()
		return ErrAuthorityClosed
	}
	gate.worktreePending = append(gate.worktreePending, submission)
	if gate.worktreeWorkerAlive {
		a.mu.Unlock()
		gate.worktreeMu.Unlock()
		return nil
	}
	gate.worktreeWorkerAlive = true
	a.lifecycleWG.Add(1)
	lifecycleCtx := a.lifecycleCtx
	a.mu.Unlock()
	gate.worktreeMu.Unlock()
	go a.runLifecycleTask(lifecycleCtx, func(ctx context.Context) {
		a.runDormantWorktreeTransitions(ctx, sessionID, gate)
	})
	return nil
}

func (a *Authority) runDormantWorktreeTransitions(
	ctx context.Context,
	sessionID runtimeids.SessionID,
	gate *sessionAdmissionGate,
) {
	for {
		gate.worktreeMu.Lock()
		if len(gate.worktreePending) == 0 {
			gate.worktreeWorkerAlive = false
			gate.worktreeMu.Unlock()
			return
		}
		submission := gate.worktreePending[0]
		gate.worktreePending[0] = worktreeTransitionSubmission{}
		gate.worktreePending = gate.worktreePending[1:]
		if len(gate.worktreePending) == 0 {
			gate.worktreePending = nil
		}
		gate.worktreeMu.Unlock()
		a.runDormantWorktreeTransition(ctx, sessionID, submission)
	}
}

func (a *Authority) runDormantWorktreeTransition(
	ctx context.Context,
	sessionID runtimeids.SessionID,
	submission worktreeTransitionSubmission,
) {
	started := false
	err := a.withMaintenanceResource(ctx, sessionID, func(
		runCtx context.Context,
		store *session.Store,
		resource *agentResource,
		engine *runtime.Engine,
	) (bool, error) {
		if resource != nil {
			submitErr := engine.SubmitWorktreeTransition(submission.operation)
			started = submitErr == nil
			return false, submitErr
		}
		started = true
		return false, submission.operation(runCtx, func(
			target clientui.SessionExecutionTarget,
			reminder *session.WorktreeReminderState,
		) error {
			_, normalizedReminder, normalizeErr := normalizeTarget(target, reminder)
			if normalizeErr != nil || normalizedReminder == nil {
				return normalizeErr
			}
			return store.SetWorktreeReminderState(normalizedReminder)
		})
	})
	if err != nil && !started && submission.admissionFailed != nil {
		submission.admissionFailed(err)
	}
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
		return false, fn(runCtx, store, &ActiveRuntimeMaintenance{
			PreviousFilesystemContext: resource.localTools.FilesystemContext(),
			Replace: func(tools.FilesystemContext) error {
				return errors.New("active Runtime maintenance cannot replace filesystem context")
			},
		})
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
	resource.mu.Lock()
	active := resource.state != AgentResourceReady ||
		resource.current != nil ||
		resource.steps != 0
	engine := resource.engine
	resource.mu.Unlock()
	if !active && engine != nil {
		active = engine.HasActiveLiveRunGroup() ||
			engine.HasPendingSteering()
	}
	return active, nil
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
		}
		a.startBackgroundContinuation(resource, backgroundEvent)
		return true, nil
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
	if err := gate.lock.LockContext(ctx); err != nil {
		return err
	}
	defer gate.lock.Unlock()
	if block := gate.unauthorizedMaintenanceBlock(ctx); block != nil {
		return errors.Join(
			ErrSessionStartsBlocked,
			fmt.Errorf("session %s maintenance is blocked by session start block %d", sessionID, block.reason),
		)
	}
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
	if err := rebindResourceExecutionTarget(resource, engine, target); err != nil {
		return false, err
	}
	if err := engine.SetWorktreeReminderState(reminder); err != nil {
		return false, err
	}
	return false, nil
}

func applyResourceExecutionTarget(resource *agentResource, target clientui.SessionExecutionTarget, reminder *session.WorktreeReminderState) error {
	if resource == nil || resource.engine == nil {
		return errors.New("active runtime resource is required")
	}
	normalizedTarget, normalizedReminder, err := normalizeTarget(target, reminder)
	if err != nil {
		return err
	}
	_, err = syncResourceExecutionTarget(resource, resource.engine, normalizedTarget, normalizedReminder)
	return err
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
