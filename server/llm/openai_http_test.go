package llm

import (
	"context"
	"core/server/auth"
	"core/shared/config"
	"core/shared/toolspec"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	openai "github.com/openai/openai-go/v3"

	"github.com/openai/openai-go/v3/responses"
)

type staticAuth struct{}

const compactResponseFixtureJSON = `{
	"id":"resp_cmp_1",
	"object":"response.compaction",
	"created_at":1731459200,
	"output":[
		{"type":"message","role":"user","content":[{"type":"input_text","text":"u1"}]},
		{"type":"compaction","id":"cmp_1","encrypted_content":"enc_1"}
	],
	"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}
}`

func (staticAuth) AuthorizationHeader(context.Context) (string, error) {
	return "Bearer token", nil
}

type oauthStaticAuth struct{}

func (oauthStaticAuth) AuthorizationHeader(context.Context) (string, error) {
	return "Bearer token", nil
}

func (oauthStaticAuth) OpenAIAuthMetadata(context.Context) (string, string, error) {
	return "oauth", "acc-1", nil
}

type missingAuth struct{}

func (missingAuth) AuthorizationHeader(context.Context) (string, error) {
	return "", auth.ErrAuthNotConfigured
}

func requireProviderCapabilities(t *testing.T, transport *HTTPTransport, mode OpenAIAuthMode) ProviderCapabilities {
	t.Helper()
	caps, err := transport.providerCapabilitiesForMode(mode)
	if err != nil {
		t.Fatalf("resolve provider capabilities: %v", err)
	}
	return caps
}

func mustBuildResponsesInput(t *testing.T, items []ResponseItem) []responses.ResponseInputItemUnionParam {
	t.Helper()
	input, err := buildResponsesInput(items)
	if err != nil {
		t.Fatalf("build responses input: %v", err)
	}
	return input
}

func TestBuildResponsesInputRawTakesPrecedenceOverTypedFields(t *testing.T) {
	items := []ResponseItem{{
		Type:   ResponseItemTypeFunctionCallOutput,
		CallID: "call_1",
		Name:   string(toolspec.ToolExecCommand),
		Output: json.RawMessage(`"typed-collapsed-output"`),
		Raw:    json.RawMessage(`{"type":"function_call_output","call_id":"call_1","output":"stale-raw-output"}`),
	}}

	jsonItems := mustMarshalItems(t, mustBuildResponsesInput(t, items))
	if got := jsonItems[0]["output"]; got != "stale-raw-output" {
		t.Fatalf("expected raw provider payload to take precedence, got %#v", got)
	}
}

func TestBuildPayload_SerializesAssistantToolCalls(t *testing.T) {
	transport := NewHTTPTransport(staticAuth{})
	payload, err := transport.buildPayload(OpenAIRequest{ToolChoiceMode: ToolChoiceModeAutomatic,
		Model:        "gpt-5",
		SystemPrompt: "sys",
		Items: ItemsFromMessages([]Message{
			{
				Role:    RoleAssistant,
				Content: "",
				ToolCalls: []ToolCall{
					{ID: "call-1", Name: "shell", Input: json.RawMessage(`{"command":"pwd"}`)},
				},
			},
			{Role: RoleTool, ToolCallID: "call-1", Name: "shell", Content: "{}"},
		}),
	}, OpenAIAuthMode{}, requireProviderCapabilities(t, transport, OpenAIAuthMode{}))
	if err != nil {
		t.Fatalf("build payload: %v", err)
	}

	if !payload.Instructions.Valid() || payload.Instructions.Value != "sys" {
		t.Fatalf("expected instructions to carry system prompt, got %+v", payload.Instructions)
	}

	jsonPayload := mustMarshalObject(t, payload)
	inputRaw, ok := jsonPayload["input"].([]any)
	if !ok {
		t.Fatalf("expected input array, got %#v", jsonPayload["input"])
	}
	if len(inputRaw) != 2 {
		t.Fatalf("expected 2 input items, got %d", len(inputRaw))
	}

	call, ok := inputRaw[0].(map[string]any)
	if !ok {
		t.Fatalf("expected function_call object, got %#v", inputRaw[0])
	}
	if call["type"] != "function_call" {
		t.Fatalf("expected function_call input item, got %v", call["type"])
	}
	if call["call_id"] != "call-1" || call["name"] != "shell" {
		t.Fatalf("unexpected function call item: %+v", call)
	}
	if call["arguments"] != "{\"command\":\"pwd\"}" {
		t.Fatalf("unexpected function call arguments: %v", call["arguments"])
	}

	result, ok := inputRaw[1].(map[string]any)
	if !ok {
		t.Fatalf("expected function_call_output object, got %#v", inputRaw[1])
	}
	if result["type"] != "function_call_output" || result["call_id"] != "call-1" {
		t.Fatalf("unexpected function call output item: %+v", result)
	}
	if result["output"] != "{}" {
		t.Fatalf("unexpected function call output payload: %v", result["output"])
	}
}

func TestItemsFromMessagesNormalizesEscapedHTMLToolArguments(t *testing.T) {
	live := ItemsFromMessages([]Message{{
		Role: RoleAssistant,
		ToolCalls: []ToolCall{{
			ID:    "call-1",
			Name:  "shell",
			Input: json.RawMessage(`{"cmd":"git diff --cached && git diff","literal":"\\u0026"}`),
		}},
	}})
	replayed := ItemsFromMessages([]Message{{
		Role: RoleAssistant,
		ToolCalls: []ToolCall{{
			ID:    "call-1",
			Name:  "shell",
			Input: json.RawMessage(`{"cmd":"git diff --cached \u0026\u0026 git diff","literal":"\\u0026"}`),
		}},
	}})
	if len(live) != 1 || len(replayed) != 1 {
		t.Fatalf("expected one item from each transcript, got live=%d replayed=%d", len(live), len(replayed))
	}
	if string(live[0].Arguments) != string(replayed[0].Arguments) {
		t.Fatalf("arguments differ\nlive=%s\nreplayed=%s", live[0].Arguments, replayed[0].Arguments)
	}
	if got := string(replayed[0].Arguments); got != `{"cmd":"git diff --cached && git diff","literal":"\\u0026"}` {
		t.Fatalf("unexpected canonical arguments: %s", got)
	}
}

