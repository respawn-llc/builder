package transcriptrender

import (
	"testing"

	"core/shared/clientui"
	"core/shared/textutil"
	"core/shared/transcript"
)

func TestExpandedWorktreeNoticeShowsFullReminderVerbatim(t *testing.T) {
	messageType := clientui.TranscriptMessageWorktreeMode
	const fullReminder = "  full worktree reminder\nwith exact spacing  "
	row := clientui.TranscriptCommittedRow{
		Visibility: transcript.EntryVisibilityOngoing,
		Integrity:  transcript.RowIntegrityValid,
		Kind:       clientui.TranscriptRowNotice,
		Notice: &clientui.TranscriptNoticeRow{
			Reason:      clientui.TranscriptNoticeRuntimeDiagnostic,
			Severity:    clientui.TranscriptNoticeInfo,
			MessageType: &messageType,
			Worktree: &clientui.TranscriptWorktreeContext{
				Branch:        textutil.Value("feature/transcript"),
				WorktreePath:  "/tmp/worktree",
				WorkspaceRoot: "/tmp/workspace",
				EffectiveCwd:  "/tmp/worktree/pkg",
			},
			Diagnostic: &clientui.TranscriptDiagnostic{
				Code:   clientui.TranscriptDiagnosticCode(transcript.EntryRoleDeveloperContext),
				Detail: fullReminder,
			},
		},
	}

	if _, got := noticeRoleAndText(row.Notice, row.Visibility, ModeDetailExpanded); got != fullReminder {
		t.Fatalf("expanded worktree reminder = %q, want full reminder preserved verbatim", got)
	}
	if !RenderDetailPresentation(row, 80, "dark").Expandable {
		t.Fatal("worktree reminder with full content is not expandable")
	}
}
