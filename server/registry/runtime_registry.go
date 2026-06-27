package registry

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"

	"core/server/runtime"
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
	directory       *runtimeDirectory
	observerMu      sync.Mutex
	observer        func(sessionID string, reason RuntimeInterestReason)
	sleepObserverMu sync.Mutex
	sleepObserver   func(active bool)
	runStateMu      sync.Mutex
	runningSessions map[string]bool
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
	return &RuntimeRegistry{
		directory:       newRuntimeDirectory(),
		runningSessions: make(map[string]bool),
	}
}

func (r *RuntimeRegistry) Register(sessionID string, engine *runtime.Engine) {
	r.RegisterRuntimeHooks(sessionID, engine, nil)
}

func (r *RuntimeRegistry) RegisterRuntimeHooks(sessionID string, engine *runtime.Engine, rebind func(string) error) {
	if r == nil || engine == nil {
		return
	}
	previous := r.directory.Register(sessionID, engine, rebind, func(previous *runtimeEntry) {
		publishExternalRuntimeStatusToEntry(previous, clientui.ExternalRuntimeStatus{State: clientui.ExternalRuntimeStateClosing, QueueAccepting: false})
		failRuntimeEntryQueuedMessages(previous, runtime.QueuedUserMessageFailureClosing)
	})
	closeRuntimeEntry(previous, io.EOF)
	if previous != nil {
		r.updateAggregateRunState(sessionID, false)
	}
}

func (r *RuntimeRegistry) Unregister(sessionID string, engine *runtime.Engine) {
	if r == nil {
		return
	}
	id, entry := r.directory.Unregister(sessionID, engine)
	if id == "" {
		return
	}
	publishExternalRuntimeStatusToEntry(entry, clientui.ExternalRuntimeStatus{State: clientui.ExternalRuntimeStateClosing, QueueAccepting: false})
	publishExternalRuntimeStatusToEntry(entry, clientui.ExternalRuntimeStatus{})
	closeRuntimeEntry(entry, io.EOF)
	r.updateAggregateRunState(id, false)
}

func (r *RuntimeRegistry) CloseRuntimeWithDrain(ctx context.Context, sessionID string, engine *runtime.Engine, drain func(context.Context) error) error {
	if r == nil {
		return nil
	}
	id, entry, drainRef := r.directory.BeginClose(sessionID, engine)
	if id == "" || entry == nil || drainRef == nil {
		return nil
	}
	publishExternalRuntimeStatusToEntry(entry, clientui.ExternalRuntimeStatus{State: clientui.ExternalRuntimeStateDraining, QueueAccepting: false})
	drainRef.WaitForGuards()
	var drainErr error
	if drain != nil {
		drainErr = drain(ctx)
	}
	removedID, removedEntry := r.directory.RemoveClosing(id, engine, entry)
	if removedID == "" || removedEntry == nil {
		drainRef.Release()
		return drainErr
	}
	publishExternalRuntimeStatusToEntry(removedEntry, clientui.ExternalRuntimeStatus{})
	drainRef.Release()
	closeRuntimeEntry(removedEntry, io.EOF)
	r.updateAggregateRunState(removedID, false)
	return drainErr
}

type RuntimeGuard interface {
	Engine() *runtime.Engine
	Generation() uint64
	Rebind(workdir string) error
	GuardedPromptResponder
	Release()
}

func (r *RuntimeRegistry) BeginRuntimeGuard(ctx context.Context, sessionID string) (RuntimeGuard, error) {
	if r == nil {
		return nil, fmt.Errorf("runtime registry is required")
	}
	return r.directory.BeginGuard(ctx, sessionID)
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
		return false, nil
	}
	defer guard.Release()
	return true, fn(guard.Engine())
}

func (r *RuntimeRegistry) IsSessionRuntimeActive(sessionID string) bool {
	if r == nil {
		return false
	}
	return r.directory.Active(sessionID)
}

func (r *RuntimeRegistry) ExternalRuntimeStatus(sessionID string) clientui.ExternalRuntimeStatus {
	if r == nil {
		return clientui.ExternalRuntimeStatus{}
	}
	id := strings.TrimSpace(sessionID)
	if id == "" {
		return clientui.ExternalRuntimeStatus{}
	}
	entry := r.directory.Entry(id)
	if entry == nil {
		return clientui.ExternalRuntimeStatus{}
	}
	return r.externalRuntimeStatusForEntry(id, entry)
}