func TestBuildResponsesInput_AssistantUsesTypedMessageInput(t *testing.T) {
	items := mustBuildResponsesInput(t, ItemsFromMessages([]Message{
		{Role: RoleUser, Content: "u1"},
		{Role: RoleAssistant, Content: "a1"},
	}))
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}

	jsonItems := mustMarshalItems(t, items)
	if got := contentTypeAt(t, jsonItems[0]); got != "input_text" {
		t.Fatalf("user content type=%q", got)
	}
	if got := jsonItems[1]["type"]; got != "message" {
		t.Fatalf("assistant item type=%#v", got)
	}
	if got := jsonItems[1]["role"]; got != string(RoleAssistant) {
		t.Fatalf("assistant role=%#v", got)
	}
	if got := jsonItems[1]["status"]; got != "completed" {
		t.Fatalf("assistant status=%#v", got)
	}
	if got := contentTypeAt(t, jsonItems[1]); got != "output_text" {
		t.Fatalf("assistant content type=%q", got)
	}
}

func TestBuildResponsesInput_AssistantPreservesPhase(t *testing.T) {
	items := mustBuildResponsesInput(t, ItemsFromMessages([]Message{{Role: RoleAssistant, Content: "a1", Phase: MessagePhaseCommentary}}))
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}

	jsonItems := mustMarshalItems(t, items)
	if got := jsonItems[0]["type"]; got != "message" {
		t.Fatalf("assistant item type=%#v", got)
	}
	if got := jsonItems[0]["phase"]; got != string(MessagePhaseCommentary) {
		t.Fatalf("assistant phase=%#v", got)
	}
	if got := jsonItems[0]["status"]; got != "completed" {
		t.Fatalf("assistant status=%#v", got)
	}
	if got := contentTypeAt(t, jsonItems[0]); got != "output_text" {
		t.Fatalf("assistant content type=%q", got)
	}
}

func TestBuildResponsesInput_CanonicalAssistantPreservesPhase(t *testing.T) {
	items := mustBuildResponsesInput(t, []ResponseItem{{
		Type:    ResponseItemTypeMessage,
		Role:    RoleAssistant,
		Content: "done",
		Phase:   MessagePhaseFinal,
	}})
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}

	jsonItems := mustMarshalItems(t, items)
	if got := jsonItems[0]["type"]; got != "message" {
		t.Fatalf("assistant item type=%#v", got)
	}
	if got := jsonItems[0]["phase"]; got != string(MessagePhaseFinal) {
		t.Fatalf("assistant phase=%#v", got)
	}
	if got := jsonItems[0]["status"]; got != "completed" {
		t.Fatalf("assistant status=%#v", got)
	}
	if got := contentTypeAt(t, jsonItems[0]); got != "output_text" {
		t.Fatalf("assistant content type=%q", got)
	}
}

func TestBuildResponsesInput_NonAssistantRolesUseInputText(t *testing.T) {
	items := mustBuildResponsesInput(t, ItemsFromMessages([]Message{
		{Role: RoleSystem, Content: "s1"},
		{Role: RoleDeveloper, Content: "d1"},
		{Role: RoleUser, Content: "u1"},
	}))
	if len(items) != 3 {
		t.Fatalf("expected 3 items, got %d", len(items))
	}

	jsonItems := mustMarshalItems(t, items)
	for i, item := range jsonItems {
		if got := contentTypeAt(t, item); got != "input_text" {
			t.Fatalf("item %d content type=%q", i, got)
		}
	}
}

func TestBuildResponsesInput_ToolOutputSupportsStructuredInputImageItems(t *testing.T) {
	items := mustBuildResponsesInput(t, ItemsFromMessages([]Message{
		{
			Role:       RoleTool,
			ToolCallID: "call_1",
			Content:    `[{"type":"input_image","image_url":"data:image/png;base64,abc"}]`,
		},
	}))
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}

	jsonItems := mustMarshalItems(t, items)
	if got := jsonItems[0]["type"]; got != "function_call_output" {
		t.Fatalf("expected function_call_output item, got %#v", got)
	}
	output, ok := jsonItems[0]["output"].([]any)
	if !ok || len(output) != 1 {
		t.Fatalf("expected structured output array, got %#v", jsonItems[0]["output"])
	}
	part, ok := output[0].(map[string]any)
	if !ok {
		t.Fatalf("expected structured output object, got %#v", output[0])
	}
	if got := part["type"]; got != "input_image" {
		t.Fatalf("expected input_image output content, got %#v", got)
	}
	if got := part["image_url"]; got != "data:image/png;base64,abc" {
		t.Fatalf("unexpected image_url in structured output: %#v", got)
	}
}

func TestMapOpenAIRequestError_UsesOpenAISDKContractError(t *testing.T) {
	err := newOpenAIRequestErrorMapper("openai").Map(&openai.Error{StatusCode: 400, Code: "context_length_exceeded", Type: "invalid_request_error", Message: "prompt too long"},
		nil,
		"openai responses compact request failed")

	if !IsContextLengthOverflowError(err) {
		t.Fatalf("expected overflow classification, got err=%v", err)
	}

	var providerErr *ProviderAPIError
	if !errors.As(err, &providerErr) {
		t.Fatalf("expected ProviderAPIError, got %T", err)
	}
	if providerErr.ProviderID != "openai" || providerErr.ProviderCode != "context_length_exceeded" {
		t.Fatalf("unexpected provider error mapping: %+v", providerErr)
	}
}

