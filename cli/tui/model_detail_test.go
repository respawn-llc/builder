package tui

import (
	"testing"

	"core/cli/tui/transcriptrender"
	"core/shared/clientui"
	"core/shared/rollbacktarget"
	"core/shared/transcript"

	tea "github.com/charmbracelet/bubbletea"
)

func TestRollbackTargetSelectionHighlightsAndCentersDetailEntry(t *testing.T) {
	targetID := rollbacktarget.EncodeUserMessageSeq(4)
	rows := make([]clientui.TranscriptCommittedRow, 0, 9)
	for index := 0; index < 9; index++ {
		row := detailUser(string(rune('a' + index)))
		if index == 3 {
			target := targetID
			row.User.RollbackTargetID = &target
		}
		rows = append(rows, row)
	}
	model := NewModel()
	next, _ := model.Update(SetViewportSizeMsg{Lines: 5, Width: 80})
	model = next.(Model)
	next, _ = model.Update(SetDetailTranscriptPageMsg{
		Page:   clientui.TranscriptPage{Entries: rows},
		Anchor: DetailTranscriptAnchorBottom,
	})
	model = next.(Model)
	next, _ = model.Update(SelectDetailTranscriptRollbackTargetMsg{
		RollbackTargetID: targetID,
		Center:           true,
	})
	model = next.(Model)

	selected, ok := model.selectedDetailIndex()
	if !ok || selected != 3 {
		t.Fatalf("selected detail index = %d/%t, want rollback target index 3", selected, ok)
	}
	lineRange, ok := model.detailEntryLineRange(selected)
	if !ok {
		t.Fatal("selected rollback target has no detail line range")
	}
	selectedCenter := (lineRange.first + lineRange.last) / 2
	viewportCenter := model.detailScroll + model.viewportLines/2
	if absInt(selectedCenter-viewportCenter) > 1 {
		t.Fatalf("selected line center = %d, viewport center = %d", selectedCenter, viewportCenter)
	}
	highlighted := false
	for _, line := range model.detailVisibleProjectedLines() {
		if line.EntryIndex == selected && line.Rail == detailRailSelected && line.SelectedFill {
			highlighted = true
			break
		}
	}
	if !highlighted {
		t.Fatal("selected rollback target was not highlighted in the detail viewport")
	}
}

func TestDetailModeRendersHydratedCommittedRows(t *testing.T) {
	model := NewModel()
	next, _ := model.Update(SetDetailTranscriptPageMsg{Page: clientui.TranscriptPage{
		Entries: []clientui.TranscriptCommittedRow{
			detailUser("hello from user"),
			detailAssistant("hello from assistant"),
		},
	}})
	model = next.(Model)
	next, _ = model.Update(SetModeMsg{Mode: ModeDetail})
	model = next.(Model)

	if model.Mode() != ModeDetail || !model.detailPageLoaded {
		t.Fatalf("detail mode/loaded = %s/%t, want detail/loaded", model.Mode(), model.detailPageLoaded)
	}
	if len(model.detailProjection.entries) != 2 || model.detailProjection.entries[0].row().Kind != clientui.TranscriptRowUser || model.detailProjection.entries[1].row().Kind != clientui.TranscriptRowAssistant {
		t.Fatalf("detail entries = %#v, want user then assistant", model.detailProjection.entries)
	}
	if selected, ok := model.selectedDetailIndex(); !ok || selected != 1 {
		t.Fatalf("selected detail entry = %d/%t, want 1/true", selected, ok)
	}
}

func TestDetailModeFiltersHiddenTranscriptEntries(t *testing.T) {
	model := NewModel()
	visible := detailUser("visible")
	visible.Visibility = clientui.EntryVisibilityOngoing
	hidden := detailAssistant("hidden")
	hidden.Visibility = clientui.EntryVisibilityHidden
	next, _ := model.Update(SetDetailTranscriptPageMsg{Page: clientui.TranscriptPage{
		Entries: []clientui.TranscriptCommittedRow{
			visible,
			hidden,
		},
	}})
	model = next.(Model)
	next, _ = model.Update(SetModeMsg{Mode: ModeDetail})
	model = next.(Model)

	if len(model.detailProjection.entries) != 1 || model.detailProjection.entries[0].row().Kind != clientui.TranscriptRowUser {
		t.Fatalf("detail entries = %#v, want visible user only", model.detailProjection.entries)
	}
	if selected, ok := model.selectedDetailIndex(); !ok || selected != 0 {
		t.Fatalf("selected detail entry = %d/%t, want 0/true", selected, ok)
	}
}

