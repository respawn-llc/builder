package app

import (
	"bytes"
	"strings"
	"testing"

	"core/cli/tui/ongoing"
	"core/shared/clientui"
)

func TestNativeOngoingViewSuppressesBubbleTeaNormalBufferFrame(t *testing.T) {
	gate := newUIRendererOutputGateState()
	m := sizedTestUIModel(newProjectedStaticUIModel(
		WithUIRendererOutputGateState(gate),
		WithUIOngoingSurface(ongoing.NewSurface(&bytes.Buffer{})),
	), 40, 8)

	if got := m.View(); got != "" {
		t.Fatalf("ongoing View() = %q, want empty renderer frame", got)
	}

	var rendererOut bytes.Buffer
	writer := newUIRendererOutputGateWriter(&rendererOut, gate)
	if _, err := writer.Write([]byte("renderer frame")); err != nil {
		t.Fatalf("write renderer frame: %v", err)
	}
	if got := rendererOut.String(); got != "" {
		t.Fatalf("suppressed renderer frame leaked: %q", got)
	}
}

func TestNativeOngoingSurfaceWritesBypassRendererGate(t *testing.T) {
	gate := newUIRendererOutputGateState()
	gate.SetSuppressRendererWrites(true)
	var out bytes.Buffer
	surface := ongoing.NewSurface(&out)

	if _, err := surface.Render(ongoing.FrameInput{
		Size:     ongoing.Size{Width: 20, Height: 3},
		Sections: []ongoing.FrameSection{{Kind: ongoing.FrameSectionStatus, Lines: []string{"ready"}}},
	}); err != nil {
		t.Fatalf("render ongoing surface: %v", err)
	}

	if got, want := out.String(), "\x1b[r\x1b[?6l\x1b[3;1H\x1b[2K\x1b[3;1Hready\x1b[?25l"; got != want {
		t.Fatalf("ongoing surface bytes = %q, want %q", got, want)
	}
}

func TestOngoingTranscriptEventReachesNativeSurface(t *testing.T) {
	var out bytes.Buffer
	surface := ongoing.NewSurface(&out)
	m := sizedTestUIModel(newProjectedStaticUIModel(
		WithUIOngoingSurface(surface),
	), 40, 8)
	m.ongoingTranscript = newOngoingTranscriptController(surface, m.ongoingFrameInput)

	_, cmd := m.Update(ongoingTranscriptEvent{
		Kind: ongoingTranscriptEventMessage,
		Message: clientui.TranscriptMessage{
			Sequence: 1,
			Kind:     clientui.TranscriptMessageHydration,
			Hydration: &clientui.TranscriptHydration{CommittedRows: []clientui.TranscriptCommittedRow{{
				Kind: clientui.TranscriptRowUser,
				User: &clientui.TranscriptUserRow{Text: "hydrated row"},
			}}},
		},
	})

	if cmd != nil {
		_ = cmd()
	}
	if got := out.String(); !strings.Contains(got, "hydrated row") {
		t.Fatalf("native surface output = %q, want hydrated row", got)
	}
}

func TestNativeOngoingViewClearsLegacyAppCursorPlacement(t *testing.T) {
	cursor := newUITerminalCursorState()
	cursor.Set(uiTerminalCursorPlacement{Visible: true, CursorRow: 1, CursorCol: 1, AnchorRow: 2})
	m := sizedTestUIModel(newProjectedStaticUIModel(
		WithUITerminalCursorState(cursor),
		WithUIOngoingSurface(ongoing.NewSurface(&bytes.Buffer{})),
	), 40, 8)

	_ = m.View()

	if _, ok := cursor.Snapshot(); ok {
		t.Fatal("legacy app cursor placement stayed active for native ongoing surface")
	}
}

func TestScratchRehydrationResultRequestsTranscriptReopen(t *testing.T) {
	var out bytes.Buffer
	surface := ongoing.NewSurface(&out)
	controller := newOngoingTranscriptController(surface, ongoingTestFrameProvider)
	if _, err := controller.Accept(ongoingHydrationMessage(1)); err != nil {
		t.Fatalf("accept hydration: %v", err)
	}
	reopened := false
	m := sizedTestUIModel(newProjectedStaticUIModel(
		WithUIOngoingSurface(surface),
		WithUIOngoingTranscriptController(controller),
		WithUIOngoingTranscriptReopen(func() { reopened = true }),
	), 40, 8)

	cmd := m.handleOngoingResult(ongoing.Result{
		Action: ongoing.ResultRequestScratchRehydration,
		Reason: ongoing.RehydrateReasonSequenceGap,
	})

	if cmd != nil {
		t.Fatalf("scratch rehydration command = %v, want synchronous handling", cmd)
	}
	if !reopened {
		t.Fatal("scratch rehydration did not request transcript subscription reopen")
	}
	if result, err := controller.Accept(ongoingHydrationMessage(1)); err != nil || result.Action != ongoing.ResultNoop {
		t.Fatalf("post-reopen hydration result=%+v err=%v, want accepted hydration", result, err)
	}
}
