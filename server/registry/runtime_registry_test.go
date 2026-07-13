package registry

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	"core/server/attentionnotify"
	"core/server/llm"
	"core/server/runtime"
	"core/server/runtimeactivity"
	"core/server/runtimeops"
	"core/server/session"
	askquestion "core/server/tools"
	"core/shared/clientui"
	"core/shared/serverapi"
)

type registryRuntimeFakeClient struct{}

func (registryRuntimeFakeClient) Generate(context.Context, llm.Request) (llm.Response, error) {
	return llm.Response{}, nil
}

func (registryRuntimeFakeClient) ProviderCapabilities(context.Context) (llm.ProviderCapabilities, error) {
	return llm.ProviderCapabilities{ProviderID: "fake", SupportsResponsesAPI: true}, nil
}

func registerReady(t *testing.T, r *RuntimeRegistry, sessionID string, engine *runtime.Engine) {
	t.Helper()
	claim, _, _ := r.AcquireRuntimeClaim(sessionID, "test-owner")
	if claim == nil {
		t.Fatalf("AcquireRuntimeClaim(%q) returned nil claim", sessionID)
	}
	claim.Resolve(engine, nil, nil)
}

func TestRuntimeRegistryCancellableStartStaysAdmissionOnlyForRuntimeActivity(t *testing.T) {
	r := NewRuntimeRegistry()
	startCtx, release, ok := r.BeginCancellableSessionRun("session-starting")
	if !ok {
		t.Fatal("BeginCancellableSessionRun rejected first start")
	}
	if startCtx == nil {
		t.Fatal("start context is required")
	}
	snapshot := r.RuntimeActivityRegistrySnapshot("session-starting")
	activity, err := runtimeactivity.ResolveRuntimeActivity(runtimeactivity.ResolverSnapshot{Registry: snapshot})
	if err != nil {
		t.Fatalf("ResolveRuntimeActivity: %v", err)
	}
	if activity.State != clientui.RuntimeActivityUnavailable {
		t.Fatalf("activity = %+v, want unavailable because long-lived starts are admission-only", activity)
	}
	release()
	if err := startCtx.Err(); !errors.Is(err, context.Canceled) {
		t.Fatalf("released start context error = %v, want canceled", err)
	}
	snapshot = r.RuntimeActivityRegistrySnapshot("session-starting")
	if snapshot.Registered || snapshot.Starting {
		t.Fatalf("snapshot after release = %+v, want unavailable/no start", snapshot)
	}
}

func TestRuntimeRegistryStartReservationDoesNotMaskRegisteredIdleActivity(t *testing.T) {
	r := NewRuntimeRegistry()
	startCtx, release, ok := r.BeginCancellableSessionRun("session-registered")
	if !ok {
		t.Fatal("BeginCancellableSessionRun rejected first start")
	}
	defer release()
	engine := &runtime.Engine{}
	registerReady(t, r, "session-registered", engine)
	t.Cleanup(func() { closeRuntime(r, "session-registered", engine) })

	activity, err := r.RuntimeActivity("session-registered")
	if err != nil {
		t.Fatalf("RuntimeActivity: %v", err)
	}
	if activity.State != clientui.RuntimeActivityRegisteredIdle {
		t.Fatalf("activity = %+v, want registered idle despite deferred control-operation start reservation", activity)
	}
	release()
	if !errors.Is(startCtx.Err(), context.Canceled) {
		t.Fatalf("released start context error = %v, want canceled", startCtx.Err())
	}
}

func closeRuntime(r *RuntimeRegistry, sessionID string, _ *runtime.Engine) {
	claim := r.RuntimeClaimFor(sessionID)
	if claim == nil {
		return
	}
	_, _ = claim.Close(context.Background(), nil)
}

func newRegistryTestRuntime(t *testing.T, onEvent func(runtime.Event)) *runtime.Engine {
	t.Helper()
	return newRegistryRuntime(t, registryRuntimeFakeClient{}, askquestion.NewRegistry(), runtime.Config{Model: "gpt-5"}, func(_ *runtime.Engine, evt runtime.Event) {
		if onEvent != nil {
			onEvent(evt)
		}
	})
}

func newRegistryRuntime(t *testing.T, client llm.Client, toolRegistry *askquestion.Registry, cfg runtime.Config, onEvent func(*runtime.Engine, runtime.Event)) *runtime.Engine {
	t.Helper()
	store, err := session.Create(t.TempDir(), "workspace", t.TempDir())
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	var engine *runtime.Engine
	cfg.OnEvent = func(evt runtime.Event) {
		if onEvent != nil {
			onEvent(engine, evt)
		}
	}
	engine, err = runtime.New(store, client, toolRegistry, cfg)
	if err != nil {
		t.Fatalf("new runtime: %v", err)
	}
	t.Cleanup(func() { _ = engine.Close() })
	return engine
}

func TestRuntimeRegistryBroadcastsSessionActivityToMultipleSubscribers(t *testing.T) {
	registry := NewRuntimeRegistry()
	engine := &runtime.Engine{}
	registerReady(t, registry, "session-1", engine)
	t.Cleanup(func() { closeRuntime(registry, "session-1", engine) })

	first, err := registry.SubscribeSessionActivity(context.Background(), "session-1")
	if err != nil {
		t.Fatalf("SubscribeSessionActivity first: %v", err)
	}
	defer func() { _ = first.Close() }()
	second, err := registry.SubscribeSessionActivity(context.Background(), "session-1")
	if err != nil {
		t.Fatalf("SubscribeSessionActivity second: %v", err)
	}
	defer func() { _ = second.Close() }()

	registry.PublishRuntimeEvent("session-1", runtime.Event{Kind: runtime.EventConversationUpdated, StepID: "step-1"})

	ctx := context.Background()
	firstEvt, err := first.Next(ctx)
	if err != nil {
		t.Fatalf("first.Next: %v", err)
	}
	secondEvt, err := second.Next(ctx)
	if err != nil {
		t.Fatalf("second.Next: %v", err)
	}
	if firstEvt.Kind != clientui.EventConversationUpdated || secondEvt.Kind != clientui.EventConversationUpdated {
		t.Fatalf("unexpected events: first=%+v second=%+v", firstEvt, secondEvt)
	}
	if firstEvt.StepID != "step-1" || secondEvt.StepID != "step-1" {
		t.Fatalf("unexpected step ids: first=%+v second=%+v", firstEvt, secondEvt)
	}
}

