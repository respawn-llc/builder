package clientui

import (
	"encoding/json"
	"reflect"
	"testing"

	"core/shared/runtimeids"
	"core/shared/transcript"
)

func TestTranscriptReasoningTraceContractSeparatesThinkingStatusAndOrderedTraces(t *testing.T) {
	stepID := transcriptTestStepID(t)
	providerIndex := int64(0)
	trace := TranscriptReasoningTraceUpdate{
		StepID: stepID,
		Identity: TranscriptReasoningTraceIdentity{
			Provider: &TranscriptProviderReasoningTraceIdentity{
				ItemID:       "rs_1",
				SummaryIndex: &providerIndex,
			},
		},
		CompactText: "Planning",
		Text:        "Planning\nDetails",
	}
	status := TranscriptThinkingStatusUpdate{StepID: stepID, Text: "Thinking"}
	if err := trace.Validate(); err != nil {
		t.Fatalf("validate trace: %v", err)
	}
	if err := status.Validate(); err != nil {
		t.Fatalf("validate status: %v", err)
	}

	hydration := TranscriptHydration{
		RuntimeReadModelUpdate: transcriptTestRuntimeReadModelUpdate(t),
		SessionIdentity:        transcriptTestSessionIdentity(t),
		SessionStatus:          transcriptTestSessionStatus(),
		CommittedRows:          []TranscriptCommittedRow{},
		GoalStatus:             &TranscriptGoalStatus{Availability: testGoalAvailability()},
		ActiveThinkingStatus:   &status,
		ActiveReasoningTraces:  []TranscriptReasoningTraceUpdate{trace},
	}
	if err := hydration.Validate(); err != nil {
		t.Fatalf("validate hydration: %v", err)
	}

	for _, event := range []TranscriptEvent{
		NewTranscriptEvent(status),
		NewTranscriptEvent(trace),
		NewTranscriptEvent(TranscriptReasoningTraceReset{StepID: stepID}),
	} {
		message := NewTranscriptMessage(2, event)
		if err := message.Validate(); err != nil {
			t.Fatalf("validate live event %q: %v", message.Kind(), err)
		}
		data, err := json.Marshal(message)
		if err != nil {
			t.Fatalf("marshal %q: %v", message.Kind(), err)
		}
		var roundTrip TranscriptMessage
		if err := json.Unmarshal(data, &roundTrip); err != nil {
			t.Fatalf("unmarshal %q: %v", message.Kind(), err)
		}
		if roundTrip.Kind() != message.Kind() {
			t.Fatalf("round-trip kind = %q, want %q", roundTrip.Kind(), message.Kind())
		}
	}
}

func TestTranscriptReasoningTraceIdentityRejectsInvalidBranchesAndIndexes(t *testing.T) {
	validIndex := int64(0)
	validProvider := TranscriptReasoningTraceIdentity{
		Provider: &TranscriptProviderReasoningTraceIdentity{
			ItemID:       "rs_1",
			SummaryIndex: &validIndex,
		},
	}
	if err := validProvider.Validate(); err != nil {
		t.Fatalf("valid provider identity: %v", err)
	}
	kent := runtimeids.NewReasoningTraceID()
	if err := (TranscriptReasoningTraceIdentity{Kent: &kent}).Validate(); err != nil {
		t.Fatalf("valid Kent identity: %v", err)
	}

	negative := int64(-1)
	invalid := []TranscriptReasoningTraceIdentity{
		{},
		{Provider: &TranscriptProviderReasoningTraceIdentity{ItemID: "", SummaryIndex: &validIndex}},
		{Provider: &TranscriptProviderReasoningTraceIdentity{ItemID: "rs_1"}},
		{Provider: &TranscriptProviderReasoningTraceIdentity{ItemID: "rs_1", SummaryIndex: nil}},
		{Provider: &TranscriptProviderReasoningTraceIdentity{ItemID: "rs_1", SummaryIndex: &negative}},
		{Provider: validProvider.Provider, Kent: &kent},
		{Kent: &runtimeids.ReasoningTraceID{}},
	}
	for _, identity := range invalid {
		if err := identity.Validate(); err == nil {
			t.Fatalf("accepted invalid identity: %+v", identity)
		}
	}
}

func TestTranscriptHydrationRejectsDuplicateReasoningTraceIdentityAndWrongOwner(t *testing.T) {
	stepID := transcriptTestStepID(t)
	index := int64(0)
	trace := TranscriptReasoningTraceUpdate{
		StepID: stepID,
		Identity: TranscriptReasoningTraceIdentity{
			Provider: &TranscriptProviderReasoningTraceIdentity{
				ItemID:       "rs_1",
				SummaryIndex: &index,
			},
		},
		CompactText: "Planning",
		Text:        "Planning",
	}
	hydration := TranscriptHydration{
		RuntimeReadModelUpdate: transcriptTestRuntimeReadModelUpdate(t),
		SessionIdentity:        transcriptTestSessionIdentity(t),
		SessionStatus:          transcriptTestSessionStatus(),
		CommittedRows:          []TranscriptCommittedRow{},
		ActiveReasoningTraces:  []TranscriptReasoningTraceUpdate{trace, trace},
	}
	if err := hydration.Validate(); err == nil {
		t.Fatal("accepted duplicate active reasoning trace identity")
	}

	otherStepID, err := runtimeids.ParseStepID("77777777-7777-4777-8777-777777777777")
	if err != nil {
		t.Fatalf("parse other step id: %v", err)
	}
	hydration.ActiveReasoningTraces = []TranscriptReasoningTraceUpdate{trace}
	hydration.ActiveReasoningTraces[0].StepID = otherStepID
	if err := hydration.Validate(); err == nil {
		t.Fatal("accepted active reasoning trace owned by another step")
	}
}

