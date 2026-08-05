package app

import (
	"hash/fnv"
	"slices"
	"testing"

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
		Locator:    detailTestLocator("user:" + text),
		User:       &clientui.TranscriptUserRow{Text: text},
	}
}

func detailTestAssistantRow(text string) clientui.TranscriptCommittedRow {
	return clientui.TranscriptCommittedRow{
		Visibility: clientui.EntryVisibilityDetail,
		Integrity:  transcript.RowIntegrityValid,
		Kind:       clientui.TranscriptRowAssistant,
		Locator:    detailTestLocator("assistant:" + text),
		Assistant:  &clientui.TranscriptAssistantRow{Text: text},
	}
}

func detailTestToolRow(tool clientui.TranscriptToolRow) clientui.TranscriptCommittedRow {
	return clientui.TranscriptCommittedRow{
		Visibility: clientui.EntryVisibilityDetail,
		Integrity:  transcript.RowIntegrityValid,
		Kind:       clientui.TranscriptRowTool,
		Locator:    detailTestLocator("tool:" + string(tool.ToolCallID) + ":" + tool.Text),
		Tool:       &tool,
	}
}

func detailTestLocator(value string) transcript.CommittedRowLocator {
	hash := fnv.New64a()
	_, _ = hash.Write([]byte(value))
	sequence := int64(hash.Sum64() & (uint64(1<<63) - 1))
	if sequence == 0 {
		sequence = 1
	}
	return transcript.CommittedRowLocator{EventSequence: sequence, RowOrdinal: 1}
}

func detailTestRowsEqual(left, right []clientui.TranscriptCommittedRow) bool {
	return slices.EqualFunc(left, right, tui.TranscriptCommittedRowEqual)
}

func TestDetailTranscriptRowEqualityIncludesLocator(t *testing.T) {
	left := detailTestUserRow("same content")
	right := left
	right.Locator.RowOrdinal++

	if !tui.TranscriptCommittedRowEqual(left, left) {
		t.Fatal("a transcript row was not equal to itself")
	}
	if tui.TranscriptCommittedRowEqual(left, right) {
		t.Fatalf("rows with different locators were treated as equal: left=%+v right=%+v", left.Locator, right.Locator)
	}
}
