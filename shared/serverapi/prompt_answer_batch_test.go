package serverapi

import (
	"encoding/json"
	"reflect"
	"testing"

	"core/shared/clientui"
	"core/shared/runtimeids"
)

func TestPromptAnswerBatchRequestValidatesTypedEntries(t *testing.T) {
	freeform := "Use the durable path"
	commentary := "This workspace is expected"
	selected := 2
	request := PromptAnswerBatchRequest{
		SessionID: mustPromptBatchSessionID(t),
		StepID:    mustPromptBatchStepID(t),
		Entries: []PromptAnswerBatchEntry{
			{
				ToolCallID: "question-1",
				QuestionAnswer: &PromptQuestionAnswer{
					SelectedOptionNumber: &selected,
					Freeform:             &freeform,
				},
			},
			{
				ToolCallID: "approval-1",
				ApprovalAnswer: &PromptApprovalAnswer{
					Decision:   clientui.ApprovalDecisionAllowOnce,
					Commentary: &commentary,
				},
			},
			{
				ToolCallID: "question-2",
				Declined:   &PromptDeclined{},
			},
		},
	}

	if err := request.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var wire map[string]any
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatalf("Unmarshal wire shape: %v", err)
	}
	if _, exists := wire["client_request_id"]; exists {
		t.Fatal("batch request unexpectedly contains client_request_id")
	}
	if _, exists := wire["error_message"]; exists {
		t.Fatal("batch request unexpectedly contains error_message")
	}
}

func TestPromptAnswerBatchRequestRejectsMalformedEntriesBeforeDelegation(t *testing.T) {
	blank := " \t"
	positive := 1
	zero := 0
	validQuestion := &PromptQuestionAnswer{SelectedOptionNumber: &positive}
	tests := []struct {
		name    string
		request PromptAnswerBatchRequest
	}{
		{
			name: "missing session",
			request: PromptAnswerBatchRequest{
				StepID:  mustPromptBatchStepID(t),
				Entries: []PromptAnswerBatchEntry{{ToolCallID: "question-1", QuestionAnswer: validQuestion}},
			},
		},
		{
			name: "missing step",
			request: PromptAnswerBatchRequest{
				SessionID: mustPromptBatchSessionID(t),
				Entries:   []PromptAnswerBatchEntry{{ToolCallID: "question-1", QuestionAnswer: validQuestion}},
			},
		},
		{
			name: "empty entries",
			request: PromptAnswerBatchRequest{
				SessionID: mustPromptBatchSessionID(t),
				StepID:    mustPromptBatchStepID(t),
			},
		},
		{
			name: "duplicate prompt",
			request: PromptAnswerBatchRequest{
				SessionID: mustPromptBatchSessionID(t),
				StepID:    mustPromptBatchStepID(t),
				Entries: []PromptAnswerBatchEntry{
					{ToolCallID: "question-1", QuestionAnswer: validQuestion},
					{ToolCallID: "question-1", Declined: &PromptDeclined{}},
				},
			},
		},
		{
			name: "blank prompt",
			request: PromptAnswerBatchRequest{
				SessionID: mustPromptBatchSessionID(t),
				StepID:    mustPromptBatchStepID(t),
				Entries:   []PromptAnswerBatchEntry{{ToolCallID: " ", Declined: &PromptDeclined{}}},
			},
		},
		{
			name: "padded prompt",
			request: PromptAnswerBatchRequest{
				SessionID: mustPromptBatchSessionID(t),
				StepID:    mustPromptBatchStepID(t),
				Entries:   []PromptAnswerBatchEntry{{ToolCallID: " question-1", Declined: &PromptDeclined{}}},
			},
		},
		{
			name: "missing union member",
			request: PromptAnswerBatchRequest{
				SessionID: mustPromptBatchSessionID(t),
				StepID:    mustPromptBatchStepID(t),
				Entries:   []PromptAnswerBatchEntry{{ToolCallID: "question-1"}},
			},
		},
		{
			name: "multiple union members",
			request: PromptAnswerBatchRequest{
				SessionID: mustPromptBatchSessionID(t),
				StepID:    mustPromptBatchStepID(t),
				Entries: []PromptAnswerBatchEntry{{
					ToolCallID:     "question-1",
					QuestionAnswer: validQuestion,
					Declined:       &PromptDeclined{},
				}},
			},
		},
		{
			name: "question missing answer",
			request: PromptAnswerBatchRequest{
				SessionID: mustPromptBatchSessionID(t),
				StepID:    mustPromptBatchStepID(t),
				Entries:   []PromptAnswerBatchEntry{{ToolCallID: "question-1", QuestionAnswer: &PromptQuestionAnswer{}}},
			},
		},
		{
			name: "question non-positive option",
			request: PromptAnswerBatchRequest{
				SessionID: mustPromptBatchSessionID(t),
				StepID:    mustPromptBatchStepID(t),
				Entries: []PromptAnswerBatchEntry{{
					ToolCallID:     "question-1",
					QuestionAnswer: &PromptQuestionAnswer{SelectedOptionNumber: &zero},
				}},
			},
		},
		{
			name: "question blank freeform",
			request: PromptAnswerBatchRequest{
				SessionID: mustPromptBatchSessionID(t),
				StepID:    mustPromptBatchStepID(t),
				Entries: []PromptAnswerBatchEntry{{
					ToolCallID:     "question-1",
					QuestionAnswer: &PromptQuestionAnswer{Freeform: &blank},
				}},
			},
		},
		{
			name: "approval invalid decision",
			request: PromptAnswerBatchRequest{
				SessionID: mustPromptBatchSessionID(t),
				StepID:    mustPromptBatchStepID(t),
				Entries: []PromptAnswerBatchEntry{{
					ToolCallID:     "approval-1",
					ApprovalAnswer: &PromptApprovalAnswer{Decision: clientui.ApprovalDecision("later")},
				}},
			},
		},
		{
			name: "approval blank commentary",
			request: PromptAnswerBatchRequest{
				SessionID: mustPromptBatchSessionID(t),
				StepID:    mustPromptBatchStepID(t),
				Entries: []PromptAnswerBatchEntry{{
					ToolCallID: "approval-1",
					ApprovalAnswer: &PromptApprovalAnswer{
						Decision:   clientui.ApprovalDecisionDeny,
						Commentary: &blank,
					},
				}},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.request.Validate(); err == nil {
				t.Fatal("malformed batch unexpectedly validated")
			}
		})
	}
}

