package registry

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"core/server/attentionnotify"
	"core/server/runtime"
	"core/server/runtimeactivity"
	"core/server/runtimeops"
	"core/server/runtimeview"
	askquestion "core/server/tools"
	"core/shared/clientui"
	"core/shared/serverapi"
)

const (
	sessionActivityBufferSize = 256
	promptActivityBufferSize  = 64
)

type RuntimeRegistry struct {
	directory                *runtimeDirectory
	observerMu               sync.Mutex
	observer                 func(sessionID string, reason RuntimeInterestReason)
	sleepObserverMu          sync.Mutex
	sleepObserver            func(active bool)
	runStateMu               sync.Mutex
	runStateCond             *sync.Cond
	blockingActivitySessions map[string]bool
	blockedRuns              map[string]int
	starts                   map[string]map[uint64]runStartReservation
	nextStartID              uint64
	operations               *runtimeops.Coordinator
	readModels               *runtimeactivity.CoordinatorCache
	attentionBroker          *attentionnotify.Broker
	questionBatches          *attentionnotify.QuestionBatchTracker
	executionTargetResolver  func(context.Context, string) (clientui.SessionExecutionTarget, error)
}

type runStartReservation struct {
	ctx       context.Context
	cancel    context.CancelFunc
	exclusive bool
}

type SessionRunStart struct {
	registry  *RuntimeRegistry
	sessionID string
	startID   uint64
	ctx       context.Context
	cancel    context.CancelFunc
	once      sync.Once
}

func (s *SessionRunStart) Context() context.Context {
	if s == nil || s.ctx == nil {
		return context.Background()
	}
	return s.ctx
}

func (s *SessionRunStart) Cancel() {
	if s == nil || s.cancel == nil {
		return
	}
	s.cancel()
}

func (s *SessionRunStart) Release() {
	if s == nil || s.registry == nil {
		return
	}
	s.once.Do(func() {
		s.Cancel()
		s.registry.runStateMu.Lock()
		defer s.registry.runStateMu.Unlock()
		s.registry.clearStartLocked(s.sessionID, s.startID)
	})
}

func (r *RuntimeRegistry) BlockSessionRuns(sessionIDs []string) func() {
	if r == nil {
		return func() {}
	}
	blocked := make([]string, 0, len(sessionIDs))
	r.runStateMu.Lock()
	for _, sessionID := range sessionIDs {
		trimmed := strings.TrimSpace(sessionID)
		if trimmed == "" {
			continue
		}
		r.blockedRuns[trimmed]++
		blocked = append(blocked, trimmed)
	}
	for r.anyInFlightStartLocked(blocked) {
		r.runStateCond.Wait()
	}
	r.runStateMu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			r.runStateMu.Lock()
			defer r.runStateMu.Unlock()
			for _, sessionID := range blocked {
				if r.blockedRuns[sessionID] <= 1 {
					delete(r.blockedRuns, sessionID)
					continue
				}
				r.blockedRuns[sessionID]--
			}
			r.runStateCond.Broadcast()
		})
	}
}

func (r *RuntimeRegistry) anyInFlightStartLocked(sessionIDs []string) bool {
	for _, sessionID := range sessionIDs {
		if len(r.starts[sessionID]) > 0 {
			return true
		}
	}
	return false
}

func (r *RuntimeRegistry) SessionRunsBlocked(sessionID string) bool {
	if r == nil {
		return false
	}
	trimmed := strings.TrimSpace(sessionID)
	if trimmed == "" {
		return false
	}
	r.runStateMu.Lock()
	defer r.runStateMu.Unlock()
	return r.blockedRuns[trimmed] > 0
}

func (r *RuntimeRegistry) BeginSessionRun(sessionID string) (func(), bool) {
	_, release, ok := r.BeginCancellableSessionRun(sessionID)
	if !ok {
		return nil, false
	}
	return release, true
}

func (r *RuntimeRegistry) BeginCancellableSessionRun(sessionID string) (context.Context, func(), bool) {
	if r == nil {
		return context.Background(), func() {}, true
	}
	trimmed := strings.TrimSpace(sessionID)
	if trimmed == "" {
		return context.Background(), func() {}, true
	}
	r.runStateMu.Lock()
	if r.blockedRuns[trimmed] > 0 || len(r.starts[trimmed]) > 0 {
		r.runStateMu.Unlock()
		return nil, nil, false
	}
	startID := r.addStartLocked(trimmed, false)
	reservation := r.starts[trimmed][startID]
	r.runStateMu.Unlock()
	token := &SessionRunStart{
		registry:  r,
		sessionID: trimmed,
		startID:   startID,
		ctx:       reservation.ctx,
		cancel:    reservation.cancel,
	}
	return token.Context(), token.Release, true
}