func TestDetailEmptyMalformedUserEntryRemainsDetailOnlyAndNonExpandable(t *testing.T) {
	entry, ok := detailTestEntryFromCommittedRow(clientui.TranscriptCommittedRow{
		Visibility: clientui.EntryVisibilityDetail,
		Integrity:  transcript.RowIntegrityUnrecoverableMalformed,
		Kind:       clientui.TranscriptRowUser,
		User:       &clientui.TranscriptUserRow{},
	})
	if !ok {
		t.Fatal("empty malformed user entry was dropped")
	}
	if entry.row().Visibility != clientui.EntryVisibilityDetail {
		t.Fatalf("empty malformed user visibility = %q, want detail-only", entry.row().Visibility)
	}
	presentation := entry.presentation()
	if presentation.Expandable || len(presentation.Collapsed) == 0 {
		t.Fatalf("empty malformed user presentation = %+v, want visible non-expandable diagnostic", presentation)
	}
}

func TestDetailUnrecoverableMalformedEntriesStayVisibleAsDetailOnlyDiagnostics(t *testing.T) {
	cases := []struct {
		name string
		row  clientui.TranscriptCommittedRow
		kind clientui.TranscriptRowKind
	}{
		{
			name: "user",
			row: clientui.TranscriptCommittedRow{
				Visibility: clientui.EntryVisibilityDetail,
				Integrity:  transcript.RowIntegrityUnrecoverableMalformed,
				Kind:       clientui.TranscriptRowUser,
				User:       &clientui.TranscriptUserRow{},
			},
			kind: clientui.TranscriptRowUser,
		},
		{
			name: "assistant",
			row: clientui.TranscriptCommittedRow{
				Visibility: clientui.EntryVisibilityDetail,
				Integrity:  transcript.RowIntegrityUnrecoverableMalformed,
				Kind:       clientui.TranscriptRowAssistant,
				Assistant:  &clientui.TranscriptAssistantRow{},
			},
			kind: clientui.TranscriptRowAssistant,
		},
		{
			name: "tool",
			row: clientui.TranscriptCommittedRow{
				Visibility: clientui.EntryVisibilityDetail,
				Integrity:  transcript.RowIntegrityUnrecoverableMalformed,
				Kind:       clientui.TranscriptRowTool,
				Tool:       &clientui.TranscriptToolRow{},
			},
			kind: clientui.TranscriptRowTool,
		},
		{
			name: "notice",
			row: clientui.TranscriptCommittedRow{
				Visibility: clientui.EntryVisibilityDetail,
				Integrity:  transcript.RowIntegrityUnrecoverableMalformed,
				Kind:       clientui.TranscriptRowNotice,
				Notice:     &clientui.TranscriptNoticeRow{},
			},
			kind: clientui.TranscriptRowNotice,
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			entry, ok := detailTestEntryFromCommittedRow(tt.row)
			if !ok {
				t.Fatal("unrecoverable malformed entry was dropped")
			}
			if row := entry.row(); row.Visibility != clientui.EntryVisibilityDetail || row.Kind != tt.kind {
				t.Fatalf("unrecoverable row = %+v, want detail-only %q", row, tt.kind)
			}
			presentation := entry.presentation()
			if presentation.Expandable || len(presentation.Collapsed) == 0 {
				t.Fatalf("unrecoverable presentation = %+v, want visible non-expandable diagnostic", presentation)
			}
		})
	}
}

