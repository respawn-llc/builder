package runtimecontrol_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"core/internal/testharness/runtimecontrolfixture"
	"core/server/attentionnotify"
	"core/server/llm"
	"core/server/registry"
	"core/server/runtime"
	"core/shared/apicontract"
	"core/shared/runtimeids"
	"core/shared/runtimeinput"
	"core/shared/serverapi"
	"core/shared/textutil"
)

type controlledWatchClient struct {
	started  chan struct{}
	release  chan struct{}
	once     sync.Once
	response llm.Response
	err      error
}

func newControlledWatchClient(response llm.Response, err error) *controlledWatchClient {
	return &controlledWatchClient{
		started:  make(chan struct{}),
		release:  make(chan struct{}),
		response: response,
		err:      err,
	}
}

func (c *controlledWatchClient) Generate(ctx context.Context, _ llm.Request) (llm.Response, error) {
	c.once.Do(func() { close(c.started) })
	select {
	case <-c.release:
		return c.response, c.err
	case <-ctx.Done():
		return llm.Response{}, context.Cause(ctx)
	}
}

type emptyWatchAskView struct{}

func (emptyWatchAskView) ListPendingAsksBySession(context.Context, serverapi.AskListPendingBySessionRequest) (serverapi.AskListPendingBySessionResponse, error) {
	return serverapi.AskListPendingBySessionResponse{}, nil
}

type observedWatchAttention struct {
	apicontract.AttentionNotificationService
	subscribed chan struct{}
	once       sync.Once
}

func (s *observedWatchAttention) SubscribeSessionAttentionNotifications(ctx context.Context, req serverapi.AttentionSessionNotificationSubscribeRequest) (serverapi.AttentionNotificationSubscription, error) {
	subscription, err := s.AttentionNotificationService.SubscribeSessionAttentionNotifications(ctx, req)
	s.once.Do(func() { close(s.subscribed) })
	return subscription, err
}

type liveWatchProductRun struct {
	fixture    *runtimecontrolfixture.Fixture
	client     *controlledWatchClient
	runDone    chan error
	attention  *observedWatchAttention
	watchDone  chan serverapi.RuntimeLiveWatchResponse
	watchError chan error
}

func startLiveWatchProductRun(t *testing.T, client *controlledWatchClient) liveWatchProductRun {
	t.Helper()
	fixture := runtimecontrolfixture.New(t, runtimecontrolfixture.Options{
		Client:  client,
		Runtime: productRuntimeConfig(),
	})
	runDone := make(chan error, 1)
	go func() {
		_, err := fixture.Service.SubmitUserTurn(context.Background(), serverapi.RuntimeSubmitUserTurnRequest{
			ClientRequestID: runtimeids.NewRuntimeClientRequestID().String(),
			SessionID:       fixture.Store.Meta().SessionID,
			Input:           runtimeinput.Text("watch this run"),
		})
		runDone <- err
	}()
	waitForSignal(t, client.started, "model request")

	broker := attentionnotify.NewBroker()
	attention := &observedWatchAttention{
		AttentionNotificationService: registry.NewRuntimeRegistry().WithAttentionNotifications(broker),
		subscribed:                   make(chan struct{}),
	}
	fixture.Service.WithLiveWatchPromptSources(emptyWatchAskView{}, retainedApprovalView{}, attention)
	return liveWatchProductRun{
		fixture:    fixture,
		client:     client,
		runDone:    runDone,
		attention:  attention,
		watchDone:  make(chan serverapi.RuntimeLiveWatchResponse, 1),
		watchError: make(chan error, 1),
	}
}

func (r liveWatchProductRun) startWatch(ctx context.Context) {
	go func() {
		response, err := r.fixture.Service.LiveWatch(ctx, serverapi.RuntimeLiveWatchRequest{
			SessionID: r.fixture.Store.Meta().SessionID,
		})
		r.watchDone <- response
		r.watchError <- err
	}()
}

