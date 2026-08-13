package session

import (
	"encoding/json"
	"testing"

	"core/shared/transcript"
)

func TestEventLogV2QuestionCompletionCorruptionMatrix(t *testing.T) {
	validPresentation := json.RawMessage(`{"ToolName":"ask_question","Question":"Choose","Suggestions":["first","second"]}`)
	validAnswer := json.RawMessage(`{"selected_option_number":2}`)
	tests := []struct {
		name        string
		toolName    string
		isError     bool
		answer      json.RawMessage
		committedAt *int64
	}{
		{
			name:        "successful Question missing typed answer",
			toolName:    askQuestionToolName,
			committedAt: int64Pointer(1),
		},
		{
			name:        "typed answer has no selected option or freeform",
			toolName:    askQuestionToolName,
			answer:      json.RawMessage(`{}`),
			committedAt: int64Pointer(1),
		},
		{
			name:        "typed answer has nonpositive selected option",
			toolName:    askQuestionToolName,
			answer:      json.RawMessage(`{"selected_option_number":0}`),
			committedAt: int64Pointer(1),
		},
		{
			name:        "typed answer has blank freeform",
			toolName:    askQuestionToolName,
			answer:      json.RawMessage(`{"freeform":" \t"}`),
			committedAt: int64Pointer(1),
		},
		{
			name:        "failed Question has typed answer",
			toolName:    askQuestionToolName,
			isError:     true,
			answer:      validAnswer,
			committedAt: int64Pointer(1),
		},
		{
			name:        "non-Question completion has typed answer",
			toolName:    "exec_command",
			answer:      validAnswer,
			committedAt: int64Pointer(1),
		},
		{
			name:     "successful Question missing committed timestamp",
			toolName: askQuestionToolName,
			answer:   validAnswer,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			line := v2CompletionFixtureLine(
				t,
				test.toolName,
				test.isError,
				validPresentation,
				test.answer,
				test.committedAt,
			)
			if _, err := decodeEventRecordV2(line); err == nil {
				t.Fatal("corrupt v2 Question completion decoded successfully")
			}
		})
	}
}

func TestEventLogV1QuestionCompletionAllowsAbsentV2Facts(t *testing.T) {
	line := v2CompletionFixtureLine(
		t,
		askQuestionToolName,
		false,
		json.RawMessage(`{"ToolName":"ask_question","Question":"Choose"}`),
		nil,
		nil,
	)
	if _, err := decodeEventRecordV1(line); err != nil {
		t.Fatalf("decode v1 Question completion without v2 facts: %v", err)
	}
}

func TestEventLogV2QuestionCompletionWireAddsNoQuestionOrSuggestionCopy(t *testing.T) {
	record, err := newEventRecord(
		1,
		nil,
		ToolCompletionRecord{
			CallID:     "call-question",
			Name:       askQuestionToolName,
			OutputKind: ToolOutputKindFunction,
			Output:     json.RawMessage(`"done"`),
			Presentation: json.RawMessage(
				`{"ToolName":"ask_question","Question":"Choose","Suggestions":["first","second"]}`,
			),
			QuestionAnswer: &QuestionAnswerRecord{SelectedOptionNumber: intPointer(2)},
		},
		func() *transcript.CommittedAtUnixMs {
			value := transcript.CommittedAtUnixMs(1)
			return &value
		}(),
	)
	if err != nil {
		t.Fatalf("create v2 Question completion: %v", err)
	}
	line, err := encodeEventRecordV2(record)
	if err != nil {
		t.Fatalf("encode v2 Question completion: %v", err)
	}
	var envelope struct {
		Payload map[string]json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(line, &envelope); err != nil {
		t.Fatalf("decode v2 Question completion wire: %v", err)
	}
	for _, field := range []string{"question", "suggestions", "recommended_option_index"} {
		if _, exists := envelope.Payload[field]; exists {
			t.Fatalf("v2 completion duplicated presentation-owned field %q", field)
		}
	}
}

func v2CompletionFixtureLine(
	t *testing.T,
	toolName string,
	isError bool,
	presentation json.RawMessage,
	answer json.RawMessage,
	committedAt *int64,
) []byte {
	t.Helper()
	payload := map[string]any{
		"call_id":      "call-question",
		"name":         toolName,
		"output_kind":  "function",
		"is_error":     isError,
		"output":       "flattened",
		"presentation": presentation,
	}
	if answer != nil {
		payload["question_answer"] = answer
	}
	envelope := map[string]any{
		"seq":     7,
		"kind":    EventKindToolCompletion,
		"payload": payload,
	}
	if committedAt != nil {
		envelope["committed_at_unix_ms"] = *committedAt
	}
	line, err := json.Marshal(envelope)
	if err != nil {
		t.Fatalf("encode v2 completion fixture: %v", err)
	}
	return line
}

func int64Pointer(value int64) *int64 {
	return &value
}
