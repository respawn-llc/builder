package app

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"core/cli/app/commands"
	"core/cli/tui"
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
	if !strings.Contains(plain, "queued two") || !strings.Contains(plain, "input one") || !strings.Contains(plain, "input two") || !strings.Contains(plain, "status") {
		t.Fatalf("native bounded full frame dropped expected tail lines, got %q", plain)
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

func TestNativeFinalAssistantStreamingPromotesMarkdownAcrossDeltas(t *testing.T) {
	var out bytes.Buffer
	m := newNativeSurfaceTestModel(&out)
	if rendered := m.View(); rendered != "" {
		t.Fatalf("native ongoing View() returned %q, want empty renderer payload", rendered)
	}
	out.Reset()

	first := applyNativeSurfaceRuntimeEventForTest(t, m, clientui.Event{
		Kind:                clientui.EventAssistantDelta,
		StepID:              "step-final",
		AssistantDelta:      "**rendered first**\n",
		AssistantDeltaPhase: clientui.MessagePhaseFinal,
	})
	_ = collectCmdMessages(t, first.cmd)
	if got := stripANSIAndTrimRight(out.String()); got != "" {
		t.Fatalf("first newline-terminated rendered markdown row promoted before following line: %q", got)
	}

	second := applyNativeSurfaceRuntimeEventForTest(t, m, clientui.Event{
		Kind:                clientui.EventAssistantDelta,
		StepID:              "step-final",
		AssistantDelta:      "second row\n",
		AssistantDeltaPhase: clientui.MessagePhaseFinal,
	})
	_ = collectCmdMessages(t, second.cmd)
	plain := stripANSIAndTrimRight(out.String())
	if strings.Contains(plain, "**") {
		t.Fatalf("native stable stream promotion exposed raw markdown markers: %q", plain)
	}
	if !strings.Contains(plain, "rendered first") {
		t.Fatalf("native stable stream promotion skipped first rendered row: %q", plain)
	}
	tail := xansi.Strip(strings.Join(m.nativeSurface.AssistantStreamTailLines(), ""))
	if !strings.Contains(tail, "second row") {
		t.Fatalf("native rendered tail skipped second row: %q", tail)
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
	if !strings.Contains(plain, "line1") || !strings.Contains(plain, "line2") {
		t.Fatalf("native rendered code promotion skipped fenced content: %q", plain)
	}
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

func TestNativeSurfaceResizeErasesPreviousLiveFrameBeforeReplacingGeometry(t *testing.T) {
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
	if !strings.Contains(raw, xansi.EraseEntireLine) {
		t.Fatalf("native resize did not erase previous live frame before replacement, raw=%q", raw)
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
	for _, rawLine := range strings.Split(strings.ReplaceAll(out.String(), "\r\n", "\n"), "\n") {
		if rawLine == "" {
			continue
		}
		if got := lipgloss.Width(rawLine); got > m.termWidth {
			t.Fatalf("resize flush stable write width = %d, want <= %d, raw=%q", got, m.termWidth, rawLine)
		}
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

func TestNativeSurfaceResizeMarksActiveAssistantStreamIncompleteForCommitRepair(t *testing.T) {
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
	if !m.nativeAssistantStreamIncomplete {
		t.Fatal("expected resize to mark native assistant stream incomplete")
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
		TranscriptEntries:          []clientui.ChatEntry{{Role: "assistant", Text: "stream interrupted by resize", Phase: string(clientui.MessagePhaseFinal)}},
	})
	_ = collectCmdMessages(t, committed.cmd)
	if m.nativeAssistantStreamIncomplete {
		t.Fatal("expected committed final to clear incomplete native assistant stream")
	}
	plain := stripANSIAndTrimRight(out.String())
	if count := strings.Count(plain, "stream interrupted by resize"); count != 1 {
		t.Fatalf("committed resize repair count = %d, want 1 in %q", count, plain)
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
	prepareSequence := xansi.ResetModeAltScreenSaveCursor + "\x1b[?6l" + "\x1b[r"
	if !strings.HasPrefix(raw, prepareSequence) {
		t.Fatalf("native output prefix = %q, want normal-buffer preparation", raw)
	}
	if rendererGate.PhysicalAltScreenActive() {
		t.Fatal("renderer output gate still reports physical alt-screen active after native preparation")
	}
	if plain := stripANSIAndTrimRight(raw); !strings.Contains(plain, "F1 or ? for help") {
		t.Fatalf("native live output missing after normal-buffer preparation: %q", plain)
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
	result := applyNativeSurfaceRuntimeEventForTest(t, m, clientui.Event{
		Kind:                clientui.EventAssistantDelta,
		StepID:              "step-final",
		AssistantDelta:      strings.Repeat("x", 120) + "\nnext\n",
		AssistantDeltaPhase: clientui.MessagePhaseFinal,
	})
	_ = collectCmdMessages(t, result.cmd)

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
	err := m.deliverNativeStableProjectionChange(previous, current, true, false, false, "")
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
	err := m.deliverNativeStableProjectionChange(previous, current, true, true, false, "streamed-answer")
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
		_ = m.deliverNativeStableProjectionChange(previous, current, true, true, false, "mutable stream tail")
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
		_ = m.deliverNativeStableProjectionChange(previous, current, true, true, false, "draft answer")
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

func TestNativeStableTranscriptPageRecoveryPanicsOnActiveStreamMismatchedCommittedAppendInDebug(t *testing.T) {
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
		defer exitMainThread()
		_ = m.runtimeAdapter().applyRuntimeTranscriptPageWithRecovery(clientui.TranscriptPageRequest{}, clientui.TranscriptPage{
			Revision:     2,
			Offset:       0,
			TotalEntries: 2,
			Entries: []clientui.ChatEntry{{
				Role: "user",
				Text: "prompt",
			}, {
				Role:  "assistant",
				Text:  "corrected answer",
				Phase: string(clientui.MessagePhaseFinal),
			}},
		}, clientui.TranscriptRecoveryCauseStreamGap)
	})
	if !strings.Contains(panicText, "Native scrollback invariant violation") ||
		!strings.Contains(panicText, nativeStableProjectionActiveStreamMismatchReason) ||
		!strings.Contains(panicText, "operation=deliverNativeStableProjectionChange") {
		t.Fatalf("panic = %q, want debug native active-stream mismatch diagnostic", panicText)
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
