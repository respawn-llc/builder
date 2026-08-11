package ongoing

import (
	"reflect"
	"strings"
	"testing"

	"core/cli/tui/transcriptrender"
	"core/shared/clientui"
	"core/shared/transcript"
)

func TestErrorNoticesRenderCompletelyThroughNormalOngoingModes(t *testing.T) {
	diagnosticDetail := "runtime diagnostic first line\nruntime diagnostic second line"
	legacyText := "legacy error first line\nlegacy error second line"
	misleadingCompact := "wrong compact source"
	misleadingCondensed := "wrong condensed source"
	messageType := clientui.TranscriptMessageErrorFeedback
	tests := []struct {
		name       string
		visibility transcript.EntryVisibility
		notice     *clientui.TranscriptNoticeRow
		wantMode   transcriptrender.Mode
		wantText   string
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
			wantText: diagnosticDetail,
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
			wantText: legacyText,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			row := clientui.TranscriptCommittedRow{Visibility: test.visibility, Kind: clientui.TranscriptRowNotice, Notice: test.notice}
			if got := ongoingRenderMode(row); got != test.wantMode {
				t.Fatalf("normal ongoing render mode = %d, want %d", got, test.wantMode)
			}
			structured := transcriptrender.RenderCommittedRow(row, 80, "dark", test.wantMode)
			gotText := make([]string, len(structured.Lines))
			for index, line := range structured.Lines {
				for _, span := range line.Spans {
					if role, ok := span.Style.Role(); ok && role == transcriptrender.StyleRoleError {
						gotText[index] += span.Text
					}
				}
				gotText[index] = strings.TrimSpace(gotText[index])
			}
			if want := strings.Split(test.wantText, "\n"); !reflect.DeepEqual(gotText, want) {
				t.Fatalf("structured ongoing error payload = %#v, want %#v", gotText, want)
			}
			if got, want := NewSurface().renderCommittedRow(row, 80, "dark"), encodeTranscriptLines(structured.Lines, "dark"); !reflect.DeepEqual(got, want) {
				t.Fatalf("ongoing surface changed structured renderer output: got=%q want=%q", got, want)
			}
		})
	}
}
