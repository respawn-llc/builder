package tui

import (
	"core/cli/tui/transcriptrender"
	"core/shared/clientui"
	"core/shared/transcript"
)

func detailUser(text string) clientui.TranscriptCommittedRow {
	return clientui.TranscriptCommittedRow{
		Visibility: clientui.EntryVisibilityOngoing,
		Integrity:  transcript.RowIntegrityValid,
		Kind:       clientui.TranscriptRowUser,
		User:       &clientui.TranscriptUserRow{Text: text},
	}
}

func detailAssistant(text string) clientui.TranscriptCommittedRow {
	return clientui.TranscriptCommittedRow{
		Visibility: clientui.EntryVisibilityOngoing,
		Integrity:  transcript.RowIntegrityValid,
		Kind:       clientui.TranscriptRowAssistant,
		Assistant: &clientui.TranscriptAssistantRow{
			Text: text,
		},
	}
}

func detailTool(tool clientui.TranscriptToolRow) clientui.TranscriptCommittedRow {
	return clientui.TranscriptCommittedRow{
		Visibility: clientui.EntryVisibilityOngoingCollapsed,
		Integrity:  transcript.RowIntegrityValid,
		Kind:       clientui.TranscriptRowTool,
		Tool:       &tool,
	}
}

func detailNotice(notice clientui.TranscriptNoticeRow) clientui.TranscriptCommittedRow {
	return clientui.TranscriptCommittedRow{
		Visibility: clientui.EntryVisibilityOngoing,
		Integrity:  transcript.RowIntegrityValid,
		Kind:       clientui.TranscriptRowNotice,
		Notice:     &notice,
	}
}

func detailTestEntryFromCommittedRow(row clientui.TranscriptCommittedRow) (detailEntry, bool) {
	return detailEntryFromCommittedRow(row, transcriptrender.NewDetailCompiler(80, ""))
}
