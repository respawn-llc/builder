package sleepguard

import (
	"errors"
	"sync"
	"testing"
	"time"

	"core/internal/testharness/testsetup"
	"core/shared/config"
)

func TestGuardRestartsExitedPlatformInhibitorWhileActive(t *testing.T) {
	platform := &fakePlatformGuard{}
	guard := Guard{impl: platform}
	if err := guard.Acquire(); err != nil {
		t.Fatalf("acquire guard: %v", err)
	}
	t.Cleanup(guard.Release)

	platform.exitUnexpectedly()

	waitForCondition(t, func() bool {
		return platform.startCount() == 2 && platform.running()
	}, "platform inhibitor restart")
}

func TestGuardReacquiresStalePlatformInhibitorOnAcquire(t *testing.T) {
	platform := &fakePlatformGuard{}
	guard := Guard{impl: platform}
	if err := guard.Acquire(); err != nil {
		t.Fatalf("acquire guard: %v", err)
	}
	t.Cleanup(guard.Release)

	platform.markStoppedWithoutExitSignal()
	if err := guard.Acquire(); err != nil {
		t.Fatalf("re-acquire stale guard: %v", err)
	}

	if got := platform.startCount(); got != 2 {
		t.Fatalf("expected stale platform inhibitor to be reacquired, starts=%d", got)
	}
}

func TestManagerReportsRestartFailureWhenActiveInhibitorDies(t *testing.T) {
	platform := &fakePlatformGuard{}
	errCh := make(chan error, 1)
	manager, err := NewManager(config.SleepPreventionModeActive, func(err error) {
		errCh <- err
	})
	if err != nil {
		t.Fatalf("create sleep manager: %v", err)
	}
	guard, ok := manager.guard.(*Guard)
	if !ok {
		t.Fatalf("manager guard = %T, want *Guard", manager.guard)
	}
	guard.impl = platform
	t.Cleanup(manager.Close)

	manager.RuntimeActiveObserver()(true)
	restartErr := errors.New("restart failed")
	platform.failStarts(restartErr)
	platform.exitUnexpectedly()

	select {
	case got := <-errCh:
		if !errors.Is(got, restartErr) {
			t.Fatalf("expected restart error %v, got %v", restartErr, got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for manager restart failure report")
	}
}

type fakePlatformGuard struct {
	mu     sync.Mutex
	starts int
	active bool
	exit   chan struct{}
	err    error
}

func (f *fakePlatformGuard) start() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.starts++
	f.active = true
	f.exit = make(chan struct{})
	return nil
}

func (f *fakePlatformGuard) stop() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.active = false
	f.closeExitLocked()
}

func (f *fakePlatformGuard) running() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.active
}

func (f *fakePlatformGuard) exited() <-chan struct{} {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.exit
}

func (f *fakePlatformGuard) startCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.starts
}

func (f *fakePlatformGuard) exitUnexpectedly() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.active = false
	f.closeExitLocked()
}

func (f *fakePlatformGuard) markStoppedWithoutExitSignal() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.active = false
}

func (f *fakePlatformGuard) failStarts(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

func (f *fakePlatformGuard) closeExitLocked() {
	if f.exit == nil {
		return
	}
	select {
	case <-f.exit:
	default:
		close(f.exit)
	}
}

func waitForCondition(t *testing.T, condition func() bool, description string) {
	t.Helper()
	testsetup.RequireUntil(t, time.Now().Add(2*time.Second), 10*time.Millisecond, condition, "timed out waiting for %s", description)
}
