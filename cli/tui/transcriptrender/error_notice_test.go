package transcriptrender

import (
	"strings"
	"testing"

	"core/shared/clientui"
	"core/shared/transcript"
)

func TestErrorNoticeClassifiesAsError(t *testing.T) {
	row := &clientui.TranscriptNoticeRow{
		Reason:   clientui.TranscriptNoticeRuntimeDiagnostic,
		Severity: clientui.TranscriptNoticeError,
		Diagnostic: &clientui.TranscriptDiagnostic{
			Code:   clientui.TranscriptDiagnosticCode(transcript.EntryRoleDeveloperErrorFeedback),
			Detail: "failure",
		},
	}
	if got := noticeStyleRole(row); got != StyleRoleError {
		t.Fatalf("error notice role = %v, want %v", got, StyleRoleError)
	}
}

func TestRuntimeDiagnosticErrorUsesCompleteTypedDetailInEveryMode(t *testing.T) {
	detail := "diagnostic alpha beta gamma \x1b[31mred\nsecond diagnostic line remains complete"
	misleadingCompact := "wrong compact source"
	misleadingCondensed := "wrong condensed source"
	row := errorNoticeRow(&clientui.TranscriptNoticeRow{
		Reason:        clientui.TranscriptNoticeRuntimeDiagnostic,
		Severity:      clientui.TranscriptNoticeError,
		CompactLabel:  &misleadingCompact,
		CondensedText: &misleadingCondensed,
		Diagnostic: &clientui.TranscriptDiagnostic{
			Code:   clientui.TranscriptDiagnosticCode(transcript.EntryRoleDeveloperErrorFeedback),
			Detail: detail,
		},
	})
	if err := row.Notice.Validate(); err != nil {
		t.Fatalf("runtime diagnostic row is invalid: %v", err)
	}

	for _, mode := range allTranscriptModes() {
		t.Run(transcriptModeName(mode), func(t *testing.T) {
			rendered := RenderCommittedRow(row, 24, "dark", mode)
			assertCompleteErrorContent(t, rendered, detail)
		})
	}
}

func TestLegacyUntypedErrorUsesCompleteLegacyTextInEveryMode(t *testing.T) {
	legacy := "legacy alpha beta gamma\nsecond legacy line remains complete"
	misleadingCompact := "wrong compact source"
	misleadingCondensed := "wrong condensed source"
	messageType := clientui.TranscriptMessageErrorFeedback
	row := errorNoticeRow(&clientui.TranscriptNoticeRow{
		Reason:        clientui.TranscriptNoticeLegacyUntypedNotice,
		Severity:      clientui.TranscriptNoticeError,
		MessageType:   &messageType,
		LegacyText:    &legacy,
		CompactLabel:  &misleadingCompact,
		CondensedText: &misleadingCondensed,
	})
	if err := row.Notice.Validate(); err != nil {
		t.Fatalf("legacy error row is invalid: %v", err)
	}

	for _, mode := range allTranscriptModes() {
		t.Run(transcriptModeName(mode), func(t *testing.T) {
			rendered := RenderCommittedRow(row, 24, "dark", mode)
			assertCompleteErrorContent(t, rendered, legacy)
		})
	}
}

func TestErrorNoticeReasonSelectsItsTypedContentSource(t *testing.T) {
	cacheWarning := &clientui.TranscriptCacheWarning{
		Scope:      string(transcript.CacheWarningScopeConversation),
		Reason:     string(transcript.CacheWarningReasonNonPostfix),
		Visibility: transcript.EntryVisibilityOngoing,
	}
	compactionDetail := "typed compaction detail"
	messageType := clientui.TranscriptMessageCompactionSummary
	repair := &transcript.ToolOutputRepairNotice{
		Kind:  transcript.ToolOutputRepairFreshResource,
		Count: 2,
	}
	diagnosticDetail := "typed runtime diagnostic"
	legacyText := "typed legacy text"
	metadataText := "typed message metadata content"
	metadataType := clientui.TranscriptMessageErrorFeedback

	tests := []struct {
		name   string
		notice *clientui.TranscriptNoticeRow
		want   string
	}{
		{
			name: "cache warning",
			notice: &clientui.TranscriptNoticeRow{
				Reason:       clientui.TranscriptNoticeCacheWarning,
				Severity:     clientui.TranscriptNoticeError,
				CacheWarning: cacheWarning,
			},
			want: cacheWarningNoticeText(cacheWarning),
		},
		{
			name: "compaction detail",
			notice: &clientui.TranscriptNoticeRow{
				Reason:      clientui.TranscriptNoticeCompaction,
				Severity:    clientui.TranscriptNoticeError,
				MessageType: &messageType,
				Compaction:  &clientui.TranscriptCompactionNotice{Detail: &compactionDetail},
			},
			want: compactionDetail,
		},
		{
			name: "compaction formatter",
			notice: &clientui.TranscriptNoticeRow{
				Reason:      clientui.TranscriptNoticeCompaction,
				Severity:    clientui.TranscriptNoticeError,
				MessageType: &messageType,
				Compaction:  &clientui.TranscriptCompactionNotice{},
			},
			want: compactionNoticeText(nil),
		},
		{
			name: "tool output repair",
			notice: &clientui.TranscriptNoticeRow{
				Reason:           clientui.TranscriptNoticeToolOutputRepair,
				Severity:         clientui.TranscriptNoticeError,
				ToolOutputRepair: repair,
			},
			want: toolOutputRepairNoticeText(repair),
		},
		{
			name: "runtime diagnostic",
			notice: &clientui.TranscriptNoticeRow{
				Reason:   clientui.TranscriptNoticeRuntimeDiagnostic,
				Severity: clientui.TranscriptNoticeError,
				Diagnostic: &clientui.TranscriptDiagnostic{
					Code:   clientui.TranscriptDiagnosticCode(transcript.EntryRoleDeveloperErrorFeedback),
					Detail: diagnosticDetail,
				},
			},
			want: diagnosticDetail,
		},
		{
			name: "legacy text",
			notice: &clientui.TranscriptNoticeRow{
				Reason:     clientui.TranscriptNoticeLegacyUntypedNotice,
				Severity:   clientui.TranscriptNoticeError,
				LegacyText: &legacyText,
			},
			want: legacyText,
		},
		{
			name: "typed message metadata",
			notice: &clientui.TranscriptNoticeRow{
				Reason:        clientui.TranscriptNoticeLegacyUntypedNotice,
				Severity:      clientui.TranscriptNoticeError,
				MessageType:   &metadataType,
				CondensedText: &metadataText,
			},
			want: metadataText,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.notice.Validate(); err != nil {
				t.Fatalf("notice is invalid: %v", err)
			}
			rendered := RenderCommittedRow(errorNoticeRow(test.notice), 32, "dark", ModeOngoingCollapsed)
			assertCompleteErrorContent(t, rendered, test.want)
		})
	}
}

