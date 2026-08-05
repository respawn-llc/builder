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
		ReasoningTrace: &TranscriptReasoningTraceRow{
			StepID:              stepID,
			CompactText:         "Planning",
			Text:                "Planning\nDetails",
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

func TestTranscriptReasoningTraceContractDoesNotAddCommittedLocatorOrTimestamp(t *testing.T) {
	for _, owner := range []any{
		TranscriptReasoningTraceRow{},
		TranscriptCommittedRow{},
	} {
		for _, field := range []string{"Locator", "Timestamp", "CreatedAt", "RowID"} {
			if _, ok := reflect.TypeOf(owner).FieldByName(field); ok {
				t.Fatalf("%T unexpectedly exposes %s", owner, field)
			}
		}
	}
}
