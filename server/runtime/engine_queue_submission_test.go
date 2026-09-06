package runtime

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"core/server/llm"
	"core/server/tools"
	"core/shared/textutil"
)

func TestStopRestoresSteerAndPostTurnQueueWithoutContinuation(t *testing.T) {
	client := newBlockingThenQueuedClient()
	var mu sync.Mutex
	var restored []InterruptedHumanInput
	engine := mustNewTestEngine(t, mustCreateTestSession(t), client, tools.NewRegistry(), Config{
		Model: "gpt-5",
		OnEvent: func(event Event) {
			if event.HumanInputInterrupted != nil {
				mu.Lock()
				restored = append(restored, event.HumanInputInterrupted.Items...)
				mu.Unlock()
			}
		},
	})
	done := make(chan error, 1)
	go func() {
		_, err := engine.SubmitUserMessage(t.Context(), "start")
		done <- err
	}()
	pendingWorkTestWait(t, client.started, "held provider")
	steer, err := engine.Steer(t.Context(), "steer", nil)
	pendingWorkTestNoError(t, err)
	queued, err := engine.QueueUserMessage(t.Context(), "queue")
	pendingWorkTestNoError(t, err)
	stopped, err := engine.TryInterruptActiveRun()
	pendingWorkTestNoError(t, err)
	close(client.releaseC)
	if !stopped {
		t.Fatal("Stop did not stop the held provider")
	}
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("stopped turn = %v", err)
	}
	waitEngineLifecycleTasks(t, engine)
	mu.Lock()
	items := append([]InterruptedHumanInput(nil), restored...)
	mu.Unlock()
	if len(items) != 2 || items[0].QueueItemID != steer.ID || items[1].QueueItemID != queued.ID {
		t.Fatalf("restored inputs = %+v, want Steer then Queue", items)
	}
	if pending := pendingWorkTestSnapshot(t, engine); len(pending.Items) != 0 {
		t.Fatalf("Stop left Pending Work: %+v", pending.Items)
	}
	client.mu.Lock()
	calls := len(client.calls)
	client.mu.Unlock()
	if calls != 1 {
		t.Fatalf("Stop launched continuation: provider calls = %d", calls)
	}
	_, err = engine.SubmitUserMessage(t.Context(), "subsequent send")
	pendingWorkTestNoError(t, err)
}

func TestPostTurnQueueDoesNotLaunchIndependentTurn(t *testing.T) {
	client := &fakeClient{responses: []llm.Response{{Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("queued work handled"), Phase: textutil.Value(llm.MessagePhaseFinal)}}}}
	engine := mustNewTestEngine(t, mustCreateTestSession(t), client, tools.NewRegistry(), Config{Model: "gpt-5"})
	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- engine.stepLifecycle.Run(context.Background(), exclusiveStepOptions{EmitRunState: true, ActiveKind: ActiveKindUserTurn}, func(context.Context, string) error {
			close(started)
			<-release
			return nil
		})
	}()
	select {
	case <-started:
	case <-time.After(runtimeTestSynchronizationTimeout):
		t.Fatal("active turn did not start")
	}
	queuedDone := make(chan struct {
		item QueuedUserMessage
		err  error
	}, 1)
	go func() {
		queued, err := engine.QueueUserMessage(t.Context(), "queued input")
		queuedDone <- struct {
			item QueuedUserMessage
			err  error
		}{item: queued, err: err}
	}()
	var queued QueuedUserMessage
	select {
	case result := <-queuedDone:
		if result.err != nil || result.item.ID == "" {
			t.Fatalf("accepted queue item = %+v/%v", result.item, result.err)
		}
		queued = result.item
	case <-time.After(runtimeTestSynchronizationTimeout):
		t.Fatal("post-turn Queue acceptance waited for the protected Step boundary")
	}
	if queued.ID == "" {
		t.Fatal("post-turn Queue accepted an empty item")
	}
	if pending := pendingWorkTestSnapshot(t, engine); len(pending.Items) != 1 || pending.Items[0].ID.String() != queued.ID {
		t.Fatalf("Pending Work during protected Step = %+v, want accepted Queue item", pending.Items)
	}
	if calls := fakeClientCallCount(client); calls != 0 {
		t.Fatalf("queued work model calls during protected Step = %d, want none", calls)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("active turn completion: %v", err)
	}
	waitEngineLifecycleTasks(t, engine)
	if calls := fakeClientCallCount(client); calls != 0 {
		t.Fatalf("queued work model calls = %d, want no independent turn", calls)
	}
	if !engine.HasQueuedUserWork() {
		t.Fatal("post-turn Queue did not remain pending for an Agent Turn")
	}
	_, err := engine.RemovePendingWork(t.Context(), mustQueueItemID(queued.ID))
	pendingWorkTestNoError(t, err)
	if engine.HasActiveLiveRunGroup() || engine.HasQueuedUserWork() {
		t.Fatal("removing the last Queue item retained its accepting execution")
	}
}