func (r *RuntimeRegistry) externalRuntimeStatusForEntry(sessionID string, entry *runtimeEntry) clientui.ExternalRuntimeStatus {
	if r == nil || entry == nil {
		return clientui.ExternalRuntimeStatus{}
	}
	closing, draining := entry.closeState()
	if closing {
		state := clientui.ExternalRuntimeStateClosing
		if draining {
			state = clientui.ExternalRuntimeStateDraining
		}
		return clientui.ExternalRuntimeStatus{
			State:          state,
			QueueAccepting: false,
		}
	}
	if r.sessionRunning(sessionID) {
		return clientui.ExternalRuntimeStatus{
			State:          clientui.ExternalRuntimeStateOwnerRunning,
			QueueAccepting: true,
		}
	}
	return clientui.ExternalRuntimeStatus{
		State:          clientui.ExternalRuntimeStateRegisteredIdle,
		QueueAccepting: true,
	}
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
	if entry == nil || entry.sessionActivity == nil {
		return
	}
	entry.sessionActivity.Publish(runtimeview.EventFromRuntime(evt))
	if evt.RunState != nil {
		reason := RuntimeInterestChanged
		if evt.RunState.Lifecycle.Phase == runtime.RunLifecycleFinished {
			reason = RuntimeInterestRunFinished
		}
		r.notifyInterestChanged(sessionID, reason)
	}
	if evt.Kind == runtime.EventRunStateChanged && evt.RunState != nil {
		r.updateAggregateRunState(sessionID, evt.RunState.Lifecycle.IsRunning())
		r.publishExternalRuntimeStatus(sessionID)
	}
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
	if req.AfterSequence == 0 {
		sub, err := entry.SubscribePromptActivityInitial(id, nil)
		if err != nil {
			return nil, err
		}
		r.notifyInterestChanged(id, RuntimeInterestChanged)
		return &notifyingPromptActivitySubscription{PromptActivitySubscription: sub, onClose: func() {
			r.notifyInterestChanged(id, RuntimeInterestChanged)
		}}, nil
	}
	sub, err := entry.promptActivity.Subscribe(nil, req.AfterSequence)
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
	snapshot, ok := entry.pendingPrompts.Begin(req)
	if !ok {
		return
	}
	entry.PublishPendingPrompt(id, snapshot, pendingPromptEventPending)
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
		entry.PublishPendingPrompt(id, snapshot, pendingPromptEventResolved)
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
		entry.PublishPendingPrompt(id, snapshot, eventType)
	})
}

func (r *RuntimeRegistry) SubmitPromptResponse(sessionID string, resp askquestion.AskQuestionResponse, err error) error {
	if r == nil {
		return fmt.Errorf("runtime registry is required")
	}
	id := strings.TrimSpace(sessionID)
	entry := r.directory.Entry(id)
	if entry == nil {
		return fmt.Errorf("runtime %q is unavailable", id)
	}
	return entry.pendingPrompts.Submit(resp, err, func(snapshot PendingPromptSnapshot, eventType pendingPromptEventType) {
		entry.PublishPendingPrompt(id, snapshot, eventType)
	})
}

func (r *RuntimeRegistry) sessionRunning(sessionID string) bool {
	if r == nil {
		return false
	}
	id := strings.TrimSpace(sessionID)
	if id == "" {
		return false
	}
	r.runStateMu.Lock()
	defer r.runStateMu.Unlock()
	return r.runningSessions[id]
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
	return entry.sessionActivity.SubscriberCount() > 0 || entry.promptActivity.SubscriberCount() > 0
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

func (r *RuntimeRegistry) updateAggregateRunState(sessionID string, running bool) {
	if r == nil {
		return
	}
	id := strings.TrimSpace(sessionID)
	if id == "" {
		return
	}
	r.runStateMu.Lock()
	wasActive := len(r.runningSessions) > 0
	if running {
		r.runningSessions[id] = true
	} else {
		delete(r.runningSessions, id)
	}
	active := len(r.runningSessions) > 0
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

func failRuntimeEntryQueuedMessages(entry *runtimeEntry, reason runtime.QueuedUserMessageFailureReason) {
	if entry == nil || entry.engine == nil {
		return
	}
	entry.engine.FailQueuedUserMessages(reason)
}

func (r *RuntimeRegistry) publishExternalRuntimeStatus(sessionID string) {
	if r == nil {
		return
	}
	id := strings.TrimSpace(sessionID)
	if id == "" {
		return
	}
	entry := r.directory.Entry(id)
	if entry == nil {
		return
	}
	publishExternalRuntimeStatusToEntry(entry, r.externalRuntimeStatusForEntry(id, entry))
}

func publishExternalRuntimeStatusToEntry(entry *runtimeEntry, status clientui.ExternalRuntimeStatus) {
	if entry == nil || entry.sessionActivity == nil {
		return
	}
	entry.sessionActivity.Publish(clientui.Event{
		Kind:                  clientui.EventExternalRuntimeStatus,
		ExternalRuntimeStatus: &status,
	})
}

type notifyingSessionActivitySubscription struct {
	serverapi.SessionActivitySubscription
	once    sync.Once
	onClose func()
}

func (s *notifyingSessionActivitySubscription) Close() error {
	var err error
	if s != nil && s.SessionActivitySubscription != nil {
		err = s.SessionActivitySubscription.Close()
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
	var err error
	if s != nil && s.PromptActivitySubscription != nil {
		err = s.PromptActivitySubscription.Close()
	}
	s.once.Do(func() {
		if s.onClose != nil {
			s.onClose()
		}
	})
	return err
}
