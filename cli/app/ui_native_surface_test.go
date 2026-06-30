package app

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"core/cli/app/commands"
	"core/cli/tui"
	"core/server/runtime"
	"core/shared/clientui"
	"core/shared/transcript"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	xansi "github.com/charmbracelet/x/ansi"
)

func TestNativeOngoingViewWritesLiveAreaAndReturnsEmpty(t *testing.T) {
	var out bytes.Buffer
	m := newNativeSurfaceTestModel(&out)
	m.replaceMainInput("inspect native surface", -1)

	rendered := m.View()
	if rendered != "" {
		t.Fatalf("native ongoing View() returned %q, want empty renderer payload", rendered)
	}
	if m.nativeSurface == nil || !m.nativeSurface.initialized() {
		t.Fatal("expected native surface to be initialized")
	}
	raw := out.String()
	plain := stripANSIAndTrimRight(raw)
	if !strings.Contains(plain, "inspect native surface") {
		t.Fatalf("native live output did not include input field content, got %q", plain)
	}
	if !strings.Contains(raw, xansi.ShowCursor) {
		t.Fatalf("native live output did not include live-area cursor placement, raw=%q", raw)
	}
}

func TestNativeOngoingLiveAreaRendersPendingToolSpinner(t *testing.T) {
	var out bytes.Buffer
	m := newNativeSurfaceTestModel(&out)
	m.spinnerFrame = 2
	seedNativeSurfaceTranscript(m, []tui.TranscriptEntry{
		{Role: tui.TranscriptRoleUser, Text: "run pwd", Committed: true},
		{
			Role:       tui.TranscriptRoleToolCall,
			Text:       "pwd",
			ToolCallID: "call_shell",
			ToolCall: &transcript.ToolCallMeta{
				ToolName: "exec_command",
				IsShell:  true,
				Command:  "pwd",
			},
		},
	})

	if rendered := m.View(); rendered != "" {
		t.Fatalf("native ongoing View() returned %q, want empty renderer payload", rendered)
	}
	plain := stripANSIAndTrimRight(out.String())
	spinner := pendingToolSpinnerFrame(m.spinnerFrame)
	if !strings.Contains(plain, spinner) || !strings.Contains(plain, "pwd") {
		t.Fatalf("native live area did not render pending tool spinner %q with command, got %q", spinner, plain)
	}
	if strings.Contains(plain, "$ pwd") {
		t.Fatalf("native live area rendered static shell symbol instead of pending spinner, got %q", plain)
	}
}

func TestNativeOngoingLiveAreaRemovesPendingToolSpinnerWhenToolCompletesDuringAssistantStream(t *testing.T) {
	var out bytes.Buffer
	m := newNativeSurfaceTestModel(&out)
	if rendered := m.View(); rendered != "" {
		t.Fatalf("native ongoing View() returned %q, want empty renderer payload", rendered)
	}

	streamed := applyNativeSurfaceRuntimeEventForTest(t, m, clientui.Event{
		Kind:                clientui.EventAssistantDelta,
		StepID:              "step-final",
		AssistantDelta:      "working",
		AssistantDeltaPhase: clientui.MessagePhaseFinal,
	})
	_ = collectCmdMessages(t, streamed.cmd)
	started := applyNativeSurfaceRuntimeEventForTest(t, m, clientui.Event{
		Kind:                       clientui.EventToolCallStarted,
		StepID:                     "step-final",
		CommittedTranscriptChanged: true,
		TranscriptRevision:         1,
		CommittedEntryStart:        0,
		CommittedEntryStartSet:     true,
		TranscriptEntries: []clientui.ChatEntry{{
			Role:       "tool_call",
			Text:       "pwd",
			ToolCallID: "call-shell",
			ToolCall:   &clientui.ToolCallMeta{ToolName: "exec_command", IsShell: true, Command: "pwd"},
		}},
	})
	_ = collectCmdMessages(t, started.cmd)
	out.Reset()

	if rendered := m.View(); rendered != "" {
		t.Fatalf("native ongoing View() returned %q after tool start, want empty renderer payload", rendered)
	}
	spinner := pendingToolSpinnerFrame(m.spinnerFrame)
	startPlain := stripANSIAndTrimRight(out.String())
	if !strings.Contains(startPlain, spinner) || !strings.Contains(startPlain, "pwd") {
		t.Fatalf("native live area did not render pending tool spinner %q with command, got %q", spinner, startPlain)
	}

	completed := applyNativeSurfaceRuntimeEventForTest(t, m, clientui.Event{
		Kind:                       clientui.EventToolCallCompleted,
		StepID:                     "step-final",
		CommittedTranscriptChanged: true,
		TranscriptRevision:         2,
		CommittedEntryStart:        1,
		CommittedEntryStartSet:     true,
		TranscriptEntries: []clientui.ChatEntry{{
			Role:       "tool_result_ok",
			Text:       "/tmp",
			ToolCallID: "call-shell",
		}},
	})
	_ = collectCmdMessages(t, completed.cmd)
	out.Reset()

	if rendered := m.View(); rendered != "" {
		t.Fatalf("native ongoing View() returned %q after tool completion, want empty renderer payload", rendered)
	}
	completedPlain := stripANSIAndTrimRight(out.String())
	if strings.Contains(completedPlain, "pwd") || strings.Contains(completedPlain, spinner) {
		t.Fatalf("native live area kept completed tool in pending live area, got %q", completedPlain)
	}
}

func TestNativeOngoingLiveAreaBoundsPendingToolsToTerminalHeight(t *testing.T) {
	var out bytes.Buffer
	m := newSizedProjectedClosedUIModel(nil, 80, 8, WithUINativeSurfaceWriter(&out))
	entries := make([]tui.TranscriptEntry, 0, 24)
	for idx := 0; idx < 24; idx++ {
		entries = append(entries, tui.TranscriptEntry{
			Role:       tui.TranscriptRoleToolCall,
			Text:       "tool pending",
			ToolCallID: "call-pending",
			ToolCall: &transcript.ToolCallMeta{
				ToolName: "exec_command",
				IsShell:  true,
				Command:  "tool pending",
			},
			Committed: true,
		})
	}
	seedNativeSurfaceTranscript(m, entries)

	if rendered := m.View(); rendered != "" {
		t.Fatalf("native ongoing View() returned %q, want empty renderer payload", rendered)
	}
	if m.nativeLiveAreaError != nil {
		t.Fatalf("native live area render error = %v, want nil", m.nativeLiveAreaError)
	}
	if got := nativeSurfaceRenderedLineCount(out.String()); got > m.termHeight {
		t.Fatalf("native live area rendered %d lines, terminal height is %d", got, m.termHeight)
	}
}

func TestNativeOngoingLiveAreaBoundsFullFrameToTerminalHeight(t *testing.T) {
	var out bytes.Buffer
	m := newSizedProjectedClosedUIModel(nil, 80, 4, WithUINativeSurfaceWriter(&out))
	frame := uiRenderFrame{
		width:       80,
		height:      4,
		chatPanel:   []string{"pending tool"},
		queuePane:   []string{"queued one", "queued two"},
		inputPane:   []string{"input one", "input two"},
		statusLine:  "status",
		tailOnly:    true,
		inputCursor: uiInputFieldCursor{},
	}

	exitMainThread := m.enterUIMainThread("native live frame test")
	defer exitMainThread()
	if rendered := m.layout().renderNativeLiveAreaFrame(frame); rendered != "" {
		t.Fatalf("native live render returned %q, want empty renderer payload", rendered)
	}
	if m.nativeLiveAreaError != nil {
		t.Fatalf("native live area render error = %v, want nil", m.nativeLiveAreaError)
	}
	if got := nativeSurfaceRenderedLineCount(out.String()); got > frame.height {
		t.Fatalf("native live area rendered %d lines, terminal height is %d", got, frame.height)
	}
	plain := stripANSIAndTrimRight(out.String())
	if strings.Contains(plain, "pending tool") || strings.Contains(plain, "queued one") {
		t.Fatalf("native bounded full frame kept far-edge lines instead of tail, got %q", plain)
	}
	if strings.Contains(plain, "queued two") {
		t.Fatalf("native bounded full frame did not reserve a stable history row, got %q", plain)
	}
	if !strings.Contains(plain, "input one") || !strings.Contains(plain, "input two") || !strings.Contains(plain, "status") {
		t.Fatalf("native bounded full frame dropped expected tail lines, got %q", plain)
	}
}

func TestNativeOngoingLargeInputKeepsTailAndCursorInsideReservedViewport(t *testing.T) {
	var out bytes.Buffer
	m := newSizedProjectedClosedUIModel(nil, 80, 8, WithUINativeSurfaceWriter(&out))
	m.replaceMainInput(strings.Join([]string{
		"line 01",
		"line 02",
		"line 03",
		"line 04",
		"line 05",
		"line 06 tail-marker",
	}, "\n"), -1)

	if rendered := m.View(); rendered != "" {
		t.Fatalf("native ongoing View() returned %q, want empty renderer payload", rendered)
	}
	if m.nativeSurface == nil || !m.nativeSurface.lastFrameSet {
		t.Fatal("native surface did not record a live frame")
	}
	frame := m.nativeSurface.lastFrame
	if len(frame.Lines) > nativeLiveAreaMaxRows(m.termHeight) {
		t.Fatalf("native live frame has %d lines, max reserved live rows is %d", len(frame.Lines), nativeLiveAreaMaxRows(m.termHeight))
	}
	if !frame.Cursor.Visible {
		t.Fatalf("large input tail cursor is not visible in native live frame: %+v", frame)
	}
	plain := stripANSIAndTrimRight(out.String())
	if !strings.Contains(plain, "line 06 tail-marker") {
		t.Fatalf("native large input viewport dropped current tail, got %q", plain)
	}
	if strings.Contains(plain, "line 01") {
		t.Fatalf("native large input viewport kept far-edge input instead of cursor tail, got %q", plain)
	}
}

func TestNativeOngoingDefersLiveAreaUntilTerminalSizeKnown(t *testing.T) {
	var out bytes.Buffer
	m := newProjectedClosedUIModel(nil, WithUINativeSurfaceWriter(&out))
	m.replaceMainInput("startup width", -1)

	if rendered := m.View(); rendered != "" {
		t.Fatalf("native ongoing View() returned %q before terminal size, want empty renderer payload", rendered)
	}
	if out.String() != "" {
		t.Fatalf("native live area wrote before terminal size was known: %q", out.String())
	}
	if m.nativeSurface.initialized() {
		t.Fatal("native surface initialized before terminal size was known")
	}

	next, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 20})
	m = next.(*uiModel)
	if rendered := m.View(); rendered != "" {
		t.Fatalf("native ongoing View() returned %q after terminal size, want empty renderer payload", rendered)
	}
	if m.nativeLiveAreaError != nil {
		t.Fatalf("native live area render error = %v, want nil", m.nativeLiveAreaError)
	}
	plain := stripANSIAndTrimRight(out.String())
	if !strings.Contains(plain, strings.Repeat("─", 100)) {
		t.Fatalf("native live area did not render at known terminal width, got %q", plain)
	}
}

func TestNativeSurfaceModelCloseDoesNotWriteAfterProgramExit(t *testing.T) {
	var out bytes.Buffer
	m := newSizedProjectedClosedUIModel(nil, 80, 20)
	m.nativeSurface = newUINativeSurface(
		uiMainThreadTerminalWriter{model: m, out: &out, kind: "native surface"},
		m.nativeNormalBufferAvailable,
		m.handleNativeDelayedWriteError,
	)
	m.replaceMainInput("close without terminal write", -1)
	if rendered := m.View(); rendered != "" {
		t.Fatalf("native ongoing View() returned %q, want empty renderer payload", rendered)
	}
	out.Reset()

	m.Close()

	if out.String() != "" {
		t.Fatalf("model close wrote to terminal after program exit: %q", out.String())
	}
}

func TestNativeOngoingSlashCommandPickerRowsFitLiveAreaWidth(t *testing.T) {
	var out bytes.Buffer
	registry := commands.NewRegistry()
	registry.RegisterWithOptions("long", strings.Repeat("wide description ", 12), commands.RegisterOptions{}, func(string) commands.Result {
		return commands.Result{Handled: true}
	})
	m := newSizedProjectedClosedUIModel(nil, 76, 36, WithUINativeSurfaceWriter(&out), WithUICommandRegistry(registry))
	m.replaceMainInput("/", -1)
	m.refreshSlashCommandFilterFromInputWithAuth(false)
	assertNativeFrameLinesFitTerminalWidth(t, m)

	if rendered := m.View(); rendered != "" {
		t.Fatalf("native ongoing View() returned %q, want empty renderer payload", rendered)
	}
	if m.nativeLiveAreaError != nil {
		t.Fatalf("native live area render error = %v, want nil", m.nativeLiveAreaError)
	}
}

func TestNativeOngoingPathReferencePickerRowsFitLiveAreaWidth(t *testing.T) {
	var out bytes.Buffer
	m := newSizedProjectedClosedUIModel(nil, 32, 18, WithUINativeSurfaceWriter(&out))
	m.replaceMainInput("inspect @wide", -1)
	m.pathReference.tracked = uiPathReferenceQuery{
		Active:          true,
		Start:           8,
		End:             13,
		RawQuery:        "wide",
		NormalizedQuery: "wide",
	}
	m.pathReference.matches = []uiPathReferenceCandidate{{
		Path: strings.Repeat("nested-directory/", 8) + "target.go",
	}}
	assertNativeFrameLinesFitTerminalWidth(t, m)

	if rendered := m.View(); rendered != "" {
		t.Fatalf("native ongoing View() returned %q, want empty renderer payload", rendered)
	}
	if m.nativeLiveAreaError != nil {
		t.Fatalf("native live area render error = %v, want nil", m.nativeLiveAreaError)
	}
}

func assertNativeFrameLinesFitTerminalWidth(t *testing.T, m *uiModel) {
	t.Helper()
	frame := m.layout().composeNativeLiveFrame(uiThemeStyles(m.theme), m.termWidth, m.termHeight)
	lines := frame.renderLines()
	if len(lines) == 0 {
		t.Fatal("expected native frame lines")
	}
	for _, line := range lines {
		if got := lipgloss.Width(line); got > m.termWidth {
			t.Fatalf("native frame line width = %d, terminal width is %d, raw=%q", got, m.termWidth, line)
		}
	}
}

func TestNativeSurfaceRehydratesCommittedTranscriptOnFirstRender(t *testing.T) {
	var out bytes.Buffer
	m := newNativeSurfaceTestModel(&out)
	m.replaceMainInput("startup live input", -1)
	seedNativeSurfaceTranscript(m, []tui.TranscriptEntry{
		{Role: tui.TranscriptRoleUser, Text: "native stable prompt", Committed: true},
		{Role: tui.TranscriptRoleAssistant, Text: "native stable answer", Committed: true},
	})

	if rendered := m.View(); rendered != "" {
		t.Fatalf("native ongoing View() returned %q, want empty renderer payload", rendered)
	}
	plain := stripANSIAndTrimRight(out.String())
	if !strings.Contains(plain, "native stable prompt") || !strings.Contains(plain, "native stable answer") {
		t.Fatalf("native stable rehydrate did not write committed transcript, got %q", plain)
	}
	raw := out.String()
	strippedRaw := xansi.Strip(raw)
	liveIndex := strings.Index(strippedRaw, "startup live input")
	stableIndex := strings.Index(strippedRaw, "native stable prompt")
	if liveIndex < 0 || stableIndex < 0 || liveIndex > stableIndex {
		t.Fatalf("native stable rehydrate must happen after first live viewport render; live_index=%d stable_index=%d raw=%q", liveIndex, stableIndex, raw)
	}
}

func TestNativeSurfaceRehydrateStylesFullWidthStableDividers(t *testing.T) {
	var out bytes.Buffer
	m := newNativeSurfaceTestModel(&out)
	seedNativeSurfaceTranscript(m, []tui.TranscriptEntry{
		{Role: tui.TranscriptRoleUser, Text: "native stable prompt", Committed: true},
		{Role: tui.TranscriptRoleAssistant, Text: "native stable answer", Committed: true},
	})

	if rendered := m.View(); rendered != "" {
		t.Fatalf("native ongoing View() returned %q, want empty renderer payload", rendered)
	}

	rawDivider := nativeStableDividerLineForTest(out.String())
	if rawDivider == "" {
		t.Fatalf("native stable rehydrate did not write a divider, raw=%q", out.String())
	}
	if rawDivider == tui.TranscriptDivider {
		t.Fatalf("native stable rehydrate wrote unstyled short divider %q", rawDivider)
	}
	if got := lipgloss.Width(rawDivider); got != m.termWidth {
		t.Fatalf("native stable divider width = %d, want %d, raw=%q", got, m.termWidth, rawDivider)
	}
}

func TestNativeSurfaceSteersCommittedRuntimeAppend(t *testing.T) {
	var out bytes.Buffer
	m := newNativeSurfaceTestModel(&out)
	if rendered := m.View(); rendered != "" {
		t.Fatalf("native ongoing View() returned %q, want empty renderer payload", rendered)
	}
	out.Reset()

	result := applyNativeSurfaceRuntimeEventForTest(t, m, clientui.Event{
		Kind:                       clientui.EventUserMessageFlushed,
		CommittedTranscriptChanged: true,
		TranscriptRevision:         1,
		CommittedEntryStart:        0,
		CommittedEntryStartSet:     true,
		TranscriptEntries:          []clientui.ChatEntry{{Role: "user", Text: "native committed append"}},
	})
	_ = collectCmdMessages(t, result.cmd)

	plain := stripANSIAndTrimRight(out.String())
	if !strings.Contains(plain, "native committed append") {
		t.Fatalf("native stable append did not steer committed runtime entry, got %q", plain)
	}
}

func TestNativeSurfaceSteersWideCommittedPatchSummaryWithoutPanic(t *testing.T) {
	var out bytes.Buffer
	m := newSizedProjectedClosedUIModel(nil, 77, 36, WithUINativeSurfaceWriter(&out), WithUIDebug(true))
	if rendered := m.View(); rendered != "" {
		t.Fatalf("native ongoing View() returned %q, want empty renderer payload", rendered)
	}
	out.Reset()

	result := applyNativeSurfaceRuntimeEventForTest(t, m, clientui.Event{
		Kind:                       clientui.EventToolCallCompleted,
		CommittedTranscriptChanged: true,
		TranscriptRevision:         1,
		CommittedEntryCount:        2,
		CommittedEntryStart:        0,
		CommittedEntryStartSet:     true,
		TranscriptEntries:          nativeWidePatchSummaryClientEntriesForTest(),
	})
	_ = collectCmdMessages(t, result.cmd)

	if m.nativeLiveAreaError != nil {
		t.Fatalf("wide native stable patch summary surfaced error: %v", m.nativeLiveAreaError)
	}
	for _, rawLine := range strings.Split(strings.ReplaceAll(out.String(), "\r\n", "\n"), "\n") {
		if rawLine == "" {
			continue
		}
		if got := lipgloss.Width(rawLine); got > m.termWidth {
			t.Fatalf("native stable write width = %d, want <= %d, raw=%q", got, m.termWidth, rawLine)
		}
	}
	plain := stripANSIAndTrimRight(out.String())
	if !strings.Contains(plain, "workflow-editor/workflowEditorGraph") {
		t.Fatalf("wide native stable patch summary was not written, got %q", plain)
	}
}

func TestNativeFinalAssistantCommitFinishesStreamWithoutDuplicateStableWrite(t *testing.T) {
	var out bytes.Buffer
	m := newNativeSurfaceTestModel(&out)
	if rendered := m.View(); rendered != "" {
		t.Fatalf("native ongoing View() returned %q, want empty renderer payload", rendered)
	}
	out.Reset()

	streamed := applyNativeSurfaceRuntimeEventForTest(t, m, clientui.Event{
		Kind:                clientui.EventAssistantDelta,
		StepID:              "step-final",
		AssistantDelta:      "native streamed final",
		AssistantDeltaPhase: clientui.MessagePhaseFinal,
	})
	_ = collectCmdMessages(t, streamed.cmd)
	if count := strings.Count(stripANSIAndTrimRight(out.String()), "native streamed final"); count != 0 {
		t.Fatalf("native final stream wrote stable output before promotion count = %d in %q", count, stripANSIAndTrimRight(out.String()))
	}
	if got := xansi.Strip(strings.Join(m.nativeSurface.AssistantStreamTailLines(), "")); !strings.Contains(got, "native streamed final") {
		t.Fatalf("native final stream tail skipped assistant text: %q", got)
	}

	out.Reset()
	committed := applyNativeSurfaceRuntimeEventForTest(t, m, clientui.Event{
		Kind:                       clientui.EventAssistantMessage,
		StepID:                     "step-final",
		CommittedTranscriptChanged: true,
		TranscriptRevision:         1,
		CommittedEntryStart:        0,
		CommittedEntryStartSet:     true,
		TranscriptEntries:          []clientui.ChatEntry{{Role: "assistant", Text: "native streamed final", Phase: string(clientui.MessagePhaseFinal)}},
	})
	_ = collectCmdMessages(t, committed.cmd)
	if m.nativeSurface.AssistantStreaming() {
		t.Fatal("expected native assistant stream to finish after committed final")
	}
	if count := strings.Count(stripANSIAndTrimRight(out.String()), "native streamed final"); count != 1 {
		t.Fatalf("committed final should promote native stable stream once, got %d time(s), output=%q", count, stripANSIAndTrimRight(out.String()))
	}
}

func TestNativeFinalAssistantStreamingUsesMarkdownProjection(t *testing.T) {
	var out bytes.Buffer
	m := newNativeSurfaceTestModel(&out)
	if rendered := m.View(); rendered != "" {
		t.Fatalf("native ongoing View() returned %q, want empty renderer payload", rendered)
	}
	out.Reset()

	streamed := applyNativeSurfaceRuntimeEventForTest(t, m, clientui.Event{
		Kind:                clientui.EventAssistantDelta,
		StepID:              "step-final",
		AssistantDelta:      "**native streamed final**",
		AssistantDeltaPhase: clientui.MessagePhaseFinal,
	})
	_ = collectCmdMessages(t, streamed.cmd)
	tail := xansi.Strip(strings.Join(m.nativeSurface.AssistantStreamTailLines(), ""))
	if strings.Contains(tail, "**") {
		t.Fatalf("native stream tail exposed raw markdown markers: %q", tail)
	}
	if !strings.Contains(tail, "native streamed final") {
		t.Fatalf("native stream tail skipped rendered assistant text: %q", tail)
	}

	out.Reset()
	committed := applyNativeSurfaceRuntimeEventForTest(t, m, clientui.Event{
		Kind:                       clientui.EventAssistantMessage,
		StepID:                     "step-final",
		CommittedTranscriptChanged: true,
		TranscriptRevision:         1,
		CommittedEntryCount:        1,
		CommittedEntryStart:        0,
		CommittedEntryStartSet:     true,
		TranscriptEntries:          []clientui.ChatEntry{{Role: "assistant", Text: "**native streamed final**", Phase: string(clientui.MessagePhaseFinal)}},
	})
	_ = collectCmdMessages(t, committed.cmd)
	plain := stripANSIAndTrimRight(out.String())
	if strings.Contains(plain, "**") {
		t.Fatalf("native stable stream promotion exposed raw markdown markers: %q", plain)
	}
	if !strings.Contains(plain, "native streamed final") {
		t.Fatalf("native stable stream promotion skipped rendered assistant text: %q", plain)
	}
}

func TestNativeFinalAssistantStreamingRendersMarkdownAcrossDeltasInMutableTail(t *testing.T) {
	var out bytes.Buffer
	m := newNativeSurfaceTestModel(&out)
	if rendered := m.View(); rendered != "" {
		t.Fatalf("native ongoing View() returned %q, want empty renderer payload", rendered)
	}
	out.Reset()

	first := applyNativeSurfaceRuntimeEventForTest(t, m, clientui.Event{
		Kind:                clientui.EventAssistantDelta,
		StepID:              "step-final",
		AssistantDelta:      "**rendered first**\n\n",
		AssistantDeltaPhase: clientui.MessagePhaseFinal,
	})
	_ = collectCmdMessages(t, first.cmd)
	if got := stripANSIAndTrimRight(out.String()); got != "" {
		t.Fatalf("completed markdown block promoted before following block started: %q", got)
	}

	second := applyNativeSurfaceRuntimeEventForTest(t, m, clientui.Event{
		Kind:                clientui.EventAssistantDelta,
		StepID:              "step-final",
		AssistantDelta:      "second row\n",
		AssistantDeltaPhase: clientui.MessagePhaseFinal,
	})
	_ = collectCmdMessages(t, second.cmd)
	plain := stripANSIAndTrimRight(out.String())
	if plain != "" {
		t.Fatalf("assistant delta wrote stable output before finalization: %q", plain)
	}
	tail := xansi.Strip(strings.Join(m.nativeSurface.AssistantStreamTailLines(), ""))
	if strings.Contains(tail, "**") || !strings.Contains(tail, "rendered first") || !strings.Contains(tail, "second row") {
		t.Fatalf("native rendered tail did not hold rendered Markdown until finalization: %q", tail)
	}
}

