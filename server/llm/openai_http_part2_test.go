package llm

import (
	"context"
	"core/shared/toolspec"
	"encoding/json"
	"errors"
	"github.com/openai/openai-go/v3/responses"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestBuildPayload_AppliesStructuredOutputJSONSchema(t *testing.T) {
	transport := NewHTTPTransport(staticAuth{})
	payload, err := transport.buildPayload(OpenAIRequest{ToolChoiceMode: ToolChoiceModeAutomatic,
		Model: "gpt-5",
		StructuredOutput: &StructuredOutput{
			Name:   "reviewer_suggestions",
			Schema: json.RawMessage(`{"type":"object","properties":{"suggestions":{"type":"array","items":{"type":"string"}}},"required":["suggestions"],"additionalProperties":false}`),
			Strict: true,
		},
	}, OpenAIAuthMode{}, requireProviderCapabilities(t, transport, OpenAIAuthMode{}))
	if err != nil {
		t.Fatalf("build payload: %v", err)
	}

	jsonPayload := mustMarshalObject(t, payload)
	text, ok := jsonPayload["text"].(map[string]any)
	if !ok {
		t.Fatalf("expected text config in payload, got %#v", jsonPayload["text"])
	}
	format, ok := text["format"].(map[string]any)
	if !ok {
		t.Fatalf("expected text.format config in payload, got %#v", text["format"])
	}
	if format["type"] != "json_schema" {
		t.Fatalf("expected text.format.type=json_schema, got %#v", format["type"])
	}
	if format["name"] != "reviewer_suggestions" {
		t.Fatalf("expected text.format.name=reviewer_suggestions, got %#v", format["name"])
	}
	if strict, ok := format["strict"].(bool); !ok || !strict {
		t.Fatalf("expected text.format.strict=true, got %#v", format["strict"])
	}
}

func TestBuildPayload_PreservesNullableStructuredOutputSchemaProperties(t *testing.T) {
	transport := NewHTTPTransport(staticAuth{})
	payload, err := transport.buildPayload(OpenAIRequest{ToolChoiceMode: ToolChoiceModeAutomatic,
		Model: "gpt-5",
		StructuredOutput: &StructuredOutput{
			Name: "workflow_completion",
			Schema: json.RawMessage(`{
				"type": "object",
				"additionalProperties": false,
				"properties": {
					"transition": {"type": "string", "enum": ["blocked", "done"]},
					"commentary": {"type": "string"},
					"risk": {"type": ["string", "null"]},
					"summary": {"type": ["string", "null"]}
				},
				"required": ["transition", "commentary", "risk", "summary"]
			}`),
			Strict: true,
		},
	}, OpenAIAuthMode{}, requireProviderCapabilities(t, transport, OpenAIAuthMode{}))
	if err != nil {
		t.Fatalf("build payload: %v", err)
	}

	schema := structuredOutputPayloadSchema(t, mustMarshalObject(t, payload))
	if _, exists := schema["oneOf"]; exists {
		t.Fatalf("payload schema should not include oneOf: %+v", schema)
	}
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("payload schema properties missing: %+v", schema)
	}
	assertNullablePayloadProperty(t, properties, "summary")
	assertNullablePayloadProperty(t, properties, "risk")
}

func structuredOutputPayloadSchema(t *testing.T, jsonPayload map[string]any) map[string]any {
	t.Helper()
	text, ok := jsonPayload["text"].(map[string]any)
	if !ok {
		t.Fatalf("expected text config in payload, got %#v", jsonPayload["text"])
	}
	format, ok := text["format"].(map[string]any)
	if !ok {
		t.Fatalf("expected text.format config in payload, got %#v", text["format"])
	}
	schema, ok := format["schema"].(map[string]any)
	if !ok {
		t.Fatalf("expected text.format.schema object, got %#v", format["schema"])
	}
	return schema
}

func assertNullablePayloadProperty(t *testing.T, properties map[string]any, name string) {
	t.Helper()
	property, ok := properties[name].(map[string]any)
	if !ok {
		t.Fatalf("payload property %s missing: %+v", name, properties)
	}
	values, ok := property["type"].([]any)
	if !ok || len(values) != 2 {
		t.Fatalf("payload property %s type = %+v, want nullable string", name, property["type"])
	}
	if values[0] != "string" || values[1] != "null" {
		t.Fatalf("payload property %s type = %+v, want [string null]", name, values)
	}
}

func TestBuildPayload_AppliesConfiguredModelVerbosityForSupportedModels(t *testing.T) {
	transport := NewHTTPTransport(staticAuth{})
	transport.ModelVerbosity = "high"
	payload, err := transport.buildPayload(OpenAIRequest{ToolChoiceMode: ToolChoiceModeAutomatic, Model: "gpt-5"}, OpenAIAuthMode{}, requireProviderCapabilities(t, transport, OpenAIAuthMode{}))
	if err != nil {
		t.Fatalf("build payload: %v", err)
	}

	jsonPayload := mustMarshalObject(t, payload)
	text, ok := jsonPayload["text"].(map[string]any)
	if !ok {
		t.Fatalf("expected text config in payload, got %#v", jsonPayload["text"])
	}
	if got := text["verbosity"]; got != "high" {
		t.Fatalf("expected text.verbosity=high, got %#v", got)
	}
}

