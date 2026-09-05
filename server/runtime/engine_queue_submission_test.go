package runtime

import (
	"context"
	"errors"
	"testing"
	"time"

	"core/server/llm"
	"core/server/tools"
	"core/shared/runtimeinput"
	"core/shared/textutil"
)

func TestPostTurnQueueStartsAfterActiveTurnCompletes(t *testing.T) {
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
		queued, err := engine.QueueUserInput(t.Context(), plainQueuedUserInput("queued input"))
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
		t.Fatal("post-turn Queue did not accept while the active turn was running")
	}
	if calls := fakeClientCallCount(client); calls != 0 {
		t.Fatalf("queued work model calls before active turn completion = %d, want 0", calls)
	}
	pending, err := engine.PendingWorkSnapshot()
	if err != nil {
		t.Fatalf("PendingWorkSnapshot: %v", err)
	}
	if len(pending.Items) != 1 ||
		pending.Items[0].ID.String() != queued.ID ||
		pending.Items[0].Lane != runtimeinput.PendingWorkLaneQueue {
		t.Fatalf("active post-turn Pending Work = %+v", pending.Items)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("active turn completion: %v", err)
	}
	if queued.ID == "" {
		t.Fatal("post-turn Queue accepted an empty item")
	}
	waitEngineLifecycleTasks(t, engine)
	if calls := fakeClientCallCount(client); calls != 1 {
		t.Fatalf("queued work model calls = %d, want 1", calls)
	}
	if engine.HasQueuedUserWork() {
		t.Fatal("post-turn Queue remained pending after the active turn completed")
	}
}

func TestPostTurnQueueStartsImmediatelyWhenRuntimeIsIdle(t *testing.T) {
	client := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{
			Role:    llm.RoleAssistant,
			Content: textutil.Value("queued work handled"),
			Phase:   textutil.Value(llm.MessagePhaseFinal),
		},
	}}}
	engine := mustNewTestEngine(t, mustCreateTestSession(t), client, tools.NewRegistry(), Config{Model: "gpt-5"})

	queued, err := engine.QueueUserInput(t.Context(), plainQueuedUserInput("queued input"))
	if err != nil {
		t.Fatalf("QueueUserMessage: %v", err)
	}
	if queued.ID == "" {
		t.Fatal("QueueUserMessage returned no Queue Item identity")
	}
	waitEngineLifecycleTasks(t, engine)
	if calls := fakeClientCallCount(client); calls != 1 {
		t.Fatalf("queued work model calls = %d, want 1", calls)
	}
	if engine.HasQueuedUserWork() {
		t.Fatal("idle Queue item remained pending after its immediate turn")
	}
}

func TestQueuedUserMessageCallerCancellationStopsWaitAndPreventsLaterAcceptance(t *testing.T) {
	engine := mustNewExecTestEngine(t, mustCreateTestSession(t), &fakeClient{}, Config{Model: "gpt-5"})
	caller, cancel := context.WithCancel(t.Context())
	reached := make(chan struct{})
	accept := func(commit func() (bool, error)) (bool, error) {
		close(reached)
		<-caller.Done()
		return false, context.Cause(caller)
	}
	done := make(chan error, 1)
	go func() {
		_, err := engine.QueueUserInputWithAcceptance(caller, plainQueuedUserInput("canceled input"), accept)
		done <- err
	}()
	select {
	case <-reached:
	case <-time.After(runtimeTestSynchronizationTimeout):
		t.Fatal("Queue acceptance was not reached")
	}

	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled Queue wait = %v, want canceled", err)
		}
	case <-time.After(runtimeTestSynchronizationTimeout):
		t.Fatal("canceled Queue caller remained blocked")
	}
	if engine.HasQueuedUserWork() {
		t.Fatal("canceled caller created Pending Work")
	}
}