func TestNativeFinalAssistantStreamingContinuesActiveParagraphInMutableTail(t *testing.T) {
	var out bytes.Buffer
	m := newSizedProjectedClosedUIModel(nil, 157, 36, WithUINativeSurfaceWriter(&out))
	if rendered := m.View(); rendered != "" {
		t.Fatalf("native ongoing View() returned %q, want empty renderer payload", rendered)
	}
	out.Reset()

	first := applyNativeSurfaceRuntimeEventForTest(t, m, clientui.Event{
		Kind:                clientui.EventAssistantDelta,
		StepID:              "step-final",
		AssistantDelta:      "Fixed and committed as `92ad33a5 fix: stabilize native background scrollback`.\n\nWhat",
		AssistantDeltaPhase: clientui.MessagePhaseFinal,
	})
	_ = collectCmdMessages(t, first.cmd)
	if plain := stripANSIAndTrimRight(out.String()); plain != "" {
		t.Fatalf("assistant delta wrote stable output before finalization: %q", plain)
	}

	continued := applyNativeSurfaceRuntimeEventForTest(t, m, clientui.Event{
		Kind:                clientui.EventAssistantDelta,
		StepID:              "step-final",
		AssistantDelta:      "ever comes next stays in the mutable live tail.",
		AssistantDeltaPhase: clientui.MessagePhaseFinal,
	})
	_ = collectCmdMessages(t, continued.cmd)
	tail := xansi.Strip(strings.Join(m.nativeSurface.AssistantStreamTailLines(), ""))
	if !strings.Contains(tail, "Fixed and committed") || !strings.Contains(tail, "Whatever comes next") {
		t.Fatalf("native rendered tail skipped continued active paragraph: %q", tail)
	}
}

func TestNativeFinalAssistantStreamingHoldsUnterminatedLongLine(t *testing.T) {
	var out bytes.Buffer
	m := newNativeSurfaceTestModel(&out)
	if rendered := m.View(); rendered != "" {
		t.Fatalf("native ongoing View() returned %q, want empty renderer payload", rendered)
	}
	out.Reset()

	first := applyNativeSurfaceRuntimeEventForTest(t, m, clientui.Event{
		Kind:                clientui.EventAssistantDelta,
		StepID:              "step-final",
		AssistantDelta:      strings.Repeat("long paragraph segment ", 8),
		AssistantDeltaPhase: clientui.MessagePhaseFinal,
	})
	_ = collectCmdMessages(t, first.cmd)
	second := applyNativeSurfaceRuntimeEventForTest(t, m, clientui.Event{
		Kind:                clientui.EventAssistantDelta,
		StepID:              "step-final",
		AssistantDelta:      strings.Repeat("continued segment ", 8),
		AssistantDeltaPhase: clientui.MessagePhaseFinal,
	})
	_ = collectCmdMessages(t, second.cmd)
	if got := stripANSIAndTrimRight(out.String()); got != "" {
		t.Fatalf("unterminated source line promoted before newline: %q", got)
	}
	if tail := xansi.Strip(strings.Join(m.nativeSurface.AssistantStreamTailLines(), "")); !strings.Contains(tail, "long paragraph") || !strings.Contains(tail, "continued") {
		t.Fatalf("unterminated line tail skipped streamed text: %q", tail)
	}
}

func TestNativeFinalAssistantStreamingHoldsUnterminatedInlineCodeLine(t *testing.T) {
	var out bytes.Buffer
	m := newSizedProjectedClosedUIModel(nil, 77, 36, WithUINativeSurfaceWriter(&out))
	if rendered := m.View(); rendered != "" {
		t.Fatalf("native ongoing View() returned %q, want empty renderer payload", rendered)
	}
	out.Reset()

	source := "The QA capture shows the live assistant stream rendered as markdown in the live area with the input/status chrome intact. I saved proof at `/Users/nek/Dev/kent/.kent/qa/proofs/20260628T205525Z-native-rendered-markdown-active-final`;"
	for _, chunk := range []string{source[:120], source[120:]} {
		result := applyNativeSurfaceRuntimeEventForTest(t, m, clientui.Event{
			Kind:                clientui.EventAssistantDelta,
			StepID:              "step-final",
			AssistantDelta:      chunk,
			AssistantDeltaPhase: clientui.MessagePhaseFinal,
		})
		_ = collectCmdMessages(t, result.cmd)
	}
	if got := stripANSIAndTrimRight(out.String()); got != "" {
		t.Fatalf("unterminated inline-code source line promoted before newline: %q", got)
	}
	tail := xansi.Strip(strings.Join(m.nativeSurface.AssistantStreamTailLines(), ""))
	if !strings.Contains(tail, "QA capture") || !strings.Contains(tail, "markdown-active-final") {
		t.Fatalf("unterminated inline-code tail skipped streamed text: %q", tail)
	}
}

func TestNativeFinalAssistantStreamingPreservesWhitespaceOnlyTail(t *testing.T) {
	var out bytes.Buffer
	m := newNativeSurfaceTestModel(&out)
	if rendered := m.View(); rendered != "" {
		t.Fatalf("native ongoing View() returned %q, want empty renderer payload", rendered)
	}
	out.Reset()

	result := applyNativeSurfaceRuntimeEventForTest(t, m, clientui.Event{
		Kind:                clientui.EventAssistantDelta,
		StepID:              "step-final",
		AssistantDelta:      "\t",
		AssistantDeltaPhase: clientui.MessagePhaseFinal,
	})
	_ = collectCmdMessages(t, result.cmd)
	tail := strings.Join(m.nativeSurface.AssistantStreamTailLines(), "\n")
	if !strings.Contains(tail, "\t") {
		t.Fatalf("native whitespace-only stream tail skipped source whitespace: %#v", m.nativeSurface.AssistantStreamTailLines())
	}
}

func TestNativeFinalAssistantStreamingHoldsUnstableMarkdownTable(t *testing.T) {
	var out bytes.Buffer
	m := newNativeSurfaceTestModel(&out)
	if rendered := m.View(); rendered != "" {
		t.Fatalf("native ongoing View() returned %q, want empty renderer payload", rendered)
	}
	out.Reset()

	chunks := []string{
		"| Name | Value |\n",
		"| --- | --- |\n",
		"| alpha | beta |\n",
	}
	for _, chunk := range chunks {
		result := applyNativeSurfaceRuntimeEventForTest(t, m, clientui.Event{
			Kind:                clientui.EventAssistantDelta,
			StepID:              "step-final",
			AssistantDelta:      chunk,
			AssistantDeltaPhase: clientui.MessagePhaseFinal,
		})
		_ = collectCmdMessages(t, result.cmd)
	}
	if got := stripANSIAndTrimRight(out.String()); got != "" {
		t.Fatalf("unstable markdown table promoted before table boundary: %q", got)
	}
	tail := xansi.Strip(strings.Join(m.nativeSurface.AssistantStreamTailLines(), "\n"))
	if !strings.Contains(tail, "Name") || !strings.Contains(tail, "alpha") {
		t.Fatalf("native rendered table tail skipped table content: %q", tail)
	}

	out.Reset()
	committed := applyNativeSurfaceRuntimeEventForTest(t, m, clientui.Event{
		Kind:                       clientui.EventAssistantMessage,
		StepID:                     "step-final",
		CommittedTranscriptChanged: true,
		TranscriptRevision:         1,
		CommittedEntryCount:        1,
		CommittedEntryStart:        0,
		CommittedEntryStartSet:     true,
		TranscriptEntries:          []clientui.ChatEntry{{Role: "assistant", Text: strings.Join(chunks, ""), Phase: string(clientui.MessagePhaseFinal)}},
	})
	_ = collectCmdMessages(t, committed.cmd)
	plain := stripANSIAndTrimRight(out.String())
	if !strings.Contains(plain, "Name") || !strings.Contains(plain, "alpha") {
		t.Fatalf("native stable table finalization skipped table content: %q", plain)
	}
}

func TestNativeFinalAssistantStreamingHoldsUnclosedFenceWithBlankLine(t *testing.T) {
	var out bytes.Buffer
	m := newNativeSurfaceTestModel(&out)
	if rendered := m.View(); rendered != "" {
		t.Fatalf("native ongoing View() returned %q, want empty renderer payload", rendered)
	}
	out.Reset()

	open := applyNativeSurfaceRuntimeEventForTest(t, m, clientui.Event{
		Kind:                clientui.EventAssistantDelta,
		StepID:              "step-final",
		AssistantDelta:      "```go\nline1\n\n",
		AssistantDeltaPhase: clientui.MessagePhaseFinal,
	})
	_ = collectCmdMessages(t, open.cmd)
	more := applyNativeSurfaceRuntimeEventForTest(t, m, clientui.Event{
		Kind:                clientui.EventAssistantDelta,
		StepID:              "step-final",
		AssistantDelta:      "line2\n",
		AssistantDeltaPhase: clientui.MessagePhaseFinal,
	})
	_ = collectCmdMessages(t, more.cmd)
	if got := stripANSIAndTrimRight(out.String()); got != "" {
		t.Fatalf("unclosed fenced code block promoted before fence closed: %q", got)
	}

	close := applyNativeSurfaceRuntimeEventForTest(t, m, clientui.Event{
		Kind:                clientui.EventAssistantDelta,
		StepID:              "step-final",
		AssistantDelta:      "```\n",
		AssistantDeltaPhase: clientui.MessagePhaseFinal,
	})
	_ = collectCmdMessages(t, close.cmd)
	plain := stripANSIAndTrimRight(out.String())
	if plain != "" {
		t.Fatalf("closed fenced code block wrote stable output before finalization: %q", plain)
	}
	tail := xansi.Strip(strings.Join(m.nativeSurface.AssistantStreamTailLines(), ""))
	if !strings.Contains(tail, "line1") || !strings.Contains(tail, "line2") {
		t.Fatalf("native rendered code tail skipped fenced content: %q", tail)
	}
}

func TestNativeFinalAssistantStreamingDoesNotPromoteRowsChangedByLaterCodeFence(t *testing.T) {
	var out bytes.Buffer
	m := newNativeSurfaceTestModel(&out)
	if rendered := m.View(); rendered != "" {
		t.Fatalf("native ongoing View() returned %q, want empty renderer payload", rendered)
	}
	out.Reset()

	first := "Yes, restart the Kent server too.\n\n" +
		"This PR changes both CLI/TUI-side native scrollback handling and server/runtime transcript event metadata. Restarting only the TUI can leave it connected to an old server emitting the old event shape/behavior.\n\n" +
		"Use whatever you normally use to stop/start the daemon, or from this repo:\n\n" +
		"```sh\n"
	open := applyNativeSurfaceRuntimeEventForTest(t, m, clientui.Event{
		Kind:                clientui.EventAssistantDelta,
		StepID:              "step-final",
		AssistantDelta:      first,
		AssistantDeltaPhase: clientui.MessagePhaseFinal,
	})
	_ = collectCmdMessages(t, open.cmd)

	more := applyNativeSurfaceRuntimeEventForTest(t, m, clientui.Event{
		Kind:                clientui.EventAssistantDelta,
		StepID:              "step-final",
		AssistantDelta:      "kent",
		AssistantDeltaPhase: clientui.MessagePhaseFinal,
	})
	_ = collectCmdMessages(t, more.cmd)
}

func TestNativeFinalAssistantStreamingKeepsMismatchedFenceMarkerOpen(t *testing.T) {
	var out bytes.Buffer
	m := newNativeSurfaceTestModel(&out)
	if rendered := m.View(); rendered != "" {
		t.Fatalf("native ongoing View() returned %q, want empty renderer payload", rendered)
	}
	out.Reset()

	open := applyNativeSurfaceRuntimeEventForTest(t, m, clientui.Event{
		Kind:                clientui.EventAssistantDelta,
		StepID:              "step-final",
		AssistantDelta:      "```go\nline1\n~~~ not close\n\n",
		AssistantDeltaPhase: clientui.MessagePhaseFinal,
	})
	_ = collectCmdMessages(t, open.cmd)
	more := applyNativeSurfaceRuntimeEventForTest(t, m, clientui.Event{
		Kind:                clientui.EventAssistantDelta,
		StepID:              "step-final",
		AssistantDelta:      "line2\n",
		AssistantDeltaPhase: clientui.MessagePhaseFinal,
	})
	_ = collectCmdMessages(t, more.cmd)
	if got := stripANSIAndTrimRight(out.String()); got != "" {
		t.Fatalf("mismatched fence marker closed fenced code block early: %q", got)
	}
}

func TestNativeFinalAssistantStreamingKeepsFenceWithTrailingTextOpen(t *testing.T) {
	var out bytes.Buffer
	m := newNativeSurfaceTestModel(&out)
	if rendered := m.View(); rendered != "" {
		t.Fatalf("native ongoing View() returned %q, want empty renderer payload", rendered)
	}
	out.Reset()

	open := applyNativeSurfaceRuntimeEventForTest(t, m, clientui.Event{
		Kind:                clientui.EventAssistantDelta,
		StepID:              "step-final",
		AssistantDelta:      "```go\nline1\n``` not close\n\n",
		AssistantDeltaPhase: clientui.MessagePhaseFinal,
	})
	_ = collectCmdMessages(t, open.cmd)
	more := applyNativeSurfaceRuntimeEventForTest(t, m, clientui.Event{
		Kind:                clientui.EventAssistantDelta,
		StepID:              "step-final",
		AssistantDelta:      "line2\n",
		AssistantDeltaPhase: clientui.MessagePhaseFinal,
	})
	_ = collectCmdMessages(t, more.cmd)
	if got := stripANSIAndTrimRight(out.String()); got != "" {
		t.Fatalf("fence line with trailing text closed fenced code block early: %q", got)
	}
}

func TestNativeSurfaceResizeRecreatesWithoutReplayingStableTranscript(t *testing.T) {
	var out bytes.Buffer
	m := newNativeSurfaceTestModel(&out)
	seedNativeSurfaceTranscript(m, []tui.TranscriptEntry{
		{Role: tui.TranscriptRoleUser, Text: "resize rehydrate prompt", Committed: true},
	})
	if rendered := m.View(); rendered != "" {
		t.Fatalf("native ongoing View() returned %q, want empty renderer payload", rendered)
	}
	firstBuffer := m.nativeSurface.buffer
	out.Reset()

	m.termWidth = 100
	if rendered := m.View(); rendered != "" {
		t.Fatalf("native ongoing View() returned %q after resize, want empty renderer payload", rendered)
	}
	if m.nativeSurface.buffer == firstBuffer {
		t.Fatal("expected resize to recreate native stable buffer")
	}
	plain := stripANSIAndTrimRight(out.String())
	if strings.Contains(plain, "resize rehydrate prompt") {
		t.Fatalf("resize replayed already-emitted committed transcript, got %q", plain)
	}
}

func TestNativeSurfaceResizeDropsPreviousLiveFrameBeforeReplacingGeometry(t *testing.T) {
	var out bytes.Buffer
	m := newSizedProjectedClosedUIModel(nil, 120, 30, WithUINativeSurfaceWriter(&out))
	m.replaceMainInput("resize anchor", -1)
	if rendered := m.View(); rendered != "" {
		t.Fatalf("native ongoing View() returned %q, want empty renderer payload", rendered)
	}
	out.Reset()

	m.termWidth = 180
	if rendered := m.View(); rendered != "" {
		t.Fatalf("native ongoing View() returned %q after resize, want empty renderer payload", rendered)
	}

	raw := out.String()
	if !strings.Contains(raw, xansi.CursorPosition(1, 27)) {
		t.Fatalf("native resize did not repaint replacement live frame at an absolute viewport anchor, raw=%q", raw)
	}
	plain := stripANSIAndTrimRight(raw)
	if !strings.Contains(plain, "resize anchor") {
		t.Fatalf("native resize did not render replacement live frame, got %q", plain)
	}
}

func TestNativeSurfaceCommentaryStreamSurvivesGeometryReplacement(t *testing.T) {
	var out bytes.Buffer
	m := newSizedProjectedClosedUIModel(nil, 120, 30, WithUINativeSurfaceWriter(&out))
	if rendered := m.View(); rendered != "" {
		t.Fatalf("native ongoing View() returned %q, want empty renderer payload", rendered)
	}
	out.Reset()

	streamed := applyNativeSurfaceRuntimeEventForTest(t, m, clientui.Event{
		Kind:                clientui.EventAssistantDelta,
		StepID:              "step-commentary",
		AssistantDelta:      "commentary before resize",
		AssistantDeltaPhase: clientui.MessagePhaseCommentary,
	})
	_ = collectCmdMessages(t, streamed.cmd)
	if !m.nativeSurface.AssistantStreaming() {
		t.Fatal("expected commentary delta to start native assistant streaming")
	}
	if got := xansi.Strip(strings.Join(m.nativeSurface.AssistantStreamTailLines(), "")); !strings.Contains(got, "commentary before resize") {
		t.Fatalf("native commentary stream tail before replacement skipped assistant text: %q", got)
	}
	out.Reset()

	m.termWidth = 180
	if rendered := m.View(); rendered != "" {
		t.Fatalf("native ongoing View() returned %q after resize, want empty renderer payload", rendered)
	}
	if m.nativeLiveAreaError != nil {
		t.Fatalf("native commentary resize replacement error = %v, want nil", m.nativeLiveAreaError)
	}
	out.Reset()

	continued := applyNativeSurfaceRuntimeEventForTest(t, m, clientui.Event{
		Kind:                clientui.EventAssistantDelta,
		StepID:              "step-commentary",
		AssistantDelta:      " and after resize",
		AssistantDeltaPhase: clientui.MessagePhaseCommentary,
	})
	_ = collectCmdMessages(t, continued.cmd)
	if got := xansi.Strip(strings.Join(m.nativeSurface.AssistantStreamTailLines(), "")); !strings.Contains(got, "and after resize") {
		t.Fatalf("native commentary stream tail after replacement skipped assistant text: %q", got)
	}
}

func TestNativeSurfaceResizeSettlesWithoutReplayingStableTranscript(t *testing.T) {
	var out bytes.Buffer
	m := newNativeSurfaceTestModel(&out)
	seedNativeSurfaceTranscript(m, []tui.TranscriptEntry{
		{Role: tui.TranscriptRoleUser, Text: "debounced resize prompt", Committed: true},
	})
	if rendered := m.View(); rendered != "" {
		t.Fatalf("native ongoing View() returned %q, want empty renderer payload", rendered)
	}
	firstBuffer := m.nativeSurface.buffer
	out.Reset()

	next, cmd := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = next.(*uiModel)
	if cmd == nil {
		t.Fatal("expected resize to schedule native stable rehydrate")
	}
	if m.nativeResizeRehydrateToken == 0 {
		t.Fatal("expected resize rehydrate token to be recorded")
	}
	if rendered := m.View(); rendered != "" {
		t.Fatalf("native ongoing View() returned %q after resize, want empty renderer payload", rendered)
	}
	if m.nativeSurface.buffer == firstBuffer {
		t.Fatal("expected resize render to recreate native stable buffer for new geometry")
	}
	if plain := stripANSIAndTrimRight(out.String()); strings.Contains(plain, "debounced resize prompt") {
		t.Fatalf("resize rehydrated stable transcript before debounce settled, got %q", plain)
	}

	out.Reset()
	next, resizeCmd := m.Update(nativeSurfaceResizeRehydrateMsg{token: m.nativeResizeRehydrateToken, width: 100, height: 30})
	m = next.(*uiModel)
	if resizeCmd != nil {
		t.Fatal("did not expect follow-up command after settled resize rehydrate")
	}
	if m.nativeResizeRehydrateToken != 0 {
		t.Fatalf("resize rehydrate token = %d, want cleared after successful rehydrate", m.nativeResizeRehydrateToken)
	}
	plain := stripANSIAndTrimRight(out.String())
	if strings.Contains(plain, "debounced resize prompt") {
		t.Fatalf("settled resize replayed already-emitted transcript, got %q", plain)
	}
}

func TestNativeSurfaceResizePendingAppendFlushesOnlyNewStableRows(t *testing.T) {
	var out bytes.Buffer
	m := newNativeSurfaceTestModel(&out)
	seedNativeSurfaceTranscript(m, []tui.TranscriptEntry{
		{Role: tui.TranscriptRoleUser, Text: "resize held prompt", Committed: true},
	})
	if rendered := m.View(); rendered != "" {
		t.Fatalf("native ongoing View() returned %q, want empty renderer payload", rendered)
	}
	out.Reset()

	next, cmd := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = next.(*uiModel)
	if cmd == nil {
		t.Fatal("expected resize to schedule native stable rehydrate")
	}
	token := m.nativeResizeRehydrateToken
	if rendered := m.View(); rendered != "" {
		t.Fatalf("native ongoing View() returned %q after resize, want empty renderer payload", rendered)
	}
	out.Reset()

	result := applyNativeSurfaceRuntimeEventForTest(t, m, clientui.Event{
		Kind:                       clientui.EventAssistantMessage,
		CommittedTranscriptChanged: true,
		TranscriptRevision:         2,
		CommittedEntryCount:        2,
		CommittedEntryStart:        1,
		CommittedEntryStartSet:     true,
		TranscriptEntries: []clientui.ChatEntry{{
			Role:  "assistant",
			Text:  "resize held answer",
			Phase: string(clientui.MessagePhaseFinal),
		}},
	})
	_ = collectCmdMessages(t, result.cmd)
	if got := out.String(); got != "" {
		t.Fatalf("resize-pending append wrote before holdoff flush: %q", got)
	}

	next, resizeCmd := m.Update(nativeSurfaceResizeRehydrateMsg{token: token, width: 100, height: 30})
	m = next.(*uiModel)
	if resizeCmd != nil {
		t.Fatal("did not expect follow-up command after settled resize rehydrate")
	}
	plain := stripANSIAndTrimRight(out.String())
	if strings.Contains(plain, "resize held prompt") {
		t.Fatalf("resize holdoff flushed already-emitted prompt, got %q", plain)
	}
	if !strings.Contains(plain, "resize held answer") {
		t.Fatalf("resize holdoff skipped new committed answer, got %q", plain)
	}
}

func TestNativeSurfaceResizeFlushesHeldStableRowsBeforeDrop(t *testing.T) {
	var out bytes.Buffer
	rendererGate := newUIRendererOutputGateState()
	m := newSizedProjectedClosedUIModel(
		nil,
		120,
		30,
		WithUIRendererOutputGateState(rendererGate),
		WithUINativeSurfaceWriter(&out),
	)
	seedNativeSurfaceTranscript(m, []tui.TranscriptEntry{
		{Role: tui.TranscriptRoleUser, Text: "holdoff prompt", Committed: true},
	})
	if rendered := m.View(); rendered != "" {
		t.Fatalf("native ongoing View() returned %q, want empty renderer payload", rendered)
	}
	out.Reset()

	rendererGate.observeWrittenPayload([]byte(xansi.SetModeAltScreenSaveCursor))
	result := applyNativeSurfaceRuntimeEventForTest(t, m, clientui.Event{
		Kind:                       clientui.EventAssistantMessage,
		CommittedTranscriptChanged: true,
		TranscriptRevision:         2,
		CommittedEntryCount:        2,
		CommittedEntryStart:        1,
		CommittedEntryStartSet:     true,
		TranscriptEntries: []clientui.ChatEntry{{
			Role:  "assistant",
			Text:  "held stable answer",
			Phase: string(clientui.MessagePhaseFinal),
		}},
	})
	_ = collectCmdMessages(t, result.cmd)
	if got := out.String(); got != "" {
		t.Fatalf("native holdoff wrote while alt-screen was active: %q", got)
	}
	if got := len(m.nativeDeliveredStableProjection.Blocks); got != 2 {
		t.Fatalf("delivered ledger count = %d, want held row recorded", got)
	}

	next, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = next.(*uiModel)
	rendererGate.observeWrittenPayload([]byte(xansi.ResetModeAltScreenSaveCursor))
	next, resizeCmd := m.Update(nativeSurfaceResizeRehydrateMsg{token: m.nativeResizeRehydrateToken, width: 100, height: 30})
	m = next.(*uiModel)
	if resizeCmd != nil {
		t.Fatal("did not expect follow-up command after settled resize rehydrate")
	}
	if m.nativeLiveAreaError != nil {
		t.Fatalf("resize holdoff flush surfaced native error: %v", m.nativeLiveAreaError)
	}
	plain := stripANSIAndTrimRight(out.String())
	if !strings.Contains(plain, "held stable answer") {
		t.Fatalf("resize dropped held stable row before flush, got %q", plain)
	}
}

func TestNativeSurfaceResizeReprojectsDeliveredLedgerForPendingAppend(t *testing.T) {
	var out bytes.Buffer
	m := newSizedProjectedClosedUIModel(nil, 48, 30, WithUINativeSurfaceWriter(&out), WithUIDebug(true))
	seedNativeSurfaceTranscript(m, []tui.TranscriptEntry{
		{Role: tui.TranscriptRoleUser, Text: "resize ledger prompt with enough content to clamp differently after resize", Committed: true},
	})
	if rendered := m.View(); rendered != "" {
		t.Fatalf("native ongoing View() returned %q, want empty renderer payload", rendered)
	}
	out.Reset()

	next, _ := m.Update(tea.WindowSizeMsg{Width: 28, Height: 30})
	m = next.(*uiModel)
	out.Reset()

	result := applyNativeSurfaceRuntimeEventForTest(t, m, clientui.Event{
		Kind:                       clientui.EventAssistantMessage,
		CommittedTranscriptChanged: true,
		TranscriptRevision:         2,
		CommittedEntryCount:        2,
		CommittedEntryStart:        1,
		CommittedEntryStartSet:     true,
		TranscriptEntries:          []clientui.ChatEntry{{Role: "assistant", Text: "append after narrow resize", Phase: string(clientui.MessagePhaseFinal)}},
	})
	_ = collectCmdMessages(t, result.cmd)
	if got := out.String(); got != "" {
		t.Fatalf("committed append wrote during resize debounce: %q", got)
	}

	next, resizeCmd := m.Update(nativeSurfaceResizeRehydrateMsg{token: m.nativeResizeRehydrateToken, width: 28, height: 30})
	m = next.(*uiModel)
	if resizeCmd != nil {
		t.Fatal("did not expect follow-up command after settled resize rehydrate")
	}
	if m.nativeLiveAreaError != nil {
		t.Fatalf("resize ledger reproject surfaced native error: %v", m.nativeLiveAreaError)
	}
	plain := stripANSIAndTrimRight(out.String())
	if strings.Contains(plain, "resize ledger prompt") {
		t.Fatalf("settled resize replayed already-emitted prompt, got %q", plain)
	}
	if !strings.Contains(plain, "append after narrow resize") {
		t.Fatalf("settled resize skipped pending append, got %q", plain)
	}
}

