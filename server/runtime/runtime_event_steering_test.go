package runtime

import (
	"context"
	"sync"
	"testing"
	"time"

	"core/server/llm"
	"core/server/runtimecommand"
	"core/server/tools"
	"core/shared/textutil"
)

func TestDurableSteeringUsesRuntimeEventAdmissionWhileStreamingRemainsDirect(t *testing.T) {
	queue := runtimecommand.NewQueue(context.Background())
	release := blockRuntimeEventAdmission(t, queue)
	defer release()

	events := make(chan Event, 8)
	store := mustCreateTestSession(t)
	engine := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{
		Model:         "gpt-5",
		RuntimeEvents: queue,
		OnEvent: func(event Event) {
			events <- event
		},
	})

	durableDone := make(chan error, 1)
	go func() {
		durableDone <- engine.steer("durable", steerMessagesWithPersistenceIntent(
			steeringPriorityNormal,
			steeringMessageEventDefault,
			true,
			[]llm.Message{{Role: llm.RoleUser, Content: textutil.Value("durable")}},
		))
	}()
	select {
	case err := <-durableDone:
		t.Fatalf("durable steering bypassed Runtime Event admission: %v", err)
	case event := <-events:
		t.Fatalf("durable steering published %v before Runtime Event admission", event.Kind)
	case <-time.After(500 * time.Millisecond):
	}

	streamingDone := make(chan error, 1)
	go func() {
		streamingDone <- engine.steer("stream", steerAssistantDeltaIntent(llm.AssistantDelta{
			Text:  "delta",
			Phase: llm.MessagePhaseCommentary,
		}))
	}()
	select {
	case err := <-streamingDone:
		if err != nil {
			t.Fatalf("direct streaming steer: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("streaming waited behind durable Runtime Event admission")
	}
	select {
	case event := <-events:
		if event.Kind != EventAssistantDelta {
			t.Fatalf("direct streaming event kind = %v, want assistant delta", event.Kind)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("direct streaming event was not published")
	}

	release()
	select {
	case err := <-durableDone:
		if err != nil {
			t.Fatalf("durable steering after Runtime Event admission: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("durable steering did not complete after Runtime Event admission")
	}
}

func blockRuntimeEventAdmission(t *testing.T, queue *runtimecommand.Queue) func() {
	t.Helper()
	blockerStarted := make(chan struct{})
	releaseBlocker := make(chan struct{})
	var releaseOnce sync.Once
	release := func() {
		releaseOnce.Do(func() {
			close(releaseBlocker)
		})
	}
	if _, err := runtimecommand.Submit(context.Background(), queue, struct{}{}, func(
		_ runtimecommand.Admission,
		_ struct{},
		complete func(struct{}, error),
	) error {
		close(blockerStarted)
		<-releaseBlocker
		complete(struct{}{}, nil)
		return nil
	}); err != nil {
		t.Fatalf("submit Runtime Event blocker: %v", err)
	}
	select {
	case <-blockerStarted:
	case <-time.After(3 * time.Second):
		release()
		t.Fatal("Runtime Event blocker did not start")
	}
	return release
}
