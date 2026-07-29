package shell

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"core/server/tools/shell/postprocess"
	"core/shared/config"

	"github.com/google/uuid"
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

func TestWriteStdinCompletionKeepsTerminalNoticeProvisional(t *testing.T) {
	manager, events := runCompletionNoticeTest(t, "bg-1", "sleep 0.15; echo done", "bg-2", 15_000)

	select {
	case evt := <-events:
		if evt.NoticeSuppressed {
			t.Fatalf("write_stdin harvest must not suppress a terminal notice before durable acceptance, got %+v", evt)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for completion event")
	}
	waitForManagerCount(t, manager, 0, time.Second)
}

func TestWriteStdinKeepsFallbackCompletionNoticeProvisional(t *testing.T) {
	manager, events := runCompletionNoticeTest(
		t,
		"bg-large-1",
		"sleep 0.15; dd if=/dev/zero bs=1048576 count=3 2>/dev/null | tr '\\000' x",
		"bg-large-2",
		15_000,
	)

	select {
	case evt := <-events:
		if evt.completion == nil || evt.completion.source != completionOutputFallback {
			t.Fatalf("expected fallback completion event, got %+v", evt)
		}
		if evt.NoticeSuppressed {
			t.Fatalf("write_stdin harvest must not suppress a fallback terminal notice before durable acceptance, got %+v", evt)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for fallback completion event")
	}
	waitForManagerCount(t, manager, 0, 3*time.Second)
}

func TestTerminalEventEmissionDoesNotHoldPollingInteractionLock(t *testing.T) {
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
	entry, err := manager.entry("1000")
	if err != nil {
		t.Fatalf("terminal process entry: %v", err)
	}
	if !entry.interactMu.TryLock() {
		t.Fatal("terminal event handler held the polling interaction lock")
	}
	entry.interactMu.Unlock()
	pollCtx, cancel := context.WithCancel(context.Background())
	pollDone := make(chan struct{})
	pollErr := make(chan error, 1)
	go func() {
		_, err := manager.WriteStdin(pollCtx, WriteRequest{
			SessionID:      "1000",
			YieldTime:      250 * time.Millisecond,
			MaxOutputChars: 16_000,
		})
		pollErr <- err
		close(pollDone)
	}()
	cancel()
	select {
	case <-pollDone:
	case <-time.After(time.Second):
		t.Fatal("canceled poll remained blocked by terminal delivery")
	}
	if err := <-pollErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled poll error = %v, want context canceled", err)
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
	waitForManagerCount(t, manager, 0, time.Second)
}

func TestPostTransitionPresentationFailureStillRegistersAndReleasesTerminalWaiter(t *testing.T) {
	hookPath := writeExecutableScript(t, "#!/bin/sh\nflag=\"$(dirname \"$0\")/.first-call\"\nif [ ! -e \"$flag\" ]; then\n  : > \"$flag\"\n  sleep 1\nfi\nprintf '{\"processed\":false}'\n")
	runner := mustPostprocessRunner(t, postprocess.Settings{
		Mode:     config.ShellPostprocessingModeUser,
		HookPath: &hookPath,
	})
	manager, err := NewManager(
		WithMinimumExecToBgTime(50*time.Millisecond),
		WithCloseTimeouts(20*time.Millisecond, 200*time.Millisecond),
		WithPostprocessor(runner),
	)
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	t.Cleanup(func() { _ = manager.Close() })

	events := make(chan Event, 2)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	manager.SetEventHandler(func(evt Event) {
		if evt.Type == EventBackgrounded || evt.Type == EventCompleted || evt.Type == EventKilled {
			events <- evt
		}
		if evt.Type == EventBackgrounded {
			cancel()
		}
	})

	_, err = manager.Start(ctx, ExecRequest{
		Command:        []string{"/bin/sh", "-c", "sleep 0.15; printf terminal"},
		DisplayCommand: "sleep 0.15; printf terminal",
		Workdir:        t.TempDir(),
		YieldTime:      50 * time.Millisecond,
	})
	if err == nil {
		t.Fatal("expected post-transition presentation failure")
	}

	select {
	case event := <-events:
		if event.Type != EventBackgrounded {
			t.Fatalf("first event = %q, want backgrounded", event.Type)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for background registration after presentation failure")
	}
	select {
	case event := <-events:
		if event.Type != EventCompleted {
			t.Fatalf("terminal event = %q, want completed", event.Type)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("terminal waiter remained blocked after post-transition presentation failure")
	}
	waitForManagerCount(t, manager, 0, time.Second)
}

func TestTerminalDeliveryDiagnosticTransfersWithoutRetainingCause(t *testing.T) {
	manager := newShellTestManager(t, 50*time.Millisecond)
	transferred := make(chan struct{})
	manager.SetEventHandler(func(event Event) {
		if event.Type != EventCompleted && event.Type != EventKilled {
			return
		}
		cause := errors.New(string([]byte{0xff}) + strings.Repeat("x", maxTerminalDiagnosticBytes*2))
		if !manager.RecordTerminalDeliveryFailure(event.Snapshot.ID, event.Snapshot.ActivityID, cause) {
			t.Errorf("record terminal delivery failure")
		}
		close(transferred)
	})
	start := callExecCommand(t, NewExecCommandTool(t.TempDir(), 16_000, manager, "owner"), "diagnostic-transfer", map[string]any{
		"cmd":           "sleep 0.15; printf done",
		"shell":         "/bin/sh",
		"login":         false,
		"yield_time_ms": 50,
	})
	if start.IsError {
		t.Fatalf("start background process: %s", string(start.Output))
	}
	select {
	case <-transferred:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for terminal diagnostic")
	}
	diagnostic, ok := manager.TakeTerminalDeliveryDiagnostic("1000", uuidFromSnapshot(t, manager, "1000"))
	if !ok {
		t.Fatal("terminal diagnostic was not available for transfer")
	}
	if len(diagnostic.Detail) > maxTerminalDiagnosticBytes || !utf8.ValidString(diagnostic.Detail) {
		t.Fatalf("terminal diagnostic detail is not bounded valid UTF-8: %d", len(diagnostic.Detail))
	}
	if !manager.RestoreTerminalDeliveryDiagnostic(diagnostic) {
		t.Fatal("restore terminal diagnostic")
	}
	if _, ok := manager.TakeTerminalDeliveryDiagnostic("1000", diagnostic.Activity); !ok {
		t.Fatal("restored terminal diagnostic was not transferable")
	}
}

func uuidFromSnapshot(t *testing.T, manager *Manager, processID string) uuid.UUID {
	t.Helper()
	snapshot, err := manager.Snapshot(processID)
	if err != nil {
		t.Fatalf("background snapshot: %v", err)
	}
	return snapshot.ActivityID
}

func TestTerminalOwnerPollFinalizationIsOwnerRelativeAndCommitGated(t *testing.T) {
	manager := newShellTestManager(t, 50*time.Millisecond)
	terminal := make(chan Event, 1)
	manager.SetEventHandler(func(evt Event) {
		if evt.Type == EventCompleted || evt.Type == EventKilled {
			terminal <- evt
		}
	})
	execTool := NewExecCommandTool(t.TempDir(), 16_000, manager, "owner-session")
	start := callExecCommand(t, execTool, "owner-poll-start", map[string]any{
		"cmd":           "sleep 0.15; printf done",
		"shell":         "/bin/sh",
		"login":         false,
		"yield_time_ms": 50,
	})
	if start.IsError {
		t.Fatalf("background start: %s", string(start.Output))
	}
	poll, err := manager.WriteStdin(context.Background(), WriteRequest{
		SessionID:      "1000",
		YieldTime:      time.Second,
		MaxOutputChars: 16_000,
	})
	if err != nil {
		t.Fatalf("provisional terminal poll: %v", err)
	}
	if poll.ExitCode == nil {
		t.Fatalf("terminal poll did not return completion: %+v", poll)
	}
	select {
	case <-terminal:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for terminal handoff")
	}
	if manager.FinalizeTerminalOwnerPoll("remote-session", "1000") {
		t.Fatal("remote poll finalized the owner's completion")
	}
	if !manager.FinalizeTerminalOwnerPoll("owner-session", "1000") {
		t.Fatal("committed owner poll did not finalize the pending completion")
	}
	if manager.ReplayPendingTerminal("1000") {
		t.Fatal("finalized owner-poll completion became replayable")
	}
	if manager.FinalizeAutomaticTerminal("1000", uuid.New()) {
		t.Fatal("automatic finalizer settled an owner-poll completion")
	}
}

func TestAutomaticFinalizationRequiresAcknowledgedTerminalHandoff(t *testing.T) {
	manager := newShellTestManager(t, 50*time.Millisecond)
	terminal := make(chan Event, 1)
	manager.SetEventHandler(func(evt Event) {
		if evt.Type != EventCompleted && evt.Type != EventKilled {
			return
		}
		if manager.AcknowledgeTerminalHandoff(evt.Snapshot.ID, evt.Snapshot.ActivityID) != TerminalHandoffAcknowledged {
			t.Errorf("acknowledge terminal handoff")
		}
		terminal <- evt
	})
	start := callExecCommand(t, NewExecCommandTool(t.TempDir(), 16_000, manager, "owner-session"), "automatic-start", map[string]any{
		"cmd":           "sleep 0.15; printf done",
		"shell":         "/bin/sh",
		"login":         false,
		"yield_time_ms": 50,
	})
	if start.IsError {
		t.Fatalf("background start: %s", string(start.Output))
	}
	var evt Event
	select {
	case evt = <-terminal:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for acknowledged terminal handoff")
	}
	if !manager.FinalizeAutomaticTerminal(evt.Snapshot.ID, evt.Snapshot.ActivityID) {
		t.Fatal("automatic finalizer did not settle acknowledged handoff")
	}
	if manager.FinalizeTerminalOwnerPoll("owner-session", evt.Snapshot.ID) {
		t.Fatal("owner poll retroactively replaced automatic settlement")
	}
	if manager.ReplayPendingTerminal(evt.Snapshot.ID) {
		t.Fatal("automatically finalized completion became replayable")
	}
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
