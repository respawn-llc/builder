package clientui

import (
	"reflect"
	"testing"
)

func TestTranscriptContractHydratesAtOneThenCarriesAtomicRuntimeReadModelUpdateAtTwo(t *testing.T) {
	update := transcriptTestRuntimeReadModelUpdate(t)

	hydration := TranscriptMessage{
		Sequence: 1,
		Kind:     TranscriptMessageHydration,
		Payload: TranscriptPayload{
			Hydration: &TranscriptHydration{
				RuntimeReadModelUpdate: update,
				SessionIdentity:        transcriptTestSessionIdentity(t),
				SessionStatus:          transcriptTestSessionStatus(),
				CommittedRows:          []TranscriptCommittedRow{},
			},
		},
	}
	if err := hydration.Validate(); err != nil {
		t.Fatalf("validate hydration: %v", err)
	}

	live := TranscriptMessage{
		Sequence: 2,
		Kind:     TranscriptMessageRuntimeReadModelUpdate,
		Payload: TranscriptPayload{
			RuntimeReadModelUpdate: &update,
		},
	}
	if err := live.Validate(); err != nil {
		t.Fatalf("validate live runtime read-model update: %v", err)
	}
}

func TestTranscriptPayloadValidationPrecedesPerSubscriptionSequenceAssignment(t *testing.T) {
	update := transcriptTestRuntimeReadModelUpdate(t)
	message := TranscriptMessage{
		Kind:    TranscriptMessageRuntimeReadModelUpdate,
		Payload: TranscriptPayload{RuntimeReadModelUpdate: &update},
	}
	if err := message.ValidatePayload(); err != nil {
		t.Fatalf("validate unsequenced payload: %v", err)
	}
	if err := message.Validate(); err == nil {
		t.Fatal("accepted final transcript message without subscription sequence")
	}
}

func TestTranscriptContractRejectsSequenceOutsideMessageLifecycle(t *testing.T) {
	tests := []TranscriptMessage{
		{
			Sequence: 2,
			Kind:     TranscriptMessageHydration,
			Payload: TranscriptPayload{
				Hydration: &TranscriptHydration{},
			},
		},
		{
			Sequence: 1,
			Kind:     TranscriptMessageRuntimeReadModelUpdate,
			Payload: TranscriptPayload{
				RuntimeReadModelUpdate: &RuntimeReadModelUpdate{},
			},
		},
	}
	for _, message := range tests {
		if err := message.Validate(); err == nil {
			t.Fatalf("accepted message with invalid sequence lifecycle: %+v", message)
		}
	}
}

func TestTranscriptContractRequiresOnePayloadMatchingItsKind(t *testing.T) {
	tests := []TranscriptMessage{
		{Sequence: 2, Kind: TranscriptMessageRuntimeReadModelUpdate},
		{
			Sequence: 2,
			Kind:     TranscriptMessageRuntimeReadModelUpdate,
			Payload: TranscriptPayload{
				Hydration:              &TranscriptHydration{},
				RuntimeReadModelUpdate: &RuntimeReadModelUpdate{},
			},
		},
		{
			Sequence: 2,
			Kind:     TranscriptMessageRuntimeReadModelUpdate,
			Payload: TranscriptPayload{
				Hydration: &TranscriptHydration{},
			},
		},
		{
			Sequence: 2,
			Kind:     TranscriptMessageKind("unknown"),
			Payload: TranscriptPayload{
				RuntimeReadModelUpdate: &RuntimeReadModelUpdate{},
			},
		},
	}
	for _, message := range tests {
		if err := message.ValidatePayload(); err == nil {
			t.Fatalf("accepted invalid payload union: %+v", message)
		}
	}
}