func TestMapOpenAIStreamErrorPayload_UsesSharedStructuredDecoder(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{
			name: "nested stream error",
			body: `{"type":"error","error":{"type":"invalid_request_error","code":"context_length_exceeded","param":"input","message":"too many tokens"}}`,
		},
		{
			name: "response error event",
			body: `{"type":"error","code":"context_length_exceeded","param":"input","message":"too many tokens"}`,
		},
		{
			name: "response failed",
			body: `{"type":"response.failed","response":{"error":{"code":"context_length_exceeded","message":"too many tokens"}}}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err, ok := mapOpenAIStreamErrorPayload("openai", []byte(tc.body), nil, http.StatusOK)
			if !ok {
				t.Fatal("expected stream error payload to map")
			}
			if !IsContextLengthOverflowError(err) {
				t.Fatalf("expected context overflow classification, got %v", err)
			}
		})
	}
}

func TestMapOpenAIRequestError_UsesOpenAIErrorEnvelopeFromRawResponse(t *testing.T) {
	rawResp := &http.Response{
		StatusCode: 422,
		Body: io.NopCloser(strings.NewReader(
			`{"error":{"type":"invalid_request_error","code":"input_too_long","param":"input","message":"too many tokens"}}`,
		)),
	}
	err := newOpenAIRequestErrorMapper("openai").Map(nil, rawResp, "openai responses compact request failed")
	if !IsContextLengthOverflowError(err) {
		t.Fatalf("expected overflow classification from raw response contract, got err=%v", err)
	}

	var providerErr *ProviderAPIError
	if !errors.As(err, &providerErr) {
		t.Fatalf("expected ProviderAPIError, got %T", err)
	}
	if providerErr.ProviderParam != "input" {
		t.Fatalf("expected param=input, got %+v", providerErr)
	}
}

func TestMapOpenAIRequestError_UnknownProviderIDFailsFast(t *testing.T) {
	rawResp := &http.Response{
		StatusCode: 400,
		Body: io.NopCloser(strings.NewReader(
			`{"error":{"type":"invalid_request_error","code":"context_length_exceeded","param":"input","message":"too many tokens"}}`,
		)),
	}
	err := newOpenAIRequestErrorMapper("ollama").Map(nil, rawResp, "openai responses compact request failed")
	if err == nil {
		t.Fatal("expected missing provider reducer error")
	}
	var providerErr *ProviderAPIError
	if !errors.As(err, &providerErr) {
		t.Fatalf("expected ProviderAPIError, got %T err=%v", err, err)
	}
	if providerErr.Code != UnifiedErrorCodeProviderContract || providerErr.ProviderID != "ollama" {
		t.Fatalf("expected provider contract error for ollama, got %+v", providerErr)
	}
	if rawResp.Body != nil {
		t.Fatal("expected response body to be closed and cleared on reducer registration failure")
	}
	if !IsNonRetriableModelError(err) {
		t.Fatalf("expected provider contract error to be non-retriable, got err=%v", err)
	}
}

func TestMapOpenAIRequestError_HandlesNilResponseBody(t *testing.T) {
	rawResp := &http.Response{StatusCode: 500, Body: nil}
	err := newOpenAIRequestErrorMapper("openai").Map(nil, rawResp, "openai responses compact request failed")

	var providerErr *ProviderAPIError
	if !errors.As(err, &providerErr) {
		t.Fatalf("expected ProviderAPIError, got %T", err)
	}
	if providerErr.Raw != "<empty error body>" {
		t.Fatalf("expected empty body sentinel, got %+v", providerErr)
	}
}

func TestMapOpenAIRequestError_RepopulatesRawResponseBody(t *testing.T) {
	body := `{"error":{"type":"invalid_request_error","code":"context_length_exceeded","param":"input","message":"too many tokens"}}`
	rawResp := &http.Response{StatusCode: 400, Body: io.NopCloser(strings.NewReader(body))}
	_ = newOpenAIRequestErrorMapper("openai").Map(nil, rawResp, "openai responses compact request failed")
	if rawResp.Body == nil {
		t.Fatal("expected response body to be re-populated")
	}
	defer rawResp.Body.Close()
	buf, err := io.ReadAll(rawResp.Body)
	if err != nil {
		t.Fatalf("read re-populated body: %v", err)
	}
	if strings.TrimSpace(string(buf)) != body {
		t.Fatalf("expected original body to remain available, got %q", string(buf))
	}
}

func TestMapOpenAIRequestError_UnwrapStabilityAcrossWrappingLayers(t *testing.T) {
	err := newOpenAIRequestErrorMapper("openai").Map(&openai.Error{StatusCode: 400, Code: "context_length_exceeded", Type: "invalid_request_error", Message: "prompt too long"},
		nil,
		"openai responses compact request failed")

	err = fmt.Errorf("openai compact: %w", err)

	var providerErr *ProviderAPIError
	if !errors.As(err, &providerErr) {
		t.Fatalf("expected ProviderAPIError in unwrap chain, got %T", err)
	}
	if providerErr.Code != UnifiedErrorCodeContextLengthOverflow {
		t.Fatalf("expected overflow code in unwrap chain, got %+v", providerErr)
	}
}

func TestCompactErrorPath_ReturnsProviderAPIErrorWithDetectedProviderID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"type":"invalid_request_error","code":"context_length_exceeded","param":"input","message":"too many tokens"}}`))
	}))
	defer server.Close()

	transport := NewHTTPTransport(staticAuth{})
	transport.BaseURL = server.URL + "/v1"

	_, err := transport.Compact(context.Background(), OpenAICompactionRequest{
		Model:      "gpt-5",
		SessionID:  "s1",
		InputItems: []ResponseItem{{Type: ResponseItemTypeMessage, Role: RoleUser, Content: "hello"}},
	})
	if err == nil {
		t.Fatal("expected compact error")
	}
	var providerErr *ProviderAPIError
	if !errors.As(err, &providerErr) {
		t.Fatalf("expected ProviderAPIError from transport path, got %T err=%v", err, err)
	}
	if providerErr.ProviderID != "openai" || providerErr.Code != UnifiedErrorCodeContextLengthOverflow {
		t.Fatalf("expected openai overflow classification on loopback transport, got %+v", providerErr)
	}
	if !IsNonRetriableModelError(err) {
		t.Fatalf("expected 400 overflow response to remain non-retriable, got %v", err)
	}
}

