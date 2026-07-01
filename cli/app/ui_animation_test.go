package app

import (
	"testing"
	"time"

	"core/server/runtime"
	"core/shared/clientui"
)

func TestFrameAnimationClockUsesElapsedFrameBoundaries(t *testing.T) {
	var clock frameAnimationClock
	anchor := time.Unix(1_700_000_000, 0)
	clock.Start(anchor)

	if got := clock.Frame(anchor.Add(-time.Millisecond), 8, 80*time.Millisecond); got != 0 {
		t.Fatalf("expected negative elapsed frame to clamp to 0, got %d", got)
	}
	if got := clock.Frame(anchor.Add(79*time.Millisecond), 8, 80*time.Millisecond); got != 0 {
		t.Fatalf("expected first frame before boundary, got %d", got)
	}
	if got := clock.Frame(anchor.Add(80*time.Millisecond), 8, 80*time.Millisecond); got != 1 {
		t.Fatalf("expected second frame at first boundary, got %d", got)
	}
	if got := clock.Frame(anchor.Add(640*time.Millisecond), 8, 80*time.Millisecond); got != 0 {
		t.Fatalf("expected frame index to wrap after full cycle, got %d", got)
	}
	if got := clock.NextDelay(anchor.Add(241*time.Millisecond), 80*time.Millisecond); got != 79*time.Millisecond {
		t.Fatalf("expected next delay aligned to next frame boundary, got %s", got)
	}
}

func TestHandleSpinnerTickJumpsFromElapsedTimeAndKeepsBoundaryAlignedDelay(t *testing.T) {
	oldInterval := spinnerTickInterval
	spinnerTickInterval = 10 * time.Millisecond
	t.Cleanup(func() { spinnerTickInterval = oldInterval })

	anchor := time.Unix(1_700_000_100, 0)
	m := newProjectedStaticUIModel()
	m.setBusy(true)
	m.spinnerTickToken = 1
	m.spinnerGeneration = 1
	m.spinnerClock.Start(anchor)

	tickAt := anchor.Add(35 * time.Millisecond)
	next, cmd := m.inputController().handleSpinnerTick(spinnerTickMsg{token: 1, at: tickAt})
	updated := next.(*uiModel)
	if got, want := updated.spinnerFrame, 3; got != want {
		t.Fatalf("expected late tick to jump to frame %d from elapsed time, got %d", want, got)
	}
	if got, want := updated.spinnerClock.NextDelay(tickAt, spinnerTickInterval), 5*time.Millisecond; got != want {
		t.Fatalf("expected next delay %s after late tick, got %s", want, got)
	}
	if got, want := updated.spinnerTickDue, tickAt.Add(5*time.Millisecond); !got.Equal(want) {
		t.Fatalf("expected next tick due at %s after late tick, got %s", want, got)
	}
	if cmd == nil {
		t.Fatal("expected spinner tick to schedule next boundary-aligned tick")
	}
}

func TestRuntimeBusyEventStartsSpinnerTicking(t *testing.T) {
	oldNow := uiAnimationNow
	anchor := time.Unix(1_700_000_150, 0)
	uiAnimationNow = func() time.Time { return anchor }
	t.Cleanup(func() { uiAnimationNow = oldNow })

	m := newProjectedStaticUIModel()
	next, cmd := m.Update(runtimeEventMsg{event: clientui.Event{
		Kind:     clientui.EventRunStateChanged,
		RunState: &clientui.RunState{Lifecycle: clientui.MustRunLifecycle(clientui.RunLifecycleRunning, clientui.RunModeTurn)},
	}})
	updated := next.(*uiModel)
	if !updated.isBusy() {
		t.Fatal("expected runtime busy event to set busy")
	}
	if updated.spinnerTickToken == 0 {
		t.Fatal("expected runtime busy event to start spinner ticking")
	}
	if updated.spinnerTickDue.IsZero() {
		t.Fatal("expected runtime busy event to record next spinner tick deadline")
	}
	if cmd == nil {
		t.Fatal("expected runtime busy event to schedule spinner tick")
	}
}

