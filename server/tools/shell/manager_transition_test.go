package shell

import (
	"context"
	"errors"
	"testing"
	"time"

	"core/server/tools/shell/postprocess"
	"core/shared/config"
)

func TestBackgroundTransitionRegistersBeforePresentationFailureAndTerminalExit(t *testing.T) {
	hookPath := writeExecutableScript(t, "#!/bin/sh\nsleep 1\nprintf '{\"processed\":true,\"replaced_output\":\"processed\"}'\n")
	manager := newManagerWithPostprocessor(t, mustPostprocessRunner(t, postprocess.Settings{
		Mode:     config.ShellPostprocessingModeUser,
		HookPath: &hookPath,
	}))
	events := make(chan Event, 2)
	manager.SetEventHandler(func(event Event) bool {
		if event.Type == EventBackgrounded || event.Type == EventCompleted || event.Type == EventKilled {
			events <- event
		}
		return true
	})

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	_, err := manager.Start(ctx, ExecRequest{
		Command:        []string{"/bin/sh", "-c", "sleep 0.1"},
		DisplayCommand: "sleep briefly",
		Workdir:        t.TempDir(),
		YieldTime:      50 * time.Millisecond,
		MaxOutputChars: 1_000,
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("transition presentation error = %v, want context deadline exceeded", err)
	}

	select {
	case event := <-events:
		if event.Type != EventBackgrounded {
			t.Fatalf("first lifecycle event = %q, want %q", event.Type, EventBackgrounded)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for background registration")
	}
	select {
	case event := <-events:
		if event.Type != EventCompleted {
			t.Fatalf("terminal lifecycle event = %q, want %q", event.Type, EventCompleted)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for terminal delivery")
	}
}
