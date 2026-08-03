package registry

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"core/server/attentionnotify"
	"core/server/llm"
	"core/server/runtime"
	"core/server/runtimeactivity"
	"core/server/sessionruntime"
	askquestion "core/server/tools"
	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/textutil"
)

type registryRuntimeFakeClient struct{}

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
	registry.PromptPending(registryTestResourceRef(sessionID), runtimeids.NewExecutionScopeID(), request, time.Now().UTC())
}

func resolvePendingPromptForTest(registry *RuntimeRegistry, sessionID string, requestID string) {
	for _, prompt := range registry.ListPendingPrompts(sessionID) {
		if prompt.Request.ID == requestID {
			registry.PromptResolved(prompt.Resource, prompt.ScopeID, requestID)
			return
		}
	}
	panic("pending prompt not found")
}

func (registryRuntimeFakeClient) Generate(context.Context, llm.Request) (llm.Response, error) {
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
			Key:  "planning",
			Text: "Planning the next action",
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
	if payload.ActiveReasoning != nil {
		t.Fatalf(
			"hydrated active reasoning = %+v, want none after canonical runtime became idle",
			payload.ActiveReasoning,
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
	registry.PromptPending(predecessor, predecessorScope, request, time.Now().UTC())
	if err := registry.ResourceDraining(context.Background(), registryTestResource(predecessor)); err != nil {
		t.Fatalf("drain predecessor: %v", err)
	}
	registerResource(t, registry, successor, engine)
	t.Cleanup(func() { _ = registry.ResourceDraining(context.Background(), registryTestResource(successor)) })
	registry.PromptPending(successor, successorScope, request, time.Now().UTC())
	registry.PromptPending(predecessor, predecessorScope, request, time.Now().UTC())
	registry.PromptResolved(predecessor, predecessorScope, request.ID)

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
	registry.PromptPending(ref, scopeID, request, time.Now().UTC())
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

	registry.PromptResolved(ref, scopeID, request.ID)
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
	registry.PublishRuntimeReadModelUpdate(sessionID, clientui.RuntimeReadModelUpdate{
		Version:             runtimeactivity.NextReadModelVersion(sessionID),
		Activity:            activity,
		InputReconciliation: clientui.RuntimeInputReconciliationSnapshot{},
	})
}