func TestDetailRecoverableMalformedEntriesStayOngoingAndExpandable(t *testing.T) {
	cases := []struct {
		name string
		row  clientui.TranscriptCommittedRow
	}{
		{
			name: "user",
			row: clientui.TranscriptCommittedRow{
				Visibility: clientui.EntryVisibilityOngoing,
				Integrity:  transcript.RowIntegrityRecoverableMalformed,
				Kind:       clientui.TranscriptRowUser,
				User:       &clientui.TranscriptUserRow{CondensedText: "legacy user"},
			},
		},
		{
			name: "assistant",
			row: clientui.TranscriptCommittedRow{
				Visibility: clientui.EntryVisibilityOngoing,
				Integrity:  transcript.RowIntegrityRecoverableMalformed,
				Kind:       clientui.TranscriptRowAssistant,
				Assistant:  &clientui.TranscriptAssistantRow{CondensedText: "legacy assistant"},
			},
		},
		{
			name: "tool",
			row: clientui.TranscriptCommittedRow{
				Visibility: clientui.EntryVisibilityOngoing,
				Integrity:  transcript.RowIntegrityRecoverableMalformed,
				Kind:       clientui.TranscriptRowTool,
				Tool:       &clientui.TranscriptToolRow{Text: "legacy tool"},
			},
		},
		{
			name: "notice",
			row: clientui.TranscriptCommittedRow{
				Visibility: clientui.EntryVisibilityOngoing,
				Integrity:  transcript.RowIntegrityRecoverableMalformed,
				Kind:       clientui.TranscriptRowNotice,
				Notice: &clientui.TranscriptNoticeRow{
					Reason: clientui.TranscriptNoticeLegacyUntypedNotice,
					Data:   clientui.TranscriptNoticeData{CompactLabel: "legacy notice"},
				},
			},
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			entry, ok := detailTestEntryFromCommittedRow(tt.row)
			if !ok {
				t.Fatal("recoverable malformed entry was dropped")
			}
			if row := entry.row(); row.Visibility != clientui.EntryVisibilityOngoing {
				t.Fatalf("recoverable row visibility = %q, want ongoing", row.Visibility)
			}
			if presentation := entry.presentation(); !presentation.Expandable {
				t.Fatalf("recoverable malformed presentation is not expandable: %+v", presentation)
			}
		})
	}
}

func TestDetailModeCachedRowsPreserveVisibility(t *testing.T) {
	model := NewModel()
	cached := detailNotice(clientui.TranscriptNoticeRow{
		Severity: clientui.TranscriptNoticeInfo,
		Data:     clientui.TranscriptNoticeData{CompactLabel: "compact notice"},
		Diagnostic: &clientui.TranscriptDiagnosticData{
			Detail: "diagnostic detail",
		},
	})
	cached.Visibility = clientui.EntryVisibilityOngoingCollapsed
	next, _ := model.Update(SetDetailTranscriptPageMsg{Page: clientui.TranscriptPage{
		Entries: []clientui.TranscriptCommittedRow{cached},
	}})
	model = next.(Model)
	next, _ = model.Update(SetModeMsg{Mode: ModeDetail})
	model = next.(Model)

	if len(model.detailProjection.entries) != 1 || model.detailProjection.entries[0].row().Visibility != clientui.EntryVisibilityOngoingCollapsed {
		t.Fatalf("detail entries = %#v, want preserved ongoing-collapsed visibility", model.detailProjection.entries)
	}
	if selected, ok := model.selectedDetailIndex(); !ok || selected != 0 {
		t.Fatalf("selected detail entry = %d/%t, want 0/true", selected, ok)
	}
}

