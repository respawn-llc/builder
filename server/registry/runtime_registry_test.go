package registry

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	"core/server/llm"
	"core/server/runtime"
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

func newRegistryTestRuntime(t *testing.T, onEvent func(runtime.Event)) *runtime.Engine {
	t.Helper()
	store, err := session.Create(t.TempDir(), "workspace", t.TempDir())
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	engine, err := runtime.New(store, registryRuntimeFakeClient{}, askquestion.NewRegistry(), runtime.Config{Model: "gpt-5", OnEvent: onEvent})
	if err != nil {
		t.Fatalf("new runtime: %v", err)
	}
	t.Cleanup(func() { _ = engine.Close() })
	return engine
}

func TestRuntimeRegistryBroadcastsSessionActivityToMultipleSubscribers(t *testing.T) {
	registry := NewRuntimeRegistry()
	engine := &runtime.Engine{}
	registry.Register("session-1", engine)
	t.Cleanup(func() { registry.Unregister("session-1", engine) })

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
	registry.Register("session-a", engineA)
	registry.Register("session-b", engineB)
	t.Cleanup(func() {
		registry.Unregister("session-a", engineA)
		registry.Unregister("session-b", engineB)
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
	registry.Register("session-1", engine)
	t.Cleanup(func() { registry.Unregister("session-1", engine) })

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
	registry.Register("session-1", engine)
	t.Cleanup(func() { registry.Unregister("session-1", engine) })

	registry.PublishRuntimeEvent("session-1", runtime.Event{Kind: runtime.EventConversationUpdated, StepID: "step-1"})
	registry.PublishRuntimeEvent("session-1", runtime.Event{Kind: runtime.EventRunStateChanged, StepID: "step-2"})

	sub, err := registry.SubscribeSessionActivityFrom(context.Background(), serverapi.SessionActivitySubscribeRequest{SessionID: "session-1", AfterSequence: 1})
	if err != nil {
		t.Fatalf("SubscribeSessionActivityFrom: %v", err)
	}
	defer func() { _ = sub.Close() }()

	evt, err := sub.Next(context.Background())
	if err != nil {
		t.Fatalf("Next replay: %v", err)
	}
	if evt.Sequence != 2 || evt.StepID != "step-2" {
		t.Fatalf("replay event = %+v, want sequence 2 step-2", evt)
	}
}

func TestRuntimeRegistryReportsRunFinishedInterestReason(t *testing.T) {
	registry := NewRuntimeRegistry()
	engine := &runtime.Engine{}
	registry.Register("session-1", engine)
	t.Cleanup(func() { registry.Unregister("session-1", engine) })
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

func TestRuntimeRegistryNotifiesSleepObserverFromRunStateEvents(t *testing.T) {
	registry := NewRuntimeRegistry()
	engine := &runtime.Engine{}
	registry.Register("session-1", engine)
	defer registry.Unregister("session-1", engine)

	notifications := make(chan bool, 2)
	registry.SetSleepObserver(func(active bool) {
		notifications <- active
	})

	registry.PublishRuntimeEvent("session-1", runtime.Event{
		Kind:     runtime.EventRunStateChanged,
		StepID:   "step-1",
		RunState: &runtime.RunState{Lifecycle: runtime.RunningRunLifecycle(runtime.RunModeTurn)},
	})
	registry.PublishRuntimeEvent("session-1", runtime.Event{
		Kind:     runtime.EventRunStateChanged,
		StepID:   "step-1",
		RunState: &runtime.RunState{Lifecycle: runtime.FinishedRunLifecycle(runtime.RunModeTurn)},
	})

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
	registry.Register("session-a", engineA)
	registry.Register("session-b", engineB)
	defer registry.Unregister("session-a", engineA)
	defer registry.Unregister("session-b", engineB)

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
	registry.Register("session-1", engine)
	defer registry.Unregister("session-1", engine)

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

func TestRuntimeRegistrySleepObserverUnregisterLastRunningSessionReportsIdle(t *testing.T) {
	registry := NewRuntimeRegistry()
	engine := &runtime.Engine{}
	registry.Register("session-1", engine)

	notifications := make(chan bool, 2)
	registry.SetSleepObserver(func(active bool) {
		notifications <- active
	})

	publishRunState(registry, "session-1", true)
	registry.Unregister("session-1", engine)

	if active := receiveSleepObserverState(t, notifications); !active {
		t.Fatal("expected aggregate active notification")
	}
	if active := receiveSleepObserverState(t, notifications); active {
		t.Fatal("expected aggregate idle notification after unregister")
	}
}

func TestRuntimeRegistryScopedGuardRejectsReplacementAndClosingEntries(t *testing.T) {
	registry := NewRuntimeRegistry()
	first := &runtime.Engine{}
	second := &runtime.Engine{}
	registry.Register("session-1", first)
	firstGuard, err := registry.BeginRuntimeGuard(context.Background(), "session-1")
	if err != nil {
		t.Fatalf("BeginRuntimeGuard first: %v", err)
	}
	if firstGuard.Engine() != first {
		t.Fatal("expected first guard to reference first engine")
	}

	replaced := make(chan struct{})
	go func() {
		registry.Register("session-1", second)
		close(replaced)
	}()
	assertNoClose(t, replaced)
	firstGuard.Release()
	waitClosed(t, replaced)

	replacementGuard, err := registry.BeginRuntimeGuard(context.Background(), "session-1")
	if err != nil {
		t.Fatalf("BeginRuntimeGuard replacement: %v", err)
	}
	replacementGuard.Release()
	registry.Unregister("session-1", second)
	if _, err := registry.BeginRuntimeGuard(context.Background(), "session-1"); err == nil {
		t.Fatal("expected guard for unregistered runtime to fail")
	}
}

func TestRuntimeRegistryReplacementDoesNotHoldDirectoryLockWhileWaitingForGuard(t *testing.T) {
	registry := NewRuntimeRegistry()
	first := &runtime.Engine{}
	second := &runtime.Engine{}
	other := &runtime.Engine{}
	registry.Register("session-1", first)
	guard, err := registry.BeginRuntimeGuard(context.Background(), "session-1")
	if err != nil {
		t.Fatalf("BeginRuntimeGuard: %v", err)
	}

	replaced := make(chan struct{})
	go func() {
		registry.Register("session-1", second)
		close(replaced)
	}()
	assertNoClose(t, replaced)

	registeredOther := make(chan struct{})
	go func() {
		registry.Register("session-2", other)
		close(registeredOther)
	}()
	waitClosed(t, registeredOther)

	guard.Release()
	waitClosed(t, replaced)
	registry.Unregister("session-1", second)
	registry.Unregister("session-2", other)
}

func TestRuntimeRegistryReplacementWaitsForCloseDrainOwnership(t *testing.T) {
	registry := NewRuntimeRegistry()
	first := &runtime.Engine{}
	second := &runtime.Engine{}
	registry.Register("session-1", first)

	drainStarted := make(chan struct{})
	releaseDrain := make(chan struct{})
	drainDone := make(chan struct{})
	go func() {
		err := registry.CloseRuntimeWithDrain(context.Background(), "session-1", first, func(context.Context) error {
			close(drainStarted)
			<-releaseDrain
			return nil
		})
		if err != nil {
			t.Errorf("CloseRuntimeWithDrain: %v", err)
		}
		close(drainDone)
	}()
	waitClosed(t, drainStarted)

	replaced := make(chan struct{})
	go func() {
		registry.Register("session-1", second)
		close(replaced)
	}()
	assertNoClose(t, replaced)

	close(releaseDrain)
	waitClosed(t, drainDone)
	waitClosed(t, replaced)
	if registry.directory.Resolve("session-1") != second {
		t.Fatal("expected replacement runtime after drain completes")
	}
	registry.Unregister("session-1", second)
}

func TestRuntimeRegistryUnregisterWaitsForCloseDrainOwnership(t *testing.T) {
	registry := NewRuntimeRegistry()
	engine := &runtime.Engine{}
	registry.Register("session-1", engine)

	drainStarted := make(chan struct{})
	releaseDrain := make(chan struct{})
	drainDone := make(chan struct{})
	go func() {
		err := registry.CloseRuntimeWithDrain(context.Background(), "session-1", engine, func(context.Context) error {
			close(drainStarted)
			<-releaseDrain
			return nil
		})
		if err != nil {
			t.Errorf("CloseRuntimeWithDrain: %v", err)
		}
		close(drainDone)
	}()
	waitClosed(t, drainStarted)

	unregistered := make(chan struct{})
	go func() {
		registry.Unregister("session-1", engine)
		close(unregistered)
	}()
	assertNoClose(t, unregistered)

	close(releaseDrain)
	waitClosed(t, drainDone)
	waitClosed(t, unregistered)
	if registry.IsSessionRuntimeActive("session-1") {
		t.Fatal("expected runtime to be inactive after drain and unregister complete")
	}
}

func TestRuntimeRegistryUnregisterWaitsForInFlightGuard(t *testing.T) {
	registry := NewRuntimeRegistry()
	engine := &runtime.Engine{}
	registry.Register("session-1", engine)
	guard, err := registry.BeginRuntimeGuard(context.Background(), "session-1")
	if err != nil {
		t.Fatalf("BeginRuntimeGuard: %v", err)
	}

	unregistered := make(chan struct{})
	go func() {
		registry.Unregister("session-1", engine)
		close(unregistered)
	}()
	assertNoClose(t, unregistered)
	if _, err := registry.BeginRuntimeGuard(context.Background(), "session-1"); err == nil {
		t.Fatal("expected closing runtime to reject new guarded mutation")
	}
	guard.Release()
	waitClosed(t, unregistered)
	if registry.IsSessionRuntimeActive("session-1") {
		t.Fatal("expected runtime to unregister after guard release")
	}
}

func TestRuntimeRegistrySleepObserverReplacingRunningSessionReportsIdleOnce(t *testing.T) {
	registry := NewRuntimeRegistry()
	first := &runtime.Engine{}
	second := &runtime.Engine{}
	registry.Register("session-1", first)
	defer registry.Unregister("session-1", second)

	notifications := make(chan bool, 4)
	registry.SetSleepObserver(func(active bool) {
		notifications <- active
	})

	publishRunState(registry, "session-1", true)
	registry.Register("session-1", second)

	if active := receiveSleepObserverState(t, notifications); !active {
		t.Fatal("expected aggregate active notification")
	}
	if active := receiveSleepObserverState(t, notifications); active {
		t.Fatal("expected aggregate idle notification after runtime replacement")
	}
	assertNoSleepObserverState(t, notifications)
}

func TestRuntimeRegistrySleepObserverReplacingOneOfMultipleRunningSessionsStaysActive(t *testing.T) {
	registry := NewRuntimeRegistry()
	firstA := &runtime.Engine{}
	secondA := &runtime.Engine{}
	engineB := &runtime.Engine{}
	registry.Register("session-a", firstA)
	registry.Register("session-b", engineB)
	defer registry.Unregister("session-a", secondA)
	defer registry.Unregister("session-b", engineB)

	notifications := make(chan bool, 4)
	registry.SetSleepObserver(func(active bool) {
		notifications <- active
	})

	publishRunState(registry, "session-a", true)
	if active := receiveSleepObserverState(t, notifications); !active {
		t.Fatal("expected aggregate active notification")
	}
	publishRunState(registry, "session-b", true)
	registry.Register("session-a", secondA)

	assertNoSleepObserverState(t, notifications)
}

func TestRuntimeRegistrySleepObserverConcurrentRunStateUpdates(t *testing.T) {
	registry := NewRuntimeRegistry()
	for i := 0; i < 20; i++ {
		id := fmt.Sprintf("session-%d", i)
		registry.Register(id, &runtime.Engine{})
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
	runningCount := len(registry.runningSessions)
	registry.runStateMu.Unlock()
	if runningCount != 0 {
		t.Fatalf("running session count = %d, want 0", runningCount)
	}
}

func TestRuntimeRegistrySleepObserverNotificationsDoNotOvertakeAggregateUpdates(t *testing.T) {
	registry := NewRuntimeRegistry()
	engine := &runtime.Engine{}
	registry.Register("session-1", engine)
	defer registry.Unregister("session-1", engine)

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
	lifecycle := runtime.FinishedRunLifecycle(runtime.RunModeTurn)
	if running {
		lifecycle = runtime.RunningRunLifecycle(runtime.RunModeTurn)
	}
	registry.PublishRuntimeEvent(sessionID, runtime.Event{
		Kind:     runtime.EventRunStateChanged,
		StepID:   "step-1",
		RunState: &runtime.RunState{Lifecycle: lifecycle},
	})
}

func TestRuntimeRegistryDeliversReplayBeforePostSubscribeLiveEvents(t *testing.T) {
	registry := NewRuntimeRegistry()
	engine := &runtime.Engine{}
	registry.Register("session-1", engine)
	t.Cleanup(func() { registry.Unregister("session-1", engine) })

	registry.PublishRuntimeEvent("session-1", runtime.Event{Kind: runtime.EventConversationUpdated, StepID: "step-1"})
	registry.PublishRuntimeEvent("session-1", runtime.Event{Kind: runtime.EventRunStateChanged, StepID: "step-2"})

	sub, err := registry.SubscribeSessionActivityFrom(context.Background(), serverapi.SessionActivitySubscribeRequest{SessionID: "session-1", AfterSequence: 1})
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
	if replay.Sequence != 2 || replay.StepID != "step-2" {
		t.Fatalf("replay event = %+v, want sequence 2 step-2", replay)
	}
	if live.Sequence != 3 || live.StepID != "step-3" {
		t.Fatalf("live event = %+v, want sequence 3 step-3", live)
	}
}

func TestRuntimeRegistryRejectsExpiredSessionActivityCursor(t *testing.T) {
	registry := NewRuntimeRegistry()
	engine := &runtime.Engine{}
	registry.Register("session-1", engine)
	t.Cleanup(func() { registry.Unregister("session-1", engine) })

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
	registry.Register("session-1", engine)
	t.Cleanup(func() { registry.Unregister("session-1", engine) })

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

	registry.Unregister("session-1", engine)
	if items := registry.ListPendingPrompts("session-1"); len(items) != 0 {
		t.Fatalf("expected no pending prompts after unregister, got %+v", items)
	}
}

func TestRuntimeRegistrySubscribePromptActivityReplaysAllPendingPromptsBeyondBufferLimit(t *testing.T) {
	registry := NewRuntimeRegistry()
	engine := &runtime.Engine{}
	registry.Register("session-1", engine)
	t.Cleanup(func() { registry.Unregister("session-1", engine) })

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
	registry.Register("session-1", engine)
	t.Cleanup(func() { registry.Unregister("session-1", engine) })

	entry := registry.directory.Entry("session-1")
	if entry == nil {
		t.Fatal("registered runtime entry not found")
	}

	promptStarted := make(chan struct{})
	promptDone := make(chan struct{})
	sub, err := entry.SubscribePromptActivityInitial("session-1", func() {
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
	pending, err := sub.Next(context.Background())
	if err != nil {
		t.Fatalf("pending Next: %v", err)
	}
	if pending.Type != clientui.PendingPromptEventPending || pending.PromptID != "ask-during-subscribe" || pending.Question != "Proceed?" {
		t.Fatalf("pending event = %+v, want ask-during-subscribe", pending)
	}
}

func TestPromptActivitySubscriptionCloseStopsInitialReplay(t *testing.T) {
	sub, err := newPromptActivityBroker().Subscribe([]clientui.PendingPromptEvent{
		{Type: clientui.PendingPromptEventPending, SessionID: "session-1", PromptID: "ask-1"},
		{Type: clientui.PendingPromptEventSnapshot, SessionID: "session-1"},
	}, 0)
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

func TestRuntimeRegistrySubmitPromptResponseRemovesPendingPromptBeforeWaiterReturns(t *testing.T) {
	registry := NewRuntimeRegistry()
	engine := &runtime.Engine{}
	registry.Register("session-1", engine)
	t.Cleanup(func() { registry.Unregister("session-1", engine) })

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

func TestRuntimeRegistryDoesNotUnregisterNewerRuntimeForSameSession(t *testing.T) {
	registry := NewRuntimeRegistry()
	older := &runtime.Engine{}
	newer := &runtime.Engine{}
	registry.Register("session-1", older)
	registry.Register("session-1", newer)
	t.Cleanup(func() { registry.Unregister("session-1", newer) })

	registry.Unregister("session-1", older)

	resolved, err := registry.ResolveRuntime(context.Background(), "session-1")
	if err != nil {
		t.Fatalf("ResolveRuntime: %v", err)
	}
	if resolved != newer {
		t.Fatalf("expected newer runtime to remain registered, got %p want %p", resolved, newer)
	}

	if _, err := registry.SubscribeSessionActivity(context.Background(), "session-1"); err != nil {
		t.Fatalf("SubscribeSessionActivity after stale unregister: %v", err)
	}
}

func TestRuntimeRegistryAwaitPromptResponseContextCanceledRemovesPendingPrompt(t *testing.T) {
	registry := NewRuntimeRegistry()
	engine := &runtime.Engine{}
	registry.Register("session-1", engine)
	t.Cleanup(func() { registry.Unregister("session-1", engine) })

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

func TestRuntimeRegistryBeginRuntimeGuardUsesCurrentEntryAfterReplacement(t *testing.T) {
	registry := NewRuntimeRegistry()
	older := &runtime.Engine{}
	newer := &runtime.Engine{}
	registry.Register("session-1", older)
	registry.Register("session-1", newer)
	t.Cleanup(func() { registry.Unregister("session-1", newer) })

	guard, err := registry.BeginRuntimeGuard(context.Background(), "session-1")
	if err != nil {
		t.Fatalf("BeginRuntimeGuard: %v", err)
	}
	defer guard.Release()
	if guard.Engine() != newer {
		t.Fatalf("guard engine = %p, want current replacement %p", guard.Engine(), newer)
	}
}

func TestRuntimeRegistryRegisterFailsPreviousQueuedMessagesOnReplacement(t *testing.T) {
	registry := NewRuntimeRegistry()
	sessionID := "session-1"
	older := newRegistryTestRuntime(t, func(evt runtime.Event) {
		registry.PublishRuntimeEvent(sessionID, evt)
	})
	newer := newRegistryTestRuntime(t, func(evt runtime.Event) {
		registry.PublishRuntimeEvent(sessionID, evt)
	})
	registry.Register(sessionID, older)
	oldSub, err := registry.SubscribeSessionActivity(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("SubscribeSessionActivity old: %v", err)
	}
	defer func() { _ = oldSub.Close() }()
	queued := older.QueueUserMessageWithClientRequestID("restore me", "req-replace")

	registry.Register(sessionID, newer)
	t.Cleanup(func() { registry.Unregister(sessionID, newer) })

	if older.HasQueuedUserWork() {
		t.Fatal("previous runtime kept queued work after replacement")
	}
	statuses := make([]clientui.QueuedUserMessageStatusEvent, 0, 2)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for {
		evt, err := oldSub.Next(ctx)
		if err != nil {
			t.Fatalf("old subscription closed before replacement queued failure: %v; statuses=%+v", err, statuses)
		}
		if evt.QueuedUserMessageStatus == nil {
			continue
		}
		statuses = append(statuses, *evt.QueuedUserMessageStatus)
		if evt.QueuedUserMessageStatus.Status == clientui.QueuedUserMessageFailed {
			break
		}
	}
	if len(statuses) != 2 || statuses[0].Status != clientui.QueuedUserMessageAccepted || statuses[1].Status != clientui.QueuedUserMessageFailed {
		t.Fatalf("queued statuses = %+v, want accepted then failed", statuses)
	}
	if statuses[1].QueueItemID != queued.ID || statuses[1].ClientRequestID != "req-replace" || statuses[1].RestoreText != "restore me" || statuses[1].FailureReason != clientui.QueuedUserMessageFailureClosing {
		t.Fatalf("failed status = %+v, want replacement close restore", statuses[1])
	}
	currentEntry := registry.directory.Entry(sessionID)
	if currentEntry == nil || currentEntry.sessionActivity == nil {
		t.Fatal("replacement session activity entry is unavailable")
	}
	for _, evt := range currentEntry.sessionActivity.history {
		if evt.QueuedUserMessageStatus != nil && evt.QueuedUserMessageStatus.QueueItemID == queued.ID {
			t.Fatalf("replacement broker received stale previous queued status: %+v", evt.QueuedUserMessageStatus)
		}
	}
}

func TestRuntimeRegistryRegisterWaitsForOldGuardsBeforePublishingReplacement(t *testing.T) {
	registry := NewRuntimeRegistry()
	older := &runtime.Engine{}
	newer := &runtime.Engine{}
	registry.Register("session-1", older)
	guard, err := registry.BeginRuntimeGuard(context.Background(), "session-1")
	if err != nil {
		t.Fatalf("BeginRuntimeGuard: %v", err)
	}
	registered := make(chan struct{})
	go func() {
		registry.Register("session-1", newer)
		close(registered)
	}()
	select {
	case <-registered:
		t.Fatal("replacement registered before old guard was released")
	case <-time.After(50 * time.Millisecond):
	}
	guard.Release()
	select {
	case <-registered:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for replacement registration")
	}
	resolved, err := registry.ResolveRuntime(context.Background(), "session-1")
	if err != nil {
		t.Fatalf("ResolveRuntime after replacement: %v", err)
	}
	if resolved != newer {
		t.Fatalf("runtime after replacement = %p, want newer %p", resolved, newer)
	}
	t.Cleanup(func() { registry.Unregister("session-1", newer) })
}

func TestRuntimeRegistryExternalRuntimeStatusReflectsRunStateAndCloseBarrier(t *testing.T) {
	registry := NewRuntimeRegistry()
	engine := &runtime.Engine{}
	registry.Register("session-1", engine)

	status := registry.ExternalRuntimeStatus("session-1")
	if status.State != clientui.ExternalRuntimeStateRegisteredIdle || !status.QueueAccepting {
		t.Fatalf("idle external status = %+v, want registered idle accepting", status)
	}
	publishRunState(registry, "session-1", true)
	status = registry.ExternalRuntimeStatus("session-1")
	if status.State != clientui.ExternalRuntimeStateOwnerRunning || !status.QueueAccepting {
		t.Fatalf("running external status = %+v, want owner running accepting", status)
	}
	publishRunState(registry, "session-1", false)
	guard, err := registry.BeginRuntimeGuard(context.Background(), "session-1")
	if err != nil {
		t.Fatalf("BeginRuntimeGuard: %v", err)
	}
	unregistered := make(chan struct{})
	go func() {
		registry.Unregister("session-1", engine)
		close(unregistered)
	}()
	waitForExternalRuntimeStatus(t, registry, "session-1", clientui.ExternalRuntimeStateClosing, false)
	guard.Release()
	select {
	case <-unregistered:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for unregister")
	}
	if status := registry.ExternalRuntimeStatus("session-1"); status.State != "" || status.QueueAccepting {
		t.Fatalf("removed external status after unregister = %+v, want zero", status)
	}

	drainEngine := &runtime.Engine{}
	registry.Register("session-1", drainEngine)

	started := make(chan struct{})
	releaseDrain := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- registry.CloseRuntimeWithDrain(context.Background(), "session-1", drainEngine, func(context.Context) error {
			close(started)
			<-releaseDrain
			return nil
		})
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for close drain")
	}
	status = registry.ExternalRuntimeStatus("session-1")
	if status.State != clientui.ExternalRuntimeStateDraining || status.QueueAccepting {
		t.Fatalf("draining external status = %+v, want draining not accepting", status)
	}
	close(releaseDrain)
	if err := <-done; err != nil {
		t.Fatalf("CloseRuntimeWithDrain: %v", err)
	}
	if status := registry.ExternalRuntimeStatus("session-1"); status.State != "" || status.QueueAccepting {
		t.Fatalf("removed external status = %+v, want zero", status)
	}
}

func waitForExternalRuntimeStatus(t *testing.T, registry *RuntimeRegistry, sessionID string, state clientui.ExternalRuntimeState, queueAccepting bool) {
	t.Helper()
	deadline := time.After(time.Second)
	for {
		status := registry.ExternalRuntimeStatus(sessionID)
		if status.State == state && status.QueueAccepting == queueAccepting {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("external runtime status = %+v, want state=%q queue=%t", status, state, queueAccepting)
		case <-time.After(10 * time.Millisecond):
		}
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
