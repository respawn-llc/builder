package tui

import (
	"testing"

	"core/cli/tui/transcriptrender"
	"core/shared/clientui"
	"core/shared/transcript"

	tea "github.com/charmbracelet/bubbletea"
)

func TestDetailModeRendersHydratedCommittedRows(t *testing.T) {
	model := NewModel()
	next, _ := model.Update(SetDetailTranscriptPageMsg{Page: clientui.TranscriptPage{
		Entries: []clientui.ChatEntry{
			{Role: "user", Text: "hello from user"},
			{Role: "assistant", Text: "hello from assistant"},
		},
	}})
	model = next.(Model)
	next, _ = model.Update(SetModeMsg{Mode: ModeDetail})
	model = next.(Model)

	if model.Mode() != ModeDetail || !model.detailPageLoaded {
		t.Fatalf("detail mode/loaded = %s/%t, want detail/loaded", model.Mode(), model.detailPageLoaded)
	}
	if len(model.detailEntries) != 2 || model.detailEntries[0].Kind != clientui.TranscriptRowUser || model.detailEntries[1].Kind != clientui.TranscriptRowAssistant {
		t.Fatalf("detail entries = %#v, want user then assistant", model.detailEntries)
	}
	if selected, ok := model.selectedDetailIndex(); !ok || selected != 1 {
		t.Fatalf("selected detail entry = %d/%t, want 1/true", selected, ok)
	}
}

func TestDetailModeFiltersHiddenTranscriptEntries(t *testing.T) {
	model := NewModel()
	next, _ := model.Update(SetDetailTranscriptPageMsg{Page: clientui.TranscriptPage{
		Entries: []clientui.ChatEntry{
			{Visibility: clientui.EntryVisibilityOngoing, Role: "user", Text: "visible"},
			{Visibility: clientui.EntryVisibilityHidden, Role: "assistant", Text: "hidden"},
		},
	}})
	model = next.(Model)
	next, _ = model.Update(SetModeMsg{Mode: ModeDetail})
	model = next.(Model)

	if len(model.detailEntries) != 1 || model.detailEntries[0].Kind != clientui.TranscriptRowUser {
		t.Fatalf("detail entries = %#v, want visible user only", model.detailEntries)
	}
	if selected, ok := model.selectedDetailIndex(); !ok || selected != 0 {
		t.Fatalf("selected detail entry = %d/%t, want 0/true", selected, ok)
	}
}

func TestDetailModeCachedRowsPreserveVisibility(t *testing.T) {
	model := NewModel()
	model.detailEntries = []detailEntry{newDetailEntry(clientui.TranscriptCommittedRow{
		Visibility: clientui.EntryVisibilityOngoingCollapsed,
		Kind:       clientui.TranscriptRowNotice,
		Notice: &clientui.TranscriptNoticeRow{
			Severity: clientui.TranscriptNoticeInfo,
			Data:     clientui.TranscriptNoticeData{CompactLabel: "compact notice"},
			Diagnostic: &clientui.TranscriptDiagnosticData{
				Detail: "diagnostic detail",
			},
		},
	})}
	model.detailPageLoaded = true
	model.setSelectedDetailIndex(0)
	next, _ := model.Update(SetModeMsg{Mode: ModeDetail})
	model = next.(Model)

	if len(model.detailEntries) != 1 || model.detailEntries[0].Visibility != clientui.EntryVisibilityOngoingCollapsed {
		t.Fatalf("detail entries = %#v, want preserved ongoing-collapsed visibility", model.detailEntries)
	}
	if selected, ok := model.selectedDetailIndex(); !ok || selected != 0 {
		t.Fatalf("selected detail entry = %d/%t, want 0/true", selected, ok)
	}
}

func TestDetailModeExpandsSelectedEntry(t *testing.T) {
	model := NewModel()
	next, _ := model.Update(SetDetailTranscriptPageMsg{Page: clientui.TranscriptPage{
		Entries: []clientui.ChatEntry{{Role: "assistant", Text: "line one\nline two\nline three\nline four"}},
	}})
	model = next.(Model)
	next, _ = model.Update(SetModeMsg{Mode: ModeDetail})
	model = next.(Model)
	if _, ok := model.expanded[0]; ok {
		t.Fatal("detail entry starts expanded")
	}

	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = next.(Model)
	if _, ok := model.expanded[0]; !ok {
		t.Fatal("selected detail entry was not expanded")
	}
}