func (r *RuntimeRegistry) BeginExclusiveSessionRun(sessionID string) (release func(), acquired bool, blocked bool) {
	if r == nil {
		return func() {}, true, false
	}
	trimmed := strings.TrimSpace(sessionID)
	if trimmed == "" {
		return func() {}, true, false
	}
	r.runStateMu.Lock()
	if r.blockedRuns[trimmed] > 0 {
		r.runStateMu.Unlock()
		return nil, false, true
	}
	if len(r.starts[trimmed]) > 0 {
		r.runStateMu.Unlock()
		return nil, false, false
	}
	startID := r.addStartLocked(trimmed, true)
	r.runStateMu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			r.runStateMu.Lock()
			defer r.runStateMu.Unlock()
			r.clearStartLocked(trimmed, startID)
		})
	}, true, false
}

func (r *RuntimeRegistry) addStartLocked(sessionID string, exclusive bool) uint64 {
	r.nextStartID++
	startID := r.nextStartID
	if r.starts[sessionID] == nil {
		r.starts[sessionID] = make(map[uint64]runStartReservation)
	}
	ctx, cancel := context.WithCancel(context.Background())
	r.starts[sessionID][startID] = runStartReservation{ctx: ctx, cancel: cancel, exclusive: exclusive}
	return startID
}

func (r *RuntimeRegistry) clearStartLocked(sessionID string, startID uint64) {
	if reservation, ok := r.starts[sessionID][startID]; ok && reservation.cancel != nil {
		reservation.cancel()
	}
	delete(r.starts[sessionID], startID)
	if len(r.starts[sessionID]) == 0 {
		delete(r.starts, sessionID)
	}
	r.runStateCond.Broadcast()
}

func (r *RuntimeRegistry) hasExclusiveStartLocked(sessionID string) bool {
	for _, reservation := range r.starts[sessionID] {
		if reservation.exclusive {
			return true
		}
	}
	return false
}

type GuardedPromptResponder interface {
	SubmitPromptResponse(resp askquestion.AskQuestionResponse, err error) error
}

type RuntimeInterestReason int

const (
	RuntimeInterestChanged RuntimeInterestReason = iota
	RuntimeInterestRunFinished
)

func NewRuntimeRegistry() *RuntimeRegistry {
	r := &RuntimeRegistry{
		directory:                newRuntimeDirectory(),
		blockingActivitySessions: make(map[string]bool),
		blockedRuns:              make(map[string]int),
		starts:                   make(map[string]map[uint64]runStartReservation),
		readModels:               runtimeactivity.NewCoordinatorCache(runtimeactivity.DefaultCoordinatorCacheLimit),
	}
	r.runStateCond = sync.NewCond(&r.runStateMu)
	return r
}

func (r *RuntimeRegistry) WithOperationCoordinator(coordinator *runtimeops.Coordinator) *RuntimeRegistry {
	if r == nil {
		return nil
	}
	r.operations = coordinator
	if r.operations != nil {
		r.operations.SetVersionAllocator(r.nextReadModelVersion)
	}
	return r
}

func (r *RuntimeRegistry) WithExecutionTargetResolver(resolver func(context.Context, string) (clientui.SessionExecutionTarget, error)) *RuntimeRegistry {
	if r == nil {
		return nil
	}
	r.executionTargetResolver = resolver
	return r
}

func (r *RuntimeRegistry) WithTranscriptContractViolationPanic(enabled bool) *RuntimeRegistry {
	if r == nil {
		return nil
	}
	transcriptContractViolationsPanic = enabled
	return r
}

func (r *RuntimeRegistry) nextReadModelVersion(sessionID string) clientui.ReadModelVersion {
	if r == nil || r.readModels == nil {
		return runtimeactivity.NextReadModelVersion(sessionID)
	}
	return r.readModels.Next(sessionID)
}

func (r *RuntimeRegistry) closeEntry(ctx context.Context, sessionID string, engine *runtime.Engine, drain func(context.Context) error) (bool, error) {
	if r == nil {
		return false, nil
	}
	id, entry, drainRef := r.directory.BeginClose(sessionID, engine)
	if id == "" || entry == nil || drainRef == nil {
		return false, nil
	}
	r.publishCurrentRuntimeActivity(id)
	return r.finishClose(ctx, id, engine, entry, drainRef, drain)
}

func (r *RuntimeRegistry) finishClose(ctx context.Context, sessionID string, engine *runtime.Engine, entry *runtimeEntry, drainRef *runtimeCloseDrainRef, drain func(context.Context) error) (bool, error) {
	drainRef.WaitForGuards()
	var drainErr error
	if drain != nil {
		drainErr = drain(ctx)
	}
	drainRef.WaitForGuards()
	removedID, removedEntry := r.directory.RemoveClosing(sessionID, engine, entry)
	if removedID == "" || removedEntry == nil {
		drainRef.Release()
		return false, drainErr
	}
	r.publishUnavailableRuntimeActivityToEntry(removedID, removedEntry)
	drainRef.Release()
	r.finishEntryTeardown(removedID, removedEntry)
	return true, drainErr
}

func (r *RuntimeRegistry) finishEntryTeardown(sessionID string, entry *runtimeEntry) {
	if entry == nil {
		return
	}
	r.unpinRuntimeReadModel(entry)
	closeRuntimeEntry(entry, io.EOF)
	if entry.teardown != nil {
		entry.teardown()
	}
	entry.signalClosed()
}

