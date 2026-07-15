package app

import (
	"bytes"
	"reflect"
	"strings"
	"testing"

	"core/cli/tui/ongoing"
	"core/shared/clientui"

	tea "github.com/charmbracelet/bubbletea"
)

func withUIOngoingTranscriptController(controller *ongoingTranscriptController) UIOption {
	return func(m *uiModel) {
		m.ongoingTranscript = controller
		m.terminalGeometry = terminalGeometryKnown(80, 24)
	}
}

func TestNativeOngoingViewSuppressesBubbleTeaNormalBufferFrame(t *testing.T) {
	gate := newUIRendererOutputGateState()
	m := sizedTestUIModel(newProjectedStaticUIModel(
		WithUIRendererOutputGateState(gate),
		WithUIOngoingSurface(ongoing.NewSurface(&bytes.Buffer{})),
	), 40, 10)

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

	if got, want := out.String(), "\x1b[r\x1b[?6l\x1b]133;C\x1b\\\x1b[3;1H\x1b]133;C\x1b\\\x1b[2K\x1b[3;1H\x1b]133;A;redraw=1\x1b\\ready\x1b[?25l"; got != want {
		t.Fatalf("ongoing surface bytes = %q, want %q", got, want)
	}
}

func TestNativeOngoingInitDefersRenderUntilWindowSizeKnown(t *testing.T) {
	var out bytes.Buffer
	m := newProjectedStaticUIModel(WithUIOngoingSurface(ongoing.NewSurface(&out)))

	_ = m.Init()

	if out.Len() != 0 {
		t.Fatalf("native ongoing init wrote before real window size: %q", out.String())
	}

	m.Update(tea.WindowSizeMsg{Width: 40, Height: 10})

	if out.Len() == 0 {
		t.Fatal("native ongoing surface did not render after first window size")
	}
}

func TestNativeOngoingHydrationWaitsForWindowSize(t *testing.T) {
	var out bytes.Buffer
	nativeSurface := ongoing.NewSurface(&out)
	spySurface := &ongoingSurfaceSpy{}
	m := newProjectedStaticUIModel(WithUIOngoingSurface(nativeSurface))
	m.ongoingTranscript = newOngoingTranscriptController(spySurface, m.ongoingFrameInput)

	_ = m.Init()
	_, _ = m.Update(ongoingTranscriptEvent{
		Kind:    ongoingTranscriptEventMessage,
		Message: ongoingHydrationMessage(1),
	})
	if len(spySurface.calls) != 0 {
		t.Fatalf("surface calls before window size = %v, want none", spySurface.callKinds())
	}

	_, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	if got, want := spySurface.callKinds(), []string{"resize", "apply"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("surface calls after window size = %v, want %v", got, want)
	}
	if got := spySurface.calls[1].frame.Size.Width; got != 80 {
		t.Fatalf("hydration frame width = %d, want 80", got)
	}
}

