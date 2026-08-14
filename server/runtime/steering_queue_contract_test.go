package runtime

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"core/server/session"
	"core/shared/clientui"
	"core/shared/config"
)

func TestSteeringQueueFIFOAndDrainingHeadContract(t *testing.T) {
	queue := newSteeringQueue()
	first := newUserShellQueueEntry("first", nil)
	second := newUserShellQueueEntry("second", nil)
	third := newUserShellQueueEntry("third", nil)
	_, _ = queue.append(first)
	_, _ = queue.append(second)
	head, ok := queue.beginNext(true)
	if !ok || head != first || !queue.pendingWork() || queue.finishDrain(nil) {
		t.Fatal("first dequeued head did not retain FIFO Draining ownership")
	}
	_, _ = queue.append(third)
	_ = queue.finishCurrent(first)
	for _, want := range []*steeringQueueEntry{second, third} {
		head, ok = queue.beginNext(true)
		if !ok || head != want {
			t.Fatal("arrival during drain changed accepted FIFO order")
		}
		_ = queue.finishCurrent(want)
	}
	if !queue.finishDrain(nil) || queue.pendingWork() {
		t.Fatal("queue did not become Idle after finalizing the accepted FIFO")
	}
}

func TestSteeringQueuePanicsOnTenThousandthPendingIntent(t *testing.T) {
	providerStarted := make(chan struct{})
	releaseProvider := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-releaseProvider:
		default:
			close(releaseProvider)
		}
	})
	client := &hookClient{
		response: commentaryResponse(
			"complete",
			completeNodeCall("complete", json.RawMessage(`{"commentary":"done","summary":"done"}`)),
		),
		beforeReturn: func() error {
			close(providerStarted)
			<-releaseProvider
			return nil
		},
	}
	engine := mustNewWorkflowTestEngine(
		t,
		mustCreateTestSession(t),
		client,
		testWorkflowConfig(&fakeWorkflowController{}, config.WorkflowCompletionModeTool),
		Config{},
	)
	submitDone := make(chan error, 1)
	go func() {
		_, err := engine.SubmitWorkflowTurn(context.Background())
		submitDone <- err
	}()
	select {
	case <-providerStarted:
	case <-time.After(runtimeTestSynchronizationTimeout):
		t.Fatal("protected Workflow Step did not start")
	}

	var (
		appliedMu sync.Mutex
		applied   []int
	)
	for index := 0; index < maxPendingSteeringIntents; index++ {
		index := index
		if err := engine.SubmitWorktreeTransition(func(
			context.Context,
			func(clientui.SessionExecutionTarget, *session.WorktreeReminderState) error,
		) error {
			appliedMu.Lock()
			applied = append(applied, index)
			appliedMu.Unlock()
			return nil
		}); err != nil {
			t.Fatalf("append Steering Intent %d: %v", index+1, err)
		}
	}
	engine.steering.mu.Lock()
	pending := len(engine.steering.pending)
	engine.steering.mu.Unlock()
	if pending != maxPendingSteeringIntents {
		t.Fatalf("pending Steering Intents = %d, want %d", pending, maxPendingSteeringIntents)
	}

	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("10,000th pending Steering Intent did not panic")
			}
		}()
		_ = engine.SubmitWorktreeTransition(func(
			context.Context,
			func(clientui.SessionExecutionTarget, *session.WorktreeReminderState) error,
		) error {
			return nil
		})
	}()
	engine.steering.mu.Lock()
	pending = len(engine.steering.pending)
	engine.steering.mu.Unlock()
	if pending != maxPendingSteeringIntents {
		t.Fatalf("panic changed pending Steering count to %d", pending)
	}

	close(releaseProvider)
	select {
	case err := <-submitDone:
		if err != nil {
			t.Fatalf("SubmitWorkflowTurn: %v", err)
		}
	case <-time.After(runtimeTestSynchronizationTimeout):
		t.Fatal("Workflow turn did not finish after releasing the provider")
	}
	waitEngineLifecycleTasks(t, engine)
	appliedMu.Lock()
	defer appliedMu.Unlock()
	if len(applied) != maxPendingSteeringIntents {
		t.Fatalf("applied Steering Intents = %d, want %d", len(applied), maxPendingSteeringIntents)
	}
	for index, value := range applied {
		if value != index {
			t.Fatalf("applied Steering order[%d] = %d", index, value)
		}
	}
}
