package sessionview

import (
	"encoding/json"
	"testing"

	"core/server/session"
)

func TestQuestionHistoryProjectionSkipsMalformedCompletionPresentation(t *testing.T) {
	tests := []struct {
		name         string
		version      int
		presentation json.RawMessage
		output       json.RawMessage
		selected     *int
	}{
		{
			name:     "v2 absent presentation",
			version:  session.EventLogVersionV2,
			output:   json.RawMessage(`"flattened"`),
			selected: sessionViewIntPointer(1),
		},
		{
			name:         "v2 invalid presentation",
			version:      session.EventLogVersionV2,
			presentation: json.RawMessage(`{"ToolName":"ask_question"}`),
			output:       json.RawMessage(`"flattened"`),
			selected:     sessionViewIntPointer(1),
		},
		{
			name:         "v2 selected option outside Suggestions",
			version:      session.EventLogVersionV2,
			presentation: questionHistoryPresentation(`["only"]`),
			output:       json.RawMessage(`"flattened"`),
			selected:     sessionViewIntPointer(2),
		},
		{
			name:     "v1 absent presentation",
			version:  session.EventLogVersionV1,
			output:   json.RawMessage(`"flattened"`),
			selected: nil,
		},
		{
			name:         "v1 non-string flattened output",
			version:      session.EventLogVersionV1,
			presentation: questionHistoryPresentation(`[]`),
			output:       json.RawMessage(`{"summary":"not a string"}`),
			selected:     nil,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			completion := session.ToolCompletionRecord{
				CallID:       "call-question",
				Name:         "ask_question",
				OutputKind:   session.ToolOutputKindFunction,
				Output:       test.output,
				Presentation: test.presentation,
			}
			if test.version == session.EventLogVersionV2 {
				completion.QuestionAnswer = &session.QuestionAnswerRecord{
					SelectedOptionNumber: test.selected,
				}
			}
			record, err := session.NewEventRecord(1, nil, completion)
			if err != nil {
				t.Fatalf("create malformed projection fixture: %v", err)
			}
			projected, err := projectQuestionHistoryRecord(record, test.version)
			if err != nil {
				t.Fatalf("project malformed completion: %v", err)
			}
			if projected != nil {
				t.Fatalf("malformed completion projected as %#v", projected)
			}
		})
	}
}

func questionHistoryPresentation(suggestionsJSON string) json.RawMessage {
	return json.RawMessage(
		`{"ToolName":"ask_question","Question":"Choose","Suggestions":` +
			suggestionsJSON + `}`,
	)
}

func sessionViewIntPointer(value int) *int {
	return &value
}