func TestRuntimeRegistryIsolatesSessionActivityBetweenSessions(t *testing.T) {
	registry := NewRuntimeRegistry()
	engineA := &runtime.Engine{}
	engineB := &runtime.Engine{}
	registerReady(t, registry, "session-a", engineA)
	registerReady(t, registry, "session-b", engineB)
	t.Cleanup(func() {
		closeRuntime(registry, "session-a", engineA)
		closeRuntime(registry, "session-b", engineB)
	})

	subA, err := registry.SubscribeSessionActivity(context.Background(), "session-a")
	if err != nil {
		t.Fatalf("SubscribeSessionActivity(session-a): %v", err)
	}
	defer func() { _ = subA.Close() }()
	subB, err := registry.SubscribeSessionActivity(context.Background(), "session-b")
	if err != nil {
		t.Fatalf("SubscribeSessionActivity(session-b): %v", err)
	}
	defer func() { _ = subB.Close() }()

	registry.PublishRuntimeEvent("session-a", runtime.Event{Kind: runtime.EventConversationUpdated, StepID: "step-a"})

	evtA, err := subA.Next(context.Background())
	if err != nil {
		t.Fatalf("subA.Next: %v", err)
	}
	if evtA.Kind != clientui.EventConversationUpdated || evtA.StepID != "step-a" {
		t.Fatalf("unexpected event for session-a: %+v", evtA)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := subB.Next(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected session-b subscriber to stay idle, got %v", err)
	}
}

func TestRuntimeRegistryClosesLaggedSubscriberWithGapError(t *testing.T) {
	registry := NewRuntimeRegistry()
	engine := &runtime.Engine{}
	registerReady(t, registry, "session-1", engine)
	t.Cleanup(func() { closeRuntime(registry, "session-1", engine) })

	sub, err := registry.SubscribeSessionActivity(context.Background(), "session-1")
	if err != nil {
		t.Fatalf("SubscribeSessionActivity: %v", err)
	}
	defer func() { _ = sub.Close() }()

	for i := 0; i <= sessionActivityBufferSize+1; i++ {
		registry.PublishRuntimeEvent("session-1", runtime.Event{Kind: runtime.EventConversationUpdated})
	}

	for i := 0; i < sessionActivityBufferSize; i++ {
		evt, err := sub.Next(context.Background())
		if err != nil {
			t.Fatalf("unexpected early stream error after %d events: %v", i, err)
		}
		if evt.Kind != clientui.EventConversationUpdated {
			t.Fatalf("unexpected event at %d: %+v", i, evt)
		}
	}
	if _, err := sub.Next(context.Background()); !errors.Is(err, serverapi.ErrStreamGap) {
		t.Fatalf("expected gap error, got %v", err)
	}
}

func TestRuntimeRegistryReplaysSessionActivityFromCursor(t *testing.T) {
	registry := NewRuntimeRegistry()
	engine := &runtime.Engine{}
	registerReady(t, registry, "session-1", engine)
	t.Cleanup(func() { closeRuntime(registry, "session-1", engine) })

	registry.PublishRuntimeEvent("session-1", runtime.Event{Kind: runtime.EventConversationUpdated, StepID: "step-1"})
	registry.PublishRuntimeEvent("session-1", runtime.Event{Kind: runtime.EventRunStateChanged, StepID: "step-2"})

	sub, err := registry.SubscribeSessionActivityFrom(context.Background(), serverapi.SessionActivitySubscribeRequest{SessionID: "session-1", AfterSequence: 3})
	if err != nil {
		t.Fatalf("SubscribeSessionActivityFrom: %v", err)
	}
	defer func() { _ = sub.Close() }()

	evt, err := sub.Next(context.Background())
	if err != nil {
		t.Fatalf("Next replay: %v", err)
	}
	if evt.Sequence != 4 || evt.StepID != "step-2" {
		t.Fatalf("replay event = %+v, want sequence 4 step-2", evt)
	}
}

func TestRuntimeRegistryPublishesVersionedRuntimeActivityOnSessionStream(t *testing.T) {
	registry := NewRuntimeRegistry()
	engine := &runtime.Engine{}
	registerReady(t, registry, "session-1", engine)
	t.Cleanup(func() { closeRuntime(registry, "session-1", engine) })
	version := clientui.ReadModelVersion{Epoch: "epoch-1", Generation: 1, Sequence: 1}
	activity := clientui.MustRuntimeActivity(clientui.RuntimeActivityRegisteredIdle, clientui.RuntimeActivityOptions{QueueAccepting: true})

	sub, err := registry.SubscribeSessionActivity(context.Background(), "session-1")
	if err != nil {
		t.Fatalf("SubscribeSessionActivity: %v", err)
	}
	defer func() { _ = sub.Close() }()

	registry.PublishRuntimeActivitySnapshot("session-1", runtimeactivity.ResponseSnapshot{
		Version:             version,
		Activity:            activity,
		InputReconciliation: clientui.NewEmptyRuntimeInputReconciliationSnapshot(version),
	})

	evt, err := sub.Next(context.Background())
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if evt.Kind != clientui.EventRuntimeActivityChanged || evt.Sequence == 0 {
		t.Fatalf("event = %+v, want runtime activity event with raw stream sequence", evt)
	}
	if evt.ReadModelVersion != version || evt.RuntimeActivity == nil || *evt.RuntimeActivity != activity {
		t.Fatalf("activity payload = %+v version=%+v, want %+v %+v", evt.RuntimeActivity, evt.ReadModelVersion, activity, version)
	}
	if evt.InputReconciliation == nil || evt.InputReconciliation.Version != version {
		t.Fatalf("reconciliation = %+v, want version %+v", evt.InputReconciliation, version)
	}
}

func TestRuntimeRegistryRecordsQueuedMessageStatusReconciliationByQueueItemID(t *testing.T) {
	operations := runtimeops.NewCoordinator()
	registry := NewRuntimeRegistry().WithOperationCoordinator(operations)
	engine := &runtime.Engine{}
	registerReady(t, registry, "session-1", engine)
	t.Cleanup(func() { closeRuntime(registry, "session-1", engine) })
	ref := clientui.RuntimeOperationRef{Kind: clientui.RuntimeOperationKindQueuedMessage, QueueItemID: "server-queue-1"}

	registry.PublishRuntimeEvent("session-1", runtime.Event{
		Kind: runtime.EventQueuedUserMessageStatus,
		QueuedUserMessageStatus: &runtime.QueuedUserMessageStatusEvent{
			SessionID:       "session-1",
			QueueItemID:     "server-queue-1",
			ClientRequestID: "client-queue-1",
			Status:          runtime.QueuedUserMessageSubmitted,
		},
	})

	snapshot := operations.Snapshot("session-1", clientui.ReadModelVersion{Epoch: "epoch-1", Generation: 1, Sequence: 1}, []clientui.RuntimeOperationRef{ref})
	if len(snapshot.Operations) != 1 || snapshot.Operations[0].State != clientui.RuntimeInputReconciliationSubmitted {
		t.Fatalf("queued-message reconciliation = %+v, want submitted", snapshot.Operations)
	}
}

func TestRuntimeActivityEventsShareSessionActivityCursorWithoutUsingRawSequenceAsVersion(t *testing.T) {
	registry := NewRuntimeRegistry()
	engine := &runtime.Engine{}
	registerReady(t, registry, "session-1", engine)
	t.Cleanup(func() { closeRuntime(registry, "session-1", engine) })
	version := clientui.ReadModelVersion{Epoch: "epoch-1", Generation: 1, Sequence: 42}
	activity := clientui.MustRuntimeActivity(clientui.RuntimeActivityRegisteredIdle, clientui.RuntimeActivityOptions{QueueAccepting: true})

	registry.PublishRuntimeEvent("session-1", runtime.Event{Kind: runtime.EventConversationUpdated, StepID: "step-1"})
	registry.PublishRuntimeActivitySnapshot("session-1", runtimeactivity.ResponseSnapshot{
		Version:             version,
		Activity:            activity,
		InputReconciliation: clientui.NewEmptyRuntimeInputReconciliationSnapshot(version),
	})
	registry.PublishRuntimeEvent("session-1", runtime.Event{Kind: runtime.EventRunStateChanged, StepID: "step-3"})

	sub, err := registry.SubscribeSessionActivityFrom(context.Background(), serverapi.SessionActivitySubscribeRequest{SessionID: "session-1", AfterSequence: 3})
	if err != nil {
		t.Fatalf("SubscribeSessionActivityFrom: %v", err)
	}
	defer func() { _ = sub.Close() }()

	activityEvent, err := sub.Next(context.Background())
	if err != nil {
		t.Fatalf("activity Next: %v", err)
	}
	if activityEvent.Kind != clientui.EventRuntimeActivityChanged || activityEvent.Sequence != 4 {
		t.Fatalf("activity event = %+v, want raw sequence 4 runtime activity", activityEvent)
	}
	if activityEvent.ReadModelVersion != version || activityEvent.ReadModelVersion.Sequence == activityEvent.Sequence {
		t.Fatalf("activity event versions = raw %d read-model %+v, want independent read-model payload", activityEvent.Sequence, activityEvent.ReadModelVersion)
	}

	rawEvent, err := sub.Next(context.Background())
	if err != nil {
		t.Fatalf("raw Next: %v", err)
	}
	if rawEvent.Kind != clientui.EventRunStateChanged || rawEvent.Sequence != 5 || rawEvent.ReadModelVersion.Validate() == nil {
		t.Fatalf("raw event = %+v, want raw run event without read-model version", rawEvent)
	}
}

func TestRuntimeRegistryReportsRunFinishedInterestReason(t *testing.T) {
	registry := NewRuntimeRegistry()
	engine := &runtime.Engine{}
	registerReady(t, registry, "session-1", engine)
	t.Cleanup(func() { closeRuntime(registry, "session-1", engine) })
	reasons := make(chan RuntimeInterestReason, 1)
	registry.SetInterestObserver(func(sessionID string, reason RuntimeInterestReason) {
		if sessionID == "session-1" {
			reasons <- reason
		}
	})

	registry.PublishRuntimeEvent("session-1", runtime.Event{
		Kind:     runtime.EventRunStateChanged,
		StepID:   "step-1",
		RunState: &runtime.RunState{Lifecycle: runtime.FinishedRunLifecycle(runtime.RunModeTurn)},
	})

	select {
	case reason := <-reasons:
		if reason != RuntimeInterestRunFinished {
			t.Fatalf("interest reason = %v, want run finished", reason)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for interest observer")
	}
}

func TestRuntimeRegistryNotifiesSleepObserverFromRuntimeActivitySnapshots(t *testing.T) {
	registry := NewRuntimeRegistry()
	engine := &runtime.Engine{}
	registerReady(t, registry, "session-1", engine)
	defer closeRuntime(registry, "session-1", engine)

	notifications := make(chan bool, 2)
	registry.SetSleepObserver(func(active bool) {
		notifications <- active
	})

	publishRunState(registry, "session-1", true)
	publishRunState(registry, "session-1", false)

	if running := receiveSleepObserverState(t, notifications); !running {
		t.Fatal("expected running sleep observer notification")
	}
	if running := receiveSleepObserverState(t, notifications); running {
		t.Fatal("expected stopped sleep observer notification")
	}
}

func TestRuntimeRegistryAggregatesSleepObserverAcrossSessions(t *testing.T) {
	registry := NewRuntimeRegistry()
	engineA := &runtime.Engine{}
	engineB := &runtime.Engine{}
	registerReady(t, registry, "session-a", engineA)
	registerReady(t, registry, "session-b", engineB)
	defer closeRuntime(registry, "session-a", engineA)
	defer closeRuntime(registry, "session-b", engineB)

	notifications := make(chan bool, 4)
	registry.SetSleepObserver(func(active bool) {
		notifications <- active
	})

	publishRunState(registry, "session-a", true)
	if active := receiveSleepObserverState(t, notifications); !active {
		t.Fatal("expected aggregate active notification")
	}
	publishRunState(registry, "session-b", true)
	publishRunState(registry, "session-a", false)
	assertNoSleepObserverState(t, notifications)
	publishRunState(registry, "session-b", false)

	if active := receiveSleepObserverState(t, notifications); active {
		t.Fatal("expected aggregate idle notification")
	}
	assertNoSleepObserverState(t, notifications)
}

func TestRuntimeRegistrySleepObserverDuplicateRunStateEventsAreIdempotent(t *testing.T) {
	registry := NewRuntimeRegistry()
	engine := &runtime.Engine{}
	registerReady(t, registry, "session-1", engine)
	defer closeRuntime(registry, "session-1", engine)

	notifications := make(chan bool, 4)
	registry.SetSleepObserver(func(active bool) {
		notifications <- active
	})

	publishRunState(registry, "session-1", true)
	publishRunState(registry, "session-1", true)
	publishRunState(registry, "session-1", false)
	publishRunState(registry, "session-1", false)

	if active := receiveSleepObserverState(t, notifications); !active {
		t.Fatal("expected aggregate active notification")
	}
	if active := receiveSleepObserverState(t, notifications); active {
		t.Fatal("expected aggregate idle notification")
	}
	assertNoSleepObserverState(t, notifications)
}

func TestRuntimeRegistrySleepObserverConcurrentRunStateUpdates(t *testing.T) {
	registry := NewRuntimeRegistry()
	for i := 0; i < 20; i++ {
		id := fmt.Sprintf("session-%d", i)
		registerReady(t, registry, id, &runtime.Engine{})
	}

	notifications := make(chan bool, 128)
	registry.SetSleepObserver(func(active bool) {
		notifications <- active
	})

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		id := fmt.Sprintf("session-%d", i)
		wg.Add(1)
		go func() {
			defer wg.Done()
			publishRunState(registry, id, true)
			publishRunState(registry, id, false)
		}()
	}
	wg.Wait()

	registry.runStateMu.Lock()
	runningCount := len(registry.blockingActivitySessions)
	registry.runStateMu.Unlock()
	if runningCount != 0 {
		t.Fatalf("running session count = %d, want 0", runningCount)
	}
}

func TestRuntimeRegistrySleepObserverNotificationsDoNotOvertakeAggregateUpdates(t *testing.T) {
	registry := NewRuntimeRegistry()
	engine := &runtime.Engine{}
	registerReady(t, registry, "session-1", engine)
	defer closeRuntime(registry, "session-1", engine)

	firstEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	notifications := make(chan bool, 2)
	var once sync.Once
	registry.SetSleepObserver(func(active bool) {
		if active {
			once.Do(func() {
				close(firstEntered)
				<-releaseFirst
			})
		}
		notifications <- active
	})

	startDone := make(chan struct{})
	go func() {
		defer close(startDone)
		publishRunState(registry, "session-1", true)
	}()
	<-firstEntered

	finishDone := make(chan struct{})
	go func() {
		defer close(finishDone)
		publishRunState(registry, "session-1", false)
	}()

	select {
	case active := <-notifications:
		t.Fatalf("notification overtook blocked active observer: %v", active)
	default:
	}

	close(releaseFirst)
	<-startDone
	<-finishDone

	if active := receiveSleepObserverState(t, notifications); !active {
		t.Fatal("expected active notification first")
	}
	if active := receiveSleepObserverState(t, notifications); active {
		t.Fatal("expected idle notification second")
	}
}

func receiveSleepObserverState(t *testing.T, notifications <-chan bool) bool {
	t.Helper()
	select {
	case running := <-notifications:
		return running
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for sleep observer notification")
		return false
	}
}

func assertNoSleepObserverState(t *testing.T, notifications <-chan bool) {
	t.Helper()
	select {
	case active := <-notifications:
		t.Fatalf("unexpected sleep observer notification: %v", active)
	default:
	}
}

func assertNoClose(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
		t.Fatal("channel closed before release")
	case <-time.After(50 * time.Millisecond):
	}
}

