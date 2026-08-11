package ongoing

import (
	"strings"
	"testing"

	"core/cli/tui/transcriptrender"
	"core/shared/clientui"
	"core/shared/transcript"

	xansi "github.com/charmbracelet/x/ansi"
)

func TestErrorNoticesRenderCompletelyThroughNormalOngoingModes(t *testing.T) {
	diagnosticDetail := "runtime diagnostic alpha beta gamma\nruntime diagnostic second line"
	legacyText := "legacy error alpha beta gamma\nlegacy error second line"
	misleadingCompact := "wrong compact source"
	misleadingCondensed := "wrong condensed source"
	messageType := clientui.TranscriptMessageErrorFeedback

	tests := []struct {
		name       string
		visibility transcript.EntryVisibility
		notice     *clientui.TranscriptNoticeRow
		source     string
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
			source:   diagnosticDetail,
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
			source:   legacyText,
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

			rendered := NewSurface().renderCommittedRow(row, 26, "dark")
			var visible strings.Builder
			for _, line := range rendered {
				plain := xansi.Strip(line)
				if strings.Contains(plain, "…") {
					t.Fatalf("ongoing error line was ellipsized: %q", plain)
				}
				if strings.HasPrefix(plain, "! ") {
					plain = strings.TrimPrefix(plain, "! ")
				} else {
					plain = strings.TrimPrefix(
						plain,
						strings.Repeat(" ", transcriptrender.RolePrefixWidth(transcriptrender.StyleRoleError)),
					)
				}
				visible.WriteString(plain)
			}
			want := strings.ReplaceAll(transcriptrender.TerminalSafePlainText(test.source), "\n", "")
			if got := visible.String(); got != want {
				t.Fatalf("ongoing error content = %q, want complete source %q", got, want)
			}
		})
	}
}