func TestPromptAnswerBatchWireTypesUseStrongIdentitiesAndNoLegacyDispositionFields(t *testing.T) {
	requestType := reflect.TypeOf(PromptAnswerBatchRequest{})
	assertPromptBatchFieldType(t, requestType, "SessionID", reflect.TypeOf(runtimeids.SessionID{}))
	assertPromptBatchFieldType(t, requestType, "StepID", reflect.TypeOf(runtimeids.StepID{}))
	if _, exists := requestType.FieldByName("ClientRequestID"); exists {
		t.Fatal("PromptAnswerBatchRequest unexpectedly has ClientRequestID")
	}

	entryType := reflect.TypeOf(PromptAnswerBatchEntry{})
	assertPromptBatchFieldType(t, entryType, "ToolCallID", reflect.TypeOf(clientui.ToolCallID("")))
	if _, exists := entryType.FieldByName("ErrorMessage"); exists {
		t.Fatal("PromptAnswerBatchEntry unexpectedly has ErrorMessage")
	}

	questionType := reflect.TypeOf(PromptQuestionAnswer{})
	for _, forbidden := range []string{"Answer", "ErrorMessage"} {
		if _, exists := questionType.FieldByName(forbidden); exists {
			t.Fatalf("PromptQuestionAnswer unexpectedly has legacy field %s", forbidden)
		}
	}
	declinedType := reflect.TypeOf(PromptDeclined{})
	if declinedType.NumField() != 0 {
		t.Fatalf("PromptDeclined fields = %d, want none", declinedType.NumField())
	}
}

