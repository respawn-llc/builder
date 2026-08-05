package registry

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"core/server/attentionnotify"
	"core/server/runtime"
	"core/server/runtimeactivity"
	"core/server/runtimeops"
	"core/server/runtimeview"
	"core/server/sessionruntime"
	askquestion "core/server/tools"
	shelltool "core/server/tools/shell"
	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

type RuntimeRegistry struct {
	authorityMu                sync.RWMutex
	authorityBySession         map[string]*authorityRuntimeEntry
	authorityChanged           chan struct{}
	sleepObserverMu            sync.Mutex
	sleepObserver              func(active bool)
	runStateMu                 sync.Mutex
	blockingActivitySessions   map[string]bool
	operations                 *runtimeops.Coordinator
	readModels                 *runtimeactivity.CoordinatorCache
	pendingPrompts             *pendingPromptStore
	attentionBroker            *attentionnotify.Broker
	questionBatches            *attentionnotify.QuestionBatchTracker
	workflowEventPublisher     func(context.Context, serverapi.WorkflowProjectEvent) error
	workflowAttentionSnapshot  WorkflowAttentionNotificationSnapshotSource
	executionTargetResolver    func(context.Context, string) (*clientui.SessionExecutionTarget, error)
	backgroundProcessSnapshots func() []shelltool.Snapshot
}

type authorityRuntimeEntry struct {
	ref         runtimeids.SessionResourceRef
	engine      *runtime.Engine
	sessionFeed *sessionFeedSequencer
	retain      func() (io.Closer, error)

	mu             sync.Mutex
	lifecycle      authorityRuntimeEntryLifecycle
	feedReady      bool
	nextRetention  uint64
	retentions     map[uint64]io.Closer
	readModelUnpin func()
}

type authorityRuntimeEntryLifecycle uint8

const (
	authorityRuntimeEntryReady authorityRuntimeEntryLifecycle = iota
	authorityRuntimeEntryDraining
	authorityRuntimeEntryRetired
)

func NewRuntimeRegistry() *RuntimeRegistry {
	return &RuntimeRegistry{
		authorityBySession:       make(map[string]*authorityRuntimeEntry),
		authorityChanged:         make(chan struct{}),
		blockingActivitySessions: make(map[string]bool),
		readModels:               runtimeactivity.NewCoordinatorCache(runtimeactivity.DefaultCoordinatorCacheLimit),
		pendingPrompts:           newPendingPromptStore(),
	}
}

func (r *RuntimeRegistry) ResourceReady(
	_ context.Context,
	resource sessionruntime.AgentResourceDescriptor,
	engine *runtime.Engine,
	retain sessionruntime.AgentResourceRetainer,
) error {
	if r == nil {
		return errors.New("runtime registry is required")
	}
	ref := resource.Ref
	if err := ref.Validate(); err != nil {
		return err
	}
	if engine == nil {
		return errors.New("authority runtime engine is required")
	}
	if retain == nil {
		return errors.New("authority runtime retainer is required")
	}
	sessionID := ref.SessionID().String()
	entry := &authorityRuntimeEntry{
		ref:         ref,
		engine:      engine,
		sessionFeed: newSessionFeedSequencer(newTranscriptSubscriptionBroker()),
		retain:      retain,
		retentions:  make(map[uint64]io.Closer),
	}
	r.authorityMu.Lock()
	if existing := r.authorityBySession[sessionID]; existing != nil {
		r.authorityMu.Unlock()
		return fmt.Errorf(
			"authority runtime resource %s generation %d cannot replace registered generation %d",
			sessionID,
			ref.Generation(),
			existing.ref.Generation(),
		)
	}
	r.authorityBySession[sessionID] = entry
	r.authorityMu.Unlock()
	if r.readModels != nil {
		entry.readModelUnpin = r.readModels.Pin(sessionID)
	}
	if err := r.publishCurrentRuntimeActivity(sessionID); err != nil {
		r.authorityMu.Lock()
		if r.authorityBySession[sessionID] == entry {
			delete(r.authorityBySession, sessionID)
			r.signalAuthorityChangeLocked()
		}
		r.authorityMu.Unlock()
		if entry.readModelUnpin != nil {
			entry.readModelUnpin()
		}
		return fmt.Errorf("initialize authority runtime feed for session %s: %w", sessionID, err)
	}
	entry.mu.Lock()
	ready := entry.lifecycle == authorityRuntimeEntryReady
	if ready {
		entry.feedReady = true
	}
	entry.mu.Unlock()
	if ready {
		r.authorityMu.Lock()
		if r.authorityBySession[sessionID] == entry {
			r.signalAuthorityChangeLocked()
		}
		r.authorityMu.Unlock()
	}
	return nil
}

