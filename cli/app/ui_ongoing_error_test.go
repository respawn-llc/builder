package app

import (
	"errors"
	"testing"

	"core/cli/tui/ongoing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestOngoingSurfaceErrorExitsTUIInRelease(t *testing.T) {
	m := newProjectedStaticUIModel()

	cmd := m.handleOngoingSurfaceError(errors.New("terminal write failed"))

	if cmd == nil {
		t.Fatal("ongoing fatal error handler did not return quit command")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("ongoing fatal error handler command did not return quit message")
	}
	if !m.Transition().Exit {
		t.Fatal("ongoing fatal error did not request clear TUI exit")
	}
}

func TestOngoingSurfaceErrorPanicsInDebug(t *testing.T) {
	m := newProjectedStaticUIModel(WithUIDebug(true))

	defer func() {
		if recover() == nil {
			t.Fatal("expected debug panic")
		}
	}()

	_ = m.handleOngoingSurfaceError(errors.New("terminal write failed"))
}

func TestOngoingTranscriptOpenFailureExitsTUI(t *testing.T) {
	controller := newNoopOngoingTranscriptController(&ongoingSurfaceSpy{}, ongoingTestFrameProvider)
	m := newProjectedStaticUIModel(withUIOngoingTranscriptController(controller))

	cmd := m.handleOngoingTranscriptEvent(ongoingTranscriptEvent{
		Kind: ongoingTranscriptEventFailure,
		Err:  errors.New("canonical hydration is invalid"),
	})

	if cmd == nil {
		t.Fatal("transcript-open failure did not return quit command")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("transcript-open failure command did not return Bubble Tea quit")
	}
	if !m.Transition().Exit {
		t.Fatal("transcript-open failure did not request clear TUI exit")
	}
}

func TestPendingScratchResetFailureExitsWithoutResettingOrReopeningTranscript(t *testing.T) {
	nativeSurface := ongoing.NewSurface(&failingTerminalCursorWriter{failAfter: 0})
	controllerSurface := &ongoingSurfaceSpy{}
	controller := newNoopOngoingTranscriptController(controllerSurface, ongoingTestFrameProvider)
	reopenCount := 0
	m := sizedTestUIModel(newProjectedStaticUIModel(
		WithUIOngoingSurface(nativeSurface),
		withUIOngoingTranscriptController(controller),
		WithUIOngoingTranscriptReopen(func() { reopenCount++ }),
	), 40, 10)
	reason := ongoing.RehydrateReasonWidthChange
	m.pendingOngoingScratchReset = &reason

	cmd := m.applyPendingOngoingScratchReset()

	if cmd == nil {
		t.Fatal("failed scratch reset did not request fatal exit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("failed scratch reset did not return Bubble Tea quit")
	}
	if m.pendingOngoingScratchReset != nil {
		t.Fatal("failed scratch reset retained a retry target")
	}
	if len(controllerSurface.calls) != 0 {
		t.Fatalf("failed scratch reset continued transcript controller: %+v", controllerSurface.calls)
	}
	if reopenCount != 0 {
		t.Fatalf("failed scratch reset reopened transcript %d times", reopenCount)
	}
}