func TestDetailSelectionChangesOnlySymbolText(t *testing.T) {
	model := NewModel()
	model.viewportWidth = 80
	model.expanded = map[int]struct{}{0: {}}
	line := transcriptrender.Line{Spans: []transcriptrender.Span{
		{Text: "!", Role: transcriptrender.StyleRoleToolError},
		{Text: " ", Role: transcriptrender.StyleRoleToolShell, Faint: true},
		{Text: "false", Role: transcriptrender.StyleRoleToolShell, Faint: true},
	}}

	decorated := model.decorateSelectedDetailLines([]transcriptrender.Line{line}, 0)
	if len(decorated) != 1 || len(decorated[0].Spans) != len(line.Spans) {
		t.Fatalf("decorated lines = %+v", decorated)
	}
	original := line.Spans[0]
	symbol := decorated[0].Spans[0]
	if symbol.Text == original.Text {
		t.Fatalf("selected symbol text was not decorated: %+v", symbol)
	}
	symbol.Text = original.Text
	if symbol != original {
		t.Fatalf("selection changed symbol metadata: got %+v, want %+v", symbol, original)
	}
}

func TestDetailSelectionActionReflectsSelectedExpansionState(t *testing.T) {
	model := NewModel()
	next, _ := model.Update(SetDetailTranscriptPageMsg{Page: clientui.TranscriptPage{
		Entries: []clientui.ChatEntry{{Role: "assistant", Text: "line one\nline two"}},
	}})
	model = next.(Model)
	next, _ = model.Update(SetModeMsg{Mode: ModeDetail})
	model = next.(Model)

	if got := model.DetailSelectionAction(); got != DetailSelectionActionExpand {
		t.Fatalf("detail action = %v, want expand", got)
	}
	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = next.(Model)
	if got := model.DetailSelectionAction(); got != DetailSelectionActionCollapse {
		t.Fatalf("detail action = %v, want collapse", got)
	}
	next, _ = model.Update(SetModeMsg{Mode: ModeOngoing})
	model = next.(Model)
	if got := model.DetailSelectionAction(); got != DetailSelectionActionNone {
		t.Fatalf("ongoing detail action = %v, want none", got)
	}
}

func TestDetailChatEntryNoticeKeepsCompactTextSeparateFromExpandedBody(t *testing.T) {
	row, ok := detailRowFromChatEntry(clientui.ChatEntry{
		Role:          "compaction_notice",
		Text:          "  full persisted notice body  ",
		CondensedText: "compact notice",
	})
	if !ok {
		t.Fatal("notice chat entry was dropped")
	}
	if row.Kind != clientui.TranscriptRowNotice || row.Notice == nil {
		t.Fatalf("row = %#v, want notice row", row)
	}
	if row.Notice.Data.LegacyText == nil || *row.Notice.Data.LegacyText != "  full persisted notice body  " {
		t.Fatalf("notice legacy text = %#v, want full body", row.Notice.Data.LegacyText)
	}
	if row.Notice.Data.CondensedText != "compact notice" {
		t.Fatalf("notice condensed text = %q, want compact metadata", row.Notice.Data.CondensedText)
	}
	expanded := transcriptrender.RenderCommittedRow(row, 80, "", transcriptrender.ModeDetailExpanded)
	if len(expanded.Lines) != 1 || len(expanded.Lines[0].Spans) < 3 {
		t.Fatalf("expanded notice row = %#v, want prefixed body span", expanded)
	}
	if got := expanded.Lines[0].Spans[2].Text; got != "  full persisted notice body  " {
		t.Fatalf("expanded notice body span = %q, want full body", got)
	}
}

func TestDetailChatEntryNoticeMapsLegacySeverityRoles(t *testing.T) {
	cases := []struct {
		role         string
		wantSeverity clientui.TranscriptNoticeSeverity
		wantReason   clientui.TranscriptNoticeReason
	}{
		{role: "error", wantSeverity: clientui.TranscriptNoticeError, wantReason: clientui.TranscriptNoticeLegacyUntypedNotice},
		{role: "warning", wantSeverity: clientui.TranscriptNoticeWarning, wantReason: clientui.TranscriptNoticeLegacyUntypedNotice},
		{role: "cache_warning", wantSeverity: clientui.TranscriptNoticeWarning, wantReason: clientui.TranscriptNoticeCacheWarning},
	}

	for _, tt := range cases {
		t.Run(tt.role, func(t *testing.T) {
			row, ok := detailRowFromChatEntry(clientui.ChatEntry{Role: tt.role, Text: "legacy notice"})
			if !ok {
				t.Fatal("notice chat entry was dropped")
			}
			if row.Kind != clientui.TranscriptRowNotice || row.Notice == nil {
				t.Fatalf("row = %#v, want notice row", row)
			}
			if row.Notice.Severity != tt.wantSeverity || row.Notice.Reason != tt.wantReason {
				t.Fatalf("notice severity/reason = %s/%s, want %s/%s", row.Notice.Severity, row.Notice.Reason, tt.wantSeverity, tt.wantReason)
			}
		})
	}
}

