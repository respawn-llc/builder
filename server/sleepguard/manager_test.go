package sleepguard

import (
	"errors"
	"sync"
	"testing"
	"time"

	"core/shared/config"
)

func TestActiveManagerAcquiresImmediatelyOnActiveState(t *testing.T) {
	manager, guard, timers := newTestManager(t)
	defer manager.Close()

	manager.RuntimeActiveObserver()(true)

	waitForCondition(t, func() bool {
		return guard.acquireCount() == 1 && guard.held()
	}, "active manager acquire")
	if timers.count() != 0 {
		t.Fatalf("expected no release timer while active, got %d", timers.count())
	}
}

func TestActiveManagerDoesNotReleaseBeforeIdleGraceTimer(t *testing.T) {
	manager, guard, timers := newTestManager(t)
	defer manager.Close()

	manager.RuntimeActiveObserver()(true)
	waitForCondition(t, func() bool { return guard.acquireCount() == 1 }, "active manager acquire")
	manager.RuntimeActiveObserver()(false)
	timer := timers.waitForTimer(t, 0)

	if timer.duration != time.Minute {
		t.Fatalf("idle grace timer duration = %s, want 1m", timer.duration)
	}
	if guard.releaseCount() != 0 {
		t.Fatalf("expected guard to remain held before grace timer, releases=%d", guard.releaseCount())
	}
}

func TestActiveManagerReleasesWhenIdleGraceTimerFires(t *testing.T) {
	manager, guard, timers := newTestManager(t)
	defer manager.Close()

	manager.RuntimeActiveObserver()(true)
	waitForCondition(t, func() bool { return guard.acquireCount() == 1 }, "active manager acquire")
	manager.RuntimeActiveObserver()(false)
	timers.waitForTimer(t, 0).Fire()

	waitForCondition(t, func() bool {
		return guard.releaseCount() == 1 && !guard.held()
	}, "idle grace release")
}

func TestActiveManagerCancelsReleaseWhenActiveDuringGrace(t *testing.T) {
	manager, guard, timers := newTestManager(t)
	defer manager.Close()

	manager.RuntimeActiveObserver()(true)
	waitForCondition(t, func() bool { return guard.acquireCount() == 1 }, "active manager acquire")
	manager.RuntimeActiveObserver()(false)
	stale := timers.waitForTimer(t, 0)
	manager.RuntimeActiveObserver()(true)
	waitForCondition(t, stale.stopped, "stale release timer stop")

	stale.Fire()

	if guard.releaseCount() != 0 {
		t.Fatalf("expected stale timer not to release guard, releases=%d", guard.releaseCount())
	}
	if guard.acquireCount() != 1 {
		t.Fatalf("expected guard to stay acquired without gap, acquires=%d", guard.acquireCount())
	}
}

func TestActiveManagerIgnoresEarlierStaleTimerDuringLaterGrace(t *testing.T) {
	manager, guard, timers := newTestManager(t)
	defer manager.Close()

	manager.RuntimeActiveObserver()(true)
	waitForCondition(t, func() bool { return guard.acquireCount() == 1 }, "active manager acquire")
	manager.RuntimeActiveObserver()(false)
	first := timers.waitForTimer(t, 0)
	manager.RuntimeActiveObserver()(true)
	waitForCondition(t, first.stopped, "first release timer stop")
	manager.RuntimeActiveObserver()(false)
	second := timers.waitForTimer(t, 1)

	first.Fire()
	if guard.releaseCount() != 0 {
		t.Fatalf("expected first stale timer not to release guard, releases=%d", guard.releaseCount())
	}

	second.Fire()
	waitForCondition(t, func() bool { return guard.releaseCount() == 1 }, "second grace release")
}

