package app

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
	"time"

	"core/cli/tui/ongoing"
	"core/internal/testharness/pty"
	"core/internal/testharness/pty/analyzer"
	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/transcript"

	tea "github.com/charmbracelet/bubbletea"
	xansi "github.com/charmbracelet/x/ansi"
)

func withUIOngoingTranscriptController(controller *ongoingTranscriptController) UIOption {
	return func(m *uiModelConstruction) {
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
	m.ongoingTranscript = newNoopOngoingTranscriptController(spySurface, m.ongoingFrameInput)

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

func TestNativeOngoingFirstWindowSizeSuppressesDelayedRendererClearAfterHydration(t *testing.T) {
	var out bytes.Buffer
	gate := newUIRendererOutputGateState()
	surface := ongoing.NewSurface(&out)
	m := newProjectedStaticUIModel(
		WithUIRendererOutputGateState(gate),
		WithUIOngoingSurface(surface),
	)
	m.ongoingTranscript = newNoopOngoingTranscriptController(surface, m.ongoingFrameInput)

	_ = m.Init()
	hydration := ongoingHydrationMessage(1)
	hydrationPayload := hydration.Payload().(clientui.TranscriptHydration)
	hydrationPayload.TailSegment.Entries = []clientui.TranscriptCommittedRow{
		ongoingTranscriptMessage(2, clientui.TranscriptMessageCommittedRow).Payload().(clientui.TranscriptCommittedRow),
	}
	hydration = clientui.NewTranscriptMessage(1, clientui.NewTranscriptEvent(hydrationPayload))
	_, _ = m.Update(ongoingTranscriptEvent{
		Kind:    ongoingTranscriptEventMessage,
		Message: hydration,
	})
	beforeHydration := append([]byte(nil), out.Bytes()...)
	_, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	afterHydration := append([]byte(nil), out.Bytes()...)
	if bytes.Equal(afterHydration, beforeHydration) {
		t.Fatal("first window size did not emit queued hydration")
	}

	renderer := newUIRendererOutputGateWriter(&out, gate)
	if _, err := renderer.Write([]byte(xansi.EraseEntireScreen)); err != nil {
		t.Fatalf("write delayed renderer clear: %v", err)
	}
	if got := out.Bytes(); !bytes.Equal(got, afterHydration) {
		t.Fatalf("delayed renderer clear changed hydrated normal buffer: before=%q after=%q", afterHydration, got)
	}
}

func TestOngoingTranscriptEventReachesNativeSurface(t *testing.T) {
	var out bytes.Buffer
	surface := ongoing.NewSurface(&out)
	m := sizedTestUIModel(newProjectedStaticUIModel(
		WithUIOngoingSurface(surface),
	), 40, 10)
	m.ongoingTranscript = newNoopOngoingTranscriptController(surface, m.ongoingFrameInput)

	hydration := ongoingHydrationMessage(1)
	hydrationPayload := hydration.Payload().(clientui.TranscriptHydration)
	hydrationPayload.TailSegment.Entries = []clientui.TranscriptCommittedRow{
		ongoingTranscriptMessage(2, clientui.TranscriptMessageCommittedRow).Payload().(clientui.TranscriptCommittedRow),
	}
	hydrationPayload.TailSegment.Entries[0].User.Text = "hydrated row"
	hydration = clientui.NewTranscriptMessage(1, clientui.NewTranscriptEvent(hydrationPayload))
	_, cmd := m.Update(ongoingTranscriptEvent{
		Kind:    ongoingTranscriptEventMessage,
		Message: hydration,
	})

	if cmd != nil {
		_ = cmd()
	}
	if got := out.String(); !strings.Contains(got, "hydrated row") {
		t.Fatalf("native surface output = %q, want hydrated row", got)
	}
}

func TestOngoingTranscriptHydrationRestoresAssistantCommentary(t *testing.T) {
	var out bytes.Buffer
	surface := ongoing.NewSurface(&out)
	m := sizedTestUIModel(newProjectedStaticUIModel(
		WithUIOngoingSurface(surface),
	), 40, 10)
	m.ongoingTranscript = newNoopOngoingTranscriptController(surface, m.ongoingFrameInput)

	hydration := ongoingHydrationMessage(1)
	payload := hydration.Payload().(clientui.TranscriptHydration)
	payload.TailSegment.Entries = []clientui.TranscriptCommittedRow{{
		Visibility: transcript.EntryVisibilityOngoing,
		Integrity:  transcript.RowIntegrityValid,
		Kind:       clientui.TranscriptRowAssistant,
		Locator:    transcript.CommittedRowLocator{EventSequence: 1, RowOrdinal: 1},
		Assistant: &clientui.TranscriptAssistantRow{
			StepID: ongoingTestStepID(),
			Text:   "restored assistant commentary",
			Phase:  transcript.AssistantPhaseCommentary,
		},
	}}
	hydration = clientui.NewTranscriptMessage(1, clientui.NewTranscriptEvent(payload))
	_, cmd := m.Update(ongoingTranscriptEvent{
		Kind:    ongoingTranscriptEventMessage,
		Message: hydration,
	})

	if cmd != nil {
		_ = cmd()
	}
	if got := out.String(); !strings.Contains(got, "restored assistant commentary") {
		t.Fatalf("native surface output = %q, want restored commentary", got)
	}
}

func TestOngoingTranscriptDeliveryKeepsCursorAbsentForAskOptionPicker(t *testing.T) {
	var out bytes.Buffer
	surface := ongoing.NewSurface(&out)
	m := sizedTestUIModel(newProjectedStaticUIModel(
		WithUIOngoingSurface(surface),
		WithUITerminalCursorState(newUITerminalCursorState()),
	), 77, 34)
	testSetMainInput(m, strings.Repeat("x", 91))
	m.ongoingTranscript = newNoopOngoingTranscriptController(surface, m.ongoingFrameInput)

	if _, _, err := m.ongoingTranscript.Accept(ongoingHydrationMessage(1)); err != nil {
		t.Fatalf("accept hydration: %v", err)
	}
	next, _ := m.Update(askEventMsg{event: testQuestionAskEvent(
		"ask-1",
		"Choose an option",
		"first",
		"second",
	)})
	m = next.(*uiModel)

	if got := m.surface(); got != uiSurfaceOngoingTranscript {
		t.Fatalf("surface = %q, want ongoing transcript", got)
	}
	if !m.ongoingTranscript.normalOwned {
		t.Fatal("ongoing transcript controller lost normal-buffer ownership")
	}
	if got := m.inputMode(); got != uiInputModeAsk {
		t.Fatalf("input mode = %q, want ask", got)
	}
	if got := m.layout().inputPaneProjection(77, 0, uiThemeStyles(m.theme)).Cursor; got.Visible {
		t.Fatalf("ask option picker cursor = %+v, want absent", got)
	}

	if _, _, err := m.ongoingTranscript.Accept(clientui.NewTranscriptMessage(2, clientui.NewTranscriptEvent(clientui.TranscriptAssistantDelta{
		StepID:   ongoingTestStepID(),
		StreamID: runtimeids.NewAssistantStreamID(),
		Delta:    "working",
		Phase:    transcript.AssistantPhaseCommentary,
	})),
	); err != nil {
		t.Fatalf("accept assistant delta: %v", err)
	}

	if got := m.ongoingFrameInput().Cursor; !reflect.DeepEqual(got, ongoing.Cursor{}) {
		t.Fatalf("ask option picker frame cursor = %+v, want absent zero value", got)
	}
}

func TestNativeOngoingTransientPickersAndAskDoNotCreateBlankScrollback(t *testing.T) {
	tests := []struct {
		name  string
		open  func(*uiModel)
		close func(*uiModel)
	}{
		{
			name: "slash picker",
			open: func(m *uiModel) {
				slash := newSlashPickerScrollTestModel()
				m.commandRegistry = slash.commandRegistry
				testSetMainInput(m, "/")
				m.refreshSlashCommandFilterFromInputWithAuth(true)
			},
			close: func(m *uiModel) {
				testSetMainInput(m, "")
				m.refreshSlashCommandFilterFromInputWithAuth(true)
			},
		},
		{
			name: "file picker",
			open: func(m *uiModel) {
				testSetMainInput(m, "@ab")
				m.pathReference.tracked = detectPathReferenceQuery("@ab", 3)
				m.pathReference.matches = []uiPathReferenceCandidate{
					{Path: "match-00.go"}, {Path: "match-01.go"}, {Path: "match-02.go"},
					{Path: "match-03.go"}, {Path: "match-04.go"}, {Path: "match-05.go"},
					{Path: "match-06.go"}, {Path: "match-07.go"}, {Path: "match-08.go"},
				}
			},
			close: func(m *uiModel) { m.clearPathReferenceState() },
		},
		{
			name: "active ask",
			open: func(m *uiModel) {
				event := testQuestionAskEvent("ask-1", "Question source", "First", "Second")
				testSetActiveAsk(m, &event)
				m.ask.activeProjection.rows = []string{"question one", "question two", "question three"}
			},
			close: func(m *uiModel) {
				m.askController().resolvePrompt("ask-1")
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			const (
				width  = 40
				height = 12
			)
			var out bytes.Buffer
			surface := ongoing.NewSurface(&out)
			m := sizedTestUIModel(newProjectedStaticUIModel(
				WithUIOngoingSurface(surface),
				WithUITerminalCursorState(newUITerminalCursorState()),
			), width, height)
			baseFrame := m.ongoingFrameInput()
			if _, err := surface.ApplyTerminalMessage(
				committedMessageForOngoingResizeTest(),
				baseFrame,
			); err != nil {
				t.Fatalf("prime immutable output: %v", err)
			}
			test.open(m)
			openFrame := m.ongoingFrameInput()
			picker, pickerVisible := frameSection(openFrame, ongoing.FrameSectionPicker)
			if test.name != "active ask" && (!pickerVisible || len(picker.Lines) != slashCommandPickerLines) {
				t.Fatalf("picker frame height = %d visible=%t, want %d", len(picker.Lines), pickerVisible, slashCommandPickerLines)
			}
			if openFrame.Cursor.Visible &&
				(openFrame.Cursor.Row < 1 || openFrame.Cursor.Row > height ||
					openFrame.Cursor.Column < 1 || openFrame.Cursor.Column > width) {
				t.Fatalf("open cursor outside terminal: %+v", openFrame.Cursor)
			}
			if _, err := surface.Render(openFrame); err != nil {
				t.Fatalf("render transient frame: %v", err)
			}
			test.close(m)
			closedFrame := m.ongoingFrameInput()
			if _, ok := frameSection(closedFrame, ongoing.FrameSectionPicker); ok {
				t.Fatal("closed transient lifecycle retained a picker frame section")
			}
			if _, err := surface.Render(closedFrame); err != nil {
				t.Fatalf("close transient frame: %v", err)
			}

			capture, err := pty.NewCapture(
				pty.MustDimensions(height, width),
				[]pty.Chunk{pty.NewChunk(0, time.Millisecond, out.Bytes())},
			)
			if err != nil {
				t.Fatalf("create lifecycle capture: %v", err)
			}
			analysis, err := analyzer.Analyze(capture)
			if err != nil {
				t.Fatalf("analyze lifecycle: %v", err)
			}
			rows := strings.Split(analysis.Screen.RenderText(), "\n")
			if row := appScreenRowIndex(rows, "❯ committed"); row < 0 {
				t.Fatalf("committed output missing after lifecycle: %q", rows)
			}
		})
	}
}

func appScreenRowIndex(rows []string, want string) int {
	for index, row := range rows {
		if strings.TrimSpace(row) == want {
			return index
		}
	}
	return -1
}

func TestNativeOngoingRepaintKeepsControllerLiveFrameSections(t *testing.T) {
	var out bytes.Buffer
	nativeSurface := ongoing.NewSurface(&out)
	spySurface := &ongoingSurfaceSpy{}
	m := sizedTestUIModel(newProjectedStaticUIModel(
		WithUIOngoingSurface(nativeSurface),
	), 40, 10)
	m.ongoingTranscript = newNoopOngoingTranscriptController(spySurface, m.ongoingFrameInput)
	if _, _, err := m.ongoingTranscript.Accept(ongoingHydrationMessage(1)); err != nil {
		t.Fatalf("accept hydration: %v", err)
	}
	if _, _, err := m.ongoingTranscript.Accept(ongoingTranscriptMessage(2, clientui.TranscriptMessagePrompt)); err != nil {
		t.Fatalf("accept pending prompt: %v", err)
	}
	spySurface.calls = nil

	testSetMainInput(m, "typing while prompt is pending")
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
	m.ongoingTranscript = newNoopOngoingTranscriptController(spySurface, m.ongoingFrameInput)
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
	if testMainInput(updated) != "/tmp/kent-clipboard.png" {
		t.Fatalf("input = %q, want pasted clipboard image path", testMainInput(updated))
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
	m.ongoingTranscript = newNoopOngoingTranscriptController(spySurface, m.ongoingFrameInput)

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
	m.ongoingTranscript = newNoopOngoingTranscriptController(spySurface, m.ongoingFrameInput)

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
	controller := newNoopOngoingTranscriptController(surface, ongoingTestFrameProvider)
	if _, _, err := controller.Accept(ongoingHydrationMessage(1)); err != nil {
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
	if result, _, err := controller.Accept(ongoingHydrationMessage(1)); err != nil || result.Action != ongoing.ResultNoop {
		t.Fatalf("post-reopen hydration result=%+v err=%v, want accepted hydration", result, err)
	}
}

func TestAwaitingPromptWithoutPromptRequestsTranscriptRehydration(t *testing.T) {
	var out bytes.Buffer
	surface := ongoing.NewSurface(&out)
	controller := newNoopOngoingTranscriptController(surface, ongoingTestFrameProvider)
	reopened := 0
	m := sizedTestUIModel(newProjectedStaticUIModel(
		WithUIOngoingSurface(surface),
		withUIOngoingTranscriptController(controller),
		WithUIOngoingTranscriptReopen(func() { reopened++ }),
	), 40, 10)
	activity := runtimeTupleTestRunningActivity()
	activity.State = clientui.RuntimeActivityAwaitingPrompt
	command := m.applyAdmittedTranscriptMessageState(
		runtimeTupleTestUpdateMessage(2, 2, activity),
		runtimeTupleMergeResult{
			decision: runtimeTupleApply,
			project:  true,
			view:     runtimeTupleTestView(2, activity),
		},
	)
	if command == nil {
		t.Fatal("awaiting-prompt activity without a prompt did not schedule recovery")
	}
	for _, message := range collectCmdMessages(t, command) {
		next, _ := m.Update(message)
		m = next.(*uiModel)
	}
	if reopened != 1 {
		t.Fatalf("transcript reopen requests = %d, want one", reopened)
	}
}
