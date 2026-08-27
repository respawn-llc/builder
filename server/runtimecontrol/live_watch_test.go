package runtimecontrol

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"core/server/attentionnotify"
	"core/server/llm"
	"core/server/registry"
	"core/server/runtime"
	servicecontract "core/shared/apicontract"
	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/textutil"
)

type liveWatchAskViewStub struct {
	asks []clientui.PendingAsk
}

func (s liveWatchAskViewStub) ListPendingAsksBySession(context.Context, serverapi.AskListPendingBySessionRequest) (serverapi.AskListPendingBySessionResponse, error) {
	return serverapi.AskListPendingBySessionResponse{Asks: s.asks}, nil
}

type liveWatchApprovalViewStub struct{}

func (liveWatchApprovalViewStub) ListPendingApprovalsBySession(context.Context, serverapi.ApprovalListPendingBySessionRequest) (serverapi.ApprovalListPendingBySessionResponse, error) {
	return serverapi.ApprovalListPendingBySessionResponse{}, nil
}

type failingLiveWatchAttention struct{ err error }

func (f failingLiveWatchAttention) SubscribeAttentionNotifications(context.Context, serverapi.AttentionNotificationSubscribeRequest) (serverapi.AttentionNotificationSubscription, error) {
	return nil, f.err
}

func (f failingLiveWatchAttention) SubscribeSessionAttentionNotifications(context.Context, serverapi.AttentionSessionNotificationSubscribeRequest) (serverapi.AttentionNotificationSubscription, error) {
	return nil, f.err
}

type liveWatchMutableAskView struct {
	mu   sync.RWMutex
	asks []clientui.PendingAsk
}

func (s *liveWatchMutableAskView) ListPendingAsksBySession(context.Context, serverapi.AskListPendingBySessionRequest) (serverapi.AskListPendingBySessionResponse, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return serverapi.AskListPendingBySessionResponse{Asks: append([]clientui.PendingAsk(nil), s.asks...)}, nil
}

func (s *liveWatchMutableAskView) set(asks ...clientui.PendingAsk) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.asks = append([]clientui.PendingAsk(nil), asks...)
}

type liveWatchObservedAttention struct {
	servicecontract.AttentionNotificationService
	subscribed chan struct{}
	once       sync.Once
}

func (s *liveWatchObservedAttention) SubscribeSessionAttentionNotifications(ctx context.Context, req serverapi.AttentionSessionNotificationSubscribeRequest) (serverapi.AttentionNotificationSubscription, error) {
	sub, err := s.AttentionNotificationService.SubscribeSessionAttentionNotifications(ctx, req)
	s.once.Do(func() { close(s.subscribed) })
	return sub, err
}

type liveWatchBlockingClient struct {
	started chan struct{}
	once    sync.Once
}

func newLiveWatchBlockingClient() *liveWatchBlockingClient {
	return &liveWatchBlockingClient{started: make(chan struct{})}
}

func (c *liveWatchBlockingClient) Generate(ctx context.Context, _ llm.Request, _ llm.StreamCallbacks) (llm.Response, error) {
	c.once.Do(func() { close(c.started) })
	<-ctx.Done()
	return llm.Response{}, context.Cause(ctx)
}

func (c *liveWatchBlockingClient) ProviderCapabilities(context.Context) (llm.ProviderCapabilities, error) {
	return llm.ProviderCapabilities{ProviderID: "test", SupportsResponsesAPI: true}, nil
}

