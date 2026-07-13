package llm

import (
	"encoding/json"
	"testing"

	"github.com/openai/openai-go/v3/responses"
)

// TestMarshalOpenAIWirePayloadMatchesTransportPath verifies the exported
// inspection seam produces byte-identical output to the internal transport
// buildPayload + json.Marshal path that the live HTTP transport uses to build the
// request body (via ResponseNewParams.MarshalJSON, the same Marshaler the
// openai-go requestconfig layer invokes).
func TestMarshalOpenAIWirePayloadMatchesTransportPath(t *testing.T) {
	req := OpenAIRequest{ToolChoiceMode: ToolChoiceModeAutomatic,
		Model:                   "gpt-5.5",
		Temperature:             1,
		SystemPrompt:            "you are helpful",
		PromptCacheKey:          "session-123",
		Items:                   []ResponseItem{{Type: "message", Role: "user", Content: "hi"}},
		Tools:                   []Tool{{Name: "exec_command", Description: "run a command"}},
		ReasoningEffort:         "medium",
		SupportsReasoningEffort: true,
	}
	store := false
	verbosity := "low"
	mode := OpenAIAuthMode{}
	caps, _ := LookupProviderCapabilityContract("chatgpt-codex")

	exported, err := MarshalOpenAIWirePayload(req, store, verbosity, mode, caps)
	if err != nil {
		t.Fatalf("MarshalOpenAIWirePayload: %v", err)
	}
	transport := &HTTPTransport{Store: store, ModelVerbosity: verbosity}
	params, err := transport.buildPayload(req, mode, caps)
	if err != nil {
		t.Fatalf("buildPayload: %v", err)
	}
	internal, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("json.Marshal params: %v", err)
	}
	if string(exported) != string(internal) {
		t.Fatalf("exported seam output differs from internal transport path:\nexported: %s\ninternal: %s", exported, internal)
	}
	// Sanity: the marshaled bytes must round-trip back into the SDK type.
	var round responses.ResponseNewParams
	if err := json.Unmarshal(internal, &round); err != nil {
		t.Fatalf("round-trip unmarshal: %v", err)
	}
}

func TestMarshalOpenAIWirePayloadSerializesRequiredToolChoice(t *testing.T) {
	req := OpenAIRequest{
		Model:          "gpt-5",
		ToolChoiceMode: ToolChoiceModeRequired,
		Tools:          []Tool{{Name: "shell"}},
	}
	caps, _ := LookupProviderCapabilityContract("openai")
	payload, err := MarshalOpenAIWirePayload(req, false, "", OpenAIAuthMode{}, caps)
	if err != nil {
		t.Fatalf("MarshalOpenAIWirePayload: %v", err)
	}
	var object map[string]any
	if err := json.Unmarshal(payload, &object); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if object["tool_choice"] != "required" {
		t.Fatalf("tool_choice = %#v, want required", object["tool_choice"])
	}
}
