package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"sync"
	"testing"
	"time"

	"core/server/attentionnotify"
	"core/server/llm"
	"core/server/runtime"
	"core/server/sessionruntime"
	askquestion "core/server/tools"
	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/textutil"
)

type registryRuntimeFakeClient struct{}

type registryBlockingClient struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func newRegistryBlockingClient() *registryBlockingClient {
	return &registryBlockingClient{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (c *registryBlockingClient) Generate(ctx context.Context, _ llm.Request, _ llm.StreamCallbacks) (llm.Response, error) {
	c.once.Do(func() { close(c.started) })
	select {
	case <-ctx.Done():
		return llm.Response{}, context.Cause(ctx)
	case <-c.release:
		return llm.Response{}, nil
	}
}

func (c *registryBlockingClient) ProviderCapabilities(context.Context) (llm.ProviderCapabilities, error) {
	return llm.ProviderCapabilities{ProviderID: "fake", SupportsResponsesAPI: true}, nil
}

type registryRetention chan struct{}

func (retention registryRetention) Close() error {
	close(retention)
	return nil
}

type registryBlockingRetention struct {
	closeStarted chan struct{}
	release      <-chan struct{}
}

func (retention *registryBlockingRetention) Close() error {
	close(retention.closeStarted)
	<-retention.release
	return nil
}

const (
	registryTestRunID  = "11111111-1111-4111-8111-111111111111"
	registryTestStepID = "22222222-2222-4222-8222-222222222222"
)

func registryTestResourceRef(sessionID string) runtimeids.SessionResourceRef {
	id, err := runtimeids.ParseSessionID(sessionID)
	if err != nil {
		panic(err)
	}
	ref, err := runtimeids.NewSessionResourceRef(id, 1)
	if err != nil {
		panic(err)
	}
	return ref
}

func registryTestResource(ref runtimeids.SessionResourceRef) sessionruntime.AgentResourceDescriptor {
	return sessionruntime.AgentResourceDescriptor{Ref: ref, State: sessionruntime.AgentResourceReady}
}

func projectPendingPromptForTest(registry *RuntimeRegistry, sessionID string, request askquestion.AskQuestionRequest) {
	projectPendingPromptResourceForTest(registry, registryTestResourceRef(sessionID), runtimeids.NewExecutionScopeID(), request, time.Now().UTC())
}

func resolvePendingPromptForTest(registry *RuntimeRegistry, sessionID string, requestID string) {
	for _, prompt := range registry.ListPendingPrompts(sessionID) {
		if prompt.Request.ID == requestID {
			resolvePendingPromptResourceForTest(registry, prompt.Resource, prompt.ScopeID, requestID)
			return
		}
	}
	panic("pending prompt not found")
}

func projectPendingPromptResourceForTest(
	registry *RuntimeRegistry,
	resource runtimeids.SessionResourceRef,
	scopeID runtimeids.ExecutionScopeID,
	request askquestion.AskQuestionRequest,
	createdAt time.Time,
) {
	id := resource.SessionID().String()
	projected := registry.withCurrentAuthorityEntry(resource, func(entry *authorityRuntimeEntry) bool {
		snapshot, admitted := registry.pendingPrompts.Begin(id, resource, scopeID, request, createdAt)
		if !admitted {
			return false
		}
		publishPendingPrompt(entry.sessionFeed, id, snapshot, pendingPromptEventPending)
		registry.publishAttentionPending(id, snapshot)
		return true
	})
	if projected {
		_ = registry.publishCurrentRuntimeActivity(id)
	}
}

func resolvePendingPromptResourceForTest(
	registry *RuntimeRegistry,
	resource runtimeids.SessionResourceRef,
	scopeID runtimeids.ExecutionScopeID,
	requestID string,
) {
	id := resource.SessionID().String()
	snapshot, resolved := registry.pendingPrompts.Complete(id, resource, scopeID, requestID)
	if !resolved {
		return
	}
	entry := registry.authorityEntryByRef(resource)
	if entry != nil {
		registry.publishPromptResolution(entry, id, snapshot)
	}
	_ = registry.publishCurrentRuntimeActivity(id)
}

func (registryRuntimeFakeClient) Generate(context.Context, llm.Request, llm.StreamCallbacks) (llm.Response, error) {
	return llm.Response{}, nil
}

func (registryRuntimeFakeClient) ProviderCapabilities(context.Context) (llm.ProviderCapabilities, error) {
	return llm.ProviderCapabilities{ProviderID: "fake", SupportsResponsesAPI: true}, nil
}

func registerResource(t *testing.T, registry *RuntimeRegistry, ref runtimeids.SessionResourceRef, engine *runtime.Engine) {
	t.Helper()
	if err := registry.ResourceReady(
		context.Background(),
		registryTestResource(ref),
		engine,
		func() (io.Closer, error) { return make(registryRetention), nil },
	); err != nil {
		t.Fatalf("register authority runtime resource %v: %v", ref, err)
	}
}

func registerReady(t *testing.T, registry *RuntimeRegistry, sessionID string, engine *runtime.Engine) {
	registerResource(t, registry, registryTestResourceRef(sessionID), engine)
}

func closeRuntime(registry *RuntimeRegistry, sessionID string, _ *runtime.Engine) {
	_ = registry.ResourceDraining(context.Background(), registryTestResource(registryTestResourceRef(sessionID)))
}

func TestSubscribeSessionTranscriptFailsExecutionTargetResolution(t *testing.T) {
	registry := NewRuntimeRegistry()
	engine := newRegistryTestRuntime(t, nil)
	registerReady(t, registry, engine.SessionID(), engine)
	registry.WithExecutionTargetResolver(func(context.Context, string) (*clientui.SessionExecutionTarget, error) {
		return nil, errors.New("execution target unavailable")
	})

	if _, err := registry.SubscribeSessionTranscript(context.Background(), serverapi.TranscriptSubscribeRequest{
		SessionID: engine.SessionID(),
	}); err == nil {
		t.Fatal("subscription succeeded despite execution-target resolution failure")
	}
}

func TestSubscriptionAndPromptResolutionWithPendingPromptDoNotDeadlock(t *testing.T) {
	registry := NewRuntimeRegistry()
	engine := newRegistryTestRuntime(t, nil)
	ref := registryTestResourceRef(engine.SessionID())
	registerResource(t, registry, ref, engine)
	scopeID := runtimeids.NewExecutionScopeID()
	projectPendingPromptResourceForTest(registry, ref, scopeID, askquestion.AskQuestionRequest{
		ID:       "ask-1",
		StepID:   registryTestStepID,
		Question: "Continue?",
	}, time.Now().UTC())

	hydrationResolverStarted := make(chan struct{})
	releaseHydrationResolver := make(chan struct{})
	registry.WithExecutionTargetResolver(func(context.Context, string) (*clientui.SessionExecutionTarget, error) {
		close(hydrationResolverStarted)
		<-releaseHydrationResolver
		return nil, nil
	})
	type subscriptionResult struct {
		sub serverapi.TranscriptSubscription
		err error
	}
	subscriptionDone := make(chan subscriptionResult, 1)
	go func() {
		sub, err := registry.SubscribeSessionTranscript(context.Background(), serverapi.TranscriptSubscribeRequest{
			SessionID: engine.SessionID(),
		})
		subscriptionDone <- subscriptionResult{sub: sub, err: err}
	}()
	<-hydrationResolverStarted
	resolutionDone := make(chan struct{})
	go func() {
		resolvePendingPromptResourceForTest(registry, ref, scopeID, "ask-1")
		close(resolutionDone)
	}()
	select {
	case <-resolutionDone:
		t.Fatal("prompt resolution completed before hydration registration")
	case <-time.After(20 * time.Millisecond):
	}
	close(releaseHydrationResolver)
	select {
	case result := <-subscriptionDone:
		if result.err != nil {
			t.Fatalf("subscribe: %v", result.err)
		}
		defer func() { _ = result.sub.Close() }()
		if message := nextTranscriptMessage(t, result.sub); message.Kind() != clientui.TranscriptMessageHydration {
			t.Fatalf("first message = %+v, want hydration", message)
		}
		var resolved bool
		for !resolved {
			message, err := result.sub.Next(context.Background())
			if err != nil {
				t.Fatalf("read after hydration: %v", err)
			}
			if message.Kind() == clientui.TranscriptMessagePrompt {
				prompt := transcriptPayload[clientui.TranscriptPrompt](t, message)
				resolved = prompt.Status == clientui.TranscriptPromptStatusResolved
			}
		}
	case <-time.After(time.Second):
		t.Fatal("subscription and prompt resolution deadlocked")
	}
	select {
	case <-resolutionDone:
	case <-time.After(time.Second):
		t.Fatal("prompt resolution did not complete")
	}
}

func TestRuntimeReadModelPublicationWaitsForHydrationAdmission(t *testing.T) {
	registry := NewRuntimeRegistry()
	engine := newRegistryTestRuntime(t, nil)
	registerReady(t, registry, engine.SessionID(), engine)
	update, err := registry.RuntimeReadModelFeedSnapshot(context.Background(), engine.SessionID())
	if err != nil {
		t.Fatalf("read runtime model: %v", err)
	}
	hydrationResolverStarted := make(chan struct{})
	releaseHydrationResolver := make(chan struct{})
	registry.WithExecutionTargetResolver(func(context.Context, string) (*clientui.SessionExecutionTarget, error) {
		close(hydrationResolverStarted)
		<-releaseHydrationResolver
		return nil, nil
	})
	subscriptionDone := make(chan error, 1)
	go func() {
		_, subscribeErr := registry.SubscribeSessionTranscript(context.Background(), serverapi.TranscriptSubscribeRequest{
			SessionID: engine.SessionID(),
		})
		subscriptionDone <- subscribeErr
	}()
	<-hydrationResolverStarted

	entry := registry.authorityEntryBySession(engine.SessionID())
	if entry == nil {
		t.Fatal("authority entry missing")
	}
	publicationAttempted := make(chan struct{})
	publicationDone := make(chan struct{})
	go func() {
		if entry.sessionFeed.mu.TryLock() {
			entry.sessionFeed.mu.Unlock()
			t.Error("sequencer was not held by hydration builder")
		}
		close(publicationAttempted)
		registry.PublishRuntimeReadModelUpdate(engine.SessionID(), update)
		close(publicationDone)
	}()
	<-publicationAttempted
	select {
	case <-publicationDone:
		t.Fatal("runtime read-model publication passed hydration admission")
	default:
	}
	close(releaseHydrationResolver)
	if err := <-subscriptionDone; err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	select {
	case <-publicationDone:
	case <-time.After(time.Second):
		t.Fatal("runtime read-model publication remained blocked after hydration")
	}
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
	eventLog, err := store.MaterializeEventLog()
	if err != nil {
		t.Fatalf("materialize event log: %v", err)
	}
	var engine *runtime.Engine
	cfg.OnEvent = func(evt runtime.Event) {
		if onEvent != nil {
			onEvent(engine, evt)
		}
	}
	engine, err = runtime.New(store, eventLog, client, toolRegistry, cfg)
	if err != nil {
		t.Fatalf("new runtime: %v", err)
	}
	t.Cleanup(func() { _ = engine.Close() })
	return engine
}

func TestAuthorityRuntimeDrainClosesSubscriptionsAndReleasesRetention(t *testing.T) {
	registry := NewRuntimeRegistry()
	engine := newRegistryTestRuntime(t, nil)
	retentionClosed := make(registryRetention)
	ref := registryTestResourceRef(engine.SessionID())
	if err := registry.ResourceReady(context.Background(), registryTestResource(ref), engine, func() (io.Closer, error) {
		return retentionClosed, nil
	}); err != nil {
		t.Fatalf("register authority runtime resource: %v", err)
	}
	sub, err := registry.SubscribeSessionTranscript(context.Background(), serverapi.TranscriptSubscribeRequest{SessionID: engine.SessionID()})
	if err != nil {
		t.Fatalf("subscribe authority transcript: %v", err)
	}
	if err := registry.ResourceDraining(context.Background(), registryTestResource(ref)); err != nil {
		t.Fatalf("drain authority runtime resource: %v", err)
	}
	var lastMessage clientui.TranscriptMessage
	var nextErr error
	for nextErr == nil {
		var message clientui.TranscriptMessage
		message, nextErr = sub.Next(context.Background())
		if nextErr == nil {
			lastMessage = message
		}
	}
	if !errors.Is(nextErr, io.EOF) {
		t.Fatalf("subscription close error = %v, want EOF", nextErr)
	}
	if lastMessage.Event().IsZero() {
		t.Fatalf("no transcript message was delivered before EOF")
	}
	if lastMessage.Kind() != clientui.TranscriptMessageRuntimeReadModelUpdate ||
		transcriptPayload[clientui.RuntimeReadModelUpdate](t, lastMessage).Activity.State != clientui.RuntimeActivityUnavailable {
		t.Fatalf("last transcript message before EOF = %+v, want unavailable runtime read-model update", lastMessage)
	}
	select {
	case <-retentionClosed:
	default:
		t.Fatal("registry drain did not release transcript retention")
	}
}

func TestRuntimeSnapshotsStopExposingRuntimeBeforeDrainCleanupCompletes(t *testing.T) {
	registry := NewRuntimeRegistry()
	engine := newRegistryTestRuntime(t, nil)
	ref := registryTestResourceRef(engine.SessionID())
	closeStarted := make(chan struct{})
	releaseClose := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseClose) }) }
	t.Cleanup(release)
	if err := registry.ResourceReady(
		context.Background(),
		registryTestResource(ref),
		engine,
		func() (io.Closer, error) {
			return &registryBlockingRetention{closeStarted: closeStarted, release: releaseClose}, nil
		},
	); err != nil {
		t.Fatalf("ResourceReady: %v", err)
	}
	registry.PublishRuntimeReadModelUpdate(
		engine.SessionID(),
		registryTestReadModelUpdate(t, 2, clientui.RuntimeActivityRunning),
	)
	subscription, err := registry.SubscribeSessionTranscript(context.Background(), serverapi.TranscriptSubscribeRequest{
		SessionID: engine.SessionID(),
	})
	if err != nil {
		t.Fatalf("SubscribeSessionTranscript: %v", err)
	}
	if _, err := subscription.Next(context.Background()); err != nil {
		t.Fatalf("read hydration: %v", err)
	}

	drainDone := make(chan error, 1)
	go func() {
		drainDone <- registry.ResourceDraining(context.Background(), registryTestResource(ref))
	}()
	select {
	case <-closeStarted:
	case <-time.After(time.Second):
		t.Fatal("Runtime drain did not reach retention cleanup")
	}
	if view, ok := registry.RuntimeMainViewSnapshot(engine.SessionID()); ok {
		t.Fatalf("Runtime Main View remained available during drain cleanup: %+v", view)
	}
	snapshots, err := registry.ActiveRuntimeActivitySnapshots(context.Background())
	if err != nil {
		t.Fatalf("ActiveRuntimeActivitySnapshots: %v", err)
	}
	if len(snapshots) != 0 {
		t.Fatalf("active snapshots during drain cleanup = %+v, want none", snapshots)
	}
	release()
	if err := <-drainDone; err != nil {
		t.Fatalf("ResourceDraining: %v", err)
	}
}