func TestBuildResponsesInput_CanonicalToolOutputPromotesStructuredInputFileItems(t *testing.T) {
	const pdfDataURL = "data:application/pdf;base64,Zm9v"
	prepared := PrepareOpenAIInputItems([]ResponseItem{
		{
			Type:   ResponseItemTypeFunctionCallOutput,
			CallID: "call_1",
			Name:   string(toolspec.ToolViewImage),
			Output: json.RawMessage(`[{"type":"input_file","file_data":"data:application/pdf;base64,Zm9v","filename":"doc.pdf"}]`),
		},
	})
	if len(prepared) != 2 {
		t.Fatalf("expected provider lifecycle to materialize 2 items, got %d", len(prepared))
	}
	items := mustBuildResponsesInput(t, prepared)
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}

	jsonItems := mustMarshalItems(t, items)
	if got := jsonItems[0]["type"]; got != "function_call_output" {
		t.Fatalf("expected function_call_output item, got %#v", got)
	}
	if output, ok := jsonItems[0]["output"].([]any); ok {
		for _, partRaw := range output {
			part, partOK := partRaw.(map[string]any)
			if !partOK {
				continue
			}
			if got := part["type"]; got == "input_file" {
				t.Fatalf("did not expect input_file inside function_call_output.output after promotion")
			}
		}
	}
	if output, ok := jsonItems[0]["output"].(string); !ok || strings.TrimSpace(output) == "" {
		t.Fatalf("expected non-empty string output for promoted file item, got %#v", jsonItems[0]["output"])
	}
	if got := jsonItems[1]["role"]; got != "user" {
		t.Fatalf("expected promoted user role, got %#v", got)
	}
	content, ok := jsonItems[1]["content"].([]any)
	if !ok || len(content) != 1 {
		t.Fatalf("expected one promoted content item, got %#v", jsonItems[1]["content"])
	}
	part, ok := content[0].(map[string]any)
	if !ok {
		t.Fatalf("expected promoted content object, got %#v", content[0])
	}
	if got := part["type"]; got != "input_file" {
		t.Fatalf("expected promoted input_file content, got %#v", got)
	}
	if got := part["file_data"]; got != pdfDataURL {
		t.Fatalf("unexpected file_data in promoted content: %#v", got)
	}
	if got := part["filename"]; got != "doc.pdf" {
		t.Fatalf("unexpected filename in promoted content: %#v", got)
	}
}

func TestBuildResponsesInputRejectsUnmaterializedViewImageInputFileOutput(t *testing.T) {
	_, err := buildResponsesInput([]ResponseItem{
		{
			Type:   ResponseItemTypeFunctionCallOutput,
			CallID: "call_1",
			Name:   string(toolspec.ToolViewImage),
			Output: json.RawMessage(`[{"type":"input_file","file_data":"data:application/pdf;base64,Zm9v","filename":"doc.pdf"}]`),
		},
	})
	if !errors.Is(err, ErrViewImageOutputNotMaterialized) {
		t.Fatalf("expected materialization error, got %v", err)
	}
}

func TestBuildResponsesInput_MessageToolOutputPromotesPDFToInputMessage(t *testing.T) {
	const pdfDataURL = "data:application/pdf;base64,Zm9v"
	items := mustBuildResponsesInput(t, ItemsFromMessages([]Message{
		{
			Role:       RoleTool,
			ToolCallID: "call_1",
			Name:       string(toolspec.ToolViewImage),
			Content:    `[{"type":"input_file","file_data":"data:application/pdf;base64,Zm9v","filename":"doc.pdf"}]`,
		},
	}))
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}

	jsonItems := mustMarshalItems(t, items)
	if got := jsonItems[0]["type"]; got != "function_call_output" {
		t.Fatalf("expected function_call_output item, got %#v", got)
	}
	if _, ok := jsonItems[0]["output"].([]any); ok {
		t.Fatalf("expected string output for promoted view_image PDF, got array")
	}
	if got := jsonItems[1]["role"]; got != "user" {
		t.Fatalf("expected promoted user role, got %#v", got)
	}
	content, ok := jsonItems[1]["content"].([]any)
	if !ok || len(content) != 1 {
		t.Fatalf("expected one promoted content item, got %#v", jsonItems[1]["content"])
	}
	part, ok := content[0].(map[string]any)
	if !ok {
		t.Fatalf("expected promoted content object, got %#v", content[0])
	}
	if got := part["type"]; got != "input_file" {
		t.Fatalf("expected promoted input_file content, got %#v", got)
	}
	if got := part["file_data"]; got != pdfDataURL {
		t.Fatalf("unexpected promoted file_data: %#v", got)
	}
	if got := part["filename"]; got != "doc.pdf" {
		t.Fatalf("unexpected promoted filename: %#v", got)
	}
}

func TestBuildResponsesInput_CanonicalNonViewImageToolOutputKeepsStructuredInputFileItems(t *testing.T) {
	items := mustBuildResponsesInput(t, []ResponseItem{
		{
			Type:   ResponseItemTypeFunctionCallOutput,
			CallID: "call_1",
			Name:   string(toolspec.ToolExecCommand),
			Output: json.RawMessage(`[{"type":"input_file","file_data":"Zm9v","filename":"doc.pdf"}]`),
		},
	})
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}

	jsonItems := mustMarshalItems(t, items)
	if got := jsonItems[0]["type"]; got != "function_call_output" {
		t.Fatalf("expected function_call_output item, got %#v", got)
	}
	output, ok := jsonItems[0]["output"].([]any)
	if !ok || len(output) != 1 {
		t.Fatalf("expected structured output array, got %#v", jsonItems[0]["output"])
	}
	part, ok := output[0].(map[string]any)
	if !ok {
		t.Fatalf("expected structured output object, got %#v", output[0])
	}
	if got := part["type"]; got != "input_file" {
		t.Fatalf("expected input_file output content, got %#v", got)
	}
}

