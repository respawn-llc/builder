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
	askquestion "core/server/tools"
	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

type registryRuntimeFakeClient struct{}

const (
	registryTestRunID  = "11111111-1111-4111-8111-111111111111"
	registryTestStepID = "22222222-2222-4222-8222-222222222222"
)

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
	return newRegistryRuntime(t, registryRuntimeFakeClient{}, askquestion.NewRegistry(), runtime.Config{Model: "gpt-5", ThinkingLevel: "medium"}, func(_ *runtime.Engine, evt runtime.Event) {
		if onEvent != nil {
			onEvent(evt)
		}
	})
}

func newRegistryRuntime(t *testing.T, client llm.Client, toolRegistry *askquestion.Registry, cfg runtime.Config, onEvent func(*runtime.Engine, runtime.Event)) *runtime.Engine {
	t.Helper()
	store := newRegistryTestSession(t, t.TempDir(), "workspace", t.TempDir())
	var engine *runtime.Engine
	cfg.OnEvent = func(evt runtime.Event) {
		if onEvent != nil {
			onEvent(engine, evt)
		}
	}
	engine, err := runtime.New(store, client, toolRegistry, cfg)
	if err != nil {
		t.Fatalf("new runtime: %v", err)
	}
	t.Cleanup(func() { _ = engine.Close() })
	return engine
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
		Kind:   runtime.EventRunStateChanged,
		StepID: registryTestStepID,
		RunState: &runtime.RunState{
			Lifecycle:  runtime.FinishedRunLifecycle(runtime.RunModeTurn),
			RunID:      registryTestStepID,
			ActiveKind: runtime.ActiveKindUserTurn,
			Status:     runtime.RunStatusCompleted,
		},
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

func waitClosed(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for channel close")
	}
}

func publishRunState(registry *RuntimeRegistry, sessionID string, running bool) {
	activity := clientui.RuntimeActivity{
		State:          clientui.RuntimeActivityRegisteredIdle,
		QueueAccepting: true,
	}
	if running {
		runID, err := runtimeids.ParseRunID(registryTestRunID)
		if err != nil {
			panic(err)
		}
		stepID, err := runtimeids.ParseStepID(registryTestStepID)
		if err != nil {
			panic(err)
		}
		activity = clientui.RuntimeActivity{
			State: clientui.RuntimeActivityRunning,
			ActiveStep: &clientui.RuntimeActiveStep{
				RunID:      runID,
				StepID:     stepID,
				ActiveKind: clientui.RuntimeActivityActiveKindUserTurn,
			},
			QueueAccepting: true,
		}
	}
	version := runtimeactivity.NextReadModelVersion(sessionID)
	registry.PublishRuntimeReadModelUpdate(sessionID, clientui.RuntimeReadModelUpdate{
		Version:             version,
		Activity:            activity,
		InputReconciliation: clientui.RuntimeInputReconciliationSnapshot{},
	})
}

func TestRuntimeRegistryTracksPendingPromptsPerSession(t *testing.T) {
	registry := NewRuntimeRegistry()
	engine := &runtime.Engine{}
	registerReady(t, registry, "session-1", engine)
	t.Cleanup(func() { closeRuntime(registry, "session-1", engine) })

	registry.BeginPendingPrompt("session-1", askquestion.AskQuestionRequest{ID: "ask-1", StepID: registryTestStepID, Question: "one?"})
	registry.BeginPendingPrompt("session-1", askquestion.AskQuestionRequest{ID: "approval-1", StepID: registryTestStepID, Question: "allow?"})

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

func TestRuntimeRegistryRejectsPromptWithoutStepIdentity(t *testing.T) {
	registry := NewRuntimeRegistry()
	engine := &runtime.Engine{}
	registerReady(t, registry, "session-1", engine)
	t.Cleanup(func() { closeRuntime(registry, "session-1", engine) })

	_, err := registry.AwaitPromptResponse(context.Background(), "session-1", askquestion.AskQuestionRequest{
		ID:       "approval-1",
		Question: "Approve?",
		Approval: true,
		ApprovalOptions: []askquestion.AskQuestionApprovalOption{
			{Decision: askquestion.AskQuestionApprovalDecisionAllowOnce, Label: "Allow once"},
			{Decision: askquestion.AskQuestionApprovalDecisionDeny, Label: "Deny"},
		},
	})
	if err == nil {
		t.Fatal("AwaitPromptResponse accepted a prompt without a step identity")
	}
	if prompts := registry.ListPendingPrompts("session-1"); len(prompts) != 0 {
		t.Fatalf("pending prompts = %+v, want none", prompts)
	}
}

func TestRuntimeRegistrySubmitPromptResponseRemovesPendingPromptBeforeWaiterReturns(t *testing.T) {
	registry := NewRuntimeRegistry()
	engine := &runtime.Engine{}
	registerReady(t, registry, "session-1", engine)
	t.Cleanup(func() { closeRuntime(registry, "session-1", engine) })

	responseDone := make(chan error, 1)
	go func() {
		_, err := registry.AwaitPromptResponse(context.Background(), "session-1", askquestion.AskQuestionRequest{ID: "ask-1", StepID: registryTestStepID, Question: "Proceed?"})
		responseDone <- err
	}()

	waitForPendingPrompt(t, registry, "session-1", "ask-1")

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
			StepID:   registryTestStepID,
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
	sub := subscribeTranscriptForTest(t, registry, sessionID)
	defer func() { _ = sub.Close() }()
	_ = nextTranscriptMessage(t, sub)
	clientRequestID := runtimeids.NewRuntimeClientRequestID()
	engine.QueueUserMessageWithClientRequestID("restore this", clientRequestID.String())
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
		message, err := sub.Next(ctx)
		if err != nil {
			t.Fatalf("Next queued failure: %v", err)
		}
		status := message.Payload.QueuedMessageState
		if status == nil || status.Status != clientui.QueuedUserMessageFailed {
			continue
		}
		if status.ClientRequestID != clientRequestID {
			t.Fatalf("client request id = %q, want %q", status.ClientRequestID.String(), clientRequestID.String())
		}
		if status.FailureReason == nil || *status.FailureReason != clientui.QueuedUserMessageFailureRuntimeUnavailable {
			t.Fatalf("failure reason = %v, want runtime_unavailable", status.FailureReason)
		}
		if status.Text == nil || *status.Text != "restore this" {
			t.Fatalf("restore text = %v, want restore this", status.Text)
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
			resp, err := registry.AwaitPromptResponse(ctx, "session-1", askquestion.AskQuestionRequest{ID: "ask-1", StepID: registryTestStepID, Question: "Proceed?"})
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
	waitForPendingPrompt(t, registry, "session-1", "ask-1")

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

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := registry.AwaitPromptResponse(ctx, "session-1", askquestion.AskQuestionRequest{ID: "ask-1", StepID: registryTestStepID, Question: "Proceed?"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("AwaitPromptResponse error=%v, want context.Canceled", err)
	}
	if items := registry.ListPendingPrompts("session-1"); len(items) != 0 {
		t.Fatalf("expected canceled prompt removed, got %+v", items)
	}
	if err := registry.SubmitPromptResponse("session-1", askquestion.AskQuestionResponse{RequestID: "ask-1", Answer: "late"}, nil); !errors.Is(err, serverapi.ErrPromptNotFound) {
		t.Fatalf("late SubmitPromptResponse error=%v, want ErrPromptNotFound", err)
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