func TestBuildPayload_IgnoresConfiguredModelVerbosityForUnsupportedModels(t *testing.T) {
	transport := NewHTTPTransport(staticAuth{})
	transport.ModelVerbosity = "high"
	payload, err := transport.buildPayload(OpenAIRequest{ToolChoiceMode: ToolChoiceModeAutomatic, Model: "gpt-4.1"}, OpenAIAuthMode{}, requireProviderCapabilities(t, transport, OpenAIAuthMode{}))
	if err != nil {
		t.Fatalf("build payload: %v", err)
	}

	jsonPayload := mustMarshalObject(t, payload)
	if _, ok := jsonPayload["text"]; ok {
		t.Fatalf("expected text config to be omitted for unsupported model, got %#v", jsonPayload["text"])
	}
}

func TestBuildPayload_AppliesConfiguredModelVerbosityForUnknownFirstPartyModels(t *testing.T) {
	transport := NewHTTPTransport(staticAuth{})
	transport.ModelVerbosity = "high"
	payload, err := transport.buildPayload(OpenAIRequest{ToolChoiceMode: ToolChoiceModeAutomatic, Model: "gpt-5-preview"}, OpenAIAuthMode{}, requireProviderCapabilities(t, transport, OpenAIAuthMode{}))
	if err != nil {
		t.Fatalf("build payload: %v", err)
	}

	jsonPayload := mustMarshalObject(t, payload)
	text, ok := jsonPayload["text"].(map[string]any)
	if !ok {
		t.Fatalf("expected text config in payload, got %#v", jsonPayload["text"])
	}
	if got := text["verbosity"]; got != "high" {
		t.Fatalf("expected text.verbosity=high, got %#v", got)
	}
}

func TestBuildPayload_IgnoresConfiguredModelVerbosityForUnknownNonFirstPartyProviders(t *testing.T) {
	transport := NewHTTPTransport(staticAuth{})
	transport.ModelVerbosity = "high"
	payload, err := transport.buildPayload(OpenAIRequest{ToolChoiceMode: ToolChoiceModeAutomatic, Model: "gpt-5-preview"}, OpenAIAuthMode{}, ProviderCapabilities{ProviderID: "openai-compatible", IsOpenAIFirstParty: false})
	if err != nil {
		t.Fatalf("build payload: %v", err)
	}

	jsonPayload := mustMarshalObject(t, payload)
	if _, ok := jsonPayload["text"]; ok {
		t.Fatalf("expected text config to be omitted for unknown non-first-party provider, got %#v", jsonPayload["text"])
	}
}

func TestRequestBuildersUseProviderVerbosityCapabilityForUnknownModels(t *testing.T) {
	tests := []struct {
		name     string
		caps     ProviderCapabilities
		wantText bool
	}{
		{
			name: "enabled independently of first-party classification",
			caps: ProviderCapabilities{
				ProviderID:                "custom-enabled",
				IsOpenAIFirstParty:        false,
				SupportsProviderVerbosity: true,
			},
			wantText: true,
		},
		{
			name: "disabled independently of first-party classification",
			caps: ProviderCapabilities{
				ProviderID:                "custom-disabled",
				IsOpenAIFirstParty:        true,
				SupportsProviderVerbosity: false,
			},
			wantText: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			transport := NewHTTPTransport(staticAuth{})
			transport.ModelVerbosity = "high"

			responsePayload, err := transport.buildPayload(OpenAIRequest{ToolChoiceMode: ToolChoiceModeAutomatic, Model: "operator-alias"}, OpenAIAuthMode{}, tt.caps)
			if err != nil {
				t.Fatalf("build response payload: %v", err)
			}
			assertTextVerbosityPresence(t, mustMarshalObject(t, responsePayload), tt.wantText)

			inputTokenCountPayload, err := transport.buildInputTokenCountParams(OpenAIRequest{ToolChoiceMode: ToolChoiceModeAutomatic, Model: "operator-alias"}, tt.caps)
			if err != nil {
				t.Fatalf("build input-token-count payload: %v", err)
			}
			assertTextVerbosityPresence(t, mustMarshalJSONMap(t, inputTokenCountPayload), tt.wantText)
		})
	}
}

func assertTextVerbosityPresence(t *testing.T, payload map[string]any, wantText bool) {
	t.Helper()
	text, ok := payload["text"].(map[string]any)
	if ok != wantText {
		t.Fatalf("text config presence = %v, want %v, payload=%#v", ok, wantText, payload)
	}
	if wantText && text["verbosity"] != "high" {
		t.Fatalf("text.verbosity = %#v, want high", text["verbosity"])
	}
}

