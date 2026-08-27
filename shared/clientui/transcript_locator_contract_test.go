package clientui

import (
	"encoding/json"
	"testing"

	"core/shared/transcript"
)

func TestCommittedRowLocatorRequiresPositiveComparableComponents(t *testing.T) {
	valid := transcript.CommittedRowLocator{
		EventSequence: 12,
		RowOrdinal:    3,
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("validate committed row locator: %v", err)
	}
	if valid != (transcript.CommittedRowLocator{EventSequence: 12, RowOrdinal: 3}) {
		t.Fatalf("committed row locator is not comparable: %+v", valid)
	}

	for _, invalid := range []transcript.CommittedRowLocator{
		{},
		{EventSequence: 0, RowOrdinal: 1},
		{EventSequence: 1, RowOrdinal: 0},
		{EventSequence: -1, RowOrdinal: 1},
		{EventSequence: 1, RowOrdinal: -1},
	} {
		if err := invalid.Validate(); err == nil {
			t.Fatalf("accepted invalid committed row locator: %+v", invalid)
		}
	}
}

func testGoalAvailability() *GoalAvailability {
	value := GoalAvailabilityAvailable
	return &value
}

func TestCommittedRowRequiresLocatorAndPreservesItThroughJSON(t *testing.T) {
	row := transcriptLocatorTestAssistantRow(t)
	if err := row.Validate(); err != nil {
		t.Fatalf("validate committed row: %v", err)
	}

	data, err := json.Marshal(NewTranscriptMessage(2, NewTranscriptEvent(row)))
	if err != nil {
		t.Fatalf("marshal committed row message: %v", err)
	}
	var decoded TranscriptMessage
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal committed row message: %v", err)
	}
	if err := decoded.ValidatePayload(); err != nil {
		t.Fatalf("validate decoded committed row message: %v", err)
	}
	got := decoded.Payload().(TranscriptCommittedRow)
	if got.Locator != row.Locator {
		t.Fatalf("decoded locator = %+v, want %+v", got.Locator, row.Locator)
	}

	row.Locator = transcript.CommittedRowLocator{}
	if err := row.Validate(); err == nil {
		t.Fatal("accepted committed row without a valid locator")
	}
}

func TestTranscriptHydrationRequiresUniqueLocatorsAndPreservesThemThroughJSON(t *testing.T) {
	row := transcriptLocatorTestAssistantRow(t)
	hydration := TranscriptHydration{
		SessionIdentity:        transcriptTestSessionIdentity(t),
		SessionStatus:          transcriptTestSessionStatus(),
		RuntimeReadModelUpdate: transcriptTestRuntimeReadModelUpdate(t),
		CommittedRows:          []TranscriptCommittedRow{row},
		GoalStatus:             &TranscriptGoalStatus{Availability: testGoalAvailability()},
	}
	if err := hydration.Validate(); err != nil {
		t.Fatalf("validate hydration: %v", err)
	}

	data, err := json.Marshal(NewTranscriptMessage(1, NewTranscriptEvent(hydration)))
	if err != nil {
		t.Fatalf("marshal hydration: %v", err)
	}
	var decoded TranscriptMessage
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal hydration: %v", err)
	}
	if err := decoded.ValidatePayload(); err != nil {
		t.Fatalf("validate decoded hydration: %v", err)
	}
	got := decoded.Payload().(TranscriptHydration)
	if got.CommittedRows[0].Locator != row.Locator {
		t.Fatalf("hydration locator = %+v, want %+v", got.CommittedRows[0].Locator, row.Locator)
	}

	duplicate := hydration
	duplicate.CommittedRows = []TranscriptCommittedRow{row, row}
	if err := duplicate.Validate(); err == nil {
		t.Fatal("accepted duplicate committed row locator in hydration")
	}
}

func transcriptLocatorTestAssistantRow(t *testing.T) TranscriptCommittedRow {
	t.Helper()
	return TranscriptCommittedRow{
		Visibility: transcript.EntryVisibilityOngoing,
		Integrity:  transcript.RowIntegrityValid,
		Kind:       TranscriptRowAssistant,
		Locator: transcript.CommittedRowLocator{
			EventSequence: 12,
			RowOrdinal:    1,
		},
		Assistant: &TranscriptAssistantRow{
			StepID: transcriptTestStepID(t),
			Text:   "Done",
			Phase:  transcript.AssistantPhaseFinal,
		},
	}
}