func TestPostTurnQueueDoesNotHoldCompletedLiveRun(t *testing.T) {
	client, started, releaseProvider := newGatedHookClient(finalTextResponse("done"), finalTextResponse("unexpected continuation"))
	defer releaseProvider()
	engine := mustNewTestEngine(t, mustCreateTestSession(t), client, tools.NewRegistry(), Config{Model: "gpt-5"})
	done := make(chan error, 1)
	go func() {
		_, err := engine.SubmitUserMessage(t.Context(), "start")
		done <- err
	}()
	pendingWorkTestWait(t, started, "held provider")
	waitCtx, cancelWait := context.WithTimeout(t.Context(), runtimeTestSynchronizationTimeout)
	defer cancelWait()
	handle, err := engine.CaptureActiveRunResult(waitCtx)
	pendingWorkTestNoError(t, err)
	queued, err := engine.QueueUserMessage(t.Context(), "later")
	pendingWorkTestNoError(t, err)
	releaseProvider()
	pendingWorkTestNoError(t, <-done)
	waitEngineLifecycleTasks(t, engine)
	result, err := handle.Wait()
	pendingWorkTestNoError(t, err)
	if result.Status != RunStatusCompleted || result.ResultKind != LiveRunResultAssistantFinalAnswer {
		t.Fatalf("completed live result = %+v", result)
	}
	if engine.HasActiveLiveRunGroup() {
		t.Fatal("post-turn Queue retained the completed execution")
	}
	if pending := pendingWorkTestSnapshot(t, engine); len(pending.Items) != 1 || pending.Items[0].ID.String() != queued.ID {
		t.Fatalf("post-turn Queue = %+v, want accepted item still pending", pending.Items)
	}
	if calls := hookClientCallCount(client); calls != 1 {
		t.Fatalf("Queue launched a continuation: provider calls = %d", calls)
	}
}

func TestQueuedUserMessageCallerCancellationStopsWaitAndPreventsLaterAcceptance(t *testing.T) {
	engine := mustNewExecTestEngine(t, mustCreateTestSession(t), &fakeClient{}, Config{Model: "gpt-5"})
	if err := engine.pauseRuntimeOperations(t.Context()); err != nil {
		t.Fatalf("pause Runtime FIFO: %v", err)
	}
	caller, cancel := context.WithCancel(t.Context())
	defer cancel()
	accepting := make(chan struct{})
	accept := func(commit func() (bool, error)) (bool, error) {
		close(accepting)
		<-caller.Done()
		return false, context.Cause(caller)
	}
	done := make(chan error, 1)
	go func() {
		_, err := engine.Steer(caller, "canceled input", accept)
		done <- err
	}()
	pendingWorkTestWait(t, accepting, "Steer acceptance while Runtime FIFO is paused")

	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled Queue wait = %v, want canceled", err)
		}
	case <-time.After(runtimeTestSynchronizationTimeout):
		t.Fatal("canceled Queue caller remained blocked")
	}
	if err := engine.drainRuntimeOperations(t.Context()); err != nil {
		t.Fatalf("drain rejected Queue operation: %v", err)
	}
	if engine.HasQueuedUserWork() {
		t.Fatal("canceled caller created Pending Work after the Runtime boundary")
	}
}
