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

func registerReady(t *testing.T, r *RuntimeRegistry, sessionID string, engine *runtime.Engine) {
	t.Helper()
	claim, _, _ := r.AcquireRuntimeClaim(sessionID, "")
	if claim == nil {
		t.Fatalf("AcquireRuntimeClaim(%q) returned nil claim", sessionID)
	}
	claim.Resolve(engine, nil, nil)
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

func TestRuntimeRegistryNotifiesSleepObserverFromRunStateEvents(t *testing.T) {
	registry := NewRuntimeRegistry()
	engine := &runtime.Engine{}
	registerReady(t, registry, "session-1", engine)
	defer closeRuntime(registry, "session-1", engine)

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
	runningCount := len(registry.runningSessions)
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
	registerReady(t, registry, "session-1", engine)
	t.Cleanup(func() { closeRuntime(registry, "session-1", engine) })

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

func TestRuntimeRegistrySubmitPromptResponseRejectedWhileClosing(t *testing.T) {
	registry := NewRuntimeRegistry()
	engine := &runtime.Engine{}
	registerReady(t, registry, "session-1", engine)
	t.Cleanup(func() { closeRuntime(registry, "session-1", engine) })

	registry.directory.Entry("session-1").markClosing()

	if err := registry.SubmitPromptResponse("session-1", askquestion.AskQuestionResponse{RequestID: "ask-1", Answer: "yes"}, nil); err == nil {
		t.Fatal("SubmitPromptResponse must be rejected while the runtime is closing")
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