func TestServiceBaseURL_UsesCodexEndpointBaseForOAuth(t *testing.T) {
	transport := NewHTTPTransport(staticAuth{})
	transport.BaseURL = "https://other.example/v1"

	got := transport.serviceBaseURL(OpenAIAuthMode{IsOAuth: true})
	if got != strings.TrimSuffix(codexResponsesEndpoint, "/responses") {
		t.Fatalf("expected oauth base endpoint %q, got %q", strings.TrimSuffix(codexResponsesEndpoint, "/responses"), got)
	}
	standard := transport.serviceBaseURL(OpenAIAuthMode{})
	if standard != "https://other.example/v1" {
		t.Fatalf("expected standard base endpoint, got %q", standard)
	}
}

func TestServiceBaseURL_ExplicitBaseURLOverridesOAuthEndpoint(t *testing.T) {
	transport := NewHTTPTransport(staticAuth{})
	transport.BaseURL = "http://localhost:8001/v1"
	transport.BaseURLExplicit = true

	got := transport.serviceBaseURL(OpenAIAuthMode{IsOAuth: true})
	if got != "http://localhost:8001/v1" {
		t.Fatalf("expected explicit base url to override oauth endpoint, got %q", got)
	}
}

func TestNewOpenAIProviderClientCanonicalizesBareDefaultOpenAIBaseURL(t *testing.T) {
	client, err := newOpenAIProviderClient(ProviderClientOptions{Auth: staticAuth{}, OpenAIBaseURL: "https://api.openai.com"})
	if err != nil {
		t.Fatalf("new openai provider client: %v", err)
	}
	openAIClient := openAIClientFromProvider(t, client)
	transport, ok := openAIClient.transport.(*HTTPTransport)
	if !ok {
		t.Fatalf("expected *HTTPTransport, got %T", openAIClient.transport)
	}
	if got := transport.serviceBaseURL(OpenAIAuthMode{}); got != defaultOpenAIBaseURL {
		t.Fatalf("service base url = %q, want %q", got, defaultOpenAIBaseURL)
	}
}

func TestBuildRequestOptions_OAuthAddsCodexHeaders(t *testing.T) {
	transport := NewHTTPTransport(staticAuth{})
	opts := transport.buildRequestOptions("Bearer x", OpenAIAuthMode{
		IsOAuth:   true,
		AccountID: "acc-1",
	}, "session-1")

	if len(opts) != 5 {
		t.Fatalf("expected 5 request options, got %d", len(opts))
	}
	if len(transport.buildRequestOptions("Bearer x", OpenAIAuthMode{}, "session-1")) != 4 {
		t.Fatal("expected non-oauth options to include auth/session/caching headers")
	}
	if len(transport.buildRequestOptions("Bearer x", OpenAIAuthMode{}, "")) != 3 {
		t.Fatal("expected non-oauth options to include auth/caching headers")
	}
}

