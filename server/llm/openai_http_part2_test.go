package llm

import (
	"context"
	"core/shared/textutil"
	"core/shared/toolspec"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/openai/openai-go/v3/responses"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestBuildPayload_AppliesStructuredOutputJSONSchema(t *testing.T) {
	transport := NewHTTPTransport(staticAuth{})
	payload, err := transport.buildPayload(OpenAIRequest{ToolChoiceMode: ToolChoiceModeAutomatic,
		Model: "gpt-5",
		StructuredOutput: &StructuredOutput{
			Name:   "reviewer_suggestions",
			Schema: mustTestStructuredSchema(t, testReviewerStructuredOutput{}),
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

func TestBuildPayload_ForwardsPreparedStructuredOutputSchemaUnchanged(t *testing.T) {
	transport := NewHTTPTransport(staticAuth{})
	prepared := mustTestStructuredSchema(t, testWorkflowStructuredOutput{})
	payload, err := transport.buildPayload(OpenAIRequest{ToolChoiceMode: ToolChoiceModeAutomatic,
		Model: "gpt-5",
		StructuredOutput: &StructuredOutput{
			Name:   "workflow_completion",
			Schema: prepared,
		},
	}, OpenAIAuthMode{}, requireProviderCapabilities(t, transport, OpenAIAuthMode{}))
	if err != nil {
		t.Fatalf("build payload: %v", err)
	}

	got := structuredOutputPayloadSchema(t, mustMarshalObject(t, payload))
	want := mustDecodeSchemaObject(t, prepared.JSON())
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("structured output schema changed in transport\ngot=%#v\nwant=%#v", got, want)
	}
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

func mustDecodeSchemaObject(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("decode prepared schema: %v", err)
	}
	return schema
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

func TestBuildPayload_MergesConfiguredModelVerbosityWithStructuredOutput(t *testing.T) {
	transport := NewHTTPTransport(staticAuth{})
	transport.ModelVerbosity = "low"
	payload, err := transport.buildPayload(OpenAIRequest{ToolChoiceMode: ToolChoiceModeAutomatic,
		Model: "gpt-5",
		StructuredOutput: &StructuredOutput{
			Name:   "reviewer_suggestions",
			Schema: mustTestStructuredSchema(t, testReviewerStructuredOutput{}),
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

func TestBuildResponsesInput_AssistantReasoningItemsUseEncryptedContentOnly(t *testing.T) {
	items := mustBuildResponsesInput(t, ItemsFromMessages([]Message{
		{
			Role:    RoleAssistant,
			Content: textutil.Value("a1"),
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

func TestBuildPayload_ForwardsPreparedFunctionSchemaUnchanged(t *testing.T) {
	transport := NewHTTPTransport(staticAuth{})
	prepared := mustTestFunctionSchema(t, testNestedFunctionInput{})
	payload, err := transport.buildPayload(OpenAIRequest{ToolChoiceMode: ToolChoiceModeAutomatic,
		Model: "gpt-5",
		Tools: []Tool{
			{
				Name:   "ask_question",
				Schema: prepared,
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
	got, ok := tool["parameters"].(map[string]any)
	if !ok {
		t.Fatalf("expected parameters object, got %#v", tool["parameters"])
	}
	want := mustDecodeSchemaObject(t, prepared.JSON())
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("function schema changed in transport\ngot=%#v\nwant=%#v", got, want)
	}
}

func TestBuildResponsesInput_CanonicalCompactionItemRoundTrip(t *testing.T) {
	items := mustBuildResponsesInput(t, PrepareOpenAIInputItems([]ResponseItem{
		{Type: ResponseItemTypeMessage, Role: textutil.Value(RoleUser), Content: textutil.Value("u1")},
		{Type: ResponseItemTypeCompaction, ID: textutil.Value("cmp_1"), EncryptedContent: textutil.Value("enc_1")},
	}))
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
	items, assistantText, assistantPhase, _, toolCalls, reasoning, reasoningItems, err := parseOutputItems(output)
	if err != nil {
		t.Fatalf("parse output: %v", err)
	}
	if assistantText != nil {
		t.Fatalf("expected no assistant text, got %#v", assistantText)
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
	if items[1].Type != ResponseItemTypeCompaction || items[1].EncryptedContent == nil || *items[1].EncryptedContent != "enc_1" {
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
	_, assistantText, assistantPhase, _, _, _, _, err := parseOutputItems(output)
	if err != nil {
		t.Fatalf("parse output: %v", err)
	}
	if optionalStringValue(assistantText) != "done" {
		t.Fatalf("assistantText = %#v, want done", assistantText)
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
	_, assistantText, assistantPhase, _, _, _, _, err := parseOutputItems(output)
	if err != nil {
		t.Fatalf("parse output: %v", err)
	}
	if optionalStringValue(assistantText) != "final-1final-2" {
		t.Fatalf("assistantText = %#v, want final-1final-2", assistantText)
	}
	if assistantPhase != MessagePhaseFinal {
		t.Fatalf("assistantPhase = %q, want %q", assistantPhase, MessagePhaseFinal)
	}
}

func TestAPIKeyCompactRequestTargetsStreamingResponsesV2(t *testing.T) {
	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			http.Error(w, "unexpected path: "+r.URL.Path, http.StatusNotFound)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"type":"response.completed","response":{"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15},"output":[{"type":"compaction","id":"cmp_1","encrypted_content":"enc_1"}]}}` + "\n\n"))
	}))
	t.Cleanup(server.Close)

	transport := NewHTTPTransport(staticAuth{})
	transport.Client = newRewritingHTTPClient(t, server)

	resp, err := transport.Compact(context.Background(), OpenAICompactionRequest{
		Model: "gpt-5",
		InputItems: PrepareOpenAIInputItems([]ResponseItem{
			{Type: ResponseItemTypeMessage, Role: textutil.Value(RoleUser), Content: textutil.Value("u1")},
		}),
	})
	if err != nil {
		t.Fatalf("compact request failed: %v", err)
	}
	if len(resp.OutputItems) != 1 || resp.OutputItems[0].Type != ResponseItemTypeCompaction {
		t.Fatalf("expected one compact output item, got %+v", resp.OutputItems)
	}
	if captured["stream"] != true {
		t.Fatalf("stream = %#v, want true", captured["stream"])
	}
	input, ok := captured["input"].([]any)
	if !ok || len(input) != 2 {
		t.Fatalf("input = %#v, want history plus trigger", captured["input"])
	}
	trigger, _ := input[len(input)-1].(map[string]any)
	if len(trigger) != 1 || trigger["type"] != "compaction_trigger" {
		t.Fatalf("final input = %#v, want exact compaction trigger", trigger)
	}
}

func TestOAuthCompactRequestTargetsStreamingResponsesWithFinalTrigger(t *testing.T) {
	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/backend-api/codex/responses" {
			http.Error(w, "unexpected path: "+r.URL.Path, http.StatusNotFound)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.completed\n" +
			`data: {"type":"response.completed","response":{"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15},"output":[{"type":"compaction","id":"cmp_1","encrypted_content":"enc_1"}]}}` + "\n\n"))
	}))
	t.Cleanup(server.Close)

	transport := NewHTTPTransport(oauthStaticAuth{})
	transport.BaseURL = "https://chatgpt.com/backend-api/codex"
	transport.BaseURLExplicit = true
	transport.Client = newRewritingHTTPClient(t, server)

	resp, err := transport.Compact(context.Background(), OpenAICompactionRequest{
		Model:          "gpt-5.6-sol",
		Instructions:   "preserved instructions",
		PromptCacheKey: "session-cache-lineage",
		InputItems: PrepareOpenAIInputItems([]ResponseItem{
			{Type: ResponseItemTypeMessage, Role: textutil.Value(RoleUser), Content: textutil.Value("history first")},
		}),
	})
	if err != nil {
		t.Fatalf("oauth compact request failed: %v", err)
	}
	if len(resp.OutputItems) != 1 {
		t.Fatalf("output items = %d, want one compaction checkpoint", len(resp.OutputItems))
	}
	checkpoint := resp.OutputItems[0]
	if checkpoint.Type != ResponseItemTypeCompaction || optionalStringValue(checkpoint.ID) != "cmp_1" || optionalStringValue(checkpoint.EncryptedContent) != "enc_1" || !json.Valid(checkpoint.Raw) {
		t.Fatalf("checkpoint = %+v, want canonical encrypted compaction with raw JSON", checkpoint)
	}
	if resp.Usage.InputTokens != 10 || resp.Usage.OutputTokens != 5 {
		t.Fatalf("usage = %+v, want completed response usage", resp.Usage)
	}
	if got := captured["stream"]; got != true {
		t.Fatalf("stream = %#v, want true", got)
	}
	if got := captured["instructions"]; got != "preserved instructions" {
		t.Fatalf("instructions = %#v, want preserved instructions", got)
	}
	if got := captured["prompt_cache_key"]; got != "session-cache-lineage" {
		t.Fatalf("prompt_cache_key = %#v, want existing session lineage", got)
	}
	input, ok := captured["input"].([]any)
	if !ok || len(input) != 2 {
		t.Fatalf("input = %#v, want history plus final compaction trigger", captured["input"])
	}
	first, _ := input[0].(map[string]any)
	if first["type"] != "message" {
		t.Fatalf("first input = %#v, want preserved history item", first)
	}
	trigger, _ := input[1].(map[string]any)
	if len(trigger) != 1 || trigger["type"] != "compaction_trigger" {
		t.Fatalf("final input = %#v, want exact compaction trigger", trigger)
	}
}

func TestOAuthCompactRequestRejectsCompletedStreamWithoutCompactionOutput(t *testing.T) {
	server := newOAuthCompactStreamServer(t, []string{
		`{"type":"response.completed","response":{"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15},"output":[]}}`,
	})
	transport := newOAuthCompactTestTransport(t, server)

	_, err := transport.Compact(context.Background(), OpenAICompactionRequest{Model: "gpt-5.6-sol"})
	if err == nil || !strings.Contains(err.Error(), "compaction_count=0 output_count=0 types=map[]") {
		t.Fatalf("error = %v, want diagnostic zero-compaction contract failure", err)
	}
}

func TestOAuthCompactRequestUsesStreamedCompactionWhenCompletedOutputIsEmpty(t *testing.T) {
	server := newOAuthCompactStreamServer(t, []string{
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"compaction","id":"cmp_streamed","encrypted_content":"enc_streamed"}}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"type":"compaction","id":"cmp_streamed","encrypted_content":"enc_streamed"}}`,
		`{"type":"response.completed","response":{"usage":{"input_tokens":9,"output_tokens":4,"total_tokens":13},"output":[]}}`,
	})
	resp, err := newOAuthCompactTestTransport(t, server).Compact(context.Background(), OpenAICompactionRequest{Model: "gpt-5.6-sol"})
	if err != nil {
		t.Fatalf("compact request failed: %v", err)
	}
	if len(resp.OutputItems) != 1 || optionalStringValue(resp.OutputItems[0].ID) != "cmp_streamed" || optionalStringValue(resp.OutputItems[0].EncryptedContent) != "enc_streamed" {
		t.Fatalf("output items = %+v, want streamed compaction checkpoint", resp.OutputItems)
	}
	if resp.Usage.InputTokens != 9 || resp.Usage.OutputTokens != 4 {
		t.Fatalf("usage = %+v, want terminal completed usage", resp.Usage)
	}
}

func TestOAuthCompactRequestRejectsMultipleCompactionOutputs(t *testing.T) {
	server := newOAuthCompactStreamServer(t, []string{
		`{"type":"response.completed","response":{"output":[{"type":"compaction","id":"cmp_1","encrypted_content":"enc_1"},{"type":"compaction","id":"cmp_2","encrypted_content":"enc_2"}]}}`,
	})
	_, err := newOAuthCompactTestTransport(t, server).Compact(context.Background(), OpenAICompactionRequest{Model: "gpt-5.6-sol"})
	if err == nil || !strings.Contains(err.Error(), "compaction_count=2 output_count=2") || !strings.Contains(err.Error(), "compaction:2") {
		t.Fatalf("error = %v, want diagnostic multiple-compaction contract failure", err)
	}
}

func TestOAuthCompactRequestRejectsCompactionWithoutEncryptedContent(t *testing.T) {
	server := newOAuthCompactStreamServer(t, []string{
		`{"type":"response.completed","response":{"output":[{"type":"compaction","id":"cmp_1"}]}}`,
	})
	_, err := newOAuthCompactTestTransport(t, server).Compact(context.Background(), OpenAICompactionRequest{Model: "gpt-5.6-sol"})
	if err == nil || !strings.Contains(err.Error(), "missing encrypted_content") || !strings.Contains(err.Error(), "compaction_count=1 output_count=1") {
		t.Fatalf("error = %v, want missing-encrypted-content contract failure", err)
	}
}

func TestOAuthCompactRequestRejectsStreamWithoutResponseCompleted(t *testing.T) {
	server := newOAuthCompactStreamServer(t, []string{
		`{"type":"response.output_item.done","output_index":0,"item":{"type":"compaction","id":"cmp_1","encrypted_content":"enc_1"}}`,
	})
	_, err := newOAuthCompactTestTransport(t, server).Compact(context.Background(), OpenAICompactionRequest{Model: "gpt-5.6-sol"})
	if err == nil || !strings.Contains(err.Error(), openAIResponsesStreamEndedBeforeTerminalMessage) {
		t.Fatalf("error = %v, want missing response.completed failure", err)
	}
}

func TestOAuthCompactRequestPreservesProviderHTTPErrorDiagnostics(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"compaction trigger rejected","type":"invalid_request_error","code":"invalid_trigger"}}`))
	}))
	t.Cleanup(server.Close)

	_, err := newOAuthCompactTestTransport(t, server).Compact(context.Background(), OpenAICompactionRequest{Model: "gpt-5.6-sol"})
	var providerErr *ProviderAPIError
	if !errors.As(err, &providerErr) {
		t.Fatalf("error = %v, want ProviderAPIError", err)
	}
	if providerErr.ProviderID != "chatgpt-codex" || providerErr.StatusCode != http.StatusBadRequest || !strings.Contains(err.Error(), "compaction trigger rejected") {
		t.Fatalf("provider error = %+v, want provider, status, and useful body diagnostics", providerErr)
	}
}

func TestOAuthCompactRequestHonorsCancellationWithoutTransportRetry(t *testing.T) {
	requests := make(chan struct{}, 2)
	releaseHandler := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- struct{}{}
		select {
		case <-r.Context().Done():
		case <-releaseHandler:
		}
	}))
	t.Cleanup(server.Close)
	t.Cleanup(func() { close(releaseHandler) })
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := newOAuthCompactTestTransport(t, server).Compact(ctx, OpenAICompactionRequest{Model: "gpt-5.6-sol"})
		done <- err
	}()
	select {
	case <-requests:
		cancel()
	case <-time.After(time.Second):
		t.Fatal("compaction request did not start")
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want context canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled compaction request did not return")
	}
	select {
	case <-requests:
		t.Fatal("transport retried canceled compaction request")
	default:
	}
}

func TestOAuthCompactRequestFailsWhenStreamStalls(t *testing.T) {
	releaseHandler := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"type":"response.created","response":{"id":"resp_stalled"}}` + "\n\n"))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		select {
		case <-r.Context().Done():
		case <-releaseHandler:
		}
	}))
	t.Cleanup(server.Close)
	t.Cleanup(func() { close(releaseHandler) })
	transport := newOAuthCompactTestTransport(t, server)
	transport.Client.Timeout = 50 * time.Millisecond

	_, err := transport.Compact(context.Background(), OpenAICompactionRequest{Model: "gpt-5.6-sol"})
	if !errors.Is(err, ErrModelStreamStalled) {
		t.Fatalf("error = %v, want bounded stream stall failure", err)
	}
}

func newOAuthCompactStreamServer(t *testing.T, events []string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/backend-api/codex/responses" {
			http.Error(w, "unexpected path: "+r.URL.Path, http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, event := range events {
			_, _ = fmt.Fprintf(w, "data: %s\n\n", event)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func newOAuthCompactTestTransport(t *testing.T, server *httptest.Server) *HTTPTransport {
	transport := NewHTTPTransport(oauthStaticAuth{})
	transport.BaseURL = "https://chatgpt.com/backend-api/codex"
	transport.BaseURLExplicit = true
	transport.Client = newRewritingHTTPClient(t, server)
	return transport
}

func newRewritingHTTPClient(t *testing.T, server *httptest.Server) *http.Client {
	t.Helper()
	target, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("parse test server URL: %v", err)
	}
	return &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		cloned := req.Clone(req.Context())
		cloned.URL.Scheme = target.Scheme
		cloned.URL.Host = target.Host
		cloned.Host = target.Host
		return server.Client().Transport.RoundTrip(cloned)
	})}
}

func TestInputTokenCountPayloadMatchesCompactPayloadInputShape(t *testing.T) {
	transport := NewHTTPTransport(staticAuth{})
	canonicalItems := PrepareOpenAIInputItems([]ResponseItem{
		{Type: ResponseItemTypeMessage, Role: textutil.Value(RoleUser), Content: textutil.Value("hello")},
		{Type: ResponseItemTypeFunctionCall, ID: textutil.Value("call_1"), CallID: textutil.Value("call_1"), Name: textutil.Value("shell"), Arguments: json.RawMessage(`{"command":"pwd"}`)},
		{
			Type:   ResponseItemTypeFunctionCallOutput,
			CallID: textutil.Value("call_1"),
			Name:   textutil.Value(string(toolspec.ToolViewImage)),
			Output: json.RawMessage(`[{"type":"input_file","file_data":"data:application/pdf;base64,Zm9v","filename":"doc.pdf"}]`),
		},
		{Type: ResponseItemTypeReasoning, ID: textutil.Value("rs_1"), EncryptedContent: textutil.Value("enc_reasoning")},
		{Type: ResponseItemTypeCompaction, ID: textutil.Value("cmp_1"), EncryptedContent: textutil.Value("enc_compaction")},
	})

	compactPayload, err := newOpenAIRequestPayloadBuilder(transport.Store, transport.ModelVerbosity, requireProviderCapabilities(t, transport, OpenAIAuthMode{})).BuildCompactV2(OpenAICompactionRequest{
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
	compactInput := compactJSON["input"].([]any)
	compactInput = compactInput[:len(compactInput)-1]
	if !reflect.DeepEqual(compactInput, countJSON["input"]) {
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
			{Name: "shell", Schema: mustTestFunctionSchema(t, struct{}{})},
			{Name: "patch", Schema: mustTestFunctionSchema(t, struct{}{})},
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

func TestBuildInputTokenCountForwardsPreparedStructuredOutputLikeGeneration(t *testing.T) {
	transport := NewHTTPTransport(staticAuth{})
	request := OpenAIRequest{
		Model:          "gpt-5",
		ToolChoiceMode: ToolChoiceModeAutomatic,
		StructuredOutput: &StructuredOutput{
			Name:        "workflow_completion",
			Description: "Complete the current workflow node.",
			Schema:      mustTestStructuredSchema(t, testWorkflowStructuredOutput{}),
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
	if !reflect.DeepEqual(generationJSON["text"], countJSON["text"]) {
		t.Fatalf(
			"structured output differs between generation and token count\ngeneration=%#v\ncount=%#v",
			generationJSON["text"],
			countJSON["text"],
		)
	}
}

func TestOpenAIRequestBuildersRejectUnpreparedViewImageInputFileOutput(t *testing.T) {
	transport := NewHTTPTransport(staticAuth{})
	unpreparedItems := []ResponseItem{unmaterializedViewImageInputFileOutput()}
	caps := requireProviderCapabilities(t, transport, OpenAIAuthMode{})
	checkErr := func(name string, err error) {
		t.Helper()
		if !errors.Is(err, ErrOpenAIInputItemUnprepared) {
			t.Fatalf("%s error = %v, want materialization failure", name, err)
		}
		var preparationErr *OpenAIInputItemPreparationError
		if !errors.As(err, &preparationErr) {
			t.Fatalf("%s error type = %T, want typed preparation error", name, err)
		}
		if preparationErr.Type != ResponseItemTypeFunctionCallOutput ||
			preparationErr.Name == nil || *preparationErr.Name != string(toolspec.ToolViewImage) ||
			preparationErr.CallID == nil || *preparationErr.CallID != "call_1" ||
			preparationErr.State != OpenAIInputPreparationMissingRaw {
			t.Fatalf("%s preparation error = %+v", name, preparationErr)
		}
	}

	_, err := transport.buildPayload(OpenAIRequest{ToolChoiceMode: ToolChoiceModeAutomatic, Model: "gpt-5", Items: unpreparedItems}, OpenAIAuthMode{}, caps)
	checkErr("buildPayload", err)

	_, err = transport.buildInputTokenCountParams(OpenAIRequest{ToolChoiceMode: ToolChoiceModeAutomatic, Model: "gpt-5", Items: unpreparedItems}, caps)
	checkErr("buildInputTokenCountParams", err)

	_, err = newOpenAIRequestPayloadBuilder(transport.Store, transport.ModelVerbosity, caps).BuildCompactV2(OpenAICompactionRequest{Model: "gpt-5", InputItems: unpreparedItems})
	checkErr("buildCompactPayload", err)
}

func unmaterializedViewImageInputFileOutput() ResponseItem {
	return ResponseItem{
		Type:   ResponseItemTypeFunctionCallOutput,
		CallID: textutil.Value("call_1"),
		Name:   textutil.Value(string(toolspec.ToolViewImage)),
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
		Items: PrepareOpenAIInputItems([]ResponseItem{
			{Type: ResponseItemTypeMessage, Role: textutil.Value(RoleUser), Content: textutil.Value("hello")},
		}),
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