func TestBuildPayload_MergesConfiguredModelVerbosityWithStructuredOutput(t *testing.T) {
	transport := NewHTTPTransport(staticAuth{})
	transport.ModelVerbosity = "low"
	payload, err := transport.buildPayload(OpenAIRequest{ToolChoiceMode: ToolChoiceModeAutomatic,
		Model: "gpt-5",
		StructuredOutput: &StructuredOutput{
			Name:   "reviewer_suggestions",
			Schema: json.RawMessage(`{"type":"object","properties":{"suggestions":{"type":"array","items":{"type":"string"}}},"required":["suggestions"],"additionalProperties":false}`),
			Strict: true,
		},
	}, OpenAIAuthMode{}, requireProviderCapabilities(t, transport, OpenAIAuthMode{}))
	if err != nil {
		t.Fatalf("build payload: %v", err)
	}

	jsonPayload := mustMarshalObject(t, payload)
	text, ok := jsonPayload["text"].(map[string]any)
	if !ok {
		t.Fatalf("expected text config in payload, got %#v", jsonPayload["text"])
	}
	if got := text["verbosity"]; got != "low" {
		t.Fatalf("expected text.verbosity=low, got %#v", got)
	}
	if _, ok := text["format"].(map[string]any); !ok {
		t.Fatalf("expected text.format to remain present, got %#v", text["format"])
	}
}

func TestBuildPayload_AppliesReasoningEffortForOpenAIModels(t *testing.T) {
	transport := NewHTTPTransport(staticAuth{})
	payload, err := transport.buildPayload(OpenAIRequest{ToolChoiceMode: ToolChoiceModeAutomatic,
		Model:           "gpt-5",
		ReasoningEffort: "xhigh",
	}, OpenAIAuthMode{}, requireProviderCapabilities(t, transport, OpenAIAuthMode{}))
	if err != nil {
		t.Fatalf("build payload: %v", err)
	}
	if payload.Reasoning.Effort != "xhigh" {
		t.Fatalf("expected effort xhigh, got %q", payload.Reasoning.Effort)
	}
	if payload.Reasoning.Summary != "concise" {
		t.Fatalf("expected concise reasoning summary, got %q", payload.Reasoning.Summary)
	}
	if len(payload.Include) != 1 || payload.Include[0] != responses.ResponseIncludableReasoningEncryptedContent {
		t.Fatalf("expected reasoning.encrypted_content include, got %+v", payload.Include)
	}
}

func TestBuildPayload_SkipsReasoningSummaryForUnknownModels(t *testing.T) {
	transport := NewHTTPTransport(staticAuth{})
	payload, err := transport.buildPayload(OpenAIRequest{ToolChoiceMode: ToolChoiceModeAutomatic,
		Model:           "custom-model",
		ReasoningEffort: "high",
	}, OpenAIAuthMode{}, requireProviderCapabilities(t, transport, OpenAIAuthMode{}))
	if err != nil {
		t.Fatalf("build payload: %v", err)
	}
	if payload.Reasoning.Effort != "high" {
		t.Fatalf("expected reasoning payload for unknown model, got %+v", payload.Reasoning)
	}
	if payload.Reasoning.Summary != "" {
		t.Fatalf("expected reasoning.summary to be omitted for unknown model, got %q", payload.Reasoning.Summary)
	}
	if len(payload.Include) == 0 {
		t.Fatalf("expected encrypted reasoning include for unknown model, got %+v", payload.Include)
	}

	jsonPayload := mustMarshalObject(t, payload)
	reasoning, ok := jsonPayload["reasoning"].(map[string]any)
	if !ok {
		t.Fatalf("expected reasoning to be present for unknown model, got %+v", jsonPayload)
	}
	if _, ok := reasoning["summary"]; ok {
		t.Fatalf("expected reasoning.summary omitted for unknown model, got %+v", reasoning)
	}
}

func TestBuildPayload_SkipsReasoningSummaryForCodexSpark(t *testing.T) {
	transport := NewHTTPTransport(staticAuth{})
	transport.ModelVerbosity = "medium"
	payload, err := transport.buildPayload(OpenAIRequest{ToolChoiceMode: ToolChoiceModeAutomatic,
		Model:           "gpt-5.3-codex-spark",
		ReasoningEffort: "high",
	}, OpenAIAuthMode{IsOAuth: true}, requireProviderCapabilities(t, transport, OpenAIAuthMode{IsOAuth: true}))
	if err != nil {
		t.Fatalf("build payload: %v", err)
	}
	if payload.Reasoning.Effort != "high" {
		t.Fatalf("expected reasoning payload for codex spark, got %+v", payload.Reasoning)
	}
	if payload.Reasoning.Summary != "" {
		t.Fatalf("expected reasoning.summary omitted for codex spark, got %+v", payload.Reasoning)
	}
	if len(payload.Include) != 1 || payload.Include[0] != responses.ResponseIncludableReasoningEncryptedContent {
		t.Fatalf("expected reasoning.encrypted_content include for codex spark, got %+v", payload.Include)
	}

	jsonPayload := mustMarshalObject(t, payload)
	reasoning, ok := jsonPayload["reasoning"].(map[string]any)
	if !ok {
		t.Fatalf("expected reasoning to be present for codex spark, got %+v", jsonPayload)
	}
	if _, ok := reasoning["summary"]; ok {
		t.Fatalf("expected reasoning.summary omitted for codex spark, got %+v", reasoning)
	}
	text, ok := jsonPayload["text"].(map[string]any)
	if !ok {
		t.Fatalf("expected text config in payload for codex spark, got %#v", jsonPayload["text"])
	}
	if got := text["verbosity"]; got != "medium" {
		t.Fatalf("expected text.verbosity=medium for codex spark, got %#v", got)
	}
}