func TestNativeSurfaceResizePendingAppendSurvivesSecondResizeBeforeSettle(t *testing.T) {
	var out bytes.Buffer
	m := newSizedProjectedClosedUIModel(nil, 48, 30, WithUINativeSurfaceWriter(&out), WithUIDebug(true))
	seedNativeSurfaceTranscript(m, []tui.TranscriptEntry{
		{Role: tui.TranscriptRoleUser, Text: "first resize prompt with enough content to wrap", Committed: true},
	})
	if rendered := m.View(); rendered != "" {
		t.Fatalf("native ongoing View() returned %q, want empty renderer payload", rendered)
	}
	out.Reset()

	next, _ := m.Update(tea.WindowSizeMsg{Width: 36, Height: 30})
	m = next.(*uiModel)
	firstToken := m.nativeResizeRehydrateToken
	result := applyNativeSurfaceRuntimeEventForTest(t, m, clientui.Event{
		Kind:                       clientui.EventAssistantMessage,
		CommittedTranscriptChanged: true,
		TranscriptRevision:         2,
		CommittedEntryCount:        2,
		CommittedEntryStart:        1,
		CommittedEntryStartSet:     true,
		TranscriptEntries:          []clientui.ChatEntry{{Role: "assistant", Text: "append after first resize", Phase: string(clientui.MessagePhaseFinal)}},
	})
	_ = collectCmdMessages(t, result.cmd)
	if got := out.String(); got != "" {
		t.Fatalf("committed append wrote during first resize debounce: %q", got)
	}

	next, _ = m.Update(tea.WindowSizeMsg{Width: 30, Height: 30})
	m = next.(*uiModel)
	if m.nativeResizeRehydrateToken == firstToken {
		t.Fatal("second resize did not replace resize token")
	}
	next, staleCmd := m.Update(nativeSurfaceResizeRehydrateMsg{token: firstToken, width: 36, height: 30})
	m = next.(*uiModel)
	if staleCmd != nil {
		t.Fatal("stale resize settle returned command")
	}
	if got := out.String(); got != "" {
		t.Fatalf("stale resize settle wrote pending append: %q", got)
	}

	next, resizeCmd := m.Update(nativeSurfaceResizeRehydrateMsg{token: m.nativeResizeRehydrateToken, width: 30, height: 30})
	m = next.(*uiModel)
	if resizeCmd != nil {
		t.Fatal("did not expect follow-up command after second settled resize")
	}
	if m.nativeLiveAreaError != nil {
		t.Fatalf("second settled resize surfaced native error: %v", m.nativeLiveAreaError)
	}
	plain := stripANSIAndTrimRight(out.String())
	if strings.Contains(plain, "first resize prompt") {
		t.Fatalf("second settled resize replayed already-emitted prompt, got %q", plain)
	}
	if strings.Count(plain, "append after first resize") != 1 {
		t.Fatalf("second settled resize append count in %q, want exactly one", plain)
	}
}

func TestNativeSurfaceResizeDisablesActiveNativeAssistantStreamBeforeFinalizer(t *testing.T) {
	var out bytes.Buffer
	m := newSizedProjectedClosedUIModel(nil, 80, 30, WithUINativeSurfaceWriter(&out), WithUIDebug(false))
	if rendered := m.View(); rendered != "" {
		t.Fatalf("native ongoing View() returned %q, want empty renderer payload", rendered)
	}
	out.Reset()

	streamed := applyNativeSurfaceRuntimeEventForTest(t, m, clientui.Event{
		Kind:                clientui.EventAssistantDelta,
		StepID:              "step-stream",
		AssistantDelta:      "promoted prefix\n\nmutable tail",
		AssistantDeltaPhase: clientui.MessagePhaseFinal,
	})
	_ = collectCmdMessages(t, streamed.cmd)
	if m.nativeSurface == nil || !m.nativeSurface.AssistantStreaming() {
		t.Fatal("expected assistant delta to start native assistant streaming")
	}
	if got := stripANSIAndTrimRight(out.String()); got != "" {
		t.Fatalf("assistant stream wrote stable setup output before finalization: %q", got)
	}
	out.Reset()

	next, resizeCmd := m.Update(tea.WindowSizeMsg{Width: 60, Height: 30})
	m = next.(*uiModel)
	_ = collectCmdMessages(t, resizeCmd)
	if m.nativeSurface != nil {
		t.Fatal("resize with active native assistant stream should disable native")
	}
	if m.nativeLiveAreaError == nil {
		t.Fatal("resize with active native assistant stream did not surface native error")
	}
	if strings.Contains(out.String(), xansi.EraseEntireLine) {
		t.Fatalf("resize active-stream disable cleared stale live frame: %q", out.String())
	}

	finalized := applyNativeSurfaceRuntimeEventForTest(t, m, clientui.Event{
		Kind:                       clientui.EventAssistantMessage,
		StepID:                     "step-stream",
		CommittedTranscriptChanged: true,
		TranscriptRevision:         1,
		CommittedEntryCount:        1,
		CommittedEntryStart:        0,
		CommittedEntryStartSet:     true,
		TranscriptEntries: []clientui.ChatEntry{{
			Role:  "assistant",
			Text:  "promoted prefix\n\nmutable tail",
			Phase: string(clientui.MessagePhaseFinal),
		}},
	})
	_ = collectCmdMessages(t, finalized.cmd)
	if strings.Contains(stripANSIAndTrimRight(out.String()), "promoted prefix") {
		t.Fatalf("finalizer replayed promoted stream prefix through native after resize disable: %q", out.String())
	}
}

func TestNativeSurfaceResizeDoesNotReprojectDeliveredBlockToReplacementContent(t *testing.T) {
	var out bytes.Buffer
	m := newSizedProjectedClosedUIModel(nil, 48, 30, WithUINativeSurfaceWriter(&out), WithUIDebug(false))
	seedNativeSurfaceTranscript(m, []tui.TranscriptEntry{
		{Role: tui.TranscriptRoleUser, Text: "original prompt before resize", Committed: true},
	})
	if rendered := m.View(); rendered != "" {
		t.Fatalf("native ongoing View() returned %q, want empty renderer payload", rendered)
	}
	out.Reset()

	next, _ := m.Update(tea.WindowSizeMsg{Width: 30, Height: 30})
	m = next.(*uiModel)
	replacement := clientui.Event{
		Kind:                       clientui.EventUserMessageFlushed,
		CommittedTranscriptChanged: true,
		TranscriptRevision:         2,
		CommittedEntryCount:        1,
		CommittedEntryStart:        0,
		CommittedEntryStartSet:     true,
		TranscriptEntries:          []clientui.ChatEntry{{Role: "user", Text: "replacement prompt during resize"}},
	}
	result := applyNativeSurfaceRuntimeEventForTest(t, m, replacement)
	_ = collectCmdMessages(t, result.cmd)
	if got := out.String(); got != "" {
		t.Fatalf("replacement wrote during resize debounce: %q", got)
	}

	next, resizeCmd := m.Update(nativeSurfaceResizeRehydrateMsg{token: m.nativeResizeRehydrateToken, width: 30, height: 30})
	m = next.(*uiModel)
	_ = collectCmdMessages(t, resizeCmd)
	if m.nativeSurface != nil {
		t.Fatal("non-appendable replacement during resize should disable native after settle")
	}
	if m.nativeLiveAreaError == nil {
		t.Fatal("non-appendable replacement during resize did not surface native error")
	}
	if strings.Contains(stripANSIAndTrimRight(out.String()), "replacement prompt during resize") {
		t.Fatalf("replacement content was written through native stable path: %q", out.String())
	}
}

func TestNativeSurfaceResizeDoesNotReprojectWhitespaceReplacementContent(t *testing.T) {
	var out bytes.Buffer
	m := newSizedProjectedClosedUIModel(nil, 48, 30, WithUINativeSurfaceWriter(&out), WithUIDebug(false))
	seedNativeSurfaceTranscript(m, []tui.TranscriptEntry{
		{Role: tui.TranscriptRoleUser, Text: "ab", Committed: true},
	})
	if rendered := m.View(); rendered != "" {
		t.Fatalf("native ongoing View() returned %q, want empty renderer payload", rendered)
	}
	out.Reset()

	next, _ := m.Update(tea.WindowSizeMsg{Width: 30, Height: 30})
	m = next.(*uiModel)
	replacement := clientui.Event{
		Kind:                       clientui.EventUserMessageFlushed,
		CommittedTranscriptChanged: true,
		TranscriptRevision:         2,
		CommittedEntryCount:        1,
		CommittedEntryStart:        0,
		CommittedEntryStartSet:     true,
		TranscriptEntries:          []clientui.ChatEntry{{Role: "user", Text: "a b"}},
	}
	result := applyNativeSurfaceRuntimeEventForTest(t, m, replacement)
	_ = collectCmdMessages(t, result.cmd)
	if got := out.String(); got != "" {
		t.Fatalf("whitespace replacement wrote during resize debounce: %q", got)
	}

	next, resizeCmd := m.Update(nativeSurfaceResizeRehydrateMsg{token: m.nativeResizeRehydrateToken, width: 30, height: 30})
	m = next.(*uiModel)
	_ = collectCmdMessages(t, resizeCmd)
	if m.nativeSurface != nil {
		t.Fatal("whitespace replacement during resize should disable native after settle")
	}
	if m.nativeLiveAreaError == nil {
		t.Fatal("whitespace replacement during resize did not surface native error")
	}
	if strings.Contains(stripANSIAndTrimRight(out.String()), "a b") {
		t.Fatalf("whitespace replacement content was written through native stable path: %q", out.String())
	}
}

func TestNativeSurfaceResizeRejectsNonLocalPhysicalReorder(t *testing.T) {
	var out bytes.Buffer
	m := newSizedProjectedClosedUIModel(nil, 80, 30, WithUINativeSurfaceWriter(&out), WithUIDebug(false))
	seedNativeSurfaceTranscript(m, []tui.TranscriptEntry{
		{Role: tui.TranscriptRoleUser, Text: "first stable user", Committed: true},
		{Role: tui.TranscriptRoleAssistant, Text: "second stable assistant", Committed: true},
	})
	if rendered := m.View(); rendered != "" {
		t.Fatalf("native ongoing View() returned %q, want empty renderer payload", rendered)
	}
	logicalWide := m.nativeDeliveredStableProjection.Clone()
	if got := len(logicalWide.Blocks); got != 2 {
		t.Fatalf("seeded block count = %d, want 2", got)
	}
	out.Reset()

	next, _ := m.Update(tea.WindowSizeMsg{Width: 40, Height: 30})
	m = next.(*uiModel)
	result := applyNativeSurfaceRuntimeEventForTest(t, m, clientui.Event{
		Kind:                       clientui.EventAssistantMessage,
		CommittedTranscriptChanged: true,
		TranscriptRevision:         2,
		CommittedEntryCount:        3,
		CommittedEntryStart:        0,
		CommittedEntryStartSet:     true,
		TranscriptEntries: []clientui.ChatEntry{
			{Role: "assistant", Text: "second stable assistant", Phase: string(clientui.MessagePhaseFinal)},
			{Role: "user", Text: "first stable user"},
			{Role: "assistant", Text: "third stable assistant", Phase: string(clientui.MessagePhaseFinal)},
		},
	})
	_ = collectCmdMessages(t, result.cmd)
	if got := out.String(); got != "" {
		t.Fatalf("append wrote during resize debounce: %q", got)
	}

	next, resizeCmd := m.Update(nativeSurfaceResizeRehydrateMsg{token: m.nativeResizeRehydrateToken, width: 40, height: 30})
	m = next.(*uiModel)
	_ = collectCmdMessages(t, resizeCmd)
	if m.nativeSurface != nil {
		t.Fatal("non-local physical reorder should disable native after resize settle")
	}
	if m.nativeLiveAreaError == nil {
		t.Fatal("non-local physical reorder did not surface native error")
	}
	if strings.Contains(stripANSIAndTrimRight(out.String()), "third stable assistant") {
		t.Fatalf("non-local physical reorder wrote new append through native stable path: %q", out.String())
	}
}

func TestNativeSurfaceResizeReprojectsPhysicalLedgerOrderForLocalSystemNotice(t *testing.T) {
	var out bytes.Buffer
	m := newSizedProjectedClosedUIModel(nil, 64, 30, WithUINativeSurfaceWriter(&out), WithUIDebug(true))
	seedNativeSurfaceTranscript(m, []tui.TranscriptEntry{
		{Role: tui.TranscriptRoleUser, Text: "resize prompt before active stream notice", Committed: true},
		{Role: tui.TranscriptRoleSystem, Text: "Background shell 8128 completed (exit 0)", LocalAppendOnly: true},
		{Role: tui.TranscriptRoleAssistant, Text: "watcher found six threads", Committed: true, Phase: clientui.MessagePhaseCommentary},
	})
	if rendered := m.View(); rendered != "" {
		t.Fatalf("native ongoing View() returned %q, want empty renderer payload", rendered)
	}
	logicalWide := m.nativeDeliveredStableProjection.Clone()
	if got := len(logicalWide.Blocks); got != 3 {
		t.Fatalf("seeded block count = %d, want 3", got)
	}
	m.nativeDeliveredStableProjection = tui.TranscriptProjection{Blocks: []tui.TranscriptProjectionBlock{
		logicalWide.Blocks[0],
		logicalWide.Blocks[2],
		logicalWide.Blocks[1],
	}}
	out.Reset()

	next, _ := m.Update(tea.WindowSizeMsg{Width: 32, Height: 30})
	m = next.(*uiModel)
	result := applyNativeSurfaceRuntimeEventForTest(t, m, clientui.Event{
		Kind:                       clientui.EventUserMessageFlushed,
		CommittedTranscriptChanged: true,
		TranscriptRevision:         2,
		CommittedEntryCount:        4,
		CommittedEntryStart:        3,
		CommittedEntryStartSet:     true,
		TranscriptEntries:          []clientui.ChatEntry{{Role: "user", Text: "next prompt after resize"}},
	})
	_ = collectCmdMessages(t, result.cmd)
	if got := out.String(); got != "" {
		t.Fatalf("committed append wrote during resize debounce: %q", got)
	}

	next, resizeCmd := m.Update(nativeSurfaceResizeRehydrateMsg{token: m.nativeResizeRehydrateToken, width: 32, height: 30})
	m = next.(*uiModel)
	if resizeCmd != nil {
		t.Fatal("did not expect follow-up command after settled resize rehydrate")
	}
	if m.nativeLiveAreaError != nil {
		t.Fatalf("resize physical-order reproject surfaced native error: %v", m.nativeLiveAreaError)
	}
	plain := stripANSIAndTrimRight(out.String())
	for _, replayed := range []string{"resize prompt before", "Background shell 8128", "watcher found six threads"} {
		if strings.Contains(plain, replayed) {
			t.Fatalf("settled resize replayed already-emitted %q in %q", replayed, plain)
		}
	}
	if !strings.Contains(plain, "next prompt after resize") {
		t.Fatalf("settled resize skipped pending append, got %q", plain)
	}
	tail := m.nativeDeliveredStableProjection.Blocks
	if len(tail) != 4 || tail[1].Role != tui.RenderIntentAssistantCommentary || tail[2].Role != tui.RenderIntentSystem || tail[3].Role != tui.RenderIntentUser {
		t.Fatalf("delivered ledger roles after resize append = %#v", tail)
	}
}

func TestNativeSurfaceResizeReprojectsLocalInsertBeforeToolRow(t *testing.T) {
	m := newProjectedClosedUIModel(nil)
	tool := tui.TranscriptProjectionBlock{
		Role:         tui.RenderIntentToolShellSuccess,
		DividerGroup: string(tui.RenderIntentTool),
		SourceKey:    "tool-shell-1",
		Lines:        []string{"$ sleep 20 && echo done"},
	}
	notice := tui.TranscriptProjectionBlock{
		Role:            tui.RenderIntentSystem,
		DividerGroup:    string(tui.RenderIntentSystem),
		SourceKey:       "notice-1",
		LocalAppendOnly: true,
		Lines:           []string{"Background shell completed"},
	}
	m.nativeDeliveredStableProjection = tui.TranscriptProjection{Blocks: []tui.TranscriptProjectionBlock{tool, notice}}
	current := tui.TranscriptProjection{Blocks: []tui.TranscriptProjectionBlock{
		{
			Role:            notice.Role,
			DividerGroup:    notice.DividerGroup,
			SourceKey:       notice.SourceKey,
			LocalAppendOnly: true,
			Lines:           []string{"Background shell completed after resize"},
		},
		{
			Role:         tool.Role,
			DividerGroup: tool.DividerGroup,
			SourceKey:    tool.SourceKey,
			Lines:        []string{"$ sleep 20 && echo done after resize"},
		},
	}}

	reprojected, ok := m.reprojectNativeDeliveredStableProjectionByPhysicalShape(current, 2)
	if !ok {
		t.Fatal("local insert before tool row was not accepted during resize reproject")
	}
	if got := reprojected.Blocks[0].Lines[0]; !strings.Contains(got, "after resize") {
		t.Fatalf("tool block was not reprojected to current geometry/content, got %q", got)
	}
	if got := reprojected.Blocks[1].Lines[0]; !strings.Contains(got, "after resize") {
		t.Fatalf("local block was not reprojected to current geometry/content, got %q", got)
	}
}

func TestNativeSurfaceResizeDebounceClampsWideCommittedPatchSummary(t *testing.T) {
	var out bytes.Buffer
	m := newSizedProjectedClosedUIModel(nil, 120, 36, WithUINativeSurfaceWriter(&out), WithUIDebug(true))
	seedNativeSurfaceTranscript(m, []tui.TranscriptEntry{{Role: tui.TranscriptRoleUser, Text: "resize base prompt", Committed: true}})
	if rendered := m.View(); rendered != "" {
		t.Fatalf("native ongoing View() returned %q, want empty renderer payload", rendered)
	}
	out.Reset()

	next, cmd := m.Update(tea.WindowSizeMsg{Width: 77, Height: 36})
	m = next.(*uiModel)
	if cmd == nil {
		t.Fatal("expected resize to schedule native stable rehydrate")
	}
	if rendered := m.View(); rendered != "" {
		t.Fatalf("native ongoing View() returned %q after resize, want empty renderer payload", rendered)
	}
	out.Reset()

	result := applyNativeSurfaceRuntimeEventForTest(t, m, clientui.Event{
		Kind:                       clientui.EventToolCallCompleted,
		CommittedTranscriptChanged: true,
		TranscriptRevision:         2,
		CommittedEntryCount:        3,
		CommittedEntryStart:        1,
		CommittedEntryStartSet:     true,
		TranscriptEntries:          nativeWidePatchSummaryClientEntriesForTest(),
	})
	_ = collectCmdMessages(t, result.cmd)
	if got := out.String(); got != "" {
		t.Fatalf("wide patch summary wrote during resize debounce: %q", got)
	}

	next, resizeCmd := m.Update(nativeSurfaceResizeRehydrateMsg{token: m.nativeResizeRehydrateToken, width: 77, height: 36})
	m = next.(*uiModel)
	if resizeCmd != nil {
		t.Fatal("did not expect follow-up command after settled resize rehydrate")
	}
	if m.nativeLiveAreaError != nil {
		t.Fatalf("wide patch summary resize flush surfaced error: %v", m.nativeLiveAreaError)
	}
	plain := stripANSIAndTrimRight(out.String())
	if !strings.Contains(plain, "workflowEditorGraph") {
		t.Fatalf("settled resize flush did not write wide patch summary, got %q", plain)
	}
	checkedPatchLine := false
	for _, rawLine := range strings.FieldsFunc(out.String(), func(r rune) bool {
		return r == '\n' || r == '\r'
	}) {
		if rawLine == "" {
			continue
		}
		if !strings.Contains(xansi.Strip(rawLine), "workflow") {
			continue
		}
		checkedPatchLine = true
		visibleStableLine := xansi.Strip(rawLine)
		if dividerIndex := strings.Index(visibleStableLine, "──"); dividerIndex >= 0 {
			visibleStableLine = visibleStableLine[:dividerIndex]
		}
		if got := lipgloss.Width(visibleStableLine); got > m.termWidth {
			t.Fatalf("resize flush stable write width = %d, want <= %d, raw=%q", got, m.termWidth, rawLine)
		}
	}
	if !checkedPatchLine {
		t.Fatalf("settled resize flush did not expose patch summary line in raw output: %q", out.String())
	}
}

func TestNativeSurfaceResizeRehydrateIgnoresStaleDebounceMessages(t *testing.T) {
	var out bytes.Buffer
	m := newNativeSurfaceTestModel(&out)
	seedNativeSurfaceTranscript(m, []tui.TranscriptEntry{
		{Role: tui.TranscriptRoleUser, Text: "latest resize prompt", Committed: true},
	})
	if rendered := m.View(); rendered != "" {
		t.Fatalf("native ongoing View() returned %q, want empty renderer payload", rendered)
	}
	out.Reset()

	next, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = next.(*uiModel)
	firstToken := m.nativeResizeRehydrateToken
	next, _ = m.Update(tea.WindowSizeMsg{Width: 90, Height: 30})
	m = next.(*uiModel)
	secondToken := m.nativeResizeRehydrateToken
	if firstToken == secondToken {
		t.Fatal("expected second resize to supersede first resize token")
	}

	next, _ = m.Update(nativeSurfaceResizeRehydrateMsg{token: firstToken, width: 100, height: 30})
	m = next.(*uiModel)
	if got := out.String(); got != "" {
		t.Fatalf("stale resize rehydrate wrote stable bytes: %q", got)
	}
	next, _ = m.Update(nativeSurfaceResizeRehydrateMsg{token: secondToken, width: 90, height: 30})
	m = next.(*uiModel)
	plain := stripANSIAndTrimRight(out.String())
	if strings.Contains(plain, "latest resize prompt") {
		t.Fatalf("latest resize replayed already-emitted transcript, got %q", plain)
	}
}

func TestNativeSurfaceResizeDebounceHoldsCommittedAppendsUntilSettledRehydrate(t *testing.T) {
	var out bytes.Buffer
	m := newNativeSurfaceTestModel(&out)
	seedNativeSurfaceTranscript(m, []tui.TranscriptEntry{
		{Role: tui.TranscriptRoleUser, Text: "resize base prompt", Committed: true},
	})
	if rendered := m.View(); rendered != "" {
		t.Fatalf("native ongoing View() returned %q, want empty renderer payload", rendered)
	}
	out.Reset()

	next, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = next.(*uiModel)
	if rendered := m.View(); rendered != "" {
		t.Fatalf("native ongoing View() returned %q after resize, want empty renderer payload", rendered)
	}
	out.Reset()

	result := applyNativeSurfaceRuntimeEventForTest(t, m, clientui.Event{
		Kind:                       clientui.EventAssistantMessage,
		CommittedTranscriptChanged: true,
		TranscriptRevision:         2,
		CommittedEntryStart:        1,
		CommittedEntryStartSet:     true,
		TranscriptEntries:          []clientui.ChatEntry{{Role: "assistant", Text: "append during resize", Phase: string(clientui.MessagePhaseFinal)}},
	})
	_ = collectCmdMessages(t, result.cmd)
	if got := out.String(); got != "" {
		t.Fatalf("committed append wrote during resize debounce: %q", got)
	}

	next, _ = m.Update(nativeSurfaceResizeRehydrateMsg{token: m.nativeResizeRehydrateToken, width: 100, height: 30})
	m = next.(*uiModel)
	plain := stripANSIAndTrimRight(out.String())
	if count := strings.Count(plain, "append during resize"); count != 1 {
		t.Fatalf("settled resize rehydrate append count = %d, want 1 in %q", count, plain)
	}
}