func (r *RuntimeRegistry) ResourceDraining(_ context.Context, resource sessionruntime.AgentResourceDescriptor) error {
	if r == nil {
		return nil
	}
	ref := resource.Ref
	if err := ref.Validate(); err != nil {
		return err
	}
	entry := r.authorityEntryByRef(ref)
	if entry == nil {
		return nil
	}
	entry.mu.Lock()
	if entry.lifecycle != authorityRuntimeEntryReady {
		entry.mu.Unlock()
		return nil
	}
	entry.lifecycle = authorityRuntimeEntryDraining
	retentions := entry.retentions
	entry.retentions = nil
	entry.mu.Unlock()

	sessionID := ref.SessionID().String()
	r.pendingPrompts.CloseSession(sessionID, func(snapshot PendingPromptSnapshot) {
		r.publishPromptResolution(entry, sessionID, snapshot)
	})
	_ = r.publishCurrentRuntimeActivity(sessionID)
	update, err := r.unavailableRuntimeReadModelFeedSnapshot(sessionID)
	entry.mu.Lock()
	lifecycle := entry.lifecycle
	if lifecycle != authorityRuntimeEntryDraining {
		entry.mu.Unlock()
		panic(fmt.Sprintf("authority runtime resource %s generation %d reached terminal feed closure from lifecycle %d", sessionID, ref.Generation(), lifecycle))
	}
	entry.lifecycle = authorityRuntimeEntryRetired
	entry.mu.Unlock()
	if err == nil {
		entry.sessionFeed.CloseWithRuntimeReadModel(update, io.EOF)
	} else {
		entry.sessionFeed.Close(io.EOF)
	}
	r.updateAggregateRuntimeActivityState(sessionID, false)
	var retentionErr error
	for _, retention := range retentions {
		retentionErr = errors.Join(retentionErr, retention.Close())
	}
	r.authorityMu.Lock()
	if r.authorityBySession[sessionID] == entry {
		delete(r.authorityBySession, sessionID)
		r.signalAuthorityChangeLocked()
	}
	r.authorityMu.Unlock()
	if entry.readModelUnpin != nil {
		entry.readModelUnpin()
	}
	return errors.Join(retentionErr, err)
}

func (r *RuntimeRegistry) authorityEntryBySession(sessionID string) *authorityRuntimeEntry {
	if r == nil {
		return nil
	}
	r.authorityMu.RLock()
	entry := r.authorityBySession[strings.TrimSpace(sessionID)]
	r.authorityMu.RUnlock()
	return entry
}

func (r *RuntimeRegistry) authorityEntryAndChange(sessionID string) (*authorityRuntimeEntry, <-chan struct{}) {
	r.authorityMu.Lock()
	defer r.authorityMu.Unlock()
	if r.authorityChanged == nil {
		r.authorityChanged = make(chan struct{})
	}
	return r.authorityBySession[strings.TrimSpace(sessionID)], r.authorityChanged
}

func (r *RuntimeRegistry) signalAuthorityChangeLocked() {
	if r.authorityChanged != nil {
		close(r.authorityChanged)
	}
	r.authorityChanged = make(chan struct{})
}

func (r *RuntimeRegistry) authorityEntryByRef(ref runtimeids.SessionResourceRef) *authorityRuntimeEntry {
	entry := r.authorityEntryBySession(ref.SessionID().String())
	if entry != nil && entry.ref != ref {
		return nil
	}
	return entry
}

func (r *RuntimeRegistry) withCurrentAuthorityEntry(ref runtimeids.SessionResourceRef, mutate func(*authorityRuntimeEntry) bool) bool {
	entry := r.authorityEntryByRef(ref)
	if entry == nil {
		return false
	}
	entry.mu.Lock()
	defer entry.mu.Unlock()
	return entry.lifecycle == authorityRuntimeEntryReady && entry.feedReady && mutate(entry)
}