func TestBuildPayload_AppliesFastModeForCodexProvider(t *testing.T) {
	transport := NewHTTPTransport(staticAuth{})
	payload, err := transport.buildPayload(OpenAIRequest{ToolChoiceMode: ToolChoiceModeAutomatic,
		Model:    "gpt-5.3-codex",
		FastMode: true,
	}, OpenAIAuthMode{IsOAuth: true}, requireProviderCapabilities(t, transport, OpenAIAuthMode{IsOAuth: true}))
	if err != nil {
		t.Fatalf("build payload: %v", err)
	}
	if payload.ServiceTier != responses.ResponseNewParamsServiceTierPriority {
		t.Fatalf("expected priority service tier, got %q", payload.ServiceTier)
	}

	jsonPayload := mustMarshalObject(t, payload)
	if got := jsonPayload["service_tier"]; got != "priority" {
		t.Fatalf("expected service_tier=priority, got %#v", got)
	}
}

func TestBuildPayload_AppliesFastModeForOpenAIProvider(t *testing.T) {
	transport := NewHTTPTransport(staticAuth{})
	payload, err := transport.buildPayload(OpenAIRequest{ToolChoiceMode: ToolChoiceModeAutomatic,
		Model:    "gpt-5.3-codex",
		FastMode: true,
	}, OpenAIAuthMode{}, requireProviderCapabilities(t, transport, OpenAIAuthMode{}))
	if err != nil {
		t.Fatalf("build payload: %v", err)
	}
	if payload.ServiceTier != responses.ResponseNewParamsServiceTierPriority {
		t.Fatalf("expected priority service tier for openai provider, got %q", payload.ServiceTier)
	}

	jsonPayload := mustMarshalObject(t, payload)
	if got := jsonPayload["service_tier"]; got != "priority" {
		t.Fatalf("expected service_tier=priority, got %#v", got)
	}
}

func TestBuildPayload_SkipsFastModeForRemoteOpenAICompatibleProvider(t *testing.T) {
	transport := NewHTTPTransport(staticAuth{})
	transport.BaseURL = "https://example.openai.azure.com/openai/v1"
	payload, err := transport.buildPayload(OpenAIRequest{ToolChoiceMode: ToolChoiceModeAutomatic,
		Model:    "gpt-5.3-codex",
		FastMode: true,
	}, OpenAIAuthMode{}, requireProviderCapabilities(t, transport, OpenAIAuthMode{}))
	if err != nil {
		t.Fatalf("build payload: %v", err)
	}
	if payload.ServiceTier != "" {
		t.Fatalf("expected no service tier for remote openai-compatible provider, got %q", payload.ServiceTier)
	}

	jsonPayload := mustMarshalObject(t, payload)
	if _, ok := jsonPayload["service_tier"]; ok {
		t.Fatalf("expected service_tier omitted, got %+v", jsonPayload["service_tier"])
	}

	providerCaps, err := transport.providerCapabilitiesForMode(OpenAIAuthMode{})
	if err != nil {
		t.Fatalf("resolve provider capabilities: %v", err)
	}
	if providerCaps.ProviderID != "openai-compatible" || providerCaps.IsOpenAIFirstParty || providerCaps.SupportsResponsesCompact || providerCaps.SupportsNativeWebSearch || providerCaps.SupportsRequestInputTokenCount {
		t.Fatalf("expected conservative remote openai-compatible capabilities, got %+v", providerCaps)
	}
}

func TestBuildPayload_DefaultsReasoningEffortForUnknownModelFamily(t *testing.T) {
	transport := NewHTTPTransport(staticAuth{})
	payload, err := transport.buildPayload(OpenAIRequest{ToolChoiceMode: ToolChoiceModeAutomatic,
		Model:           "custom-model",
		ReasoningEffort: "high",
	}, OpenAIAuthMode{}, requireProviderCapabilities(t, transport, OpenAIAuthMode{}))
	if err != nil {
		t.Fatalf("build payload: %v", err)
	}
	if payload.Reasoning.Effort != "high" {
		t.Fatalf("expected reasoning payload for unknown model, got %+v", payload.Reasoning)
	}
	if payload.Reasoning.Summary != "" {
		t.Fatalf("expected unknown model to omit reasoning summary, got %+v", payload.Reasoning)
	}
	if len(payload.Include) == 0 {
		t.Fatalf("expected encrypted reasoning include for unknown model, got %+v", payload.Include)
	}

	jsonPayload := mustMarshalObject(t, payload)
	if _, ok := jsonPayload["reasoning"]; !ok {
		t.Fatalf("expected reasoning to be present for unknown model, got %+v", jsonPayload)
	}
}