func TestPromptAnswerBatchResponseValidationAndCorrelationIgnoreResultOrder(t *testing.T) {
	request := validPromptAnswerBatchRequest(t)
	response := PromptAnswerBatchResponse{Results: []PromptAnswerBatchResult{
		{ToolCallID: "approval-1", Outcome: PromptAnswerBatchOutcomeSkipped},
		{ToolCallID: "question-1", Outcome: PromptAnswerBatchOutcomeResolved},
	}}
	if err := response.Validate(); err != nil {
		t.Fatalf("response Validate: %v", err)
	}
	if err := ValidatePromptAnswerBatchResponse(request, response); err != nil {
		t.Fatalf("reordered exact response set rejected: %v", err)
	}

	tests := []struct {
		name     string
		response PromptAnswerBatchResponse
	}{
		{
			name: "missing identity",
			response: PromptAnswerBatchResponse{Results: []PromptAnswerBatchResult{
				{ToolCallID: "question-1", Outcome: PromptAnswerBatchOutcomeResolved},
			}},
		},
		{
			name: "foreign identity",
			response: PromptAnswerBatchResponse{Results: []PromptAnswerBatchResult{
				{ToolCallID: "question-1", Outcome: PromptAnswerBatchOutcomeResolved},
				{ToolCallID: "foreign", Outcome: PromptAnswerBatchOutcomeSkipped},
			}},
		},
		{
			name: "duplicate identity",
			response: PromptAnswerBatchResponse{Results: []PromptAnswerBatchResult{
				{ToolCallID: "question-1", Outcome: PromptAnswerBatchOutcomeResolved},
				{ToolCallID: "question-1", Outcome: PromptAnswerBatchOutcomeSkipped},
			}},
		},
		{
			name: "blank identity",
			response: PromptAnswerBatchResponse{Results: []PromptAnswerBatchResult{
				{ToolCallID: "", Outcome: PromptAnswerBatchOutcomeResolved},
				{ToolCallID: "approval-1", Outcome: PromptAnswerBatchOutcomeSkipped},
			}},
		},
		{
			name: "invalid outcome",
			response: PromptAnswerBatchResponse{Results: []PromptAnswerBatchResult{
				{ToolCallID: "question-1", Outcome: PromptAnswerBatchOutcome("later")},
				{ToolCallID: "approval-1", Outcome: PromptAnswerBatchOutcomeSkipped},
			}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := ValidatePromptAnswerBatchResponse(request, test.response); err == nil {
				t.Fatal("malformed response unexpectedly correlated")
			}
		})
	}
}

func validPromptAnswerBatchRequest(t *testing.T) PromptAnswerBatchRequest {
	t.Helper()
	selected := 1
	return PromptAnswerBatchRequest{
		SessionID: mustPromptBatchSessionID(t),
		StepID:    mustPromptBatchStepID(t),
		Entries: []PromptAnswerBatchEntry{
			{ToolCallID: "question-1", QuestionAnswer: &PromptQuestionAnswer{SelectedOptionNumber: &selected}},
			{ToolCallID: "approval-1", Declined: &PromptDeclined{}},
		},
	}
}

func mustPromptBatchSessionID(t *testing.T) runtimeids.SessionID {
	t.Helper()
	id, err := runtimeids.ParseSessionID("11111111-1111-4111-8111-111111111111")
	if err != nil {
		t.Fatalf("ParseSessionID: %v", err)
	}
	return id
}

func mustPromptBatchStepID(t *testing.T) runtimeids.StepID {
	t.Helper()
	id, err := runtimeids.ParseStepID("22222222-2222-4222-8222-222222222222")
	if err != nil {
		t.Fatalf("ParseStepID: %v", err)
	}
	return id
}

func assertPromptBatchFieldType(t *testing.T, owner reflect.Type, fieldName string, want reflect.Type) {
	t.Helper()
	field, exists := owner.FieldByName(fieldName)
	if !exists {
		t.Fatalf("%s missing field %s", owner.Name(), fieldName)
	}
	if field.Type != want {
		t.Fatalf("%s.%s type = %v, want %v", owner.Name(), fieldName, field.Type, want)
	}
}
