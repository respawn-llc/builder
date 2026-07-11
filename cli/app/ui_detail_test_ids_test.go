package app

import (
	"slices"

	"core/cli/tui"
	"core/shared/clientui"
	"core/shared/transcript"
)

const (
	detailTestSessionID            = "58e121b5-30f7-4d0f-a1fa-fb3e6695e39c"
	detailTestReplacementSessionID = "2fd85f0b-70fe-4d0d-8dfc-946d895502f8"
	detailTestStaleSessionID       = "d09bb9c6-5a8a-4634-b39c-79b0e2132fa7"
)

func detailTestUserRow(text string) clientui.TranscriptCommittedRow {
	return clientui.TranscriptCommittedRow{
		Visibility: clientui.EntryVisibilityDetail,
		Integrity:  transcript.RowIntegrityValid,
		Kind:       clientui.TranscriptRowUser,
		User:       &clientui.TranscriptUserRow{Text: text},
	}
}

func detailTestAssistantRow(text string) clientui.TranscriptCommittedRow {
	return clientui.TranscriptCommittedRow{
		Visibility: clientui.EntryVisibilityDetail,
		Integrity:  transcript.RowIntegrityValid,
		Kind:       clientui.TranscriptRowAssistant,
		Assistant:  &clientui.TranscriptAssistantRow{Text: text},
	}
}

func detailTestToolRow(tool clientui.TranscriptToolRow) clientui.TranscriptCommittedRow {
	return clientui.TranscriptCommittedRow{
		Visibility: clientui.EntryVisibilityDetail,
		Integrity:  transcript.RowIntegrityValid,
		Kind:       clientui.TranscriptRowTool,
		Tool:       &tool,
	}
}

func detailTestRowsEqual(left, right []clientui.TranscriptCommittedRow) bool {
	return slices.EqualFunc(left, right, tui.TranscriptCommittedRowEqual)
}
