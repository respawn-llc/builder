package shell

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestWriteStdinHarvestWaitsForTerminalEventDelivery(t *testing.T) {
	manager := newShellTestManager(t, 50*time.Millisecond)
	terminalStarted := make(chan struct{})
	releaseTerminal := make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(releaseTerminal)
		}
	}()
	manager.SetEventHandler(func(evt Event) {
		if evt.Type != EventCompleted && evt.Type != EventKilled {
			return
		}
		close(terminalStarted)
		<-releaseTerminal
	})

	started, err := manager.Start(context.Background(), ExecRequest{
		Command:        []string{"/bin/sh", "-c", "sleep 0.15; printf done"},
		DisplayCommand: "sleep briefly",
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

	writeDone := make(chan error, 1)
	go func() {
		_, err := manager.WriteStdin(context.Background(), WriteRequest{
			SessionID:      started.SessionID,
			YieldTime:      15 * time.Second,
			MaxOutputChars: 16_000,
		})
		writeDone <- err
	}()

	select {
	case <-terminalStarted:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for terminal event delivery")
	}
	select {
	case err := <-writeDone:
		t.Fatalf("write_stdin returned before terminal event delivery completed: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	close(releaseTerminal)
	released = true
	select {
	case err := <-writeDone:
		if err != nil {
			t.Fatalf("write_stdin completion: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("write_stdin did not return after terminal event delivery")
	}
	waitForManagerCount(t, manager, 0, time.Second)
}

func TestWriteStdinHarvestCancellationDoesNotWaitForTerminalEventDelivery(t *testing.T) {
	manager := newShellTestManager(t, 50*time.Millisecond)
	terminalStarted := make(chan struct{})
	releaseTerminal := make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(releaseTerminal)
		}
	}()
	manager.SetEventHandler(func(evt Event) {
		if evt.Type != EventCompleted && evt.Type != EventKilled {
			return
		}
		close(terminalStarted)
		<-releaseTerminal
	})

	started, err := manager.Start(context.Background(), ExecRequest{
		Command:        []string{"/bin/sh", "-c", "sleep 0.15; printf done"},
		DisplayCommand: "sleep briefly",
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

	ctx, cancel := context.WithCancel(context.Background())
	writeDone := make(chan error, 1)
	go func() {
		_, err := manager.WriteStdin(ctx, WriteRequest{
			SessionID:      started.SessionID,
			YieldTime:      15 * time.Second,
			MaxOutputChars: 16_000,
		})
		writeDone <- err
	}()

	select {
	case <-terminalStarted:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for terminal event delivery")
	}
	select {
	case err := <-writeDone:
		t.Fatalf("write_stdin returned before cancellation: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	cancel()
	select {
	case err := <-writeDone:
		var pollErr *PollingCanceledError
		if !errors.As(err, &pollErr) {
			t.Fatalf("write_stdin cancellation error = %T %v, want PollingCanceledError", err, err)
		}
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("write_stdin cancellation error = %v, want context canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("write_stdin cancellation waited for terminal event delivery")
	}

	close(releaseTerminal)
	released = true
	waitForManagerCount(t, manager, 0, time.Second)
}
