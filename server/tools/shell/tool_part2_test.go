package shell

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func runCompletionNoticeTest(t *testing.T, execID string, command string, pollID string, pollYieldMS int) (*Manager, <-chan Event) {
	t.Helper()
	manager := newShellTestManager(t, 50*time.Millisecond)
	events := make(chan Event, 2)
	manager.SetEventHandler(func(evt Event) {
		if evt.Type == EventCompleted || evt.Type == EventKilled {
			select {
			case events <- evt:
			default:
			}
		}
	})

	result := callExecCommand(t, NewExecCommandTool(t.TempDir(), 16_000, manager, ""), execID, map[string]any{
		"cmd":           command,
		"shell":         "/bin/sh",
		"login":         false,
		"yield_time_ms": 50,
	})
	if result.IsError {
		t.Fatalf("unexpected exec_command error: %s", string(result.Output))
	}

	pollResult := callWriteStdin(t, NewWriteStdinTool(16_000, manager), pollID, map[string]any{
		"session_id":    1000,
		"yield_time_ms": pollYieldMS,
	})
	if pollResult.IsError {
		t.Fatalf("unexpected write_stdin error: %s", string(pollResult.Output))
	}
	return manager, events
}

func TestWriteStdinCompletionSuppressesBackgroundNoticeEvent(t *testing.T) {
	manager, events := runCompletionNoticeTest(t, "bg-1", "sleep 0.15; echo done", "bg-2", 800)

	select {
	case evt := <-events:
		if !evt.NoticeSuppressed {
			t.Fatalf("expected completion event notice to be suppressed after write_stdin harvest, got %+v", evt)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for completion event")
	}
	waitForManagerCount(t, manager, 0, time.Second)
}

func TestWriteStdinSuppressesFallbackCompletionNoticeEvent(t *testing.T) {
	manager, events := runCompletionNoticeTest(
		t,
		"bg-large-1",
		"sleep 0.15; dd if=/dev/zero bs=1048576 count=3 2>/dev/null | tr '\\000' x",
		"bg-large-2",
		1_500,
	)

	select {
	case evt := <-events:
		if evt.completion == nil || evt.completion.source != completionOutputFallback {
			t.Fatalf("expected fallback completion event, got %+v", evt)
		}
		if !evt.NoticeSuppressed {
			t.Fatalf("expected fallback completion event notice to be suppressed after write_stdin harvest, got %+v", evt)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for fallback completion event")
	}
	waitForManagerCount(t, manager, 0, 3*time.Second)
}

func TestTerminalEventEmissionHoldsPollingInteractionLock(t *testing.T) {
	workspace := t.TempDir()
	manager := newShellTestManager(t, 50*time.Millisecond)
	execTool := NewExecCommandTool(workspace, 16_000, manager, "")
	terminalHandlerStarted := make(chan struct{})
	releaseTerminalHandler := make(chan struct{})
	events := make(chan Event, 1)
	manager.SetEventHandler(func(evt Event) {
		if evt.Type != EventCompleted && evt.Type != EventKilled {
			return
		}
		close(terminalHandlerStarted)
		<-releaseTerminalHandler
		events <- evt
	})

	result := callExecCommand(t, execTool, "bg-lock-1", map[string]any{
		"cmd":           "sleep 0.15; echo done",
		"shell":         "/bin/sh",
		"login":         false,
		"yield_time_ms": 50,
	})
	if result.IsError {
		t.Fatalf("unexpected exec_command error: %s", string(result.Output))
	}

	select {
	case <-terminalHandlerStarted:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for terminal event handler")
	}
	pollDone := make(chan struct{})
	go func() {
		_, _ = manager.WriteStdin(context.Background(), WriteRequest{
			SessionID:      "1000",
			YieldTime:      250 * time.Millisecond,
			MaxOutputChars: 16_000,
		})
		close(pollDone)
	}()
	select {
	case <-pollDone:
		t.Fatal("poll acquired the interaction lock while terminal event delivery was in progress")
	case <-time.After(100 * time.Millisecond):
	}
	close(releaseTerminalHandler)
	select {
	case evt := <-events:
		if evt.NoticeSuppressed {
			t.Fatalf("terminal event claimed notice suppression before polling could acquire the lock: %+v", evt)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for terminal event")
	}
	select {
	case <-pollDone:
	case <-time.After(time.Second):
		t.Fatal("poll did not complete after terminal event delivery")
	}
	waitForManagerCount(t, manager, 0, time.Second)
}

func TestExecCommandClosesStdinForNonInteractiveProcess(t *testing.T) {
	workspace := t.TempDir()
	manager := newBackgroundTestManager(t)
	events := make(chan Event, 1)
	manager.SetEventHandler(func(evt Event) {
		select {
		case events <- evt:
		default:
		}
	})
	execTool := NewExecCommandTool(workspace, 16_000, manager, "")

	result := callExecCommand(t, execTool, "eof-1", map[string]any{
		"cmd":           "if read line; then echo line:$line; else echo eof; fi",
		"shell":         "/bin/sh",
		"login":         false,
		"yield_time_ms": 1_500,
	})
	if result.IsError {
		t.Fatalf("unexpected exec_command error: %s", string(result.Output))
	}
	var output string
	if err := json.Unmarshal(result.Output, &output); err != nil {
		t.Fatalf("exec_command result must be a JSON string: %v", err)
	}
	if output == "" {
		t.Fatal("foreground EOF completion must have non-empty output")
	}
	if result.PresentationDelta != nil && result.PresentationDelta.MovedToBackground {
		t.Fatalf("foreground EOF completion must not be backgrounded: %+v", result.PresentationDelta)
	}
	waitForManagerCount(t, manager, 0, 3*time.Second)
	select {
	case evt := <-events:
		t.Fatalf("did not expect foreground exec_command event, got %+v", evt)
	default:
	}
}

func TestManagerCloseKillsRunningProcesses(t *testing.T) {
	manager, err := NewManager(WithMinimumExecToBgTime(50*time.Millisecond), WithCloseTimeouts(20*time.Millisecond, 200*time.Millisecond))
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	events := make(chan Event, 1)
	manager.SetEventHandler(func(evt Event) {
		if evt.Type == EventKilled {
			select {
			case events <- evt:
			default:
			}
		}
	})

	result, err := manager.Start(context.Background(), ExecRequest{
		Command:        []string{"/bin/sh", "-c", "trap '' TERM INT; sleep 30"},
		DisplayCommand: "trap '' TERM INT; sleep 30",
		Workdir:        t.TempDir(),
		YieldTime:      50 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("start background process: %v", err)
	}
	if !result.MovedToBackground || !result.Running {
		t.Fatalf("expected background process, got %+v", result)
	}
	if manager.Count() != 1 {
		t.Fatalf("manager count = %d, want 1", manager.Count())
	}

	start := time.Now()
	if err := manager.Close(); err != nil {
		t.Fatalf("close manager: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("close took too long: %v", elapsed)
	}

	select {
	case evt := <-events:
		if evt.Snapshot.ID != result.SessionID {
			t.Fatalf("killed event id = %s, want %s", evt.Snapshot.ID, result.SessionID)
		}
		if evt.Snapshot.State != "killed" {
			t.Fatalf("killed event state = %s, want killed", evt.Snapshot.State)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for killed event")
	}
	waitForManagerCount(t, manager, 0, time.Second)
}
