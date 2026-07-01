package app

import (
	"fmt"
	"strconv"
	"strings"
	"testing"

	"core/cli/tui"
	"core/server/runtime"
	"core/shared/clientui"
	"core/shared/theme"
	"core/shared/transcript"

	tea "github.com/charmbracelet/bubbletea"
)

func TestPageKeysScrollTranscriptWhileInputFocused(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.termWidth = 80
	m.termHeight = 8

	for i := 0; i < 12; i++ {
		m.forwardToView(tui.AppendTranscriptMsg{Role: "assistant", Text: fmt.Sprintf("line %d", i)})
	}
	m.forwardToView(tui.ToggleModeMsg{}) // detail mode starts at scroll=0

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyPgDown})
	updated := next.(*uiModel)
	if down := updated.view.View(); down == "" {
		t.Fatal("expected detail transcript to remain visible after pgdown")
	}

	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyPgUp})
	updated = next.(*uiModel)
	if up := updated.view.View(); up == "" {
		t.Fatal("expected detail transcript to remain visible after pgup")
	}
}

func TestDetailModeUpDownScrollTranscript(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.termWidth = 80
	m.termHeight = 8
	m.layout().syncViewport()

	for i := 0; i < 16; i++ {
		m.forwardToView(tui.AppendTranscriptMsg{Role: "assistant", Text: fmt.Sprintf("line %d", i)})
	}
	m = updateUIModel(t, m, tea.KeyMsg{Type: tea.KeyShiftTab})

	initial := stripDetailSelectionRail(stripANSIAndTrimRight(m.view.View()))
	if initial == "" {
		t.Fatal("expected detail transcript visible before scrolling")
	}
	m = updateUIModel(t, m, tea.KeyMsg{Type: tea.KeyUp})
	afterUp := stripDetailSelectionRail(stripANSIAndTrimRight(m.view.View()))
	if afterUp == initial {
		t.Fatal("expected detail transcript to change after up")
	}

	beforeDownScroll := m.view.DetailScroll()
	m = updateUIModel(t, m, tea.KeyMsg{Type: tea.KeyDown})
	if got := m.view.DetailScroll(); got > beforeDownScroll {
		t.Fatalf("expected detail down after prior up not to move away from bottom, got %d from %d", got, beforeDownScroll)
	}
	if afterDown := stripDetailSelectionRail(stripANSIAndTrimRight(m.view.View())); afterDown == afterUp {
		t.Fatal("expected detail transcript to change after down")
	}
}

func TestDetailModeLineScrollRoundTripsScrollAndSelectionState(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.termWidth = 80
	m.termHeight = 8
	m.layout().syncViewport()

	for idx := 0; idx < 20; idx++ {
		m.forwardToView(tui.AppendTranscriptMsg{Role: "assistant", Text: fmt.Sprintf("state entry %02d", idx)})
	}
	m = updateUIModel(t, m, tea.KeyMsg{Type: tea.KeyShiftTab})
	m = updateUIModel(t, m, tea.KeyMsg{Type: tea.KeyPgUp})
	startScroll := m.view.DetailScroll()
	startSelected, startSelectedOK := m.view.DetailSelectedEntry()
	if !startSelectedOK {
		t.Fatal("expected selected detail entry before state round-trip")
	}

	m = updateUIModel(t, m, tea.KeyMsg{Type: tea.KeyUp})
	if got := m.view.DetailScroll(); got != startScroll+1 {
		t.Fatalf("expected up to move detail scroll state by one line, got %d want %d", got, startScroll+1)
	}
	m = updateUIModel(t, m, tea.KeyMsg{Type: tea.KeyDown})
	if got := m.view.DetailScroll(); got != startScroll {
		t.Fatalf("expected up/down detail scroll state to round-trip, got %d want %d", got, startScroll)
	}
	selected, selectedOK := m.view.DetailSelectedEntry()
	if !selectedOK || selected != startSelected {
		t.Fatalf("expected selected entry state to round-trip, got %d ok=%v want %d", selected, selectedOK, startSelected)
	}
}