func TestActiveManagerCloseReleasesAndIgnoresLateActiveEvents(t *testing.T) {
	manager, guard, timers := newTestManager(t)

	manager.RuntimeActiveObserver()(true)
	waitForCondition(t, func() bool { return guard.acquireCount() == 1 }, "active manager acquire")
	manager.RuntimeActiveObserver()(false)
	timer := timers.waitForTimer(t, 0)

	manager.Close()
	if !timer.stopped() {
		t.Fatal("expected close to stop pending idle timer")
	}
	if guard.releaseCount() != 1 || guard.held() {
		t.Fatalf("expected close to release held guard, releases=%d held=%v", guard.releaseCount(), guard.held())
	}

	manager.RuntimeActiveObserver()(true)
	time.Sleep(20 * time.Millisecond)
	if guard.acquireCount() != 1 {
		t.Fatalf("late active event after close reacquired guard, acquires=%d", guard.acquireCount())
	}
}

func TestActiveManagerRetriesAcquireFailureWhileStillActive(t *testing.T) {
	manager, guard, timers := newTestManager(t)
	defer manager.Close()

	retryErr := errors.New("acquire failed")
	guard.setAcquireError(retryErr)
	manager.RuntimeActiveObserver()(true)
	retry := timers.waitForTimer(t, 0)

	if retry.duration != defaultActiveAcquireRetry {
		t.Fatalf("retry timer duration = %s, want %s", retry.duration, defaultActiveAcquireRetry)
	}
	if guard.acquireCount() != 1 || guard.held() {
		t.Fatalf("expected failed acquire without held guard, acquires=%d held=%v", guard.acquireCount(), guard.held())
	}

	guard.setAcquireError(nil)
	retry.Fire()
	waitForCondition(t, func() bool {
		return guard.acquireCount() == 2 && guard.held()
	}, "active acquire retry")
}

func TestActiveManagerRetriesAfterGuardRestartFailureWhileActive(t *testing.T) {
	manager, guard, _ := newTestManager(t)
	defer manager.Close()

	manager.RuntimeActiveObserver()(true)
	waitForCondition(t, func() bool {
		return guard.acquireCount() == 1 && guard.held()
	}, "active manager acquire")

	guard.failFromGuard(errors.New("restart failed"))

	waitForCondition(t, func() bool {
		return guard.acquireCount() == 2 && guard.held()
	}, "active manager reacquire after guard failure")
}

func TestManagerModeComposition(t *testing.T) {
	activeGuard := &fakeSleepInhibitor{}
	active := requireManager(t, config.SleepPreventionModeActive, withGuard(activeGuard), withReleaseTimerFactory((&manualTimerFactory{}).AfterFunc))
	defer active.Close()
	if active.RuntimeActiveObserver() == nil {
		t.Fatal("expected active mode to expose runtime active observer")
	}
	if activeGuard.acquireCount() != 0 {
		t.Fatalf("active mode acquired before runtime activity, acquires=%d", activeGuard.acquireCount())
	}

	alwaysGuard := &fakeSleepInhibitor{}
	always := requireManager(t, config.SleepPreventionModeAlways, withGuard(alwaysGuard))
	if always.RuntimeActiveObserver() != nil {
		t.Fatal("expected always mode not to expose runtime active observer")
	}
	if alwaysGuard.acquireCount() != 1 {
		t.Fatalf("always mode acquires at construction, acquires=%d", alwaysGuard.acquireCount())
	}
	always.Close()
	if alwaysGuard.releaseCount() != 1 {
		t.Fatalf("always mode releases on close, releases=%d", alwaysGuard.releaseCount())
	}

	neverGuard := &fakeSleepInhibitor{}
	never := requireManager(t, config.SleepPreventionModeNever, withGuard(neverGuard))
	if never.RuntimeActiveObserver() != nil {
		t.Fatal("expected never mode not to expose runtime active observer")
	}
	never.Close()
	if neverGuard.acquireCount() != 0 || neverGuard.releaseCount() != 0 {
		t.Fatalf("never mode should not touch guard, acquires=%d releases=%d", neverGuard.acquireCount(), neverGuard.releaseCount())
	}
}