func TestRuntimeReadModelUpdateOwnsTheOnlyReconciliationVersion(t *testing.T) {
	if _, present := reflect.TypeOf(RuntimeReadModelUpdate{}).FieldByName("Version"); !present {
		t.Fatal("RuntimeReadModelUpdate.Version is required")
	}
	for _, value := range []any{
		RuntimeInputReconciliationSnapshot{},
		RuntimeInputReconciliation{},
	} {
		if _, present := reflect.TypeOf(value).FieldByName("Version"); present {
			t.Fatalf("%T must not repeat RuntimeReadModelUpdate.Version", value)
		}
	}
}

func TestRuntimeFeedContractUsesPointersForOptionalScalarFacts(t *testing.T) {
	tests := []struct {
		owner any
		field string
	}{
		{owner: TranscriptSessionIdentity{}, field: "SessionName"},
		{owner: TranscriptUserRow{}, field: "CondensedText"},
		{owner: TranscriptAssistantRow{}, field: "CondensedText"},
		{owner: TranscriptToolRow{}, field: "ResultSummary"},
		{owner: TranscriptToolRow{}, field: "CondensedText"},
		{owner: TranscriptPrompt{}, field: "RecommendedOptionIndex"},
	}
	for _, test := range tests {
		field, present := reflect.TypeOf(test.owner).FieldByName(test.field)
		if !present {
			t.Fatalf("%T.%s is missing", test.owner, test.field)
		}
		if field.Type.Kind() != reflect.Pointer {
			t.Fatalf("%T.%s type = %s, want pointer-encoded absence", test.owner, test.field, field.Type)
		}
	}
}

func TestRuntimeReadModelUpdateRejectsInvalidNestedFacts(t *testing.T) {
	version, err := NewReadModelVersion("epoch-1", 1, 1)
	if err != nil {
		t.Fatalf("new read model version: %v", err)
	}
	clientRequestID := transcriptTestClientRequestID(t)
	valid := RuntimeReadModelUpdate{
		Version:  version,
		Activity: RuntimeActivity{State: RuntimeActivityRegisteredIdle, QueueAccepting: true},
	}
	tests := []RuntimeReadModelUpdate{
		{Activity: valid.Activity},
		{Version: version, Activity: RuntimeActivity{State: RuntimeActivityState("unknown")}},
		{
			Version:  version,
			Activity: valid.Activity,
			InputReconciliation: RuntimeInputReconciliationSnapshot{
				Operations: []RuntimeInputReconciliation{{
					Operation: RuntimeOperationRef{
						Kind:            RuntimeOperationKindSubmit,
						ClientRequestID: clientRequestID,
					},
					State: RuntimeInputReconciliationState("unknown_state"),
				}},
			},
		},
	}
	for _, update := range tests {
		if err := update.Validate(); err == nil {
			t.Fatalf("accepted invalid runtime read-model update: %+v", update)
		}
	}
}

func TestRuntimeInputReconciliationRejectsDuplicateOperationIdentity(t *testing.T) {
	operation := RuntimeInputReconciliation{
		Operation: RuntimeOperationRef{
			Kind:            RuntimeOperationKindSubmit,
			ClientRequestID: transcriptTestClientRequestID(t),
		},
		State: RuntimeInputReconciliationCommitted,
	}
	snapshot := RuntimeInputReconciliationSnapshot{
		Operations: []RuntimeInputReconciliation{operation, operation},
	}
	if err := snapshot.Validate(); err == nil {
		t.Fatal("accepted duplicate runtime reconciliation operation")
	}
}

func TestRuntimeActivityRejectsActiveIdentityOutsideRunningStates(t *testing.T) {
	active := &RuntimeActiveStep{
		RunID:      transcriptTestRunID(t),
		StepID:     transcriptTestStepID(t),
		ActiveKind: RuntimeActivityActiveKindUserTurn,
	}
	tests := []RuntimeActivity{
		{State: RuntimeActivityRegisteredIdle, ActiveStep: active},
		{State: RuntimeActivityStarting, QueueAccepting: true},
		{State: RuntimeActivityRunning},
	}
	for _, activity := range tests {
		if err := activity.Validate(); err == nil {
			t.Fatalf("accepted invalid runtime activity: %+v", activity)
		}
	}
}