func TestNativeSurfaceResizeDisablesActiveAssistantStreamInsteadOfCommitRepair(t *testing.T) {
	var out bytes.Buffer
	m := newNativeSurfaceTestModel(&out)
	if rendered := m.View(); rendered != "" {
		t.Fatalf("native ongoing View() returned %q, want empty renderer payload", rendered)
	}

	streamed := applyNativeSurfaceRuntimeEventForTest(t, m, clientui.Event{
		Kind:                clientui.EventAssistantDelta,
		StepID:              "step-final",
		AssistantDelta:      "stream interrupted by resize",
		AssistantDeltaPhase: clientui.MessagePhaseFinal,
	})
	_ = collectCmdMessages(t, streamed.cmd)
	if !m.nativeSurface.AssistantStreaming() {
		t.Fatal("expected native assistant stream to be active before resize")
	}
	out.Reset()

	next, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = next.(*uiModel)
	if m.nativeSurface != nil {
		t.Fatal("expected resize to disable native surface with active assistant stream")
	}
	if m.nativeLiveAreaError == nil {
		t.Fatal("expected resize to surface native assistant stream error")
	}
	out.Reset()

	committed := applyNativeSurfaceRuntimeEventForTest(t, m, clientui.Event{
		Kind:                       clientui.EventAssistantMessage,
		StepID:                     "step-final",
		CommittedTranscriptChanged: true,
		TranscriptRevision:         1,
		CommittedEntryStart:        0,
		CommittedEntryStartSet:     true,
		TranscriptEntries:          []clientui.ChatEntry{{Role: "assistant", Text: "stream interrupted by resize", Phase: string(clientui.MessagePhaseFinal)}},
	})
	_ = collectCmdMessages(t, committed.cmd)
	if m.nativeAssistantStreamIncomplete {
		t.Fatal("expected committed final to clear incomplete native assistant stream")
	}
	plain := stripANSIAndTrimRight(out.String())
	if strings.Contains(plain, "stream interrupted by resize") {
		t.Fatalf("committed finalizer replayed active stream after resize disable: %q", plain)
	}
}

func TestNativeSurfaceAssistantStreamStartingDuringResizeDebounceCommitsThroughRepair(t *testing.T) {
	var out bytes.Buffer
	m := newNativeSurfaceTestModel(&out)
	if rendered := m.View(); rendered != "" {
		t.Fatalf("native ongoing View() returned %q, want empty renderer payload", rendered)
	}
	out.Reset()

	next, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = next.(*uiModel)
	streamed := applyNativeSurfaceRuntimeEventForTest(t, m, clientui.Event{
		Kind:                clientui.EventAssistantDelta,
		StepID:              "step-final",
		AssistantDelta:      "resize-window assistant",
		AssistantDeltaPhase: clientui.MessagePhaseFinal,
	})
	_ = collectCmdMessages(t, streamed.cmd)
	if !m.nativeAssistantStreamIncomplete {
		t.Fatal("expected assistant stream starting during resize debounce to be marked incomplete")
	}
	if got := out.String(); got != "" {
		t.Fatalf("assistant delta wrote during resize debounce: %q", got)
	}

	next, _ = m.Update(nativeSurfaceResizeRehydrateMsg{token: m.nativeResizeRehydrateToken, width: 100, height: 30})
	m = next.(*uiModel)
	out.Reset()

	committed := applyNativeSurfaceRuntimeEventForTest(t, m, clientui.Event{
		Kind:                       clientui.EventAssistantMessage,
		StepID:                     "step-final",
		CommittedTranscriptChanged: true,
		TranscriptRevision:         1,
		CommittedEntryStart:        0,
		CommittedEntryStartSet:     true,
		TranscriptEntries:          []clientui.ChatEntry{{Role: "assistant", Text: "resize-window assistant", Phase: string(clientui.MessagePhaseFinal)}},
	})
	_ = collectCmdMessages(t, committed.cmd)
	plain := stripANSIAndTrimRight(out.String())
	if count := strings.Count(plain, "resize-window assistant"); count != 1 {
		t.Fatalf("committed resize-window assistant repair count = %d, want 1 in %q", count, plain)
	}
}

func TestNativeSurfaceResizeRehydrateWaitsForReturnFromDetail(t *testing.T) {
	var out bytes.Buffer
	m := newNativeSurfaceTestModel(&out)
	seedNativeSurfaceTranscript(m, []tui.TranscriptEntry{
		{Role: tui.TranscriptRoleUser, Text: "detail resize prompt", Committed: true},
	})
	if rendered := m.View(); rendered != "" {
		t.Fatalf("native ongoing View() returned %q, want empty renderer payload", rendered)
	}
	out.Reset()

	next, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = next.(*uiModel)
	token := m.nativeResizeRehydrateToken
	m.activeSurface = uiSurfaceTranscriptDetail
	m.altScreenActive = true
	next, _ = m.Update(nativeSurfaceResizeRehydrateMsg{token: token, width: 100, height: 30})
	m = next.(*uiModel)
	if m.nativeResizeRehydrateToken != token {
		t.Fatalf("resize token changed while detail was active: got %d want %d", m.nativeResizeRehydrateToken, token)
	}
	if got := out.String(); got != "" {
		t.Fatalf("resize rehydrate wrote while detail was active: %q", got)
	}

	m.activeSurface = uiSurfaceOngoingTranscript
	m.altScreenActive = false
	next, resumeCmd := m.Update(nativeSurfaceResumeMsg{})
	m = next.(*uiModel)
	msgs := collectCmdMessages(t, resumeCmd)
	if len(msgs) != 1 {
		t.Fatalf("resume command messages = %#v, want resize rehydrate message", msgs)
	}
	resizeMsg, ok := msgs[0].(nativeSurfaceResizeRehydrateMsg)
	if !ok {
		t.Fatalf("resume command message = %T, want nativeSurfaceResizeRehydrateMsg", msgs[0])
	}
	next, _ = m.Update(resizeMsg)
	m = next.(*uiModel)
	plain := stripANSIAndTrimRight(out.String())
	if strings.Contains(plain, "detail resize prompt") {
		t.Fatalf("return from detail replayed already-emitted transcript, got %q", plain)
	}
	if m.nativeResizeRehydrateToken != 0 {
		t.Fatalf("resize token = %d, want cleared after return rehydrate", m.nativeResizeRehydrateToken)
	}
}

func TestNativeSurfaceResizeReturnBeforeDebounceDoesNotRehydrateEarly(t *testing.T) {
	var out bytes.Buffer
	m := newNativeSurfaceTestModel(&out)
	seedNativeSurfaceTranscript(m, []tui.TranscriptEntry{
		{Role: tui.TranscriptRoleUser, Text: "early return resize prompt", Committed: true},
	})
	if rendered := m.View(); rendered != "" {
		t.Fatalf("native ongoing View() returned %q, want empty renderer payload", rendered)
	}
	out.Reset()

	next, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = next.(*uiModel)
	token := m.nativeResizeRehydrateToken
	m.activeSurface = uiSurfaceTranscriptDetail
	m.altScreenActive = true
	m.activeSurface = uiSurfaceOngoingTranscript
	m.altScreenActive = false
	next, resumeCmd := m.Update(nativeSurfaceResumeMsg{})
	m = next.(*uiModel)
	if msgs := collectCmdMessages(t, resumeCmd); len(msgs) != 0 {
		t.Fatalf("resume before resize debounce produced messages %#v, want none", msgs)
	}
	if got := out.String(); got != "" {
		t.Fatalf("return before resize debounce wrote stable bytes: %q", got)
	}
	if m.nativeResizeRehydrateToken != token {
		t.Fatalf("resize token changed before debounce: got %d want %d", m.nativeResizeRehydrateToken, token)
	}

	next, _ = m.Update(nativeSurfaceResizeRehydrateMsg{token: token, width: 100, height: 30})
	m = next.(*uiModel)
	plain := stripANSIAndTrimRight(out.String())
	if strings.Contains(plain, "early return resize prompt") {
		t.Fatalf("debounce tick after return replayed already-emitted transcript, got %q", plain)
	}
}

func TestNativeSurfaceDoesNotWriteStableBytesWhileDetailSurfaceIsActive(t *testing.T) {
	var out bytes.Buffer
	m := newNativeSurfaceTestModel(&out)
	seedNativeSurfaceTranscript(m, []tui.TranscriptEntry{
		{Role: tui.TranscriptRoleUser, Text: "already emitted stable prompt", Committed: true},
	})
	if rendered := m.View(); rendered != "" {
		t.Fatalf("native ongoing View() returned %q, want empty renderer payload", rendered)
	}
	out.Reset()

	_ = m.transitionTranscriptModeWithOptions(transcriptModeTransitionOptions{
		target:            tui.ModeDetail,
		suppressAltScreen: true,
	})
	result := applyNativeSurfaceRuntimeEventForTest(t, m, clientui.Event{
		Kind:                       clientui.EventAssistantMessage,
		CommittedTranscriptChanged: true,
		TranscriptRevision:         2,
		CommittedEntryStart:        1,
		CommittedEntryStartSet:     true,
		TranscriptEntries:          []clientui.ChatEntry{{Role: "assistant", Text: "detail committed append", Phase: string(clientui.MessagePhaseFinal)}},
	})
	_ = collectCmdMessages(t, result.cmd)
	if got := out.String(); got != "" {
		t.Fatalf("native stable bytes leaked while detail surface was active: %q", got)
	}

	_ = m.transitionTranscriptModeWithOptions(transcriptModeTransitionOptions{
		target:            tui.ModeOngoing,
		suppressAltScreen: true,
	})
	if rendered := m.View(); rendered != "" {
		t.Fatalf("native ongoing View() returned %q after detail, want empty renderer payload", rendered)
	}
	plain := stripANSIAndTrimRight(out.String())
	if !strings.Contains(plain, "detail committed append") {
		t.Fatalf("return to ongoing did not flush buffered stable append, got %q", plain)
	}
	if strings.Contains(plain, "already emitted stable prompt") {
		t.Fatalf("return to ongoing rehydrated already-emitted stable transcript instead of flushing only buffered append, got %q", plain)
	}
}

func TestNativeSurfacePreparesNormalBufferBeforeFirstNativeWrite(t *testing.T) {
	var out bytes.Buffer
	rendererGate := newUIRendererOutputGateState()
	m := newSizedProjectedClosedUIModel(
		nil,
		120,
		30,
		WithUIRendererOutputGateState(rendererGate),
		WithUINativeSurfaceWriter(&out),
	)

	if rendered := m.View(); rendered != "" {
		t.Fatalf("native ongoing View() returned %q, want empty renderer payload", rendered)
	}
	raw := out.String()
	prepareSequence := xansi.ResetModeAltScreenSaveCursor + xansi.SaveCursor + "\x1b[?6l" + "\x1b[r" + xansi.RestoreCursor
	if !strings.HasPrefix(raw, prepareSequence) {
		t.Fatalf("native output prefix = %q, want normal-buffer preparation", raw)
	}
	if rendererGate.PhysicalAltScreenActive() {
		t.Fatal("renderer output gate still reports physical alt-screen active after native preparation")
	}
	liveOutput := strings.TrimPrefix(raw, prepareSequence)
	if strings.TrimSpace(xansi.Strip(liveOutput)) == "" {
		t.Fatalf("native live output missing after normal-buffer preparation: %q", liveOutput)
	}
}

func TestNativeSurfaceWaitsForKnownPhysicalAltScreenExit(t *testing.T) {
	var out bytes.Buffer
	rendererGate := newUIRendererOutputGateState()
	m := newSizedProjectedClosedUIModel(
		nil,
		120,
		30,
		WithUIRendererOutputGateState(rendererGate),
		WithUINativeSurfaceWriter(&out),
	)
	rendererGate.observeWrittenPayload([]byte(xansi.SetModeAltScreenSaveCursor))

	if rendered := m.View(); rendered != "" {
		t.Fatalf("native ongoing View() returned %q while physical alt-screen was active, want empty renderer payload", rendered)
	}
	if got := out.String(); got != "" {
		t.Fatalf("native live output was written before physical alt-screen exit: %q", got)
	}
	_, retryCmd := m.Update(nativeSurfaceResumeMsg{})
	if retryCmd == nil {
		t.Fatal("expected native surface resume to retry while physical alt-screen is active")
	}

	rendererGate.observeWrittenPayload([]byte(xansi.ResetModeAltScreenSaveCursor))
	next, resumeCmd := m.Update(nativeSurfaceResumeMsg{})
	m = next.(*uiModel)
	if resumeCmd != nil {
		t.Fatal("did not expect another retry after physical alt-screen exit")
	}
	if got := out.String(); got == "" {
		t.Fatal("expected native live output after physical alt-screen exit")
	}
}

func TestNativeAssistantUnknownPhaseStreamCommitsThroughSteer(t *testing.T) {
	var out bytes.Buffer
	m := newNativeSurfaceTestModel(&out)
	if rendered := m.View(); rendered != "" {
		t.Fatalf("native ongoing View() returned %q, want empty renderer payload", rendered)
	}
	out.Reset()

	unknown := applyNativeSurfaceRuntimeEventForTest(t, m, clientui.Event{
		Kind:           clientui.EventAssistantDelta,
		StepID:         "step-final",
		AssistantDelta: "hello ",
	})
	_ = collectCmdMessages(t, unknown.cmd)
	typed := applyNativeSurfaceRuntimeEventForTest(t, m, clientui.Event{
		Kind:                clientui.EventAssistantDelta,
		StepID:              "step-final",
		AssistantDelta:      "world",
		AssistantDeltaPhase: clientui.MessagePhaseFinal,
	})
	_ = collectCmdMessages(t, typed.cmd)
	if got := stripANSIAndTrimRight(out.String()); strings.Contains(got, "hello") || strings.Contains(got, "world") {
		t.Fatalf("incomplete phase stream should not write native stable chunks before commit, got %q", got)
	}

	committed := applyNativeSurfaceRuntimeEventForTest(t, m, clientui.Event{
		Kind:                       clientui.EventAssistantMessage,
		StepID:                     "step-final",
		CommittedTranscriptChanged: true,
		TranscriptRevision:         1,
		CommittedEntryStart:        0,
		CommittedEntryStartSet:     true,
		TranscriptEntries:          []clientui.ChatEntry{{Role: "assistant", Text: "hello world", Phase: string(clientui.MessagePhaseFinal)}},
	})
	_ = collectCmdMessages(t, committed.cmd)
	plain := stripANSIAndTrimRight(out.String())
	if count := strings.Count(plain, "hello world"); count != 1 {
		t.Fatalf("committed unknown-phase stream write count = %d, want 1 in %q", count, plain)
	}
}

func TestNativeAssistantDeltaBuffersWhileDetailSurfaceIsActive(t *testing.T) {
	var out bytes.Buffer
	m := newNativeSurfaceTestModel(&out)
	if rendered := m.View(); rendered != "" {
		t.Fatalf("native ongoing View() returned %q, want empty renderer payload", rendered)
	}
	out.Reset()

	_ = m.transitionTranscriptModeWithOptions(transcriptModeTransitionOptions{
		target:            tui.ModeDetail,
		suppressAltScreen: true,
	})
	away := applyNativeSurfaceRuntimeEventForTest(t, m, clientui.Event{
		Kind:                clientui.EventAssistantDelta,
		StepID:              "step-final",
		AssistantDelta:      "away ",
		AssistantDeltaPhase: clientui.MessagePhaseFinal,
	})
	_ = collectCmdMessages(t, away.cmd)
	if got := out.String(); got != "" {
		t.Fatalf("native assistant delta leaked while detail surface was active: %q", got)
	}

	_ = m.transitionTranscriptModeWithOptions(transcriptModeTransitionOptions{
		target:            tui.ModeOngoing,
		suppressAltScreen: true,
	})
	if rendered := m.View(); rendered != "" {
		t.Fatalf("native ongoing View() returned %q after detail, want empty renderer payload", rendered)
	}
	if got := stripANSIAndTrimRight(out.String()); !strings.Contains(got, "away") {
		t.Fatalf("return to ongoing did not flush held assistant stream chunk, got %q", got)
	}
	out.Reset()

	back := applyNativeSurfaceRuntimeEventForTest(t, m, clientui.Event{
		Kind:                clientui.EventAssistantDelta,
		StepID:              "step-final",
		AssistantDelta:      "back",
		AssistantDeltaPhase: clientui.MessagePhaseFinal,
	})
	_ = collectCmdMessages(t, back.cmd)
	if got := xansi.Strip(strings.Join(m.nativeSurface.AssistantStreamTailLines(), "")); !strings.Contains(got, "away back") {
		t.Fatalf("native stream tail after held detail chunk skipped assistant text: %q", got)
	}
	out.Reset()

	committed := applyNativeSurfaceRuntimeEventForTest(t, m, clientui.Event{
		Kind:                       clientui.EventAssistantMessage,
		StepID:                     "step-final",
		CommittedTranscriptChanged: true,
		TranscriptRevision:         1,
		CommittedEntryStart:        0,
		CommittedEntryStartSet:     true,
		TranscriptEntries:          []clientui.ChatEntry{{Role: "assistant", Text: "away back", Phase: string(clientui.MessagePhaseFinal)}},
	})
	_ = collectCmdMessages(t, committed.cmd)
	if m.nativeSurface.AssistantStreaming() {
		t.Fatal("expected native assistant stream to finish after committed final")
	}
	if count := strings.Count(stripANSIAndTrimRight(out.String()), "away back"); count != 1 {
		t.Fatalf("committed buffered detail stream should promote active tail once, got %d time(s), output=%q", count, stripANSIAndTrimRight(out.String()))
	}
}

func TestNativeAssistantStreamingErrorFinishesActiveStream(t *testing.T) {
	var out bytes.Buffer
	m := newNativeSurfaceTestModel(&out)
	if rendered := m.View(); rendered != "" {
		t.Fatalf("native ongoing View() returned %q, want empty renderer payload", rendered)
	}
	out.Reset()

	streamed := applyNativeSurfaceRuntimeEventForTest(t, m, clientui.Event{
		Kind:                clientui.EventAssistantDelta,
		StepID:              "step-final",
		AssistantDelta:      "partial",
		AssistantDeltaPhase: clientui.MessagePhaseFinal,
	})
	_ = collectCmdMessages(t, streamed.cmd)
	if !m.nativeSurface.AssistantStreaming() {
		t.Fatal("expected native assistant stream to be active after typed delta")
	}

	errored := applyNativeSurfaceRuntimeEventForTest(t, m, clientui.Event{Kind: clientui.EventStreamingErrorUpdated})
	_ = collectCmdMessages(t, errored.cmd)
	if m.nativeSurface.AssistantStreaming() {
		t.Fatal("expected streaming error update to finish native assistant stream")
	}
}

func TestNativeSurfaceStreamWriteErrorDoesNotMarkAssistantStreamActive(t *testing.T) {
	writeErr := errors.New("terminal closed")
	writer := nativeSurfaceFailingWriter{err: writeErr}
	surface := newUINativeSurface(writer, func() bool { return true }, nil)
	if !surface.ensure(80, 24) {
		t.Fatal("expected native surface to initialize")
	}

	err := surface.StreamAssistantFinalAnswerContent(strings.Repeat("x", 81) + "\nnext\n")
	if !errors.Is(err, writeErr) {
		t.Fatalf("stream error = %v, want %v", err, writeErr)
	}
	if surface.AssistantStreaming() {
		t.Fatal("failed first stream write marked native assistant stream active")
	}
	if err := surface.FinishAssistantStreaming(); err != nil {
		t.Fatalf("finish after failed first stream write returned error: %v", err)
	}
}

func TestNativeStableWriteErrorDisablesNativeSurface(t *testing.T) {
	writeErr := errors.New("terminal closed")
	writer := &nativeSurfaceScriptedWriter{errors: []error{nil, nil, writeErr}}
	m := newSizedProjectedClosedUIModel(nil, 120, 30, WithUINativeSurfaceWriter(writer))

	if rendered := m.View(); rendered != "" {
		t.Fatalf("native ongoing View() returned %q, want empty renderer payload", rendered)
	}
	streamed := applyNativeSurfaceRuntimeEventForTest(t, m, clientui.Event{
		Kind:                clientui.EventAssistantDelta,
		StepID:              "step-final",
		AssistantDelta:      strings.Repeat("x", 120) + "\n\nnext\n",
		AssistantDeltaPhase: clientui.MessagePhaseFinal,
	})
	_ = collectCmdMessages(t, streamed.cmd)
	committed := applyNativeSurfaceRuntimeEventForTest(t, m, clientui.Event{
		Kind:                       clientui.EventAssistantMessage,
		StepID:                     "step-final",
		CommittedTranscriptChanged: true,
		TranscriptRevision:         1,
		CommittedEntryCount:        1,
		CommittedEntryStart:        0,
		CommittedEntryStartSet:     true,
		TranscriptEntries: []clientui.ChatEntry{{
			Role:  "assistant",
			Text:  strings.Repeat("x", 120) + "\n\nnext\n",
			Phase: string(clientui.MessagePhaseFinal),
		}},
	})
	_ = collectCmdMessages(t, committed.cmd)

	if m.nativeLiveAreaError == nil {
		t.Fatal("expected native stable write error to be recorded")
	}
	if !errors.Is(m.nativeLiveAreaError, writeErr) {
		t.Fatalf("native stable write error = %v, want %v", m.nativeLiveAreaError, writeErr)
	}
	if m.nativeSurface != nil {
		t.Fatal("expected native stable write error to disable native surface")
	}
}

func TestNativeSurfaceCloseCleanupWriteErrorDoesNotRecurse(t *testing.T) {
	cleanupErr := errors.New("cleanup terminal closed")
	writer := &nativeSurfaceScriptedWriter{errors: []error{nil, nil, cleanupErr}}
	m := newSizedProjectedClosedUIModel(nil, 120, 30, WithUINativeSurfaceWriter(writer))
	if rendered := m.View(); rendered != "" {
		t.Fatalf("native ongoing View() returned %q, want empty renderer payload", rendered)
	}

	exitMainThread := m.enterUIMainThread("native surface cleanup failure test")
	m.closeNativeSurface()
	exitMainThread()

	if m.nativeSurface != nil {
		t.Fatal("expected cleanup write failure to leave native surface closed")
	}
	if m.nativeLiveAreaError == nil {
		t.Fatal("expected cleanup write failure to be recorded")
	}
	if !errors.Is(m.nativeLiveAreaError, cleanupErr) {
		t.Fatalf("cleanup write error = %v, want %v", m.nativeLiveAreaError, cleanupErr)
	}
	if writer.attempts != 3 {
		t.Fatalf("writer attempts = %d, want normal-buffer preparation, initial frame render, and one cleanup erase attempt", writer.attempts)
	}
}

func TestNativeLiveRenderErrorSurfacesInStatusLine(t *testing.T) {
	writeErr := errors.New("terminal closed")
	m := newSizedProjectedClosedUIModel(nil, 120, 30, WithUINativeSurfaceWriter(nativeSurfaceFailingWriter{err: writeErr}))

	rendered := m.View()
	if rendered == "" {
		t.Fatal("native live render failure returned empty payload instead of fallback renderer output")
	}
	if m.nativeLiveAreaError == nil {
		t.Fatal("expected failed native live render to record an error")
	}
	if m.nativeSurface != nil {
		t.Fatal("expected failed native live render to disable native surface for fallback rendering")
	}
	if plain := xansi.Strip(rendered); !strings.Contains(plain, "native terminal write failed") || !strings.Contains(plain, "terminal closed") {
		t.Fatalf("native live render error was not surfaced in fallback render, got %q", plain)
	}

	statusLine := m.layout().renderStatusLine(120, uiThemeStyles(m.theme))
	plain := xansi.Strip(statusLine)
	if !strings.Contains(plain, "native terminal write failed") || !strings.Contains(plain, "terminal closed") {
		t.Fatalf("native live render error was not surfaced in status line, got %q", plain)
	}
}

func TestNativeRecentTailHydrateDeliversRecoveredCommittedRows(t *testing.T) {
	var out bytes.Buffer
	m := newNativeSurfaceTestModel(&out)
	seedNativeSurfaceTranscript(m, []tui.TranscriptEntry{{Role: tui.TranscriptRoleUser, Text: "prompt", Committed: true}})
	if rendered := m.View(); rendered != "" {
		t.Fatalf("native ongoing View() returned %q, want empty renderer payload", rendered)
	}
	out.Reset()

	exitMainThread := m.enterUIMainThread("native recent-tail hydrate test")
	cmd := m.runtimeAdapter().applyRuntimeTranscriptPageWithRecovery(clientui.TranscriptPageRequest{}, clientui.TranscriptPage{
		SessionID:    "session-1",
		Revision:     2,
		NewerCursor:  2,
		HasMoreBelow: false,
		HasMoreAbove: false,
		OlderCursor:  0,
		Entries: []clientui.ChatEntry{
			{Role: "user", Text: "prompt"},
			{Role: "assistant", Text: "recovered answer", Phase: string(clientui.MessagePhaseFinal)},
		},
	}, clientui.TranscriptRecoveryCauseStreamGap)
	exitMainThread()
	_ = collectCmdMessages(t, cmd)

	plain := stripANSIAndTrimRight(out.String())
	if !strings.Contains(plain, "recovered answer") {
		t.Fatalf("native recent-tail hydrate did not deliver recovered row, got %q", plain)
	}
	if strings.Contains(plain, "prompt") {
		t.Fatalf("native recent-tail hydrate replayed already-emitted row, got %q", plain)
	}
}