func TestBuildResponsesInput_AssistantReasoningItemsUseEncryptedContentOnly(t *testing.T) {
	items := mustBuildResponsesInput(t, ItemsFromMessages([]Message{
		{
			Role:    RoleAssistant,
			Content: "a1",
			ReasoningItems: []ReasoningItem{
				{ID: "rs_1", EncryptedContent: "enc_1"},
			},
		},
	}))
	if len(items) != 2 {
		t.Fatalf("expected assistant message + reasoning item, got %d", len(items))
	}

	jsonItems := mustMarshalItems(t, items)
	second := jsonItems[1]
	if second["type"] != "reasoning" {
		t.Fatalf("expected reasoning item type, got %#v", second["type"])
	}
	if second["id"] != "rs_1" {
		t.Fatalf("expected reasoning id rs_1, got %#v", second["id"])
	}
	if second["encrypted_content"] != "enc_1" {
		t.Fatalf("expected encrypted content enc_1, got %#v", second["encrypted_content"])
	}
	if text, ok := second["text"].(string); ok && strings.TrimSpace(text) != "" {
		t.Fatalf("expected no reasoning text to be serialized, got %q", text)
	}
}

func TestBuildPayload_AddsAdditionalPropertiesFalseToToolSchemas(t *testing.T) {
	transport := NewHTTPTransport(staticAuth{})
	payload, err := transport.buildPayload(OpenAIRequest{ToolChoiceMode: ToolChoiceModeAutomatic,
		Model: "gpt-5",
		Tools: []Tool{
			{
				Name:   "ask_question",
				Schema: json.RawMessage(`{"type":"object","required":["question"],"properties":{"question":{"type":"string"},"meta":{"type":"object","properties":{"foo":{"type":"string"}}}}}`),
			},
		},
	}, OpenAIAuthMode{}, requireProviderCapabilities(t, transport, OpenAIAuthMode{}))
	if err != nil {
		t.Fatalf("build payload: %v", err)
	}

	jsonPayload := mustMarshalObject(t, payload)
	tools, ok := jsonPayload["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("expected one tool, got %#v", jsonPayload["tools"])
	}
	tool, ok := tools[0].(map[string]any)
	if !ok {
		t.Fatalf("unexpected tool value: %#v", tools[0])
	}
	if strict, ok := tool["strict"].(bool); !ok || strict {
		t.Fatalf("expected function tool strict=false, got %#v", tool["strict"])
	}
	params, ok := tool["parameters"].(map[string]any)
	if !ok {
		t.Fatalf("expected parameters object, got %#v", tool["parameters"])
	}
	if got, ok := params["additionalProperties"].(bool); !ok || got {
		t.Fatalf("expected root additionalProperties=false, got %#v", params["additionalProperties"])
	}

	props, ok := params["properties"].(map[string]any)
	if !ok {
		t.Fatalf("expected root properties object, got %#v", params["properties"])
	}
	meta, ok := props["meta"].(map[string]any)
	if !ok {
		t.Fatalf("expected nested meta object schema, got %#v", props["meta"])
	}
	if got, ok := meta["additionalProperties"].(bool); !ok || got {
		t.Fatalf("expected nested additionalProperties=false, got %#v", meta["additionalProperties"])
	}
}

func TestBuildResponsesInput_CanonicalCompactionItemRoundTrip(t *testing.T) {
	items := mustBuildResponsesInput(t, []ResponseItem{
		{Type: ResponseItemTypeMessage, Role: RoleUser, Content: "u1"},
		{Type: ResponseItemTypeCompaction, ID: "cmp_1", EncryptedContent: "enc_1"},
	})
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}
	jsonItems := mustMarshalItems(t, items)
	if got := contentTypeAt(t, jsonItems[0]); got != "input_text" {
		t.Fatalf("expected user input text content, got %q", got)
	}
	if got := jsonItems[0]["role"]; got != "user" {
		t.Fatalf("expected user role, got %#v", got)
	}
	if got := jsonItems[1]["type"]; got != "compaction" {
		t.Fatalf("expected compaction item, got %#v", got)
	}
	if got := jsonItems[1]["encrypted_content"]; got != "enc_1" {
		t.Fatalf("unexpected compaction encrypted content: %#v", got)
	}
}