func TestDetailModeCompactExpansionRoutesThroughUIModel(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.termWidth = 80
	m.termHeight = 12
	m.layout().syncViewport()
	m.forwardToView(tui.AppendTranscriptMsg{
		Role:       "tool_call",
		Text:       "cat large.txt",
		ToolCallID: "call_1",
		ToolCall:   &transcript.ToolCallMeta{ToolName: "exec_command", IsShell: true, Command: "cat large.txt"},
	})
	m.forwardToView(tui.AppendTranscriptMsg{Role: "tool_result_ok", ToolCallID: "call_1", Text: "line 1\nline 2"})

	m = updateUIModel(t, m, tea.KeyMsg{Type: tea.KeyShiftTab})
	collapsed := stripANSIAndTrimRight(m.view.View())
	if !strings.Contains(collapsed, "▶ cat large.txt") {
		t.Fatalf("expected collapsed compact tool row, got %q", collapsed)
	}
	if strings.Contains(collapsed, "line 2") {
		t.Fatalf("expected collapsed detail to hide tool output, got %q", collapsed)
	}

	m = updateUIModel(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	expanded := stripANSIAndTrimRight(m.view.View())
	if !strings.Contains(expanded, "▼ cat large.txt") || !strings.Contains(expanded, "│ line 1") || !strings.Contains(expanded, "└ line 2") {
		t.Fatalf("expected UI-routed enter to expand tool output, got %q", expanded)
	}
}

func TestDetailModeStatusLineShowsSelectedExpansionAction(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.termWidth = 80
	m.termHeight = 12
	m.layout().syncViewport()
	m.forwardToView(tui.AppendTranscriptMsg{
		Role:       "tool_call",
		Text:       "cat large.txt",
		ToolCallID: "call_1",
		ToolCall:   &transcript.ToolCallMeta{ToolName: "exec_command", IsShell: true, Command: "cat large.txt"},
	})
	m.forwardToView(tui.AppendTranscriptMsg{Role: "tool_result_ok", ToolCallID: "call_1", Text: "line 1\nline 2"})

	m = updateUIModel(t, m, tea.KeyMsg{Type: tea.KeyShiftTab})
	status := stripANSIAndTrimRight(m.layout().renderStatusLine(120, uiThemeStyles("dark")))
	if !strings.Contains(status, "Enter to expand") {
		t.Fatalf("expected detail status line expansion hint, got %q", status)
	}

	m = updateUIModel(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	status = stripANSIAndTrimRight(m.layout().renderStatusLine(120, uiThemeStyles("dark")))
	if !strings.Contains(status, "Enter to collapse") {
		t.Fatalf("expected detail status line collapse hint, got %q", status)
	}
}

func TestDetailModeStatusLineFallsBackWhenSelectionIsNotExpandable(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.termWidth = 80
	m.termHeight = 8
	m.layout().syncViewport()
	errorLines := make([]string, 0, 16)
	for idx := 0; idx < 16; idx++ {
		errorLines = append(errorLines, fmt.Sprintf("non expandable error line %02d", idx))
	}
	m.forwardToView(tui.AppendTranscriptMsg{Role: "error", Text: strings.Join(errorLines, "\n")})
	for idx := 0; idx < 12; idx++ {
		callID := fmt.Sprintf("call_%d", idx)
		command := fmt.Sprintf("cmd %d", idx)
		m.forwardToView(tui.AppendTranscriptMsg{
			Role:       "tool_call",
			Text:       command,
			ToolCallID: callID,
			ToolCall:   &transcript.ToolCallMeta{ToolName: "exec_command", IsShell: true, Command: command},
		})
		m.forwardToView(tui.AppendTranscriptMsg{Role: "tool_result_ok", ToolCallID: callID, Text: "line 1\nline 2"})
	}

	m = updateUIModel(t, m, tea.KeyMsg{Type: tea.KeyShiftTab})
	status := stripANSIAndTrimRight(m.layout().renderStatusLine(120, uiThemeStyles("dark")))
	if !strings.Contains(status, "Enter to expand") {
		t.Fatalf("expected expandable selection hint before scrolling, got %q", status)
	}

	for guard := 0; guard < 8; guard++ {
		m = updateUIModel(t, m, tea.KeyMsg{Type: tea.KeyPgUp})
		status = stripANSIAndTrimRight(m.layout().renderStatusLine(120, uiThemeStyles("dark")))
		if !strings.Contains(status, "Enter to expand") && !strings.Contains(status, "Enter to collapse") {
			break
		}
	}
	if strings.Contains(status, "Enter to expand") || strings.Contains(status, "Enter to collapse") {
		t.Fatalf("did not expect expansion hint after scrolling to non-expandable selection, got %q", status)
	}
}

func TestDetailModeEnterOnShortSelectedMessageDoesNotShowExpansionHintOrMutateState(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.termWidth = 80
	m.termHeight = 8
	m.layout().syncViewport()
	m.forwardToView(tui.AppendTranscriptMsg{Role: "user", Text: "short user"})
	m.forwardToView(tui.AppendTranscriptMsg{Role: "assistant", Text: "short assistant"})
	m = updateUIModel(t, m, tea.KeyMsg{Type: tea.KeyShiftTab})

	beforeView := stripANSIAndTrimRight(m.view.View())
	status := stripANSIAndTrimRight(m.layout().renderStatusLine(120, uiThemeStyles("dark")))
	if strings.Contains(beforeView, "▶") || strings.Contains(beforeView, "▼") || strings.Contains(status, "Enter to expand") || strings.Contains(status, "Enter to collapse") {
		t.Fatalf("did not expect expansion affordance for selected short message, view=%q status=%q", beforeView, status)
	}

	m = updateUIModel(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	afterView := stripANSIAndTrimRight(m.view.View())
	status = stripANSIAndTrimRight(m.layout().renderStatusLine(120, uiThemeStyles("dark")))
	if afterView != beforeView || strings.Contains(afterView, "▶") || strings.Contains(afterView, "▼") || strings.Contains(status, "Enter to expand") || strings.Contains(status, "Enter to collapse") {
		t.Fatalf("expected enter on selected short message to be no-op with normal help, before=%q after=%q status=%q", beforeView, afterView, status)
	}
}

func TestDetailModeArrowScrollsDetailByLineAndTracksCenterSelection(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.termWidth = 80
	m.termHeight = 8
	m.layout().syncViewport()
	m.forwardToView(tui.AppendTranscriptMsg{
		Role:       "tool_call",
		Text:       "first-command",
		ToolCallID: "call_1",
		ToolCall:   &transcript.ToolCallMeta{ToolName: "exec_command", IsShell: true, Command: "first-command"},
	})
	firstOutput := make([]string, 0, 20)
	for idx := 0; idx < 20; idx++ {
		firstOutput = append(firstOutput, fmt.Sprintf("first output line %02d", idx))
	}
	m.forwardToView(tui.AppendTranscriptMsg{Role: "tool_result_ok", ToolCallID: "call_1", Text: strings.Join(firstOutput, "\n")})
	m.forwardToView(tui.AppendTranscriptMsg{
		Role:       "tool_call",
		Text:       "second-command",
		ToolCallID: "call_2",
		ToolCall:   &transcript.ToolCallMeta{ToolName: "exec_command", IsShell: true, Command: "second-command"},
	})
	m.forwardToView(tui.AppendTranscriptMsg{Role: "tool_result_ok", ToolCallID: "call_2", Text: "second output"})
	m.forwardToView(tui.AppendTranscriptMsg{
		Role:       "tool_call",
		Text:       "third-command",
		ToolCallID: "call_3",
		ToolCall:   &transcript.ToolCallMeta{ToolName: "exec_command", IsShell: true, Command: "third-command"},
	})
	m.forwardToView(tui.AppendTranscriptMsg{Role: "tool_result_ok", ToolCallID: "call_3", Text: "third output"})
	for idx := 0; idx < 10; idx++ {
		m.forwardToView(tui.AppendTranscriptMsg{Role: "assistant", Text: fmt.Sprintf("tail entry %02d", idx)})
	}

	m = updateUIModel(t, m, tea.KeyMsg{Type: tea.KeyShiftTab})
	topVisible := 0
	ok := false
	for guard := 0; guard < 20; guard++ {
		topVisible, _, ok = m.view.DetailVisibleEntryRange()
		if ok && topVisible == 0 {
			break
		}
		m = updateUIModel(t, m, tea.KeyMsg{Type: tea.KeyPgUp})
	}
	topVisible, _, ok = m.view.DetailVisibleEntryRange()
	if !ok || topVisible != 0 {
		t.Fatalf("expected top command visible before expansion, range=(%d, ok=%v) view=%q", topVisible, ok, stripANSIAndTrimRight(m.view.View()))
	}
	m = updateUIModel(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	beforeScroll := m.view.DetailScroll()
	scrolled := 0
	for guard := 0; guard < 20 && scrolled < 5; guard++ {
		before := m.view.DetailScroll()
		m = updateUIModel(t, m, tea.KeyMsg{Type: tea.KeyDown})
		got := m.view.DetailScroll()
		if got == before {
			continue
		}
		scrolled++
		if want := before + 1; got != want {
			t.Fatalf("scroll step %d: expected detail arrow scroll by one line, got %d want %d", scrolled, got, want)
		}
		if selected := selectedDetailContentLine(t, m.view.View()); selected == "" {
			t.Fatalf("scroll step %d: expected center selection to remain visible", scrolled)
		}
	}
	if scrolled != 5 {
		t.Fatalf("expected five one-line scroll steps after selection walked to center, got %d from start scroll %d", scrolled, beforeScroll)
	}

	if selected := selectedDetailContentLine(t, m.view.View()); selected == "" {
		t.Fatal("expected centered selection after line scrolling")
	}
	if spacer := selectedDetailSpacerLine(t, m.view.View()); spacer == "" {
		t.Fatal("expected selected card spacer rail after line scrolling")
	}
}

func TestDetailModeReviewerSuggestionsCollapseAndExpandThroughUIModel(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.termWidth = 80
	m.termHeight = 12
	m.layout().syncViewport()
	m.forwardToView(tui.AppendTranscriptMsg{
		Role:          "reviewer_suggestions",
		Text:          "Supervisor suggested:\n1. Add app-level coverage.\n2. Rebuild before final answer.",
		CondensedText: "Supervisor made 2 suggestions.",
	})

	m = updateUIModel(t, m, tea.KeyMsg{Type: tea.KeyShiftTab})
	collapsed := stripANSIAndTrimRight(m.view.View())
	if !strings.Contains(collapsed, "Supervisor made 2 suggestions.") {
		t.Fatalf("expected collapsed reviewer suggestions summary, got %q", collapsed)
	}
	if strings.Contains(collapsed, "Add app-level coverage") || strings.Contains(collapsed, "Rebuild before final answer") {
		t.Fatalf("expected collapsed reviewer suggestions to hide full text, got %q", collapsed)
	}

	m = updateUIModel(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	expanded := stripANSIAndTrimRight(m.view.View())
	if !strings.Contains(expanded, "Add app-level coverage") || !strings.Contains(expanded, "Rebuild before final answer") {
		t.Fatalf("expected UI-routed enter to expand reviewer suggestions, got %q", expanded)
	}
}

func TestDetailModeEnterRoutesThroughInputControllerWhenInputLocked(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.termWidth = 80
	m.termHeight = 12
	m.layout().syncViewport()
	m.input = "locked draft"
	m.setInputSubmitLocked(true)
	m.lockedInjectText = "locked draft"
	m.forwardToView(tui.AppendTranscriptMsg{
		Role:       "tool_call",
		Text:       "cat large.txt",
		ToolCallID: "call_1",
		ToolCall:   &transcript.ToolCallMeta{ToolName: "exec_command", IsShell: true, Command: "cat large.txt"},
	})
	m.forwardToView(tui.AppendTranscriptMsg{Role: "tool_result_ok", ToolCallID: "call_1", Text: "line 1\nline 2"})
	m = updateUIModel(t, m, tea.KeyMsg{Type: tea.KeyShiftTab})

	controller := uiInputController{model: m}
	next, cmd := controller.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Fatalf("expected detail enter to be handled without command, got %T", cmd)
	}
	updated := next.(*uiModel)
	expanded := stripANSIAndTrimRight(updated.view.View())
	if !strings.Contains(expanded, "▼ cat large.txt") || !strings.Contains(expanded, "│ line 1") || !strings.Contains(expanded, "└ line 2") {
		t.Fatalf("expected input-controller enter to expand detail even while input locked, got %q", expanded)
	}
	if updated.input != "locked draft" || !updated.isInputSubmitLocked() || updated.lockedInjectText != "locked draft" {
		t.Fatalf("expected locked input state preserved, input=%q locked=%t inject=%q", updated.input, updated.isInputSubmitLocked(), updated.lockedInjectText)
	}
}

func TestDetailModeEnterDoesNotRequestTranscriptPage(t *testing.T) {
	client := &recordingTranscriptRuntimeClient{}
	m := newProjectedTestUIModel(client, closedProjectedRuntimeEvents(), closedAskEvents())
	m.termWidth = 80
	m.termHeight = 12
	m.layout().syncViewport()
	for idx := 0; idx < 4; idx++ {
		m.forwardToView(tui.AppendTranscriptMsg{Role: "assistant", Text: fmt.Sprintf("entry %02d\nhidden", idx)})
	}
	m.detailTranscript.replace(clientui.TranscriptPage{
		SessionID: "session-1",
		Entries:   []clientui.ChatEntry{{Role: "assistant", Text: "seed"}},
	})
	m = updateUIModel(t, m, tea.KeyMsg{Type: tea.KeyShiftTab})

	controller := uiInputController{model: m}
	next, cmd := controller.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Fatalf("expected detail enter to avoid transcript paging command, got %T", cmd)
	}
	_ = next
	if got := len(client.loadRequests); got != 0 {
		t.Fatalf("expected no transcript page loads on detail enter, got %d", got)
	}
}

func TestDetailModeMouseWheelScrollTranscript(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.termWidth = 80
	m.termHeight = 8
	m.layout().syncViewport()

	for i := 0; i < 16; i++ {
		m.forwardToView(tui.AppendTranscriptMsg{Role: "assistant", Text: fmt.Sprintf("line %d", i)})
	}
	m = updateUIModel(t, m, tea.KeyMsg{Type: tea.KeyShiftTab})

	initial := stripDetailSelectionRail(stripANSIAndTrimRight(m.view.View()))
	if initial == "" {
		t.Fatal("expected detail transcript visible before mouse scrolling")
	}

	m = updateUIModel(t, m, tea.MouseMsg{Button: tea.MouseButtonWheelUp})
	afterWheelUp := stripDetailSelectionRail(stripANSIAndTrimRight(m.view.View()))
	if afterWheelUp == initial {
		t.Fatal("expected detail transcript to change after mouse wheel up")
	}

	beforeWheelDownScroll := m.view.DetailScroll()
	m = updateUIModel(t, m, tea.MouseMsg{Button: tea.MouseButtonWheelDown})
	if got := m.view.DetailScroll(); got > beforeWheelDownScroll {
		t.Fatalf("expected detail wheel down after prior wheel up not to move away from bottom, got %d from %d", got, beforeWheelDownScroll)
	}
	if afterWheelDown := stripDetailSelectionRail(stripANSIAndTrimRight(m.view.View())); afterWheelDown == afterWheelUp {
		t.Fatal("expected detail transcript to change after mouse wheel down")
	}
}

func TestDetailModeUpAfterBottomScrollbackWalksHighlightOneVisualLine(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.termWidth = 80
	m.termHeight = 8
	m.layout().syncViewport()

	for idx := 0; idx < 24; idx++ {
		m.forwardToView(tui.AppendTranscriptMsg{Role: "assistant", Text: fmt.Sprintf("line %02d", idx)})
	}
	m = updateUIModel(t, m, tea.KeyMsg{Type: tea.KeyShiftTab})

	centerLine := m.termHeight / 2
	for guard := 0; guard < 12; guard++ {
		if selectedDetailLineIndex(t, m.view.View()) > centerLine {
			break
		}
		m = updateUIModel(t, m, tea.KeyMsg{Type: tea.KeyUp})
	}
	beforeLine := selectedDetailLineIndex(t, m.view.View())
	if beforeLine <= centerLine {
		t.Fatalf("expected bottom scrollback repro to place selected row below center, line=%d center=%d view=%q", beforeLine, centerLine, stripANSIAndTrimRight(m.view.View()))
	}
	beforeScroll := m.view.DetailScroll()

	m = updateUIModel(t, m, tea.KeyMsg{Type: tea.KeyUp})

	if got := m.view.DetailScroll(); got != beforeScroll {
		t.Fatalf("expected camera to hold while selected row walks toward center, got scroll %d want %d", got, beforeScroll)
	}
	if got := selectedDetailLineIndex(t, m.view.View()); got != beforeLine-1 {
		t.Fatalf("expected selected row to move up one visual line, got %d want %d view=%q", got, beforeLine-1, stripANSIAndTrimRight(m.view.View()))
	}
}

func TestDetailModeScrollThenEnterExpandsCenterSelectedItem(t *testing.T) {
	tests := []struct {
		name   string
		scroll tea.Msg
	}{
		{name: "mouse wheel", scroll: tea.MouseMsg{Button: tea.MouseButtonWheelUp}},
		{name: "page key", scroll: tea.KeyMsg{Type: tea.KeyPgUp}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newProjectedStaticUIModel()
			m.termWidth = 80
			m.termHeight = 8
			m.layout().syncViewport()

			for idx := 0; idx < 8; idx++ {
				callID := fmt.Sprintf("call_%d", idx)
				command := fmt.Sprintf("cmd %d", idx)
				m.forwardToView(tui.AppendTranscriptMsg{
					Role:       "tool_call",
					Text:       command,
					ToolCallID: callID,
					ToolCall:   &transcript.ToolCallMeta{ToolName: "exec_command", IsShell: true, Command: command},
				})
				m.forwardToView(tui.AppendTranscriptMsg{
					Role:       "tool_result_ok",
					ToolCallID: callID,
					Text:       fmt.Sprintf("output %d line 1\noutput %d line 2", idx, idx),
				})
			}
			m = updateUIModel(t, m, tea.KeyMsg{Type: tea.KeyShiftTab})
			m = updateUIModel(t, m, tt.scroll)

			selected := selectedDetailCommandIndex(t, m.view.View())
			m = updateUIModel(t, m, tea.KeyMsg{Type: tea.KeyEnter})

			expanded := stripANSIAndTrimRight(m.view.View())
			if !strings.Contains(expanded, fmt.Sprintf("▼ cmd %d", selected)) || !strings.Contains(expanded, fmt.Sprintf("└ output %d line 2", selected)) {
				t.Fatalf("expected enter after %s scroll to expand selected center command %d, got %q", tt.name, selected, expanded)
			}
		})
	}
}

func TestRollbackSelectionInDetailUsesPagedDetailWindow(t *testing.T) {
	m := newProjectedStaticUIModel(WithUIInitialTranscript([]UITranscriptEntry{
		{Role: "user", Text: "tail user"},
		{Role: "assistant", Text: "tail answer"},
	}))
	m.termWidth = 80
	m.termHeight = 10
	m.layout().syncViewport()
	detailPage := clientui.TranscriptPage{
		HasMoreAbove: true,
		Entries: []clientui.ChatEntry{
			{Role: "user", Text: "older user one"},
			{Role: "assistant", Text: "older answer one"},
			{Role: "user", Text: "older user two\nfull second line\nfull third line\nfull fourth line"},
			{Role: "assistant", Text: "older answer two"},
		},
	}
	m.detailTranscript.setKnownBounds(100, 104)
	m.detailTranscript.replace(detailPage)
	m.forwardToView(tui.SetModeMsg{Mode: tui.ModeDetail})
	m.forwardToView(tui.SetConversationMsg{
		BaseOffset:   m.detailTranscript.offset,
		TotalEntries: m.detailTranscript.totalEntries,
		Entries:      transcriptEntriesFromPage(detailPage),
	})
	seedTestRollbackTargets(m)

	m = updateUIModel(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	m = updateUIModel(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	if !testRollbackSelecting(m) {
		t.Fatal("expected rollback selection mode after double esc in detail")
	}
	if got := testRollbackCandidates(m); len(got) != 2 || got[0].TranscriptIndex != 100 || got[1].TranscriptIndex != 102 {
		t.Fatalf("expected paged detail user candidates, got %+v", got)
	}
	if testRollbackSelection(m) != 1 {
		t.Fatalf("expected newest detail user selected, got %d", testRollbackSelection(m))
	}

	view := stripANSIAndTrimRight(m.View())
	if !strings.Contains(view, "> older user two") || !strings.Contains(view, "full second line") || !strings.Contains(view, "full fourth line") {
		t.Fatalf("expected selected rollback message rendered in full with picker cursor, got %q", view)
	}
	if strings.Contains(view, "▶ older user two") || strings.Contains(view, "▼ older user two") {
		t.Fatalf("expected selected rollback cursor to avoid collapsed/expanded chevron, got %q", view)
	}

	m = updateUIModel(t, m, tea.KeyMsg{Type: tea.KeyUp})
	if testRollbackSelection(m) != 0 {
		t.Fatalf("expected up to jump to previous user message, got %d", testRollbackSelection(m))
	}
	m = updateUIModel(t, m, tea.KeyMsg{Type: tea.KeyDown})
	if testRollbackSelection(m) != 1 {
		t.Fatalf("expected down to jump to next user message, got %d", testRollbackSelection(m))
	}
	m = updateUIModel(t, m, tea.MouseMsg{Button: tea.MouseButtonWheelUp})
	if testRollbackSelection(m) != 1 {
		t.Fatalf("expected mouse wheel to be ignored in rollback picker, got %d", testRollbackSelection(m))
	}

	m = updateUIModel(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if !testRollbackEditing(m) || m.input != "older user two\nfull second line\nfull third line\nfull fourth line" {
		t.Fatalf("expected enter to start editing selected detail user, editing=%t input=%q", testRollbackEditing(m), m.input)
	}
	if m.nextForkRollbackTargetID != "" {
		t.Fatalf("did not expect fork before edit submission, got %q", m.nextForkRollbackTargetID)
	}

	m.input = "edited older user"
	m = updateUIModel(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	if !testRollbackSelecting(m) || testRollbackSelection(m) != 1 {
		t.Fatalf("expected esc from edit to return to picker with selection preserved, selecting=%t selection=%d", testRollbackSelecting(m), testRollbackSelection(m))
	}
}

func TestRollbackForkSubmissionUsesPagedDetailAbsoluteIndex(t *testing.T) {
	m := newProjectedStaticUIModel(WithUIInitialTranscript([]UITranscriptEntry{
		{Role: "user", Text: "tail user"},
		{Role: "assistant", Text: "tail answer"},
	}))
	m.termWidth = 80
	m.termHeight = 10
	m.layout().syncViewport()
	detailPage := clientui.TranscriptPage{
		HasMoreAbove: true,
		Entries: []clientui.ChatEntry{
			{Role: "user", Text: "first paged user"},
			{Role: "assistant", Text: "first answer"},
			{Role: "user", Text: "second paged user"},
			{Role: "assistant", Text: "second answer"},
		},
	}
	m.detailTranscript.setKnownBounds(100, 104)
	m.detailTranscript.replace(detailPage)
	m.forwardToView(tui.SetModeMsg{Mode: tui.ModeDetail})
	m.forwardToView(tui.SetConversationMsg{
		BaseOffset:   m.detailTranscript.offset,
		TotalEntries: m.detailTranscript.totalEntries,
		Entries:      transcriptEntriesFromPage(detailPage),
	})
	seedTestRollbackTargets(m)

	m = updateUIModel(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	m = updateUIModel(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	m = updateUIModel(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if !testRollbackEditing(m) {
		t.Fatal("expected rollback editing mode before fork submission")
	}

	m.input = "edited second paged user"
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated := next.(*uiModel)
	if updated.exitAction != UIActionForkRollback {
		t.Fatalf("expected fork rollback action, got %q", updated.exitAction)
	}
	if updated.nextForkRollbackTargetID == "" {
		t.Fatal("expected rollback target id")
	}
	if updated.nextSessionInitialPrompt != "edited second paged user" {
		t.Fatalf("expected edited prompt to be used for fork, got %q", updated.nextSessionInitialPrompt)
	}
}

func TestRollbackSelectionPagesBeforeCompactionTail(t *testing.T) {
	olderEntries := make([]clientui.ChatEntry, 100)
	for idx := range olderEntries {
		olderEntries[idx] = clientui.ChatEntry{Role: "assistant", Text: fmt.Sprintf("older answer %03d", idx)}
	}
	olderEntries[0] = clientui.ChatEntry{Role: "user", Text: "first ever user", RollbackTargetID: rollbackTargetIDForTestSelection(0)}
	olderEntries[98] = clientui.ChatEntry{Role: "user", Text: "pre-compaction user", RollbackTargetID: rollbackTargetIDForTestSelection(98)}
	client := &recordingTranscriptRuntimeClient{
		loadPage: clientui.TranscriptPage{
			SessionID:    "session-1",
			OlderCursor:  0,
			HasMoreAbove: false,
			Entries:      olderEntries,
		},
	}
	m := newProjectedTestUIModel(client, closedProjectedRuntimeEvents(), closedAskEvents())
	m.termWidth = 80
	m.termHeight = 10
	m.layout().syncViewport()
	tailEntries := make([]clientui.ChatEntry, 60)
	for idx := range tailEntries {
		tailEntries[idx] = clientui.ChatEntry{Role: "assistant", Text: fmt.Sprintf("post-compaction answer %03d", idx)}
	}
	tailEntries[0] = clientui.ChatEntry{Role: "user", Text: "post-compaction user", RollbackTargetID: rollbackTargetIDForTestSelection(100)}
	tailEntries[58] = clientui.ChatEntry{Role: "user", Text: "tail user", RollbackTargetID: rollbackTargetIDForTestSelection(158)}
	detailPage := clientui.TranscriptPage{
		SessionID:    "session-1",
		OlderCursor:  100,
		HasMoreAbove: true,
		Entries:      tailEntries,
	}
	m.detailTranscript.setKnownBounds(100, 160)
	m.detailTranscript.replace(detailPage)
	m.forwardToView(tui.SetModeMsg{Mode: tui.ModeDetail})
	m.forwardToView(tui.SetConversationMsg{
		BaseOffset:   m.detailTranscript.offset,
		TotalEntries: m.detailTranscript.totalEntries,
		Entries:      transcriptEntriesFromPage(detailPage),
	})
	seedTestRollbackTargets(m)

	m = updateUIModel(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	m = updateUIModel(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	if !testRollbackSelecting(m) {
		t.Fatal("expected rollback selection mode")
	}
	m = updateUIModel(t, m, tea.KeyMsg{Type: tea.KeyUp})
	if testRollbackSelection(m) != 0 {
		t.Fatalf("expected selection at oldest loaded candidate before paging, got %d", testRollbackSelection(m))
	}

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = next.(*uiModel)
	if cmd == nil {
		t.Fatal("expected rollback up at page edge to request previous transcript page")
	}
	for i := 0; i < 3; i++ {
		m = updateUIModel(t, m, tea.KeyMsg{Type: tea.KeyPgDown})
	}
	if got := m.view.DetailScroll(); got == 0 {
		t.Fatalf("expected page down during in-flight rollback page load to move detail scroll, got %d", got)
	}
	if strings.Contains(stripANSIAndTrimRight(m.view.View()), "post-compaction user") {
		t.Fatalf("expected page down to move current rollback point out of view, got %q", stripANSIAndTrimRight(m.view.View()))
	}
	next, duplicateCmd := m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = next.(*uiModel)
	if duplicateCmd != nil {
		t.Fatalf("expected in-flight rollback page request to suppress duplicate command, got %T", duplicateCmd())
	}
	if got := m.view.DetailScroll(); got != 0 {
		t.Fatalf("expected busy edge up fallback to refocus current rollback point, got detail scroll %d", got)
	}
	if !strings.Contains(stripANSIAndTrimRight(m.view.View()), "post-compaction user") {
		t.Fatalf("expected busy edge up fallback to show current rollback point, got %q", stripANSIAndTrimRight(m.view.View()))
	}
	msgs := collectCmdMessages(t, cmd)
	if len(msgs) != 1 {
		t.Fatalf("expected one transcript refresh message, got %#v", msgs)
	}
	refresh, ok := msgs[0].(runtimeTranscriptRefreshedMsg)
	if !ok {
		t.Fatalf("expected runtimeTranscriptRefreshedMsg, got %T", msgs[0])
	}
	if want := (clientui.TranscriptPageRequest{Cursor: 100}); refresh.req != want {
		t.Fatalf("previous page request = %+v, want %+v", refresh.req, want)
	}

	m = updateUIModel(t, m, refresh)
	candidates := testRollbackCandidates(m)
	if len(candidates) != 4 {
		t.Fatalf("expected merged rollback candidates across page boundary, got %+v", candidates)
	}
	if got := candidates[testRollbackSelection(m)].Text; got != "pre-compaction user" {
		t.Fatalf("expected selection to move before compaction tail, got %q candidates=%+v", got, candidates)
	}
}

type pagedRollbackRuntimeClient struct {
	runtimeControlFakeClient
	requests []clientui.TranscriptPageRequest
}

func (c *pagedRollbackRuntimeClient) LoadTranscriptPage(req clientui.TranscriptPageRequest) (clientui.TranscriptPage, error) {
	c.requests = append(c.requests, req)
	const pageSize = 250
	end := int(req.Cursor)
	if req.Cursor <= 0 {
		end = 1502
	}
	start := end - pageSize
	if start < 0 {
		start = 0
	}
	entries := make([]clientui.ChatEntry, end-start)
	for idx := range entries {
		absolute := start + idx
		entries[idx] = clientui.ChatEntry{Role: "assistant", Text: fmt.Sprintf("answer %04d", absolute)}
	}
	if len(entries) > 0 {
		entries[0] = clientui.ChatEntry{Role: "user", Text: fmt.Sprintf("user %04d", start), RollbackTargetID: rollbackTargetIDForTestSelection(start)}
	}
	return clientui.TranscriptPage{
		SessionID:    "session-1",
		OlderCursor:  int64(start),
		HasMoreAbove: start > 0,
		Entries:      entries,
	}, nil
}

func TestRollbackSelectionPagesToFirstUserAcrossTrimmedDetailWindow(t *testing.T) {
	client := &pagedRollbackRuntimeClient{}
	m := newProjectedTestUIModel(client, closedProjectedRuntimeEvents(), closedAskEvents())
	m.termWidth = 80
	m.termHeight = 10
	m.layout().syncViewport()
	tailEntries := make([]clientui.ChatEntry, 252)
	for idx := range tailEntries {
		absolute := 1250 + idx
		tailEntries[idx] = clientui.ChatEntry{Role: "assistant", Text: fmt.Sprintf("tail answer %04d", absolute)}
	}
	tailEntries[0] = clientui.ChatEntry{Role: "user", Text: "user 1250", RollbackTargetID: rollbackTargetIDForTestSelection(1250)}
	tailEntries[250] = clientui.ChatEntry{Role: "user", Text: "user 1500", RollbackTargetID: rollbackTargetIDForTestSelection(1500)}
	detailPage := clientui.TranscriptPage{
		SessionID:    "session-1",
		OlderCursor:  1250,
		HasMoreAbove: true,
		Entries:      tailEntries,
	}
	m.detailTranscript.setKnownBounds(1250, 1502)
	m.detailTranscript.replace(detailPage)
	m.forwardToView(tui.SetModeMsg{Mode: tui.ModeDetail})
	m.forwardToView(tui.SetConversationMsg{
		BaseOffset:   m.detailTranscript.offset,
		TotalEntries: m.detailTranscript.totalEntries,
		Entries:      transcriptEntriesFromPage(detailPage),
	})
	seedTestRollbackTargets(m)

	m = updateUIModel(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	m = updateUIModel(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	if !testRollbackSelecting(m) {
		t.Fatal("expected rollback selection mode")
	}
	for steps := 0; testRollbackCandidates(m)[testRollbackSelection(m)].TranscriptIndex != 0; steps++ {
		if steps > 20 {
			t.Fatalf("rollback selection did not reach first user, selection=%d candidates=%+v requests=%+v", testRollbackSelection(m), testRollbackCandidates(m), client.requests)
		}
		next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyUp})
		m = next.(*uiModel)
		if cmd == nil {
			continue
		}
		msgs := collectCmdMessages(t, cmd)
		if len(msgs) != 1 {
			t.Fatalf("expected one transcript page response, got %#v", msgs)
		}
		refresh, ok := msgs[0].(runtimeTranscriptRefreshedMsg)
		if !ok {
			t.Fatalf("expected runtimeTranscriptRefreshedMsg, got %T", msgs[0])
		}
		m = updateUIModel(t, m, refresh)
	}
	if len(client.requests) < 5 {
		t.Fatalf("expected multiple page requests across trimmed detail window, got %+v", client.requests)
	}
	if got := m.detailTranscript.offset; got != 0 {
		t.Fatalf("expected detail cache to include first page after paging to start, offset=%d", got)
	}
	if got := testRollbackCandidates(m)[testRollbackSelection(m)].Text; got != "user 0000" {
		t.Fatalf("expected selection at first user message, got %q", got)
	}
}

func TestRollbackTransitionsUseFixedDetailAltScreen(t *testing.T) {
	altOnEntry := true

	t.Run("ongoing_to_picker_to_edit_to_picker", func(t *testing.T) {
		m := newProjectedStaticUIModel(
			WithUIInitialTranscript([]UITranscriptEntry{
				{Role: "user", Text: "u1"},
				{Role: "assistant", Text: "a1"},
				{Role: "user", Text: "u2"},
			}),
		)
		m.termWidth = 80
		m.termHeight = 10
		m.layout().syncViewport()

		m = updateUIModel(t, m, tea.KeyMsg{Type: tea.KeyEsc})
		next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
		m = next.(*uiModel)
		if cmd == nil {
			t.Fatal("expected picker entry transition command")
		}
		if !testRollbackSelecting(m) || m.view.Mode() != tui.ModeDetail || m.altScreenActive != altOnEntry {
			t.Fatalf("unexpected picker entry state: selecting=%t mode=%q alt=%t", testRollbackSelecting(m), m.view.Mode(), m.altScreenActive)
		}
		m = updateUIModel(t, m, tea.MouseMsg{Button: tea.MouseButtonWheelUp})
		if testRollbackSelection(m) != 1 {
			t.Fatalf("expected mouse wheel ignored while selecting, got selection %d", testRollbackSelection(m))
		}

		next, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		m = next.(*uiModel)
		_ = collectCmdMessages(t, cmd)
		if !testRollbackEditing(m) || m.view.Mode() != tui.ModeDetail {
			t.Fatalf("unexpected edit state: editing=%t mode=%q", testRollbackEditing(m), m.view.Mode())
		}
		beforeScroll := m.view.DetailScroll()
		m = updateUIModel(t, m, tea.MouseMsg{Button: tea.MouseButtonWheelUp})
		if got := m.view.DetailScroll(); got != beforeScroll {
			t.Fatalf("expected mouse wheel ignored while editing, got scroll %d want %d", got, beforeScroll)
		}

		next, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
		m = next.(*uiModel)
		_ = collectCmdMessages(t, cmd)
		if !testRollbackSelecting(m) || m.view.Mode() != tui.ModeDetail || m.altScreenActive != altOnEntry {
			t.Fatalf("unexpected picker restore state: selecting=%t mode=%q alt=%t", testRollbackSelecting(m), m.view.Mode(), m.altScreenActive)
		}
	})

	t.Run("detail_to_picker_to_edit_to_picker", func(t *testing.T) {
		m := newProjectedStaticUIModel(
			WithUIInitialTranscript([]UITranscriptEntry{
				{Role: "user", Text: "u1"},
				{Role: "assistant", Text: "a1"},
				{Role: "user", Text: "u2"},
			}),
		)
		m.termWidth = 80
		m.termHeight = 10
		m.layout().syncViewport()
		m = updateUIModel(t, m, tea.KeyMsg{Type: tea.KeyShiftTab})
		if m.view.Mode() != tui.ModeDetail || m.altScreenActive != altOnEntry {
			t.Fatalf("unexpected detail state before picker: mode=%q alt=%t", m.view.Mode(), m.altScreenActive)
		}
		seedTestRollbackTargets(m)

		m = updateUIModel(t, m, tea.KeyMsg{Type: tea.KeyEsc})
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
		m = next.(*uiModel)
		if !testRollbackSelecting(m) || m.view.Mode() != tui.ModeDetail || m.altScreenActive != altOnEntry {
			t.Fatalf("unexpected detail picker state: selecting=%t mode=%q alt=%t", testRollbackSelecting(m), m.view.Mode(), m.altScreenActive)
		}

		next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		m = next.(*uiModel)
		if !testRollbackEditing(m) || m.view.Mode() != tui.ModeDetail {
			t.Fatalf("unexpected detail edit state: editing=%t mode=%q", testRollbackEditing(m), m.view.Mode())
		}
		beforeScroll := m.view.DetailScroll()
		m = updateUIModel(t, m, tea.MouseMsg{Button: tea.MouseButtonWheelUp})
		if got := m.view.DetailScroll(); got != beforeScroll {
			t.Fatalf("expected mouse wheel ignored while detail edit active, got scroll %d want %d", got, beforeScroll)
		}

		next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
		m = next.(*uiModel)
		if !testRollbackSelecting(m) || m.view.Mode() != tui.ModeDetail || m.altScreenActive != altOnEntry {
			t.Fatalf("unexpected detail picker restore state: selecting=%t mode=%q alt=%t", testRollbackSelecting(m), m.view.Mode(), m.altScreenActive)
		}
	})
}

func TestUpDownRouteByTranscriptMode(t *testing.T) {
	m := newProjectedStaticUIModel(WithUIPromptHistory([]string{"hello"}))
	m.termWidth = 80
	m.termHeight = 8
	m.layout().syncViewport()
	for i := 0; i < 20; i++ {
		m.forwardToView(tui.AppendTranscriptMsg{Role: "assistant", Text: fmt.Sprintf("line %d", i)})
	}

	ongoingStart := m.view.OngoingScroll()
	if ongoingStart == 0 {
		t.Fatal("expected ongoing transcript to be scrollable")
	}

	m = updateUIModel(t, m, tea.KeyMsg{Type: tea.KeyUp})
	if m.input != "hello" {
		t.Fatalf("expected ongoing mode up to recall prompt history, got %q", m.input)
	}
	if got := m.view.OngoingScroll(); got != ongoingStart {
		t.Fatalf("expected ongoing mode up not to scroll transcript, got %d from %d", got, ongoingStart)
	}

	m = updateUIModel(t, m, tea.KeyMsg{Type: tea.KeyShiftTab})
	if m.view.Mode() != tui.ModeDetail {
		t.Fatalf("expected detail mode, got %q", m.view.Mode())
	}
	initialDetail := m.view.View()

	m = updateUIModel(t, m, tea.KeyMsg{Type: tea.KeyUp})
	afterDetailUp := m.view.View()
	if afterDetailUp == initialDetail {
		t.Fatal("expected detail mode up to scroll transcript")
	}
	if m.input != "hello" {
		t.Fatalf("expected detail mode scrolling not to mutate recalled input, got %q", m.input)
	}
}

func selectedDetailCommandIndex(t *testing.T, view string) int {
	t.Helper()

	line := strings.TrimPrefix(selectedDetailContentLine(t, view), theme.SelectionRailGlyph)
	line = strings.TrimSpace(line)
	_, suffix, ok := strings.Cut(line, " cmd ")
	if !ok {
		t.Fatalf("expected selected command line, got %q in %q", line, stripANSIAndTrimRight(view))
	}
	value, parseErr := strconv.Atoi(strings.TrimSpace(suffix))
	if parseErr != nil {
		t.Fatalf("failed to parse selected command index from %q: %v", line, parseErr)
	}
	return value
}

func selectedDetailContentLine(t *testing.T, view string) string {
	t.Helper()

	for _, line := range strings.Split(stripANSIAndTrimRight(view), "\n") {
		if strings.HasPrefix(line, theme.SelectionRailGlyph) && strings.TrimSpace(strings.TrimPrefix(line, theme.SelectionRailGlyph)) != "" {
			return line
		}
	}
	t.Fatalf("expected selected detail line in %q", stripANSIAndTrimRight(view))
	return ""
}

func selectedDetailLineIndex(t *testing.T, view string) int {
	t.Helper()

	for idx, line := range strings.Split(stripANSIAndTrimRight(view), "\n") {
		if strings.HasPrefix(line, theme.SelectionRailGlyph) && strings.TrimSpace(strings.TrimPrefix(line, theme.SelectionRailGlyph)) != "" {
			return idx
		}
	}
	t.Fatalf("expected selected detail line in %q", stripANSIAndTrimRight(view))
	return -1
}

func selectedDetailSpacerLine(t *testing.T, view string) string {
	t.Helper()

	for _, line := range strings.Split(stripANSIAndTrimRight(view), "\n") {
		if strings.HasPrefix(line, theme.SelectionRailGlyph) && strings.TrimSpace(strings.TrimPrefix(line, theme.SelectionRailGlyph)) == "" {
			return line
		}
	}
	t.Fatalf("expected selected detail spacer line in %q", stripANSIAndTrimRight(view))
	return ""
}

func stripDetailSelectionRail(view string) string {
	lines := strings.Split(view, "\n")
	for idx, line := range lines {
		if strings.HasPrefix(line, theme.SelectionRailGlyph) {
			lines[idx] = theme.SelectionRailBlank + strings.TrimPrefix(line, theme.SelectionRailGlyph)
		}
	}
	return strings.Join(lines, "\n")
}

func TestMainInputUpDownAtBoundsStayInInput(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.termWidth = 80
	m.termHeight = 8
	m.layout().syncViewport()
	for i := 0; i < 20; i++ {
		m.forwardToView(tui.AppendTranscriptMsg{Role: "assistant", Text: fmt.Sprintf("line %d", i)})
	}
	m.input = "abcd"
	m.inputCursor = 2

	start := m.view.OngoingScroll()
	if start == 0 {
		t.Fatal("expected ongoing transcript to be scrollable")
	}

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyUp})
	updated := next.(*uiModel)
	if updated.inputCursor != 0 {
		t.Fatalf("expected first up to move cursor to start, got %d", updated.inputCursor)
	}
	if got := updated.view.OngoingScroll(); got != start {
		t.Fatalf("expected first up not to scroll transcript, got %d from %d", got, start)
	}

	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyUp})
	updated = next.(*uiModel)
	if updated.inputCursor != 0 {
		t.Fatalf("expected second up at top to stay at start, got %d", updated.inputCursor)
	}
	if got := updated.view.OngoingScroll(); got != start {
		t.Fatalf("expected second up at top not to scroll transcript, got %d from %d", got, start)
	}

	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyDown})
	updated = next.(*uiModel)
	if updated.inputCursor != len([]rune(updated.input)) {
		t.Fatalf("expected first down to move cursor to end, got %d", updated.inputCursor)
	}
	if got := updated.view.OngoingScroll(); got != start {
		t.Fatalf("expected first down not to scroll transcript, got %d from %d", got, start)
	}

	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyDown})
	updated = next.(*uiModel)
	if updated.inputCursor != len([]rune(updated.input)) {
		t.Fatalf("expected second down at end to stay at end, got %d", updated.inputCursor)
	}
	if got := updated.view.OngoingScroll(); got != start {
		t.Fatalf("expected second down at end not to scroll transcript, got %d from %d", got, start)
	}
}

func TestReviewerRunStillAllowsEditingWithoutTranscriptScroll(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.termWidth = 80
	m.termHeight = 8
	m.layout().syncViewport()
	for i := 0; i < 20; i++ {
		m.forwardToView(tui.AppendTranscriptMsg{Role: "assistant", Text: fmt.Sprintf("line %d", i)})
	}
	m.setBusy(true)
	m.activity = uiActivityRunning
	m.input = "keep this draft"

	start := m.view.OngoingScroll()
	if start == 0 {
		t.Fatal("expected ongoing transcript to be scrollable")
	}

	next, _ := m.Update(projectedRuntimeEventMsg(runtime.Event{Kind: runtime.EventReviewerStarted}))
	locked := next.(*uiModel)
	if !locked.isReviewerBlocking() {
		t.Fatal("expected reviewer running state")
	}
	if locked.isInputSubmitLocked() {
		t.Fatal("did not expect reviewer to lock input")
	}

	next, _ = locked.Update(tea.KeyMsg{Type: tea.KeyUp})
	locked = next.(*uiModel)

	next, _ = locked.Update(tea.KeyMsg{Type: tea.KeyUp})
	locked = next.(*uiModel)
	if locked.inputCursor != 0 {
		t.Fatalf("expected up to move cursor to start while reviewer runs, got %d", locked.inputCursor)
	}
	if got := locked.view.OngoingScroll(); got != start {
		t.Fatalf("expected up not to scroll transcript while reviewer runs, got %d from %d", got, start)
	}
	if locked.input != "keep this draft" {
		t.Fatalf("expected input text preserved while reviewer runs, got %q", locked.input)
	}

	next, _ = locked.Update(tea.KeyMsg{Type: tea.KeyDown})
	locked = next.(*uiModel)

	next, _ = locked.Update(tea.KeyMsg{Type: tea.KeyDown})
	locked = next.(*uiModel)
	if locked.inputCursor != len([]rune(locked.input)) {
		t.Fatalf("expected down to move cursor to end while reviewer runs, got %d", locked.inputCursor)
	}
	if got := locked.view.OngoingScroll(); got != start {
		t.Fatalf("expected down not to scroll transcript while reviewer runs, got %d from %d", got, start)
	}

	next, _ = locked.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	locked = next.(*uiModel)
	if locked.input != "keep this draftx" {
		t.Fatalf("expected input editable while reviewer runs, got %q", locked.input)
	}
}
