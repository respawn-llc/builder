package serverapi

import (
	"encoding/json"
	"strings"
	"testing"

	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/runtimeinput"
)

func TestRuntimeUserTurnInputIsAValidatedDiscriminatedUnion(t *testing.T) {
	text := runtimeinput.Text("hello")
	if err := text.Validate(); err != nil {
		t.Fatalf("text Validate: %v", err)
	}
	prompt := runtimeinput.Command("prompt:review", "src")
	if err := prompt.Validate(); err != nil {
		t.Fatalf("prompt Validate: %v", err)
	}

	wire, err := json.Marshal(prompt)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var decoded RuntimeUserTurnInput
	if err := json.Unmarshal(wire, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if err := decoded.Validate(); err != nil {
		t.Fatalf("decoded Validate: %v", err)
	}
	if decoded.Kind != RuntimeUserTurnInputKindPromptCommand ||
		decoded.PromptCommand == nil ||
		decoded.PromptCommand.Name != "prompt:review" {
		t.Fatalf("decoded = %+v", decoded)
	}
}

func TestRuntimeUserTurnInputRejectsInvalidCardinality(t *testing.T) {
	tests := []RuntimeUserTurnInput{
		{},
		{Kind: RuntimeUserTurnInputKindText},
		{Kind: RuntimeUserTurnInputKindText, Text: runtimeInputStringPtr("text"), PromptCommand: &RuntimePromptCommandInput{Name: "prompt:x"}},
		{Kind: RuntimeUserTurnInputKindPromptCommand, PromptCommand: &RuntimePromptCommandInput{}},
		{Kind: RuntimeUserTurnInputKind("other"), Text: stringPtr("text")},
	}
	for _, input := range tests {
		if err := input.Validate(); err == nil {
			t.Fatalf("Validate(%+v) succeeded", input)
		}
	}
}

func TestRuntimeSubmitUserTurnRequestUsesInputAndRejectsMissingInput(t *testing.T) {
	req := RuntimeSubmitUserTurnRequest{
		ClientRequestID: "request-1",
		SessionID:       "session-1",
		Input:           runtimeinput.Text("hello"),
		OperationRef: clientui.RuntimeOperationRef{
			Kind:            clientui.RuntimeOperationKindSubmit,
			ClientRequestID: runtimeids.NewRuntimeClientRequestID(),
		},
	}
	if err := req.Validate(); err == nil || !strings.Contains(err.Error(), "operation_ref") {
		t.Fatalf("Validate = %v, want existing operation validation", err)
	}
	req.Input = RuntimeUserTurnInput{}
	if err := req.Validate(); err == nil {
		t.Fatal("Validate missing input succeeded")
	}
}

func runtimeInputStringPtr(value string) *string {
	return &value
}