func TestNativeTranscriptPageRecoveryPanicsForInsertedCommittedToolBeforeDeliveredTailInDebug(t *testing.T) {
	var out bytes.Buffer
	m := newSizedProjectedClosedUIModel(nil, 120, 30, WithUINativeSurfaceWriter(&out), WithUIDebug(true))
	previousClientEntries := nativeShellClientEntriesForTest("call-continue", "kent run --continue \"ab410dc6\" \"Fixed remaining\"", "continued")
	previousClientEntries = append(previousClientEntries, nativeShellClientEntriesForTest("call-poll", "Polled session 12283 for 5m0s", "polled")...)
	previousClientEntries = append(previousClientEntries, nativeShellClientEntriesForTest("call-status", "git status --short", "")...)
	previousEntries := make([]tui.TranscriptEntry, 0, len(previousClientEntries))
	for _, entry := range previousClientEntries {
		previousEntries = append(previousEntries, transcriptEntryFromProjectedChatEntry(entry, false, true))
	}
	seedNativeSurfaceTranscript(m, previousEntries)
	if rendered := m.View(); rendered != "" {
		t.Fatalf("native ongoing View() returned %q, want empty renderer payload", rendered)
	}
	m.nativeDeliveredStableProjection = m.nativeCommittedProjectionForEntries(previousEntries)
	out.Reset()

	recoveredEntries := append([]clientui.ChatEntry{}, nativeWidePatchSummaryClientEntriesForTest()...)
	recoveredEntries = append(recoveredEntries, previousClientEntries...)
	recoveredEntries = append(recoveredEntries, nativeShellClientEntriesForTest("call-complete", "kent task complete --commentary 'Revised plan'", "completed")...)
	panicText := captureNativeSurfacePanicText(t, func() {
		exitMainThread := m.enterUIMainThread("native recovery inserted committed tool app test")
		_ = m.runtimeAdapter().applyRuntimeTranscriptPageWithRecovery(clientui.TranscriptPageRequest{}, clientui.TranscriptPage{
			SessionID: "session-1",
			Revision:  2,
			Entries:   recoveredEntries,
		}, clientui.TranscriptRecoveryCauseStreamGap)
		exitMainThread()
	})
	if !strings.Contains(panicText, "Native scrollback invariant violation") ||
		!strings.Contains(panicText, nativeStableProjectionNonContiguousReason) ||
		!strings.Contains(panicText, "operation=deliverNativeStableProjectionChange") {
		t.Fatalf("panic = %q, want debug native stable invariant diagnostic", panicText)
	}
	plain := stripANSIAndTrimRight(out.String())
	if strings.Contains(plain, nativeWidePatchSummaryPathForTest) || strings.Contains(plain, "kent task complete") {
		t.Fatalf("non-contiguous recovery page wrote inserted committed rows into native scrollback: %q", plain)
	}
	if len(m.transcriptEntries) != len(recoveredEntries) {
		t.Fatalf("transcript entry count = %d, want recovered page count %d", len(m.transcriptEntries), len(recoveredEntries))
	}
	if !strings.Contains(m.transcriptEntries[0].Text, nativeWidePatchSummaryPathForTest) {
		t.Fatalf("authoritative transcript was not updated from recovery page, first entry = %#v", m.transcriptEntries[0])
	}
}

func TestNativeCompleteStreamFinalizerDeliversMergedCommittedRows(t *testing.T) {
	var out bytes.Buffer
	m := newNativeSurfaceTestModel(&out)
	seedNativeSurfaceTranscript(m, []tui.TranscriptEntry{{Role: tui.TranscriptRoleUser, Text: "prompt", Committed: true}})
	if rendered := m.View(); rendered != "" {
		t.Fatalf("native ongoing View() returned %q, want empty renderer payload", rendered)
	}
	out.Reset()

	streamed := applyNativeSurfaceRuntimeEventForTest(t, m, clientui.Event{
		Kind:                clientui.EventAssistantDelta,
		StepID:              "step-final",
		AssistantDelta:      "done",
		AssistantDeltaPhase: clientui.MessagePhaseFinal,
	})
	_ = collectCmdMessages(t, streamed.cmd)
	if m.nativeSurface == nil || !m.nativeSurface.AssistantStreaming() {
		t.Fatal("expected native assistant stream to be active after typed delta")
	}
	finalized := applyNativeSurfaceRuntimeEventForTest(t, m, clientui.Event{
		Kind:                       clientui.EventAssistantMessage,
		CommittedTranscriptChanged: true,
		TranscriptRevision:         2,
		CommittedEntryStart:        1,
		CommittedEntryStartSet:     true,
		TranscriptEntries: []clientui.ChatEntry{
			{Role: "assistant", Text: "done", Phase: string(clientui.MessagePhaseFinal)},
			{Role: "user", Text: "next prompt"},
		},
	})
	_ = collectCmdMessages(t, finalized.cmd)

	plain := stripANSIAndTrimRight(out.String())
	if got := strings.Count(plain, "done"); got != 1 {
		t.Fatalf("native complete stream finalizer wrote assistant text %d times, want once; output=%q", got, plain)
	}
	if !strings.Contains(plain, "next prompt") {
		t.Fatalf("native complete stream finalizer skipped merged committed row, got %q", plain)
	}
}

func TestNativeStableProjectionChangeAppendsAfterOverlappingRecentTail(t *testing.T) {
	var out bytes.Buffer
	m := newNativeSurfaceTestModel(&out)
	if rendered := m.View(); rendered != "" {
		t.Fatalf("native ongoing View() returned %q, want empty renderer payload", rendered)
	}
	out.Reset()

	previous := nativeStableProjectionForTest("old-a", "old-b")
	current := nativeStableProjectionForTest("old-b", "new-c", "new-d")
	exitMainThread := m.enterUIMainThread("native stable overlap test")
	err := m.deliverNativeStableProjectionChange(nativeStableLiveAppendIntent("deliverNativeStableProjectionChange"), previous, current, true, false, false, "")
	exitMainThread()
	if err != nil {
		t.Fatalf("overlapping projection delivery returned error: %v", err)
	}

	plain := stripANSIAndTrimRight(out.String())
	if strings.Contains(plain, "old-b") {
		t.Fatalf("overlapping projection delivery replayed overlap block, got %q", plain)
	}
	if !strings.Contains(plain, "new-c") || !strings.Contains(plain, "new-d") {
		t.Fatalf("overlapping projection delivery skipped new tail blocks, got %q", plain)
	}
}

func TestNativeStableProjectionChangeAppendsRepeatedBlockAfterOverlappingRecentTail(t *testing.T) {
	var out bytes.Buffer
	m := newNativeSurfaceTestModel(&out)
	if rendered := m.View(); rendered != "" {
		t.Fatalf("native ongoing View() returned %q, want empty renderer payload", rendered)
	}
	out.Reset()

	previous := tui.TranscriptProjection{Blocks: []tui.TranscriptProjectionBlock{
		{
			Role:         tui.RenderIntentUser,
			DividerGroup: string(tui.RenderIntentUser),
			SourceKey:    "user-1",
			Lines:        []string{"repeat prompt"},
		},
		{
			Role:         tui.RenderIntentAssistant,
			DividerGroup: string(tui.RenderIntentAssistant),
			SourceKey:    "assistant-1",
			Lines:        []string{"overlap answer"},
		},
	}}
	current := tui.TranscriptProjection{Blocks: []tui.TranscriptProjectionBlock{
		previous.Blocks[1],
		{
			Role:         tui.RenderIntentUser,
			DividerGroup: string(tui.RenderIntentUser),
			SourceKey:    "user-2",
			Lines:        []string{"repeat prompt"},
		},
	}}
	exitMainThread := m.enterUIMainThread("native stable repeated overlap test")
	err := m.deliverNativeStableProjectionChange(nativeStableRecoveryReconcileIntent("deliverNativeStableProjectionChange"), previous, current, true, false, false, "")
	exitMainThread()
	if err != nil {
		t.Fatalf("repeated block after overlap returned error: %v", err)
	}
	plain := stripANSIAndTrimRight(out.String())
	if strings.Contains(plain, "overlap answer") {
		t.Fatalf("overlap append replayed overlapping block, got %q", plain)
	}
	if !strings.Contains(plain, "repeat prompt") {
		t.Fatalf("overlap append skipped repeated new block, got %q", plain)
	}
}

func TestNativeStableProjectionChangeAppendsCompactionResetAsPhysicalEpoch(t *testing.T) {
	var out bytes.Buffer
	m := newNativeSurfaceTestModel(&out)
	if rendered := m.View(); rendered != "" {
		t.Fatalf("native ongoing View() returned %q, want empty renderer payload", rendered)
	}
	out.Reset()

	previous := nativeStableProjectionForTest("old-a", "old-b")
	compaction := nativeStableProjectionForTest("context compacted")
	compaction.Blocks[0].Role = tui.RenderIntentCompactionSummary
	compaction.Blocks[0].DividerGroup = string(tui.RenderIntentCompactionSummary)
	exitMainThread := m.enterUIMainThread("native stable compaction reset test")
	err := m.deliverNativeStableProjectionChange(nativeStableLiveAppendIntent("deliverNativeStableProjectionChange"), previous, compaction, true, false, false, "")
	exitMainThread()
	if err != nil {
		t.Fatalf("compaction reset delivery returned error: %v", err)
	}

	plain := stripANSIAndTrimRight(out.String())
	if strings.Contains(plain, "old-a") || strings.Contains(plain, "old-b") {
		t.Fatalf("compaction reset delivery replayed pre-compaction rows, got %q", plain)
	}
	if !strings.Contains(plain, "context compacted") {
		t.Fatalf("compaction reset delivery skipped compaction marker, got %q", plain)
	}
	if got := len(m.nativeDeliveredStableProjection.Blocks); got != 3 {
		t.Fatalf("delivered native ledger block count = %d, want old history plus compaction marker", got)
	}

	out.Reset()
	current := compaction.Clone()
	current.Blocks = append(current.Blocks, nativeStableProjectionForTest("post-compaction user").Blocks[0])
	exitMainThread = m.enterUIMainThread("native stable post compaction append test")
	err = m.deliverNativeStableProjectionChange(nativeStableLiveAppendIntent("deliverNativeStableProjectionChange"), m.nativeDeliveredStableProjection, current, true, false, false, "")
	exitMainThread()
	if err != nil {
		t.Fatalf("post-compaction append delivery returned error: %v", err)
	}
	plain = stripANSIAndTrimRight(out.String())
	if strings.Contains(plain, "context compacted") {
		t.Fatalf("post-compaction append replayed compaction marker, got %q", plain)
	}
	if !strings.Contains(plain, "post-compaction user") {
		t.Fatalf("post-compaction append skipped new row, got %q", plain)
	}
}

func TestNativeSurfaceResizeReprojectsDeliveredCompactionEpochSuffix(t *testing.T) {
	m := newProjectedClosedUIModel(nil)
	oldEpoch := tui.TranscriptProjectionBlock{
		Role:         tui.RenderIntentCompactionSummary,
		DividerGroup: string(tui.RenderIntentCompactionSummary),
		SourceKey:    "compaction-epoch-1",
		Lines:        []string{"compaction marker before resize"},
	}
	current := tui.TranscriptProjection{Blocks: []tui.TranscriptProjectionBlock{{
		Role:         tui.RenderIntentCompactionSummary,
		DividerGroup: string(tui.RenderIntentCompactionSummary),
		SourceKey:    "compaction-epoch-1",
		Lines:        []string{"compaction marker", "after resize"},
	}}}
	oldPhysical := nativeStableProjectionForTest("pre-compaction physical row")
	oldPhysical.Blocks = append(oldPhysical.Blocks, oldEpoch)
	m.nativeDeliveredStableProjection = oldPhysical

	reprojected, ok := m.reprojectNativeDeliveredStableProjectionSuffixPrefix(current)
	if !ok {
		t.Fatal("compaction epoch suffix was not reprojected")
	}
	appendBlocks, ok := m.nativeStableAppendBlocksForProjectionChange(nativeStableLiveAppendIntent("test"), reprojected, current)
	if !ok {
		t.Fatal("reprojected compaction epoch suffix did not reconcile")
	}
	if len(appendBlocks) != 0 {
		t.Fatalf("reprojected compaction epoch wanted append blocks %v, want none", appendBlocks)
	}
}

func TestNativeProjectionSourceKeyIgnoresHydrationVolatileFlags(t *testing.T) {
	block := tui.TranscriptProjectionBlock{EntryIndex: 0, EntryEnd: 0}
	live := []tui.TranscriptEntry{{
		Role:      tui.TranscriptRoleAssistant,
		Text:      "same durable answer",
		Committed: true,
	}}
	hydrated := []tui.TranscriptEntry{{
		Role:      tui.TranscriptRoleAssistant,
		Text:      "same durable answer",
		Transient: true,
	}}

	liveKey := nativeProjectionBlockSourceKey(block, live, 0)
	hydratedKey := nativeProjectionBlockSourceKey(block, hydrated, 0)
	if liveKey != hydratedKey {
		t.Fatalf("source key changed across hydration flags:\nlive=%q\nhydrated=%q", liveKey, hydratedKey)
	}
}

func TestNativeProjectionSourceKeyIncludesCommittedPosition(t *testing.T) {
	first := tui.TranscriptProjectionBlock{EntryIndex: 10, EntryEnd: 10}
	second := tui.TranscriptProjectionBlock{EntryIndex: 11, EntryEnd: 11}
	entries := []tui.TranscriptEntry{
		{Role: tui.TranscriptRoleAssistant, Text: "same answer", Committed: true},
		{Role: tui.TranscriptRoleAssistant, Text: "same answer", Committed: true},
	}

	firstKey := nativeProjectionBlockSourceKey(first, entries, 10)
	secondKey := nativeProjectionBlockSourceKey(second, entries, 10)
	if firstKey == secondKey {
		t.Fatalf("duplicate committed rows produced identical source key %q", firstKey)
	}
}

func TestNativeProjectionLocalAppendOnlyRequiresEventProvenance(t *testing.T) {
	systemEntry := clientui.ChatEntry{Role: "system", Text: "background notice"}
	hydrated := transcriptEntryFromProjectedChatEntry(systemEntry, false, false)
	if hydrated.LocalAppendOnly {
		t.Fatal("hydrated authoritative system row was marked local append-only")
	}
	local := transcriptEntryFromProjectedEventEntry(clientui.Event{Kind: clientui.EventBackgroundUpdated}, systemEntry, false, false)
	if !local.LocalAppendOnly {
		t.Fatal("background event system row was not marked local append-only")
	}
	committedSystem := transcriptEntryFromProjectedEventEntry(clientui.Event{Kind: clientui.EventAssistantMessage}, systemEntry, false, true)
	if committedSystem.LocalAppendOnly {
		t.Fatal("committed system row from authoritative event was marked local append-only")
	}
	committedCacheWarning := transcriptEntryFromProjectedEventEntry(clientui.Event{Kind: clientui.EventCacheWarning, CommittedTranscriptChanged: true}, clientui.ChatEntry{Role: "cache_warning", Text: "cache warning"}, false, true)
	if committedCacheWarning.LocalAppendOnly {
		t.Fatal("committed cache warning event was marked local append-only")
	}
	localCacheWarning := transcriptEntryFromProjectedEventEntry(clientui.Event{Kind: clientui.EventCacheWarning}, clientui.ChatEntry{Role: "cache_warning", Text: "cache warning"}, false, false)
	if !localCacheWarning.LocalAppendOnly {
		t.Fatal("uncommitted cache warning event was not marked local append-only")
	}
}

func TestNativeCommittedProjectionUsesAppTailBaseOffsetWhileDetailViewIsOpen(t *testing.T) {
	m := newSizedProjectedClosedUIModel(nil, 120, 30)
	m.transcriptBaseOffset = 200
	m.transcriptEntries = []tui.TranscriptEntry{{
		Role:      tui.TranscriptRoleUser,
		Text:      "tail prompt",
		Committed: true,
	}}
	m.transcriptTotalEntries = 201
	m.forwardToView(tui.SetConversationMsg{
		BaseOffset:   10,
		TotalEntries: 201,
		Entries: []tui.TranscriptEntry{{
			Role:      tui.TranscriptRoleUser,
			Text:      "detail prompt",
			Committed: true,
		}},
	})

	projection := m.nativeCommittedProjectionForEntries(m.transcriptEntries)
	if len(projection.Blocks) != 1 {
		t.Fatalf("projection block count = %d, want 1", len(projection.Blocks))
	}
	if got := projection.Blocks[0].EntryIndex; got != 200 {
		t.Fatalf("native projection entry index = %d, want app tail offset 200", got)
	}
	if projection.Blocks[0].SourceKey == nativeProjectionBlockLineSourceKey(projection.Blocks[0]) {
		t.Fatalf("native projection fell back to line source key instead of tail entry payload key: %#v", projection.Blocks[0])
	}
}

func TestNativeStableProjectionChangeRejectsCorrectionAfterDeliveredCompactionMarker(t *testing.T) {
	var out bytes.Buffer
	m := newNativeSurfaceTestModel(&out)
	if rendered := m.View(); rendered != "" {
		t.Fatalf("native ongoing View() returned %q, want empty renderer payload", rendered)
	}
	out.Reset()

	preCompaction := nativeStableProjectionForTest("old-a", "old-b")
	compaction := nativeStableProjectionForTest("context compacted")
	compaction.Blocks[0].Role = tui.RenderIntentCompactionSummary
	compaction.Blocks[0].DividerGroup = string(tui.RenderIntentCompactionSummary)
	delivered := nativeStableProjectionWithAppendedBlocks(preCompaction, compaction, []int{0})
	originalPostCompaction := nativeStableProjectionForTest("original post-compaction row")
	delivered.Blocks = append(delivered.Blocks, originalPostCompaction.Blocks[0])
	current := compaction.Clone()
	current.Blocks = append(current.Blocks, nativeStableProjectionForTest("corrected post-compaction row").Blocks[0])

	exitMainThread := m.enterUIMainThread("native stable post-compaction correction test")
	err := m.deliverNativeStableProjectionChange(nativeStableLiveAppendIntent("deliverNativeStableProjectionChange"), delivered, current, true, false, false, "")
	exitMainThread()
	if err == nil {
		t.Fatal("expected correction after delivered compaction marker to be rejected")
	}
	if got := out.String(); got != "" {
		t.Fatalf("post-compaction correction wrote native bytes: %q", got)
	}
}

func TestNativeStableProjectionChangeRejectsAuthoritativeTailAfterPriorReviewerStatus(t *testing.T) {
	var out bytes.Buffer
	m := newNativeSurfaceTestModel(&out)
	if rendered := m.View(); rendered != "" {
		t.Fatalf("native ongoing View() returned %q, want empty renderer payload", rendered)
	}
	out.Reset()

	previous := nativeStableProjectionForTest("old-a", "local status")
	previous.Blocks[1].Role = tui.RenderIntentReviewerStatus
	previous.Blocks[1].DividerGroup = string(tui.RenderIntentReviewerStatus)
	current := nativeStableProjectionForTest("old-a", "new user prompt")
	current.Blocks[1].Role = tui.RenderIntentUser
	current.Blocks[1].DividerGroup = string(tui.RenderIntentUser)
	exitMainThread := m.enterUIMainThread("native stable local status append test")
	err := m.deliverNativeStableProjectionChange(nativeStableLiveAppendIntent("deliverNativeStableProjectionChange"), previous, current, true, false, false, "")
	exitMainThread()
	if err == nil {
		t.Fatal("expected prior reviewer-status suffix to be rejected as non-appendable")
	}

	plain := stripANSIAndTrimRight(out.String())
	if strings.Contains(plain, "new user prompt") {
		t.Fatalf("prior reviewer-status rejection wrote authoritative suffix, got %q", plain)
	}
}

func TestNativeStableProjectionChangeAppendsBehindPriorLocalReviewerStatus(t *testing.T) {
	var out bytes.Buffer
	m := newNativeSurfaceTestModel(&out)
	if rendered := m.View(); rendered != "" {
		t.Fatalf("native ongoing View() returned %q, want empty renderer payload", rendered)
	}
	out.Reset()

	previous := nativeStableProjectionForTest("final answer", "reviewer diagnostic")
	previous.Blocks[0].Role = tui.RenderIntentAssistant
	previous.Blocks[0].DividerGroup = string(tui.RenderIntentAssistant)
	previous.Blocks[1].Role = tui.RenderIntentReviewerStatus
	previous.Blocks[1].DividerGroup = string(tui.RenderIntentReviewerStatus)
	previous.Blocks[1].LocalAppendOnly = true
	current := nativeStableProjectionForTest("final answer", "next user prompt")
	current.Blocks[0].Role = tui.RenderIntentAssistant
	current.Blocks[0].DividerGroup = string(tui.RenderIntentAssistant)
	current.Blocks[1].Role = tui.RenderIntentUser
	current.Blocks[1].DividerGroup = string(tui.RenderIntentUser)

	exitMainThread := m.enterUIMainThread("native stable local reviewer suffix append test")
	err := m.deliverNativeStableProjectionChange(nativeStableLiveAppendIntent("deliverNativeStableProjectionChange"), previous, current, true, false, false, "")
	exitMainThread()
	if err != nil {
		t.Fatalf("local reviewer-status suffix delivery returned error: %v", err)
	}
	plain := stripANSIAndTrimRight(out.String())
	if strings.Contains(plain, "reviewer diagnostic") || strings.Contains(plain, "final answer") {
		t.Fatalf("local reviewer-status suffix delivery replayed delivered rows, got %q", plain)
	}
	if !strings.Contains(plain, "next user prompt") {
		t.Fatalf("local reviewer-status suffix delivery skipped user row, got %q", plain)
	}
	if m.nativeStableProjectionNeedsDelivery(nativeStableLiveAppendIntent("test"), m.nativeDeliveredStableProjection, current) {
		t.Fatal("logical current projection should reconcile after appending user behind local reviewer status")
	}
}

func TestNativeStableProjectionChangeAppendsDuplicateLocalNoticeWithDistinctSource(t *testing.T) {
	var out bytes.Buffer
	m := newNativeSurfaceTestModel(&out)
	if rendered := m.View(); rendered != "" {
		t.Fatalf("native ongoing View() returned %q, want empty renderer payload", rendered)
	}
	out.Reset()

	previous := tui.TranscriptProjection{Blocks: []tui.TranscriptProjectionBlock{
		{
			Role:         tui.RenderIntentUser,
			DividerGroup: string(tui.RenderIntentUser),
			SourceKey:    "user-1",
			Lines:        []string{"prompt"},
		},
		{
			Role:            tui.RenderIntentSystem,
			DividerGroup:    string(tui.RenderIntentSystem),
			SourceKey:       "notice-1",
			LocalAppendOnly: true,
			Lines:           []string{"Background shell completed"},
		},
		{
			Role:         tui.RenderIntentAssistant,
			DividerGroup: string(tui.RenderIntentAssistant),
			SourceKey:    "assistant-1",
			Lines:        []string{"answer"},
		},
	}}
	current := tui.TranscriptProjection{Blocks: []tui.TranscriptProjectionBlock{
		previous.Blocks[0],
		{
			Role:            tui.RenderIntentSystem,
			DividerGroup:    string(tui.RenderIntentSystem),
			SourceKey:       "notice-2",
			LocalAppendOnly: true,
			Lines:           []string{"Background shell completed"},
		},
		previous.Blocks[2],
	}}

	exitMainThread := m.enterUIMainThread("native duplicate local notice append test")
	err := m.deliverNativeStableProjectionChange(nativeStableLiveAppendIntent("deliverNativeStableProjectionChange"), previous, current, true, false, false, "")
	exitMainThread()
	if err != nil {
		t.Fatalf("duplicate local notice delivery returned error: %v", err)
	}
	plain := stripANSIAndTrimRight(out.String())
	if !strings.Contains(plain, "Background shell completed") {
		t.Fatalf("duplicate local notice was treated as already delivered, got %q", plain)
	}
}

func TestNativeStableProjectionChangeRejectsAuthoritativePrefixBehindPriorToolPatchSuccess(t *testing.T) {
	var out bytes.Buffer
	m := newNativeSurfaceTestModel(&out)
	if rendered := m.View(); rendered != "" {
		t.Fatalf("native ongoing View() returned %q, want empty renderer payload", rendered)
	}
	out.Reset()

	current := nativeStableProjectionForTest("old-a", "commentary")
	current.Blocks[1].Role = tui.RenderIntentAssistantCommentary
	current.Blocks[1].DividerGroup = string(tui.RenderIntentAssistant)
	previous := current.Clone()
	previous.Blocks = append(previous.Blocks, tui.TranscriptProjectionBlock{
		Role:         tui.RenderIntentToolPatchSuccess,
		DividerGroup: string(tui.RenderIntentTool),
		Lines:        []string{"⇄ ./cli/tui/transcript_projection.go +10"},
	})
	exitMainThread := m.enterUIMainThread("native stable local patch prefix test")
	err := m.deliverNativeStableProjectionChange(nativeStableLiveAppendIntent("deliverNativeStableProjectionChange"), previous, current, true, false, false, "")
	exitMainThread()
	if err == nil {
		t.Fatal("expected prior tool patch row removal to be rejected as non-appendable")
	}
	if got := out.String(); got != "" {
		t.Fatalf("prior tool patch rejection wrote native bytes: %q", got)
	}
}

