package invariant

import (
	"testing"

	"core/shared/clientui"
	"core/shared/rollbacktarget"
	"core/shared/runtimeids"
	"core/shared/transcript"

	"github.com/google/uuid"
)

func TestValidateTranscriptCommittedRow(t *testing.T) {
	stepID, err := runtimeids.ParseStepID(uuid.NewString())
	if err != nil {
		t.Fatalf("parse step id: %v", err)
	}
	streamID := runtimeids.NewAssistantStreamID()
	rollbackTargetID := rollbacktarget.EncodeUserMessageSeq(12)
	noticeText := "notice"
	validRows := []clientui.TranscriptCommittedRow{
		{
			Visibility: clientui.EntryVisibilityOngoing,
			Kind:       clientui.TranscriptRowUser,
			Locator:    transcript.CommittedRowLocator{EventSequence: 1, RowOrdinal: 1},
			User:       &clientui.TranscriptUserRow{StepID: stepID, Text: "user", RollbackTargetID: &rollbackTargetID},
		},
		{
			Visibility: clientui.EntryVisibilityOngoingCollapsed,
			Kind:       clientui.TranscriptRowAssistant,
			Locator:    transcript.CommittedRowLocator{EventSequence: 2, RowOrdinal: 1},
			Assistant:  &clientui.TranscriptAssistantRow{StepID: stepID, Text: "assistant", StreamID: &streamID, Phase: transcript.AssistantPhaseFinal},
		},
		{
			Visibility: clientui.EntryVisibilityDetail,
			Kind:       clientui.TranscriptRowTool,
			Locator:    transcript.CommittedRowLocator{EventSequence: 3, RowOrdinal: 1},
			Tool: &clientui.TranscriptToolRow{
				StepID:     stepID,
				ToolCallID: "tool-call",
				ToolName:   "shell",
			},
		},
		{
			Visibility: clientui.EntryVisibilityDetail,
			Kind:       clientui.TranscriptRowReasoningTrace,
			Locator:    transcript.CommittedRowLocator{EventSequence: 4, RowOrdinal: 1},
			ReasoningTrace: &clientui.TranscriptReasoningTraceRow{
				StepID:      stepID,
				CompactText: "Planning",
				Text:        "Planning\nDetails",
			},
		},
		{
			Visibility: clientui.EntryVisibilityHidden,
			Kind:       clientui.TranscriptRowNotice,
			Locator:    transcript.CommittedRowLocator{EventSequence: 5, RowOrdinal: 1},
			Notice: &clientui.TranscriptNoticeRow{
				Reason:     clientui.TranscriptNoticeLegacyUntypedNotice,
				Severity:   clientui.TranscriptNoticeInfo,
				LegacyText: &noticeText,
			},
		},
	}
	for _, row := range validRows {
		if err := ValidateTranscriptCommittedRow(row); err != nil {
			t.Fatalf("valid committed row rejected: %#v: %v", row, err)
		}
	}

	zeroStreamID := runtimeids.AssistantStreamID{}
	invalidRollbackTargetID := "not-a-rollback-target"
	invalidRows := []clientui.TranscriptCommittedRow{
		{Visibility: clientui.EntryVisibilityDetail, Integrity: transcript.RowIntegrity(255), Kind: clientui.TranscriptRowUser, User: &clientui.TranscriptUserRow{}},
		{Visibility: clientui.EntryVisibilityDetail, Kind: clientui.TranscriptRowUser},
		{Visibility: clientui.EntryVisibilityDetail, Kind: clientui.TranscriptRowUser, Tool: &clientui.TranscriptToolRow{ToolCallID: "tool-call"}},
		{Visibility: clientui.EntryVisibilityDetail, Kind: clientui.TranscriptRowAssistant, Assistant: &clientui.TranscriptAssistantRow{StreamID: &zeroStreamID}},
		{Visibility: clientui.EntryVisibilityDetail, Kind: clientui.TranscriptRowTool, Tool: &clientui.TranscriptToolRow{}},
		{Visibility: clientui.EntryVisibilityDetail, Kind: clientui.TranscriptRowNotice, Notice: &clientui.TranscriptNoticeRow{}, User: &clientui.TranscriptUserRow{}},
		{Visibility: clientui.EntryVisibilityAuto, Kind: clientui.TranscriptRowUser, User: &clientui.TranscriptUserRow{}},
		{Visibility: clientui.EntryVisibility("unknown"), Kind: clientui.TranscriptRowUser, User: &clientui.TranscriptUserRow{}},
		{Visibility: clientui.EntryVisibilityDetail, Kind: clientui.TranscriptRowUser, User: &clientui.TranscriptUserRow{Text: "user", RollbackTargetID: &invalidRollbackTargetID}},
		{Visibility: clientui.EntryVisibilityDetail, Integrity: transcript.RowIntegrityRecoverableMalformed, Kind: clientui.TranscriptRowUser, User: &clientui.TranscriptUserRow{RollbackTargetID: &invalidRollbackTargetID}},
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
			Locator:    transcript.CommittedRowLocator{EventSequence: 5, RowOrdinal: 1},
			Tool:       &clientui.TranscriptToolRow{},
		},
		{
			Visibility: clientui.EntryVisibilityDetail,
			Integrity:  transcript.RowIntegrityUnrecoverableMalformed,
			Kind:       clientui.TranscriptRowTool,
			Locator:    transcript.CommittedRowLocator{EventSequence: 6, RowOrdinal: 1},
			Tool:       &clientui.TranscriptToolRow{ToolCallID: " "},
		},
	}
	for _, row := range malformedToolRows {
		if err := ValidateTranscriptCommittedRow(row); err != nil {
			t.Fatalf("structurally valid malformed row rejected: %#v: %v", row, err)
		}
	}
}