func TestRuntimeEventRearmsExpiredSpinnerTick(t *testing.T) {
	oldInterval := spinnerTickInterval
	oldGrace := spinnerTickRearmGrace
	oldNow := uiAnimationNow
	spinnerTickInterval = 10 * time.Millisecond
	spinnerTickRearmGrace = 30 * time.Millisecond
	anchor := time.Unix(1_700_000_175, 0)
	now := anchor
	uiAnimationNow = func() time.Time { return now }
	t.Cleanup(func() {
		spinnerTickInterval = oldInterval
		spinnerTickRearmGrace = oldGrace
		uiAnimationNow = oldNow
	})

	m := newProjectedStaticUIModel()
	m.setBusy(true)
	m.spinnerGeneration = 1
	m.spinnerTickToken = 1
	m.spinnerClock.Start(anchor)
	m.spinnerTickDue = anchor.Add(spinnerTickInterval)
	now = anchor.Add(spinnerTickInterval + spinnerTickRearmGrace + time.Millisecond)

	next, cmd := m.Update(runtimeEventMsg{event: clientui.Event{
		Kind:           clientui.EventAssistantDelta,
		StepID:         "step-1",
		AssistantDelta: "working",
	}})
	updated := next.(*uiModel)
	if updated.spinnerTickToken == 1 {
		t.Fatal("expected expired spinner tick to be replaced with a fresh token")
	}
	if updated.spinnerTickToken == 0 {
		t.Fatal("expected spinner to remain active")
	}
	if !updated.spinnerTickDue.After(now) {
		t.Fatalf("expected rearmed spinner due after current time, got due=%s now=%s", updated.spinnerTickDue, now)
	}
	if cmd == nil {
		t.Fatal("expected runtime event to rearm expired spinner tick")
	}
}

func TestRuntimeEventRearmsActiveSpinnerTickBeforeGrace(t *testing.T) {
	oldInterval := spinnerTickInterval
	oldGrace := spinnerTickRearmGrace
	oldNow := uiAnimationNow
	spinnerTickInterval = 10 * time.Millisecond
	spinnerTickRearmGrace = 30 * time.Second
	anchor := time.Unix(1_700_000_185, 0)
	now := anchor
	uiAnimationNow = func() time.Time { return now }
	t.Cleanup(func() {
		spinnerTickInterval = oldInterval
		spinnerTickRearmGrace = oldGrace
		uiAnimationNow = oldNow
	})

	m := newProjectedStaticUIModel()
	m.setBusy(true)
	m.spinnerGeneration = 1
	m.spinnerTickToken = 1
	m.spinnerClock.Start(anchor)
	m.spinnerTickDue = anchor.Add(spinnerTickInterval)
	now = anchor.Add(25 * time.Millisecond)

	next, cmd := m.Update(runtimeEventMsg{event: clientui.Event{
		Kind:           clientui.EventAssistantDelta,
		StepID:         "step-1",
		AssistantDelta: "working",
	}})
	updated := next.(*uiModel)
	if updated.spinnerTickToken == 1 {
		t.Fatal("expected runtime progress event to replace active spinner token")
	}
	if got, want := updated.spinnerFrame, 2; got != want {
		t.Fatalf("expected runtime progress event to advance spinner frame to %d, got %d", want, got)
	}
	if !updated.spinnerTickDue.After(now) {
		t.Fatalf("expected rearmed spinner due after current time, got due=%s now=%s", updated.spinnerTickDue, now)
	}
	if cmd == nil {
		t.Fatal("expected runtime progress event to schedule fresh spinner tick")
	}
}

func TestReviewerOnlyRuntimeEventStartsAdvancesAndStopsSpinner(t *testing.T) {
	oldInterval := spinnerTickInterval
	oldNow := uiAnimationNow
	spinnerTickInterval = 10 * time.Millisecond
	anchor := time.Unix(1_700_000_200, 0)
	uiAnimationNow = func() time.Time { return anchor }
	t.Cleanup(func() {
		spinnerTickInterval = oldInterval
		uiAnimationNow = oldNow
	})

	m := newProjectedStaticUIModel()
	next, _ := m.Update(projectedRuntimeEventMsg(runtime.Event{Kind: runtime.EventReviewerStarted}))
	started := next.(*uiModel)
	if !started.isReviewerRunning() {
		t.Fatal("expected reviewer to start running")
	}
	if started.spinnerTickToken == 0 {
		t.Fatal("expected reviewer-only runtime event to start spinner ticking")
	}

	token := started.spinnerTickToken
	tickAt := anchor.Add(25 * time.Millisecond)
	next, _ = started.Update(spinnerTickMsg{token: token, at: tickAt})
	advanced := next.(*uiModel)
	if got, want := advanced.spinnerFrame, 2; got != want {
		t.Fatalf("expected reviewer-only spinner to advance to frame %d, got %d", want, got)
	}

	next, _ = advanced.Update(projectedRuntimeEventMsg(runtime.Event{Kind: runtime.EventReviewerCompleted}))
	completed := next.(*uiModel)
	if completed.isReviewerRunning() {
		t.Fatal("expected reviewer running state cleared on completion")
	}
	if completed.spinnerTickToken != 0 {
		t.Fatalf("expected reviewer completion to stop spinner ticking, got token %d", completed.spinnerTickToken)
	}
}