func (r *RuntimeRegistry) retireGuardedRuntime(guard *runtimeGuard, reason runtime.QueuedUserMessageFailureReason) error {
	if r == nil || guard == nil || guard.entry == nil {
		return fmt.Errorf("runtime guard is unavailable")
	}
	id := strings.TrimSpace(guard.sessionID)
	if id == "" {
		return fmt.Errorf("runtime session id is required")
	}
	if guard.engine != nil {
		guard.engine.FailQueuedUserMessages(reason)
	}
	r.directory.mu.Lock()
	if r.directory.entries[id] != guard.entry {
		r.directory.mu.Unlock()
		return ErrRuntimeGuardOvertaken
	}
	delete(r.directory.entries, id)
	r.directory.mu.Unlock()
	guard.entry.markClosing()
	r.publishUnavailableRuntimeActivityToEntry(id, guard.entry)
	var closeErr error
	if guard.engine != nil {
		closeErr = guard.engine.Close()
	}
	go r.finishEntryTeardown(id, guard.entry)
	return closeErr
}

type RuntimeGuard interface {
	Engine() *runtime.Engine
	Generation() uint64
	Rebind(workdir string) error
	Retire(reason runtime.QueuedUserMessageFailureReason) error
	GuardedPromptResponder
	Release()
}

func (r *RuntimeRegistry) BeginRuntimeGuard(ctx context.Context, sessionID string) (RuntimeGuard, error) {
	if r == nil {
		return nil, fmt.Errorf("runtime registry is required")
	}
	guard, err := r.directory.BeginGuard(ctx, sessionID)
	if guard != nil {
		guard.registry = r
	}
	return guard, err
}

func (r *RuntimeRegistry) ResolveRuntime(_ context.Context, sessionID string) (*runtime.Engine, error) {
	if r == nil {
		return nil, nil
	}
	return r.directory.Resolve(sessionID), nil
}

func (r *RuntimeRegistry) WithGuardedRuntime(ctx context.Context, sessionID string, fn func(*runtime.Engine) error) (bool, error) {
	if r == nil {
		return false, nil
	}
	guard, err := r.directory.BeginGuard(ctx, sessionID)
	if err != nil {
		if errors.Is(err, serverapi.ErrRuntimeUnavailable) {
			return false, nil
		}
		return false, err
	}
	defer guard.Release()
	return true, fn(guard.Engine())
}

func (r *RuntimeRegistry) WithAcquiredRuntime(ctx context.Context, sessionID string, engine *runtime.Engine, fn func(context.Context, *runtime.Engine) error) (bool, error) {
	if r == nil {
		return false, nil
	}
	guard, err := r.directory.BeginGuard(ctx, sessionID)
	if err != nil {
		return false, err
	}
	defer guard.Release()
	if guard.Engine() != engine {
		return false, nil
	}
	return true, fn(ctx, guard.Engine())
}

func (r *RuntimeRegistry) IsSessionRuntimeActive(sessionID string) bool {
	if r == nil {
		return false
	}
	return r.directory.Active(sessionID)
}

func (r *RuntimeRegistry) RuntimeActivity(sessionID string) (clientui.RuntimeActivity, error) {
	id := strings.TrimSpace(sessionID)
	if r == nil || id == "" {
		return clientui.NewRuntimeActivity(clientui.RuntimeActivityUnavailable, clientui.RuntimeActivityOptions{})
	}
	snapshot, err := r.RuntimeReadModelSnapshot(context.Background(), id, nil)
	if err != nil {
		return clientui.RuntimeActivity{}, err
	}
	return snapshot.Activity, nil
}

func (r *RuntimeRegistry) RuntimeReadModelSnapshot(ctx context.Context, sessionID string, refs []clientui.RuntimeOperationRef) (runtimeactivity.ResponseSnapshot, error) {
	id := strings.TrimSpace(sessionID)
	if r == nil || id == "" {
		return runtimeactivity.BuildSnapshot(id, func(version clientui.ReadModelVersion) (runtimeactivity.SnapshotInput, error) {
			return runtimeactivity.SnapshotInput{
				Resolver:            runtimeactivity.ResolverSnapshot{},
				InputReconciliation: clientui.NewEmptyRuntimeInputReconciliationSnapshot(version),
			}, nil
		})
	}
	return r.readModelSnapshot(id, func(version clientui.ReadModelVersion) (runtimeactivity.SnapshotInput, error) {
		resolver, err := r.runtimeActivityResolverSnapshot(ctx, id)
		if err != nil {
			return runtimeactivity.SnapshotInput{}, err
		}
		reconciliation := clientui.NewEmptyRuntimeInputReconciliationSnapshot(version)
		if r.operations != nil {
			reconciliation = r.operations.Snapshot(id, version, refs)
		}
		return runtimeactivity.SnapshotInput{
			Resolver:            resolver,
			InputReconciliation: reconciliation,
		}, nil
	})
}