func TestValidateTranscriptCommittedRowRejectsInvalidTimestampBeforeIntegrityShortCircuit(t *testing.T) {
	stepID, err := runtimeids.ParseStepID(uuid.NewString())
	if err != nil {
		t.Fatalf("parse step id: %v", err)
	}
	outOfRange := transcript.MaxCommittedAtUnixMs + 1
	for _, integrity := range []transcript.RowIntegrity{
		transcript.RowIntegrityValid,
		transcript.RowIntegrityRecoverableMalformed,
		transcript.RowIntegrityUnrecoverableMalformed,
	} {
		for _, row := range []clientui.TranscriptCommittedRow{
			{
				Visibility: transcript.EntryVisibilityOngoing,
				Integrity:  integrity,
				Kind:       clientui.TranscriptRowUser,
				Locator:    transcript.CommittedRowLocator{EventSequence: 1, RowOrdinal: 1},
				User:       &clientui.TranscriptUserRow{StepID: stepID, Text: "user", CommittedAtUnixMs: &outOfRange},
			},
			{
				Visibility: transcript.EntryVisibilityOngoing,
				Integrity:  integrity,
				Kind:       clientui.TranscriptRowAssistant,
				Locator:    transcript.CommittedRowLocator{EventSequence: 1, RowOrdinal: 1},
				Assistant: &clientui.TranscriptAssistantRow{
					StepID: stepID, Text: "assistant", Phase: transcript.AssistantPhaseFinal,
					CommittedAtUnixMs: &outOfRange,
				},
			},
		} {
			if err := ValidateTranscriptCommittedRow(row); err == nil {
				t.Fatalf("integrity %v accepted invalid timestamp: %#v", integrity, row)
			}
		}
	}
}

func TestValidateTranscriptPage(t *testing.T) {
	olderCursor := int64(10)
	newerCursor := int64(20)
	stepID, err := runtimeids.ParseStepID(uuid.NewString())
	if err != nil {
		t.Fatalf("parse step id: %v", err)
	}
	noticeText := "notice"
	validPage := clientui.TranscriptPage{
		SessionID:    uuid.NewString(),
		OlderCursor:  &olderCursor,
		HasMoreAbove: true,
		NewerCursor:  &newerCursor,
		HasMoreBelow: true,
		LatestRollbackCandidate: &rollbacktarget.CandidateLocator{
			UserMessageSeq:       7,
			CandidatePageEndByte: 15,
		},
		Entries: []clientui.TranscriptCommittedRow{
			{Visibility: clientui.EntryVisibilityOngoing, Kind: clientui.TranscriptRowUser, Locator: transcript.CommittedRowLocator{EventSequence: 1, RowOrdinal: 1}, User: &clientui.TranscriptUserRow{StepID: stepID, Text: "valid"}},
			{Visibility: clientui.EntryVisibilityHidden, Kind: clientui.TranscriptRowNotice, Locator: transcript.CommittedRowLocator{EventSequence: 2, RowOrdinal: 1}, Notice: &clientui.TranscriptNoticeRow{
				StepID:     &stepID,
				Reason:     clientui.TranscriptNoticeLegacyUntypedNotice,
				Severity:   clientui.TranscriptNoticeInfo,
				LegacyText: &noticeText,
			}},
			{
				Visibility: clientui.EntryVisibilityDetail,
				Integrity:  transcript.RowIntegrityRecoverableMalformed,
				Kind:       clientui.TranscriptRowTool,
				Locator:    transcript.CommittedRowLocator{EventSequence: 3, RowOrdinal: 1},
				Tool:       &clientui.TranscriptToolRow{},
			},
			{
				Visibility: clientui.EntryVisibilityOngoingCollapsed,
				Integrity:  transcript.RowIntegrityUnrecoverableMalformed,
				Kind:       clientui.TranscriptRowTool,
				Locator:    transcript.CommittedRowLocator{EventSequence: 4, RowOrdinal: 1},
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
		{SessionID: "../not-a-session"},
		{SessionID: uuid.NewString(), HasMoreAbove: true},
		{SessionID: uuid.NewString(), OlderCursor: &olderCursor},
		{SessionID: uuid.NewString(), OlderCursor: &zeroCursor, HasMoreAbove: true},
		{SessionID: uuid.NewString(), NewerCursor: &negativeCursor, HasMoreBelow: true},
		{SessionID: uuid.NewString(), HasMoreBelow: true},
		{SessionID: uuid.NewString(), NewerCursor: &newerCursor},
		{
			SessionID: uuid.NewString(),
			LatestRollbackCandidate: &rollbacktarget.CandidateLocator{
				CandidatePageEndByte: 15,
			},
		},
		{
			SessionID: uuid.NewString(),
			LatestRollbackCandidate: &rollbacktarget.CandidateLocator{
				UserMessageSeq: 7,
			},
		},
	}
	for _, page := range invalidPages {
		if err := ValidateTranscriptPage(page); err == nil {
			t.Fatalf("invalid transcript page accepted: %#v", page)
		}
	}
}
