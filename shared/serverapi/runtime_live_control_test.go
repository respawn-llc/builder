package serverapi

import (
	"encoding/json"
	"errors"
	"testing"
)

const (
	validLiveSessionID       = "9b9447ad-04e7-4c70-b4b0-f0eb1a53b47d"
	validLiveClientRequestID = "8b0364cc-5c6c-412e-a4e8-31380661d1e1"
	validLiveQueueItemID     = "540a27aa-1e97-4696-8483-6d528ff8bbdd"
)

func TestRuntimeLiveSteerRequestValidateUsesUUIDV4Boundaries(t *testing.T) {
	req := RuntimeLiveSteerRequest{
		ClientRequestID: validLiveClientRequestID,
		SessionID:       validLiveSessionID,
		Text:            " steer the run ",
	}
	if err := req.Validate(); err != nil {
		t.Fatalf("Validate valid request: %v", err)
	}

	cases := []RuntimeLiveSteerRequest{
		{ClientRequestID: "request-1", SessionID: validLiveSessionID, Text: "text"},
		{ClientRequestID: validLiveClientRequestID, SessionID: "session-1", Text: "text"},
		{ClientRequestID: validLiveClientRequestID, SessionID: "018f3f8c-5b1a-7b72-8cb9-01af2c01a7e8", Text: "text"},
		{ClientRequestID: validLiveClientRequestID, SessionID: validLiveSessionID, Text: " \t "},
	}
	for _, tc := range cases {
		if err := tc.Validate(); err == nil {
			t.Fatalf("Validate accepted invalid request: %+v", tc)
		}
	}
}

func TestRuntimeLiveStopRequestValidateAndStatus(t *testing.T) {
	if err := (RuntimeLiveStopRequest{
		ClientRequestID: validLiveClientRequestID,
		SessionID:       validLiveSessionID,
	}).Validate(); err != nil {
		t.Fatalf("Validate valid stop: %v", err)
	}
	if err := (RuntimeLiveStopResponse{Status: RuntimeLiveStopStatusStopped}).Validate(); err != nil {
		t.Fatalf("Validate stopped status: %v", err)
	}
	if err := (RuntimeLiveStopResponse{Status: RuntimeLiveStopStatusIdle}).Validate(); err != nil {
		t.Fatalf("Validate idle status: %v", err)
	}
	if err := (RuntimeLiveStopResponse{Status: RuntimeLiveStopStatus("paused")}).Validate(); err == nil {
		t.Fatal("Validate accepted unsupported stop status")
	}
}

func TestRuntimeLiveWaitRequestAndResponseValidation(t *testing.T) {
	if err := (RuntimeLiveWaitRequest{SessionID: validLiveSessionID}).Validate(); err != nil {
		t.Fatalf("Validate valid wait: %v", err)
	}
	if err := (RuntimeLiveWaitResponse{
		SessionID:      validLiveSessionID,
		SessionName:    "Session",
		Result:         stringPtr("final"),
		DurationMillis: 42,
		LiveRunGroupID: validLiveQueueItemID,
		TerminalRunID:  validLiveQueueItemID,
		TerminalStepID: validLiveQueueItemID,
		TerminalStatus: "completed",
		ResultKind:     RuntimeLiveResultKindAssistantFinalAnswer,
	}).Validate(); err != nil {
		t.Fatalf("Validate valid wait response: %v", err)
	}
	if err := (RuntimeLiveWaitResponse{
		SessionID:      validLiveSessionID,
		SessionName:    "Session",
		Result:         stringPtr("final"),
		DurationMillis: -1,
		LiveRunGroupID: validLiveQueueItemID,
		TerminalRunID:  validLiveQueueItemID,
		TerminalStepID: validLiveQueueItemID,
		TerminalStatus: "completed",
		ResultKind:     RuntimeLiveResultKindAssistantFinalAnswer,
	}).Validate(); err == nil {
		t.Fatal("Validate accepted negative duration")
	}
}

func TestRuntimeLiveWaitResponseSerializesNoAnswerReasonAsNull(t *testing.T) {
	raw, err := json.Marshal(RuntimeLiveWaitResponse{
		SessionID:      validLiveSessionID,
		SessionName:    "Session",
		Result:         stringPtr("final"),
		DurationMillis: 42,
		LiveRunGroupID: validLiveQueueItemID,
		TerminalRunID:  validLiveQueueItemID,
		TerminalStepID: validLiveQueueItemID,
		TerminalStatus: "completed",
		ResultKind:     RuntimeLiveResultKindAssistantFinalAnswer,
	})
	if err != nil {
		t.Fatalf("Marshal wait response: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("Unmarshal wait response: %v", err)
	}
	if value, ok := decoded["no_answer_reason"]; !ok || value != nil {
		t.Fatalf("no_answer_reason = %#v, want JSON null", value)
	}
}

func stringPtr(value string) *string {
	return &value
}

func TestRuntimeLiveControlSentinels(t *testing.T) {
	if !errors.Is(ErrRuntimeNoActiveRun, ErrRuntimeNoActiveRun) {
		t.Fatal("ErrRuntimeNoActiveRun does not satisfy errors.Is")
	}
	if !errors.Is(ErrRuntimeNoFinalAnswer, ErrRuntimeNoFinalAnswer) {
		t.Fatal("ErrRuntimeNoFinalAnswer does not satisfy errors.Is")
	}
}