func (r *RuntimeRegistry) readModelSnapshot(sessionID string, build runtimeactivity.SnapshotBuilder) (runtimeactivity.ResponseSnapshot, error) {
	if r == nil || r.readModels == nil {
		return runtimeactivity.BuildSnapshot(sessionID, build)
	}
	return r.readModels.WithSnapshot(sessionID, build)
}

func (r *RuntimeRegistry) unavailableRuntimeReadModelSnapshot(sessionID string) (runtimeactivity.ResponseSnapshot, error) {
	id := strings.TrimSpace(sessionID)
	return r.readModelSnapshot(id, func(version clientui.ReadModelVersion) (runtimeactivity.SnapshotInput, error) {
		return runtimeactivity.SnapshotInput{
			Resolver:            runtimeactivity.ResolverSnapshot{},
			InputReconciliation: clientui.NewEmptyRuntimeInputReconciliationSnapshot(version),
		}, nil
	})
}

func (r *RuntimeRegistry) pinRuntimeReadModel(sessionID string, entry *runtimeEntry) {
	if r == nil || r.readModels == nil || entry == nil {
		return
	}
	entry.mu.Lock()
	defer entry.mu.Unlock()
	if entry.readModelUnpin == nil {
		entry.readModelUnpin = r.readModels.Pin(sessionID)
	}
	entry.readModelVersion = r.nextReadModelVersion
}

func (r *RuntimeRegistry) unpinRuntimeReadModel(entry *runtimeEntry) {
	if entry == nil {
		return
	}
	entry.mu.Lock()
	unpin := entry.readModelUnpin
	entry.readModelUnpin = nil
	entry.readModelVersion = nil
	entry.mu.Unlock()
	if unpin != nil {
		unpin()
	}
}