func TestSessionTranscriptSubscriptionWaitsForReplacementRuntime(t *testing.T) {
	registry := NewRuntimeRegistry()
	engine := newRegistryTestRuntime(t, nil)
	sessionID, err := runtimeids.ParseSessionID(engine.SessionID())
	if err != nil {
		t.Fatalf("parse runtime Session ID: %v", err)
	}
	firstRef, err := runtimeids.NewSessionResourceRef(sessionID, 1)
	if err != nil {
		t.Fatalf("first resource reference: %v", err)
	}
	registerResource(t, registry, firstRef, engine)
	first := subscribeTranscriptForTest(t, registry, engine.SessionID())
	_ = nextTranscriptMessage(t, first)
	if err := registry.ResourceDraining(context.Background(), registryTestResource(firstRef)); err != nil {
		t.Fatalf("drain first runtime resource: %v", err)
	}
	for {
		if _, err := first.Next(context.Background()); err != nil {
			if !errors.Is(err, io.EOF) {
				t.Fatalf("first subscription close error = %v, want EOF", err)
			}
			break
		}
	}

	type subscribeResult struct {
		sub serverapi.TranscriptSubscription
		err error
	}
	result := make(chan subscribeResult, 1)
	subscribeCtx, cancelSubscribe := context.WithCancel(context.Background())
	defer cancelSubscribe()
	go func() {
		sub, err := registry.SubscribeSessionTranscript(
			subscribeCtx,
			serverapi.TranscriptSubscribeRequest{SessionID: engine.SessionID()},
		)
		result <- subscribeResult{sub: sub, err: err}
	}()
	select {
	case got := <-result:
		t.Fatalf("replacement subscription returned before runtime wake: %+v", got)
	case <-time.After(25 * time.Millisecond):
	}

	secondRef, err := runtimeids.NewSessionResourceRef(sessionID, 2)
	if err != nil {
		t.Fatalf("second resource reference: %v", err)
	}
	registerResource(t, registry, secondRef, engine)
	t.Cleanup(func() {
		_ = registry.ResourceDraining(context.Background(), registryTestResource(secondRef))
	})
	var replacement serverapi.TranscriptSubscription
	select {
	case got := <-result:
		if got.err != nil {
			t.Fatalf("replacement subscription: %v", got.err)
		}
		replacement = got.sub
	case <-time.After(time.Second):
		t.Fatal("replacement runtime wake did not open transcript subscription")
	}
	defer func() { _ = replacement.Close() }()
	if hydration := nextTranscriptMessage(t, replacement); hydration.Kind() != clientui.TranscriptMessageHydration {
		t.Fatalf("replacement first message = %+v, want hydration", hydration)
	}
}