func (e *authorityRuntimeEntry) retainSubscription() (uint64, error) {
	if e == nil || e.retain == nil {
		return 0, fmt.Errorf("authority runtime subscription is unavailable: %w", serverapi.ErrStreamUnavailable)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.lifecycle != authorityRuntimeEntryReady || !e.feedReady {
		return 0, fmt.Errorf("authority runtime subscription is not ready: %w", serverapi.ErrStreamUnavailable)
	}
	retention, err := e.retain()
	if err != nil {
		return 0, err
	}
	e.nextRetention++
	id := e.nextRetention
	if id == 0 {
		_ = retention.Close()
		panic("authority runtime subscription retention id overflow")
	}
	e.retentions[id] = retention
	return id, nil
}

func (e *authorityRuntimeEntry) releaseSubscription(id uint64) error {
	if e == nil || id == 0 {
		return nil
	}
	e.mu.Lock()
	retention := e.retentions[id]
	delete(e.retentions, id)
	e.mu.Unlock()
	if retention == nil {
		return nil
	}
	return retention.Close()
}

func (r *RuntimeRegistry) WithOperationCoordinator(coordinator *runtimeops.Coordinator) *RuntimeRegistry {
	if r == nil {
		return nil
	}
	r.operations = coordinator
	return r
}

func (r *RuntimeRegistry) WithExecutionTargetResolver(resolver func(context.Context, string) (*clientui.SessionExecutionTarget, error)) *RuntimeRegistry {
	if r == nil {
		return nil
	}
	r.executionTargetResolver = resolver
	return r
}

func (r *RuntimeRegistry) WithBackgroundProcessSnapshots(source func() []shelltool.Snapshot) *RuntimeRegistry {
	if r == nil {
		return nil
	}
	r.backgroundProcessSnapshots = source
	return r
}

func (r *RuntimeRegistry) WithTranscriptContractViolationPanic(enabled bool) *RuntimeRegistry {
	if r == nil {
		return nil
	}
	transcriptContractViolationsPanic = enabled
	return r
}

func (r *RuntimeRegistry) RuntimeActivity(sessionID string) (clientui.RuntimeActivity, error) {
	id := strings.TrimSpace(sessionID)
	if r == nil || id == "" {
		return clientui.RuntimeActivity{State: clientui.RuntimeActivityUnavailable}, nil
	}
	snapshot, err := r.RuntimeReadModelSnapshot(context.Background(), id, nil)
	if err != nil {
		return clientui.RuntimeActivity{}, err
	}
	return snapshot.Activity, nil
}

func (r *RuntimeRegistry) RuntimeReadModelSnapshot(ctx context.Context, sessionID string, refs []clientui.RuntimeOperationRef) (runtimeactivity.ResponseSnapshot, error) {
	update, err := r.runtimeReadModelFeedSnapshot(ctx, sessionID, refs)
	if err != nil {
		return runtimeactivity.ResponseSnapshot{}, err
	}
	return runtimeactivity.ResponseSnapshot{
		Version:             update.Version,
		Activity:            update.Activity,
		InputReconciliation: update.InputReconciliation,
	}, nil
}

func (r *RuntimeRegistry) RuntimeReadModelFeedSnapshot(ctx context.Context, sessionID string, refs []clientui.RuntimeOperationRef) (clientui.RuntimeReadModelUpdate, error) {
	return r.runtimeReadModelFeedSnapshot(ctx, sessionID, refs)
}

func (r *RuntimeRegistry) runtimeReadModelFeedSnapshot(ctx context.Context, sessionID string, refs []clientui.RuntimeOperationRef) (clientui.RuntimeReadModelUpdate, error) {
	id := strings.TrimSpace(sessionID)
	if r == nil || id == "" {
		return runtimeactivity.BuildFeedSnapshot(id, func() (runtimeactivity.SnapshotInput, error) {
			return runtimeactivity.SnapshotInput{
				Resolver:            runtimeactivity.ResolverSnapshot{},
				InputReconciliation: clientui.RuntimeInputReconciliationSnapshot{},
			}, nil
		})
	}
	return r.readModelFeedSnapshot(id, func() (runtimeactivity.SnapshotInput, error) {
		resolver, err := r.runtimeActivityResolverSnapshot(ctx, id)
		if err != nil {
			return runtimeactivity.SnapshotInput{}, err
		}
		reconciliation := clientui.RuntimeInputReconciliationSnapshot{}
		if r.operations != nil {
			reconciliation, err = r.operations.FeedSnapshot(id, refs)
			if err != nil {
				return runtimeactivity.SnapshotInput{}, err
			}
		}
		return runtimeactivity.SnapshotInput{
			Resolver:            resolver,
			InputReconciliation: reconciliation,
		}, nil
	})
}

func (r *RuntimeRegistry) readModelFeedSnapshot(sessionID string, build runtimeactivity.SnapshotBuilder) (clientui.RuntimeReadModelUpdate, error) {
	if r == nil || r.readModels == nil {
		return runtimeactivity.BuildFeedSnapshot(sessionID, build)
	}
	return r.readModels.WithFeedSnapshot(sessionID, build)
}

func (r *RuntimeRegistry) unavailableRuntimeReadModelFeedSnapshot(sessionID string) (clientui.RuntimeReadModelUpdate, error) {
	id := strings.TrimSpace(sessionID)
	return r.readModelFeedSnapshot(id, func() (runtimeactivity.SnapshotInput, error) {
		return runtimeactivity.SnapshotInput{
			Resolver:            runtimeactivity.ResolverSnapshot{},
			InputReconciliation: clientui.RuntimeInputReconciliationSnapshot{},
		}, nil
	})
}

func (r *RuntimeRegistry) runtimeActivityResolverSnapshot(ctx context.Context, sessionID string) (runtimeactivity.ResolverSnapshot, error) {
	id := strings.TrimSpace(sessionID)
	if r == nil || id == "" {
		return runtimeactivity.ResolverSnapshot{}, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var engine *runtime.Engine
	if entry := r.authorityEntryBySession(id); entry != nil {
		engine = entry.engine
	}
	snapshot := runtimeactivity.ResolverSnapshot{Registry: r.RuntimeActivityRegistrySnapshot(id)}
	snapshot.Active = runtimeactivity.ActiveStepFromProvider(engine)
	if engine != nil {
		snapshot.LiveRunActive = engine.HasActiveLiveRunGroup()
	}
	if len(r.pendingPrompts.List(id)) > 0 {
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
	if authorityEntry := r.authorityEntryBySession(id); authorityEntry != nil {
		authorityEntry.mu.Lock()
		lifecycle := authorityEntry.lifecycle
		authorityEntry.mu.Unlock()
		switch lifecycle {
		case authorityRuntimeEntryReady:
			return runtimeactivity.RegistrySnapshot{
				Registered:     true,
				QueueAccepting: true,
			}
		case authorityRuntimeEntryDraining:
			return runtimeactivity.RegistrySnapshot{
				Registered: true,
				Draining:   true,
			}
		case authorityRuntimeEntryRetired:
			return runtimeactivity.RegistrySnapshot{}
		default:
			panic(fmt.Sprintf("authority runtime resource for session %q has unknown registry lifecycle %d", id, lifecycle))
		}
	}
	return runtimeactivity.RegistrySnapshot{}
}

func (r *RuntimeRegistry) publishCurrentRuntimeActivity(sessionID string) error {
	if r == nil {
		return nil
	}
	id := strings.TrimSpace(sessionID)
	update, err := r.runtimeReadModelFeedSnapshot(context.Background(), id, nil)
	if err != nil {
		return err
	}
	r.PublishRuntimeReadModelUpdate(id, update)
	return nil
}

func (r *RuntimeRegistry) PublishRuntimeEventToAll(evt runtime.Event) error {
	if r == nil {
		return nil
	}
	r.authorityMu.RLock()
	authorityEntries := make([]*authorityRuntimeEntry, 0, len(r.authorityBySession))
	for _, entry := range r.authorityBySession {
		authorityEntries = append(authorityEntries, entry)
	}
	r.authorityMu.RUnlock()
	for _, entry := range authorityEntries {
		if err := r.publishRuntimeEvent(entry, evt); err != nil {
			return err
		}
	}
	return nil
}

func (r *RuntimeRegistry) PublishAuthorityRuntimeEvent(ref runtimeids.SessionResourceRef, evt runtime.Event) error {
	if r == nil {
		return nil
	}
	entry := r.authorityEntryByRef(ref)
	if entry == nil {
		return nil
	}
	return r.publishRuntimeEvent(entry, evt)
}

func (r *RuntimeRegistry) publishRuntimeEvent(entry *authorityRuntimeEntry, evt runtime.Event) error {
	if !transcriptEventRequiresVisibleSubscriber(evt) || entry.sessionFeed.HasSubscribers() {
		messages, err := runtimeview.TranscriptMessagesFromRuntimeEventChecked(evt)
		if err != nil {
			contractErr := entry.sessionFeed.CloseContractViolation(fmt.Errorf("project runtime transcript event: %w", err))
			return contractErr
		}
		entry.sessionFeed.Publish(messages)
	}
	if runtimeEventShouldPublishSessionStatus(evt) {
		if err := entry.sessionFeed.PublishBuilt(func() ([]clientui.TranscriptEvent, error) {
			status, err := runtimeview.TranscriptSessionStatusFromRuntime(entry.engine)
			if err != nil {
				return nil, err
			}
			return []clientui.TranscriptEvent{clientui.NewTranscriptEvent(status)}, nil
		}); err != nil {
			return err
		}
	}
	r.recordQueuedMessageOperationStatus(evt)
	return nil
}

func (r *RuntimeRegistry) PublishSessionIdentity(sessionID string) error {
	if r == nil {
		return nil
	}
	id := strings.TrimSpace(sessionID)
	entry := r.authorityEntryBySession(id)
	if entry == nil {
		return nil
	}
	return entry.sessionFeed.PublishBuilt(func() ([]clientui.TranscriptEvent, error) {
		identity, err := runtimeview.TranscriptSessionIdentityFromRuntime(entry.engine)
		if err != nil {
			return nil, err
		}
		target, err := r.resolveSessionExecutionTarget(context.Background(), id)
		if err != nil {
			return nil, err
		}
		identity.ExecutionTarget = target
		return []clientui.TranscriptEvent{clientui.NewTranscriptEvent(identity)}, nil
	})
}

func (r *RuntimeRegistry) PublishSessionStatus(sessionID string) error {
	if r == nil {
		return nil
	}
	entry := r.authorityEntryBySession(sessionID)
	if entry == nil {
		return nil
	}
	return entry.sessionFeed.PublishBuilt(func() ([]clientui.TranscriptEvent, error) {
		status, err := runtimeview.TranscriptSessionStatusFromRuntime(entry.engine)
		if err != nil {
			return nil, err
		}
		return []clientui.TranscriptEvent{clientui.NewTranscriptEvent(status)}, nil
	})
}

func (r *RuntimeRegistry) resolveSessionExecutionTarget(ctx context.Context, sessionID string) (*clientui.SessionExecutionTarget, error) {
	if r == nil || r.executionTargetResolver == nil {
		return nil, nil
	}
	target, err := r.executionTargetResolver(ctx, strings.TrimSpace(sessionID))
	if err != nil {
		return nil, fmt.Errorf("resolve execution target for session %q: %w", strings.TrimSpace(sessionID), err)
	}
	if target == nil {
		return nil, nil
	}
	normalized := clientui.NormalizeSessionExecutionTarget(*target)
	if clientui.SessionExecutionTargetIsZero(normalized) {
		return nil, fmt.Errorf("resolve execution target for session %q returned an empty target", strings.TrimSpace(sessionID))
	}
	return &normalized, nil
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
	clientRequestID, err := runtimeids.ParseRuntimeClientRequestID(status.ClientRequestID)
	if err != nil {
		panic(fmt.Sprintf("record queued-message runtime status with invalid client request id for session %q queue item %q: %v", strings.TrimSpace(status.SessionID), strings.TrimSpace(status.QueueItemID), err))
	}
	queueItemID, err := runtimeids.ParseQueueItemID(status.QueueItemID)
	if err != nil {
		panic(fmt.Sprintf("record queued-message runtime status with invalid queue item id for session %q client request %q: %v", strings.TrimSpace(status.SessionID), strings.TrimSpace(status.ClientRequestID), err))
	}
	ref := clientui.RuntimeOperationRef{
		Kind:            clientui.RuntimeOperationKindQueuedMessage,
		ClientRequestID: clientRequestID,
		QueueItemID:     &queueItemID,
	}
	var state clientui.RuntimeInputReconciliationState
	switch status.Status {
	case runtime.QueuedUserMessageAccepted:
		state = clientui.RuntimeInputReconciliationAccepted
	case runtime.QueuedUserMessageSubmitted:
		state = clientui.RuntimeInputReconciliationSubmitted
	case runtime.QueuedUserMessageFailed:
		state = clientui.RuntimeInputReconciliationFailedWithRestore
	case runtime.QueuedUserMessageDiscarded:
		state = clientui.RuntimeInputReconciliationCanceledNotCommitted
	default:
		return
	}
	recordErr := r.operations.RecordQueuedMessageStatus(status.SessionID, ref, state)
	if recordErr != nil {
		panic(fmt.Sprintf("record queued-message runtime status for session %q client request %q queue item %q: %v", strings.TrimSpace(status.SessionID), clientRequestID.String(), queueItemID.String(), recordErr))
	}
}

func (r *RuntimeRegistry) PublishRuntimeReadModelUpdate(sessionID string, update clientui.RuntimeReadModelUpdate) {
	if r == nil {
		return
	}
	if authorityEntry := r.authorityEntryBySession(sessionID); authorityEntry != nil {
		authorityEntry.sessionFeed.PublishRuntimeReadModel(update)
		r.updateAggregateRuntimeActivityForAuthority(sessionID, authorityEntry, update.Activity.ActiveForControl())
	}
}

func (r *RuntimeRegistry) PublishWorktreeTransitionOutcome(sessionID string, outcome clientui.WorktreeTransitionOutcome) {
	if r == nil {
		return
	}
	if err := outcome.Validate(); err != nil {
		panic(fmt.Sprintf("publish invalid worktree transition outcome for session %q: %v", strings.TrimSpace(sessionID), err))
	}
	entry := r.authorityEntryBySession(sessionID)
	if entry == nil {
		return
	}
	transcriptOutcome := clientui.TranscriptWorktreeTransitionOutcome{
		OperationID: outcome.OperationID,
		Transition:  outcome.Transition,
		State:       outcome.State,
	}
	if outcome.Failure != nil {
		transcriptOutcome.Failure = &clientui.TranscriptDiagnostic{
			Code:   clientui.TranscriptDiagnosticCode("worktree_transition_failed"),
			Detail: outcome.Failure.Diagnostic,
		}
		if outcome.Failure.DeletePrecondition != nil {
			dirtyState := *outcome.Failure.DeletePrecondition
			transcriptOutcome.DeletePrecondition = &dirtyState
		}
	}
	entry.sessionFeed.Publish([]clientui.TranscriptEvent{clientui.NewTranscriptEvent(transcriptOutcome)})
}

func (r *RuntimeRegistry) SubscribeSessionTranscript(ctx context.Context, req serverapi.TranscriptSubscribeRequest) (serverapi.TranscriptSubscription, error) {
	if r == nil {
		return nil, fmt.Errorf("runtime registry is required")
	}
	if err := req.Validate(); err != nil {
		return nil, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	id := strings.TrimSpace(req.SessionID)
	for {
		authorityEntry, authorityChanged := r.authorityEntryAndChange(id)
		if authorityEntry != nil {
			subscription, err := r.subscribeAuthorityTranscript(ctx, id, authorityEntry)
			if err == nil || !errors.Is(err, serverapi.ErrStreamUnavailable) {
				return subscription, err
			}
		}
		select {
		case <-ctx.Done():
			return nil, context.Cause(ctx)
		case <-authorityChanged:
		}
	}
}

func (r *RuntimeRegistry) subscribeAuthorityTranscript(ctx context.Context, id string, entry *authorityRuntimeEntry) (serverapi.TranscriptSubscription, error) {
	retentionID, err := entry.retainSubscription()
	if err != nil {
		return nil, err
	}
	releaseRetention := func() {
		_ = entry.releaseSubscription(retentionID)
	}
	var sub *transcriptSubscription
	err = entry.engine.WithTranscriptHydrationSnapshot(func(snapshot runtime.TranscriptHydrationSnapshot) error {
		var subscribeErr error
		sub, subscribeErr = entry.sessionFeed.Subscribe(func() (clientui.TranscriptHydration, error) {
			return r.composeTranscriptHydration(ctx, id, entry, snapshot)
		})
		return subscribeErr
	})
	if err != nil {
		releaseRetention()
		return nil, err
	}
	return &notifyingSessionTranscriptSubscription{TranscriptSubscription: sub, onClose: func() {
		releaseRetention()
	}}, nil
}

func (r *RuntimeRegistry) PromptPendingScope(scope sessionruntime.ExecutionScope, req askquestion.AskQuestionRequest, createdAt time.Time) error {
	if r == nil {
		return nil
	}
	resource, ok := scope.Resource()
	if !ok {
		panic(fmt.Sprintf("workflow prompt scope %s has no session resource", scope.ID()))
	}
	id := resource.SessionID().String()
	entry := r.authorityEntryByRef(resource)
	var snapshot PendingPromptSnapshot
	projected := r.withCurrentAuthorityEntry(resource, func(_ *authorityRuntimeEntry) bool {
		var admitted bool
		snapshot, admitted = r.pendingPrompts.Begin(id, resource, scope.ID(), req, createdAt)
		return admitted
	})
	if projected && entry != nil {
		publishPendingPrompt(entry.sessionFeed, id, snapshot, pendingPromptEventPending)
		r.publishAttentionPending(id, snapshot)
		if err := r.publishTaskQuestionWaitingForScope(scope, snapshot); err != nil {
			return err
		}
		r.publishCurrentRuntimeActivity(id)
	}
	return nil
}

func (r *RuntimeRegistry) PromptResolvedScope(scope sessionruntime.ExecutionScope, requestID string) error {
	if r == nil {
		return nil
	}
	resource, ok := scope.Resource()
	if !ok {
		panic(fmt.Sprintf("workflow prompt scope %s has no session resource", scope.ID()))
	}
	id := resource.SessionID().String()
	var snapshot PendingPromptSnapshot
	entry := r.authorityEntryByRef(resource)
	resolved := r.withCurrentAuthorityEntry(resource, func(_ *authorityRuntimeEntry) bool {
		var ok bool
		snapshot, ok = r.pendingPrompts.Complete(id, resource, scope.ID(), requestID)
		return ok
	})
	if resolved {
		if entry != nil {
			publishPendingPrompt(entry.sessionFeed, id, snapshot, pendingPromptEventResolved)
		}
		r.publishAttentionResolved(id, snapshot)
		r.publishCurrentRuntimeActivity(id)
	}
	return nil
}

func (r *RuntimeRegistry) publishPromptResolution(entry *authorityRuntimeEntry, sessionID string, snapshot PendingPromptSnapshot) {
	if r == nil || entry == nil {
		return
	}
	publishPendingPrompt(entry.sessionFeed, sessionID, snapshot, pendingPromptEventResolved)
	r.publishAttentionResolved(sessionID, snapshot)
}

func (r *RuntimeRegistry) ListPendingPrompts(sessionID string) []PendingPromptSnapshot {
	return r.pendingPrompts.List(sessionID)
}

func (r *RuntimeRegistry) SetSleepObserver(observer func(active bool)) {
	if r == nil {
		return
	}
	r.sleepObserverMu.Lock()
	r.sleepObserver = observer
	r.sleepObserverMu.Unlock()
}

func (r *RuntimeRegistry) updateAggregateRuntimeActivityForAuthority(sessionID string, entry *authorityRuntimeEntry, activeForControl bool) bool {
	if r == nil || entry == nil {
		return false
	}
	id := strings.TrimSpace(sessionID)
	if id == "" {
		return false
	}
	entry.mu.Lock()
	lifecycle := entry.lifecycle
	entry.mu.Unlock()
	if lifecycle == authorityRuntimeEntryRetired && activeForControl {
		return false
	}
	r.authorityMu.RLock()
	current := r.authorityBySession[id]
	if current != entry && (activeForControl || current != nil) {
		r.authorityMu.RUnlock()
		return false
	}
	r.updateAggregateRuntimeActivityState(id, activeForControl)
	r.authorityMu.RUnlock()
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

type notifyingSessionTranscriptSubscription struct {
	serverapi.TranscriptSubscription
	once    sync.Once
	onClose func()
}

func (s *notifyingSessionTranscriptSubscription) Close() error {
	if s == nil {
		return nil
	}
	var err error
	if s.TranscriptSubscription != nil {
		err = s.TranscriptSubscription.Close()
	}
	s.once.Do(func() {
		if s.onClose != nil {
			s.onClose()
		}
	})
	return err
}