func TestNativeStableProjectionChangeRejectsCommittedSystemRewrite(t *testing.T) {
	var out bytes.Buffer
	m := newNativeSurfaceTestModel(&out)
	if rendered := m.View(); rendered != "" {
		t.Fatalf("native ongoing View() returned %q, want empty renderer payload", rendered)
	}
	out.Reset()

	previous := tui.TranscriptProjection{Blocks: []tui.TranscriptProjectionBlock{
		{
			Role:         tui.RenderIntentUser,
			DividerGroup: string(tui.RenderIntentUser),
			SourceKey:    "user-1",
			Lines:        []string{"prompt"},
		},
		{
			Role:         tui.RenderIntentSystem,
			DividerGroup: string(tui.RenderIntentSystem),
			SourceKey:    "committed-system-1",
			Lines:        []string{"committed system row"},
		},
		{
			Role:         tui.RenderIntentAssistant,
			DividerGroup: string(tui.RenderIntentAssistant),
			SourceKey:    "assistant-1",
			Lines:        []string{"answer"},
		},
	}}
	current := tui.TranscriptProjection{Blocks: []tui.TranscriptProjectionBlock{
		previous.Blocks[0],
		{
			Role:         tui.RenderIntentAssistant,
			DividerGroup: string(tui.RenderIntentAssistant),
			SourceKey:    "assistant-2",
			Lines:        []string{"inserted answer"},
		},
		previous.Blocks[1],
		previous.Blocks[2],
	}}

	exitMainThread := m.enterUIMainThread("native committed system rewrite test")
	err := m.deliverNativeStableProjectionChange(nativeStableRecoveryReconcileIntent("deliverNativeStableProjectionChange"), previous, current, true, false, false, "")
	exitMainThread()
	if err == nil {
		t.Fatal("expected committed system reorder to be rejected")
	}
	if plain := stripANSIAndTrimRight(out.String()); strings.Contains(plain, "inserted answer") {
		t.Fatalf("committed system rewrite wrote inserted row through native stable path: %q", plain)
	}
}

func TestNativeStableProjectionChangeIgnoresTextBeyondEmittedWidth(t *testing.T) {
	var out bytes.Buffer
	m := newSizedProjectedClosedUIModel(nil, 12, 30, WithUINativeSurfaceWriter(&out), WithUIDebug(true))
	if rendered := m.View(); rendered != "" {
		t.Fatalf("native ongoing View() returned %q, want empty renderer payload", rendered)
	}
	out.Reset()

	previous := nativeStableProjectionForTest("same-prefix-before-old")
	current := nativeStableProjectionForTest("same-prefix-before-new")
	exitMainThread := m.enterUIMainThread("native stable truncated equality test")
	err := m.deliverNativeStableProjectionChange(nativeStableLiveAppendIntent("deliverNativeStableProjectionChange"), previous, current, true, false, false, "")
	exitMainThread()
	if err != nil {
		t.Fatalf("truncated-equivalent projection delivery returned error: %v", err)
	}
	if got := out.String(); got != "" {
		t.Fatalf("truncated-equivalent projection wrote native bytes: %q", got)
	}
}

func TestNativeStableProjectionChangeRejectsInsertedLocalStatusBeforeEmittedRows(t *testing.T) {
	var out bytes.Buffer
	m := newNativeSurfaceTestModel(&out)
	if rendered := m.View(); rendered != "" {
		t.Fatalf("native ongoing View() returned %q, want empty renderer payload", rendered)
	}
	out.Reset()

	previous := nativeStableProjectionForTest("assistant answer", "user crash report", "commentary")
	previous.Blocks[0].Role = tui.RenderIntentAssistant
	previous.Blocks[0].DividerGroup = string(tui.RenderIntentAssistant)
	previous.Blocks[1].Role = tui.RenderIntentUser
	previous.Blocks[1].DividerGroup = string(tui.RenderIntentUser)
	previous.Blocks[2].Role = tui.RenderIntentAssistantCommentary
	previous.Blocks[2].DividerGroup = string(tui.RenderIntentAssistant)
	current := tui.TranscriptProjection{Blocks: []tui.TranscriptProjectionBlock{
		previous.Blocks[0],
		{
			Role:         tui.RenderIntentReviewerStatus,
			DividerGroup: string(tui.RenderIntentReviewerStatus),
			Lines:        []string{"§ Supervisor ran: 2 suggestions, applied."},
		},
		previous.Blocks[1],
		previous.Blocks[2],
	}}

	exitMainThread := m.enterUIMainThread("native stable inserted local status test")
	err := m.deliverNativeStableProjectionChange(nativeStableLiveAppendIntent("deliverNativeStableProjectionChange"), previous, current, true, false, false, "")
	exitMainThread()
	if err == nil {
		t.Fatal("expected inserted local-status before emitted rows to be rejected")
	}

	plain := stripANSIAndTrimRight(out.String())
	if strings.Contains(plain, "user crash report") || strings.Contains(plain, "commentary") {
		t.Fatalf("inserted local-status delivery replayed already emitted blocks, got %q", plain)
	}
	if strings.Contains(plain, "Supervisor ran") {
		t.Fatalf("inserted local-status delivery wrote out-of-order local status, got %q", plain)
	}
}

func TestNativeStableProjectionChangeAppendsInsertedSystemNoticeAtPhysicalTail(t *testing.T) {
	var out bytes.Buffer
	m := newNativeSurfaceTestModel(&out)
	if rendered := m.View(); rendered != "" {
		t.Fatalf("native ongoing View() returned %q, want empty renderer payload", rendered)
	}
	out.Reset()

	previous := nativeStableProjectionForTest("shell command", "poll result", "commentary")
	previous.Blocks[0].Role = tui.RenderIntentToolShellSuccess
	previous.Blocks[0].DividerGroup = string(tui.RenderIntentTool)
	previous.Blocks[1].Role = tui.RenderIntentToolShellSuccess
	previous.Blocks[1].DividerGroup = string(tui.RenderIntentTool)
	previous.Blocks[2].Role = tui.RenderIntentAssistantCommentary
	previous.Blocks[2].DividerGroup = string(tui.RenderIntentAssistant)
	systemNotice := tui.TranscriptProjectionBlock{
		Role:         tui.RenderIntentSystem,
		DividerGroup: string(tui.RenderIntentSystem),
		Lines:        []string{"Background shell 7645 completed (exit 0)"},
	}
	current := tui.TranscriptProjection{Blocks: []tui.TranscriptProjectionBlock{
		previous.Blocks[0],
		previous.Blocks[1],
		systemNotice,
		previous.Blocks[2],
	}}

	exitMainThread := m.enterUIMainThread("native stable inserted system notice test")
	err := m.deliverNativeStableProjectionChange(nativeStableLiveAppendIntent("deliverNativeStableProjectionChange"), previous, current, true, false, false, "")
	exitMainThread()
	if err != nil {
		t.Fatalf("inserted system notice delivery returned error: %v", err)
	}
	plain := stripANSIAndTrimRight(out.String())
	if strings.Contains(plain, "shell command") || strings.Contains(plain, "poll result") || strings.Contains(plain, "commentary") {
		t.Fatalf("inserted system notice delivery replayed already emitted blocks, got %q", plain)
	}
	if !strings.Contains(plain, "Background shell 7645 completed") {
		t.Fatalf("inserted system notice delivery skipped local notice, got %q", plain)
	}

	out.Reset()
	next := current.Clone()
	next.Blocks = append(next.Blocks, tui.TranscriptProjectionBlock{
		Role:         tui.RenderIntentUser,
		DividerGroup: string(tui.RenderIntentUser),
		Lines:        []string{"next prompt"},
	})
	exitMainThread = m.enterUIMainThread("native stable after inserted system notice test")
	err = m.deliverNativeStableProjectionChange(nativeStableLiveAppendIntent("deliverNativeStableProjectionChange"), m.nativeDeliveredStableProjection, next, true, false, false, "")
	exitMainThread()
	if err != nil {
		t.Fatalf("post-system-notice delivery returned error: %v", err)
	}
	plain = stripANSIAndTrimRight(out.String())
	if strings.Contains(plain, "Background shell 7645 completed") || strings.Contains(plain, "commentary") {
		t.Fatalf("post-system-notice delivery replayed reordered local or old row, got %q", plain)
	}
	if !strings.Contains(plain, "next prompt") {
		t.Fatalf("post-system-notice delivery skipped new prompt, got %q", plain)
	}
}

func TestNativeStableProjectionChangeSkipsPriorLocalSuffixWhenAuthoritativeRowsArriveBeforeIt(t *testing.T) {
	var out bytes.Buffer
	m := newNativeSurfaceTestModel(&out)
	if rendered := m.View(); rendered != "" {
		t.Fatalf("native ongoing View() returned %q, want empty renderer payload", rendered)
	}
	out.Reset()

	systemNotice := tui.TranscriptProjectionBlock{
		Role:         tui.RenderIntentSystem,
		DividerGroup: string(tui.RenderIntentSystem),
		Lines:        []string{"Background shell 9911 completed (exit 0)"},
	}
	previous := nativeStableProjectionForTest("prompt")
	previous.Blocks[0].Role = tui.RenderIntentUser
	previous.Blocks[0].DividerGroup = string(tui.RenderIntentUser)
	previous.Blocks = append(previous.Blocks, systemNotice)
	authoritative := nativeStableProjectionForTest("committed answer")
	authoritative.Blocks[0].Role = tui.RenderIntentAssistant
	authoritative.Blocks[0].DividerGroup = string(tui.RenderIntentAssistant)
	current := tui.TranscriptProjection{Blocks: []tui.TranscriptProjectionBlock{
		previous.Blocks[0],
		authoritative.Blocks[0],
		systemNotice,
	}}

	exitMainThread := m.enterUIMainThread("native stable local suffix before authoritative row test")
	err := m.deliverNativeStableProjectionChange(nativeStableLiveAppendIntent("deliverNativeStableProjectionChange"), previous, current, true, false, false, "")
	exitMainThread()
	if err != nil {
		t.Fatalf("local suffix reconciliation returned error: %v", err)
	}
	plain := stripANSIAndTrimRight(out.String())
	if strings.Contains(plain, "Background shell 9911") || strings.Contains(plain, "prompt") {
		t.Fatalf("local suffix reconciliation replayed delivered rows, got %q", plain)
	}
	if !strings.Contains(plain, "committed answer") {
		t.Fatalf("local suffix reconciliation skipped authoritative row, got %q", plain)
	}
	if m.nativeStableProjectionNeedsDelivery(nativeStableLiveAppendIntent("test"), m.nativeDeliveredStableProjection, current) {
		t.Fatal("logical current projection should reconcile after appending authoritative row behind local suffix")
	}
}

func TestNativeStableProjectionChangeAppendsInsertedLocalCacheWarningAtPhysicalTail(t *testing.T) {
	var out bytes.Buffer
	m := newNativeSurfaceTestModel(&out)
	if rendered := m.View(); rendered != "" {
		t.Fatalf("native ongoing View() returned %q, want empty renderer payload", rendered)
	}
	out.Reset()

	previous := nativeStableProjectionForTest("answer commentary")
	previous.Blocks[0].Role = tui.RenderIntentAssistantCommentary
	previous.Blocks[0].DividerGroup = string(tui.RenderIntentAssistant)
	cacheWarning := tui.TranscriptProjectionBlock{
		Role:            tui.RenderIntentCacheWarning,
		DividerGroup:    string(tui.RenderIntentCacheWarning),
		LocalAppendOnly: true,
		Lines:           []string{"Cache miss: request was not a postfix"},
	}
	current := tui.TranscriptProjection{Blocks: []tui.TranscriptProjectionBlock{
		cacheWarning,
		previous.Blocks[0],
	}}

	exitMainThread := m.enterUIMainThread("native stable inserted local cache warning test")
	err := m.deliverNativeStableProjectionChange(nativeStableLiveAppendIntent("deliverNativeStableProjectionChange"), previous, current, true, false, false, "")
	exitMainThread()
	if err != nil {
		t.Fatalf("inserted local cache warning delivery returned error: %v", err)
	}
	plain := stripANSIAndTrimRight(out.String())
	if strings.Contains(plain, "answer commentary") {
		t.Fatalf("inserted local cache warning replayed already emitted assistant row, got %q", plain)
	}
	if !strings.Contains(plain, "Cache miss") {
		t.Fatalf("inserted local cache warning skipped warning row, got %q", plain)
	}
	if m.nativeStableProjectionNeedsDelivery(nativeStableLiveAppendIntent("test"), m.nativeDeliveredStableProjection, current) {
		t.Fatal("logical current projection should reconcile after appending local cache warning at physical tail")
	}
}

func TestNativeStableProjectionChangeSkipsStreamedBlockAfterOverlappingRecentTail(t *testing.T) {
	var out bytes.Buffer
	m := newNativeSurfaceTestModel(&out)
	if rendered := m.View(); rendered != "" {
		t.Fatalf("native ongoing View() returned %q, want empty renderer payload", rendered)
	}
	out.Reset()
	streamExitMainThread := m.enterUIMainThread("native stable overlap stream setup")
	if err := m.nativeSurface.StreamAssistantFinalAnswerContent("streamed-answer"); err != nil {
		streamExitMainThread()
		t.Fatalf("stream native assistant content: %v", err)
	}
	streamExitMainThread()

	previous := nativeStableProjectionForTest("old-a", "old-b")
	streamProjection := m.nativeCommittedProjectionForEntries([]tui.TranscriptEntry{{
		Role:      tui.TranscriptRoleAssistant,
		Text:      "streamed-answer",
		Committed: true,
	}})
	current := tui.TranscriptProjection{Blocks: []tui.TranscriptProjectionBlock{
		previous.Blocks[1],
		streamProjection.Blocks[0],
		{DividerGroup: "test", Lines: []string{"after-stream"}},
	}}
	exitMainThread := m.enterUIMainThread("native stable overlap active stream test")
	err := m.deliverNativeStableProjectionChange(nativeStableLiveAppendIntent("deliverNativeStableProjectionChange"), previous, current, true, true, false, "streamed-answer")
	exitMainThread()
	if err != nil {
		t.Fatalf("overlapping active-stream projection delivery returned error: %v", err)
	}

	plain := stripANSIAndTrimRight(out.String())
	if got := strings.Count(plain, "streamed-answer"); got != 1 {
		t.Fatalf("overlapping active-stream delivery wrote streamed block %d times, want once; output=%q", got, plain)
	}
	if strings.Contains(plain, "old-b") {
		t.Fatalf("overlapping active-stream delivery replayed overlap block, got %q", plain)
	}
	if !strings.Contains(plain, "after-stream") {
		t.Fatalf("overlapping active-stream delivery skipped post-stream block, got %q", plain)
	}
}

func TestNativeStableReplaceDeliversCommittedProjectionAppend(t *testing.T) {
	var out bytes.Buffer
	m := newNativeSurfaceTestModel(&out)
	seedNativeSurfaceTranscript(m, []tui.TranscriptEntry{
		{Role: tui.TranscriptRoleUser, Text: "run native replace", Committed: true},
		{Role: tui.TranscriptRoleAssistant, Text: "transient answer", Transient: true},
	})
	if rendered := m.View(); rendered != "" {
		t.Fatalf("native ongoing View() returned %q, want empty renderer payload", rendered)
	}
	out.Reset()

	result := applyNativeSurfaceRuntimeEventForTest(t, m, clientui.Event{
		Kind:                       clientui.EventAssistantMessage,
		CommittedTranscriptChanged: true,
		TranscriptRevision:         2,
		CommittedEntryStart:        1,
		CommittedEntryStartSet:     true,
		TranscriptEntries:          []clientui.ChatEntry{{Role: "assistant", Text: "committed replacement", Phase: string(clientui.MessagePhaseFinal)}},
	})
	_ = collectCmdMessages(t, result.cmd)

	plain := stripANSIAndTrimRight(out.String())
	if !strings.Contains(plain, "committed replacement") {
		t.Fatalf("native stable replace did not deliver committed projection append, got %q", plain)
	}
	if strings.Contains(plain, "run native replace") {
		t.Fatalf("native stable replace rehydrated already-emitted committed prompt, got %q", plain)
	}
}

func TestNativeStableReplaceRejectsNonAppendProjectionRewrite(t *testing.T) {
	var out bytes.Buffer
	m := newNativeSurfaceTestModel(&out)
	seedNativeSurfaceTranscript(m, []tui.TranscriptEntry{
		{Role: tui.TranscriptRoleUser, Text: "original prompt", Committed: true},
		{Role: tui.TranscriptRoleAssistant, Text: "original answer", Committed: true},
	})
	if rendered := m.View(); rendered != "" {
		t.Fatalf("native ongoing View() returned %q, want empty renderer payload", rendered)
	}
	out.Reset()

	result := applyNativeSurfaceRuntimeEventForTest(t, m, clientui.Event{
		Kind:                       clientui.EventConversationUpdated,
		CommittedTranscriptChanged: true,
		TranscriptRevision:         2,
		CommittedEntryStart:        0,
		CommittedEntryStartSet:     true,
		TranscriptEntries:          []clientui.ChatEntry{{Role: "user", Text: "rewritten prompt"}},
	})
	_ = collectCmdMessages(t, result.cmd)

	if m.nativeLiveAreaError == nil {
		t.Fatal("expected native stable replacement rewrite to surface an error")
	}
	if !strings.Contains(m.nativeLiveAreaError.Error(), "native stable append is not contiguous") {
		t.Fatalf("native stable replacement error = %v, want non-contiguous append error", m.nativeLiveAreaError)
	}
	if plain := stripANSIAndTrimRight(out.String()); strings.Contains(plain, "rewritten prompt") {
		t.Fatalf("native stable replacement rewrite wrote non-append stable content, got %q", plain)
	}
}

func TestNativeStableReplacePanicsOnNonAppendProjectionRewriteInDebug(t *testing.T) {
	var out bytes.Buffer
	m := newSizedProjectedClosedUIModel(nil, 120, 30, WithUINativeSurfaceWriter(&out), WithUIDebug(true))
	seedNativeSurfaceTranscript(m, []tui.TranscriptEntry{
		{Role: tui.TranscriptRoleUser, Text: "original prompt", Committed: true},
		{Role: tui.TranscriptRoleAssistant, Text: "original answer", Committed: true},
	})
	if rendered := m.View(); rendered != "" {
		t.Fatalf("native ongoing View() returned %q, want empty renderer payload", rendered)
	}

	panicText := captureNativeSurfacePanicText(t, func() {
		result := applyNativeSurfaceRuntimeEventForTest(t, m, clientui.Event{
			Kind:                       clientui.EventConversationUpdated,
			CommittedTranscriptChanged: true,
			TranscriptRevision:         2,
			CommittedEntryCount:        2,
			CommittedEntryStart:        0,
			CommittedEntryStartSet:     true,
			TranscriptEntries:          []clientui.ChatEntry{{Role: "user", Text: "rewritten prompt"}},
		})
		_ = collectCmdMessages(t, result.cmd)
	})
	if !strings.Contains(panicText, "Native scrollback invariant violation") ||
		!strings.Contains(panicText, "native stable append is not contiguous") ||
		!strings.Contains(panicText, "operation=deliverNativeStableProjectionChange") {
		t.Fatalf("panic = %q, want debug native stable invariant diagnostic", panicText)
	}
}

func TestNativeStableReplaceWithActiveStreamPanicsBeforeFinalizingNonAppendTailInDebug(t *testing.T) {
	var out bytes.Buffer
	m := newSizedProjectedClosedUIModel(nil, 120, 30, WithUINativeSurfaceWriter(&out), WithUIDebug(true))
	seedNativeSurfaceTranscript(m, []tui.TranscriptEntry{
		{Role: tui.TranscriptRoleUser, Text: "original prompt", Committed: true},
		{Role: tui.TranscriptRoleAssistant, Text: "original answer", Committed: true},
	})
	if rendered := m.View(); rendered != "" {
		t.Fatalf("native ongoing View() returned %q, want empty renderer payload", rendered)
	}
	out.Reset()

	streamed := applyNativeSurfaceRuntimeEventForTest(t, m, clientui.Event{
		Kind:                clientui.EventAssistantDelta,
		StepID:              "step-final",
		AssistantDelta:      "mutable stream tail",
		AssistantDeltaPhase: clientui.MessagePhaseFinal,
	})
	_ = collectCmdMessages(t, streamed.cmd)
	if m.nativeSurface == nil || !m.nativeSurface.AssistantStreaming() {
		t.Fatal("expected native assistant stream to be active before non-append replacement")
	}
	previous := m.nativeCommittedProjectionForEntries(m.transcriptEntries)
	current := nativeStableProjectionForTest("rewritten prompt", "original answer")

	panicText := captureNativeSurfacePanicText(t, func() {
		exitMainThread := m.enterUIMainThread("native active stream non-append replacement test")
		defer exitMainThread()
		_ = m.deliverNativeStableProjectionChange(nativeStableLiveAppendIntent("deliverNativeStableProjectionChange"), previous, current, true, true, false, "mutable stream tail")
	})
	if !strings.Contains(panicText, "Native scrollback invariant violation") ||
		!strings.Contains(panicText, "native stable append is not contiguous") ||
		!strings.Contains(panicText, "operation=deliverNativeStableProjectionChange") {
		t.Fatalf("panic = %q, want debug native stable invariant diagnostic", panicText)
	}
	plain := stripANSIAndTrimRight(out.String())
	if strings.Contains(plain, "mutable stream tail") {
		t.Fatalf("non-append replacement finalized mutable stream tail before disabling native: %q", plain)
	}
	if strings.Contains(plain, "rewritten prompt") {
		t.Fatalf("non-append replacement wrote rewritten stable content before disabling native: %q", plain)
	}
}

func TestNativeStableAppendWithActiveStreamPanicsOnMismatchedCommittedBlockInDebug(t *testing.T) {
	var out bytes.Buffer
	m := newSizedProjectedClosedUIModel(nil, 120, 30, WithUINativeSurfaceWriter(&out), WithUIDebug(true))
	seedNativeSurfaceTranscript(m, []tui.TranscriptEntry{
		{Role: tui.TranscriptRoleUser, Text: "prompt", Committed: true},
	})
	if rendered := m.View(); rendered != "" {
		t.Fatalf("native ongoing View() returned %q, want empty renderer payload", rendered)
	}
	out.Reset()

	streamed := applyNativeSurfaceRuntimeEventForTest(t, m, clientui.Event{
		Kind:                clientui.EventAssistantDelta,
		StepID:              "step-final",
		AssistantDelta:      "draft answer",
		AssistantDeltaPhase: clientui.MessagePhaseFinal,
	})
	_ = collectCmdMessages(t, streamed.cmd)
	if m.nativeSurface == nil || !m.nativeSurface.AssistantStreaming() {
		t.Fatal("expected native assistant stream to be active before authoritative append")
	}
	previous := m.nativeCommittedProjectionForEntries(m.transcriptEntries)
	entries := append([]tui.TranscriptEntry(nil), m.transcriptEntries...)
	entries = append(entries, tui.TranscriptEntry{
		Role:      tui.TranscriptRoleAssistant,
		Text:      "corrected answer",
		Committed: true,
	})
	current := m.nativeCommittedProjectionForEntries(entries)

	panicText := captureNativeSurfacePanicText(t, func() {
		exitMainThread := m.enterUIMainThread("native active stream mismatched append test")
		defer exitMainThread()
		_ = m.deliverNativeStableProjectionChange(nativeStableLiveAppendIntent("deliverNativeStableProjectionChange"), previous, current, true, true, false, "draft answer")
	})
	if !strings.Contains(panicText, "Native scrollback invariant violation") ||
		!strings.Contains(panicText, nativeStableProjectionActiveStreamMismatchReason) ||
		!strings.Contains(panicText, "operation=deliverNativeStableProjectionChange") {
		t.Fatalf("panic = %q, want debug native active-stream mismatch diagnostic", panicText)
	}
	plain := stripANSIAndTrimRight(out.String())
	if strings.Contains(plain, "draft answer") {
		t.Fatalf("mismatched committed append finalized draft stream tail before disabling native: %q", plain)
	}
	if strings.Contains(plain, "corrected answer") {
		t.Fatalf("mismatched committed append skipped/wrote authoritative block through native stable path: %q", plain)
	}
}

func TestNativeStableAppendWithActiveStreamQueuesNonAssistantCommittedRows(t *testing.T) {
	var out bytes.Buffer
	m := newNativeSurfaceTestModel(&out)
	if rendered := m.View(); rendered != "" {
		t.Fatalf("native ongoing View() returned %q, want empty renderer payload", rendered)
	}
	out.Reset()

	streamExitMainThread := m.enterUIMainThread("native active stream non-assistant setup")
	if err := m.nativeSurface.StreamAssistantCommentaryContent("working"); err != nil {
		streamExitMainThread()
		t.Fatalf("stream native assistant content: %v", err)
	}
	streamExitMainThread()

	previous := nativeStableProjectionForTest("old-a")
	previous.Blocks[0].Role = tui.RenderIntentUser
	previous.Blocks[0].DividerGroup = string(tui.RenderIntentUser)
	current := previous.Clone()
	current.Blocks = append(current.Blocks,
		tui.TranscriptProjectionBlock{
			Role:         tui.RenderIntentToolPatchSuccess,
			DividerGroup: string(tui.RenderIntentTool),
			Lines:        []string{"⇄ ./.builder/plans/BUI-146-goal-interrupt-recon.md -1 +1"},
		},
		tui.TranscriptProjectionBlock{
			Role:         tui.RenderIntentUser,
			DividerGroup: string(tui.RenderIntentUser),
			Lines:        []string{"❯ you can talk to me via commentary channel"},
		},
	)
	exitMainThread := m.enterUIMainThread("native active stream non-assistant append test")
	err := m.deliverNativeStableProjectionChange(nativeStableLiveAppendIntent("deliverNativeStableProjectionChange"), previous, current, true, true, false, "working")
	exitMainThread()
	if err != nil {
		t.Fatalf("active stream non-assistant delivery returned error: %v", err)
	}
	if !m.nativeSurface.AssistantStreaming() {
		t.Fatal("non-assistant committed rows finalized active assistant stream")
	}
	if got := stripANSIAndTrimRight(out.String()); got != "" {
		t.Fatalf("queued non-assistant rows wrote before active stream finished: %q", got)
	}

	finishExitMainThread := m.enterUIMainThread("native active stream non-assistant finish")
	err = m.finishNativeAssistantStreaming()
	finishExitMainThread()
	if err != nil {
		t.Fatalf("finish native assistant stream after queued rows returned error: %v", err)
	}
	plain := stripANSIAndTrimRight(out.String())
	if !strings.Contains(plain, "working") ||
		!strings.Contains(plain, "BUI-146-goal-interrupt-recon.md") ||
		!strings.Contains(plain, "you can talk to me") {
		t.Fatalf("finish skipped stream or queued non-assistant rows, got %q", plain)
	}
}

