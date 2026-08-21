package registry

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"core/server/attentionnotify"
	"core/server/runtime"
	"core/server/runtimeactivity"
	"core/server/runtimeview"
	"core/server/sessionruntime"
	askquestion "core/server/tools"
	shelltool "core/server/tools/shell"
	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

type RuntimeRegistry struct {
	authorityMu                sync.Mutex
	authorityBySession         sync.Map
	authorityChanged           chan struct{}
	sleepObserverMu            sync.Mutex
	sleepObserver              func(active bool)
	runStateMu                 sync.Mutex
	blockingActivitySessions   map[string]bool
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
	mainView    atomic.Pointer[clientui.RuntimeMainView]

	publicationMu sync.Mutex
	mu            sync.Mutex
	lifecycle     authorityRuntimeEntryLifecycle
	feedReady     bool
	nextRetention uint64
	retentions    map[uint64]io.Closer
}

type authorityRuntimeEntryLifecycle uint8

const (
	authorityRuntimeEntryReady authorityRuntimeEntryLifecycle = iota
	authorityRuntimeEntryDraining
	authorityRuntimeEntryRetired
)

func NewRuntimeRegistry() *RuntimeRegistry {
	return &RuntimeRegistry{
		authorityChanged:         make(chan struct{}),
		blockingActivitySessions: make(map[string]bool),
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
	if existing := r.authorityEntryBySession(sessionID); existing != nil {
		r.authorityMu.Unlock()
		return fmt.Errorf(
			"authority runtime resource %s generation %d cannot replace registered generation %d",
			sessionID,
			ref.Generation(),
			existing.ref.Generation(),
		)
	}
	r.authorityBySession.Store(sessionID, entry)
	r.authorityMu.Unlock()
	if err := r.publishCurrentRuntimeActivity(sessionID); err != nil {
		r.authorityMu.Lock()
		if r.authorityEntryBySession(sessionID) == entry {
			r.authorityBySession.Delete(sessionID)
			r.signalAuthorityChangeLocked()
		}
		r.authorityMu.Unlock()
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
		if r.authorityEntryBySession(sessionID) == entry {
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
	entry.publicationMu.Lock()
	entry.mu.Lock()
	if entry.lifecycle != authorityRuntimeEntryReady {
		entry.mu.Unlock()
		entry.publicationMu.Unlock()
		return nil
	}
	entry.lifecycle = authorityRuntimeEntryDraining
	retentions := entry.retentions
	entry.retentions = nil
	entry.mu.Unlock()
	entry.mainView.Store(nil)
	entry.publicationMu.Unlock()

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
	if r.authorityEntryBySession(sessionID) == entry {
		r.authorityBySession.Delete(sessionID)
		r.signalAuthorityChangeLocked()
	}
	r.authorityMu.Unlock()
	return errors.Join(retentionErr, err)
}

func (r *RuntimeRegistry) authorityEntryBySession(sessionID string) *authorityRuntimeEntry {
	if r == nil {
		return nil
	}
	value, ok := r.authorityBySession.Load(strings.TrimSpace(sessionID))
	if !ok {
		return nil
	}
	entry, ok := value.(*authorityRuntimeEntry)
	if !ok {
		panic("authority Runtime index contains an invalid entry")
	}
	return entry
}

func (r *RuntimeRegistry) authorityEntryAndChange(sessionID string) (*authorityRuntimeEntry, <-chan struct{}) {
	r.authorityMu.Lock()
	defer r.authorityMu.Unlock()
	if r.authorityChanged == nil {
		r.authorityChanged = make(chan struct{})
	}
	return r.authorityEntryBySession(sessionID), r.authorityChanged
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

func (r *RuntimeRegistry) ActiveRuntimeActivitySnapshots(context.Context) ([]runtimeactivity.ActiveSessionSnapshot, error) {
	if r == nil {
		return []runtimeactivity.ActiveSessionSnapshot{}, nil
	}
	snapshots := make([]runtimeactivity.ActiveSessionSnapshot, 0)
	r.authorityBySession.Range(func(_, value any) bool {
		entry, ok := value.(*authorityRuntimeEntry)
		if !ok {
			panic("Runtime Main View index contains an invalid entry")
		}
		view := entry.mainView.Load()
		if view == nil || !view.Activity.ActiveForControl() {
			return true
		}
		snapshots = append(snapshots, runtimeactivity.ActiveSessionSnapshot{
			SessionID: entry.ref.SessionID().String(),
			Activity:  cloneRuntimeActivity(view.Activity),
		})
		return true
	})
	sort.Slice(snapshots, func(i, j int) bool {
		return snapshots[i].SessionID < snapshots[j].SessionID
	})
	return snapshots, nil
}

func (r *RuntimeRegistry) RuntimeReadModelFeedSnapshot(_ context.Context, sessionID string) (clientui.RuntimeReadModelUpdate, error) {
	id := strings.TrimSpace(sessionID)
	if r == nil || id == "" {
		return runtimeactivity.BuildFeedSnapshot(
			runtimeactivity.NextReadModelVersion(id),
			runtimeactivity.ResolverSnapshot{},
		)
	}
	resolver := r.runtimeActivityResolverSnapshot(id)
	return runtimeactivity.BuildFeedSnapshot(r.readModelVersion(id), resolver)
}

func (r *RuntimeRegistry) readModelVersion(sessionID string) clientui.ReadModelVersion {
	return runtimeactivity.NextReadModelVersion(sessionID)
}

func (r *RuntimeRegistry) unavailableRuntimeReadModelFeedSnapshot(sessionID string) (clientui.RuntimeReadModelUpdate, error) {
	id := strings.TrimSpace(sessionID)
	return runtimeactivity.BuildFeedSnapshot(r.readModelVersion(id), runtimeactivity.ResolverSnapshot{})
}

func (r *RuntimeRegistry) runtimeActivityResolverSnapshot(sessionID string) runtimeactivity.ResolverSnapshot {
	id := strings.TrimSpace(sessionID)
	if r == nil || id == "" {
		return runtimeactivity.ResolverSnapshot{}
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
	return snapshot
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
	update, err := r.RuntimeReadModelFeedSnapshot(context.Background(), id)
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
	authorityEntries := make([]*authorityRuntimeEntry, 0)
	r.authorityBySession.Range(func(_, value any) bool {
		entry, ok := value.(*authorityRuntimeEntry)
		if !ok {
			panic("authority Runtime index contains an invalid entry")
		}
		authorityEntries = append(authorityEntries, entry)
		return true
	})
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
		return r.publishTranscriptAndMainView(entry, func() ([]clientui.TranscriptEvent, error) {
			status, err := runtimeview.TranscriptSessionStatusFromRuntime(entry.engine)
			if err != nil {
				return nil, err
			}
			return []clientui.TranscriptEvent{clientui.NewTranscriptEvent(status)}, nil
		})
	}
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
	return r.publishTranscriptAndMainView(entry, func() ([]clientui.TranscriptEvent, error) {
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
	return r.publishTranscriptAndMainView(entry, func() ([]clientui.TranscriptEvent, error) {
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

func (r *RuntimeRegistry) PublishRuntimeReadModelUpdate(sessionID string, update clientui.RuntimeReadModelUpdate) {
	if r == nil {
		return
	}
	entry := r.authorityEntryBySession(sessionID)
	if entry == nil {
		return
	}
	if err := update.Validate(); err != nil {
		panic(fmt.Sprintf("publish invalid canonical runtime read-model update: %+v: %v", update, err))
	}
	entry.publicationMu.Lock()
	defer entry.publicationMu.Unlock()
	current := entry.mainView.Load()
	if current != nil &&
		(current.Version == update.Version || current.Version.NewerThan(update.Version)) {
		return
	}
	publicationErr := r.publishRuntimeMainViewLocked(entry, update.Version, update.Activity)
	if publicationErr != nil {
		log.Printf("publish Runtime Main View for Session %q: %v", strings.TrimSpace(sessionID), publicationErr)
	}
	entry.sessionFeed.PublishRuntimeReadModel(update)
	r.updateAggregateRuntimeActivityForAuthority(sessionID, entry, update.Activity.ActiveForControl())
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
		wakeErr := r.publishTaskQuestionWaitingForScope(scope, snapshot)
		r.publishCurrentRuntimeActivity(id)
		if wakeErr != nil {
			return wakeErr
		}
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
		return r.publishTaskQuestionCleared(id, snapshot)
	}
	return nil
}

func (r *RuntimeRegistry) publishPromptResolution(entry *authorityRuntimeEntry, sessionID string, snapshot PendingPromptSnapshot) {
	if r == nil || entry == nil {
		return
	}
	publishPendingPrompt(entry.sessionFeed, sessionID, snapshot, pendingPromptEventResolved)
	r.publishAttentionResolved(sessionID, snapshot)
	if err := r.publishTaskQuestionCleared(sessionID, snapshot); err != nil {
		logAttentionNotificationOperationFailure(
			"publish workflow prompt resolution event",
			sessionID,
			snapshot.Request.ID,
			err,
		)
	}
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
	r.authorityMu.Lock()
	current := r.authorityEntryBySession(id)
	if current != entry && (activeForControl || current != nil) {
		r.authorityMu.Unlock()
		return false
	}
	r.updateAggregateRuntimeActivityState(id, activeForControl)
	r.authorityMu.Unlock()
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
