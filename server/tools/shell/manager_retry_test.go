package shell

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"core/server/tools/shell/postprocess"
	"core/shared/config"
)

func TestRetryTerminalEventsRedeliversOnlyUndeliveredCompletion(t *testing.T) {
	hookCallsPath := filepath.Join(t.TempDir(), "hook-calls")
	hookPath := writeExecutableScript(t, fmt.Sprintf(
		"#!/bin/sh\nprintf x >> %q\nprintf '{\"processed\":true,\"replaced_output\":\"processed\"}'\n",
		hookCallsPath,
	))
	manager := newManagerWithPostprocessor(t, mustPostprocessRunner(t, postprocess.Settings{
		Mode:     config.ShellPostprocessingModeUser,
		HookPath: &hookPath,
	}))
	manager.SetMinimumExecToBgTime(50 * time.Millisecond)
	var attempts atomic.Int32
	deliveries := make(chan Event, 2)
	manager.SetEventHandler(func(event Event) bool {
		if event.Type != EventCompleted && event.Type != EventKilled {
			return true
		}
		attempt := attempts.Add(1)
		deliveries <- event
		return attempt > 1
	})

	releasePath := filepath.Join(t.TempDir(), "release")
	started, err := manager.Start(context.Background(), ExecRequest{
		Command:        []string{"/bin/sh", "-c", fmt.Sprintf("while [ ! -f %q ]; do sleep 0.01; done; printf done", releasePath)},
		DisplayCommand: "wait for release",
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
	commitManagerTransition(t, started)
	if err := os.WriteFile(releasePath, []byte("release"), 0o600); err != nil {
		t.Fatalf("release background shell: %v", err)
	}
	var initial Event
	select {
	case initial = <-deliveries:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for initial terminal delivery")
	}
	initialHookCalls, err := os.ReadFile(hookCallsPath)
	if err != nil {
		t.Fatalf("read initial hook calls: %v", err)
	}

	manager.RetryTerminalEvents("session-retry")
	var retried Event
	select {
	case retried = <-deliveries:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for retried terminal delivery")
	}
	if initial.Type != retried.Type ||
		initial.Snapshot.ActivityID != retried.Snapshot.ActivityID ||
		initial.NoticeSuppressed != retried.NoticeSuppressed ||
		initial.completion == nil ||
		retried.completion == nil ||
		initial.completion.source != retried.completion.source ||
		initial.completion.output.Content() != retried.completion.output.Content() {
		t.Fatalf("retried terminal event changed: initial=%+v retried=%+v", initial, retried)
	}
	hookCalls, err := os.ReadFile(hookCallsPath)
	if err != nil {
		t.Fatalf("read hook calls: %v", err)
	}
	if len(hookCalls) != len(initialHookCalls) {
		t.Fatalf("postprocessing hook calls after retry = %d, want unchanged %d", len(hookCalls), len(initialHookCalls))
	}

	manager.RetryTerminalEvents("session-retry")
	select {
	case event := <-deliveries:
		t.Fatalf("delivered acknowledged terminal event again: %+v", event)
	case <-time.After(100 * time.Millisecond):
	}
}