func TestOngoingTranscriptEventReachesNativeSurface(t *testing.T) {
	var out bytes.Buffer
	surface := ongoing.NewSurface(&out)
	m := sizedTestUIModel(newProjectedStaticUIModel(
		WithUIOngoingSurface(surface),
	), 40, 10)
	m.ongoingTranscript = newOngoingTranscriptController(surface, m.ongoingFrameInput)

	_, cmd := m.Update(ongoingTranscriptEvent{
		Kind: ongoingTranscriptEventMessage,
		Message: clientui.TranscriptMessage{
			Sequence: 1,
			Kind:     clientui.TranscriptMessageHydration,
			Hydration: &clientui.TranscriptHydration{CommittedRows: []clientui.TranscriptCommittedRow{{
				Visibility: clientui.EntryVisibilityOngoing,
				Kind:       clientui.TranscriptRowUser,
				User:       &clientui.TranscriptUserRow{Text: "hydrated row"},
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

func TestNativeOngoingRepaintKeepsControllerLiveFrameSections(t *testing.T) {
	var out bytes.Buffer
	nativeSurface := ongoing.NewSurface(&out)
	spySurface := &ongoingSurfaceSpy{}
	m := sizedTestUIModel(newProjectedStaticUIModel(
		WithUIOngoingSurface(nativeSurface),
	), 40, 10)
	m.ongoingTranscript = newOngoingTranscriptController(spySurface, m.ongoingFrameInput)
	if _, err := m.ongoingTranscript.Accept(ongoingHydrationMessage(1)); err != nil {
		t.Fatalf("accept hydration: %v", err)
	}
	if _, err := m.ongoingTranscript.Accept(clientui.TranscriptMessage{
		Sequence: 2,
		Kind:     clientui.TranscriptMessagePendingSessionPrompt,
		PendingSessionPrompt: &clientui.TranscriptPendingSessionPrompt{
			ID:    "ask-1",
			State: clientui.TranscriptPromptPending,
			Data:  clientui.TranscriptPendingSessionPromptData{Question: "Approve command?"},
		},
	}); err != nil {
		t.Fatalf("accept pending prompt: %v", err)
	}
	spySurface.calls = nil

	m.input = "typing while prompt is pending"
	_ = m.renderNativeOngoingSurface()

	if got, want := spySurface.callKinds(), []string{"render"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("surface calls = %v, want %v", got, want)
	}
	if got, want := spySurface.lastFrameSectionLines(ongoing.FrameSectionPendingPrompt), []string{"Approve command?"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("prompt section lines = %v, want %v", got, want)
	}
}

func TestNativeOngoingClipboardPasteRepaintsInput(t *testing.T) {
	var out bytes.Buffer
	nativeSurface := ongoing.NewSurface(&out)
	spySurface := &ongoingSurfaceSpy{}
	m := sizedTestUIModel(newProjectedStaticUIModel(
		WithUIOngoingSurface(nativeSurface),
	), 40, 10)
	m.ongoingTranscript = newOngoingTranscriptController(spySurface, m.ongoingFrameInput)
	m.mainInputDraftToken = 3

	next, _ := m.Update(clipboardPasteDoneMsg{
		Target:         uiClipboardPasteTargetMain,
		MainDraftToken: 3,
		Content:        retainedClipboardImage("/tmp/kent-clipboard.png"),
	})
	updated := next.(*uiModel)

	if got, want := spySurface.callKinds(), []string{"render"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("surface calls = %v, want %v", got, want)
	}
	if updated.input != "/tmp/kent-clipboard.png" {
		t.Fatalf("input = %q, want pasted clipboard image path", updated.input)
	}
	section, ok := frameSection(updated.ongoingFrameInput(), ongoing.FrameSectionInput)
	if !ok {
		t.Fatal("input section missing from updated ongoing frame")
	}
	if got, want := spySurface.lastFrameSectionLines(ongoing.FrameSectionInput), section.Lines; !reflect.DeepEqual(got, want) {
		t.Fatalf("rendered input section lines = %v, want %v", got, want)
	}
}

func TestNativeOngoingClipboardPasteErrorRepaintsStatus(t *testing.T) {
	var out bytes.Buffer
	nativeSurface := ongoing.NewSurface(&out)
	spySurface := &ongoingSurfaceSpy{}
	m := sizedTestUIModel(newProjectedStaticUIModel(
		WithUIOngoingSurface(nativeSurface),
	), 40, 10)
	m.ongoingTranscript = newOngoingTranscriptController(spySurface, m.ongoingFrameInput)

	next, _ := m.Update(clipboardPasteDoneMsg{
		Target: uiClipboardPasteTargetMain,
		Err:    &uiClipboardPasteError{Kind: uiClipboardPasteErrorNoContent, Message: "Clipboard does not contain supported content"},
	})
	updated := next.(*uiModel)

	if got, want := spySurface.callKinds(), []string{"render"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("surface calls = %v, want %v", got, want)
	}
	section, ok := frameSection(updated.ongoingFrameInput(), ongoing.FrameSectionStatus)
	if !ok {
		t.Fatal("status section missing from updated ongoing frame")
	}
	if got, want := spySurface.lastFrameSectionLines(ongoing.FrameSectionStatus), section.Lines; !reflect.DeepEqual(got, want) {
		t.Fatalf("rendered status section lines = %v, want %v", got, want)
	}
}

func TestNativeOngoingReconnectWarningRepaintsAndClearsStatus(t *testing.T) {
	disableTransientStatusClearForTest(t)
	var out bytes.Buffer
	nativeSurface := ongoing.NewSurface(&out)
	spySurface := &ongoingSurfaceSpy{}
	m := sizedTestUIModel(newProjectedStaticUIModel(
		WithUIOngoingSurface(nativeSurface),
	), 40, 10)
	m.ongoingTranscript = newOngoingTranscriptController(spySurface, m.ongoingFrameInput)

	next, _ := m.Update(runtimeReconnectWarningMsg{text: "connection interrupted"})
	updated := next.(*uiModel)
	if got, want := spySurface.callKinds(), []string{"render"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("surface calls after reconnect warning = %v, want %v", got, want)
	}
	statusSection, ok := frameSection(updated.ongoingFrameInput(), ongoing.FrameSectionStatus)
	if !ok {
		t.Fatal("warning status section missing")
	}
	if got, want := spySurface.lastFrameSectionLines(ongoing.FrameSectionStatus), statusSection.Lines; !reflect.DeepEqual(got, want) {
		t.Fatalf("rendered warning status = %v, want %v", got, want)
	}

	_, _ = updated.Update(clearTransientStatusMsg{token: updated.transientStatusToken})
	if got, want := spySurface.callKinds(), []string{"render", "render"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("surface calls after warning clear = %v, want %v", got, want)
	}
	if updated.transientStatus != "" {
		t.Fatalf("transient status after clear = %q, want empty", updated.transientStatus)
	}
}

func TestNativeOngoingViewClearsLegacyAppCursorPlacement(t *testing.T) {
	cursor := newUITerminalCursorState()
	cursor.Set(uiTerminalCursorPlacement{Visible: true, CursorRow: 1, CursorCol: 1, AnchorRow: 2})
	m := sizedTestUIModel(newProjectedStaticUIModel(
		WithUITerminalCursorState(cursor),
		WithUIOngoingSurface(ongoing.NewSurface(&bytes.Buffer{})),
	), 40, 10)

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
		withUIOngoingTranscriptController(controller),
		WithUIOngoingTranscriptReopen(func() { reopened = true }),
	), 40, 10)

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
