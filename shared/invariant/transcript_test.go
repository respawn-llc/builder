package invariant

import (
	"testing"

	"core/shared/clientui"
	"core/shared/transcript"

	"github.com/google/uuid"
)

func TestValidateTranscriptCommittedRow(t *testing.T) {
	streamID := uuid.New()
	validRows := []clientui.TranscriptCommittedRow{
		{
			Visibility: clientui.EntryVisibilityOngoing,
			Kind:       clientui.TranscriptRowUser,
			User:       &clientui.TranscriptUserRow{Text: "user"},
		},
		{
			Visibility: clientui.EntryVisibilityOngoingCollapsed,
			Kind:       clientui.TranscriptRowAssistant,
			Assistant:  &clientui.TranscriptAssistantRow{Text: "assistant", StreamID: &streamID},
		},
		{
			Visibility: clientui.EntryVisibilityDetail,
			Kind:       clientui.TranscriptRowTool,
			Tool: &clientui.TranscriptToolRow{
				ToolCallID: "tool-call",
				ToolName:   "shell",
			},
		},
		{
			Visibility: clientui.EntryVisibilityHidden,
			Kind:       clientui.TranscriptRowNotice,
			Notice:     &clientui.TranscriptNoticeRow{},
		},
	}
	for _, row := range validRows {
		if err := ValidateTranscriptCommittedRow(row); err != nil {
			t.Fatalf("valid committed row rejected: %#v: %v", row, err)
		}
	}

	zeroStreamID := uuid.Nil
	invalidRows := []clientui.TranscriptCommittedRow{
		{Visibility: clientui.EntryVisibilityDetail, Integrity: transcript.RowIntegrity(255), Kind: clientui.TranscriptRowUser, User: &clientui.TranscriptUserRow{}},
		{Visibility: clientui.EntryVisibilityDetail, Kind: clientui.TranscriptRowUser},
		{Visibility: clientui.EntryVisibilityDetail, Kind: clientui.TranscriptRowUser, Tool: &clientui.TranscriptToolRow{ToolCallID: "tool-call"}},
		{Visibility: clientui.EntryVisibilityDetail, Kind: clientui.TranscriptRowAssistant, Assistant: &clientui.TranscriptAssistantRow{StreamID: &zeroStreamID}},
		{Visibility: clientui.EntryVisibilityDetail, Kind: clientui.TranscriptRowTool, Tool: &clientui.TranscriptToolRow{}},
		{Visibility: clientui.EntryVisibilityDetail, Kind: clientui.TranscriptRowNotice, Notice: &clientui.TranscriptNoticeRow{}, User: &clientui.TranscriptUserRow{}},
		{Visibility: clientui.EntryVisibilityAuto, Kind: clientui.TranscriptRowUser, User: &clientui.TranscriptUserRow{}},
		{Visibility: clientui.EntryVisibility("unknown"), Kind: clientui.TranscriptRowUser, User: &clientui.TranscriptUserRow{}},
	}
	for _, row := range invalidRows {
		if err := ValidateTranscriptCommittedRow(row); err == nil {
			t.Fatalf("invalid committed row accepted: %#v", row)
		}
	}

	malformedToolRows := []clientui.TranscriptCommittedRow{
		{
			Visibility: clientui.EntryVisibilityDetail,
			Integrity:  transcript.RowIntegrityRecoverableMalformed,
			Kind:       clientui.TranscriptRowTool,
			Tool:       &clientui.TranscriptToolRow{},
		},
		{
			Visibility: clientui.EntryVisibilityDetail,
			Integrity:  transcript.RowIntegrityUnrecoverableMalformed,
			Kind:       clientui.TranscriptRowTool,
			Tool:       &clientui.TranscriptToolRow{ToolCallID: " "},
		},
	}
	for _, row := range malformedToolRows {
		if err := ValidateTranscriptCommittedRow(row); err != nil {
			t.Fatalf("structurally valid malformed row rejected: %#v: %v", row, err)
		}
	}
}

func TestValidateTranscriptPage(t *testing.T) {
	olderCursor := int64(10)
	newerCursor := int64(20)
	validPage := clientui.TranscriptPage{
		SessionID:    uuid.NewString(),
		OlderCursor:  &olderCursor,
		HasMoreAbove: true,
		NewerCursor:  &newerCursor,
		HasMoreBelow: true,
		Entries: []clientui.TranscriptCommittedRow{
			{Visibility: clientui.EntryVisibilityOngoing, Kind: clientui.TranscriptRowUser, User: &clientui.TranscriptUserRow{Text: "valid"}},
			{Visibility: clientui.EntryVisibilityHidden, Kind: clientui.TranscriptRowNotice, Notice: &clientui.TranscriptNoticeRow{}},
			{
				Visibility: clientui.EntryVisibilityDetail,
				Integrity:  transcript.RowIntegrityRecoverableMalformed,
				Kind:       clientui.TranscriptRowTool,
				Tool:       &clientui.TranscriptToolRow{},
			},
			{
				Visibility: clientui.EntryVisibilityOngoingCollapsed,
				Integrity:  transcript.RowIntegrityUnrecoverableMalformed,
				Kind:       clientui.TranscriptRowTool,
				Tool:       &clientui.TranscriptToolRow{},
			},
		},
	}
	if err := ValidateTranscriptPage(validPage); err != nil {
		t.Fatalf("valid transcript page rejected: %v", err)
	}

	invalidPage := validPage
	invalidPage.Entries = append([]clientui.TranscriptCommittedRow(nil), validPage.Entries...)
	invalidPage.Entries[1] = clientui.TranscriptCommittedRow{Visibility: clientui.EntryVisibilityDetail, Kind: clientui.TranscriptRowTool, Tool: &clientui.TranscriptToolRow{}}
	if err := ValidateTranscriptPage(invalidPage); err == nil {
		t.Fatalf("page with an invalid committed row was accepted: %#v", invalidPage)
	}

	zeroCursor := int64(0)
	negativeCursor := int64(-1)
	invalidPages := []clientui.TranscriptPage{
		{SessionID: ""},
		{SessionID: "not-a-session-uuid"},
		{SessionID: uuid.NewString(), HasMoreAbove: true},
		{SessionID: uuid.NewString(), OlderCursor: &olderCursor},
		{SessionID: uuid.NewString(), OlderCursor: &zeroCursor, HasMoreAbove: true},
		{SessionID: uuid.NewString(), NewerCursor: &negativeCursor, HasMoreBelow: true},
		{SessionID: uuid.NewString(), HasMoreBelow: true},
		{SessionID: uuid.NewString(), NewerCursor: &newerCursor},
	}
	for _, page := range invalidPages {
		if err := ValidateTranscriptPage(page); err == nil {
			t.Fatalf("invalid transcript page accepted: %#v", page)
		}
	}
}
