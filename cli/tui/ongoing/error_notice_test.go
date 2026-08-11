package ongoing

import (
	"reflect"
	"testing"

	"core/cli/tui/transcriptrender"
	"core/shared/clientui"
	"core/shared/transcript"
)

func TestErrorNoticesRenderCompletelyThroughNormalOngoingModes(t *testing.T) {
	diagnosticLines := []string{"runtime diagnostic alpha beta gamma", "runtime diagnostic second line"}
	diagnosticDetail := diagnosticLines[0] + "\n" + diagnosticLines[1]
	legacyLines := []string{"legacy error alpha beta gamma", "legacy error second line"}
	legacyText := legacyLines[0] + "\n" + legacyLines[1]
	misleadingCompact := "wrong compact source"
	misleadingCondensed := "wrong condensed source"
	messageType := clientui.TranscriptMessageErrorFeedback

	tests := []struct {
		name       string
		visibility transcript.EntryVisibility
		notice     *clientui.TranscriptNoticeRow
		source     []string
		wantMode   transcriptrender.Mode
	}{
		{
			name:       "runtime diagnostic ongoing",
			visibility: transcript.EntryVisibilityOngoing,
			notice: &clientui.TranscriptNoticeRow{
				Reason:        clientui.TranscriptNoticeRuntimeDiagnostic,
				Severity:      clientui.TranscriptNoticeError,
				CompactLabel:  &misleadingCompact,
				CondensedText: &misleadingCondensed,
				Diagnostic: &clientui.TranscriptDiagnostic{
					Code:   clientui.TranscriptDiagnosticCode(transcript.EntryRoleDeveloperErrorFeedback),
					Detail: diagnosticDetail,
				},
			},
			source:   diagnosticLines,
			wantMode: transcriptrender.ModeOngoing,
		},
		{
			name:       "legacy error ongoing collapsed",
			visibility: transcript.EntryVisibilityOngoingCollapsed,
			notice: &clientui.TranscriptNoticeRow{
				Reason:        clientui.TranscriptNoticeLegacyUntypedNotice,
				Severity:      clientui.TranscriptNoticeError,
				MessageType:   &messageType,
				LegacyText:    &legacyText,
				CompactLabel:  &misleadingCompact,
				CondensedText: &misleadingCondensed,
			},
			source:   legacyLines,
			wantMode: transcriptrender.ModeOngoingCollapsed,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			row := clientui.TranscriptCommittedRow{
				Visibility: test.visibility,
				Integrity:  transcript.RowIntegrityValid,
				Kind:       clientui.TranscriptRowNotice,
				Notice:     test.notice,
			}
			if err := test.notice.Validate(); err != nil {
				t.Fatalf("notice is invalid: %v", err)
			}
			if got := ongoingRenderMode(row); got != test.wantMode {
				t.Fatalf("normal ongoing render mode = %d, want %d", got, test.wantMode)
			}

			const width = 80
			structured := transcriptrender.RenderCommittedRow(row, width, "dark", test.wantMode)
			if len(structured.Lines) != len(test.source) {
				t.Fatalf("structured ongoing rows = %d, want %d", len(structured.Lines), len(test.source))
			}
			for index, line := range structured.Lines {
				if got := ongoingErrorLineContent(t, line, index); got != test.source[index] {
					t.Fatalf("structured ongoing error line %d = %q, want %q", index, got, test.source[index])
				}
			}

			wantEncoded := encodeTranscriptLines(structured.Lines, "dark")
			if got := NewSurface().renderCommittedRow(row, width, "dark"); !reflect.DeepEqual(got, wantEncoded) {
				t.Fatalf("ongoing surface changed structured renderer output: got=%q want=%q", got, wantEncoded)
			}
		})
	}
}

func ongoingErrorLineContent(t *testing.T, line transcriptrender.Line, index int) string {
	t.Helper()
	start := 0
	if line.LeadingSymbol != nil {
		if index != 0 ||
			line.LeadingSymbol.Style.Kind != transcriptrender.SpanStyleSemantic ||
			line.LeadingSymbol.Style.SemanticRole != transcriptrender.StyleRoleError {
			t.Fatalf("ongoing error line %d has invalid leading symbol: %+v", index, line.LeadingSymbol)
		}
		if len(line.Spans) == 0 ||
			line.Spans[0].Style.Kind != transcriptrender.SpanStyleSemantic ||
			line.Spans[0].Style.SemanticRole != transcriptrender.StyleRoleError ||
			line.Spans[0].Text != " " {
			t.Fatalf("ongoing error line %d has invalid structured symbol gap: %+v", index, line.Spans)
		}
		start = 1
	}
	content := ""
	for _, span := range line.Spans[start:] {
		if span.Style.Kind == transcriptrender.SpanStyleSemantic &&
			span.Style.SemanticRole == transcriptrender.StyleRoleError {
			content += span.Text
		}
	}
	return content
}