func TestSessionTranscriptSubscriptionRacingInitialRuntimeReadyHydrates(t *testing.T) {
	registry := NewRuntimeRegistry()
	engine := newRegistryTestRuntime(t, nil)
	type subscribeResult struct {
		sub serverapi.TranscriptSubscription
		err error
	}
	result := make(chan subscribeResult, 1)
	subscribeCtx, cancelSubscribe := context.WithCancel(context.Background())
	defer cancelSubscribe()
	go func() {
		sub, err := registry.SubscribeSessionTranscript(
			subscribeCtx,
			serverapi.TranscriptSubscribeRequest{SessionID: engine.SessionID()},
		)
		result <- subscribeResult{sub: sub, err: err}
	}()
	select {
	case got := <-result:
		t.Fatalf("initial subscription returned before runtime wake: %+v", got)
	case <-time.After(25 * time.Millisecond):
	}

	registerReady(t, registry, engine.SessionID(), engine)
	t.Cleanup(func() {
		_ = registry.ResourceDraining(
			context.Background(),
			registryTestResource(registryTestResourceRef(engine.SessionID())),
		)
	})
	var subscription serverapi.TranscriptSubscription
	select {
	case got := <-result:
		if got.err != nil {
			t.Fatalf("initial subscription after runtime wake: %v", got.err)
		}
		subscription = got.sub
	case <-time.After(time.Second):
		t.Fatal("initial runtime wake did not open transcript subscription")
	}
	defer func() { _ = subscription.Close() }()
	if hydration := nextTranscriptMessage(t, subscription); hydration.Kind() != clientui.TranscriptMessageHydration {
		t.Fatalf("initial first message = %+v, want hydration", hydration)
	}
}