func (r *RuntimeRegistry) runtimeActivityResolverSnapshot(ctx context.Context, sessionID string) (runtimeactivity.ResolverSnapshot, error) {
	id := strings.TrimSpace(sessionID)
	if r == nil || id == "" {
		return runtimeactivity.ResolverSnapshot{}, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	entry := r.directory.Entry(id)
	engine, err := r.ResolveRuntime(ctx, id)
	if err != nil {
		return runtimeactivity.ResolverSnapshot{}, err
	}
	snapshot := runtimeactivity.ResolverSnapshot{Registry: r.RuntimeActivityRegistrySnapshot(id)}
	snapshot.Active = runtimeactivity.ActiveStepFromProvider(engine)
	if engine != nil {
		snapshot.LiveRunActive = engine.HasActiveLiveRunGroup()
	}
	if entry != nil && len(entry.pendingPrompts.List()) > 0 {
		snapshot.PromptWait = true
	}
	return snapshot, nil
}

func (r *RuntimeRegistry) RuntimeActivityRegistrySnapshot(sessionID string) runtimeactivity.RegistrySnapshot {
	if r == nil {
		return runtimeactivity.RegistrySnapshot{}
	}
	id := strings.TrimSpace(sessionID)
	if id == "" {
		return runtimeactivity.RegistrySnapshot{}
	}
	entry := r.directory.Entry(id)
	if entry == nil {
		return runtimeactivity.RegistrySnapshot{}
	}
	closing, draining := entry.closeState()
	starting := entry.buildInProgress()
	return runtimeactivity.RegistrySnapshot{
		Registered:     true,
		QueueAccepting: !closing && !draining,
		Draining:       draining,
		Closing:        closing && !draining,
		Starting:       starting,
	}
}

func (r *RuntimeRegistry) publishCurrentRuntimeActivity(sessionID string) {
	if r == nil {
		return
	}
	id := strings.TrimSpace(sessionID)
	response, err := r.RuntimeReadModelSnapshot(context.Background(), id, nil)
	if err != nil {
		return
	}
	r.PublishRuntimeActivitySnapshot(id, response)
}

func (r *RuntimeRegistry) publishUnavailableRuntimeActivityToEntry(sessionID string, entry *runtimeEntry) {
	if r == nil || entry == nil || entry.sessionActivity == nil {
		return
	}
	id := strings.TrimSpace(sessionID)
	response, err := r.unavailableRuntimeReadModelSnapshot(id)
	if err != nil {
		return
	}
	activity := response.Activity
	reconciliation := response.InputReconciliation
	if entry.sessionFeed != nil {
		entry.sessionFeed.Publish([]clientui.TranscriptMessage{
			{Kind: clientui.TranscriptMessageRuntimeActivity, RuntimeActivity: &activity},
			{Kind: clientui.TranscriptMessageInputReconciliation, InputReconciliation: &reconciliation},
		})
	}
	entry.sessionActivity.Publish(clientui.Event{
		Kind:                clientui.EventRuntimeActivityChanged,
		ReadModelVersion:    response.Version,
		RuntimeActivity:     &activity,
		InputReconciliation: &reconciliation,
	})
	r.updateAggregateRuntimeActivityForEntry(id, entry, false)
}

func (r *RuntimeRegistry) PublishRuntimeEventToAll(evt runtime.Event) {
	if r == nil {
		return
	}
	for _, id := range r.directory.IDs() {
		r.PublishRuntimeEvent(id, evt)
	}
}

func (r *RuntimeRegistry) PublishRuntimeEvent(sessionID string, evt runtime.Event) {
	if r == nil {
		return
	}
	entry := r.directory.Entry(sessionID)
	r.publishRuntimeEventToEntry(sessionID, entry, evt)
}

func (r *RuntimeRegistry) PublishRuntimeEventForEngine(sessionID string, engine *runtime.Engine, evt runtime.Event) {
	if r == nil || engine == nil {
		return
	}
	entry := r.directory.Entry(sessionID)
	if entry == nil || entry.engineRef() != engine {
		return
	}
	r.publishRuntimeEventToEntry(sessionID, entry, evt)
}

func (r *RuntimeRegistry) publishRuntimeEventToEntry(sessionID string, entry *runtimeEntry, evt runtime.Event) {
	if entry == nil || entry.sessionActivity == nil {
		return
	}
	if entry.sessionFeed != nil {
		if !transcriptEventRequiresVisibleSubscriber(evt) || entry.sessionFeed.HasSubscribers() {
			entry.sessionFeed.Publish(runtimeview.TranscriptMessagesFromRuntimeEvent(evt))
		}
		if engine := entry.engineRef(); engine != nil && runtimeEventShouldPublishSessionStatus(evt) {
			status := runtimeview.TranscriptSessionStatusFromRuntime(engine)
			entry.sessionFeed.Publish([]clientui.TranscriptMessage{{Kind: clientui.TranscriptMessageSessionStatus, SessionStatus: &status}})
		}
	}
	entry.sessionActivity.Publish(runtimeview.EventFromRuntime(evt))
	r.recordQueuedMessageOperationStatus(evt)
	if evt.RunState != nil {
		reason := RuntimeInterestChanged
		if evt.RunState.Lifecycle.Phase == runtime.RunLifecycleFinished {
			reason = RuntimeInterestRunFinished
		}
		r.notifyInterestChanged(sessionID, reason)
	}
}

func (r *RuntimeRegistry) PublishSessionIdentity(sessionID string, target *clientui.SessionExecutionTarget) {
	if r == nil {
		return
	}
	id := strings.TrimSpace(sessionID)
	entry := r.directory.Entry(id)
	if entry == nil || entry.sessionFeed == nil {
		return
	}
	engine := entry.engineRef()
	if engine == nil {
		return
	}
	identity := runtimeview.TranscriptSessionIdentityFromRuntime(engine)
	if target != nil {
		identity.ExecutionTarget = clientui.NormalizeSessionExecutionTarget(*target)
	} else if resolved, ok := r.resolveSessionExecutionTarget(context.Background(), id); ok {
		identity.ExecutionTarget = resolved
	}
	entry.sessionFeed.Publish([]clientui.TranscriptMessage{{Kind: clientui.TranscriptMessageSessionIdentity, SessionIdentity: &identity}})
}

func (r *RuntimeRegistry) PublishSessionStatus(sessionID string) {
	if r == nil {
		return
	}
	entry := r.directory.Entry(strings.TrimSpace(sessionID))
	if entry == nil || entry.sessionFeed == nil {
		return
	}
	engine := entry.engineRef()
	if engine == nil {
		return
	}
	status := runtimeview.TranscriptSessionStatusFromRuntime(engine)
	entry.sessionFeed.Publish([]clientui.TranscriptMessage{{Kind: clientui.TranscriptMessageSessionStatus, SessionStatus: &status}})
}

func (r *RuntimeRegistry) resolveSessionExecutionTarget(ctx context.Context, sessionID string) (clientui.SessionExecutionTarget, bool) {
	if r == nil || r.executionTargetResolver == nil {
		return clientui.SessionExecutionTarget{}, false
	}
	target, err := r.executionTargetResolver(ctx, strings.TrimSpace(sessionID))
	if err != nil {
		return clientui.SessionExecutionTarget{}, false
	}
	return clientui.NormalizeSessionExecutionTarget(target), true
}

func runtimeEventShouldPublishSessionStatus(evt runtime.Event) bool {
	return evt.ContextUsage != nil || evt.GoalStatus != nil || evt.Compaction != nil || evt.Kind == runtime.EventAssistantMessage
}

func transcriptEventRequiresVisibleSubscriber(evt runtime.Event) bool {
	return evt.Kind == runtime.EventAssistantDelta || evt.Kind == runtime.EventAssistantDeltaReset
}

func (r *RuntimeRegistry) recordQueuedMessageOperationStatus(evt runtime.Event) {
	if r == nil || r.operations == nil || evt.Kind != runtime.EventQueuedUserMessageStatus || evt.QueuedUserMessageStatus == nil {
		return
	}
	status := evt.QueuedUserMessageStatus
	ref := clientui.RuntimeOperationRef{
		Kind:        clientui.RuntimeOperationKindQueuedMessage,
		QueueItemID: status.QueueItemID,
	}
	switch status.Status {
	case runtime.QueuedUserMessageSubmitted:
		r.operations.RecordQueuedMessageSubmitted(status.SessionID, ref)
	case runtime.QueuedUserMessageFailed:
		r.operations.RecordQueuedMessageFailed(status.SessionID, ref)
	case runtime.QueuedUserMessageDiscarded:
		r.operations.RecordCanceledNotCommitted(status.SessionID, ref)
	}
}

func (r *RuntimeRegistry) PublishRuntimeActivitySnapshot(sessionID string, snapshot runtimeactivity.ResponseSnapshot) {
	if r == nil {
		return
	}
	entry := r.directory.Entry(sessionID)
	if entry == nil || entry.sessionActivity == nil {
		return
	}
	activity := snapshot.Activity
	reconciliation := snapshot.InputReconciliation
	if entry.sessionFeed != nil {
		entry.sessionFeed.Publish([]clientui.TranscriptMessage{
			{Kind: clientui.TranscriptMessageRuntimeActivity, RuntimeActivity: &activity},
			{Kind: clientui.TranscriptMessageInputReconciliation, InputReconciliation: &reconciliation},
		})
	}
	entry.sessionActivity.Publish(clientui.Event{
		Kind:                clientui.EventRuntimeActivityChanged,
		ReadModelVersion:    snapshot.Version,
		RuntimeActivity:     &activity,
		InputReconciliation: &reconciliation,
	})
	if r.updateAggregateRuntimeActivityForEntry(sessionID, entry, activity.ActiveForControl()) {
		r.notifyInterestChanged(sessionID, RuntimeInterestChanged)
	}
}

func (r *RuntimeRegistry) PublishWorktreeTransitionOutcome(sessionID string, outcome clientui.WorktreeTransitionOutcome) {
	if r == nil {
		return
	}
	if err := outcome.Validate(); err != nil {
		panic(fmt.Sprintf("publish invalid worktree transition outcome for session %q: %v", strings.TrimSpace(sessionID), err))
	}
	entry := r.directory.Entry(strings.TrimSpace(sessionID))
	if entry == nil || entry.sessionActivity == nil {
		return
	}
	entry.sessionActivity.Publish(clientui.Event{
		Kind:               clientui.EventWorktreeTransitionOutcome,
		WorktreeTransition: &outcome,
	})
}

func (r *RuntimeRegistry) SubscribeSessionActivity(_ context.Context, sessionID string) (serverapi.SessionActivitySubscription, error) {
	return r.SubscribeSessionActivityFrom(context.Background(), serverapi.SessionActivitySubscribeRequest{SessionID: sessionID})
}

func (r *RuntimeRegistry) SubscribeSessionActivityFrom(_ context.Context, req serverapi.SessionActivitySubscribeRequest) (serverapi.SessionActivitySubscription, error) {
	if r == nil {
		return nil, fmt.Errorf("runtime registry is required")
	}
	id := strings.TrimSpace(req.SessionID)
	entry := r.directory.Entry(id)
	if entry == nil || entry.sessionActivity == nil {
		return nil, fmt.Errorf("session activity stream for %q is unavailable: %w", id, serverapi.ErrSessionActivityUnavailable)
	}
	sub, err := entry.sessionActivity.Subscribe(req.AfterSequence)
	if err != nil {
		return nil, err
	}
	r.notifyInterestChanged(id, RuntimeInterestChanged)
	return &notifyingSessionActivitySubscription{SessionActivitySubscription: sub, onClose: func() {
		r.notifyInterestChanged(id, RuntimeInterestChanged)
	}}, nil
}

func (r *RuntimeRegistry) SubscribeSessionTranscript(_ context.Context, req serverapi.SessionTranscriptSubscribeRequest) (serverapi.SessionTranscriptSubscription, error) {
	if r == nil {
		return nil, fmt.Errorf("runtime registry is required")
	}
	id := strings.TrimSpace(req.SessionID)
	entry := r.directory.Entry(id)
	if entry == nil || entry.sessionFeed == nil {
		return nil, fmt.Errorf("session transcript stream for %q is unavailable: %w", id, serverapi.ErrStreamUnavailable)
	}
	engine := entry.engineRef()
	if engine == nil {
		return nil, fmt.Errorf("session transcript stream for %q is unavailable: %w", id, serverapi.ErrStreamUnavailable)
	}
	var sub *transcriptSubscription
	err := engine.WithTranscriptHydrationSnapshot(func(snapshot runtime.TranscriptHydrationSnapshot) error {
		var subscribeErr error
		hydration := runtimeview.TranscriptHydrationFromSnapshot(snapshot)
		hydration.InFlightTools = runtimeview.TranscriptToolStartsFromRuntime(engine.TranscriptLiveToolSnapshot())
		hydration.SessionStatus = runtimeview.TranscriptSessionStatusFromRuntime(engine)
		hydration.SessionIdentity = runtimeview.TranscriptSessionIdentityFromRuntime(engine)
		if target, ok := r.resolveSessionExecutionTarget(context.Background(), id); ok {
			hydration.SessionIdentity.ExecutionTarget = target
		}
		sub, subscribeErr = entry.sessionFeed.Subscribe(hydration)
		return subscribeErr
	})
	if err != nil {
		return nil, err
	}
	r.notifyInterestChanged(id, RuntimeInterestChanged)
	return &notifyingSessionTranscriptSubscription{SessionTranscriptSubscription: sub, onClose: func() {
		r.notifyInterestChanged(id, RuntimeInterestChanged)
	}}, nil
}

func (r *RuntimeRegistry) SubscribePromptActivity(_ context.Context, sessionID string) (serverapi.PromptActivitySubscription, error) {
	return r.SubscribePromptActivityFrom(context.Background(), serverapi.PromptActivitySubscribeRequest{SessionID: sessionID})
}

func (r *RuntimeRegistry) SubscribePromptActivityFrom(_ context.Context, req serverapi.PromptActivitySubscribeRequest) (serverapi.PromptActivitySubscription, error) {
	if r == nil {
		return nil, fmt.Errorf("runtime registry is required")
	}
	id := strings.TrimSpace(req.SessionID)
	entry := r.directory.Entry(id)
	if entry == nil || entry.promptActivity == nil {
		return nil, fmt.Errorf("prompt activity stream for %q is unavailable: %w", id, serverapi.ErrStreamUnavailable)
	}
	if isZeroReadModelVersion(req.AfterReadModelVersion) {
		sub, err := entry.SubscribePromptActivityInitial(id, r.nextReadModelVersion(id), nil)
		if err != nil {
			return nil, err
		}
		r.notifyInterestChanged(id, RuntimeInterestChanged)
		return &notifyingPromptActivitySubscription{PromptActivitySubscription: sub, onClose: func() {
			r.notifyInterestChanged(id, RuntimeInterestChanged)
		}}, nil
	}
	sub, err := entry.promptActivity.Subscribe(nil, req.AfterReadModelVersion)
	if err != nil {
		return nil, err
	}
	if sub == nil {
		return nil, fmt.Errorf("prompt activity stream for %q is unavailable: %w", id, serverapi.ErrStreamUnavailable)
	}
	r.notifyInterestChanged(id, RuntimeInterestChanged)
	return &notifyingPromptActivitySubscription{PromptActivitySubscription: sub, onClose: func() {
		r.notifyInterestChanged(id, RuntimeInterestChanged)
	}}, nil
}

func (r *RuntimeRegistry) BeginPendingPrompt(sessionID string, req askquestion.AskQuestionRequest) {
	if r == nil {
		return
	}
	id := strings.TrimSpace(sessionID)
	entry := r.directory.Entry(id)
	if entry == nil {
		return
	}
	_, ok := entry.pendingPrompts.Begin(req, func(snapshot PendingPromptSnapshot, eventType pendingPromptEventType) {
		entry.PublishPendingPrompt(id, snapshot, eventType, r.nextReadModelVersion(id))
		if eventType == pendingPromptEventPending {
			r.publishAttentionPending(id, snapshot)
		}
	})
	if !ok {
		return
	}
	r.publishCurrentRuntimeActivity(id)
}

func (r *RuntimeRegistry) CompletePendingPrompt(sessionID string, requestID string) {
	if r == nil {
		return
	}
	id := strings.TrimSpace(sessionID)
	entry := r.directory.Entry(id)
	if entry == nil {
		return
	}
	snapshot, ok := entry.pendingPrompts.Complete(requestID)
	if ok {
		entry.PublishPendingPrompt(id, snapshot, pendingPromptEventResolved, r.nextReadModelVersion(id))
		r.publishCurrentRuntimeActivity(id)
		r.publishAttentionResolved(id, snapshot)
	}
}

func (r *RuntimeRegistry) ListPendingPrompts(sessionID string) []PendingPromptSnapshot {
	if r == nil {
		return nil
	}
	entry := r.directory.Entry(sessionID)
	if entry == nil {
		return nil
	}
	return entry.pendingPrompts.List()
}

func (r *RuntimeRegistry) AwaitPromptResponse(ctx context.Context, sessionID string, req askquestion.AskQuestionRequest) (askquestion.AskQuestionResponse, error) {
	if r == nil {
		return askquestion.AskQuestionResponse{}, fmt.Errorf("runtime registry is required")
	}
	id := strings.TrimSpace(sessionID)
	entry := r.directory.Entry(id)
	if entry == nil {
		return askquestion.AskQuestionResponse{}, fmt.Errorf("runtime %q is unavailable", id)
	}
	return entry.pendingPrompts.Await(ctx, req, func(snapshot PendingPromptSnapshot, eventType pendingPromptEventType) {
		entry.PublishPendingPrompt(id, snapshot, eventType, r.nextReadModelVersion(id))
		if eventType == pendingPromptEventPending {
			r.publishAttentionPending(id, snapshot)
		} else if eventType == pendingPromptEventResolved {
			r.publishAttentionResolved(id, snapshot)
		}
	})
}

func (r *RuntimeRegistry) SubmitPromptResponse(sessionID string, resp askquestion.AskQuestionResponse, err error) error {
	if r == nil {
		return fmt.Errorf("runtime registry is required")
	}
	id := strings.TrimSpace(sessionID)
	guard, guardErr := r.directory.BeginPromptResponseGuard(context.Background(), id, resp.RequestID)
	if guardErr != nil {
		return guardErr
	}
	defer guard.Release()
	submitErr := guard.entry.pendingPrompts.Submit(resp, err, func(snapshot PendingPromptSnapshot, eventType pendingPromptEventType) {
		guard.entry.PublishPendingPrompt(id, snapshot, eventType, r.nextReadModelVersion(id))
		r.publishCurrentRuntimeActivity(id)
		if eventType == pendingPromptEventPending {
			r.publishAttentionPending(id, snapshot)
		} else if eventType == pendingPromptEventResolved {
			r.publishAttentionResolved(id, snapshot)
		}
	})
	if submitErr == nil {
		r.publishCurrentRuntimeActivity(id)
	}
	return submitErr
}

func (r *RuntimeRegistry) SetInterestObserver(observer func(sessionID string, reason RuntimeInterestReason)) {
	if r == nil {
		return
	}
	r.observerMu.Lock()
	r.observer = observer
	r.observerMu.Unlock()
}

func (r *RuntimeRegistry) SetSleepObserver(observer func(active bool)) {
	if r == nil {
		return
	}
	r.sleepObserverMu.Lock()
	r.sleepObserver = observer
	r.sleepObserverMu.Unlock()
}

func (r *RuntimeRegistry) HasRuntimeSubscribers(sessionID string) bool {
	if r == nil {
		return false
	}
	entry := r.directory.Entry(sessionID)
	if entry == nil {
		return false
	}
	return entry.sessionActivity.SubscriberCount() > 0 || entry.promptActivity.SubscriberCount() > 0 || entry.sessionFeed.HasSubscribers()
}

func (r *RuntimeRegistry) notifyInterestChanged(sessionID string, reason RuntimeInterestReason) {
	if r == nil {
		return
	}
	id := strings.TrimSpace(sessionID)
	if id == "" {
		return
	}
	r.observerMu.Lock()
	observer := r.observer
	r.observerMu.Unlock()
	if observer != nil {
		observer(id, reason)
	}
}

func (r *RuntimeRegistry) updateAggregateRuntimeActivity(sessionID string, activeForControl bool) {
	if r == nil {
		return
	}
	id := strings.TrimSpace(sessionID)
	if id == "" {
		return
	}
	r.updateAggregateRuntimeActivityState(id, activeForControl)
}

func (r *RuntimeRegistry) updateAggregateRuntimeActivityForEntry(sessionID string, entry *runtimeEntry, activeForControl bool) bool {
	if r == nil {
		return false
	}
	id := strings.TrimSpace(sessionID)
	if id == "" {
		return false
	}
	r.directory.mu.RLock()
	current := r.directory.entries[id]
	if current != entry && (activeForControl || current != nil) {
		r.directory.mu.RUnlock()
		return false
	}
	r.updateAggregateRuntimeActivityState(id, activeForControl)
	r.directory.mu.RUnlock()
	return true
}

func (r *RuntimeRegistry) updateAggregateRuntimeActivityState(sessionID string, activeForControl bool) {
	r.runStateMu.Lock()
	wasActive := len(r.blockingActivitySessions) > 0
	if activeForControl {
		r.blockingActivitySessions[sessionID] = true
	} else {
		delete(r.blockingActivitySessions, sessionID)
	}
	active := len(r.blockingActivitySessions) > 0
	if wasActive == active {
		r.runStateMu.Unlock()
		return
	}
	r.sleepObserverMu.Lock()
	observer := r.sleepObserver
	r.runStateMu.Unlock()
	defer r.sleepObserverMu.Unlock()
	if observer != nil {
		observer(active)
	}
}

type notifyingSessionActivitySubscription struct {
	serverapi.SessionActivitySubscription
	once    sync.Once
	onClose func()
}

func (s *notifyingSessionActivitySubscription) Close() error {
	if s == nil {
		return nil
	}
	var err error
	if s.SessionActivitySubscription != nil {
		err = s.SessionActivitySubscription.Close()
	}
	s.once.Do(func() {
		if s.onClose != nil {
			s.onClose()
		}
	})
	return err
}

type notifyingSessionTranscriptSubscription struct {
	serverapi.SessionTranscriptSubscription
	once    sync.Once
	onClose func()
}

func (s *notifyingSessionTranscriptSubscription) Close() error {
	if s == nil {
		return nil
	}
	var err error
	if s.SessionTranscriptSubscription != nil {
		err = s.SessionTranscriptSubscription.Close()
	}
	s.once.Do(func() {
		if s.onClose != nil {
			s.onClose()
		}
	})
	return err
}

type notifyingPromptActivitySubscription struct {
	serverapi.PromptActivitySubscription
	once    sync.Once
	onClose func()
}

func (s *notifyingPromptActivitySubscription) Close() error {
	if s == nil {
		return nil
	}
	var err error
	if s.PromptActivitySubscription != nil {
		err = s.PromptActivitySubscription.Close()
	}
	s.once.Do(func() {
		if s.onClose != nil {
			s.onClose()
		}
	})
	return err
}
