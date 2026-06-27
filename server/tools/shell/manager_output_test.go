package shell

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestManagerSubscribeOutputWaitsForLogFlushNotification(t *testing.T) {
	manager := newBackgroundTestManager(t)
	workspace := t.TempDir()

	result, err := manager.Start(context.Background(), ExecRequest{
		Command:        []string{"sh", "-c", "sleep 0.15; printf 'flush-ready\\n'; sleep 1"},
		DisplayCommand: "flush-notify",
		Workdir:        workspace,
		YieldTime:      50 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if !result.Backgrounded {
		t.Fatalf("expected backgrounded process, got %+v", result)
	}
	defer func() { _ = manager.Kill(result.SessionID) }()

	sub, err := manager.SubscribeOutput(context.Background(), result.SessionID, 0)
	if err != nil {
		t.Fatalf("SubscribeOutput: %v", err)
	}
	defer func() { _ = sub.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 600*time.Millisecond)
	defer cancel()
	chunk, err := sub.Next(ctx)
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if !strings.Contains(chunk.Text, "flush-ready") {
		t.Fatalf("expected flushed output, got %+v", chunk)
	}
}