func TestSessionTranscriptSubscriptionWaitStopsWithContext(t *testing.T) {
	registry := NewRuntimeRegistry()
	sessionID := runtimeids.NewSessionID().String()
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := registry.SubscribeSessionTranscript(
			ctx,
			serverapi.TranscriptSubscribeRequest{SessionID: sessionID},
		)
		result <- err
	}()
	select {
	case err := <-result:
		t.Fatalf("unavailable transcript subscription returned before cancellation: %v", err)
	case <-time.After(25 * time.Millisecond):
	}

	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled transcript subscription error = %v, want context cancellation", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled transcript subscription remained blocked")
	}
}

func TestSessionTranscriptSubscriptionRejectsMissingSession(t *testing.T) {
	registry := NewRuntimeRegistry()
	_, err := registry.SubscribeSessionTranscript(
		context.Background(),
		serverapi.TranscriptSubscribeRequest{},
	)
	if err == nil {
		t.Fatal("missing Session subscription did not fail")
	}
}

func TestAuthorityRuntimeDrainCannotRestoreAggregateActivityAfterTerminalState(t *testing.T) {
	registry := NewRuntimeRegistry()
	engine := newRegistryTestRuntime(t, nil)
	ref := registryTestResourceRef(engine.SessionID())
	retentionCloseStarted := make(chan struct{})
	retentionRelease := make(chan struct{})
	var releaseRetention sync.Once
	release := func() {
		releaseRetention.Do(func() { close(retentionRelease) })
	}
	t.Cleanup(release)
	if err := registry.ResourceReady(context.Background(), registryTestResource(ref), engine, func() (io.Closer, error) {
		return &registryBlockingRetention{closeStarted: retentionCloseStarted, release: retentionRelease}, nil
	}); err != nil {
		t.Fatalf("register authority runtime resource: %v", err)
	}
	sub := subscribeTranscriptForTest(t, registry, engine.SessionID())
	defer func() { _ = sub.Close() }()
	_ = nextTranscriptMessage(t, sub)
	notifications := make(chan bool, 3)
	registry.SetSleepObserver(func(active bool) { notifications <- active })
	defer registry.SetSleepObserver(nil)

	drainResult := make(chan error, 1)
	go func() {
		drainResult <- registry.ResourceDraining(context.Background(), registryTestResource(ref))
	}()
	select {
	case <-retentionCloseStarted:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for retention close")
	}
	if active := receiveSleepObserverState(t, notifications); !active {
		t.Fatal("expected draining runtime to activate aggregate activity")
	}
	if active := receiveSleepObserverState(t, notifications); active {
		t.Fatal("expected terminal runtime state to clear aggregate activity")
	}

	publishRunState(registry, engine.SessionID(), true)
	assertNoSleepObserverState(t, notifications)

	release()
	if err := <-drainResult; err != nil {
		t.Fatalf("drain authority runtime resource: %v", err)
	}
	assertNoSleepObserverState(t, notifications)
}