func TestAlwaysManagerReacquiresAfterInhibitorRestartFailure(t *testing.T) {
	guard := &fakeSleepInhibitor{}
	timers := &manualTimerFactory{}
	manager := requireManager(
		t,
		config.SleepPreventionModeAlways,
		withGuard(guard),
		withAcquireRetryDelay(defaultActiveAcquireRetry),
		withReleaseTimerFactory(timers.AfterFunc),
	)
	defer manager.Close()
	waitForCondition(t, func() bool { return guard.acquireCount() == 1 && guard.held() }, "always-mode startup acquire")

	// A restart failure: the next re-acquire fails, then the guard reports the failure.
	guard.setAcquireError(errors.New("restart failed"))
	guard.failFromGuard(errors.New("inhibitor exited"))

	// Always mode must keep retrying on a timer rather than giving up permanently.
	retry := timers.waitForTimer(t, 0)
	if retry.duration != defaultActiveAcquireRetry {
		t.Fatalf("retry timer duration = %s, want %s", retry.duration, defaultActiveAcquireRetry)
	}

	// Once the inhibitor recovers, the scheduled retry re-acquires the guard.
	guard.setAcquireError(nil)
	retry.Fire()
	waitForCondition(t, func() bool { return guard.held() }, "always-mode reacquire after retry")
}

func newTestManager(t *testing.T) (*Manager, *fakeSleepInhibitor, *manualTimerFactory) {
	t.Helper()
	guard := &fakeSleepInhibitor{}
	timers := &manualTimerFactory{}
	manager := requireManager(
		t,
		config.SleepPreventionModeActive,
		withGuard(guard),
		withIdleGrace(time.Minute),
		withAcquireRetryDelay(defaultActiveAcquireRetry),
		withReleaseTimerFactory(timers.AfterFunc),
	)
	return manager, guard, timers
}

func requireManager(t *testing.T, mode config.SleepPreventionMode, options ...managerOption) *Manager {
	t.Helper()
	manager, err := NewManager(mode, nil, options...)
	if err != nil {
		t.Fatalf("create %q sleep prevention manager: %v", mode, err)
	}
	return manager
}

type fakeSleepInhibitor struct {
	mu       sync.Mutex
	acquires int
	releases int
	isHeld   bool
	handler  func(error)
	err      error
}

func (f *fakeSleepInhibitor) Acquire() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		f.acquires++
		return f.err
	}
	f.acquires++
	f.isHeld = true
	return nil
}

func (f *fakeSleepInhibitor) Release() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.releases++
	f.isHeld = false
}

func (f *fakeSleepInhibitor) SetErrorHandler(handler func(error)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.handler = handler
}

func (f *fakeSleepInhibitor) acquireCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.acquires
}

func (f *fakeSleepInhibitor) releaseCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.releases
}

func (f *fakeSleepInhibitor) held() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.isHeld
}

func (f *fakeSleepInhibitor) setAcquireError(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

func (f *fakeSleepInhibitor) failFromGuard(err error) {
	f.mu.Lock()
	f.isHeld = false
	handler := f.handler
	f.mu.Unlock()
	if handler != nil {
		handler(err)
	}
}

type manualTimerFactory struct {
	mu     sync.Mutex
	timers []*manualTimer
}

func (f *manualTimerFactory) AfterFunc(duration time.Duration, callback func()) releaseTimer {
	timer := &manualTimer{duration: duration, callback: callback}
	f.mu.Lock()
	f.timers = append(f.timers, timer)
	f.mu.Unlock()
	return timer
}

func (f *manualTimerFactory) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.timers)
}

func (f *manualTimerFactory) waitForTimer(t *testing.T, index int) *manualTimer {
	t.Helper()
	var timer *manualTimer
	waitForCondition(t, func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		if len(f.timers) <= index {
			return false
		}
		timer = f.timers[index]
		return true
	}, "manual timer creation")
	return timer
}

type manualTimer struct {
	mu        sync.Mutex
	duration  time.Duration
	callback  func()
	isStopped bool
}

func (t *manualTimer) Stop() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.isStopped {
		return false
	}
	t.isStopped = true
	return true
}

func (t *manualTimer) Fire() {
	t.callback()
}

func (t *manualTimer) stopped() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.isStopped
}