func waitClosed(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for channel close")
	}
}

func publishRunState(registry *RuntimeRegistry, sessionID string, running bool) {
	activity := clientui.MustRuntimeActivity(clientui.RuntimeActivityRegisteredIdle, clientui.RuntimeActivityOptions{QueueAccepting: true})
	if running {
		activity = clientui.MustRuntimeActivity(clientui.RuntimeActivityRunning, clientui.RuntimeActivityOptions{
			ActiveKind:     clientui.RuntimeActivityActiveKindUserTurn,
			RunID:          "run-1",
			StepID:         "step-1",
			QueueAccepting: true,
		})
	}
	version := runtimeactivity.NextReadModelVersion(sessionID)
	registry.PublishRuntimeActivitySnapshot(sessionID, runtimeactivity.ResponseSnapshot{
		Version:             version,
		Activity:            activity,
		InputReconciliation: clientui.NewEmptyRuntimeInputReconciliationSnapshot(version),
	})
}

func TestRuntimeRegistryDeliversReplayBeforePostSubscribeLiveEvents(t *testing.T) {
	registry := NewRuntimeRegistry()
	engine := &runtime.Engine{}
	registerReady(t, registry, "session-1", engine)
	t.Cleanup(func() { closeRuntime(registry, "session-1", engine) })

	registry.PublishRuntimeEvent("session-1", runtime.Event{Kind: runtime.EventConversationUpdated, StepID: "step-1"})
	registry.PublishRuntimeEvent("session-1", runtime.Event{Kind: runtime.EventRunStateChanged, StepID: "step-2"})

	sub, err := registry.SubscribeSessionActivityFrom(context.Background(), serverapi.SessionActivitySubscribeRequest{SessionID: "session-1", AfterSequence: 3})
	if err != nil {
		t.Fatalf("SubscribeSessionActivityFrom: %v", err)
	}
	defer func() { _ = sub.Close() }()

	registry.PublishRuntimeEvent("session-1", runtime.Event{Kind: runtime.EventAssistantDelta, StepID: "step-3"})

	replay, err := sub.Next(context.Background())
	if err != nil {
		t.Fatalf("Next replay: %v", err)
	}
	live, err := sub.Next(context.Background())
	if err != nil {
		t.Fatalf("Next live: %v", err)
	}
	if replay.Sequence != 4 || replay.StepID != "step-2" {
		t.Fatalf("replay event = %+v, want sequence 4 step-2", replay)
	}
	if live.Sequence != 5 || live.StepID != "step-3" {
		t.Fatalf("live event = %+v, want sequence 5 step-3", live)
	}
}

func TestRuntimeRegistryRejectsExpiredSessionActivityCursor(t *testing.T) {
	registry := NewRuntimeRegistry()
	engine := &runtime.Engine{}
	registerReady(t, registry, "session-1", engine)
	t.Cleanup(func() { closeRuntime(registry, "session-1", engine) })

	for i := 0; i <= sessionActivityBufferSize+1; i++ {
		registry.PublishRuntimeEvent("session-1", runtime.Event{Kind: runtime.EventConversationUpdated})
	}

	if _, err := registry.SubscribeSessionActivityFrom(context.Background(), serverapi.SessionActivitySubscribeRequest{SessionID: "session-1", AfterSequence: 1}); !errors.Is(err, serverapi.ErrStreamGap) {
		t.Fatalf("expected stream gap for expired cursor, got %v", err)
	}
}