func TestParseOutputItems_PreservesCompactionItem(t *testing.T) {
	raw := []byte(`[
		{
			"type":"message",
			"role":"user",
			"id":"msg_1",
			"content":[{"type":"input_text","text":"hello"}]
		},
		{
			"type":"compaction",
			"id":"cmp_1",
			"encrypted_content":"enc_1"
		}
	]`)
	var output []responses.ResponseOutputItemUnion
	if err := json.Unmarshal(raw, &output); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}
	items, assistantText, assistantPhase, toolCalls, reasoning, reasoningItems := parseOutputItems(output)
	if assistantText != "" {
		t.Fatalf("expected no assistant text, got %q", assistantText)
	}
	if assistantPhase != "" {
		t.Fatalf("expected empty assistant phase, got %q", assistantPhase)
	}
	if len(toolCalls) != 0 || len(reasoning) != 0 || len(reasoningItems) != 0 {
		t.Fatalf("expected no tool/reasoning outputs, got calls=%d reasoning=%d encrypted=%d", len(toolCalls), len(reasoning), len(reasoningItems))
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 canonical items, got %d", len(items))
	}
	if items[1].Type != ResponseItemTypeCompaction || items[1].EncryptedContent != "enc_1" {
		t.Fatalf("unexpected compaction item: %+v", items[1])
	}
}

func TestParseOutputItems_UsesLastAssistantMessageWhenMultipleUnphased(t *testing.T) {
	raw := []byte(`[
		{
			"type":"message",
			"role":"assistant",
			"id":"msg_1",
			"content":[{"type":"output_text","text":"working..."}]
		},
		{
			"type":"message",
			"role":"assistant",
			"id":"msg_2",
			"content":[{"type":"output_text","text":"done"}]
		}
	]`)
	var output []responses.ResponseOutputItemUnion
	if err := json.Unmarshal(raw, &output); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}
	_, assistantText, assistantPhase, _, _, _ := parseOutputItems(output)
	if assistantText != "done" {
		t.Fatalf("assistantText = %q, want done", assistantText)
	}
	if assistantPhase != "" {
		t.Fatalf("assistantPhase = %q, want empty", assistantPhase)
	}
}

func TestParseOutputItems_UsesTrailingAssistantPhaseBlock(t *testing.T) {
	raw := []byte(`[
		{
			"type":"message",
			"role":"assistant",
			"id":"msg_1",
			"phase":"commentary",
			"content":[{"type":"output_text","text":"prep"}]
		},
		{
			"type":"message",
			"role":"assistant",
			"id":"msg_2",
			"phase":"final_answer",
			"content":[{"type":"output_text","text":"final-1"}]
		},
		{
			"type":"message",
			"role":"assistant",
			"id":"msg_3",
			"phase":"final_answer",
			"content":[{"type":"output_text","text":"final-2"}]
		}
	]`)
	var output []responses.ResponseOutputItemUnion
	if err := json.Unmarshal(raw, &output); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}
	_, assistantText, assistantPhase, _, _, _ := parseOutputItems(output)
	if assistantText != "final-1final-2" {
		t.Fatalf("assistantText = %q, want final-1final-2", assistantText)
	}
	if assistantPhase != MessagePhaseFinal {
		t.Fatalf("assistantPhase = %q, want %q", assistantPhase, MessagePhaseFinal)
	}
}

func TestCompactRequestTargetsResponsesCompactPath(t *testing.T) {
	server := newCompactResponseServer(t, "application/json")

	transport := NewHTTPTransport(staticAuth{})
	transport.BaseURL = server.URL + "/v1"
	transport.Client = server.Client()

	resp, err := transport.Compact(context.Background(), OpenAICompactionRequest{
		Model: "gpt-5",
		InputItems: []ResponseItem{
			{Type: ResponseItemTypeMessage, Role: RoleUser, Content: "u1"},
		},
	})
	if err != nil {
		t.Fatalf("compact request failed: %v", err)
	}
	if len(resp.OutputItems) != 2 {
		t.Fatalf("expected compact output items, got %d", len(resp.OutputItems))
	}
	if resp.OutputItems[1].Type != ResponseItemTypeCompaction {
		t.Fatalf("expected compaction output item, got %+v", resp.OutputItems[1])
	}
	if resp.TrimmedItemsCount != nil {
		t.Fatalf("trimmed item count = %#v, want unavailable nil", resp.TrimmedItemsCount)
	}
}

func TestCompactRequestAcceptsJSONBodyWithNonJSONContentType(t *testing.T) {
	server := newCompactResponseServer(t, "text/plain")

	transport := NewHTTPTransport(staticAuth{})
	transport.BaseURL = server.URL + "/v1"
	transport.Client = server.Client()

	resp, err := transport.Compact(context.Background(), OpenAICompactionRequest{
		Model: "gpt-5",
		InputItems: []ResponseItem{
			{Type: ResponseItemTypeMessage, Role: RoleUser, Content: "u1"},
		},
	})
	if err != nil {
		t.Fatalf("compact request failed: %v", err)
	}
	if len(resp.OutputItems) != 2 {
		t.Fatalf("expected compact output items, got %d", len(resp.OutputItems))
	}
	if resp.OutputItems[1].Type != ResponseItemTypeCompaction {
		t.Fatalf("expected compaction output item, got %+v", resp.OutputItems[1])
	}
}

func newCompactResponseServer(t *testing.T, contentType string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses/compact" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", contentType)
		_, _ = w.Write([]byte(compactResponseFixtureJSON))
	}))
	t.Cleanup(server.Close)
	return server
}

