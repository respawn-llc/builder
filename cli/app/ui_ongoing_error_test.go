package app

import (
	"errors"
	"net"
	"reflect"
	"strings"
	"testing"

	"core/cli/tui/ongoing"
	"core/shared/clientui"

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

func TestOngoingTranscriptNonTransportOpenFailureExitsTUI(t *testing.T) {
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

func TestOngoingTranscriptTransportOpenFailureKeepsTUIAndShowsDisconnect(t *testing.T) {
	controller := newNoopOngoingTranscriptController(&ongoingSurfaceSpy{}, ongoingTestFrameProvider)
	m := newProjectedTestUIModel(
		&runtimeControlFakeClient{},
		withUIOngoingTranscriptController(controller),
	)
	err := &net.OpError{Err: errors.New("connection refused")}

	cmd := m.handleOngoingTranscriptEvent(ongoingTranscriptEvent{Kind: ongoingTranscriptEventFailure, Err: err})

	if cmd != nil {
		for _, msg := range collectCmdMessages(t, cmd) {
			if _, quit := msg.(tea.QuitMsg); quit {
				t.Fatal("transport failure requested TUI exit")
			}
		}
	}
	if m.Transition().Exit {
		t.Fatal("transport failure requested clear TUI exit")
	}
	if !m.runtimeDisconnectStatusVisible() {
		t.Fatal("transport failure did not show the runtime disconnect status")
	}
	if got := m.runtimeDisconnectStatusText(); got != runtimeDisconnectedStatusMessage {
		t.Fatalf("disconnect status = %q, want %q", got, runtimeDisconnectedStatusMessage)
	}
}

func TestRecoverableTranscriptAdmissionErrorSurfacesAndRehydrates(t *testing.T) {
	t.Setenv("KENT_INVARIANT_MODE", "diagnostic")
	runtimeClient := newTestSessionRuntimeClientWithControls(&reconnectRetryRuntimeControlClient{})
	controller := newOngoingTranscriptController(
		&ongoingSurfaceSpy{},
		ongoingTestFrameProvider,
		runtimeClient.admitTranscriptMessageState,
		func(clientui.TranscriptMessage, runtimeTupleMergeResult) tea.Cmd { return nil },
	)
	if _, _, err := controller.Accept(ongoingHydrationMessage(1)); err != nil {
		t.Fatalf("accept hydration: %v", err)
	}
	reopened := false
	m := newProjectedTestUIModel(
		runtimeClient,
		withUIOngoingTranscriptController(controller),
		WithUIOngoingTranscriptReopen(func() { reopened = true }),
	)

	cmd := m.handleOngoingTranscriptEvent(ongoingTranscriptEvent{
		Kind: ongoingTranscriptEventMessage,
		Message: clientui.NewTranscriptMessage(2, clientui.NewTranscriptEvent(clientui.TranscriptGoalStatus{
			Goal: transcriptGoalFixture("goal-1", "ship feature", clientui.RuntimeGoalStatusActive),
		})),
	})

	if cmd == nil {
		t.Fatal("recoverable admission error did not surface a status command")
	}
	if m.Transition().Exit {
		t.Fatal("recoverable admission error exited the TUI")
	}
	if !reopened {
		t.Fatal("recoverable admission error did not reopen transcript hydration")
	}
	if m.transientStatus == "" || m.transientStatusKind != uiStatusNoticeError {
		t.Fatalf("transient status = %q kind=%q, want surfaced error", m.transientStatus, m.transientStatusKind)
	}
}

func TestRecoveredTranscriptHydrationClearsDisconnectStatusLine(t *testing.T) {
	surface := &ongoingSurfaceSpy{}
	m := newProjectedTestUIModel(
		&runtimeControlFakeClient{},
		WithUIOngoingSurface(ongoing.NewSurface(nil)),
	)
	m.ongoingTranscript = newNoopOngoingTranscriptController(surface, m.ongoingFrameInput)
	m.terminalGeometry = terminalGeometryKnown(80, 24)
	m.setRuntimeDisconnected(true)

	_ = m.handleOngoingTranscriptEvent(ongoingTranscriptEvent{
		Kind:    ongoingTranscriptEventMessage,
		Message: ongoingHydrationMessage(1),
	})

	if m.runtimeDisconnectStatusVisible() {
		t.Fatal("successful transcript hydration did not clear the disconnect state")
	}
	if got, want := surface.callKinds(), []string{"apply", "render"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("recovery surface calls = %v, want %v", got, want)
	}
	for _, section := range surface.calls[1].frame.Sections {
		for _, line := range section.Lines {
			if strings.Contains(line, runtimeDisconnectedStatusMessage) {
				t.Fatalf("recovery repaint retained disconnect status: %q", line)
			}
		}
	}
}

func TestRejectedPostReconnectTranscriptMessageRetainsDisconnectStatus(t *testing.T) {
	controller := newNoopOngoingTranscriptController(&ongoingSurfaceSpy{}, ongoingTestFrameProvider)
	m := newProjectedTestUIModel(
		&runtimeControlFakeClient{},
		withUIOngoingTranscriptController(controller),
	)
	m.setRuntimeDisconnected(true)

	_ = m.handleOngoingTranscriptEvent(ongoingTranscriptEvent{
		Kind:    ongoingTranscriptEventMessage,
		Message: ongoingTranscriptMessage(2, clientui.TranscriptMessageSessionStatus),
	})

	if !m.runtimeDisconnectStatusVisible() {
		t.Fatal("rejected post-reconnect message cleared the disconnect status")
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
