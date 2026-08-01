package shell

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestRetryTerminalEventsRedeliversOnlyUndeliveredCompletion(t *testing.T) {
	manager := newShellTestManager(t, 50*time.Millisecond)
	var attempts atomic.Int32
	deliveries := make(chan int32, 2)
	manager.SetEventHandler(func(event Event) bool {
		if event.Type != EventCompleted && event.Type != EventKilled {
			return true
		}
		attempt := attempts.Add(1)
		deliveries <- attempt
		return attempt > 1
	})

	started, err := manager.Start(context.Background(), ExecRequest{
		Command:        []string{"/bin/sh", "-c", "sleep 0.15; printf done"},
		DisplayCommand: "sleep briefly",
		OwnerSessionID: "session-retry",
		Workdir:        t.TempDir(),
		YieldTime:      50 * time.Millisecond,
		MaxOutputChars: 16_000,
	})
	if err != nil {
		t.Fatalf("start background process: %v", err)
	}
	if !started.Backgrounded {
		t.Fatalf("process must transition to background, got %+v", started)
	}
	select {
	case attempt := <-deliveries:
		if attempt != 1 {
			t.Fatalf("initial terminal delivery attempt = %d, want 1", attempt)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for initial terminal delivery")
	}

	manager.RetryTerminalEvents("session-retry")
	select {
	case attempt := <-deliveries:
		if attempt != 2 {
			t.Fatalf("retried terminal delivery attempt = %d, want 2", attempt)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for retried terminal delivery")
	}

	manager.RetryTerminalEvents("session-retry")
	select {
	case attempt := <-deliveries:
		t.Fatalf("delivered acknowledged terminal event again on attempt %d", attempt)
	case <-time.After(100 * time.Millisecond):
	}
}
