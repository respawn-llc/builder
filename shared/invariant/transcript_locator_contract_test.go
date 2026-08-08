package invariant

import (
	"testing"

	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/transcript"
)

func TestValidateTranscriptPageRejectsDuplicateLocatorsButAllowsIndependentRecurrence(t *testing.T) {
	stepID, err := runtimeids.ParseStepID("22222222-2222-4222-8222-222222222222")
	if err != nil {
		t.Fatalf("parse step id: %v", err)
	}
	row := clientui.TranscriptCommittedRow{
		Visibility: transcript.EntryVisibilityOngoing,
		Integrity:  transcript.RowIntegrityValid,
		Kind:       clientui.TranscriptRowUser,
		Locator: transcript.CommittedRowLocator{
			EventSequence: 8,
			RowOrdinal:    1,
		},
		User: &clientui.TranscriptUserRow{
			StepID: stepID,
			Text:   "hello",
		},
	}
	page := clientui.TranscriptPage{
		SessionID: "12345678-1234-4234-8234-123456789012",
		Entries:   []clientui.TranscriptCommittedRow{row},
	}
	if err := ValidateTranscriptPage(page); err != nil {
		t.Fatalf("validate page: %v", err)
	}
	if err := ValidateTranscriptPage(page); err != nil {
		t.Fatalf("independent recurrence rejected: %v", err)
	}

	page.Entries = []clientui.TranscriptCommittedRow{row, row}
	if err := ValidateTranscriptPage(page); err == nil {
		t.Fatal("accepted duplicate committed row locator in one page")
	}
}