func TestInputTokenCountPayloadMatchesCompactPayloadInputShape(t *testing.T) {
	transport := NewHTTPTransport(staticAuth{})
	canonicalItems := PrepareOpenAIInputItems([]ResponseItem{
		{Type: ResponseItemTypeMessage, Role: RoleUser, Content: "hello"},
		{Type: ResponseItemTypeFunctionCall, ID: "call_1", CallID: "call_1", Name: "shell", Arguments: json.RawMessage(`{"command":"pwd"}`)},
		{
			Type:   ResponseItemTypeFunctionCallOutput,
			CallID: "call_1",
			Name:   string(toolspec.ToolViewImage),
			Output: json.RawMessage(`[{"type":"input_file","file_data":"data:application/pdf;base64,Zm9v","filename":"doc.pdf"}]`),
		},
		{Type: ResponseItemTypeReasoning, ID: "rs_1", EncryptedContent: "enc_reasoning"},
		{Type: ResponseItemTypeCompaction, ID: "cmp_1", EncryptedContent: "enc_compaction"},
	})

	compactPayload, err := newOpenAIRequestPayloadBuilder(transport.Store, transport.ModelVerbosity, ProviderCapabilities{}).BuildCompact(OpenAICompactionRequest{
		Model:        "gpt-5",
		Instructions: "compaction instructions",
		InputItems:   canonicalItems,
	})
	if err != nil {
		t.Fatalf("build compact payload: %v", err)
	}
	countPayload, err := transport.buildInputTokenCountParams(OpenAIRequest{ToolChoiceMode: ToolChoiceModeAutomatic,
		Model:        "gpt-5",
		SystemPrompt: "compaction instructions",
		Items:        canonicalItems,
	}, requireProviderCapabilities(t, transport, OpenAIAuthMode{}))
	if err != nil {
		t.Fatalf("build input-token-count payload: %v", err)
	}

	compactJSON := mustMarshalJSONMap(t, compactPayload)
	countJSON := mustMarshalJSONMap(t, countPayload)
	if !reflect.DeepEqual(compactJSON["input"], countJSON["input"]) {
		t.Fatalf("expected input shape parity between compact and input-token-count payloads\ncompact=%#v\ncount=%#v", compactJSON["input"], countJSON["input"])
	}
	if compactJSON["instructions"] != countJSON["instructions"] {
		t.Fatalf("expected instructions parity between compact and input-token-count payloads, compact=%#v count=%#v", compactJSON["instructions"], countJSON["instructions"])
	}
}

func TestBuildInputTokenCountPreservesRequiredToolChoiceAndEffectiveTools(t *testing.T) {
	transport := NewHTTPTransport(staticAuth{})
	request := OpenAIRequest{
		Model:                 "gpt-5",
		ToolChoiceMode:        ToolChoiceModeRequired,
		EnableNativeWebSearch: true,
		Tools: []Tool{
			{Name: "shell"},
			{Name: "patch"},
		},
	}
	caps := requireProviderCapabilities(t, transport, OpenAIAuthMode{})
	generation, err := transport.buildPayload(request, OpenAIAuthMode{}, caps)
	if err != nil {
		t.Fatalf("build generation payload: %v", err)
	}
	count, err := transport.buildInputTokenCountParams(request, caps)
	if err != nil {
		t.Fatalf("build input-token-count payload: %v", err)
	}
	generationJSON := mustMarshalJSONMap(t, generation)
	countJSON := mustMarshalJSONMap(t, count)
	if countJSON["tool_choice"] != "required" {
		t.Fatalf("count tool_choice = %#v, want required", countJSON["tool_choice"])
	}
	if !reflect.DeepEqual(generationJSON["tools"], countJSON["tools"]) {
		t.Fatalf("effective tools differ\ngeneration=%#v\ncount=%#v", generationJSON["tools"], countJSON["tools"])
	}
	if countJSON["parallel_tool_calls"] != true {
		t.Fatalf("count parallel_tool_calls = %#v, want true", countJSON["parallel_tool_calls"])
	}
}

func TestOpenAIRequestBuildersRejectUnmaterializedViewImageInputFileOutput(t *testing.T) {
	transport := NewHTTPTransport(staticAuth{})
	unpreparedItems := []ResponseItem{unmaterializedViewImageInputFileOutput()}
	caps := requireProviderCapabilities(t, transport, OpenAIAuthMode{})
	checkErr := func(name string, err error) {
		t.Helper()
		if !errors.Is(err, ErrViewImageOutputNotMaterialized) {
			t.Fatalf("%s error = %v, want materialization failure", name, err)
		}
	}

	_, err := transport.buildPayload(OpenAIRequest{ToolChoiceMode: ToolChoiceModeAutomatic, Model: "gpt-5", Items: unpreparedItems}, OpenAIAuthMode{}, caps)
	checkErr("buildPayload", err)

	_, err = transport.buildInputTokenCountParams(OpenAIRequest{ToolChoiceMode: ToolChoiceModeAutomatic, Model: "gpt-5", Items: unpreparedItems}, caps)
	checkErr("buildInputTokenCountParams", err)

	_, err = newOpenAIRequestPayloadBuilder(transport.Store, transport.ModelVerbosity, ProviderCapabilities{}).BuildCompact(OpenAICompactionRequest{Model: "gpt-5", InputItems: unpreparedItems})
	checkErr("buildCompactPayload", err)
}