func TestAuthorityEventFeedProjectsExactResourceGeneration(t *testing.T) {
	registry := NewRuntimeRegistry()
	engine := newRegistryTestRuntime(t, nil)
	ref := registryTestResourceRef(engine.SessionID())
	registerReady(t, registry, engine.SessionID(), engine)
	t.Cleanup(func() { closeRuntime(registry, engine.SessionID(), engine) })
	sub := subscribeTranscriptForTest(t, registry, engine.SessionID())
	t.Cleanup(func() { _ = sub.Close() })
	_ = nextTranscriptMessage(t, sub)

	staleRef, err := runtimeids.NewSessionResourceRef(ref.SessionID(), ref.Generation()+1)
	if err != nil {
		t.Fatalf("new stale authority resource ref: %v", err)
	}
	registry.PublishAuthorityRuntimeEvent(staleRef, runtime.Event{
		Kind:                       runtime.EventAssistantMessage,
		StepID:                     registryTestStepID,
		Message:                    llm.Message{Role: llm.RoleAssistant, Phase: textutil.Value(llm.MessagePhaseFinal), Content: textutil.Value("stale authority event")},
		CommittedTranscriptChanged: true,
	})
	if message, nextErr := nextTranscriptMessageTimeout(sub, 20*time.Millisecond); nextErr == nil {
		t.Fatalf("stale authority generation projected transcript event: %+v", message)
	}
	registry.PublishAuthorityRuntimeEvent(ref, runtime.Event{
		Kind:                       runtime.EventAssistantMessage,
		StepID:                     registryTestStepID,
		Message:                    llm.Message{Role: llm.RoleAssistant, Phase: textutil.Value(llm.MessagePhaseFinal), Content: textutil.Value("authority event")},
		CommittedTranscriptChanged: true,
		CommittedProvenance:        &runtime.TranscriptCommittedRowProvenance{EventSequence: 1},
	})
	message := nextTranscriptMessage(t, sub)
	if message.Kind() != clientui.TranscriptMessageCommittedRow {
		t.Fatalf("authority event projection = %+v", message)
	}

	outcome := clientui.WorktreeTransitionOutcome{
		OperationID: clientui.NewWorktreeTransitionID(),
		Transition:  clientui.WorktreeTransitionEnter,
		State:       clientui.WorktreeTransitionCompleted,
	}
	registry.PublishWorktreeTransitionOutcome(engine.SessionID(), outcome)

	message = nextTranscriptMessageOfKind(t, sub, clientui.TranscriptMessageWorktreeTransitionOutcome)
	projected := transcriptPayload[clientui.TranscriptWorktreeTransitionOutcome](t, message)
	if projected.OperationID != outcome.OperationID {
		t.Fatalf("worktree transition projection = %+v, want %+v", projected, outcome)
	}

	dirtyCount := 2
	failed := clientui.WorktreeTransitionOutcome{
		OperationID: clientui.NewWorktreeTransitionID(),
		Transition:  clientui.WorktreeTransitionDelete,
		State:       clientui.WorktreeTransitionFailed,
		Failure: &clientui.WorktreeTransitionFailure{
			Diagnostic: "delete precondition",
			DeletePrecondition: &clientui.WorktreeDirtyState{
				Kind:           clientui.WorktreeDirtyStateDirty,
				DirtyFileCount: &dirtyCount,
			},
		},
	}
	registry.PublishWorktreeTransitionOutcome(engine.SessionID(), failed)
	message = nextTranscriptMessageOfKind(t, sub, clientui.TranscriptMessageWorktreeTransitionOutcome)
	projected = transcriptPayload[clientui.TranscriptWorktreeTransitionOutcome](t, message)
	if projected.DeletePrecondition == nil ||
		projected.DeletePrecondition.Kind != clientui.WorktreeDirtyStateDirty ||
		projected.DeletePrecondition.DirtyFileCount == nil ||
		*projected.DeletePrecondition.DirtyFileCount != dirtyCount {
		t.Fatalf("typed delete precondition projection = %+v, want dirty count %d", projected, dirtyCount)
	}
}

