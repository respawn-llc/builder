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
		t.Fatalf("aborted transition published lifecycle event: %q", event.Type)
	case <-time.After(300 * time.Millisecond):
	}
	waitForManagerCount(t, manager, 0, 2*time.Second)
}