func unmaterializedViewImageInputFileOutput() ResponseItem {
	return ResponseItem{
		Type:   ResponseItemTypeFunctionCallOutput,
		CallID: "call_1",
		Name:   string(toolspec.ToolViewImage),
		Output: json.RawMessage(`[{"type":"input_file","file_data":"data:application/pdf;base64,Zm9v","filename":"doc.pdf"}]`),
	}
}

func TestBuildInputTokenCountParams_AppliesConfiguredModelVerbosity(t *testing.T) {
	transport := NewHTTPTransport(staticAuth{})
	transport.ModelVerbosity = "medium"
	payload, err := transport.buildInputTokenCountParams(OpenAIRequest{ToolChoiceMode: ToolChoiceModeAutomatic, Model: "gpt-5"}, requireProviderCapabilities(t, transport, OpenAIAuthMode{}))
	if err != nil {
		t.Fatalf("build input-token-count payload: %v", err)
	}

	jsonPayload := mustMarshalJSONMap(t, payload)
	text, ok := jsonPayload["text"].(map[string]any)
	if !ok {
		t.Fatalf("expected text config in payload, got %#v", jsonPayload["text"])
	}
	if got := text["verbosity"]; got != "medium" {
		t.Fatalf("expected text.verbosity=medium, got %#v", got)
	}
}

func TestCountRequestInputTokensTargetsResponsesInputTokensPath(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses/input_tokens" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"response.input_tokens","input_tokens":12345}`))
	}))
	defer server.Close()

	transport := NewHTTPTransport(staticAuth{})
	transport.BaseURL = server.URL + "/v1"
	transport.Client = server.Client()

	count, err := transport.CountRequestInputTokens(context.Background(), OpenAIRequest{ToolChoiceMode: ToolChoiceModeAutomatic,
		Model:        "gpt-5",
		SystemPrompt: "sys",
		Items: []ResponseItem{
			{Type: ResponseItemTypeMessage, Role: RoleUser, Content: "hello"},
		},
	})
	if err != nil {
		t.Fatalf("count request input tokens failed: %v", err)
	}
	if count != 12345 {
		t.Fatalf("expected input token count 12345, got %d", count)
	}
}

func TestResolveModelContextWindowUsesModelMetadataFromAPI(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models/gpt-5" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"gpt-5",
			"object":"model",
			"created":1731459200,
			"owned_by":"openai",
			"context_window":272000
		}`))
	}))
	defer server.Close()

	transport := NewHTTPTransport(staticAuth{})
	transport.BaseURL = server.URL + "/v1"
	transport.Client = server.Client()
	transport.ContextWindowTokens = 0

	window, err := transport.ResolveModelContextWindow(context.Background(), "gpt-5")
	if err != nil {
		t.Fatalf("resolve model context window failed: %v", err)
	}
	if window != 272000 {
		t.Fatalf("expected context window 272000 from model metadata, got %d", window)
	}
}

func TestResolveModelContextWindowFallsBackToInputTokenLimitField(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models/gpt-5" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"gpt-5",
			"object":"model",
			"created":1731459200,
			"owned_by":"openai",
			"limits":{"input_token_limit":190000}
		}`))
	}))
	defer server.Close()

	transport := NewHTTPTransport(staticAuth{})
	transport.BaseURL = server.URL + "/v1"
	transport.Client = server.Client()
	transport.ContextWindowTokens = 0

	window, err := transport.ResolveModelContextWindow(context.Background(), "gpt-5")
	if err != nil {
		t.Fatalf("resolve model context window failed: %v", err)
	}
	if window != 190000 {
		t.Fatalf("expected context window 190000 from nested input_token_limit field, got %d", window)
	}
}

func mustMarshalObject(t *testing.T, payload responses.ResponseNewParams) map[string]any {
	t.Helper()
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	return out
}

func mustMarshalItems(t *testing.T, items []responses.ResponseInputItemUnionParam) []map[string]any {
	t.Helper()
	b, err := json.Marshal(items)
	if err != nil {
		t.Fatalf("marshal input items: %v", err)
	}
	var out []map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal input items: %v", err)
	}
	return out
}

func mustMarshalJSONMap(t *testing.T, payload any) map[string]any {
	t.Helper()
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	return out
}

func contentTypeAt(t *testing.T, item map[string]any) string {
	t.Helper()
	parts, ok := item["content"].([]any)
	if !ok || len(parts) == 0 {
		t.Fatalf("expected content array, got %#v", item["content"])
	}
	part, ok := parts[0].(map[string]any)
	if !ok {
		t.Fatalf("expected first content object, got %#v", parts[0])
	}
	typ, ok := part["type"].(string)
	if !ok {
		t.Fatalf("expected content type string, got %#v", part["type"])
	}
	return typ
}