func TestTranscriptHydrationRetiresStepOwnedStateWhenCanonicalRuntimeBecomesIdle(t *testing.T) {
	registry := NewRuntimeRegistry()
	engine := newRegistryTestRuntime(t, nil)
	ref := registryTestResourceRef(engine.SessionID())
	registerReady(t, registry, engine.SessionID(), engine)
	t.Cleanup(func() { closeRuntime(registry, engine.SessionID(), engine) })

	publishRunState(registry, engine.SessionID(), true)
	if err := registry.PublishAuthorityRuntimeEvent(ref, runtime.Event{
		Kind:   runtime.EventRunStateChanged,
		StepID: registryTestStepID,
		RunState: &runtime.RunState{
			Lifecycle:  runtime.RunningRunLifecycle(runtime.RunModeTurn),
			RunID:      registryTestRunID,
			ActiveKind: runtime.ActiveKindUserTurn,
			Status:     runtime.RunStatusRunning,
			StartedAt:  time.Now().UTC(),
		},
	}); err != nil {
		t.Fatalf("publish running step state: %v", err)
	}
	if err := registry.PublishAuthorityRuntimeEvent(ref, runtime.Event{
		Kind:   runtime.EventReasoningDelta,
		StepID: registryTestStepID,
		ReasoningDelta: &llm.ReasoningSummaryDelta{
			SourceCoordinate: &llm.ReasoningSourceCoordinate{
				OutputIndex: func() *int64 { value := int64(0); return &value }(),
				PartIndex:   func() *int64 { value := int64(0); return &value }(),
			},
			ItemIdentity: func() *llm.ReasoningItemIdentity {
				part := int64(0)
				return &llm.ReasoningItemIdentity{ItemID: "planning", PartIndex: &part}
			}(),
			Text: "Planning the next action",
		},
		ReasoningTraceIdentity: &runtime.TranscriptReasoningTraceIdentity{
			Provider: func() *llm.ReasoningItemIdentity {
				part := int64(0)
				return &llm.ReasoningItemIdentity{ItemID: "planning", PartIndex: &part}
			}(),
		},
	}); err != nil {
		t.Fatalf("publish active reasoning: %v", err)
	}
	if err := registry.PublishAuthorityRuntimeEvent(ref, runtime.Event{
		Kind:   runtime.EventReviewerStarted,
		StepID: registryTestStepID,
	}); err != nil {
		t.Fatalf("publish active reviewer: %v", err)
	}
	if err := registry.PublishAuthorityRuntimeEvent(ref, runtime.Event{
		Kind:       runtime.EventCompactionStarted,
		StepID:     registryTestStepID,
		Compaction: &runtime.CompactionStatus{Mode: "auto", Count: 1},
	}); err != nil {
		t.Fatalf("publish active compaction: %v", err)
	}
	if err := registry.PublishAuthorityRuntimeEvent(ref, runtime.Event{
		Kind:   runtime.EventToolCallStarted,
		StepID: registryTestStepID,
		ToolCall: &llm.ToolCall{
			ID:    "tool-1",
			Name:  "exec_command",
			Input: json.RawMessage(`{"cmd":"sleep 1"}`),
		},
	}); err != nil {
		t.Fatalf("publish in-flight tool: %v", err)
	}
	publishRunState(registry, engine.SessionID(), false)

	sub := subscribeTranscriptForTest(t, registry, engine.SessionID())
	defer func() { _ = sub.Close() }()
	hydration := nextTranscriptMessage(t, sub)
	payload := transcriptPayload[clientui.TranscriptHydration](t, hydration)
	if payload.ActiveStep != nil {
		t.Fatalf(
			"hydrated active step = %+v, want none after canonical runtime became idle",
			payload.ActiveStep,
		)
	}
	if payload.ActiveThinkingStatus != nil || len(payload.ActiveReasoningTraces) != 0 {
		t.Fatalf(
			"hydrated active reasoning = status %+v traces %+v, want none after canonical runtime became idle",
			payload.ActiveThinkingStatus, payload.ActiveReasoningTraces,
		)
	}
	if payload.ActiveReviewer != nil {
		t.Fatalf(
			"hydrated active reviewer = %+v, want none after canonical runtime became idle",
			payload.ActiveReviewer,
		)
	}
	if payload.ActiveCompaction != nil {
		t.Fatalf(
			"hydrated active compaction = %+v, want none after canonical runtime became idle",
			payload.ActiveCompaction,
		)
	}
	if len(payload.InFlightTools) != 0 {
		t.Fatalf(
			"hydrated in-flight tools = %+v, want none after canonical runtime became idle",
			payload.InFlightTools,
		)
	}
}

func TestExecutionPromptProjectionRetainsExactAuthorityGeneration(t *testing.T) {
	registry := NewRuntimeRegistry()
	sessionID := runtimeids.NewSessionID()
	predecessor := registryTestResourceRef(sessionID.String())
	successor, err := runtimeids.NewSessionResourceRef(sessionID, 2)
	if err != nil {
		t.Fatalf("successor resource: %v", err)
	}
	predecessorScope := runtimeids.NewExecutionScopeID()
	successorScope := runtimeids.NewExecutionScopeID()
	request := askquestion.AskQuestionRequest{ID: "ask-1", StepID: registryTestStepID, Question: "Proceed?"}

	engine := &runtime.Engine{}
	registerResource(t, registry, predecessor, engine)
	projectPendingPromptResourceForTest(registry, predecessor, predecessorScope, request, time.Now().UTC())
	if err := registry.ResourceDraining(context.Background(), registryTestResource(predecessor)); err != nil {
		t.Fatalf("drain predecessor: %v", err)
	}
	registerResource(t, registry, successor, engine)
	t.Cleanup(func() { _ = registry.ResourceDraining(context.Background(), registryTestResource(successor)) })
	projectPendingPromptResourceForTest(registry, successor, successorScope, request, time.Now().UTC())
	projectPendingPromptResourceForTest(registry, predecessor, predecessorScope, request, time.Now().UTC())
	resolvePendingPromptResourceForTest(registry, predecessor, predecessorScope, request.ID)

	items := registry.ListPendingPrompts(sessionID.String())
	if len(items) != 1 || items[0].Resource != successor || items[0].ScopeID != successorScope {
		t.Fatalf("pending prompts after stale resolution = %+v, want exact successor", items)
	}
}