func TestNativeStableAppendWithActiveStreamRejectsLaterMatchingFinalizer(t *testing.T) {
	var out bytes.Buffer
	m := newNativeSurfaceTestModel(&out)
	if rendered := m.View(); rendered != "" {
		t.Fatalf("native ongoing View() returned %q, want empty renderer payload", rendered)
	}
	out.Reset()

	streamExitMainThread := m.enterUIMainThread("native active stream later finalizer setup")
	if err := m.nativeSurface.StreamAssistantFinalAnswerContent("final answer"); err != nil {
		streamExitMainThread()
		t.Fatalf("stream native assistant content: %v", err)
	}
	streamExitMainThread()

	previous := nativeStableProjectionForTest("old-a")
	previous.Blocks[0].Role = tui.RenderIntentUser
	previous.Blocks[0].DividerGroup = string(tui.RenderIntentUser)
	streamProjection := m.nativeCommittedProjectionForEntries([]tui.TranscriptEntry{{
		Role:      tui.TranscriptRoleAssistant,
		Text:      "final answer",
		Committed: true,
	}})
	current := previous.Clone()
	current.Blocks = append(current.Blocks,
		tui.TranscriptProjectionBlock{
			Role:         tui.RenderIntentUser,
			DividerGroup: string(tui.RenderIntentUser),
			Lines:        []string{"❯ queued user"},
		},
		streamProjection.Blocks[0],
	)
	exitMainThread := m.enterUIMainThread("native active stream later finalizer append test")
	err := m.deliverNativeStableProjectionChange(nativeStableLiveAppendIntent("deliverNativeStableProjectionChange"), previous, current, true, true, false, "final answer")
	exitMainThread()
	if err == nil {
		t.Fatal("expected later active stream finalizer to be rejected instead of reordered")
	}
	plain := stripANSIAndTrimRight(out.String())
	if strings.Contains(plain, "final answer") {
		t.Fatalf("later finalizer rejection finished or rewrote stream, got %q", plain)
	}
	if strings.Contains(plain, "queued user") {
		t.Fatalf("later finalizer rejection reordered queued user after stream, got %q", plain)
	}
}

func TestNativeStableAppendWithActiveStreamRecoveryMismatchPanicsInDebug(t *testing.T) {
	var out bytes.Buffer
	m := newSizedProjectedClosedUIModel(nil, 120, 30, WithUINativeSurfaceWriter(&out), WithUIDebug(true))
	if rendered := m.View(); rendered != "" {
		t.Fatalf("native ongoing View() returned %q, want empty renderer payload", rendered)
	}
	out.Reset()

	streamExitMainThread := m.enterUIMainThread("native active stream recovery mismatch setup")
	if err := m.nativeSurface.StreamAssistantFinalAnswerContent("live active stream"); err != nil {
		streamExitMainThread()
		t.Fatalf("stream native assistant content: %v", err)
	}
	streamExitMainThread()

	previous := nativeStableProjectionForTest("tool row")
	previous.Blocks[0].Role = tui.RenderIntentToolShellSuccess
	previous.Blocks[0].DividerGroup = string(tui.RenderIntentTool)
	current := previous.Clone()
	mismatched := m.nativeCommittedProjectionForEntries([]tui.TranscriptEntry{{
		Role:      tui.TranscriptRoleAssistant,
		Text:      "different committed assistant",
		Committed: true,
		Phase:     clientui.MessagePhaseFinal,
	}})
	current.Blocks = append(current.Blocks, mismatched.Blocks[0])

	panicText := captureNativeSurfacePanicText(t, func() {
		exitMainThread := m.enterUIMainThread("native active stream recovery mismatch test")
		_ = m.deliverNativeStableProjectionChange(nativeStableRecoveryReconcileIntent("deliverNativeStableProjectionChange"), previous, current, true, true, false, "live active stream")
		exitMainThread()
	})
	if !strings.Contains(panicText, "Native scrollback invariant violation") ||
		!strings.Contains(panicText, nativeStableProjectionActiveStreamMismatchReason) ||
		!strings.Contains(panicText, "operation=deliverNativeStableProjectionChange") {
		t.Fatalf("panic = %q, want debug native stable invariant diagnostic", panicText)
	}
	if got := out.String(); strings.Contains(got, "different committed assistant") {
		t.Fatalf("recovery mismatch wrote committed assistant through native stable path: %q", got)
	}
}

func TestNativeStableRecoveryReconcilePanicsForInsertedCommittedToolBeforeDeliveredTailInDebug(t *testing.T) {
	var out bytes.Buffer
	m := newSizedProjectedClosedUIModel(nil, 120, 30, WithUINativeSurfaceWriter(&out), WithUIDebug(true))
	if rendered := m.View(); rendered != "" {
		t.Fatalf("native ongoing View() returned %q, want empty renderer payload", rendered)
	}
	out.Reset()

	previous := tui.TranscriptProjection{Blocks: []tui.TranscriptProjectionBlock{
		{
			Role:         tui.RenderIntentToolShellSuccess,
			DividerGroup: string(tui.RenderIntentTool),
			SourceKey:    "tool-shell-continue",
			Lines:        []string{"$ kent run --continue \"ab410dc6\" \"Fixed remaining\""},
		},
		{
			Role:         tui.RenderIntentToolShellSuccess,
			DividerGroup: string(tui.RenderIntentTool),
			SourceKey:    "tool-shell-poll",
			Lines:        []string{"$ Polled session 12283 for 5m0s"},
		},
		{
			Role:         tui.RenderIntentToolShellSuccess,
			DividerGroup: string(tui.RenderIntentTool),
			SourceKey:    "tool-shell-status",
			Lines:        []string{"$ git status --short"},
		},
	}}
	current := tui.TranscriptProjection{Blocks: []tui.TranscriptProjectionBlock{
		{
			Role:         tui.RenderIntentToolPatchSuccess,
			DividerGroup: string(tui.RenderIntentTool),
			SourceKey:    "tool-patch-plan",
			Lines:        []string{"⇄ ./.builder/plans/bui-142-queued-steering-compaction.md -1 +1"},
		},
		previous.Blocks[0],
		previous.Blocks[1],
		previous.Blocks[2],
		{
			Role:         tui.RenderIntentToolShellSuccess,
			DividerGroup: string(tui.RenderIntentTool),
			SourceKey:    "tool-shell-complete",
			Lines:        []string{"$ kent task complete --commentary 'Revised plan'"},
		},
	}}

	panicText := captureNativeSurfacePanicText(t, func() {
		exitMainThread := m.enterUIMainThread("native recovery inserted committed tool before delivered tail test")
		_ = m.deliverNativeStableProjectionChange(nativeStableRecoveryReconcileIntent("deliverNativeStableProjectionChange"), previous, current, true, false, false, "")
		exitMainThread()
	})
	if !strings.Contains(panicText, "Native scrollback invariant violation") ||
		!strings.Contains(panicText, nativeStableProjectionNonContiguousReason) ||
		!strings.Contains(panicText, "operation=deliverNativeStableProjectionChange") {
		t.Fatalf("panic = %q, want debug native stable invariant diagnostic", panicText)
	}
	if plain := stripANSIAndTrimRight(out.String()); strings.Contains(plain, "bui-142-queued-steering-compaction") || strings.Contains(plain, "kent task complete") {
		t.Fatalf("recovery reconcile wrote non-contiguous committed rows into native scrollback: %q", plain)
	}
}

func TestNativeStablePendingRecoveryIntentSurvivesLaterLiveAppendBeforeResizeSettle(t *testing.T) {
	var out bytes.Buffer
	m := newSizedProjectedClosedUIModel(nil, 120, 30, WithUINativeSurfaceWriter(&out), WithUIDebug(true))
	if rendered := m.View(); rendered != "" {
		t.Fatalf("native ongoing View() returned %q, want empty renderer payload", rendered)
	}
	out.Reset()

	previous := tui.TranscriptProjection{Blocks: []tui.TranscriptProjectionBlock{
		{
			Role:         tui.RenderIntentToolShellSuccess,
			DividerGroup: string(tui.RenderIntentTool),
			SourceKey:    "tool-shell-continue",
			Lines:        []string{"$ kent run --continue \"ab410dc6\" \"Fixed remaining\""},
		},
		{
			Role:         tui.RenderIntentToolShellSuccess,
			DividerGroup: string(tui.RenderIntentTool),
			SourceKey:    "tool-shell-poll",
			Lines:        []string{"$ Polled session 12283 for 5m0s"},
		},
	}}
	recovered := tui.TranscriptProjection{Blocks: []tui.TranscriptProjectionBlock{
		{
			Role:         tui.RenderIntentToolPatchSuccess,
			DividerGroup: string(tui.RenderIntentTool),
			SourceKey:    "tool-patch-plan",
			Lines:        []string{"⇄ ./.builder/plans/bui-142-queued-steering-compaction.md -1 +1"},
		},
		previous.Blocks[0],
		previous.Blocks[1],
	}}
	liveAfterRecovery := recovered.Clone()
	liveAfterRecovery.Blocks = append(liveAfterRecovery.Blocks, tui.TranscriptProjectionBlock{
		Role:         tui.RenderIntentToolShellSuccess,
		DividerGroup: string(tui.RenderIntentTool),
		SourceKey:    "tool-shell-complete",
		Lines:        []string{"$ kent task complete --commentary 'Revised plan'"},
	})

	exitMainThread := m.enterUIMainThread("native pending recovery intent setup")
	err := m.deliverNativeStableProjectionChange(nativeStableRecoveryReconcileIntent("deliverNativeStableProjectionChange"), previous, recovered, false, false, false, "")
	exitMainThread()
	if err != nil {
		t.Fatalf("deferred recovery delivery returned error before resize settle: %v", err)
	}
	if m.nativePendingStableIntent.source != nativeStableDeliveryRecoveryReconcile {
		t.Fatalf("pending intent after recovery = %q, want recovery", m.nativePendingStableIntent.source)
	}

	exitMainThread = m.enterUIMainThread("native pending live append after recovery")
	err = m.deliverNativeStableProjectionChange(nativeStableLiveAppendIntent("deliverNativeStableProjectionChange"), previous, liveAfterRecovery, false, false, false, "")
	exitMainThread()
	if err != nil {
		t.Fatalf("deferred live delivery returned error before resize settle: %v", err)
	}
	if m.nativePendingStableIntent.source != nativeStableDeliveryRecoveryReconcile {
		t.Fatalf("pending intent after later live append = %q, want recovery", m.nativePendingStableIntent.source)
	}

	panicText := captureNativeSurfacePanicText(t, func() {
		exitMainThread = m.enterUIMainThread("native pending recovery resize settle")
		_ = m.deliverNativeStableProjectionChange(m.nativePendingStableIntent, previous, liveAfterRecovery, true, false, false, "")
		exitMainThread()
	})
	if !strings.Contains(panicText, "Native scrollback invariant violation") ||
		!strings.Contains(panicText, nativeStableProjectionNonContiguousReason) ||
		!strings.Contains(panicText, "operation=deliverNativeStableProjectionChange") {
		t.Fatalf("panic = %q, want debug native stable invariant diagnostic", panicText)
	}
	if plain := stripANSIAndTrimRight(out.String()); strings.Contains(plain, "bui-142-queued-steering-compaction") || strings.Contains(plain, "kent task complete") {
		t.Fatalf("pending recovery wrote non-contiguous rows into native scrollback: %q", plain)
	}
}

func TestNativeStableAppendWithActiveStreamAppendsInsertedSystemBeforeFinalizerAtPhysicalTail(t *testing.T) {
	var out bytes.Buffer
	m := newNativeSurfaceTestModel(&out)
	if rendered := m.View(); rendered != "" {
		t.Fatalf("native ongoing View() returned %q, want empty renderer payload", rendered)
	}
	out.Reset()

	streamExitMainThread := m.enterUIMainThread("native active stream local system before finalizer setup")
	if err := m.nativeSurface.StreamAssistantCommentaryContent("watcher found six threads"); err != nil {
		streamExitMainThread()
		t.Fatalf("stream native assistant content: %v", err)
	}
	streamExitMainThread()

	previous := nativeStableProjectionForTest("old-a")
	previous.Blocks[0].Role = tui.RenderIntentToolShellSuccess
	previous.Blocks[0].DividerGroup = string(tui.RenderIntentTool)
	streamProjection := m.nativeCommittedProjectionForEntries([]tui.TranscriptEntry{{
		Role:      tui.TranscriptRoleAssistant,
		Text:      "watcher found six threads",
		Committed: true,
		Phase:     clientui.MessagePhaseCommentary,
	}})
	current := previous.Clone()
	current.Blocks = append(current.Blocks,
		tui.TranscriptProjectionBlock{
			Role:         tui.RenderIntentSystem,
			DividerGroup: string(tui.RenderIntentSystem),
			Lines:        []string{"Background shell 8128 completed (exit 0)"},
		},
		streamProjection.Blocks[0],
	)

	exitMainThread := m.enterUIMainThread("native active stream local system before finalizer append test")
	err := m.deliverNativeStableProjectionChange(nativeStableLiveAppendIntent("deliverNativeStableProjectionChange"), previous, current, true, true, false, "watcher found six threads")
	exitMainThread()
	if err != nil {
		t.Fatalf("active stream local system-before-finalizer delivery returned error: %v", err)
	}
	if m.nativeSurface.AssistantStreaming() {
		t.Fatal("matching committed commentary left native assistant stream active")
	}
	plain := stripANSIAndTrimRight(out.String())
	streamIndex := strings.Index(plain, "watcher found six threads")
	systemIndex := strings.Index(plain, "Background shell 8128 completed")
	if streamIndex < 0 || systemIndex < 0 || streamIndex > systemIndex {
		t.Fatalf("physical output order = %q, want streamed assistant before local system notice", plain)
	}
	if got := len(m.nativeDeliveredStableProjection.Blocks); got != len(previous.Blocks)+2 {
		t.Fatalf("delivered block count = %d, want %d", got, len(previous.Blocks)+2)
	}
	tail := m.nativeDeliveredStableProjection.Blocks[len(m.nativeDeliveredStableProjection.Blocks)-2:]
	if tail[0].Role != tui.RenderIntentAssistantCommentary || tail[1].Role != tui.RenderIntentSystem {
		t.Fatalf("delivered physical tail roles = %s,%s; want assistant_commentary,system", tail[0].Role, tail[1].Role)
	}
	if m.nativeStableProjectionNeedsDelivery(nativeStableLiveAppendIntent("test"), m.nativeDeliveredStableProjection, current) {
		t.Fatal("logical current projection should reconcile against physical delivered stream/system order without replay")
	}
}

func TestNativeStableAppendWithActiveStreamUsesFinalizedStreamAsPostAppendDividerBaseline(t *testing.T) {
	var out bytes.Buffer
	m := newNativeSurfaceTestModel(&out)
	if rendered := m.View(); rendered != "" {
		t.Fatalf("native ongoing View() returned %q, want empty renderer payload", rendered)
	}
	out.Reset()

	streamExitMainThread := m.enterUIMainThread("native active stream post-finalizer divider setup")
	if err := m.nativeSurface.StreamAssistantFinalAnswerContent("final answer"); err != nil {
		streamExitMainThread()
		t.Fatalf("stream native assistant content: %v", err)
	}
	streamExitMainThread()

	previous := nativeStableProjectionForTest("prompt")
	previous.Blocks[0].Role = tui.RenderIntentUser
	previous.Blocks[0].DividerGroup = string(tui.RenderIntentUser)
	streamProjection := m.nativeCommittedProjectionForEntries([]tui.TranscriptEntry{{
		Role:      tui.TranscriptRoleAssistant,
		Text:      "final answer",
		Committed: true,
	}})
	current := previous.Clone()
	current.Blocks = append(current.Blocks,
		streamProjection.Blocks[0],
		tui.TranscriptProjectionBlock{
			Role:         tui.RenderIntentToolShellSuccess,
			DividerGroup: string(tui.RenderIntentTool),
			Lines:        []string{"$ gh pr checks 457 --watch=false"},
		},
	)

	exitMainThread := m.enterUIMainThread("native active stream post-finalizer divider append test")
	err := m.deliverNativeStableProjectionChange(nativeStableLiveAppendIntent("deliverNativeStableProjectionChange"), previous, current, true, true, false, "final answer")
	exitMainThread()
	if err != nil {
		t.Fatalf("active stream post-finalizer delivery returned error: %v", err)
	}

	plain := stripANSIAndTrimRight(out.String())
	finalIndex := strings.Index(plain, "final answer")
	dividerIndex := strings.Index(plain, tui.TranscriptDivider)
	toolIndex := strings.Index(plain, "gh pr checks")
	if finalIndex < 0 || dividerIndex < 0 || toolIndex < 0 || !(finalIndex < dividerIndex && dividerIndex < toolIndex) {
		t.Fatalf("output = %q, want finalized stream then divider then tool append", plain)
	}
}

func TestAssistantStreamFinalizerRejectsDifferentNonEmptyStepIDDespiteMatchingText(t *testing.T) {
	state := projectedTranscriptEventState{
		liveAssistantPending: true,
		liveAssistantText:    "same text",
		liveAssistantStepID:  "active-step",
	}
	evt := clientui.Event{
		Kind:                       clientui.EventAssistantMessage,
		StepID:                     "other-step",
		CommittedTranscriptChanged: true,
		TranscriptEntries: []clientui.ChatEntry{{
			Role: "assistant",
			Text: "same text",
		}},
	}

	if isAssistantStreamFinalizerEvent(state, evt) {
		t.Fatal("different non-empty assistant step IDs must not fall back to text matching")
	}
}

func TestNativeAssistantStreamStepResetRequiresKnownActiveStepID(t *testing.T) {
	var out bytes.Buffer
	m := newNativeSurfaceTestModel(&out)
	if rendered := m.View(); rendered != "" {
		t.Fatalf("native ongoing View() returned %q, want empty renderer payload", rendered)
	}
	result := applyNativeSurfaceRuntimeEventForTest(t, m, clientui.Event{
		Kind:                clientui.EventAssistantDelta,
		AssistantDelta:      "stream started before step IDs were known",
		AssistantDeltaPhase: clientui.MessagePhaseFinal,
	})
	_ = collectCmdMessages(t, result.cmd)
	state := newProjectedTranscriptEventState(projectedTranscriptEventSnapshotFromModel(m))
	evt := clientui.Event{
		Kind:   clientui.EventAssistantDelta,
		StepID: "known-step",
	}
	if !eventStartsDifferentAssistantStep(state, evt) {
		t.Fatal("new non-empty step ID should still be a turn-boundary signal")
	}
	if m.shouldResetActiveAssistantStreamForNewStep(state, evt) {
		t.Fatal("unknown active native step ID should not reset native assistant stream")
	}
	state.liveAssistantStepID = "active-step"
	if !m.shouldResetActiveAssistantStreamForNewStep(state, evt) {
		t.Fatal("known mismatched step IDs should reset native assistant stream")
	}
}

func TestActiveAssistantStreamSourceResetsWhenStepIDChanges(t *testing.T) {
	m := newProjectedClosedUIModel(nil)
	m.appendActiveAssistantStreamDelta("step-1", "old text")
	m.appendActiveAssistantStreamDelta("step-2", "new text")

	if got := m.activeAssistantStreamText(); got != "new text" {
		t.Fatalf("active assistant stream text = %q, want new text", got)
	}
	if got := m.activeAssistantStreamStepID; got != "step-2" {
		t.Fatalf("active assistant stream step = %q, want step-2", got)
	}
}

func TestAssistantDeltaStepChangeDisablesActiveNativeStreamBeforeReset(t *testing.T) {
	var out bytes.Buffer
	m := newSizedProjectedClosedUIModel(nil, 120, 30, WithUINativeSurfaceWriter(&out), WithUIDebug(true))
	if rendered := m.View(); rendered != "" {
		t.Fatalf("native ongoing View() returned %q, want empty renderer payload", rendered)
	}
	out.Reset()

	first := applyNativeSurfaceRuntimeEventForTest(t, m, clientui.Event{
		Kind:                clientui.EventAssistantDelta,
		StepID:              "step-1",
		AssistantDelta:      "old text",
		AssistantDeltaPhase: clientui.MessagePhaseFinal,
	})
	_ = collectCmdMessages(t, first.cmd)
	if m.nativeSurface == nil || !m.nativeSurface.AssistantStreaming() {
		t.Fatal("expected first step to start a native assistant stream")
	}

	second := applyNativeSurfaceRuntimeEventForTest(t, m, clientui.Event{
		Kind:                clientui.EventAssistantDelta,
		StepID:              "step-2",
		AssistantDelta:      "new text",
		AssistantDeltaPhase: clientui.MessagePhaseFinal,
	})
	_ = collectCmdMessages(t, second.cmd)
	if m.nativeSurface != nil {
		t.Fatal("step change with active native stream should disable native instead of appending into the old stream")
	}
	if got := m.activeAssistantStreamText(); got != "new text" {
		t.Fatalf("active assistant stream text = %q, want new text", got)
	}
	if got := m.view.OngoingStreamingText(); got != "new text" {
		t.Fatalf("view assistant stream text = %q, want new text", got)
	}
}

func TestToolCallStartedStepChangeDisablesActiveNativeStreamBeforeQueuingToolRows(t *testing.T) {
	var out bytes.Buffer
	m := newSizedProjectedClosedUIModel(nil, 120, 30, WithUINativeSurfaceWriter(&out), WithUIDebug(true))
	if rendered := m.View(); rendered != "" {
		t.Fatalf("native ongoing View() returned %q, want empty renderer payload", rendered)
	}
	out.Reset()

	first := applyNativeSurfaceRuntimeEventForTest(t, m, clientui.Event{
		Kind:                clientui.EventAssistantDelta,
		StepID:              "step-1",
		AssistantDelta:      "old text",
		AssistantDeltaPhase: clientui.MessagePhaseFinal,
	})
	_ = collectCmdMessages(t, first.cmd)
	if m.nativeSurface == nil || !m.nativeSurface.AssistantStreaming() {
		t.Fatal("expected first step to start a native assistant stream")
	}

	toolStarted := applyNativeSurfaceRuntimeEventForTest(t, m, clientui.Event{
		Kind:                       clientui.EventToolCallStarted,
		StepID:                     "step-2",
		CommittedTranscriptChanged: true,
		TranscriptRevision:         1,
		CommittedEntryCount:        1,
		CommittedEntryStart:        0,
		CommittedEntryStartSet:     true,
		TranscriptEntries: []clientui.ChatEntry{{
			Role:       "tool_call",
			Text:       "pwd",
			ToolCallID: "call-step-2",
			ToolCall:   &clientui.ToolCallMeta{ToolName: "exec_command", IsShell: true, Command: "pwd"},
		}},
	})
	_ = collectCmdMessages(t, toolStarted.cmd)
	if m.nativeSurface != nil {
		t.Fatal("tool-start step change with active native stream should disable native instead of queuing behind the old stream")
	}
	if got := m.activeAssistantStreamText(); got != "" {
		t.Fatalf("active assistant stream text = %q, want cleared", got)
	}
	if got := m.view.OngoingStreamingText(); got != "" {
		t.Fatalf("view assistant stream text = %q, want cleared", got)
	}
	if m.activeAssistantStreamPending() {
		t.Fatal("tool-start step reset left assistant stream marked pending")
	}
}

func TestAssistantDeltaStepChangeClearsViewOnlyStreamText(t *testing.T) {
	m := newProjectedClosedUIModel(nil)
	m.sawAssistantDelta = true
	m.forwardToView(tui.StreamAssistantMsg{Delta: "view-only old text"})

	_, cmd := m.handleRuntimeEventBatch([]clientui.Event{{
		Kind:           clientui.EventAssistantDelta,
		StepID:         "step-2",
		AssistantDelta: "new text",
	}})
	_ = collectCmdMessages(t, cmd)

	if got := m.activeAssistantStreamText(); got != "new text" {
		t.Fatalf("active assistant stream text = %q, want new text", got)
	}
	if got := m.view.OngoingStreamingText(); got != "new text" {
		t.Fatalf("view assistant stream text = %q, want new text", got)
	}
}