func TestTranscriptReasoningTraceCommittedRowCarriesProjectedTextAndNullableCorrelation(t *testing.T) {
	stepID := transcriptTestStepID(t)
	index := int64(0)
	durationMs := int64(321)
	identity := TranscriptReasoningTraceIdentity{
		Provider: &TranscriptProviderReasoningTraceIdentity{
			ItemID:       "rs_1",
			SummaryIndex: &index,
		},
	}
	row := TranscriptCommittedRow{
		Visibility: transcript.EntryVisibilityDetail,
		Integrity:  transcript.RowIntegrityValid,
		Kind:       TranscriptRowReasoningTrace,
		Locator:    transcript.CommittedRowLocator{EventSequence: 1, RowOrdinal: 1},
		ReasoningTrace: &TranscriptReasoningTraceRow{
			StepID:              stepID,
			CompactText:         "Planning",
			Text:                "Planning\nDetails",
			DurationMs:          &durationMs,
			ProvisionalIdentity: &identity,
		},
	}
	if err := row.Validate(); err != nil {
		t.Fatalf("validate correlated reasoning row: %v", err)
	}
	row.ReasoningTrace.ProvisionalIdentity = nil
	if err := row.Validate(); err != nil {
		t.Fatalf("validate completed-only reasoning row: %v", err)
	}
	zeroDurationMs := int64(0)
	for _, test := range []struct {
		name  string
		value *int64
		wire  string
	}{{"absent", nil, "null"}, {"zero", &zeroDurationMs, "0"}, {"positive", &durationMs, "321"}} {
		row.ReasoningTrace.DurationMs = test.value
		data, err := json.Marshal(row.ReasoningTrace)
		if err != nil {
			t.Fatalf("marshal %s duration: %v", test.name, err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(data, &fields); err != nil {
			t.Fatalf("decode %s duration: %v", test.name, err)
		}
		if got := string(fields["duration_ms"]); got != test.wire {
			t.Fatalf("%s duration wire = %s, want %s", test.name, got, test.wire)
		}
		if _, ok := fields["DurationMs"]; ok {
			t.Fatalf("%s duration used Go field name", test.name)
		}
	}
	durationMs = -1
	if err := row.Validate(); err == nil {
		t.Fatal("accepted negative reasoning duration")
	}
}

func TestTranscriptHydrationRejectsCommittedReasoningCorrelation(t *testing.T) {
	stepID := transcriptTestStepID(t)
	index := int64(0)
	identity := TranscriptReasoningTraceIdentity{
		Provider: &TranscriptProviderReasoningTraceIdentity{
			ItemID:       "rs_1",
			SummaryIndex: &index,
		},
	}
	hydration := TranscriptHydration{
		RuntimeReadModelUpdate: transcriptTestRuntimeReadModelUpdate(t),
		SessionIdentity:        transcriptTestSessionIdentity(t),
		SessionStatus:          transcriptTestSessionStatus(),
		CommittedRows: []TranscriptCommittedRow{{
			Visibility: transcript.EntryVisibilityDetail,
			Integrity:  transcript.RowIntegrityValid,
			Kind:       TranscriptRowReasoningTrace,
			Locator:    transcript.CommittedRowLocator{EventSequence: 1, RowOrdinal: 1},
			ReasoningTrace: &TranscriptReasoningTraceRow{
				StepID:              stepID,
				CompactText:         "Planning",
				Text:                "Planning",
				ProvisionalIdentity: &identity,
			},
		}},
	}
	if err := hydration.Validate(); err == nil {
		t.Fatal("accepted committed reasoning correlation in hydration")
	}
}

func TestTranscriptReasoningTraceContractDoesNotAddTimestampOrDuplicateLocator(t *testing.T) {
	for _, field := range []string{"Locator", "Timestamp", "CreatedAt", "RowID"} {
		if _, ok := reflect.TypeOf(TranscriptReasoningTraceRow{}).FieldByName(field); ok {
			t.Fatalf("TranscriptReasoningTraceRow unexpectedly exposes %s", field)
		}
	}
	if _, ok := reflect.TypeOf(TranscriptCommittedRow{}).FieldByName("Locator"); !ok {
		t.Fatal("TranscriptCommittedRow omits the shared committed-row Locator")
	}
	for _, field := range []string{"Timestamp", "CreatedAt", "RowID"} {
		if _, ok := reflect.TypeOf(TranscriptCommittedRow{}).FieldByName(field); ok {
			t.Fatalf("TranscriptCommittedRow unexpectedly exposes %s", field)
		}
	}
}