func TestResourceDrainingResolvesPendingPromptBeforeClosingStreams(t *testing.T) {
	broker := attentionnotify.NewBroker()
	registry := NewRuntimeRegistry().WithAttentionNotifications(broker)
	engine := newRegistryTestRuntime(t, nil)
	ref := registryTestResourceRef(engine.SessionID())
	registerResource(t, registry, ref, engine)

	transcriptSub := subscribeTranscriptForTest(t, registry, engine.SessionID())
	defer func() { _ = transcriptSub.Close() }()
	_ = nextTranscriptMessage(t, transcriptSub)
	attentionSub, err := registry.SubscribeSessionAttentionNotifications(
		context.Background(),
		serverapi.AttentionSessionNotificationSubscribeRequest{SessionID: engine.SessionID()},
	)
	if err != nil {
		t.Fatalf("subscribe session attention notifications: %v", err)
	}
	defer func() { _ = attentionSub.Close() }()

	scopeID := runtimeids.NewExecutionScopeID()
	request := askquestion.AskQuestionRequest{
		ID:       "ask-draining",
		StepID:   registryTestStepID,
		Question: "Proceed?",
	}
	projectPendingPromptResourceForTest(registry, ref, scopeID, request, time.Now().UTC())
	pendingTranscript := nextTranscriptMessageOfKind(t, transcriptSub, clientui.TranscriptMessagePrompt)
	pendingPrompt := transcriptPayload[clientui.TranscriptPrompt](t, pendingTranscript)
	if pendingPrompt.Status != clientui.TranscriptPromptStatusPending || pendingPrompt.PromptID != "ask-draining" {
		t.Fatalf("pending transcript prompt = %+v", pendingPrompt)
	}
	pendingAttention := nextRegistryAttentionEvent(t, attentionSub)
	if pendingAttention.Type != clientui.AttentionNotificationEventPending {
		t.Fatalf("pending attention event = %+v", pendingAttention)
	}

	if err := registry.ResourceDraining(context.Background(), registryTestResource(ref)); err != nil {
		t.Fatalf("drain resource: %v", err)
	}

	resolvedTranscript := nextTranscriptMessageOfKind(t, transcriptSub, clientui.TranscriptMessagePrompt)
	resolvedPrompt := transcriptPayload[clientui.TranscriptPrompt](t, resolvedTranscript)
	if resolvedPrompt.Status != clientui.TranscriptPromptStatusResolved || resolvedPrompt.PromptID != "ask-draining" {
		t.Fatalf("resolved transcript prompt = %+v", resolvedPrompt)
	}
	resolvedCtx, cancelResolved := context.WithTimeout(context.Background(), time.Second)
	defer cancelResolved()
	resolvedAttention, err := attentionSub.Next(resolvedCtx)
	if err != nil {
		t.Fatalf("next resolved attention event: %v", err)
	}
	promptID := attentionNotificationID(clientui.AttentionNotificationKindQuestion, "ask-draining")
	if resolvedAttention.Type != clientui.AttentionNotificationEventResolved ||
		!attentionNotificationEventIDMatches(resolvedAttention, promptID) {
		t.Fatalf("resolved attention event = %+v", resolvedAttention)
	}
	if prompts := registry.ListPendingPrompts(engine.SessionID()); len(prompts) != 0 {
		t.Fatalf("pending prompts after draining = %+v, want none", prompts)
	}

	resolvePendingPromptResourceForTest(registry, ref, scopeID, request.ID)
}