func TestNativeStableAppendWithActiveStreamFinalizesFromAppSourceWhenViewStreamIsEmpty(t *testing.T) {
	var out bytes.Buffer
	m := newSizedProjectedClosedUIModel(nil, 120, 30, WithUINativeSurfaceWriter(&out), WithUIDebug(true))
	seedNativeSurfaceTranscript(m, []tui.TranscriptEntry{
		{Role: tui.TranscriptRoleUser, Text: "prompt", Committed: true},
	})
	if rendered := m.View(); rendered != "" {
		t.Fatalf("native ongoing View() returned %q, want empty renderer payload", rendered)
	}
	out.Reset()

	streamed := applyNativeSurfaceRuntimeEventForTest(t, m, clientui.Event{
		Kind:                clientui.EventAssistantDelta,
		StepID:              "step-final",
		AssistantDelta:      "final answer",
		AssistantDeltaPhase: clientui.MessagePhaseFinal,
	})
	_ = collectCmdMessages(t, streamed.cmd)
	if m.nativeSurface == nil || !m.nativeSurface.AssistantStreaming() {
		t.Fatal("expected native assistant stream to be active before authoritative append")
	}
	if got := m.activeAssistantStreamText(); got != "final answer" {
		t.Fatalf("active assistant stream source = %q, want final answer", got)
	}
	m.forwardToView(tui.ClearOngoingAssistantMsg{})
	if got := m.view.OngoingStreamingText(); got != "" {
		t.Fatalf("view stream = %q, want empty split-brain repro state", got)
	}
	out.Reset()

	committed := applyNativeSurfaceRuntimeEventForTest(t, m, clientui.Event{
		Kind:                       clientui.EventAssistantMessage,
		StepID:                     "step-final",
		CommittedTranscriptChanged: true,
		TranscriptRevision:         2,
		CommittedEntryCount:        2,
		CommittedEntryStart:        1,
		CommittedEntryStartSet:     true,
		TranscriptEntries: []clientui.ChatEntry{{
			Role:  "assistant",
			Text:  "final answer",
			Phase: string(clientui.MessagePhaseFinal),
		}},
	})
	_ = collectCmdMessages(t, committed.cmd)
	if m.nativeLiveAreaError != nil {
		t.Fatalf("matching committed final produced native error: %v", m.nativeLiveAreaError)
	}
	if m.nativeSurface == nil {
		t.Fatal("matching committed final disabled native surface")
	}
	if m.nativeSurface.AssistantStreaming() {
		t.Fatal("matching committed final left native assistant stream active")
	}
	if got := m.activeAssistantStreamText(); got != "" {
		t.Fatalf("active assistant stream source = %q, want cleared", got)
	}
	plain := stripANSIAndTrimRight(out.String())
	if count := strings.Count(plain, "final answer"); count != 1 {
		t.Fatalf("matching committed final output count = %d, want 1 in %q", count, plain)
	}
}

func TestNativeStableAppendWithActiveCommentaryStreamFinalizesCommittedCommentary(t *testing.T) {
	var out bytes.Buffer
	m := newSizedProjectedClosedUIModel(nil, 120, 30, WithUINativeSurfaceWriter(&out), WithUIDebug(true))
	seedNativeSurfaceTranscript(m, []tui.TranscriptEntry{
		{Role: tui.TranscriptRoleUser, Text: "prompt", Committed: true},
	})
	if rendered := m.View(); rendered != "" {
		t.Fatalf("native ongoing View() returned %q, want empty renderer payload", rendered)
	}
	out.Reset()

	streamed := applyNativeSurfaceRuntimeEventForTest(t, m, clientui.Event{
		Kind:                clientui.EventAssistantDelta,
		StepID:              "step-commentary",
		AssistantDelta:      "I am fixing the deterministic test.",
		AssistantDeltaPhase: clientui.MessagePhaseCommentary,
	})
	_ = collectCmdMessages(t, streamed.cmd)
	if m.nativeSurface == nil || !m.nativeSurface.AssistantStreaming() {
		t.Fatal("expected native commentary stream to be active before committed commentary")
	}
	out.Reset()

	committed := applyNativeSurfaceRuntimeEventForTest(t, m, clientui.Event{
		Kind:                       clientui.EventAssistantMessage,
		StepID:                     "step-commentary",
		CommittedTranscriptChanged: true,
		TranscriptRevision:         2,
		CommittedEntryCount:        2,
		CommittedEntryStart:        1,
		CommittedEntryStartSet:     true,
		TranscriptEntries: []clientui.ChatEntry{{
			Role:  "assistant",
			Text:  "I am fixing the deterministic test.",
			Phase: string(clientui.MessagePhaseCommentary),
		}},
	})
	_ = collectCmdMessages(t, committed.cmd)

	if m.nativeLiveAreaError != nil {
		t.Fatalf("matching committed commentary produced native error: %v", m.nativeLiveAreaError)
	}
	if m.nativeSurface == nil {
		t.Fatal("matching committed commentary disabled native surface")
	}
	if m.nativeSurface.AssistantStreaming() {
		t.Fatal("matching committed commentary left native assistant stream active")
	}
	plain := stripANSIAndTrimRight(out.String())
	if count := strings.Count(plain, "I am fixing the deterministic test."); count != 1 {
		t.Fatalf("matching committed commentary output count = %d, want 1 in %q", count, plain)
	}
}

func TestNativeStableSameStepCommentaryToolErrorSequenceStaysAppendOnly(t *testing.T) {
	var out bytes.Buffer
	m := newSizedProjectedClosedUIModel(nil, 160, 40, WithUINativeSurfaceWriter(&out), WithUIDebug(true))
	seedNativeSurfaceTranscript(m, []tui.TranscriptEntry{
		{Role: tui.TranscriptRoleUser, Text: "prompt", Committed: true},
	})
	if rendered := m.View(); rendered != "" {
		t.Fatalf("native ongoing View() returned %q, want empty renderer payload", rendered)
	}
	out.Reset()

	stepID := "step-background"
	firstCommentary := "I am waiting for QA-REAL-BG-DONE."
	firstCompletion := "Background shell completed successfully with QA-REAL-BG-DONE."
	secondCommentary := "Waiting for background shell 1005."
	secondCompletion := "Background shell completed: QA-REAL-BG-DONE."

	events := []clientui.Event{
		{
			Kind:                clientui.EventAssistantDelta,
			StepID:              stepID,
			AssistantDelta:      firstCommentary,
			AssistantDeltaPhase: clientui.MessagePhaseCommentary,
		},
		{
			Kind:                       clientui.EventAssistantMessage,
			StepID:                     stepID,
			CommittedTranscriptChanged: true,
			TranscriptRevision:         2,
			CommittedEntryCount:        3,
			CommittedEntryStart:        1,
			CommittedEntryStartSet:     true,
			TranscriptEntries: []clientui.ChatEntry{{
				Role:  "assistant",
				Text:  firstCommentary,
				Phase: string(clientui.MessagePhaseCommentary),
			}, {
				Role:       "tool_call",
				Text:       "Polled session 1005 for 20s",
				ToolCallID: "call-first-poll",
				ToolCall: &clientui.ToolCallMeta{
					ToolName:    "write_stdin",
					CompactText: "Polled session 1005 for 20s",
				},
			}},
		},
		{
			Kind:                       clientui.EventToolCallCompleted,
			StepID:                     stepID,
			CommittedTranscriptChanged: true,
			TranscriptRevision:         3,
			CommittedEntryCount:        4,
			CommittedEntryStart:        3,
			CommittedEntryStartSet:     true,
			TranscriptEntries: []clientui.ChatEntry{{
				Role:       "tool_result_ok",
				Text:       "QA-REAL-BG-DONE",
				ToolCallID: "call-first-poll",
			}},
		},
		{
			Kind:                clientui.EventAssistantDelta,
			StepID:              stepID,
			AssistantDelta:      firstCompletion,
			AssistantDeltaPhase: clientui.MessagePhaseFinal,
		},
		{
			Kind:                       clientui.EventAssistantMessage,
			StepID:                     stepID,
			CommittedTranscriptChanged: true,
			TranscriptRevision:         4,
			CommittedEntryCount:        6,
			CommittedEntryStart:        4,
			CommittedEntryStartSet:     true,
			TranscriptEntries: []clientui.ChatEntry{{
				Role:  "assistant",
				Text:  firstCompletion,
				Phase: string(clientui.MessagePhaseFinal),
			}, {
				Role:       "tool_call",
				Text:       `{"t_content":"QA-REAL-BG-DONE"}`,
				ToolCallID: "call-final-answer",
				ToolCall: &clientui.ToolCallMeta{
					ToolName:    "final_answer",
					CompactText: `{"t_content":"QA-REAL-BG-DONE"}`,
				},
			}},
		},
		{
			Kind:                       clientui.EventToolCallCompleted,
			StepID:                     stepID,
			CommittedTranscriptChanged: true,
			TranscriptRevision:         5,
			CommittedEntryCount:        7,
			CommittedEntryStart:        6,
			CommittedEntryStartSet:     true,
			TranscriptEntries: []clientui.ChatEntry{{
				Role:       "tool_result_error",
				Text:       `{"error":"unknown tool"}`,
				ToolCallID: "call-final-answer",
			}},
		},
		{
			Kind:                clientui.EventAssistantDelta,
			StepID:              stepID,
			AssistantDelta:      secondCommentary,
			AssistantDeltaPhase: clientui.MessagePhaseCommentary,
		},
		{
			Kind:                       clientui.EventAssistantMessage,
			StepID:                     stepID,
			CommittedTranscriptChanged: true,
			TranscriptRevision:         6,
			CommittedEntryCount:        9,
			CommittedEntryStart:        7,
			CommittedEntryStartSet:     true,
			TranscriptEntries: []clientui.ChatEntry{{
				Role:  "assistant",
				Text:  secondCommentary,
				Phase: string(clientui.MessagePhaseCommentary),
			}, {
				Role:       "tool_call",
				Text:       "Polled session 1005 for 20s",
				ToolCallID: "call-second-poll",
				ToolCall: &clientui.ToolCallMeta{
					ToolName:    "write_stdin",
					CompactText: "Polled session 1005 for 20s",
				},
			}},
		},
		{
			Kind:                       clientui.EventToolCallCompleted,
			StepID:                     stepID,
			CommittedTranscriptChanged: true,
			TranscriptRevision:         7,
			CommittedEntryCount:        10,
			CommittedEntryStart:        9,
			CommittedEntryStartSet:     true,
			TranscriptEntries: []clientui.ChatEntry{{
				Role:       "tool_result_ok",
				Text:       "No output",
				ToolCallID: "call-second-poll",
			}},
		},
		{
			Kind:                clientui.EventAssistantDelta,
			StepID:              stepID,
			AssistantDelta:      secondCompletion,
			AssistantDeltaPhase: clientui.MessagePhaseFinal,
		},
		{
			Kind:                       clientui.EventAssistantMessage,
			StepID:                     stepID,
			CommittedTranscriptChanged: true,
			TranscriptRevision:         8,
			CommittedEntryCount:        11,
			CommittedEntryStart:        10,
			CommittedEntryStartSet:     true,
			TranscriptEntries: []clientui.ChatEntry{{
				Role:  "assistant",
				Text:  secondCompletion,
				Phase: string(clientui.MessagePhaseFinal),
			}},
		},
	}
	for _, evt := range events {
		result := applyNativeSurfaceRuntimeEventForTest(t, m, evt)
		_ = collectCmdMessages(t, result.cmd)
		if m.nativeLiveAreaError != nil {
			t.Fatalf("native surface error after %s: %v", evt.Kind, m.nativeLiveAreaError)
		}
		if m.nativeSurface == nil {
			t.Fatalf("native surface disabled after %s", evt.Kind)
		}
	}

	plain := stripANSIAndTrimRight(out.String())
	for _, want := range []string{firstCommentary, firstCompletion, "QA-REAL-BG-DONE", secondCommentary, secondCompletion} {
		if !strings.Contains(plain, want) {
			t.Fatalf("native output missing %q after same-step commentary/tool sequence: %q", want, plain)
		}
	}
	if m.nativeSurface.AssistantStreaming() {
		t.Fatal("same-step commentary/tool sequence left native assistant stream active")
	}
}

func TestNativeStableAppendsBackgroundNoticeAfterCompletedTurn(t *testing.T) {
	var out bytes.Buffer
	m := newSizedProjectedClosedUIModel(&runtimeControlFakeClient{}, 160, 40, WithUINativeSurfaceWriter(&out), WithUIDebug(true))
	if rendered := m.View(); rendered != "" {
		t.Fatalf("native ongoing View() returned %q, want empty renderer payload", rendered)
	}
	out.Reset()

	stepID := "step-background-notice"
	events := []clientui.Event{
		{
			Kind:                       clientui.EventUserMessageFlushed,
			StepID:                     stepID,
			CommittedTranscriptChanged: true,
			TranscriptRevision:         1,
			CommittedEntryCount:        1,
			CommittedEntryStart:        0,
			CommittedEntryStartSet:     true,
			UserMessage:                "run background",
			TranscriptEntries: []clientui.ChatEntry{{
				Role: "user",
				Text: "run background",
			}},
		},
		{
			Kind:                       clientui.EventToolCallStarted,
			StepID:                     stepID,
			CommittedTranscriptChanged: true,
			TranscriptRevision:         2,
			CommittedEntryCount:        2,
			CommittedEntryStart:        1,
			CommittedEntryStartSet:     true,
			TranscriptEntries: []clientui.ChatEntry{{
				Role:       "tool_call",
				Text:       "sleep 20 && echo QA-REAL-BG-SECOND",
				ToolCallID: "call-bg",
				ToolCall: &clientui.ToolCallMeta{
					ToolName:    "exec_command",
					IsShell:     true,
					Command:     "sleep 20 && echo QA-REAL-BG-SECOND",
					CompactText: "sleep 20 && echo QA-REAL-BG-SECOND",
				},
			}},
		},
		{
			Kind:                       clientui.EventToolCallCompleted,
			StepID:                     stepID,
			CommittedTranscriptChanged: true,
			TranscriptRevision:         3,
			CommittedEntryCount:        3,
			CommittedEntryStart:        2,
			CommittedEntryStartSet:     true,
			TranscriptEntries: []clientui.ChatEntry{{
				Role:       "tool_result_ok",
				Text:       "Process moved to background with ID 1001.\nNo output",
				ToolCallID: "call-bg",
			}},
		},
		{
			Kind:                clientui.EventAssistantDelta,
			StepID:              stepID,
			AssistantDelta:      "Waiting for QA-REAL-BG-SECOND to complete from the background session.",
			AssistantDeltaPhase: clientui.MessagePhaseCommentary,
		},
		{
			Kind:                       clientui.EventAssistantMessage,
			StepID:                     stepID,
			CommittedTranscriptChanged: true,
			TranscriptRevision:         4,
			CommittedEntryCount:        5,
			CommittedEntryStart:        3,
			CommittedEntryStartSet:     true,
			TranscriptEntries: []clientui.ChatEntry{{
				Role:  "assistant",
				Text:  "Waiting for QA-REAL-BG-SECOND to complete from the background session.",
				Phase: string(clientui.MessagePhaseCommentary),
			}, {
				Role:       "tool_call",
				Text:       "Polled session 1001 for 25s",
				ToolCallID: "call-poll",
				ToolCall: &clientui.ToolCallMeta{
					ToolName:    "write_stdin",
					CompactText: "Polled session 1001 for 25s",
				},
			}},
		},
		{
			Kind:                       clientui.EventToolCallCompleted,
			StepID:                     stepID,
			CommittedTranscriptChanged: true,
			TranscriptRevision:         5,
			CommittedEntryCount:        6,
			CommittedEntryStart:        5,
			CommittedEntryStartSet:     true,
			TranscriptEntries: []clientui.ChatEntry{{
				Role:       "tool_result_ok",
				Text:       "QA-REAL-BG-SECOND",
				ToolCallID: "call-poll",
			}},
		},
		{
			Kind:                clientui.EventAssistantDelta,
			StepID:              stepID,
			AssistantDelta:      "Background session completed successfully, output confirmed: QA-REAL-BG-SECOND.",
			AssistantDeltaPhase: clientui.MessagePhaseFinal,
		},
		{
			Kind:                       clientui.EventAssistantMessage,
			StepID:                     stepID,
			CommittedTranscriptChanged: true,
			TranscriptRevision:         6,
			CommittedEntryCount:        7,
			CommittedEntryStart:        6,
			CommittedEntryStartSet:     true,
			TranscriptEntries: []clientui.ChatEntry{{
				Role:  "assistant",
				Text:  "Background session completed successfully, output confirmed: QA-REAL-BG-SECOND.",
				Phase: string(clientui.MessagePhaseFinal),
			}},
		},
	}
	for _, evt := range events {
		result := applyNativeSurfaceRuntimeEventForTest(t, m, evt)
		_ = collectCmdMessages(t, result.cmd)
		if m.nativeLiveAreaError != nil {
			t.Fatalf("native surface error after %s: %v", evt.Kind, m.nativeLiveAreaError)
		}
	}

	notice := projectRuntimeEvent(runtime.Event{
		Kind:                runtime.EventBackgroundUpdated,
		TranscriptRevision:  6,
		CommittedEntryCount: 7,
		Background: &runtime.BackgroundShellEvent{
			Type:        "completed",
			ID:          "1001",
			State:       "completed",
			NoticeText:  "Background shell 1001 completed (exit 0)",
			CompactText: "Background shell 1001 completed (exit 0)",
		},
	})
	result := applyNativeSurfaceRuntimeEventForTest(t, m, notice)
	_ = collectCmdMessages(t, result.cmd)
	if m.nativeLiveAreaError != nil {
		t.Fatalf("background notice disabled native surface: %v", m.nativeLiveAreaError)
	}
	if m.nativeSurface == nil {
		t.Fatal("background notice disabled native surface")
	}
	authoritative := make([]clientui.ChatEntry, 0, len(m.transcriptEntries))
	for _, entry := range committedTranscriptEntriesForApp(m.transcriptEntries) {
		authoritative = append(authoritative, clientui.ChatEntry{
			Role:              string(entry.Role),
			Text:              entry.Text,
			CondensedText:     entry.CondensedText,
			Phase:             string(entry.Phase),
			MessageType:       string(entry.MessageType),
			ToolCallID:        entry.ToolCallID,
			CompactLabel:      entry.CompactLabel,
			ToolResultSummary: entry.ToolResultSummary,
			ToolCall:          transcriptToolCallMetaClient(entry.ToolCall),
		})
	}
	exitMainThread := m.enterUIMainThread("native background notice hydrate test")
	cmd := m.runtimeAdapter().applyRuntimeTranscriptPageWithRecovery(clientui.TranscriptPageRequest{}, clientui.TranscriptPage{
		Revision: 6,
		Entries:  authoritative,
	}, clientui.TranscriptRecoveryCauseNone)
	exitMainThread()
	_ = collectCmdMessages(t, cmd)
	if m.nativeLiveAreaError != nil {
		t.Fatalf("authoritative page without transient background notice disabled native: %v", m.nativeLiveAreaError)
	}
}

func TestNativeStableTranscriptPageRecoveryPanicsOnActiveStreamMismatchInDebug(t *testing.T) {
	var out bytes.Buffer
	m := newSizedProjectedClosedUIModel(nil, 120, 30, WithUINativeSurfaceWriter(&out), WithUIDebug(true))
	seedNativeSurfaceTranscript(m, []tui.TranscriptEntry{
		{Role: tui.TranscriptRoleUser, Text: "prompt", Committed: true},
	})
	if rendered := m.View(); rendered != "" {
		t.Fatalf("native ongoing View() returned %q, want empty renderer payload", rendered)
	}
	out.Reset()

	streamed := applyNativeSurfaceRuntimeEventForTest(t, m, clientui.Event{
		Kind:                clientui.EventAssistantDelta,
		StepID:              "step-final",
		AssistantDelta:      "draft answer",
		AssistantDeltaPhase: clientui.MessagePhaseFinal,
	})
	_ = collectCmdMessages(t, streamed.cmd)
	if m.nativeSurface == nil || !m.nativeSurface.AssistantStreaming() {
		t.Fatal("expected native assistant stream to be active before authoritative append")
	}
	out.Reset()

	panicText := captureNativeSurfacePanicText(t, func() {
		exitMainThread := m.enterUIMainThread("native active stream mismatched page recovery test")
		_ = m.runtimeAdapter().applyRuntimeTranscriptPageWithRecovery(clientui.TranscriptPageRequest{}, clientui.TranscriptPage{
			Revision: 2,
			Entries: []clientui.ChatEntry{{
				Role: "user",
				Text: "prompt",
			}, {
				Role:  "assistant",
				Text:  "corrected answer",
				Phase: string(clientui.MessagePhaseFinal),
			}},
		}, clientui.TranscriptRecoveryCauseStreamGap)
		exitMainThread()
	})
	if !strings.Contains(panicText, "Native scrollback invariant violation") ||
		!strings.Contains(panicText, nativeStableProjectionActiveStreamMismatchReason) ||
		!strings.Contains(panicText, "operation=deliverNativeStableProjectionChange") {
		t.Fatalf("panic = %q, want debug native stable invariant diagnostic", panicText)
	}
	plain := stripANSIAndTrimRight(out.String())
	if strings.Contains(plain, "draft answer") {
		t.Fatalf("mismatched runtime append finalized draft stream tail before disabling native: %q", plain)
	}
	if strings.Contains(plain, "corrected answer") {
		t.Fatalf("mismatched runtime append wrote authoritative block through native stable path: %q", plain)
	}
}

func nativeStableProjectionForTest(lines ...string) tui.TranscriptProjection {
	blocks := make([]tui.TranscriptProjectionBlock, 0, len(lines))
	for _, line := range lines {
		blocks = append(blocks, tui.TranscriptProjectionBlock{
			DividerGroup: "test",
			Lines:        []string{line},
		})
	}
	return tui.TranscriptProjection{Blocks: blocks}
}

const nativeWidePatchSummaryPathForTest = "./apps/desktop/src/test-support/workflow-editor/workflowEditorGraphMutationFixtures.ts"

func nativeWidePatchSummaryClientEntriesForTest() []clientui.ChatEntry {
	patchSummary := nativeWidePatchSummaryPathForTest + " -1 +1"
	return []clientui.ChatEntry{{
		Role:       "tool_call",
		Text:       "patch " + nativeWidePatchSummaryPathForTest,
		ToolCallID: "call_patch",
		ToolCall: &clientui.ToolCallMeta{
			ToolName:    "patch",
			Command:     nativeWidePatchSummaryPathForTest,
			CompactText: nativeWidePatchSummaryPathForTest,
		},
	}, {
		Role:       "tool_result_ok",
		ToolCallID: "call_patch",
		ToolCall: &clientui.ToolCallMeta{
			ToolName:     "patch",
			Command:      patchSummary,
			CompactText:  patchSummary,
			PatchSummary: patchSummary,
		},
	}}
}

func nativeShellClientEntriesForTest(toolCallID string, command string, output string) []clientui.ChatEntry {
	meta := &clientui.ToolCallMeta{
		ToolName:    "exec_command",
		IsShell:     true,
		Command:     command,
		CompactText: command,
	}
	return []clientui.ChatEntry{{
		Role:       "tool_call",
		Text:       command,
		ToolCallID: toolCallID,
		ToolCall:   meta,
	}, {
		Role:       "tool_result_ok",
		Text:       output,
		ToolCallID: toolCallID,
		ToolCall:   meta,
	}}
}

func newNativeSurfaceTestModel(out *bytes.Buffer) *uiModel {
	return newSizedProjectedClosedUIModel(nil, 120, 30, WithUINativeSurfaceWriter(out))
}

func applyNativeSurfaceRuntimeEventForTest(t *testing.T, m *uiModel, event clientui.Event) runtimeEventApplyResult {
	t.Helper()
	exitMainThread := m.enterUIMainThread("native surface test runtime event")
	defer exitMainThread()
	return m.runtimeAdapter().applyProjectedRuntimeEvent(event)
}

func seedNativeSurfaceTranscript(m *uiModel, entries []tui.TranscriptEntry) {
	m.transcriptBaseOffset = 0
	m.transcriptEntries = append([]tui.TranscriptEntry(nil), entries...)
	m.transcriptTotalEntries = len(entries)
	m.transcriptRevision = 1
	m.forwardToView(tui.SetConversationMsg{
		BaseOffset:   0,
		TotalEntries: len(entries),
		Entries:      append([]tui.TranscriptEntry(nil), entries...),
	})
}

func nativeStableDividerLineForTest(raw string) string {
	for _, line := range strings.FieldsFunc(raw, func(r rune) bool {
		return r == '\n' || r == '\r'
	}) {
		stripped := xansi.Strip(line)
		if stripped == "" {
			continue
		}
		if strings.Trim(stripped, "─") == "" {
			return line
		}
	}
	return ""
}

func nativeSurfaceRenderedLineCount(raw string) int {
	plain := xansi.Strip(raw)
	plain = strings.ReplaceAll(plain, "\r\n", "\n")
	plain = strings.ReplaceAll(plain, "\r", "\n")
	plain = strings.TrimRight(plain, "\n")
	if plain == "" {
		return 0
	}
	return len(strings.Split(plain, "\n"))
}

type nativeSurfaceFailingWriter struct {
	err error
}

func (w nativeSurfaceFailingWriter) Write([]byte) (int, error) {
	return 0, w.err
}

type nativeSurfaceScriptedWriter struct {
	errors   []error
	writes   []string
	attempts int
}

func (w *nativeSurfaceScriptedWriter) Write(p []byte) (int, error) {
	w.attempts++
	if len(w.errors) > 0 {
		err := w.errors[0]
		w.errors = w.errors[1:]
		if err != nil {
			return 0, err
		}
	}
	w.writes = append(w.writes, string(p))
	return len(p), nil
}

func captureNativeSurfacePanicText(t *testing.T, fn func()) (panicText string) {
	t.Helper()
	defer func() {
		recovered := recover()
		if recovered == nil {
			t.Fatal("expected panic")
		}
		text, ok := recovered.(string)
		if !ok {
			t.Fatalf("panic = %T(%v), want string", recovered, recovered)
		}
		panicText = text
	}()
	fn()
	return ""
}