func TestNonErrorNoticeAndToolRowsRetainCompactOneLineLayout(t *testing.T) {
	legacy := "ordinary notice first line\nordinary notice second line"
	notice := &clientui.TranscriptNoticeRow{
		Reason:     clientui.TranscriptNoticeLegacyUntypedNotice,
		Severity:   clientui.TranscriptNoticeInfo,
		LegacyText: &legacy,
	}
	rendered := RenderCommittedRow(errorNoticeRow(notice), 80, "dark", ModeOngoingCollapsed)
	if got := len(rendered.Lines); got != 1 {
		t.Fatalf("ordinary notice lines = %d, want compact single line", got)
	}
	if !strings.Contains(rendered.Lines[0].Plain(), "…") {
		t.Fatal("ordinary multiline notice lost compact ellipsis")
	}

	tool := clientui.TranscriptCommittedRow{
		Visibility: transcript.EntryVisibilityOngoingCollapsed,
		Integrity:  transcript.RowIntegrityValid,
		Kind:       clientui.TranscriptRowTool,
		Tool: &clientui.TranscriptToolRow{
			ToolName: "ordinary_tool",
			Text:     "ordinary tool first line\nordinary tool second line",
		},
	}
	rendered = RenderCommittedRow(tool, 80, "dark", ModeOngoingCollapsed)
	if got := len(rendered.Lines); got != 1 {
		t.Fatalf("ordinary tool lines = %d, want compact single line", got)
	}
}

func errorNoticeRow(notice *clientui.TranscriptNoticeRow) clientui.TranscriptCommittedRow {
	return clientui.TranscriptCommittedRow{
		Visibility: transcript.EntryVisibilityOngoing,
		Integrity:  transcript.RowIntegrityValid,
		Kind:       clientui.TranscriptRowNotice,
		Notice:     notice,
	}
}

func allTranscriptModes() []Mode {
	return []Mode{
		ModeOngoing,
		ModeOngoingCollapsed,
		ModeDetailCollapsed,
		ModeOngoingFull,
		ModeOngoingStable,
		ModeDetailExpanded,
	}
}

func transcriptModeName(mode Mode) string {
	switch mode {
	case ModeOngoing:
		return "ongoing"
	case ModeOngoingCollapsed:
		return "ongoing_collapsed"
	case ModeOngoingFull:
		return "ongoing_full"
	case ModeOngoingStable:
		return "ongoing_stable"
	case ModeDetailCollapsed:
		return "detail_collapsed"
	case ModeDetailExpanded:
		return "detail_expanded"
	default:
		return "unknown"
	}
}

func assertCompleteErrorContent(t *testing.T, rendered Row, source string) {
	t.Helper()
	var content strings.Builder
	for _, line := range rendered.Lines {
		for _, span := range line.Spans {
			if span.Style.Kind == SpanStyleSemantic && span.Style.SemanticRole == StyleRoleError {
				content.WriteString(span.Text)
			}
		}
		if strings.Contains(line.Plain(), "…") {
			t.Fatalf("error line was ellipsized: %q", line.Plain())
		}
	}
	want := strings.ReplaceAll(TerminalSafePlainText(source), "\n", "")
	if got := strings.TrimSpace(content.String()); got != want {
		t.Fatalf("rendered error content = %q, want complete terminal-safe source %q", got, want)
	}
}