func TestWorkflowModeChatEntryRendersCollapsedOngoingAndExpandedDetail(t *testing.T) {
	row, ok := detailRowFromChatEntry(clientui.ChatEntry{
		Visibility:    clientui.EntryVisibilityOngoingCollapsed,
		Role:          "developer_context",
		Text:          "full workflow instructions\nwith second line",
		CondensedText: "workflow compact",
		MessageType:   clientui.MessageTypeWorkflowMode,
	})
	if !ok {
		t.Fatal("workflow chat entry was dropped")
	}
	if row.Visibility != clientui.EntryVisibilityOngoingCollapsed || row.Kind != clientui.TranscriptRowNotice || row.Notice == nil {
		t.Fatalf("workflow row = %#v, want ongoing-collapsed notice", row)
	}

	ongoing := transcriptrender.RenderCommittedRow(row, 80, "", transcriptrender.ModeOngoingCollapsed)
	if len(ongoing.Lines) != 1 {
		t.Fatalf("ongoing workflow lines = %d, want compact single line", len(ongoing.Lines))
	}
	if got, want := ongoing.Lines[0].Plain(), "ℹ workflow compact"; got != want {
		t.Fatalf("ongoing workflow line = %q, want %q", got, want)
	}
	if len(ongoing.Lines[0].Spans) < 3 || ongoing.Lines[0].Spans[2].Role != transcriptrender.StyleRoleNoticePrimary {
		t.Fatalf("ongoing workflow spans = %+v, want primary notice body", ongoing.Lines[0].Spans)
	}

	detail := transcriptrender.RenderCommittedRow(row, 80, "", transcriptrender.ModeDetailExpanded)
	if len(detail.Lines) != 2 {
		t.Fatalf("detail workflow lines = %d, want full body lines", len(detail.Lines))
	}
	if got, want := detail.Lines[0].Plain(), "ℹ full workflow instructions"; got != want {
		t.Fatalf("detail workflow first line = %q, want %q", got, want)
	}
	if got, want := detail.Lines[1].Plain(), "└ with second line"; got != want {
		t.Fatalf("detail workflow continuation = %q, want %q", got, want)
	}
}

func TestDetailChatEntryToolCallRendersAsToolRow(t *testing.T) {
	const callID = "1bcbb5bd-f688-4e64-8a35-89f6b0706cf1"
	row, ok := detailRowFromChatEntry(clientui.ChatEntry{
		Role:       "tool_call",
		ToolCallID: callID,
		ToolCall: &clientui.ToolCallMeta{
			ToolName:    "exec_command",
			IsShell:     true,
			CompactText: "run tests",
			Command:     "./scripts/test.sh ./cli/tui",
		},
	})
	if !ok {
		t.Fatal("tool_call chat entry was dropped")
	}
	if row.Kind != clientui.TranscriptRowTool || row.Tool == nil {
		t.Fatalf("row = %#v, want tool row", row)
	}
	if row.Tool.ToolCallID != callID || row.Tool.ToolName != "exec_command" {
		t.Fatalf("tool row identity = %#v, want persisted tool call identity", row.Tool)
	}
	if row.Tool.ToolPresentation == nil || row.Tool.ToolPresentation.CompactText != "run tests" {
		t.Fatalf("tool presentation = %#v, want compact call metadata", row.Tool.ToolPresentation)
	}
}

func TestDetailReviewerEntriesPreserveDiagnosticRoles(t *testing.T) {
	for _, role := range []transcript.EntryRole{transcript.EntryRoleReviewerStatus, transcript.EntryRoleReviewerError} {
		t.Run(string(role), func(t *testing.T) {
			row, ok := detailRowFromChatEntry(clientui.ChatEntry{
				Role:        string(role),
				Text:        "review result",
				MessageType: clientui.MessageTypeReviewerFeedback,
			})
			if !ok || row.Notice == nil || row.Notice.Diagnostic == nil {
				t.Fatalf("reviewer row = %+v, ok=%t", row, ok)
			}
			if row.Notice.Diagnostic.Code != string(role) {
				t.Fatalf("reviewer diagnostic role = %q, want %q", row.Notice.Diagnostic.Code, role)
			}
		})
	}
}
