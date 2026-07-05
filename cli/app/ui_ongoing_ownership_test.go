package app

import (
	"bytes"
	"reflect"
	"testing"

	"core/cli/tui"
	"core/cli/tui/ongoing"
	"core/shared/clientui"
)

func TestOngoingSurfaceTransitionQueuesTranscriptWhileDetailActive(t *testing.T) {
	surface := &ongoingSurfaceSpy{}
	controller := newOngoingTranscriptController(surface, ongoingTestFrameProvider)
	if _, err := controller.Accept(ongoingHydrationMessage(1)); err != nil {
		t.Fatalf("accept hydration: %v", err)
	}
	surface.calls = nil
	m := newProjectedStaticUIModel(WithUIOngoingTranscriptController(controller))

	if cmd := m.activateSurface(uiSurfaceTranscriptDetail); cmd == nil {
		t.Fatal("expected detail activation command")
	}
	if _, err := controller.Accept(ongoingTranscriptMessage(2, clientui.TranscriptMessageRuntimeActivity)); err != nil {
		t.Fatalf("accept detail queued message: %v", err)
	}
	if len(surface.calls) != 0 {
		t.Fatalf("surface calls while detail active = %v, want none", surface.calls)
	}

	if cmd := m.activateSurface(uiSurfaceOngoingTranscript); cmd == nil {
		t.Fatal("expected ongoing restore command")
	}
	if len(surface.calls) != 0 {
		t.Fatalf("surface calls before alt-screen exit completes = %v, want none", surface.calls)
	}

	if _, cmd := m.Update(ongoingNormalBufferOwnedMsg{owned: true}); cmd != nil {
		t.Fatalf("post-exit ownership update returned command, want nil")
	}

	if got, want := surface.callKinds(), []string{"render"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("surface calls after ongoing restore = %v, want %v", got, want)
	}
}

func TestTranscriptModeTransitionMarksOngoingUnowned(t *testing.T) {
	surface := &ongoingSurfaceSpy{}
	controller := newOngoingTranscriptController(surface, ongoingTestFrameProvider)
	if _, err := controller.Accept(ongoingHydrationMessage(1)); err != nil {
		t.Fatalf("accept hydration: %v", err)
	}
	surface.calls = nil
	m := newProjectedStaticUIModel(WithUIOngoingTranscriptController(controller))

	cmd := m.transitionTranscriptModeWithOptions(transcriptModeTransitionOptions{target: tui.ModeDetail})
	if cmd == nil {
		t.Fatal("transition to detail did not return alt-screen command")
	}
	if _, err := controller.Accept(ongoingTranscriptMessage(2, clientui.TranscriptMessageRuntimeActivity)); err != nil {
		t.Fatalf("accept detail queued message: %v", err)
	}

	if len(surface.calls) != 0 {
		t.Fatalf("surface calls while detail owns terminal = %v, want none", surface.calls)
	}
}

func TestTranscriptModeReturnDrainsOngoingOnlyAfterPostExitMessage(t *testing.T) {
	surface := &ongoingSurfaceSpy{}
	controller := newOngoingTranscriptController(surface, ongoingTestFrameProvider)
	if _, err := controller.Accept(ongoingHydrationMessage(1)); err != nil {
		t.Fatalf("accept hydration: %v", err)
	}
	m := newProjectedStaticUIModel(WithUIOngoingTranscriptController(controller))
	if cmd := m.transitionTranscriptModeWithOptions(transcriptModeTransitionOptions{target: tui.ModeDetail}); cmd == nil {
		t.Fatal("transition to detail did not return alt-screen command")
	}
	if _, err := controller.Accept(ongoingTranscriptMessage(2, clientui.TranscriptMessageRuntimeActivity)); err != nil {
		t.Fatalf("accept queued message: %v", err)
	}
	surface.calls = nil

	if cmd := m.transitionTranscriptModeWithOptions(transcriptModeTransitionOptions{target: tui.ModeOngoing}); cmd == nil {
		t.Fatal("transition to ongoing did not return exit-alt-screen command")
	}
	if len(surface.calls) != 0 {
		t.Fatalf("surface calls before post-exit message = %v, want none", surface.calls)
	}

	if _, cmd := m.Update(ongoingNormalBufferOwnedMsg{owned: true}); cmd != nil {
		t.Fatalf("post-exit ownership update returned command, want nil")
	}
	if got, want := surface.callKinds(), []string{"render"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("surface calls after post-exit message = %v, want %v", got, want)
	}
}

func TestScratchResetWhileDetailActiveDefersRawSurfaceWriteUntilOngoingOwnsTerminal(t *testing.T) {
	var raw bytes.Buffer
	nativeSurface := ongoing.NewSurface(&raw)
	if _, err := nativeSurface.Render(ongoingTestFrameProvider()); err != nil {
		t.Fatalf("prime native surface: %v", err)
	}
	raw.Reset()
	controller := newOngoingTranscriptController(&ongoingSurfaceSpy{}, ongoingTestFrameProvider)
	if _, err := controller.Accept(ongoingHydrationMessage(1)); err != nil {
		t.Fatalf("accept hydration: %v", err)
	}
	reopenCount := 0
	m := newProjectedStaticUIModel(
		WithUIOngoingSurface(nativeSurface),
		WithUIOngoingTranscriptController(controller),
		WithUIOngoingTranscriptReopen(func() { reopenCount++ }),
	)

	if cmd := m.activateSurface(uiSurfaceTranscriptDetail); cmd == nil {
		t.Fatal("expected detail activation command")
	}
	if cmd := m.handleOngoingResult(ongoing.Result{Action: ongoing.ResultRequestScratchRehydration, Reason: ongoing.RehydrateReasonSequenceGap}); cmd != nil {
		t.Fatalf("scratch request while detail active returned command, want nil")
	}

	if raw.Len() != 0 {
		t.Fatalf("raw ongoing bytes written while detail active: %q", raw.String())
	}
	if reopenCount != 1 {
		t.Fatalf("reopen count = %d, want 1", reopenCount)
	}

	if cmd := m.activateSurface(uiSurfaceOngoingTranscript); cmd == nil {
		t.Fatal("expected ongoing activation command")
	}
	if raw.Len() != 0 {
		t.Fatalf("raw ongoing bytes written before ownership restore: %q", raw.String())
	}
	if _, cmd := m.Update(ongoingNormalBufferOwnedMsg{owned: true}); cmd != nil {
		t.Fatalf("post-exit ownership update returned command, want nil")
	}
	if raw.Len() == 0 {
		t.Fatal("expected deferred raw scratch reset after ongoing ownership restore")
	}
}