func TestRuntimeRegistryAggregatesSleepObserverAcrossAuthorityResources(t *testing.T) {
	registry := NewRuntimeRegistry()
	engineA := &runtime.Engine{}
	engineB := &runtime.Engine{}
	registerReady(t, registry, "session-a", engineA)
	registerReady(t, registry, "session-b", engineB)
	t.Cleanup(func() { closeRuntime(registry, "session-a", engineA) })
	t.Cleanup(func() { closeRuntime(registry, "session-b", engineB) })
	notifications := make(chan bool, 2)
	registry.SetSleepObserver(func(active bool) { notifications <- active })
	defer registry.SetSleepObserver(nil)

	publishRunState(registry, "session-a", true)
	publishRunState(registry, "session-b", true)
	publishRunState(registry, "session-a", false)
	if active := receiveSleepObserverState(t, notifications); !active {
		t.Fatal("expected aggregate active notification")
	}
	assertNoSleepObserverState(t, notifications)
	publishRunState(registry, "session-b", false)
	if active := receiveSleepObserverState(t, notifications); active {
		t.Fatal("expected aggregate idle notification")
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
	update, err := registry.RuntimeReadModelFeedSnapshot(context.Background(), sessionID)
	if err != nil {
		panic(fmt.Sprintf("build Runtime Read Model test update for Session %q: %v", sessionID, err))
	}
	update.Activity = activity
	registry.PublishRuntimeReadModelUpdate(sessionID, update)
}

func TestActiveRuntimeActivitySnapshotsExcludeRegisteredIdlePopulation(t *testing.T) {
	registry := NewRuntimeRegistry()
	for index := range 500 {
		sessionID := fmt.Sprintf("idle-session-%03d", index)
		registerReady(t, registry, sessionID, &runtime.Engine{})
	}
	runningEngine, runningDone := startRegistryBlockingRuntime(t, registry)
	questionEngine, questionDone := startRegistryBlockingRuntime(t, registry)
	projectPendingPromptForTest(registry, questionEngine.SessionID(), askquestion.AskQuestionRequest{
		ID:       "question-active-snapshot",
		StepID:   registryTestStepID,
		Question: "Continue?",
	})

	snapshots, err := registry.ActiveRuntimeActivitySnapshots(context.Background())
	if err != nil {
		t.Fatalf("ActiveRuntimeActivitySnapshots: %v", err)
	}
	wantIDs := []string{runningEngine.SessionID(), questionEngine.SessionID()}
	slices.Sort(wantIDs)
	if len(snapshots) != len(wantIDs) {
		t.Fatalf("snapshots = %+v, want only %v", snapshots, wantIDs)
	}
	for index, snapshot := range snapshots {
		if snapshot.SessionID != wantIDs[index] {
			t.Fatalf("snapshots = %+v, want sorted IDs %v", snapshots, wantIDs)
		}
		if !snapshot.Activity.ActiveForControl() {
			t.Fatalf("snapshot %q is not active: %+v", snapshot.SessionID, snapshot.Activity)
		}
	}

	closeRegistryBlockingRuntime(t, runningEngine, runningDone)
	closeRegistryBlockingRuntime(t, questionEngine, questionDone)
}

func TestRuntimeReadModelPublicationRejectsProvablyOlderUpdate(t *testing.T) {
	registry := NewRuntimeRegistry()
	engine := newRegistryTestRuntime(t, nil)
	registerReady(t, registry, engine.SessionID(), engine)

	newer := registryTestReadModelUpdate(t, 2, clientui.RuntimeActivityRunning)
	registry.PublishRuntimeReadModelUpdate(engine.SessionID(), newer)
	subscription, err := registry.SubscribeSessionTranscript(context.Background(), serverapi.TranscriptSubscribeRequest{
		SessionID: engine.SessionID(),
	})
	if err != nil {
		t.Fatalf("SubscribeSessionTranscript: %v", err)
	}
	if _, err := subscription.Next(context.Background()); err != nil {
		t.Fatalf("read hydration: %v", err)
	}

	older := registryTestReadModelUpdate(t, 1, clientui.RuntimeActivityRegisteredIdle)
	registry.PublishRuntimeReadModelUpdate(engine.SessionID(), older)

	view, ok := registry.RuntimeMainViewSnapshot(engine.SessionID())
	if !ok || view.Version != newer.Version || view.Activity.State != clientui.RuntimeActivityRunning {
		t.Fatalf("Runtime Main View = %+v, %t; want newer running publication", view, ok)
	}
	snapshots, err := registry.ActiveRuntimeActivitySnapshots(context.Background())
	if err != nil {
		t.Fatalf("ActiveRuntimeActivitySnapshots: %v", err)
	}
	if len(snapshots) != 1 || snapshots[0].SessionID != engine.SessionID() {
		t.Fatalf("active snapshots = %+v, want only %q", snapshots, engine.SessionID())
	}
	nextCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if message, err := subscription.Next(nextCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("stale update message = %+v, error = %v; want no publication", message, err)
	}
}

func TestRuntimeActivitySnapshotsDoNotShareActiveStep(t *testing.T) {
	registry := NewRuntimeRegistry()
	engine := newRegistryTestRuntime(t, nil)
	registerReady(t, registry, engine.SessionID(), engine)
	update := registryTestReadModelUpdate(t, 2, clientui.RuntimeActivityRunning)
	registry.PublishRuntimeReadModelUpdate(engine.SessionID(), update)

	view, ok := registry.RuntimeMainViewSnapshot(engine.SessionID())
	if !ok || view.Activity.ActiveStep == nil {
		t.Fatalf("Runtime Main View = %+v, %t; want active step", view, ok)
	}
	view.Activity.ActiveStep.ActiveKind = clientui.RuntimeActivityActiveKindBackground
	again, ok := registry.RuntimeMainViewSnapshot(engine.SessionID())
	if !ok || again.Activity.ActiveStep == nil ||
		again.Activity.ActiveStep.ActiveKind != clientui.RuntimeActivityActiveKindUserTurn {
		t.Fatalf("Runtime Main View after caller mutation = %+v, %t", again, ok)
	}

	snapshots, err := registry.ActiveRuntimeActivitySnapshots(context.Background())
	if err != nil {
		t.Fatalf("ActiveRuntimeActivitySnapshots: %v", err)
	}
	if len(snapshots) != 1 || snapshots[0].Activity.ActiveStep == nil {
		t.Fatalf("active snapshots = %+v, want one active step", snapshots)
	}
	snapshots[0].Activity.ActiveStep.ActiveKind = clientui.RuntimeActivityActiveKindBackground
	againSnapshots, err := registry.ActiveRuntimeActivitySnapshots(context.Background())
	if err != nil {
		t.Fatalf("ActiveRuntimeActivitySnapshots after mutation: %v", err)
	}
	if len(againSnapshots) != 1 || againSnapshots[0].Activity.ActiveStep == nil ||
		againSnapshots[0].Activity.ActiveStep.ActiveKind != clientui.RuntimeActivityActiveKindUserTurn {
		t.Fatalf("active snapshots after caller mutation = %+v", againSnapshots)
	}
}

func registryTestReadModelUpdate(
	t *testing.T,
	sequence uint64,
	state clientui.RuntimeActivityState,
) clientui.RuntimeReadModelUpdate {
	t.Helper()
	version, err := clientui.NewReadModelVersion("registry-publication-test", 1, sequence)
	if err != nil {
		t.Fatalf("NewReadModelVersion: %v", err)
	}
	activity := clientui.RuntimeActivity{State: state, QueueAccepting: true}
	if state == clientui.RuntimeActivityRunning {
		runID, err := runtimeids.ParseRunID(registryTestRunID)
		if err != nil {
			t.Fatalf("ParseRunID: %v", err)
		}
		stepID, err := runtimeids.ParseStepID(registryTestStepID)
		if err != nil {
			t.Fatalf("ParseStepID: %v", err)
		}
		activity.ActiveStep = &clientui.RuntimeActiveStep{
			RunID:      runID,
			StepID:     stepID,
			ActiveKind: clientui.RuntimeActivityActiveKindUserTurn,
		}
	}
	return clientui.RuntimeReadModelUpdate{Version: version, Activity: activity}
}

func startRegistryBlockingRuntime(t *testing.T, registry *RuntimeRegistry) (*runtime.Engine, <-chan error) {
	t.Helper()
	client := newRegistryBlockingClient()
	engine := newRegistryRuntime(
		t,
		client,
		askquestion.NewRegistry(),
		runtime.Config{Model: "gpt-5", ThinkingLevel: "medium"},
		func(engine *runtime.Engine, evt runtime.Event) {
			if err := registry.PublishAuthorityRuntimeEvent(registryTestResourceRef(engine.SessionID()), evt); err != nil {
				t.Errorf("PublishAuthorityRuntimeEvent: %v", err)
			}
		},
	)
	registerReady(t, registry, engine.SessionID(), engine)
	done := make(chan error, 1)
	go func() {
		_, err := engine.SubmitUserMessage(context.Background(), "work")
		done <- err
	}()
	select {
	case <-client.started:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for active runtime")
	}
	if err := registry.publishCurrentRuntimeActivity(engine.SessionID()); err != nil {
		t.Fatalf("publish active runtime: %v", err)
	}
	t.Cleanup(func() {
		select {
		case <-client.release:
		default:
			close(client.release)
		}
	})
	return engine, done
}

func closeRegistryBlockingRuntime(t *testing.T, engine *runtime.Engine, done <-chan error) {
	t.Helper()
	if _, err := engine.TryInterruptActiveRun(); err != nil {
		t.Fatalf("TryInterruptActiveRun: %v", err)
	}
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("SubmitUserMessage: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for active runtime to stop")
	}
}
