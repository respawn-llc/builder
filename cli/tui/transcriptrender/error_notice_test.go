package transcriptrender

import (
	"reflect"
	"strings"
	"testing"
	"unicode"

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
	detail := "diagnostic \x1b[31mred\tvalue\x00\nsecond diagnostic \x07line remains complete"
	want := []string{
		"diagnostic [31mred value",
		"second diagnostic line remains complete",
	}
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
			rendered := RenderCommittedRow(row, 120, "dark", mode)
			assertCompleteErrorContent(
				t,
				rendered,
				want,
			)
		})
	}
}

func TestLegacyUntypedErrorUsesCompleteLegacyTextInEveryMode(t *testing.T) {
	legacy := "legacy alpha\tbeta\x00\nsecond legacy \x07line remains complete"
	want := []string{
		"legacy alpha beta",
		"second legacy line remains complete",
	}
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
			rendered := RenderCommittedRow(row, 120, "dark", mode)
			assertCompleteErrorContent(
				t,
				rendered,
				want,
			)
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
	metadataType := clientui.TranscriptMessageWorktreeMode
	metadataNotice := &clientui.TranscriptNoticeRow{
		Reason:      clientui.TranscriptNoticeLegacyUntypedNotice,
		Severity:    clientui.TranscriptNoticeError,
		MessageType: &metadataType,
		Worktree: &clientui.TranscriptWorktreeContext{
			Branch:        stringPtr("feature/error-source"),
			WorktreePath:  "/workspace/feature",
			WorkspaceRoot: "/workspace",
			EffectiveCwd:  "/workspace/feature",
		},
	}
	metadataText, metadataTextPresent := worktreeNoticeText(metadataNotice, ModeDetailExpanded)
	if !metadataTextPresent {
		t.Fatal("typed Worktree message metadata has no formatter")
	}

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
			name:   "typed message metadata",
			notice: metadataNotice,
			want:   metadataText,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.notice.Validate(); err != nil {
				t.Fatalf("notice is invalid: %v", err)
			}
			rendered := RenderCommittedRow(errorNoticeRow(test.notice), 120, "dark", ModeOngoingCollapsed)
			assertCompleteErrorContent(t, rendered, []string{test.want})
		})
	}
}

func TestMetadataOnlyLegacyErrorUsesCompleteCondensedText(t *testing.T) {
	messageType := clientui.TranscriptMessageErrorFeedback
	condensed := "complete metadata error content"
	compact := "compact label is not error content"
	sourcePath := "/preview/source/path"
	notice := &clientui.TranscriptNoticeRow{
		Reason:        clientui.TranscriptNoticeLegacyUntypedNotice,
		Severity:      clientui.TranscriptNoticeError,
		MessageType:   &messageType,
		CondensedText: &condensed,
		CompactLabel:  &compact,
		SourcePath:    &sourcePath,
	}
	if err := notice.Validate(); err != nil {
		t.Fatalf("metadata-only legacy error is invalid: %v", err)
	}
	rendered := RenderCommittedRow(errorNoticeRow(notice), 120, "dark", ModeOngoingCollapsed)
	assertCompleteErrorContent(t, rendered, []string{condensed})
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

func assertCompleteErrorContent(t *testing.T, rendered Row, want []string) {
	t.Helper()
	got := make([]string, 0, len(rendered.Lines))
	for _, line := range rendered.Lines {
		var content strings.Builder
		for _, span := range line.Spans {
			if role, ok := span.Style.Role(); ok && role == StyleRoleError {
				content.WriteString(span.Text)
			}
		}
		projected := strings.TrimSpace(content.String())
		for _, value := range projected {
			if unicode.IsControl(value) {
				t.Fatalf("rendered error payload contains terminal control %U", value)
			}
		}
		got = append(got, projected)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("rendered semantic error payload = %#v, want %#v", got, want)
	}
}