type liveWatchReleasableFinalClient struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func newLiveWatchReleasableFinalClient() *liveWatchReleasableFinalClient {
	return &liveWatchReleasableFinalClient{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (c *liveWatchReleasableFinalClient) Generate(ctx context.Context, _ llm.Request, _ llm.StreamCallbacks) (llm.Response, error) {
	c.once.Do(func() { close(c.started) })
	select {
	case <-c.release:
		return llm.Response{
			Assistant: llm.Message{
				Role:    llm.RoleAssistant,
				Content: textutil.Value("done"),
				Phase:   textutil.Value(llm.MessagePhaseFinal),
			},
			Usage: llm.Usage{WindowTokens: 200000},
		}, nil
	case <-ctx.Done():
		return llm.Response{}, context.Cause(ctx)
	}
}

func (c *liveWatchReleasableFinalClient) ProviderCapabilities(context.Context) (llm.ProviderCapabilities, error) {
	return llm.ProviderCapabilities{ProviderID: "test", SupportsResponsesAPI: true}, nil
}

func TestLiveWatchReturnsInitialPendingQuestionWhenNoRunIsActive(t *testing.T) {
	store, _, service := newRuntimeControlTestService(t, nil, nil, runtime.Config{})
	sessionID := store.Meta().SessionID
	attention := registry.NewRuntimeRegistry().WithAttentionNotifications(attentionnotify.NewBroker())
	service.WithLiveWatchPromptSources(
		liveWatchAskViewStub{asks: []clientui.PendingAsk{{
			PromptID: "ask-1", SessionID: mustRuntimeControlSessionID(t, sessionID),
			StepID: mustRuntimeControlStepID(t), Question: "Continue?", CreatedAt: time.Now().UTC(),
		}}},
		liveWatchApprovalViewStub{},
		attention,
	)

	response, err := service.LiveWatch(context.Background(), serverapi.RuntimeLiveWatchRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("LiveWatch: %v", err)
	}
	if response.Outcome.Kind != serverapi.RuntimeLiveWatchQuestion ||
		response.Outcome.Question == nil || response.Outcome.Question.Ask == nil ||
		response.Outcome.Question.Ask.PromptID != "ask-1" {
		t.Fatalf("LiveWatch response = %+v", response)
	}
}

func TestLiveWatchSurfacesAttentionStreamFailureWhileRunIsBlocked(t *testing.T) {
	client := newLiveWatchBlockingClient()
	store, engine, service := newRuntimeControlTestService(t, client, nil, runtime.Config{})
	broker := attentionnotify.NewBroker()
	attention := registry.NewRuntimeRegistry().WithAttentionNotifications(broker)
	observed := &liveWatchObservedAttention{
		AttentionNotificationService: attention,
		subscribed:                   make(chan struct{}),
	}
	service.WithLiveWatchPromptSources(liveWatchAskViewStub{}, liveWatchApprovalViewStub{}, observed)

	runDone := make(chan error, 1)
	go func() {
		_, err := service.SubmitUserTurn(context.Background(), runtimeControlUserTurnRequest(store, "watch-stream-loss", "hello"))
		runDone <- err
	}()
	<-client.started

	watchDone := make(chan error, 1)
	go func() {
		_, err := service.LiveWatch(context.Background(), serverapi.RuntimeLiveWatchRequest{SessionID: store.Meta().SessionID})
		watchDone <- err
	}()
	<-observed.subscribed

	streamErr := errors.New("attention stream failed while blocked")
	broker.Close(streamErr)
	if err := <-watchDone; !errors.Is(err, streamErr) {
		t.Fatalf("LiveWatch error = %v, want stream failure", err)
	}
	if err := engine.Interrupt(); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	select {
	case <-runDone:
	case <-time.After(3 * time.Second):
		t.Fatal("live run did not stop after stream failure")
	}
}

func TestLiveWatchPromptWakeWinsWhileRunIsBlocked(t *testing.T) {
	client := newLiveWatchBlockingClient()
	store, engine, service := newRuntimeControlTestService(t, client, nil, runtime.Config{})
	runDone := make(chan error, 1)
	go func() {
		_, err := service.SubmitUserTurn(context.Background(), runtimeControlUserTurnRequest(store, "watch-prompt", "hello"))
		runDone <- err
	}()
	<-client.started

	askView := &liveWatchMutableAskView{}
	broker := attentionnotify.NewBroker()
	attention := registry.NewRuntimeRegistry().WithAttentionNotifications(broker)
	observed := &liveWatchObservedAttention{
		AttentionNotificationService: attention,
		subscribed:                   make(chan struct{}),
	}
	service.WithLiveWatchPromptSources(askView, liveWatchApprovalViewStub{}, observed)
	watchDone := make(chan serverapi.RuntimeLiveWatchResponse, 1)
	watchErr := make(chan error, 1)
	go func() {
		response, err := service.LiveWatch(context.Background(), serverapi.RuntimeLiveWatchRequest{SessionID: store.Meta().SessionID})
		watchDone <- response
		watchErr <- err
	}()
	<-observed.subscribed

	now := time.Now().UTC()
	askView.set(clientui.PendingAsk{
		PromptID: "ask-1", SessionID: mustRuntimeControlSessionID(t, store.Meta().SessionID),
		StepID: mustRuntimeControlStepID(t), Question: "Continue?", CreatedAt: now,
	})
	if err := broker.PublishPending(
		attentionnotify.RoutingScope{Kind: attentionnotify.RoutingSessionPrompt, SessionID: store.Meta().SessionID},
		clientui.AttentionNotification{
			ID:         clientui.AttentionNotificationID{Kind: clientui.AttentionNotificationKindQuestion, UUID: "ask-1"},
			Kind:       clientui.AttentionNotificationKindQuestion,
			OccurredAt: now,
			Revision:   1,
			Target: clientui.AttentionNotificationTarget{
				Kind:      clientui.AttentionNotificationTargetSessionPrompt,
				SessionID: store.Meta().SessionID,
			},
			Question: &clientui.AttentionNotificationQuestionState{
				PreparedAskIDs:          []string{"ask-1"},
				MaterializedAskIDs:      []string{"ask-1"},
				CurrentUnresolvedAskIDs: []string{"ask-1"},
				Preview:                 "Continue?",
				DisplayCount:            1,
				MaterializedCount:       1,
			},
		},
	); err != nil {
		t.Fatalf("PublishPending: %v", err)
	}
	if err := <-watchErr; err != nil {
		t.Fatalf("LiveWatch: %v", err)
	}
	response := <-watchDone
	if response.Outcome.Kind != serverapi.RuntimeLiveWatchQuestion ||
		response.Outcome.Question == nil ||
		response.Outcome.Question.Ask == nil ||
		response.Outcome.Question.Ask.PromptID != "ask-1" {
		t.Fatalf("LiveWatch outcome = %+v", response.Outcome)
	}
	if err := engine.Interrupt(); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	select {
	case <-runDone:
	case <-time.After(3 * time.Second):
		t.Fatal("live run did not stop after prompt wake")
	}
}

func mustRuntimeControlSessionID(t *testing.T, raw string) runtimeids.SessionID {
	t.Helper()
	id, err := runtimeids.ParseSessionID(raw)
	if err != nil {
		t.Fatalf("ParseSessionID: %v", err)
	}
	return id
}

func TestLiveWatchCancellationWhileRunIsBlocked(t *testing.T) {
	client := newLiveWatchBlockingClient()
	store, engine, service := newRuntimeControlTestService(t, client, nil, runtime.Config{})
	runDone := make(chan error, 1)
	go func() {
		_, err := service.SubmitUserTurn(context.Background(), runtimeControlUserTurnRequest(store, "watch-cancel", "hello"))
		runDone <- err
	}()
	<-client.started

	broker := attentionnotify.NewBroker()
	attention := registry.NewRuntimeRegistry().WithAttentionNotifications(broker)
	observed := &liveWatchObservedAttention{
		AttentionNotificationService: attention,
		subscribed:                   make(chan struct{}),
	}
	service.WithLiveWatchPromptSources(liveWatchAskViewStub{}, liveWatchApprovalViewStub{}, observed)
	ctx, cancel := context.WithCancel(context.Background())
	watchDone := make(chan error, 1)
	go func() {
		_, err := service.LiveWatch(ctx, serverapi.RuntimeLiveWatchRequest{SessionID: store.Meta().SessionID})
		watchDone <- err
	}()
	<-observed.subscribed
	cancel()
	if err := <-watchDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("LiveWatch error = %v, want context cancellation", err)
	}
	if err := engine.Interrupt(); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	select {
	case <-runDone:
	case <-time.After(3 * time.Second):
		t.Fatal("live run did not stop after cancellation")
	}
}

func TestLiveWatchTerminalCompletionWinsWhileRunIsBlocked(t *testing.T) {
	client := newLiveWatchReleasableFinalClient()
	store, engine, service := newRuntimeControlTestService(t, client, nil, runtime.Config{})
	runDone := make(chan error, 1)
	go func() {
		_, err := service.SubmitUserTurn(context.Background(), runtimeControlUserTurnRequest(store, "watch-terminal", "hello"))
		runDone <- err
	}()
	<-client.started

	broker := attentionnotify.NewBroker()
	attention := registry.NewRuntimeRegistry().WithAttentionNotifications(broker)
	observed := &liveWatchObservedAttention{
		AttentionNotificationService: attention,
		subscribed:                   make(chan struct{}),
	}
	service.WithLiveWatchPromptSources(liveWatchAskViewStub{}, liveWatchApprovalViewStub{}, observed)
	watchDone := make(chan serverapi.RuntimeLiveWatchResponse, 1)
	watchErr := make(chan error, 1)
	go func() {
		response, err := service.LiveWatch(context.Background(), serverapi.RuntimeLiveWatchRequest{SessionID: store.Meta().SessionID})
		watchDone <- response
		watchErr <- err
	}()
	<-observed.subscribed
	close(client.release)
	if err := <-watchErr; err != nil {
		t.Fatalf("LiveWatch: %v", err)
	}
	if response := <-watchDone; response.Outcome.Kind != serverapi.RuntimeLiveWatchFinalAnswer {
		t.Fatalf("LiveWatch outcome = %+v, want final answer", response.Outcome)
	}
	select {
	case <-runDone:
	case <-time.After(3 * time.Second):
		t.Fatal("live run did not finish after terminal completion")
	}
	_ = engine
}

func TestLiveWatchReturnsInterruptedOutcomeWhenRunStops(t *testing.T) {
	client := newLiveWatchBlockingClient()
	store, engine, service := newRuntimeControlTestService(t, client, nil, runtime.Config{})
	runDone := make(chan error, 1)
	go func() {
		_, err := service.SubmitUserTurn(context.Background(), runtimeControlUserTurnRequest(store, "watch-interrupt", "hello"))
		runDone <- err
	}()
	<-client.started

	broker := attentionnotify.NewBroker()
	attention := registry.NewRuntimeRegistry().WithAttentionNotifications(broker)
	observed := &liveWatchObservedAttention{
		AttentionNotificationService: attention,
		subscribed:                   make(chan struct{}),
	}
	service.WithLiveWatchPromptSources(liveWatchAskViewStub{}, liveWatchApprovalViewStub{}, observed)
	watchDone := make(chan serverapi.RuntimeLiveWatchResponse, 1)
	watchErr := make(chan error, 1)
	go func() {
		response, err := service.LiveWatch(context.Background(), serverapi.RuntimeLiveWatchRequest{SessionID: store.Meta().SessionID})
		watchDone <- response
		watchErr <- err
	}()
	<-observed.subscribed

	stopped, err := engine.TryInterruptActiveRun()
	if err != nil || !stopped {
		t.Fatalf("TryInterruptActiveRun stopped=%t err=%v", stopped, err)
	}
	if err := <-watchErr; err != nil {
		t.Fatalf("LiveWatch: %v", err)
	}
	response := <-watchDone
	if response.Outcome.Kind != serverapi.RuntimeLiveWatchInterrupted ||
		response.Outcome.Failure == nil ||
		response.Outcome.Failure.Reason != string(runtime.RunStatusInterrupted) ||
		response.Outcome.Failure.Diagnostic == nil {
		t.Fatalf("LiveWatch outcome = %+v, want interrupted failure", response.Outcome)
	}
	select {
	case <-runDone:
	case <-time.After(3 * time.Second):
		t.Fatal("live run did not finish after interruption")
	}
}

func TestLiveWatchSurfacesCanceledAttentionStreamWhileRunIsBlocked(t *testing.T) {
	client := newLiveWatchBlockingClient()
	store, engine, service := newRuntimeControlTestService(t, client, nil, runtime.Config{})
	runDone := make(chan error, 1)
	go func() {
		_, err := service.SubmitUserTurn(context.Background(), runtimeControlUserTurnRequest(store, "watch-stream-canceled", "hello"))
		runDone <- err
	}()
	<-client.started

	broker := attentionnotify.NewBroker()
	attention := registry.NewRuntimeRegistry().WithAttentionNotifications(broker)
	observed := &liveWatchObservedAttention{
		AttentionNotificationService: attention,
		subscribed:                   make(chan struct{}),
	}
	service.WithLiveWatchPromptSources(liveWatchAskViewStub{}, liveWatchApprovalViewStub{}, observed)
	watchErr := make(chan error, 1)
	go func() {
		_, err := service.LiveWatch(context.Background(), serverapi.RuntimeLiveWatchRequest{SessionID: store.Meta().SessionID})
		watchErr <- err
	}()
	<-observed.subscribed

	broker.Close(context.Canceled)
	if err := <-watchErr; !errors.Is(err, serverapi.ErrStreamFailed) || errors.Is(err, context.Canceled) {
		t.Fatalf("LiveWatch error = %v, want canceled attention stream", err)
	}
	if err := engine.Interrupt(); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	select {
	case <-runDone:
	case <-time.After(3 * time.Second):
		t.Fatal("live run did not stop after attention stream cancellation")
	}
}

func TestLiveWatchSurfacesAttentionStreamFailureBeforeArbitration(t *testing.T) {
	store, _, service := newRuntimeControlTestService(t, nil, nil, runtime.Config{})
	streamErr := errors.New("attention stream failed")
	service.WithLiveWatchPromptSources(liveWatchAskViewStub{}, liveWatchApprovalViewStub{}, failingLiveWatchAttention{err: streamErr})

	_, err := service.LiveWatch(context.Background(), serverapi.RuntimeLiveWatchRequest{SessionID: store.Meta().SessionID})
	if !errors.Is(err, streamErr) {
		t.Fatalf("LiveWatch error = %v, want attention stream failure", err)
	}
}

func TestLiveWatchResultClassifiesTypedTerminalStates(t *testing.T) {
	id := runtimeids.NewSessionID()
	cases := []struct {
		name       string
		result     runtime.LiveRunResult
		err        error
		kind       string
		reason     string
		diagnostic string
	}{
		{"no final", runtime.LiveRunResult{NoFinalReason: runtime.LiveRunNoFinalAnswerReasonGoalLoop}, runtime.ErrLiveRunNoFinalAnswer, "no_final_result", "", ""},
		{"interrupted", runtime.LiveRunResult{Status: runtime.RunStatusInterrupted, Error: errors.New("stop detail")}, errors.New("terminal"), "interrupted", "interrupted", "stop detail"},
		{"error", runtime.LiveRunResult{Status: runtime.RunStatusFailed, Error: errors.New("failure detail")}, errors.New("terminal"), "execution_error", "terminal", "failure detail"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			response, err := liveWatchResult(id, "session", tc.result, tc.err)
			if err != nil || string(response.Outcome.Kind) != tc.kind {
				t.Fatalf("result = %+v, err = %v", response, err)
			}
			if tc.reason == "" {
				return
			}
			if response.Outcome.Failure == nil || response.Outcome.Failure.Reason != tc.reason ||
				response.Outcome.Failure.Diagnostic == nil || *response.Outcome.Failure.Diagnostic != tc.diagnostic {
				t.Fatalf("failure = %+v", response.Outcome.Failure)
			}
		})
	}
}