func TestRuntimeRegistryRejectsInactiveSessionActivityStreamWithUnavailableError(t *testing.T) {
	registry := NewRuntimeRegistry()
	if _, err := registry.SubscribeSessionActivity(context.Background(), "missing-session"); !errors.Is(err, serverapi.ErrStreamUnavailable) {
		t.Fatalf("expected unavailable error, got %v", err)
	}
}

func TestRuntimeRegistryNormalizesSessionActivitySubscriptionFailures(t *testing.T) {
	sub, err := newSessionActivityBroker().Subscribe(0)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	sub.closeWithError(errors.New("writer failed"))
	if _, err := sub.Next(context.Background()); !errors.Is(err, serverapi.ErrStreamFailed) {
		t.Fatalf("expected stream failed error, got %v", err)
	}
}

func TestRuntimeRegistryPassesThroughSessionActivityEOF(t *testing.T) {
	sub, err := newSessionActivityBroker().Subscribe(0)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	sub.closeWithError(io.EOF)
	if _, err := sub.Next(context.Background()); !errors.Is(err, io.EOF) {
		t.Fatalf("expected EOF, got %v", err)
	}
}

func TestRuntimeRegistryPassesThroughSessionActivityContextCanceled(t *testing.T) {
	sub, err := newSessionActivityBroker().Subscribe(0)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := sub.Next(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestRuntimeRegistryTracksPendingPromptsPerSession(t *testing.T) {
	registry := NewRuntimeRegistry()
	engine := &runtime.Engine{}
	registerReady(t, registry, "session-1", engine)
	t.Cleanup(func() { closeRuntime(registry, "session-1", engine) })

	registry.BeginPendingPrompt("session-1", askquestion.AskQuestionRequest{ID: "ask-1", Question: "one?"})
	registry.BeginPendingPrompt("session-1", askquestion.AskQuestionRequest{ID: "approval-1", Question: "allow?", Approval: true})

	items := registry.ListPendingPrompts("session-1")
	if len(items) != 2 {
		t.Fatalf("expected two pending prompts, got %+v", items)
	}
	if items[0].Request.ID != "ask-1" || items[1].Request.ID != "approval-1" {
		t.Fatalf("unexpected pending prompts ordering: %+v", items)
	}

	registry.CompletePendingPrompt("session-1", "ask-1")
	items = registry.ListPendingPrompts("session-1")
	if len(items) != 1 || items[0].Request.ID != "approval-1" {
		t.Fatalf("unexpected pending prompts after completion: %+v", items)
	}

	closeRuntime(registry, "session-1", engine)
	if items := registry.ListPendingPrompts("session-1"); len(items) != 0 {
		t.Fatalf("expected no pending prompts after unregister, got %+v", items)
	}
}

func TestRuntimeRegistrySubscribePromptActivityReplaysAllPendingPromptsBeyondBufferLimit(t *testing.T) {
	registry := NewRuntimeRegistry()
	engine := &runtime.Engine{}
	registerReady(t, registry, "session-1", engine)
	t.Cleanup(func() { closeRuntime(registry, "session-1", engine) })

	for i := 0; i < promptActivityBufferSize+5; i++ {
		registry.BeginPendingPrompt("session-1", askquestion.AskQuestionRequest{ID: fmt.Sprintf("ask-%03d", i), Question: "pending"})
	}

	sub, err := registry.SubscribePromptActivity(context.Background(), "session-1")
	if err != nil {
		t.Fatalf("SubscribePromptActivity: %v", err)
	}
	defer func() { _ = sub.Close() }()

	for i := 0; i < promptActivityBufferSize+5; i++ {
		evt, err := sub.Next(context.Background())
		if err != nil {
			t.Fatalf("Next %d: %v", i, err)
		}
		wantID := fmt.Sprintf("ask-%03d", i)
		if evt.Type != clientui.PendingPromptEventPending || evt.PromptID != wantID {
			t.Fatalf("event %d = %+v, want pending %q", i, evt, wantID)
		}
	}
	evt, err := sub.Next(context.Background())
	if err != nil {
		t.Fatalf("snapshot complete: %v", err)
	}
	if evt.Type != clientui.PendingPromptEventSnapshot {
		t.Fatalf("expected snapshot completion event, got %+v", evt)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := sub.Next(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected no extra replay events, got %v", err)
	}
}

func TestRuntimeRegistrySubscribePromptActivityDeliversPromptStartedDuringInitialSubscribe(t *testing.T) {
	registry := NewRuntimeRegistry()
	engine := &runtime.Engine{}
	registerReady(t, registry, "session-1", engine)
	t.Cleanup(func() { closeRuntime(registry, "session-1", engine) })

	entry := registry.directory.Entry("session-1")
	if entry == nil {
		t.Fatal("registered runtime entry not found")
	}

	promptStarted := make(chan struct{})
	promptDone := make(chan struct{})
	sub, err := entry.SubscribePromptActivityInitial("session-1", registry.nextReadModelVersion("session-1"), func() {
		go func() {
			close(promptStarted)
			registry.BeginPendingPrompt("session-1", askquestion.AskQuestionRequest{ID: "ask-during-subscribe", Question: "Proceed?"})
			close(promptDone)
		}()
		<-promptStarted
		select {
		case <-promptDone:
			t.Fatal("prompt publish completed before initial subscription registered")
		default:
		}
	})
	if err != nil {
		t.Fatalf("subscribePromptActivityInitial: %v", err)
	}
	defer func() { _ = sub.Close() }()

	select {
	case <-promptDone:
	case <-time.After(time.Second):
		t.Fatal("prompt publish did not complete after initial subscription registered")
	}

	snapshot, err := sub.Next(context.Background())
	if err != nil {
		t.Fatalf("snapshot Next: %v", err)
	}
	if snapshot.Type != clientui.PendingPromptEventSnapshot {
		t.Fatalf("first event = %+v, want snapshot completion", snapshot)
	}
	if err := snapshot.ReadModelVersion.Validate(); err != nil {
		t.Fatalf("snapshot read-model version is not stamped: %v", err)
	}
	pending, err := sub.Next(context.Background())
	if err != nil {
		t.Fatalf("pending Next: %v", err)
	}
	if pending.Type != clientui.PendingPromptEventPending || pending.PromptID != "ask-during-subscribe" || pending.Question != "Proceed?" {
		t.Fatalf("pending event = %+v, want ask-during-subscribe", pending)
	}
	if !pending.ReadModelVersion.NewerThan(snapshot.ReadModelVersion) {
		t.Fatalf("pending version = %+v, want newer than snapshot %+v", pending.ReadModelVersion, snapshot.ReadModelVersion)
	}
}

func TestRuntimeRegistryPromptInitialSnapshotVersionBecomesReplayCursor(t *testing.T) {
	registry := NewRuntimeRegistry()
	engine := &runtime.Engine{}
	registerReady(t, registry, "session-1", engine)
	t.Cleanup(func() { closeRuntime(registry, "session-1", engine) })

	registry.BeginPendingPrompt("session-1", askquestion.AskQuestionRequest{ID: "ask-1", Question: "Proceed?"})
	initial, err := registry.SubscribePromptActivityFrom(context.Background(), serverapi.PromptActivitySubscribeRequest{SessionID: "session-1"})
	if err != nil {
		t.Fatalf("SubscribePromptActivity initial: %v", err)
	}
	defer func() { _ = initial.Close() }()
	pending := nextPromptEventForTest(t, initial)
	snapshot := nextPromptEventForTest(t, initial)
	if pending.Type != clientui.PendingPromptEventPending || snapshot.Type != clientui.PendingPromptEventSnapshot {
		t.Fatalf("initial events = %+v then %+v, want pending then snapshot", pending, snapshot)
	}
	if pending.ReadModelVersion != snapshot.ReadModelVersion {
		t.Fatalf("snapshot events should share one read-model version, pending=%+v snapshot=%+v", pending.ReadModelVersion, snapshot.ReadModelVersion)
	}
	if err := snapshot.ReadModelVersion.Validate(); err != nil {
		t.Fatalf("snapshot version: %v", err)
	}

	registry.CompletePendingPrompt("session-1", "ask-1")
	replay, err := registry.SubscribePromptActivityFrom(context.Background(), serverapi.PromptActivitySubscribeRequest{
		SessionID:             "session-1",
		AfterReadModelVersion: snapshot.ReadModelVersion,
	})
	if err != nil {
		t.Fatalf("SubscribePromptActivity replay: %v", err)
	}
	defer func() { _ = replay.Close() }()
	resolved := nextPromptEventForTest(t, replay)
	if resolved.Type != clientui.PendingPromptEventResolved || resolved.PromptID != "ask-1" {
		t.Fatalf("replay event = %+v, want ask-1 resolved", resolved)
	}
	if !resolved.ReadModelVersion.NewerThan(snapshot.ReadModelVersion) {
		t.Fatalf("resolved version = %+v, want newer than snapshot cursor %+v", resolved.ReadModelVersion, snapshot.ReadModelVersion)
	}
}

func TestRuntimeRegistryPromptEventSharesRuntimeReadModelGeneration(t *testing.T) {
	registry := NewRuntimeRegistry().WithOperationCoordinator(runtimeops.NewCoordinator())
	engine := &runtime.Engine{}
	registerReady(t, registry, "session-1", engine)
	t.Cleanup(func() { closeRuntime(registry, "session-1", engine) })

	runtimeSnapshot, err := registry.RuntimeReadModelSnapshot(context.Background(), "session-1", nil)
	if err != nil {
		t.Fatalf("RuntimeReadModelSnapshot: %v", err)
	}
	registry.BeginPendingPrompt("session-1", askquestion.AskQuestionRequest{ID: "ask-1", Question: "Proceed?"})
	initial, err := registry.SubscribePromptActivityFrom(context.Background(), serverapi.PromptActivitySubscribeRequest{SessionID: "session-1"})
	if err != nil {
		t.Fatalf("SubscribePromptActivity initial: %v", err)
	}
	defer func() { _ = initial.Close() }()
	pending := nextPromptEventForTest(t, initial)
	if pending.ReadModelVersion.Epoch != runtimeSnapshot.Version.Epoch || pending.ReadModelVersion.Generation != runtimeSnapshot.Version.Generation {
		t.Fatalf("prompt version %+v does not share runtime read-model generation %+v", pending.ReadModelVersion, runtimeSnapshot.Version)
	}
	if pending.ReadModelVersion.Sequence <= runtimeSnapshot.Version.Sequence {
		t.Fatalf("prompt sequence = %+v, want after runtime snapshot %+v", pending.ReadModelVersion, runtimeSnapshot.Version)
	}
}

func TestRuntimeRegistryPromptReplayRejectsForeignReadModelGenerationBeforeRingOverflow(t *testing.T) {
	registry := NewRuntimeRegistry()
	engine := &runtime.Engine{}
	registerReady(t, registry, "session-1", engine)
	t.Cleanup(func() { closeRuntime(registry, "session-1", engine) })
	registry.BeginPendingPrompt("session-1", askquestion.AskQuestionRequest{ID: "ask-1", Question: "Proceed?"})
	foreignVersion, err := clientui.NewReadModelVersion("foreign-epoch", 1, 1)
	if err != nil {
		t.Fatalf("foreign version: %v", err)
	}

	_, err = registry.SubscribePromptActivityFrom(context.Background(), serverapi.PromptActivitySubscribeRequest{
		SessionID:             "session-1",
		AfterReadModelVersion: foreignVersion,
	})
	if !errors.Is(err, serverapi.ErrStreamGap) {
		t.Fatalf("SubscribePromptActivity foreign generation error = %v, want stream gap", err)
	}
}

func TestRuntimeRegistryPromptResolutionDuringInitialSnapshotIsDeliveredAfterSnapshot(t *testing.T) {
	registry := NewRuntimeRegistry()
	engine := &runtime.Engine{}
	registerReady(t, registry, "session-1", engine)
	t.Cleanup(func() { closeRuntime(registry, "session-1", engine) })
	registry.BeginPendingPrompt("session-1", askquestion.AskQuestionRequest{ID: "ask-1", Question: "Proceed?"})

	entry := registry.directory.Entry("session-1")
	if entry == nil {
		t.Fatal("registered runtime entry not found")
	}
	resolveStarted := make(chan struct{})
	resolveDone := make(chan struct{})
	sub, err := entry.SubscribePromptActivityInitial("session-1", registry.nextReadModelVersion("session-1"), func() {
		go func() {
			close(resolveStarted)
			registry.CompletePendingPrompt("session-1", "ask-1")
			close(resolveDone)
		}()
		<-resolveStarted
		select {
		case <-resolveDone:
			t.Fatal("prompt resolution completed before initial subscription registered")
		default:
		}
	})
	if err != nil {
		t.Fatalf("SubscribePromptActivityInitial: %v", err)
	}
	defer func() { _ = sub.Close() }()
	select {
	case <-resolveDone:
	case <-time.After(time.Second):
		t.Fatal("prompt resolution did not complete after initial subscription registered")
	}

	pending := nextPromptEventForTest(t, sub)
	snapshot := nextPromptEventForTest(t, sub)
	resolved := nextPromptEventForTest(t, sub)
	if pending.Type != clientui.PendingPromptEventPending || snapshot.Type != clientui.PendingPromptEventSnapshot || resolved.Type != clientui.PendingPromptEventResolved {
		t.Fatalf("events = %+v, %+v, %+v; want pending snapshot resolved", pending, snapshot, resolved)
	}
	if pending.ReadModelVersion != snapshot.ReadModelVersion {
		t.Fatalf("initial prompt snapshot version mismatch: pending=%+v snapshot=%+v", pending.ReadModelVersion, snapshot.ReadModelVersion)
	}
	if !resolved.ReadModelVersion.NewerThan(snapshot.ReadModelVersion) {
		t.Fatalf("resolved version = %+v, want newer than snapshot %+v", resolved.ReadModelVersion, snapshot.ReadModelVersion)
	}
}

func TestRuntimeRegistryPromptRingLowerBoundIgnoresRuntimeReadModelChurn(t *testing.T) {
	registry := NewRuntimeRegistry()
	engine := &runtime.Engine{}
	registerReady(t, registry, "session-1", engine)
	t.Cleanup(func() { closeRuntime(registry, "session-1", engine) })

	initial, err := registry.SubscribePromptActivityFrom(context.Background(), serverapi.PromptActivitySubscribeRequest{SessionID: "session-1"})
	if err != nil {
		t.Fatalf("SubscribePromptActivity initial: %v", err)
	}
	snapshot := nextPromptEventForTest(t, initial)
	_ = initial.Close()
	if snapshot.Type != clientui.PendingPromptEventSnapshot {
		t.Fatalf("initial event = %+v, want snapshot", snapshot)
	}
	for i := 0; i < promptActivityBufferSize*2; i++ {
		version := runtimeactivity.NextReadModelVersion("session-1")
		registry.PublishRuntimeActivitySnapshot("session-1", runtimeactivity.ResponseSnapshot{
			Version:             version,
			Activity:            clientui.MustRuntimeActivity(clientui.RuntimeActivityRegisteredIdle, clientui.RuntimeActivityOptions{QueueAccepting: true}),
			InputReconciliation: clientui.NewEmptyRuntimeInputReconciliationSnapshot(version),
		})
	}
	for i := 0; i < promptActivityBufferSize; i++ {
		registry.BeginPendingPrompt("session-1", askquestion.AskQuestionRequest{ID: fmt.Sprintf("ask-%03d", i), Question: "pending"})
	}
	replay, err := registry.SubscribePromptActivityFrom(context.Background(), serverapi.PromptActivitySubscribeRequest{
		SessionID:             "session-1",
		AfterReadModelVersion: snapshot.ReadModelVersion,
	})
	if err != nil {
		t.Fatalf("SubscribePromptActivity after unrelated runtime churn: %v", err)
	}
	defer func() { _ = replay.Close() }()
	for i := 0; i < promptActivityBufferSize; i++ {
		evt := nextPromptEventForTest(t, replay)
		if evt.Type != clientui.PendingPromptEventPending || evt.PromptID != fmt.Sprintf("ask-%03d", i) {
			t.Fatalf("replay %d = %+v", i, evt)
		}
	}

	registry.BeginPendingPrompt("session-1", askquestion.AskQuestionRequest{ID: "ask-overflow", Question: "overflow"})
	if _, err := registry.SubscribePromptActivityFrom(context.Background(), serverapi.PromptActivitySubscribeRequest{
		SessionID:             "session-1",
		AfterReadModelVersion: snapshot.ReadModelVersion,
	}); !errors.Is(err, serverapi.ErrStreamGap) {
		t.Fatalf("SubscribePromptActivity after prompt ring overflow = %v, want stream gap", err)
	}
}

func TestPromptActivitySubscriptionCloseStopsInitialReplay(t *testing.T) {
	sub, err := newPromptActivityBroker().Subscribe([]clientui.PendingPromptEvent{
		{Type: clientui.PendingPromptEventPending, SessionID: "session-1", PromptID: "ask-1"},
		{Type: clientui.PendingPromptEventSnapshot, SessionID: "session-1"},
	}, clientui.ReadModelVersion{})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	if err := sub.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if evt, err := sub.Next(context.Background()); !evt.IsZero() || !errors.Is(err, io.EOF) {
		t.Fatalf("Next after close = evt=%+v err=%v, want EOF without initial replay", evt, err)
	}
}

func TestPromptActivityInitialSubscribeUsesSnapshotVersionAsReplayCursor(t *testing.T) {
	broker := newPromptActivityBroker()
	snapshotVersion, err := clientui.NewReadModelVersion("epoch-1", 1, 1)
	if err != nil {
		t.Fatalf("snapshot version: %v", err)
	}
	resolvedVersion, err := clientui.NewReadModelVersion("epoch-1", 1, 2)
	if err != nil {
		t.Fatalf("resolved version: %v", err)
	}
	broker.Publish(clientui.PendingPromptEvent{
		Type:             clientui.PendingPromptEventResolved,
		SessionID:        "session-1",
		PromptID:         "ask-1",
		ReadModelVersion: resolvedVersion,
	})
	sub, err := broker.Subscribe([]clientui.PendingPromptEvent{{
		Type:             clientui.PendingPromptEventSnapshot,
		SessionID:        "session-1",
		ReadModelVersion: snapshotVersion,
	}}, snapshotVersion)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer func() { _ = sub.Close() }()
	snapshot := nextPromptEventForTest(t, sub)
	resolved := nextPromptEventForTest(t, sub)
	if snapshot.Type != clientui.PendingPromptEventSnapshot || snapshot.ReadModelVersion != snapshotVersion {
		t.Fatalf("snapshot event = %+v, want version %+v", snapshot, snapshotVersion)
	}
	if resolved.Type != clientui.PendingPromptEventResolved || resolved.PromptID != "ask-1" || resolved.ReadModelVersion != resolvedVersion {
		t.Fatalf("resolved event = %+v, want ask-1 version %+v", resolved, resolvedVersion)
	}
}

func TestRuntimeRegistrySubmitPromptResponseRemovesPendingPromptBeforeWaiterReturns(t *testing.T) {
	registry := NewRuntimeRegistry()
	engine := &runtime.Engine{}
	registerReady(t, registry, "session-1", engine)
	t.Cleanup(func() { closeRuntime(registry, "session-1", engine) })

	responseDone := make(chan error, 1)
	go func() {
		_, err := registry.AwaitPromptResponse(context.Background(), "session-1", askquestion.AskQuestionRequest{ID: "ask-1", Question: "Proceed?"})
		responseDone <- err
	}()

	deadline := time.Now().Add(time.Second)
	for {
		items := registry.ListPendingPrompts("session-1")
		if len(items) == 1 && items[0].Request.ID == "ask-1" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("pending prompt was not registered: %+v", items)
		}
		time.Sleep(10 * time.Millisecond)
	}

	if err := registry.SubmitPromptResponse("session-1", askquestion.AskQuestionResponse{RequestID: "ask-1", Answer: "yes"}, nil); err != nil {
		t.Fatalf("SubmitPromptResponse: %v", err)
	}
	if items := registry.ListPendingPrompts("session-1"); len(items) != 0 {
		t.Fatalf("expected pending prompt removed immediately, got %+v", items)
	}
	select {
	case err := <-responseDone:
		if err != nil {
			t.Fatalf("AwaitPromptResponse error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for prompt response")
	}
}

func TestRuntimeRegistrySubmitPromptResponseRejectsInvalidApprovalBeforeResolving(t *testing.T) {
	broker := attentionnotify.NewBroker()
	registry := NewRuntimeRegistry().WithAttentionNotifications(broker)
	engine := &runtime.Engine{}
	registerReady(t, registry, "session-1", engine)
	t.Cleanup(func() { closeRuntime(registry, "session-1", engine) })
	sessionSub, err := registry.SubscribeSessionAttentionNotifications(context.Background(), serverapi.AttentionSessionNotificationSubscribeRequest{SessionID: "session-1"})
	if err != nil {
		t.Fatalf("SubscribeSessionAttentionNotifications: %v", err)
	}
	defer func() { _ = sessionSub.Close() }()

	responseDone := make(chan error, 1)
	go func() {
		_, err := registry.AwaitPromptResponse(context.Background(), "session-1", askquestion.AskQuestionRequest{
			ID:       "approval-1",
			Question: "Approve?",
			Approval: true,
			ApprovalOptions: []askquestion.AskQuestionApprovalOption{
				{Decision: askquestion.AskQuestionApprovalDecisionAllowOnce, Label: "Allow once"},
				{Decision: askquestion.AskQuestionApprovalDecisionDeny, Label: "Deny"},
			},
		})
		responseDone <- err
	}()
	pendingEvent := nextRegistryAttentionEvent(t, sessionSub)
	if pendingEvent.Type != clientui.AttentionNotificationEventPending {
		t.Fatalf("pending event = %+v", pendingEvent)
	}

	err = registry.SubmitPromptResponse("session-1", askquestion.AskQuestionResponse{
		RequestID: "approval-1",
		Approval:  &askquestion.AskQuestionApprovalPayload{Decision: askquestion.AskQuestionApprovalDecisionAllowSession},
	}, nil)
	if err == nil {
		t.Fatal("expected invalid approval response to be rejected")
	}
	items := registry.ListPendingPrompts("session-1")
	if len(items) != 1 || items[0].Request.ID != "approval-1" {
		t.Fatalf("pending prompts after invalid response = %+v, want approval-1 still pending", items)
	}
	if event, err := sessionSub.Next(shortRegistryContext(t)); err == nil {
		t.Fatalf("invalid approval response published resolved event: %+v", event)
	}

	if err := registry.SubmitPromptResponse("session-1", askquestion.AskQuestionResponse{
		RequestID: "approval-1",
		Approval:  &askquestion.AskQuestionApprovalPayload{Decision: askquestion.AskQuestionApprovalDecisionDeny, Commentary: "no"},
	}, nil); err != nil {
		t.Fatalf("valid SubmitPromptResponse: %v", err)
	}
	resolvedEvent := nextRegistryAttentionEvent(t, sessionSub)
	if resolvedEvent.Type != clientui.AttentionNotificationEventResolved {
		t.Fatalf("resolved event after valid response = %+v", resolvedEvent)
	}
	select {
	case err := <-responseDone:
		if err != nil {
			t.Fatalf("AwaitPromptResponse error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for prompt response")
	}
}

func TestRuntimeRegistryBeginGuardWaitsForBuild(t *testing.T) {
	registry := NewRuntimeRegistry()
	claim, _, _ := registry.AcquireRuntimeClaim("session-1", "owner-a")
	engine := &runtime.Engine{}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := registry.BeginRuntimeGuard(ctx, "session-1"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("BeginRuntimeGuard on a building runtime err=%v, want a wait (context.DeadlineExceeded), not a guard over a nil engine", err)
	}

	claim.Resolve(engine, nil, func() {})
	t.Cleanup(func() { closeRuntime(registry, "session-1", engine) })

	guard, err := registry.BeginRuntimeGuard(context.Background(), "session-1")
	if err != nil {
		t.Fatalf("BeginRuntimeGuard after build: %v", err)
	}
	defer guard.Release()
	if guard.Engine() != engine {
		t.Fatal("guard must expose the freshly built engine")
	}
}

func TestRuntimeGuardRetirePublishesQueuedFailureBeforeEntryRemoval(t *testing.T) {
	registry := NewRuntimeRegistry()
	sessionID := "session-1"
	var engine *runtime.Engine
	engine = newRegistryTestRuntime(t, func(evt runtime.Event) {
		registry.PublishRuntimeEventForEngine(sessionID, engine, evt)
	})
	registerReady(t, registry, sessionID, engine)
	sub, err := registry.SubscribeSessionActivity(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("SubscribeSessionActivity: %v", err)
	}
	defer func() { _ = sub.Close() }()
	engine.QueueUserMessageWithClientRequestID("restore this", "client-queue-1")
	guard, err := registry.BeginRuntimeGuard(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("BeginRuntimeGuard: %v", err)
	}
	defer guard.Release()
	if err := guard.Retire(runtime.QueuedUserMessageFailureRuntimeUnavailable); err != nil {
		t.Fatalf("Retire: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for {
		evt, err := sub.Next(ctx)
		if err != nil {
			t.Fatalf("Next queued failure: %v", err)
		}
		status := evt.QueuedUserMessageStatus
		if status == nil || status.Status != clientui.QueuedUserMessageFailed {
			continue
		}
		if status.ClientRequestID != "client-queue-1" {
			t.Fatalf("client request id = %q, want client-queue-1", status.ClientRequestID)
		}
		if status.FailureReason != clientui.QueuedUserMessageFailureRuntimeUnavailable {
			t.Fatalf("failure reason = %q, want runtime_unavailable", status.FailureReason)
		}
		if status.RestoreText != "restore this" {
			t.Fatalf("restore text = %q, want restore this", status.RestoreText)
		}
		return
	}
}

func TestRuntimeGuardRetireDelaysTeardownUntilGuardsRelease(t *testing.T) {
	registry := NewRuntimeRegistry()
	sessionID := "session-1"
	var engine *runtime.Engine
	engine = newRegistryTestRuntime(t, func(evt runtime.Event) {
		registry.PublishRuntimeEventForEngine(sessionID, engine, evt)
	})
	claim, _, _ := registry.AcquireRuntimeClaim(sessionID, "owner-a")
	teardownDone := make(chan struct{})
	claim.Resolve(engine, nil, func() { close(teardownDone) })
	first, err := registry.BeginRuntimeGuard(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("BeginRuntimeGuard first: %v", err)
	}
	second, err := registry.BeginRuntimeGuard(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("BeginRuntimeGuard second: %v", err)
	}
	if err := first.Retire(runtime.QueuedUserMessageFailureRuntimeUnavailable); err != nil {
		t.Fatalf("Retire: %v", err)
	}
	select {
	case <-teardownDone:
		t.Fatal("teardown ran while retiring guard was still held")
	default:
	}
	first.Release()
	select {
	case <-teardownDone:
		t.Fatal("teardown ran while second guard was still held")
	case <-time.After(50 * time.Millisecond):
	}
	second.Release()
	select {
	case <-teardownDone:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for teardown after guards released")
	}
}

func TestRuntimeGuardRetireTeardownDoesNotClearFreshActiveRuntime(t *testing.T) {
	registry := NewRuntimeRegistry()
	sessionID := "session-1"
	var oldEngine *runtime.Engine
	oldEngine = newRegistryTestRuntime(t, func(evt runtime.Event) {
		registry.PublishRuntimeEventForEngine(sessionID, oldEngine, evt)
	})
	oldClaim, _, _ := registry.AcquireRuntimeClaim(sessionID, "owner-a")
	oldTeardownDone := make(chan struct{})
	oldClaim.Resolve(oldEngine, nil, func() { close(oldTeardownDone) })
	first, err := registry.BeginRuntimeGuard(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("BeginRuntimeGuard first: %v", err)
	}
	second, err := registry.BeginRuntimeGuard(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("BeginRuntimeGuard second: %v", err)
	}
	publishRunState(registry, sessionID, true)
	if !registrySessionAggregateActive(registry, sessionID) {
		t.Fatal("expected old active runtime to mark session active")
	}
	if err := first.Retire(runtime.QueuedUserMessageFailureRuntimeUnavailable); err != nil {
		t.Fatalf("Retire: %v", err)
	}
	if registrySessionAggregateActive(registry, sessionID) {
		t.Fatal("expected retired old runtime to clear session active state before fresh runtime starts")
	}
	var freshEngine *runtime.Engine
	freshEngine = newRegistryTestRuntime(t, func(evt runtime.Event) {
		registry.PublishRuntimeEventForEngine(sessionID, freshEngine, evt)
	})
	freshClaim, reused, closing := registry.AcquireRuntimeClaim(sessionID, "owner-b")
	if freshClaim == nil || reused || closing {
		t.Fatalf("fresh claim = (%v, reused=%t, closing=%t), want new claim", freshClaim, reused, closing)
	}
	if ok := freshClaim.Resolve(freshEngine, nil, func() { _ = freshEngine.Close() }); !ok {
		t.Fatal("fresh claim resolve failed")
	}
	if current := registry.directory.Entry(sessionID); current == nil || current == oldClaim.entry {
		t.Fatalf("fresh entry not current after resolve: current=%p old=%p", current, oldClaim.entry)
	}
	publishRunState(registry, sessionID, true)
	if !registrySessionAggregateActive(registry, sessionID) {
		t.Fatal("expected fresh active runtime to mark session active")
	}
	first.Release()
	second.Release()
	waitClosed(t, oldTeardownDone)
	if !registrySessionAggregateActive(registry, sessionID) {
		t.Fatal("old teardown cleared fresh active runtime state")
	}
	closeRuntime(registry, sessionID, freshEngine)
	if registrySessionAggregateActive(registry, sessionID) {
		t.Fatal("expected fresh runtime close to clear session active state")
	}
}

func registrySessionAggregateActive(registry *RuntimeRegistry, sessionID string) bool {
	registry.runStateMu.Lock()
	defer registry.runStateMu.Unlock()
	return registry.blockingActivitySessions[sessionID]
}

func TestRuntimeClaimRejectsOwnerlessLeaseMutation(t *testing.T) {
	registry := NewRuntimeRegistry()
	engine := &runtime.Engine{}
	claim, _, _ := registry.AcquireRuntimeClaim("session-1", "")
	claim.Resolve(engine, nil, nil)

	if claim.OwnerCount() != 0 {
		t.Fatalf("owner count = %d, want no anonymous owner ref", claim.OwnerCount())
	}
	if outcome, err := claim.JoinAsOwner(" "); outcome != ClaimFailed || !errors.Is(err, ErrRuntimeOwnerIDRequired) {
		t.Fatalf("JoinAsOwner empty = (%v, %v), want owner-id-required failure", outcome, err)
	}
	if decision, _ := claim.BeginRelease("", true, true); decision != RuntimeReleaseNotOwner {
		t.Fatalf("BeginRelease empty owner = %v, want not-owner", decision)
	}
}

func TestRuntimeClaimIdleReleaseDropsOwnerBeforeCloseRace(t *testing.T) {
	registry := NewRuntimeRegistry()
	engine := &runtime.Engine{}
	claim, _, _ := registry.AcquireRuntimeClaim("session-1", "owner-a")
	claim.Resolve(engine, nil, nil)

	decision, expectedRefs := claim.BeginRelease("owner-a", true, true)
	if decision != RuntimeReleaseIdleCheck || expectedRefs != 0 {
		t.Fatalf("BeginRelease owner-a decision=%v refs=%d, want idle check with zero refs", decision, expectedRefs)
	}
	if _, err := claim.JoinAsOwner("owner-b"); err != nil {
		t.Fatalf("JoinAsOwner owner-b: %v", err)
	}
	closed, err := claim.CloseIfIdle(context.Background(), expectedRefs, nil)
	if err != nil {
		t.Fatalf("CloseIfIdle owner-a race: %v", err)
	}
	if closed {
		t.Fatal("CloseIfIdle must lose after a new owner joins")
	}

	decision, expectedRefs = claim.BeginRelease("owner-b", true, true)
	if decision != RuntimeReleaseIdleCheck || expectedRefs != 0 {
		t.Fatalf("BeginRelease owner-b decision=%v refs=%d, want idle check with zero refs", decision, expectedRefs)
	}
	closed, err = claim.CloseIfIdle(context.Background(), expectedRefs, nil)
	if err != nil {
		t.Fatalf("CloseIfIdle owner-b: %v", err)
	}
	if !closed {
		t.Fatal("runtime should close after the last real owner releases")
	}
}

func TestRuntimeClaimResolveRejectsClosedClaim(t *testing.T) {
	registry := NewRuntimeRegistry()
	claim, _, _ := registry.AcquireRuntimeClaim("session-1", "owner-a")
	closed, err := claim.Close(context.Background(), nil)
	if err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !closed {
		t.Fatal("expected building claim close to remove the entry")
	}
	if ok := claim.Resolve(&runtime.Engine{}, nil, nil); ok {
		t.Fatal("Resolve must reject a claim that was closed before build completed")
	}
	if registry.IsSessionRuntimeActive("session-1") {
		t.Fatal("closed stale build must not become active")
	}
}

func TestRuntimeRegistrySubmitPromptResponseRejectedWhileClosing(t *testing.T) {
	registry := NewRuntimeRegistry()
	engine := &runtime.Engine{}
	registerReady(t, registry, "session-1", engine)
	t.Cleanup(func() { closeRuntime(registry, "session-1", engine) })

	registry.directory.Entry("session-1").markClosing()

	if err := registry.SubmitPromptResponse("session-1", askquestion.AskQuestionResponse{RequestID: "ask-1", Answer: "yes"}, nil); !errors.Is(err, serverapi.ErrPromptNotFound) {
		t.Fatalf("SubmitPromptResponse error=%v, want ErrPromptNotFound for stale closing response", err)
	}
}

func TestRuntimeRegistrySubmitPromptResponseAllowedForClosingDrainPrompt(t *testing.T) {
	registry := NewRuntimeRegistry()
	engine := &runtime.Engine{}
	registerReady(t, registry, "session-1", engine)

	claim := registry.RuntimeClaimFor("session-1")
	drainStarted := make(chan struct{})
	closeDone := make(chan error, 1)
	go func() {
		_, err := claim.Close(context.Background(), func(ctx context.Context) error {
			close(drainStarted)
			resp, err := registry.AwaitPromptResponse(ctx, "session-1", askquestion.AskQuestionRequest{ID: "ask-1", Question: "Proceed?"})
			if err != nil {
				return err
			}
			if resp.Answer != "yes" {
				return fmt.Errorf("prompt answer = %q, want yes", resp.Answer)
			}
			return nil
		})
		closeDone <- err
	}()

	select {
	case <-drainStarted:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for close drain")
	}
	deadline := time.Now().Add(time.Second)
	for {
		items := registry.ListPendingPrompts("session-1")
		if len(items) == 1 && items[0].Request.ID == "ask-1" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("pending drain prompt was not published: %+v", items)
		}
		time.Sleep(10 * time.Millisecond)
	}

	if err := registry.SubmitPromptResponse("session-1", askquestion.AskQuestionResponse{RequestID: "ask-1", Answer: "yes"}, nil); err != nil {
		t.Fatalf("SubmitPromptResponse while closing drain waits: %v", err)
	}
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("close drain: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("close drain hung after prompt response")
	}
}

func TestRuntimeRegistryAwaitPromptResponseContextCanceledRemovesPendingPrompt(t *testing.T) {
	registry := NewRuntimeRegistry()
	engine := &runtime.Engine{}
	registerReady(t, registry, "session-1", engine)
	t.Cleanup(func() { closeRuntime(registry, "session-1", engine) })

	sub, err := registry.SubscribePromptActivity(context.Background(), "session-1")
	if err != nil {
		t.Fatalf("SubscribePromptActivity: %v", err)
	}
	defer func() { _ = sub.Close() }()
	if _, err := sub.Next(context.Background()); err != nil {
		t.Fatalf("initial snapshot Next: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = registry.AwaitPromptResponse(ctx, "session-1", askquestion.AskQuestionRequest{ID: "ask-1", Question: "Proceed?"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("AwaitPromptResponse error=%v, want context.Canceled", err)
	}
	if items := registry.ListPendingPrompts("session-1"); len(items) != 0 {
		t.Fatalf("expected canceled prompt removed, got %+v", items)
	}
	if err := registry.SubmitPromptResponse("session-1", askquestion.AskQuestionResponse{RequestID: "ask-1", Answer: "late"}, nil); !errors.Is(err, serverapi.ErrPromptNotFound) {
		t.Fatalf("late SubmitPromptResponse error=%v, want ErrPromptNotFound", err)
	}
	pending, err := sub.Next(context.Background())
	if err != nil {
		t.Fatalf("pending Next: %v", err)
	}
	if pending.Type != clientui.PendingPromptEventPending || pending.PromptID != "ask-1" {
		t.Fatalf("pending event=%+v, want ask-1 pending", pending)
	}
	resolved, err := sub.Next(context.Background())
	if err != nil {
		t.Fatalf("resolved Next: %v", err)
	}
	if resolved.Type != clientui.PendingPromptEventResolved || resolved.PromptID != "ask-1" {
		t.Fatalf("resolved event=%+v, want ask-1 resolved", resolved)
	}
}

func TestPendingPromptStoreCloseDoesNotBlockWhenResponseAlreadyBuffered(t *testing.T) {
	store := newPendingPromptStore()
	pending := &pendingPromptEntry{
		PendingPromptSnapshot: PendingPromptSnapshot{Request: askquestion.AskQuestionRequest{ID: "ask-1"}},
		response:              make(chan promptResponseResult, 1),
	}
	pending.response <- promptResponseResult{response: askquestion.AskQuestionResponse{RequestID: "ask-1"}}
	store.pending["ask-1"] = pending

	done := make(chan struct{})
	go func() {
		store.Close(io.EOF)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("closePendingPrompts blocked with buffered response")
	}
}

func TestRuntimeRegistryBlockSessionRunsRefCounts(t *testing.T) {
	r := NewRuntimeRegistry()
	if r.SessionRunsBlocked("s1") {
		t.Fatal("s1 should start unblocked")
	}
	releaseA := r.BlockSessionRuns([]string{"s1", "s2"})
	releaseB := r.BlockSessionRuns([]string{"s1"})
	if !r.SessionRunsBlocked("s1") || !r.SessionRunsBlocked("s2") {
		t.Fatal("s1 and s2 should be blocked while exclusions are held")
	}
	releaseB()
	if !r.SessionRunsBlocked("s1") {
		t.Fatal("s1 should remain blocked while the first exclusion still holds it")
	}
	releaseA()
	if r.SessionRunsBlocked("s1") || r.SessionRunsBlocked("s2") {
		t.Fatal("all sessions should be unblocked after every exclusion is released")
	}
}

func TestRuntimeRegistryBeginSessionRunRejectedWhenBlocked(t *testing.T) {
	r := NewRuntimeRegistry()
	release := r.BlockSessionRuns([]string{"s1"})
	defer release()
	if _, ok := r.BeginSessionRun("s1"); ok {
		t.Fatal("BeginSessionRun must be rejected while the session is blocked")
	}
	releaseRun, ok := r.BeginSessionRun("s2")
	if !ok {
		t.Fatal("an unrelated session must not be blocked")
	}
	releaseRun()
}

func TestRuntimeRegistryBlockSessionRunsWaitsForInFlightStart(t *testing.T) {
	r := NewRuntimeRegistry()
	releaseRun, ok := r.BeginSessionRun("s1")
	if !ok {
		t.Fatal("BeginSessionRun should succeed when unblocked")
	}
	blocked := make(chan func(), 1)
	go func() {
		blocked <- r.BlockSessionRuns([]string{"s1"})
	}()
	deadline := time.After(time.Second)
	for !r.SessionRunsBlocked("s1") {
		select {
		case <-deadline:
			t.Fatal("BlockSessionRuns never registered the block")
		case <-time.After(10 * time.Millisecond):
		}
	}
	select {
	case <-blocked:
		t.Fatal("BlockSessionRuns must wait for the in-flight start to drain")
	default:
	}
	releaseRun()
	select {
	case release := <-blocked:
		release()
	case <-time.After(time.Second):
		t.Fatal("BlockSessionRuns must return once the in-flight start drains")
	}
}

func TestRuntimeRegistryBeginSessionRunRejectsParallelPreActiveStart(t *testing.T) {
	r := NewRuntimeRegistry()
	releaseFirst, ok := r.BeginSessionRun("s1")
	if !ok {
		t.Fatal("first BeginSessionRun should succeed")
	}
	if releaseSecond, ok := r.BeginSessionRun("s1"); ok {
		releaseSecond()
		t.Fatal("second BeginSessionRun should be rejected while first pre-active start is in flight")
	}
	blocked := make(chan func(), 1)
	go func() {
		blocked <- r.BlockSessionRuns([]string{"s1"})
	}()
	deadline := time.After(time.Second)
	for !r.SessionRunsBlocked("s1") {
		select {
		case <-deadline:
			t.Fatal("BlockSessionRuns never registered the block")
		case <-time.After(10 * time.Millisecond):
		}
	}
	releaseFirst()
	select {
	case release := <-blocked:
		release()
	case <-time.After(time.Second):
		t.Fatal("BlockSessionRuns must return after the only in-flight start drains")
	}
}

func TestRuntimeRegistryExclusiveStartRejectsNormalStartsUntilReleased(t *testing.T) {
	r := NewRuntimeRegistry()
	releaseExclusive, acquired, blocked := r.BeginExclusiveSessionRun("s1")
	if !acquired || blocked {
		t.Fatalf("BeginExclusiveSessionRun = acquired %t blocked %t, want acquired", acquired, blocked)
	}
	if _, ok := r.BeginSessionRun("s1"); ok {
		t.Fatal("normal start must be rejected while exclusive start is reserved")
	}
	releaseExclusive()
	releaseNormal, ok := r.BeginSessionRun("s1")
	if !ok {
		t.Fatal("normal start should succeed after exclusive reservation release")
	}
	releaseNormal()
}

func nextPromptEventForTest(t *testing.T, sub serverapi.PromptActivitySubscription) clientui.PendingPromptEvent {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	evt, err := sub.Next(ctx)
	if err != nil {
		t.Fatalf("prompt Next: %v", err)
	}
	return evt
}
