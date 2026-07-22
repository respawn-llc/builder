package tui

import (
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
		Assistant:  &clientui.TranscriptAssistantRow{Text: text},
	}
}