func TestLiveWatchReturnsFinalAnswer(t *testing.T) {
	client := newControlledWatchClient(llm.Response{
		Assistant: llm.Message{
			Role:    llm.RoleAssistant,
			Content: textutil.Value("done"),
			Phase:   textutil.Value(llm.MessagePhaseFinal),
		},
		Usage: llm.Usage{WindowTokens: 200000},
	}, nil)
	run := startLiveWatchProductRun(t, client)
	run.startWatch(context.Background())
	waitForSignal(t, run.attention.subscribed, "LiveWatch subscription")
	close(client.release)

	response, err := waitForWatch(t, run)
	if err != nil || response.Outcome.Kind != serverapi.RuntimeLiveWatchFinalAnswer ||
		response.Outcome.FinalAnswer == nil || response.Outcome.FinalAnswer.Result == nil ||
		*response.Outcome.FinalAnswer.Result != "done" {
		t.Fatalf("LiveWatch = %+v, %v; want final answer", response, err)
	}
	waitForRun(t, run.runDone)
}

func TestLiveWatchReturnsExecutionError(t *testing.T) {
	executionErr := &llm.APIStatusError{StatusCode: 400, Body: "model execution failed"}
	run := startLiveWatchProductRun(t, newControlledWatchClient(llm.Response{}, executionErr))
	run.startWatch(context.Background())
	waitForSignal(t, run.attention.subscribed, "LiveWatch subscription")
	close(run.client.release)

	response, err := waitForWatch(t, run)
	if err != nil || response.Outcome.Kind != serverapi.RuntimeLiveWatchExecutionError ||
		response.Outcome.Failure == nil || response.Outcome.Failure.Reason == "" {
		t.Fatalf("LiveWatch = %+v, %v; want typed execution error", response, err)
	}
	waitForRun(t, run.runDone)
}

func TestLiveWatchReturnsInterrupted(t *testing.T) {
	run := startLiveWatchProductRun(t, newControlledWatchClient(llm.Response{}, nil))
	run.startWatch(context.Background())
	waitForSignal(t, run.attention.subscribed, "LiveWatch subscription")
	stopped, err := run.fixture.Engine.TryInterruptActiveRun()
	if err != nil || !stopped {
		t.Fatalf("TryInterruptActiveRun = %t, %v", stopped, err)
	}

	response, err := waitForWatch(t, run)
	if err != nil || response.Outcome.Kind != serverapi.RuntimeLiveWatchInterrupted ||
		response.Outcome.Failure == nil ||
		response.Outcome.Failure.Reason != string(runtime.RunStatusInterrupted) {
		t.Fatalf("LiveWatch = %+v, %v; want typed interruption", response, err)
	}
	waitForRun(t, run.runDone)
}

func TestLiveWatchHonorsCancellation(t *testing.T) {
	run := startLiveWatchProductRun(t, newControlledWatchClient(llm.Response{}, nil))
	ctx, cancel := context.WithCancel(context.Background())
	run.startWatch(ctx)
	waitForSignal(t, run.attention.subscribed, "LiveWatch subscription")
	cancel()

	if _, err := waitForWatch(t, run); !errors.Is(err, context.Canceled) {
		t.Fatalf("LiveWatch cancellation error = %v", err)
	}
	if stopped, err := run.fixture.Engine.TryInterruptActiveRun(); err != nil || !stopped {
		t.Fatalf("TryInterruptActiveRun = %t, %v", stopped, err)
	}
	waitForRun(t, run.runDone)
}

func waitForWatch(t *testing.T, run liveWatchProductRun) (serverapi.RuntimeLiveWatchResponse, error) {
	t.Helper()
	select {
	case err := <-run.watchError:
		return <-run.watchDone, err
	case <-time.After(3 * time.Second):
		t.Fatal("LiveWatch did not return")
		return serverapi.RuntimeLiveWatchResponse{}, nil
	}
}

func waitForRun(t *testing.T, runDone <-chan error) {
	t.Helper()
	select {
	case <-runDone:
	case <-time.After(3 * time.Second):
		t.Fatal("runtime command did not finish")
	}
}

func waitForSignal(t *testing.T, signal <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(3 * time.Second):
		t.Fatalf("%s did not occur", name)
	}
}

var _ apicontract.AskViewService = emptyWatchAskView{}
var _ apicontract.AttentionNotificationService = (*observedWatchAttention)(nil)
