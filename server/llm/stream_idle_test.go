package llm

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestStreamIdleWatchdogFiresAfterIdleSilence(t *testing.T) {
	w := newStreamIdleWatchdog(context.Background(), 30*time.Millisecond)
	defer w.stop()

	select {
	case <-w.ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("watchdog did not fire after idle window")
	}
	if !errors.Is(context.Cause(w.ctx), ErrModelStreamStalled) {
		t.Fatalf("cause = %v, want ErrModelStreamStalled", context.Cause(w.ctx))
	}
}

func TestStreamIdleWatchdogPingResetsTimer(t *testing.T) {
	w := newStreamIdleWatchdog(context.Background(), 60*time.Millisecond)
	defer w.stop()

	deadline := time.After(300 * time.Millisecond)
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-deadline:
			if w.ctx.Err() != nil {
				t.Fatalf("watchdog fired despite regular pings: %v", context.Cause(w.ctx))
			}
			return
		case <-ticker.C:
			w.ping()
		case <-w.ctx.Done():
			t.Fatalf("watchdog fired despite regular pings: %v", context.Cause(w.ctx))
		}
	}
}

func TestStreamIdleWatchdogStopIsCleanAndIdempotent(t *testing.T) {
	w := newStreamIdleWatchdog(context.Background(), 50*time.Millisecond)
	w.stop()
	w.stop()
	if w.ctx.Err() == nil {
		t.Fatal("expected context canceled after stop")
	}
}

func TestStreamIdleWatchdogPassthroughWhenIdleNonPositive(t *testing.T) {
	w := newStreamIdleWatchdog(context.Background(), 0)
	defer w.stop()
	w.ping()
	select {
	case <-w.ctx.Done():
		t.Fatal("passthrough watchdog should not cancel on its own")
	case <-time.After(80 * time.Millisecond):
	}
}

func TestStreamIdleWatchdogStopDoesNotOverrideStallCause(t *testing.T) {
	w := newStreamIdleWatchdog(context.Background(), 20*time.Millisecond)
	select {
	case <-w.ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("watchdog did not fire after idle window")
	}
	w.stop()
	if !errors.Is(context.Cause(w.ctx), ErrModelStreamStalled) {
		t.Fatalf("stop overrode stall cause: %v", context.Cause(w.ctx))
	}
}