func TestDetailModeExpandsSelectedEntry(t *testing.T) {
	model := NewModel()
	next, _ := model.Update(SetDetailTranscriptPageMsg{Page: clientui.TranscriptPage{
		Entries: []clientui.TranscriptCommittedRow{detailAssistant("line one\nline two\nline three\nline four")},
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
	leading := transcriptrender.SemanticSpan("!", transcriptrender.StyleRoleToolError)
	line := transcriptrender.Line{LeadingSymbol: &leading, Spans: []transcriptrender.Span{
		transcriptrender.SemanticSpan(" ", transcriptrender.StyleRoleToolShell, transcriptrender.SpanAttributeFaint),
		transcriptrender.SemanticSpan("false", transcriptrender.StyleRoleToolShell, transcriptrender.SpanAttributeFaint),
	}}

	decorated := model.decorateSelectedDetailLines([]transcriptrender.Line{line}, 0, model.viewportWidth-1)
	if len(decorated) != 1 || len(decorated[0].Spans) != len(line.Spans) {
		t.Fatalf("decorated lines = %+v", decorated)
	}
	if decorated[0].LeadingSymbol == nil {
		t.Fatalf("decorated line has no typed symbol: %+v", decorated[0])
	}
	original := *line.LeadingSymbol
	symbol := *decorated[0].LeadingSymbol
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
		Entries: []clientui.TranscriptCommittedRow{detailAssistant("line one\nline two\nline three\nline four")},
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

func TestDetailNonExpandableSelectionHasNoActionOrEnterMutation(t *testing.T) {
	model := NewModel()
	next, _ := model.Update(SetDetailTranscriptPageMsg{Page: clientui.TranscriptPage{
		Entries: []clientui.TranscriptCommittedRow{detailUser("short row")},
	}})
	model = next.(Model)
	next, _ = model.Update(SetModeMsg{Mode: ModeDetail})
	model = next.(Model)

	if got := model.DetailSelectionAction(); got != DetailSelectionActionNone {
		t.Fatalf("detail action = %v, want none for non-expandable row", got)
	}
	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = next.(Model)
	if _, expanded := model.expanded[0]; expanded {
		t.Fatal("Enter mutated expansion state for non-expandable row")
	}
	presentation := model.detailProjection.entries[0].presentation()
	if presentation.Collapsed[0].LeadingSymbol == nil {
		t.Fatalf("short row has no typed role symbol: %+v", presentation)
	}
	if got := model.detailRenderedLines()[0].Text; got == "" {
		t.Fatal("non-expandable selected row rendered no content")
	}
}

func TestDetailNoticeKeepsCompactTextSeparateFromExpandedBody(t *testing.T) {
	fullBody := "  full persisted notice body  "
	entry, ok := detailTestEntryFromCommittedRow(detailNotice(clientui.TranscriptNoticeRow{
		Reason:   clientui.TranscriptNoticeLegacyUntypedNotice,
		Severity: clientui.TranscriptNoticeInfo,
		Data: clientui.TranscriptNoticeData{
			LegacyText:    &fullBody,
			CondensedText: "compact notice",
		},
	}))
	if !ok {
		t.Fatal("notice row was dropped")
	}
	row := entry.row()
	if row.Kind != clientui.TranscriptRowNotice || row.Notice == nil {
		t.Fatalf("row = %#v, want notice row", row)
	}
	if row.Notice.Data.LegacyText == nil || *row.Notice.Data.LegacyText != fullBody {
		t.Fatalf("notice legacy text = %#v, want full body", row.Notice.Data.LegacyText)
	}
	if row.Notice.Data.CondensedText != "compact notice" {
		t.Fatalf("notice condensed text = %q, want compact metadata", row.Notice.Data.CondensedText)
	}
	expanded := transcriptrender.RenderCommittedRow(row, 80, "", transcriptrender.ModeDetailExpanded)
	if len(expanded.Lines) != 1 || expanded.Lines[0].LeadingSymbol == nil || len(expanded.Lines[0].Spans) < 2 {
		t.Fatalf("expanded notice row = %#v, want prefixed body span", expanded)
	}
	if got := expanded.Lines[0].Spans[1].Text; got != fullBody {
		t.Fatalf("expanded notice body span = %q, want full body", got)
	}
}

func TestDetailNoticePreservesServerProjectedSeverityAndReason(t *testing.T) {
	cases := []struct {
		name         string
		wantSeverity clientui.TranscriptNoticeSeverity
		wantReason   clientui.TranscriptNoticeReason
	}{
		{name: "error", wantSeverity: clientui.TranscriptNoticeError, wantReason: clientui.TranscriptNoticeLegacyUntypedNotice},
		{name: "warning", wantSeverity: clientui.TranscriptNoticeWarning, wantReason: clientui.TranscriptNoticeLegacyUntypedNotice},
		{name: "cache warning", wantSeverity: clientui.TranscriptNoticeWarning, wantReason: clientui.TranscriptNoticeCacheWarning},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			entry, ok := detailTestEntryFromCommittedRow(detailNotice(clientui.TranscriptNoticeRow{
				Reason:   tt.wantReason,
				Severity: tt.wantSeverity,
				Data:     clientui.TranscriptNoticeData{CompactLabel: "notice"},
			}))
			if !ok {
				t.Fatal("notice row was dropped")
			}
			row := entry.row()
			if row.Kind != clientui.TranscriptRowNotice || row.Notice == nil {
				t.Fatalf("row = %#v, want notice row", row)
			}
			if row.Notice.Severity != tt.wantSeverity || row.Notice.Reason != tt.wantReason {
				t.Fatalf("notice severity/reason = %s/%s, want %s/%s", row.Notice.Severity, row.Notice.Reason, tt.wantSeverity, tt.wantReason)
			}
		})
	}
}

func TestWorkflowModeNoticeRendersCollapsedOngoingAndExpandedDetail(t *testing.T) {
	fullBody := "full workflow instructions\nwith second line"
	row := detailNotice(clientui.TranscriptNoticeRow{
		Severity: clientui.TranscriptNoticeInfo,
		Data: clientui.TranscriptNoticeData{
			LegacyText:    &fullBody,
			CondensedText: "workflow compact",
			MessageType:   clientui.MessageTypeWorkflowMode,
		},
	})
	row.Visibility = clientui.EntryVisibilityOngoingCollapsed
	entry, ok := detailTestEntryFromCommittedRow(row)
	if !ok {
		t.Fatal("workflow row was dropped")
	}
	row = entry.row()
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
	if ongoing.Lines[0].LeadingSymbol == nil ||
		len(ongoing.Lines[0].Spans) < 2 ||
		ongoing.Lines[0].Spans[1].Style.SemanticRole != transcriptrender.StyleRoleNoticePrimary {
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

func TestDetailToolRowPreservesToolCallData(t *testing.T) {
	const callID = "1bcbb5bd-f688-4e64-8a35-89f6b0706cf1"
	entry, ok := detailTestEntryFromCommittedRow(detailTool(clientui.TranscriptToolRow{
		ToolCallID: callID,
		ToolName:   "exec_command",
		ToolPresentation: &clientui.ToolCallMeta{
			ToolName:     "exec_command",
			Presentation: clientui.ToolPresentationShell,
			CompactText:  "run tests",
			Command:      "./scripts/test.sh ./cli/tui",
		},
	}))
	if !ok {
		t.Fatal("tool row was dropped")
	}
	row := entry.row()
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

func TestDetailReviewerRowsPreserveDiagnosticCodes(t *testing.T) {
	for _, role := range []transcript.EntryRole{transcript.EntryRoleReviewerStatus, transcript.EntryRoleReviewerError} {
		t.Run(string(role), func(t *testing.T) {
			entry, ok := detailTestEntryFromCommittedRow(detailNotice(clientui.TranscriptNoticeRow{
				Severity: clientui.TranscriptNoticeInfo,
				Data: clientui.TranscriptNoticeData{
					MessageType:  clientui.MessageTypeReviewerFeedback,
					CompactLabel: "review result",
				},
				Diagnostic: &clientui.TranscriptDiagnosticData{Code: string(role)},
			}))
			if !ok {
				t.Fatal("reviewer row was dropped")
			}
			row := entry.row()
			if row.Notice == nil || row.Notice.Diagnostic == nil {
				t.Fatalf("reviewer row = %+v", row)
			}
			if row.Notice.Diagnostic.Code != string(role) {
				t.Fatalf("reviewer diagnostic code = %q, want %q", row.Notice.Diagnostic.Code, role)
			}
		})
	}
}