func TestGenerateSendsConfiguredProviderIdentityHeaders(t *testing.T) {
	requestHeaders := make(chan http.Header, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestHeaders <- r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"resp_identity_1",
			"object":"response",
			"output":[],
			"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}
		}`))
	}))
	defer server.Close()

	transport := NewHTTPTransport(staticAuth{})
	transport.BaseURL = server.URL
	transport.Client = server.Client()
	transport.ProviderIdentifier = "acme_agent"

	if _, err := transport.Generate(context.Background(), OpenAIRequest{ToolChoiceMode: ToolChoiceModeAutomatic, Model: "gpt-5"}); err != nil {
		t.Fatalf("generate: %v", err)
	}
	headers := <-requestHeaders
	if got := headers.Get("originator"); got != "acme_agent" {
		t.Fatalf("originator = %q, want acme_agent", got)
	}
	if got, want := headers.Get("User-Agent"), "acme_agent/"+config.Version; got != want {
		t.Fatalf("User-Agent = %q, want %q", got, want)
	}
}

func TestSupportsRequestInputTokenCount_DisablesCodexOAuth(t *testing.T) {
	transport := NewHTTPTransport(oauthStaticAuth{})

	supported, err := transport.SupportsRequestInputTokenCount(context.Background())
	if err != nil {
		t.Fatalf("SupportsRequestInputTokenCount: %v", err)
	}
	if supported {
		t.Fatal("expected chatgpt-codex oauth input token counting to be unsupported")
	}
}

func TestSupportsRequestInputTokenCount_AllowsStandardOpenAI(t *testing.T) {
	transport := NewHTTPTransport(staticAuth{})

	supported, err := transport.SupportsRequestInputTokenCount(context.Background())
	if err != nil {
		t.Fatalf("SupportsRequestInputTokenCount: %v", err)
	}
	if !supported {
		t.Fatal("expected standard openai input token counting to remain supported")
	}
}

func TestNewOpenAIProviderContractErrorPreservesMissingResponseStatus(t *testing.T) {
	cause := errors.New("invalid provider response")
	err := newOpenAIProviderContractError("openai-compatible", nil, cause)
	if !errors.Is(err, cause) {
		t.Fatalf("provider contract error does not preserve cause: %v", err)
	}
	var missingStatus *providerContractErrorWithoutStatus
	if !errors.As(err, &missingStatus) {
		t.Fatalf("error = %T, want typed missing-status provider contract error", err)
	}
}

func TestBuildRequestOptions_OmitsAuthorizationHeaderWhenAuthHeaderEmpty(t *testing.T) {
	transport := NewHTTPTransport(staticAuth{})
	if len(transport.buildRequestOptions("", OpenAIAuthMode{}, "")) != 2 {
		t.Fatal("expected empty auth header to omit Authorization request option")
	}
	if len(transport.buildRequestOptions("   ", OpenAIAuthMode{}, "")) != 2 {
		t.Fatal("expected whitespace auth header to omit Authorization request option")
	}
	if len(transport.buildRequestOptions("", OpenAIAuthMode{}, "session-1")) != 3 {
		t.Fatal("expected session header to remain when Authorization is omitted")
	}
}

func TestResolveAuth_AllowsAnonymousWhenBaseURLExplicitAndAuthNotConfigured(t *testing.T) {
	transport := NewHTTPTransport(missingAuth{})
	transport.BaseURL = "http://127.0.0.1:8080/v1"
	transport.BaseURLExplicit = true

	authHeader, mode, err := transport.resolveAuth(context.Background())
	if err != nil {
		t.Fatalf("resolveAuth: %v", err)
	}
	if authHeader != "" {
		t.Fatalf("expected empty auth header, got %q", authHeader)
	}
	if mode.IsOAuth || mode.AccountID != "" {
		t.Fatalf("expected anonymous non-oauth mode, got %+v", mode)
	}
}

func TestGenerate_ExplicitBaseURLAllowsAnonymousRequests(t *testing.T) {
	authHeaderErrs := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if got := strings.TrimSpace(r.Header.Get("Authorization")); got != "" {
			authHeaderErrs <- fmt.Errorf("expected anonymous request without Authorization header, got %q", got)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"resp_anon_1",
			"object":"response",
			"output":[
				{
					"type":"message",
					"id":"msg_anon_1",
					"role":"assistant",
					"status":"completed",
					"content":[{"type":"output_text","text":"hello from anonymous compatible server"}]
				}
			],
			"usage":{"input_tokens":11,"output_tokens":7,"total_tokens":18}
		}`))
	}))
	defer server.Close()
	targetURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("parse server url: %v", err)
	}

	transport := NewHTTPTransport(nil)
	transport.BaseURL = "https://example.openrouter.ai/v1"
	transport.BaseURLExplicit = true
	transport.Client = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		cloned := req.Clone(req.Context())
		cloned.URL.Scheme = targetURL.Scheme
		cloned.URL.Host = targetURL.Host
		return server.Client().Transport.RoundTrip(cloned)
	})}

	providerCaps, err := transport.ProviderCapabilities(context.Background())
	if err != nil {
		t.Fatalf("provider capabilities: %v", err)
	}
	if providerCaps.ProviderID != "openai-compatible" {
		t.Fatalf("expected openai-compatible provider capabilities, got %+v", providerCaps)
	}

	resp, err := transport.Generate(context.Background(), OpenAIRequest{ToolChoiceMode: ToolChoiceModeAutomatic,
		Model: "vendor-custom-model",
		Items: []ResponseItem{{Type: ResponseItemTypeMessage, Role: RoleUser, Content: "hello"}},
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	select {
	case err := <-authHeaderErrs:
		t.Fatal(err)
	default:
	}
	if resp.AssistantText != "hello from anonymous compatible server" {
		t.Fatalf("assistant text = %q", resp.AssistantText)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestBuildPayload_UsesTransportStoreSetting(t *testing.T) {
	transport := NewHTTPTransport(staticAuth{})
	transport.Store = true
	payload, err := transport.buildPayload(OpenAIRequest{ToolChoiceMode: ToolChoiceModeAutomatic, Model: "gpt-5"}, OpenAIAuthMode{}, requireProviderCapabilities(t, transport, OpenAIAuthMode{}))
	if err != nil {
		t.Fatalf("build payload: %v", err)
	}
	jsonPayload := mustMarshalObject(t, payload)
	if got, ok := jsonPayload["store"].(bool); !ok || !got {
		t.Fatalf("expected store=true in payload, got %#v", jsonPayload["store"])
	}
}

func TestBuildPayload_AddsNativeWebSearchToolWhenEnabled(t *testing.T) {
	transport := NewHTTPTransport(staticAuth{})
	payload, err := transport.buildPayload(OpenAIRequest{ToolChoiceMode: ToolChoiceModeAutomatic,
		Model:                 "gpt-5",
		EnableNativeWebSearch: true,
	}, OpenAIAuthMode{}, requireProviderCapabilities(t, transport, OpenAIAuthMode{}))
	if err != nil {
		t.Fatalf("build payload: %v", err)
	}

	jsonPayload := mustMarshalObject(t, payload)
	tools, ok := jsonPayload["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("expected one native tool, got %#v", jsonPayload["tools"])
	}
	tool, ok := tools[0].(map[string]any)
	if !ok {
		t.Fatalf("expected web search tool object, got %#v", tools[0])
	}
	if got := tool["type"]; got != "web_search" {
		t.Fatalf("expected web_search tool, got %#v", got)
	}
}

func TestBuildPayloadRejectsRequiredToolChoiceForNonResponsesAdapter(t *testing.T) {
	transport := NewHTTPTransport(staticAuth{})
	_, err := transport.buildPayload(OpenAIRequest{
		Model:          "gpt-5",
		ToolChoiceMode: ToolChoiceModeRequired,
		Tools:          []Tool{{Name: "shell"}},
	}, OpenAIAuthMode{}, ProviderCapabilities{ProviderID: "anthropic"})
	if !errors.Is(err, ErrUnsupportedToolChoicePolicy) {
		t.Fatalf("buildPayload() error = %v, want ErrUnsupportedToolChoicePolicy", err)
	}
}

func TestBuildPayloadSerializesRequiredToolChoice(t *testing.T) {
	transport := NewHTTPTransport(staticAuth{})
	payload, err := transport.buildPayload(OpenAIRequest{
		Model:          "gpt-5",
		ToolChoiceMode: ToolChoiceModeRequired,
		Tools:          []Tool{{Name: "shell"}},
	}, OpenAIAuthMode{}, requireProviderCapabilities(t, transport, OpenAIAuthMode{}))
	if err != nil {
		t.Fatalf("buildPayload: %v", err)
	}
	jsonPayload := mustMarshalObject(t, payload)
	if got := jsonPayload["tool_choice"]; got != "required" {
		t.Fatalf("tool_choice = %#v, want required", got)
	}
}

func TestBuildPayloadSerializesAutomaticToolChoice(t *testing.T) {
	transport := NewHTTPTransport(staticAuth{})
	payload, err := transport.buildPayload(OpenAIRequest{
		Model:          "gpt-5",
		ToolChoiceMode: ToolChoiceModeAutomatic,
		Tools:          []Tool{{Name: "shell"}},
	}, OpenAIAuthMode{}, requireProviderCapabilities(t, transport, OpenAIAuthMode{}))
	if err != nil {
		t.Fatalf("buildPayload: %v", err)
	}
	jsonPayload := mustMarshalObject(t, payload)
	if got := jsonPayload["tool_choice"]; got != "auto" {
		t.Fatalf("tool_choice = %#v, want auto", got)
	}
}

func TestBuildPayloadRequiredToolChoicePreservesEffectiveToolsAndParallelSetting(t *testing.T) {
	transport := NewHTTPTransport(staticAuth{})
	caps := requireProviderCapabilities(t, transport, OpenAIAuthMode{})
	base := OpenAIRequest{
		Model:                 "gpt-5",
		EnableNativeWebSearch: true,
		Tools: []Tool{
			{Name: "shell"},
			{Name: "patch"},
		},
	}
	base.ToolChoiceMode = ToolChoiceModeAutomatic
	automatic, err := transport.buildPayload(base, OpenAIAuthMode{}, caps)
	if err != nil {
		t.Fatalf("build automatic payload: %v", err)
	}
	base.ToolChoiceMode = ToolChoiceModeRequired
	required, err := transport.buildPayload(base, OpenAIAuthMode{}, caps)
	if err != nil {
		t.Fatalf("build required payload: %v", err)
	}
	automaticJSON := mustMarshalObject(t, automatic)
	requiredJSON := mustMarshalObject(t, required)
	if !reflect.DeepEqual(automaticJSON["tools"], requiredJSON["tools"]) {
		t.Fatalf("tool declarations changed across modes\nautomatic=%#v\nrequired=%#v", automaticJSON["tools"], requiredJSON["tools"])
	}
	if automaticJSON["parallel_tool_calls"] != true || requiredJSON["parallel_tool_calls"] != true {
		t.Fatalf("parallel tool settings = automatic:%#v required:%#v", automaticJSON["parallel_tool_calls"], requiredJSON["parallel_tool_calls"])
	}
}

func TestBuildPayloadAcceptsRequiredToolChoiceWithHostedWebSearchOnly(t *testing.T) {
	transport := NewHTTPTransport(staticAuth{})
	payload, err := transport.buildPayload(OpenAIRequest{
		Model:                 "gpt-5",
		ToolChoiceMode:        ToolChoiceModeRequired,
		EnableNativeWebSearch: true,
	}, OpenAIAuthMode{}, requireProviderCapabilities(t, transport, OpenAIAuthMode{}))
	if err != nil {
		t.Fatalf("buildPayload: %v", err)
	}
	jsonPayload := mustMarshalObject(t, payload)
	tools, ok := jsonPayload["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("tools = %#v, want hosted web search only", jsonPayload["tools"])
	}
}

func TestBuildPayloadRejectsRequiredToolChoiceWithoutMaterializedTools(t *testing.T) {
	transport := NewHTTPTransport(staticAuth{})
	_, err := transport.buildPayload(OpenAIRequest{
		Model:          "gpt-5",
		ToolChoiceMode: ToolChoiceModeRequired,
	}, OpenAIAuthMode{}, requireProviderCapabilities(t, transport, OpenAIAuthMode{}))
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("buildPayload() error = %v, want ErrInvalidRequest", err)
	}
}

func TestBuildPayload_SerializesPatchAsCustomGrammarTool(t *testing.T) {
	transport := NewHTTPTransport(staticAuth{})
	payload, err := transport.buildPayload(OpenAIRequest{ToolChoiceMode: ToolChoiceModeAutomatic,
		Model: "gpt-5",
		Tools: []Tool{{
			Name:        string(toolspec.ToolPatch),
			Description: "Apply edits to files using freeform patch syntax.",
			Custom:      &CustomToolFormat{Type: "grammar", Syntax: "lark", Definition: "start: \"x\""},
		}},
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
		t.Fatalf("expected tool object, got %#v", tools[0])
	}
	if got := tool["type"]; got != "custom" {
		t.Fatalf("expected custom tool type, got %#v", got)
	}
	if got := tool["name"]; got != string(toolspec.ToolPatch) {
		t.Fatalf("expected patch tool name, got %#v", got)
	}
	if _, ok := tool["parameters"]; ok {
		t.Fatalf("custom patch tool must not include JSON parameters: %#v", tool)
	}
	format, ok := tool["format"].(map[string]any)
	if !ok {
		t.Fatalf("expected custom format object, got %#v", tool["format"])
	}
	if format["type"] != "grammar" || format["syntax"] != "lark" || format["definition"] != "start: \"x\"" {
		t.Fatalf("unexpected custom format: %#v", format)
	}
}

func TestBuildPayload_UsesExplicitPatchCustomGrammarTool(t *testing.T) {
	transport := NewHTTPTransport(oauthStaticAuth{})
	mode := OpenAIAuthMode{IsOAuth: true, AccountID: "acc-1"}
	payload, err := transport.buildPayload(OpenAIRequest{ToolChoiceMode: ToolChoiceModeAutomatic,
		Model: "gpt-5.4",
		Tools: []Tool{
			{Name: string(toolspec.ToolExecCommand), Description: "shell", Schema: json.RawMessage(`{"type":"object","additionalProperties":false}`)},
			{Name: string(toolspec.ToolPatch), Description: "patch", Custom: &CustomToolFormat{Type: "grammar", Syntax: "lark", Definition: PatchToolLarkGrammar}},
		},
	}, mode, requireProviderCapabilities(t, transport, mode))
	if err != nil {
		t.Fatalf("build payload: %v", err)
	}

	jsonPayload := mustMarshalObject(t, payload)
	if got, ok := jsonPayload["parallel_tool_calls"].(bool); !ok || !got {
		t.Fatalf("expected parallel_tool_calls=true, got %#v", jsonPayload["parallel_tool_calls"])
	}
	tools, ok := jsonPayload["tools"].([]any)
	if !ok || len(tools) != 2 {
		t.Fatalf("expected two tools, got %#v", jsonPayload["tools"])
	}
	names := make([]string, 0, len(tools))
	for idx, raw := range tools {
		tool, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("expected tool object, got %#v", raw)
		}
		if idx == 0 {
			if got := tool["type"]; got != "function" {
				t.Fatalf("expected shell function tool, got %#v", got)
			}
		}
		name, ok := tool["name"].(string)
		if !ok {
			t.Fatalf("expected function name, got %#v", tool["name"])
		}
		names = append(names, name)
	}
	if !reflect.DeepEqual(names, []string{string(toolspec.ToolExecCommand), string(toolspec.ToolPatch)}) {
		t.Fatalf("tool names = %+v, want raw requested names only", names)
	}
	patchTool, ok := tools[1].(map[string]any)
	if !ok {
		t.Fatalf("expected patch tool object, got %#v", tools[1])
	}
	if got := patchTool["type"]; got != "custom" {
		t.Fatalf("expected patch custom tool, got %#v", got)
	}
	format, ok := patchTool["format"].(map[string]any)
	if !ok || format["type"] != "grammar" || format["syntax"] != "lark" {
		t.Fatalf("expected patch grammar format, got %#v", patchTool["format"])
	}
	if _, ok := patchTool["parameters"]; ok {
		t.Fatalf("custom patch tool must not include legacy JSON parameters: %#v", patchTool)
	}
}

func TestBuildFunctionToolParamRejectsBlankCustomToolName(t *testing.T) {
	_, err := buildFunctionToolParam(Tool{
		Name:   "   ",
		Custom: &CustomToolFormat{Type: "grammar", Syntax: "lark", Definition: "start: \"x\""},
	})
	if !errors.Is(err, ErrCustomToolNameRequired) {
		t.Fatalf("error = %v, want blank custom tool name rejection", err)
	}
}

func TestBuildPayload_DoesNotAddNativeWebSearchToolWhenDisabled(t *testing.T) {
	transport := NewHTTPTransport(staticAuth{})
	payload, err := transport.buildPayload(OpenAIRequest{ToolChoiceMode: ToolChoiceModeAutomatic,
		Model:                 "gpt-5",
		EnableNativeWebSearch: false,
	}, OpenAIAuthMode{}, requireProviderCapabilities(t, transport, OpenAIAuthMode{}))
	if err != nil {
		t.Fatalf("build payload: %v", err)
	}

	jsonPayload := mustMarshalObject(t, payload)
	if _, ok := jsonPayload["tools"]; ok {
		t.Fatalf("expected no tools in payload, got %#v", jsonPayload["tools"])
	}
}

func TestBuildPayload_SetsPromptCacheKey(t *testing.T) {
	transport := NewHTTPTransport(staticAuth{})
	payload, err := transport.buildPayload(OpenAIRequest{ToolChoiceMode: ToolChoiceModeAutomatic,
		Model:          "gpt-5",
		PromptCacheKey: "cache-key-1",
	}, OpenAIAuthMode{}, requireProviderCapabilities(t, transport, OpenAIAuthMode{}))
	if err != nil {
		t.Fatalf("build payload: %v", err)
	}

	jsonPayload := mustMarshalObject(t, payload)
	if got := jsonPayload["prompt_cache_key"]; got != "cache-key-1" {
		t.Fatalf("expected prompt_cache_key=cache-key-1, got %#v", got)
	}
}

func TestBuildPayload_DoesNotSetPromptCacheKeyForOpenAICompatibleProvider(t *testing.T) {
	transport := NewHTTPTransport(staticAuth{})
	payload, err := transport.buildPayload(OpenAIRequest{ToolChoiceMode: ToolChoiceModeAutomatic,
		Model:          "gpt-5",
		PromptCacheKey: "cache-key-1",
	}, OpenAIAuthMode{}, ProviderCapabilities{
		ProviderID:           "openai-compatible",
		SupportsResponsesAPI: true,
		IsOpenAIFirstParty:   false,
	})
	if err != nil {
		t.Fatalf("build payload: %v", err)
	}

	jsonPayload := mustMarshalObject(t, payload)
	if _, ok := jsonPayload["prompt_cache_key"]; ok {
		t.Fatalf("expected prompt_cache_key omitted for openai-compatible provider, got %#v", jsonPayload["prompt_cache_key"])
	}
}

func TestBuildPayload_SetsPromptCacheKeyWhenExplicitCapabilityIsEnabled(t *testing.T) {
	transport := NewHTTPTransport(staticAuth{})
	payload, err := transport.buildPayload(OpenAIRequest{ToolChoiceMode: ToolChoiceModeAutomatic,
		Model:          "gpt-5",
		PromptCacheKey: "cache-key-1",
	}, OpenAIAuthMode{}, ProviderCapabilities{
		ProviderID:             "openai-compatible",
		SupportsResponsesAPI:   true,
		SupportsPromptCacheKey: true,
		IsOpenAIFirstParty:     false,
	})
	if err != nil {
		t.Fatalf("build payload: %v", err)
	}

	jsonPayload := mustMarshalObject(t, payload)
	if got := jsonPayload["prompt_cache_key"]; got != "cache-key-1" {
		t.Fatalf("expected prompt_cache_key=cache-key-1, got %#v", got)
	}
}

func TestHTTPTransport_ProviderCapabilitiesOverrideControlsPromptCacheKeyPayload(t *testing.T) {
	transport := NewHTTPTransport(staticAuth{})
	transport.ProviderCapabilitiesOverride = &ProviderCapabilities{
		ProviderID:             "openai-compatible",
		SupportsResponsesAPI:   true,
		SupportsPromptCacheKey: false,
	}
	payload, err := transport.buildPayload(OpenAIRequest{ToolChoiceMode: ToolChoiceModeAutomatic,
		Model:          "gpt-5",
		PromptCacheKey: "cache-key-1",
	}, OpenAIAuthMode{}, requireProviderCapabilities(t, transport, OpenAIAuthMode{}))
	if err != nil {
		t.Fatalf("build payload: %v", err)
	}
	jsonPayload := mustMarshalObject(t, payload)
	if _, ok := jsonPayload["prompt_cache_key"]; ok {
		t.Fatalf("expected provider capability override to omit prompt_cache_key, got %#v", jsonPayload["prompt_cache_key"])
	}
}
