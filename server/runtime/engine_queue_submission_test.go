package runtime

import (
	"context"
	"errors"
	"testing"
	"time"

	"core/server/llm"
	"core/server/tools"
	"core/shared/textutil"
)

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
	select {
	case result := <-queuedDone:
		t.Fatalf("post-turn Queue applied before the protected Step boundary: %+v/%v", result.item, result.err)
	case <-time.After(25 * time.Millisecond):
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("active turn completion: %v", err)
	}
	var queued QueuedUserMessage
	select {
	case result := <-queuedDone:
		if result.err != nil || result.item.ID == "" {
			t.Fatalf("accepted queue item = %+v/%v", result.item, result.err)
		}
		queued = result.item
	case <-time.After(runtimeTestSynchronizationTimeout):
		t.Fatal("post-turn Queue did not apply at the protected Step boundary")
	}
	if queued.ID == "" {
		t.Fatal("post-turn Queue accepted an empty item")
	}
	waitEngineLifecycleTasks(t, engine)
	if calls := fakeClientCallCount(client); calls != 0 {
		t.Fatalf("queued work model calls = %d, want no independent turn", calls)
	}
	if !engine.HasQueuedUserWork() {
		t.Fatal("post-turn Queue did not remain pending for an Agent Turn")
	}
}

func TestQueuedUserMessageCallerCancellationStopsWaitAndPreventsLaterAcceptance(t *testing.T) {
	engine := mustNewExecTestEngine(t, mustCreateTestSession(t), &fakeClient{}, Config{Model: "gpt-5"})
	if err := engine.pauseRuntimeOperations(t.Context()); err != nil {
		t.Fatalf("pause Runtime FIFO: %v", err)
	}
	caller, cancel := context.WithCancel(t.Context())
	accept := func(commit func() (bool, error)) (bool, error) {
		select {
		case <-caller.Done():
			return false, context.Cause(caller)
		default:
			return commit()
		}
	}
	done := make(chan error, 1)
	go func() {
		_, err := engine.QueueUserMessageForAutoDrainWithAcceptance(caller, "canceled input", accept)
		done <- err
	}()
	waitForPendingRuntimeOperation(t, engine)

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
