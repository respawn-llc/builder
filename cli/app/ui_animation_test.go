package app

import (
	"testing"
	"time"

	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/transcript"
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
	m.setRuntimeActivityBusyForTest(true)
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

func TestTranscriptRuntimeProgressStartsAndRearmsSpinner(t *testing.T) {
	oldInterval := spinnerTickInterval
	oldGrace := spinnerTickRearmGrace
	oldNow := uiAnimationNow
	spinnerTickInterval = 10 * time.Millisecond
	spinnerTickRearmGrace = 30 * time.Millisecond
	anchor := time.Unix(1_700_000_150, 0)
	now := anchor
	uiAnimationNow = func() time.Time { return now }
	t.Cleanup(func() {
		spinnerTickInterval = oldInterval
		spinnerTickRearmGrace = oldGrace
		uiAnimationNow = oldNow
	})

	m := newAnimationTranscriptModel(t)
	running := ongoingTranscriptMessage(2, clientui.TranscriptMessageRuntimeReadModelUpdate)
	running.Payload.RuntimeReadModelUpdate.Activity = clientui.RuntimeActivity{
		State: clientui.RuntimeActivityRunning,
		ActiveStep: &clientui.RuntimeActiveStep{
			RunID:      ongoingTestRunID(),
			StepID:     ongoingTestStepID(),
			ActiveKind: clientui.RuntimeActivityActiveKindUserTurn,
		},
	}
	next, cmd := m.Update(ongoingTranscriptEvent{Kind: ongoingTranscriptEventMessage, SourceSessionID: ongoingTestSessionID(), Message: running})
	updated := next.(*uiModel)
	if !updated.isBusy() || updated.spinnerTickToken == 0 || updated.spinnerTickDue.IsZero() || cmd == nil {
		t.Fatalf(
			"runtime progress did not start spinner: busy=%t token=%d due=%s cmd=%v",
			updated.isBusy(),
			updated.spinnerTickToken,
			updated.spinnerTickDue,
			cmd,
		)
	}

	previousToken := updated.spinnerTickToken
	updated.spinnerTickDue = anchor.Add(spinnerTickInterval)
	now = updated.spinnerTickDue.Add(spinnerTickRearmGrace + time.Millisecond)
	next, cmd = updated.Update(ongoingTranscriptEvent{
		Kind:            ongoingTranscriptEventMessage,
		SourceSessionID: ongoingTestSessionID(),
		Message:         animationAssistantDeltaMessage(3),
	})
	updated = next.(*uiModel)
	if updated.spinnerTickToken == 0 || updated.spinnerTickToken == previousToken {
		t.Fatalf("runtime progress did not rearm spinner token: previous=%d current=%d", previousToken, updated.spinnerTickToken)
	}
	if !updated.spinnerTickDue.After(now) || cmd == nil {
		t.Fatalf("runtime progress did not schedule a fresh spinner tick: due=%s now=%s cmd=%v", updated.spinnerTickDue, now, cmd)
	}
}

func TestTranscriptReviewerLifecycleStartsAndStopsSpinner(t *testing.T) {
	oldNow := uiAnimationNow
	uiAnimationNow = func() time.Time { return time.Unix(1_700_000_200, 0) }
	t.Cleanup(func() { uiAnimationNow = oldNow })

	m := newAnimationTranscriptModel(t)
	startedMessage := clientui.TranscriptMessage{
		Sequence: 2,
		Kind:     clientui.TranscriptMessageReviewerState,
		Payload: clientui.TranscriptPayload{ReviewerState: &clientui.TranscriptReviewerState{
			StepID: ongoingTestStepID(),
			State:  clientui.ReviewerStateRunning,
		}},
	}
	next, _ := m.Update(ongoingTranscriptEvent{Kind: ongoingTranscriptEventMessage, SourceSessionID: ongoingTestSessionID(), Message: startedMessage})
	started := next.(*uiModel)
	if !started.isReviewerRunning() || started.spinnerTickToken == 0 {
		t.Fatalf("reviewer start did not start spinner: running=%t token=%d", started.isReviewerRunning(), started.spinnerTickToken)
	}

	completedMessage := startedMessage
	completedMessage.Sequence = 3
	completedMessage.Payload.ReviewerState = &clientui.TranscriptReviewerState{
		StepID: ongoingTestStepID(),
		State:  clientui.ReviewerStateCompleted,
	}
	next, _ = started.Update(ongoingTranscriptEvent{Kind: ongoingTranscriptEventMessage, SourceSessionID: ongoingTestSessionID(), Message: completedMessage})
	completed := next.(*uiModel)
	if completed.isReviewerRunning() || completed.spinnerTickToken != 0 {
		t.Fatalf("reviewer completion did not stop spinner: running=%t token=%d", completed.isReviewerRunning(), completed.spinnerTickToken)
	}
}

func newAnimationTranscriptModel(t *testing.T) *uiModel {
	t.Helper()
	surface := &ongoingSurfaceSpy{}
	m := newProjectedStaticUIModel()
	runtimeClient := &sessionRuntimeClient{sessionID: ongoingTestSessionID().String()}
	m.ongoingTranscript = newOngoingTranscriptController(
		surface,
		m.ongoingFrameInput,
		runtimeClient.admitTranscriptMessageState,
		m.applyAdmittedTranscriptMessageState,
		m,
	)
	next, _ := m.Update(ongoingTranscriptEvent{
		Kind:            ongoingTranscriptEventMessage,
		SourceSessionID: ongoingTestSessionID(),
		Message:         ongoingHydrationMessage(1),
	})
	return next.(*uiModel)
}

func animationAssistantDeltaMessage(sequence uint64) clientui.TranscriptMessage {
	streamID := runtimeids.NewAssistantStreamID()
	return clientui.TranscriptMessage{
		Sequence: sequence,
		Kind:     clientui.TranscriptMessageAssistantDelta,
		Payload: clientui.TranscriptPayload{AssistantDelta: &clientui.TranscriptAssistantDelta{
			StepID:   ongoingTestStepID(),
			StreamID: streamID,
			Delta:    "working",
			Phase:    transcript.AssistantPhaseCommentary,
		}},
	}
}
