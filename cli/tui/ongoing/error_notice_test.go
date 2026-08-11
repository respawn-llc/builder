package ongoing

import (
	"reflect"
	"testing"

	"core/cli/tui/transcriptrender"
	"core/shared/clientui"
	"core/shared/transcript"
)

func TestErrorNoticesRenderCompletelyThroughNormalOngoingModes(t *testing.T) {
	diagnosticDetail := "runtime diagnostic \x1b[31mred\tgamma\nruntime diagnostic second \x00line"
	legacyText := "legacy error alpha\tbeta\nlegacy error second \x07line"
	misleadingCompact := "wrong compact source"
	misleadingCondensed := "wrong condensed source"
	messageType := clientui.TranscriptMessageErrorFeedback

	tests := []struct {
		name       string
		visibility transcript.EntryVisibility
		notice     *clientui.TranscriptNoticeRow
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
			if got := len(structured.Lines); got != 2 {
				t.Fatalf("structured ongoing error lines = %d, want both authored lines", got)
			}

			wantEncoded := encodeTranscriptLines(structured.Lines, "dark")
			if got := NewSurface().renderCommittedRow(row, width, "dark"); !reflect.DeepEqual(got, wantEncoded) {
				t.Fatalf("ongoing surface changed structured renderer output: got=%q want=%q", got, wantEncoded)
			}
		})
	}
}
