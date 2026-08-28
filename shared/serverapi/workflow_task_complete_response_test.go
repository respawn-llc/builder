package serverapi

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestWorkflowTaskCompleteResponseEncodesExactlyOneNullableVariant(t *testing.T) {
	tests := []struct {
		name     string
		response WorkflowTaskCompleteResponse
		active   string
		inactive string
	}{
		{
			name:     "agent completion",
			response: WorkflowTaskCompleteResponse{AgentCompletion: &WorkflowTaskAgentCompletion{}},
			active:   "agent_completion",
			inactive: "forced_move",
		},
		{
			name:     "forced move",
			response: WorkflowTaskCompleteResponse{ForcedMove: &WorkflowTaskForcedCompletionMove{}},
			active:   "forced_move",
			inactive: "agent_completion",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.response.Validate(); err != nil {
				t.Fatalf("Validate: %v", err)
			}
			data, err := json.Marshal(test.response)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(data, &fields); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			if len(fields) != 2 {
				t.Fatalf("fields = %v, want both completion variants", fields)
			}
			if bytes.Equal(fields[test.active], []byte("null")) {
				t.Fatalf("%s = null, want active completion", test.active)
			}
			if !bytes.Equal(fields[test.inactive], []byte("null")) {
				t.Fatalf("%s = %s, want null", test.inactive, fields[test.inactive])
			}
		})
	}
}

func TestWorkflowTaskCompleteResponseRejectsMissingOrMultipleVariants(t *testing.T) {
	tests := []WorkflowTaskCompleteResponse{
		{},
		{
			AgentCompletion: &WorkflowTaskAgentCompletion{},
			ForcedMove:      &WorkflowTaskForcedCompletionMove{},
		},
	}
	for _, response := range tests {
		if err := response.Validate(); err == nil {
			t.Fatalf("Validate(%+v) succeeded, want exactly-one-variant error", response)
		}
	}
}
