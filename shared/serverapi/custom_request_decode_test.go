package serverapi

import (
	"encoding/json"
	"testing"
)

func TestCustomRequestUnmarshalIsRepresentationOnly(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		out  any
	}{
		{
			name: "run prompt top-level semantics",
			raw:  `{"client_request_id":"","intent":{"kind":"create_new","origin":{"kind":"independent"}},"prompt":"","timeout":0,"overrides":{}}`,
			out:  &RunPromptRequest{},
		},
		{
			name: "session plan top-level semantics",
			raw:  `{"client_request_id":"","mode":"","intent":{"kind":"create_new","origin":{"kind":"independent"}},"overrides":{}}`,
			out:  &SessionPlanRequest{},
		},
		{
			name: "draft operation semantics",
			raw:  `{"kind":"update_message"}`,
			out:  &WorkspaceChatDraftOperation{},
		},
		{
			name: "draft request semantics",
			raw:  `{"operation":{"kind":"unknown"}}`,
			out:  &WorkspaceChatDraftRequest{},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := json.Unmarshal([]byte(test.raw), test.out); err != nil {
				t.Fatalf("representation decode failed: %v", err)
			}
		})
	}
}

func TestCustomRequestUnmarshalStillRejectsMalformedRepresentation(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		out  any
	}{
		{name: "run prompt unknown field", raw: `{"unknown":true}`, out: &RunPromptRequest{}},
		{name: "session plan invalid nested intent", raw: `{"client_request_id":"id","mode":"headless","intent":{"kind":"invalid"},"overrides":{}}`, out: &SessionPlanRequest{}},
		{name: "draft operation unknown field", raw: `{"kind":"clear","unknown":true}`, out: &WorkspaceChatDraftOperation{}},
		{name: "draft request unknown field", raw: `{"operation":{"kind":"clear"},"unknown":true}`, out: &WorkspaceChatDraftRequest{}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := json.Unmarshal([]byte(test.raw), test.out); err == nil {
				t.Fatal("malformed representation decoded successfully")
			}
		})
	}
}

func TestWorkspaceChatDraftRequestValidateOwnsOperationSemantics(t *testing.T) {
	request := WorkspaceChatDraftRequest{
		Operation: WorkspaceChatDraftOperation{Kind: WorkspaceChatDraftUpdateMessage},
	}
	if err := request.Validate(); err == nil {
		t.Fatal("update_message without message validated")
	}
}
