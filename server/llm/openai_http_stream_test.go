package llm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type staticAuthHeader struct{}

func (staticAuthHeader) AuthorizationHeader(context.Context) (string, error) {
	return "Bearer test", nil
}

func newOpenAIStreamTestServer(t *testing.T, events ...string) *httptest.Server {
	t.Helper()
	var stream strings.Builder
	for _, event := range events {
		_, _ = fmt.Fprintf(&stream, "data: %s\n\n", event)
	}
	return newOpenAIRawStreamTestServer(t, stream.String())
}

func newOpenAIRawStreamTestServer(t *testing.T, stream string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(stream))
	}))
	t.Cleanup(server.Close)
	return server
}

func newOpenAIStreamTestTransport(t *testing.T, events ...string) *HTTPTransport {
	t.Helper()
	server := newOpenAIStreamTestServer(t, events...)
	return newOpenAIStreamTestTransportForServer(server)
}

func newOpenAIStreamTestTransportForServer(server *httptest.Server) *HTTPTransport {
	transport := NewHTTPTransport(staticAuthHeader{})
	transport.BaseURL = server.URL
	transport.Client = server.Client()
	return transport
}

func newOpenAIRawStreamTestTransport(t *testing.T, stream string) *HTTPTransport {
	t.Helper()
	server := newOpenAIRawStreamTestServer(t, stream)
	return newOpenAIStreamTestTransportForServer(server)
}

func joinedAssistantDeltas(deltas []AssistantDelta) string {
	var text strings.Builder
	for _, delta := range deltas {
		text.WriteString(delta.Text)
	}
	return text.String()
}

func TestGenerateStream_AcceptsCompletedResponseEOFWithoutDoneSentinel(t *testing.T) {
	transport := newOpenAIRawStreamTestTransport(t, strings.Join([]string{
		`event: response.completed`,
		`data: {"type":"response.completed","sequence_number":1,"response":{"usage":{"input_tokens":11,"output_tokens":7,"total_tokens":18},"output":[{"type":"message","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"Hello"}]}]}}`,
		``,
		``,
	}, "\n"))

	resp, err := transport.GenerateStreamWithEvents(context.Background(), OpenAIRequest{ToolChoiceMode: ToolChoiceModeAutomatic, Model: "gpt-5"}, StreamCallbacks{})
	if err != nil {
		t.Fatalf("GenerateStream failed: %v", err)
	}

	if resp.AssistantText != "Hello" {
		t.Fatalf("assistant text = %q, want Hello", resp.AssistantText)
	}
	if resp.Usage.InputTokens != 11 || resp.Usage.OutputTokens != 7 {
		t.Fatalf("unexpected usage: %+v", resp.Usage)
	}
}

func TestGenerateStream_SalvagesCompletedResponseBeforeTrailingMalformedEvent(t *testing.T) {
	transport := newOpenAIRawStreamTestTransport(t, strings.Join([]string{
		`event: response.completed`,
		`data: {"type":"response.completed","sequence_number":1,"response":{"usage":{"input_tokens":3,"output_tokens":5,"total_tokens":8},"output":[{"type":"message","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"Done"}]}]}}`,
		``,
		`event: response.completed`,
		`data: `,
		``,
		``,
	}, "\n"))

	resp, err := transport.GenerateStreamWithEvents(context.Background(), OpenAIRequest{ToolChoiceMode: ToolChoiceModeAutomatic, Model: "gpt-5"}, StreamCallbacks{})
	if err != nil {
		t.Fatalf("GenerateStream failed: %v", err)
	}

	if resp.AssistantText != "Done" {
		t.Fatalf("assistant text = %q, want Done", resp.AssistantText)
	}
	if resp.Usage.InputTokens != 3 || resp.Usage.OutputTokens != 5 {
		t.Fatalf("unexpected usage: %+v", resp.Usage)
	}
}

func TestGenerateStream_MapsResponseIncompleteEventToProviderAPIError(t *testing.T) {
	transport := newOpenAIRawStreamTestTransport(t, strings.Join([]string{
		`event: response.output_text.delta`,
		`data: {"type":"response.output_text.delta","output_index":0,"delta":"partial"}`,
		``,
		`event: response.incomplete`,
		`data: {"type":"response.incomplete","sequence_number":2,"response":{"id":"resp_1","created_at":1,"incomplete_details":{"reason":"max_output_tokens"},"output":[{"type":"message","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"partial"}]}]}}`,
		``,
		``,
	}, "\n"))

	_, err := transport.GenerateStreamWithEvents(context.Background(), OpenAIRequest{ToolChoiceMode: ToolChoiceModeAutomatic, Model: "gpt-5"}, StreamCallbacks{})
	if err == nil {
		t.Fatal("expected response incomplete event")
	}
	var providerErr *ProviderAPIError
	if !errors.As(err, &providerErr) {
		t.Fatalf("expected ProviderAPIError, got %T", err)
	}
	if providerErr.ProviderType != "response.incomplete" {
		t.Fatalf("provider type = %q, want response.incomplete", providerErr.ProviderType)
	}
	if providerErr.ProviderCode != "max_output_tokens" {
		t.Fatalf("provider code = %q, want max_output_tokens", providerErr.ProviderCode)
	}
	if providerErr.ProviderParam != "response.incomplete_details.reason" {
		t.Fatalf("provider param = %q, want response.incomplete_details.reason", providerErr.ProviderParam)
	}
	if providerErr.StatusCode != http.StatusOK {
		t.Fatalf("provider status = %d, want %d", providerErr.StatusCode, http.StatusOK)
	}
	if !IsNonRetriableModelError(err) {
		t.Fatalf("expected response.incomplete terminal error to be non-retriable: %v", err)
	}
}

func TestGenerateStream_MapsResponseIncompleteWithoutReasonToProviderContractError(t *testing.T) {
	transport := newOpenAIRawStreamTestTransport(t, strings.Join([]string{
		`event: response.incomplete`,
		`data: {"type":"response.incomplete","sequence_number":1,"response":{"id":"resp_1","created_at":1,"output":[]}}`,
		``,
		``,
	}, "\n"))

	_, err := transport.GenerateStreamWithEvents(context.Background(), OpenAIRequest{ToolChoiceMode: ToolChoiceModeAutomatic, Model: "gpt-5"}, StreamCallbacks{})
	if err == nil {
		t.Fatal("expected response incomplete event")
	}
	var providerErr *ProviderAPIError
	if !errors.As(err, &providerErr) {
		t.Fatalf("expected ProviderAPIError, got %T", err)
	}
	if providerErr.Code != UnifiedErrorCodeProviderContract {
		t.Fatalf("provider code = %q, want %q", providerErr.Code, UnifiedErrorCodeProviderContract)
	}
	if providerErr.StatusCode != http.StatusOK {
		t.Fatalf("provider status = %d, want %d", providerErr.StatusCode, http.StatusOK)
	}
}

func TestGenerateStream_RejectsCompletedEventWithoutResponsePayload(t *testing.T) {
	cases := []struct {
		name string
		data string
	}{
		{name: "missing_response", data: `{"type":"response.completed","sequence_number":1}`},
		{name: "empty_response", data: `{"type":"response.completed","sequence_number":1,"response":{}}`},
		{name: "null_output", data: `{"type":"response.completed","sequence_number":1,"response":{"output":null}}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			transport := newOpenAIRawStreamTestTransport(t, strings.Join([]string{
				`event: response.completed`,
				`data: ` + tc.data,
				``,
				``,
			}, "\n"))

			_, err := transport.GenerateStreamWithEvents(context.Background(), OpenAIRequest{ToolChoiceMode: ToolChoiceModeAutomatic, Model: "gpt-5"}, StreamCallbacks{})
			if err == nil {
				t.Fatal("expected provider contract error")
			}
			var providerErr *ProviderAPIError
			if !errors.As(err, &providerErr) {
				t.Fatalf("expected ProviderAPIError, got %T", err)
			}
			if providerErr.Code != UnifiedErrorCodeProviderContract {
				t.Fatalf("provider code = %q, want %q", providerErr.Code, UnifiedErrorCodeProviderContract)
			}
			if providerErr.StatusCode != http.StatusOK {
				t.Fatalf("provider status = %d, want %d", providerErr.StatusCode, http.StatusOK)
			}
		})
	}
}

func TestGenerateStream_RejectsPreTerminalMalformedResponsesStream(t *testing.T) {
	cases := map[string]string{
		"eof_without_terminal": strings.Join([]string{
			`event: response.output_text.delta`,
			`data: {"type":"response.output_text.delta","output_index":0,"delta":"partial"}`,
			``,
			``,
		}, "\n"),
		"malformed_json_before_terminal": strings.Join([]string{
			`event: response.output_text.delta`,
			`data: {"type":`,
			``,
			``,
		}, "\n"),
		"empty_data_before_terminal": strings.Join([]string{
			`data: `,
			``,
			``,
		}, "\n"),
		"invalid_schema_before_terminal": strings.Join([]string{
			`data: {"type":1}`,
			``,
			``,
		}, "\n"),
	}

	for name, stream := range cases {
		t.Run(name, func(t *testing.T) {
			transport := newOpenAIRawStreamTestTransport(t, stream)

			_, err := transport.GenerateStreamWithEvents(context.Background(), OpenAIRequest{ToolChoiceMode: ToolChoiceModeAutomatic, Model: "gpt-5"}, StreamCallbacks{})
			if err == nil {
				t.Fatal("expected provider contract error")
			}
			var providerErr *ProviderAPIError
			if !errors.As(err, &providerErr) {
				t.Fatalf("expected ProviderAPIError, got %T", err)
			}
			if providerErr.Code != UnifiedErrorCodeProviderContract {
				t.Fatalf("provider code = %q, want %q", providerErr.Code, UnifiedErrorCodeProviderContract)
			}
			if !IsNonRetriableModelError(err) {
				t.Fatalf("expected provider-contract stream error to be non-retriable: %v", err)
			}
		})
	}
}

func TestGenerateStream_LeavesPreResponseEOFRetryable(t *testing.T) {
	transport := NewHTTPTransport(staticAuthHeader{})
	transport.BaseURL = "https://example.invalid"
	transport.Client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, io.EOF
	})}

	_, err := transport.GenerateStreamWithEvents(context.Background(), OpenAIRequest{ToolChoiceMode: ToolChoiceModeAutomatic, Model: "gpt-5"}, StreamCallbacks{})
	if err == nil {
		t.Fatal("expected pre-response EOF")
	}
	var providerErr *ProviderAPIError
	if errors.As(err, &providerErr) {
		t.Fatalf("expected pre-response EOF to stay outside provider-contract classification, got %+v", providerErr)
	}
	if IsNonRetriableModelError(err) {
		t.Fatalf("expected pre-response EOF to remain retryable: %v", err)
	}
}

func TestGenerateStream_EmitsAssistantDeltasAndToolCalls(t *testing.T) {
	transport := newOpenAIStreamTestTransport(t,
		`{"type":"response.output_item.added","item":{"id":"fc_1","type":"function_call","name":"shell","call_id":"call_1","arguments":""}}`,
		`{"type":"response.function_call_arguments.delta","item_id":"fc_1","delta":"{\"command\":\"pwd\"}"}`,
		`{"type":"response.output_item.added","output_index":1,"item":{"type":"message","role":"assistant","phase":"commentary","content":[]}}`,
		`{"type":"response.output_text.delta","output_index":1,"delta":"Hel"}`,
		`{"type":"response.output_text.delta","output_index":1,"delta":"lo"}`,
		`{"type":"response.reasoning_summary_text.delta","item_id":"rs_1","output_index":1,"summary_index":0,"delta":"Plan"}`,
		`{"type":"response.completed","response":{"usage":{"input_tokens":11,"input_tokens_details":{"cached_tokens":4},"output_tokens":7,"output_tokens_details":{"reasoning_tokens":2},"total_tokens":18},"output":[{"type":"message","role":"assistant","phase":"commentary","content":[{"type":"output_text","text":"Hello"}]},{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"Plan"}],"content":[{"type":"reasoning_text","text":"internal trace"}],"encrypted_content":"enc_1"},{"type":"function_call","id":"fc_1","name":"shell","call_id":"call_1","arguments":"{\"command\":\"pwd\"}"}]}}`,
		`[DONE]`,
	)

	var deltas []AssistantDelta
	var reasoning []ReasoningSummaryDelta
	resp, err := transport.GenerateStreamWithEvents(context.Background(), OpenAIRequest{ToolChoiceMode: ToolChoiceModeAutomatic, Model: "gpt-5"}, StreamCallbacks{
		OnAssistantDelta: func(delta AssistantDelta) {
			deltas = append(deltas, delta)
		},
		OnReasoningSummaryDelta: func(delta ReasoningSummaryDelta) {
			reasoning = append(reasoning, delta)
		},
	})
	if err != nil {
		t.Fatalf("GenerateStream failed: %v", err)
	}

	if joinedAssistantDeltas(deltas) != "Hello" {
		t.Fatalf("unexpected deltas: %+v", deltas)
	}
	if len(deltas) != 2 || deltas[0].Phase != MessagePhaseCommentary || deltas[1].Phase != MessagePhaseCommentary {
		t.Fatalf("unexpected delta phases: %+v", deltas)
	}
	if resp.AssistantText != "Hello" {
		t.Fatalf("unexpected assistant text: %q", resp.AssistantText)
	}
	if resp.AssistantPhase != MessagePhaseCommentary {
		t.Fatalf("unexpected assistant phase: %q", resp.AssistantPhase)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(resp.ToolCalls))
	}
	if resp.ToolCalls[0].ID != "call_1" || resp.ToolCalls[0].Name != "shell" {
		t.Fatalf("unexpected tool call: %+v", resp.ToolCalls[0])
	}
	if string(resp.ToolCalls[0].Input) != "{\"command\":\"pwd\"}" {
		t.Fatalf("unexpected tool args: %s", string(resp.ToolCalls[0].Input))
	}
	if resp.Usage.InputTokens != 11 || resp.Usage.OutputTokens != 7 {
		t.Fatalf("unexpected usage: %+v", resp.Usage)
	}
	if !resp.Usage.HasCachedInputTokens || resp.Usage.CachedInputTokens != 4 {
		t.Fatalf("unexpected cached usage details: %+v", resp.Usage)
	}
	if len(resp.Reasoning) != 1 || resp.Reasoning[0].Role != "reasoning" || resp.Reasoning[0].Text != "Plan" {
		t.Fatalf("unexpected reasoning summary entries: %+v", resp.Reasoning)
	}
	if len(resp.ReasoningItems) != 1 || resp.ReasoningItems[0].ID != "rs_1" || resp.ReasoningItems[0].EncryptedContent != "enc_1" {
		t.Fatalf("unexpected reasoning items: %+v", resp.ReasoningItems)
	}
	if len(reasoning) != 1 || reasoning[0].Key == "" || reasoning[0].Role != "reasoning" || reasoning[0].Text != "Plan" {
		t.Fatalf("unexpected reasoning delta callbacks: %+v", reasoning)
	}
}

func TestGenerateStream_EmitsUnknownPhaseWhenDeltaPrecedesAssistantItem(t *testing.T) {
	transport := newOpenAIStreamTestTransport(t,
		`{"type":"response.output_text.delta","output_index":0,"delta":"Hel"}`,
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"message","role":"assistant","phase":"final_answer","content":[]}}`,
		`{"type":"response.output_text.delta","output_index":0,"delta":"lo"}`,
		`{"type":"response.completed","response":{"output":[{"type":"message","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"Hello"}]}]}}`,
		`[DONE]`,
	)

	var deltas []AssistantDelta
	resp, err := transport.GenerateStreamWithEvents(context.Background(), OpenAIRequest{ToolChoiceMode: ToolChoiceModeAutomatic, Model: "gpt-5"}, StreamCallbacks{
		OnAssistantDelta: func(delta AssistantDelta) {
			deltas = append(deltas, delta)
		},
	})
	if err != nil {
		t.Fatalf("GenerateStream failed: %v", err)
	}

	if joinedAssistantDeltas(deltas) != "Hello" {
		t.Fatalf("unexpected deltas: %+v", deltas)
	}
	if len(deltas) != 2 {
		t.Fatalf("expected two deltas, got %+v", deltas)
	}
	if deltas[0].Phase != "" {
		t.Fatalf("expected unknown phase before assistant item, got %+v", deltas[0])
	}
	if deltas[1].Phase != MessagePhaseFinal {
		t.Fatalf("expected structured phase after assistant item, got %+v", deltas[1])
	}
	if resp.AssistantPhase != MessagePhaseFinal {
		t.Fatalf("unexpected final assistant phase: %q", resp.AssistantPhase)
	}
}

func TestGenerateStream_PreservesDisplayedDeltasWhenCompletedMessageIsShorter(t *testing.T) {
	transport := newOpenAIStreamTestTransport(t,
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"message","role":"assistant","phase":"final_answer","content":[]}}`,
		`{"type":"response.output_text.delta","output_index":0,"delta":"Hello"}`,
		`{"type":"response.output_text.delta","output_index":0,"delta":"\n\n"}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"type":"message","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"Hello"}]}}`,
		`{"type":"response.completed","response":{"output":[{"type":"message","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"Hello"}]}]}}`,
		`[DONE]`,
	)

	var deltas []AssistantDelta
	resp, err := transport.GenerateStreamWithEvents(context.Background(), OpenAIRequest{ToolChoiceMode: ToolChoiceModeAutomatic, Model: "gpt-5"}, StreamCallbacks{
		OnAssistantDelta: func(delta AssistantDelta) {
			deltas = append(deltas, delta)
		},
	})
	if err != nil {
		t.Fatalf("GenerateStream failed: %v", err)
	}

	const streamed = "Hello\n\n"
	if got := joinedAssistantDeltas(deltas); got != streamed {
		t.Fatalf("streamed deltas = %q, want %q", got, streamed)
	}
	if resp.AssistantText != streamed {
		t.Fatalf("assistant text = %q, want exact streamed text", resp.AssistantText)
	}
	if len(resp.OutputItems) != 1 || resp.OutputItems[0].Content != streamed {
		t.Fatalf("output items = %+v, want assistant content repaired to streamed text", resp.OutputItems)
	}
}

func TestGenerateStream_DoesNotRepairMultiMessageAssistantOutputWithAggregateText(t *testing.T) {
	transport := newOpenAIStreamTestTransport(t,
		`{"type":"response.completed","response":{"output":[{"type":"message","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"A"}]},{"type":"message","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"B"}]}]}}`,
		`[DONE]`,
	)

	resp, err := transport.GenerateStreamWithEvents(context.Background(), OpenAIRequest{ToolChoiceMode: ToolChoiceModeAutomatic, Model: "gpt-5"}, StreamCallbacks{})
	if err != nil {
		t.Fatalf("GenerateStream failed: %v", err)
	}

	if resp.AssistantText != "AB" {
		t.Fatalf("assistant text = %q, want aggregate completed text", resp.AssistantText)
	}
	if len(resp.OutputItems) != 2 || resp.OutputItems[0].Content != "A" || resp.OutputItems[1].Content != "B" {
		t.Fatalf("output items = %+v, want original assistant message segments", resp.OutputItems)
	}
}

func TestGenerateStream_MapsStructuredStreamErrorToProviderAPIError(t *testing.T) {
	transport := newOpenAIStreamTestTransport(t, `{"type":"error","error":{"type":"invalid_request_error","code":"context_length_exceeded","param":"input","message":"too many tokens"}}`)

	_, err := transport.GenerateStreamWithEvents(context.Background(), OpenAIRequest{ToolChoiceMode: ToolChoiceModeAutomatic, Model: "gpt-5"}, StreamCallbacks{})
	if err == nil {
		t.Fatal("expected stream error")
	}
	if !IsContextLengthOverflowError(err) {
		t.Fatalf("expected context overflow classification, got %v", err)
	}
	var providerErr *ProviderAPIError
	if !errors.As(err, &providerErr) {
		t.Fatalf("expected ProviderAPIError, got %T", err)
	}
	if providerErr.ProviderCode != "context_length_exceeded" || providerErr.ProviderParam != "input" {
		t.Fatalf("unexpected provider error: %+v", providerErr)
	}
}

func TestGenerateStream_MapsResponseErrorEventToProviderAPIError(t *testing.T) {
	transport := newOpenAIStreamTestTransport(t,
		`{"type":"error","code":"context_length_exceeded","param":"input","message":"too many tokens","sequence_number":1}`,
		`[DONE]`,
	)

	_, err := transport.GenerateStreamWithEvents(context.Background(), OpenAIRequest{ToolChoiceMode: ToolChoiceModeAutomatic, Model: "gpt-5"}, StreamCallbacks{})
	if err == nil {
		t.Fatal("expected response error event")
	}
	if !IsContextLengthOverflowError(err) {
		t.Fatalf("expected context overflow classification, got %v", err)
	}
}

func TestGenerateStream_MapsResponseFailedEventToProviderAPIError(t *testing.T) {
	transport := newOpenAIStreamTestTransport(t,
		`{"type":"response.failed","sequence_number":1,"response":{"id":"resp_1","created_at":1,"error":{"code":"context_length_exceeded","message":"too many tokens"}}}`,
		`[DONE]`,
	)

	_, err := transport.GenerateStreamWithEvents(context.Background(), OpenAIRequest{ToolChoiceMode: ToolChoiceModeAutomatic, Model: "gpt-5"}, StreamCallbacks{})
	if err == nil {
		t.Fatal("expected response failed event")
	}
	if !IsContextLengthOverflowError(err) {
		t.Fatalf("expected context overflow classification, got %v", err)
	}
}

func TestGenerateStream_ReturnsUnknownProviderErrorForUnrecognizedStructuredStreamError(t *testing.T) {
	transport := newOpenAIStreamTestTransport(t,
		`{"type":"error","details":{"unexpected":"shape"},"sequence_number":1}`,
		`[DONE]`,
	)

	_, err := transport.GenerateStreamWithEvents(context.Background(), OpenAIRequest{ToolChoiceMode: ToolChoiceModeAutomatic, Model: "gpt-5"}, StreamCallbacks{})
	if err == nil {
		t.Fatal("expected unrecognized stream error")
	}
	var providerErr *ProviderAPIError
	if !errors.As(err, &providerErr) {
		t.Fatalf("expected ProviderAPIError, got %T", err)
	}
	if providerErr.Code != UnifiedErrorCodeUnknown {
		t.Fatalf("provider code = %q, want %q", providerErr.Code, UnifiedErrorCodeUnknown)
	}
}

func TestGenerateStream_ParsesCustomPatchToolCall(t *testing.T) {
	patchInput := "*** Begin Patch\n*** Add File: a.txt\n+hi\n*** End Patch\n"
	transport := newOpenAIStreamTestTransport(t,
		`{"type":"response.output_item.added","item":{"id":"ct_1","type":"custom_tool_call","name":"patch","call_id":"call_1","input":""}}`,
		fmt.Sprintf(`{"type":"response.custom_tool_call_input.delta","item_id":"ct_1","delta":%q}`, patchInput),
		fmt.Sprintf(`{"type":"response.completed","response":{"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2},"output":[{"type":"custom_tool_call","id":"ct_1","name":"patch","call_id":"call_1","input":%q}]}}`, patchInput),
		`[DONE]`,
	)

	resp, err := transport.GenerateStreamWithEvents(context.Background(), OpenAIRequest{ToolChoiceMode: ToolChoiceModeAutomatic, Model: "gpt-5"}, StreamCallbacks{})
	if err != nil {
		t.Fatalf("GenerateStream failed: %v", err)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(resp.ToolCalls))
	}
	if resp.ToolCalls[0].ID != "call_1" || resp.ToolCalls[0].Name != "patch" {
		t.Fatalf("unexpected custom tool call: %+v", resp.ToolCalls[0])
	}
	if !resp.ToolCalls[0].Custom || resp.ToolCalls[0].CustomInput != patchInput {
		t.Fatalf("unexpected custom patch tool call: %+v", resp.ToolCalls[0])
	}
	if len(resp.OutputItems) != 1 || resp.OutputItems[0].Type != ResponseItemTypeCustomToolCall || resp.OutputItems[0].CustomInput != patchInput {
		t.Fatalf("unexpected custom output item: %+v", resp.OutputItems)
	}
}

func TestToolCallAccumulatorMergesCompletedCustomInputWithoutJSONInput(t *testing.T) {
	accumulator := newToolCallAccumulator()
	accumulator.Merge([]ToolCall{{ID: "call-1", Name: "patch", Custom: true, CustomInput: "partial"}})
	accumulator.Merge([]ToolCall{{ID: "call-1", Name: "patch", Custom: true, CustomInput: "complete"}})

	calls := accumulator.ToToolCalls()
	if len(calls) != 1 {
		t.Fatalf("expected one call, got %+v", calls)
	}
	if !calls[0].Custom || calls[0].CustomInput != "complete" {
		t.Fatalf("expected completed custom input to replace partial input, got %+v", calls[0])
	}
}

func TestGenerateStream_PreservesBoldReasoningTextWithoutInferringStatus(t *testing.T) {
	transport := newOpenAIStreamTestTransport(t,
		`{"type":"response.reasoning_summary_text.delta","item_id":"rs_1","output_index":0,"summary_index":0,"delta":"**Preparing patch**\n\nPlain summary text"}`,
		`{"type":"response.completed","response":{"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2},"output":[{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"**Preparing patch**\n\nPlain summary text"}],"encrypted_content":"enc_1"}]}}`,
		`[DONE]`,
	)

	var reasoning []ReasoningSummaryDelta
	resp, err := transport.GenerateStreamWithEvents(context.Background(), OpenAIRequest{ToolChoiceMode: ToolChoiceModeAutomatic, Model: "gpt-5"}, StreamCallbacks{
		OnReasoningSummaryDelta: func(delta ReasoningSummaryDelta) {
			reasoning = append(reasoning, delta)
		},
	})
	if err != nil {
		t.Fatalf("GenerateStream failed: %v", err)
	}

	if len(reasoning) != 1 {
		t.Fatalf("expected 1 reasoning delta callback, got %+v", reasoning)
	}
	if reasoning[0].Text != "**Preparing patch**\n\nPlain summary text" {
		t.Fatalf("summary = %q", reasoning[0].Text)
	}
	if len(resp.Reasoning) != 1 || resp.Reasoning[0].Text != "**Preparing patch**\n\nPlain summary text" {
		t.Fatalf("unexpected final reasoning summary entries: %+v", resp.Reasoning)
	}
}

func TestGenerateStream_PreservesStreamedAssistantTextWhenCompletedMessageIsEmpty(t *testing.T) {
	transport := newOpenAIStreamTestTransport(t,
		`{"type":"response.output_item.added","output_index":0,"item":{"id":"msg_1","type":"message","role":"assistant","phase":"commentary","content":[]}}`,
		`{"type":"response.output_text.delta","delta":"Hel"}`,
		`{"type":"response.output_text.delta","delta":"lo"}`,
		`{"type":"response.output_item.added","output_index":1,"item":{"id":"fc_1","type":"function_call","name":"shell","call_id":"call_1","arguments":"{\"command\":\"pwd\"}"}}`,
		`{"type":"response.completed","response":{"usage":{"input_tokens":3,"output_tokens":4,"total_tokens":7},"output":[{"id":"msg_1","type":"message","role":"assistant","content":[]},{"type":"function_call","id":"fc_1","name":"shell","call_id":"call_1","arguments":"{\"command\":\"pwd\"}"}]}}`,
		`[DONE]`,
	)

	resp, err := transport.GenerateStreamWithEvents(context.Background(), OpenAIRequest{ToolChoiceMode: ToolChoiceModeAutomatic, Model: "gpt-5"}, StreamCallbacks{})
	if err != nil {
		t.Fatalf("GenerateStream failed: %v", err)
	}

	if resp.AssistantText != "Hello" {
		t.Fatalf("assistant text = %q, want Hello", resp.AssistantText)
	}
	if resp.AssistantPhase != MessagePhaseCommentary {
		t.Fatalf("assistant phase = %q, want %q", resp.AssistantPhase, MessagePhaseCommentary)
	}
	if len(resp.OutputItems) != 2 {
		t.Fatalf("expected 2 output items, got %+v", resp.OutputItems)
	}
	if resp.OutputItems[0].Type != ResponseItemTypeMessage || resp.OutputItems[0].Content != "Hello" {
		t.Fatalf("unexpected assistant output item: %+v", resp.OutputItems[0])
	}
	if resp.OutputItems[0].Phase != MessagePhaseCommentary {
		t.Fatalf("assistant output phase = %q, want %q", resp.OutputItems[0].Phase, MessagePhaseCommentary)
	}
}

func TestGenerateStream_PreservesAssistantOutputItemPhaseWhenCompletedPhaseIsMissing(t *testing.T) {
	transport := newOpenAIStreamTestTransport(t,
		`{"type":"response.output_item.done","output_index":0,"item":{"id":"msg_1","type":"message","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"Done"}]}}`,
		`{"type":"response.completed","response":{"usage":{"input_tokens":2,"output_tokens":2,"total_tokens":4},"output":[{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"output_text","text":"Done"}]}]}}`,
		`[DONE]`,
	)

	resp, err := transport.GenerateStreamWithEvents(context.Background(), OpenAIRequest{ToolChoiceMode: ToolChoiceModeAutomatic, Model: "gpt-5"}, StreamCallbacks{})
	if err != nil {
		t.Fatalf("GenerateStream failed: %v", err)
	}

	if resp.AssistantText != "Done" {
		t.Fatalf("assistant text = %q, want Done", resp.AssistantText)
	}
	if resp.AssistantPhase != MessagePhaseFinal {
		t.Fatalf("assistant phase = %q, want %q", resp.AssistantPhase, MessagePhaseFinal)
	}
	if len(resp.OutputItems) != 1 {
		t.Fatalf("expected 1 output item, got %+v", resp.OutputItems)
	}
	if resp.OutputItems[0].Phase != MessagePhaseFinal {
		t.Fatalf("assistant output phase = %q, want %q", resp.OutputItems[0].Phase, MessagePhaseFinal)
	}
}

func TestGenerateStream_PrefersPhaseResolvedAssistantTextOverRawDeltaConcatenation(t *testing.T) {
	transport := newOpenAIStreamTestTransport(t,
		`{"type":"response.output_item.done","output_index":0,"item":{"id":"msg_1","type":"message","role":"assistant","phase":"commentary","content":[{"type":"output_text","text":"Draft: "}]}}`,
		`{"type":"response.output_text.delta","delta":"Draft: "}`,
		`{"type":"response.output_item.done","output_index":2,"item":{"id":"msg_2","type":"message","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"Done"}]}}`,
		`{"type":"response.output_text.delta","delta":"Done"}`,
		`{"type":"response.completed","response":{"usage":{"input_tokens":2,"output_tokens":3,"total_tokens":5},"output":[{"type":"function_call","id":"fc_1","name":"shell","call_id":"call_1","arguments":"{\"command\":\"pwd\"}"}]}}`,
		`[DONE]`,
	)

	resp, err := transport.GenerateStreamWithEvents(context.Background(), OpenAIRequest{ToolChoiceMode: ToolChoiceModeAutomatic, Model: "gpt-5"}, StreamCallbacks{})
	if err != nil {
		t.Fatalf("GenerateStream failed: %v", err)
	}

	if resp.AssistantText != "Done" {
		t.Fatalf("assistant text = %q, want Done", resp.AssistantText)
	}
	if resp.AssistantPhase != MessagePhaseFinal {
		t.Fatalf("assistant phase = %q, want %q", resp.AssistantPhase, MessagePhaseFinal)
	}
}

func TestGenerateStream_RepairsMissingAssistantOutputItemAtNonZeroOutputIndex(t *testing.T) {
	transport := newOpenAIStreamTestTransport(t,
		`{"type":"response.output_item.done","output_index":2,"item":{"id":"msg_2","type":"message","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"Done"}]}}`,
		`{"type":"response.completed","response":{"usage":{"input_tokens":2,"output_tokens":3,"total_tokens":5},"output":[{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"Plan"}],"encrypted_content":"enc_1"},{"type":"function_call","id":"fc_1","name":"shell","call_id":"call_1","arguments":"{\"command\":\"pwd\"}"}]}}`,
		`[DONE]`,
	)

	resp, err := transport.GenerateStreamWithEvents(context.Background(), OpenAIRequest{ToolChoiceMode: ToolChoiceModeAutomatic, Model: "gpt-5"}, StreamCallbacks{})
	if err != nil {
		t.Fatalf("GenerateStream failed: %v", err)
	}

	if len(resp.OutputItems) != 3 {
		t.Fatalf("expected repaired output items, got %+v", resp.OutputItems)
	}
	if resp.OutputItems[0].Type != ResponseItemTypeReasoning || resp.OutputItems[0].OutputIndex != 0 {
		t.Fatalf("expected reasoning item to stay first, got %+v", resp.OutputItems[0])
	}
	if resp.OutputItems[1].Type != ResponseItemTypeFunctionCall || resp.OutputItems[1].OutputIndex != 1 {
		t.Fatalf("expected tool call to stay second, got %+v", resp.OutputItems[1])
	}
	if resp.OutputItems[2].Type != ResponseItemTypeMessage || resp.OutputItems[2].OutputIndex != 2 || resp.OutputItems[2].Content != "Done" {
		t.Fatalf("expected synthesized assistant item inserted at output_index=2, got %+v", resp.OutputItems[2])
	}
}

func TestGenerateStream_PreservesHostedWebSearchOutputItemFromStream(t *testing.T) {
	transport := newOpenAIStreamTestTransport(t,
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"web_search_call","id":"ws_1","status":"completed","action":{"type":"search","query":"kent cli"}}}`,
		`{"type":"response.output_item.added","output_index":1,"item":{"id":"msg_1","type":"message","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"Done"}]}}`,
		`{"type":"response.completed","response":{"usage":{"input_tokens":2,"output_tokens":3,"total_tokens":5},"output":[{"id":"msg_1","type":"message","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"Done"}]}]}}`,
		`[DONE]`,
	)

	resp, err := transport.GenerateStreamWithEvents(context.Background(), OpenAIRequest{ToolChoiceMode: ToolChoiceModeAutomatic, Model: "gpt-5"}, StreamCallbacks{})
	if err != nil {
		t.Fatalf("GenerateStream failed: %v", err)
	}
	if resp.AssistantText != "Done" {
		t.Fatalf("assistant text = %q, want Done", resp.AssistantText)
	}
	if len(resp.OutputItems) != 2 {
		t.Fatalf("expected hosted passthrough output item + assistant message, got %+v", resp.OutputItems)
	}
	foundAssistant := false
	foundHosted := false
	for _, item := range resp.OutputItems {
		if item.Type == ResponseItemTypeMessage && item.Content == "Done" {
			foundAssistant = true
		}
		if item.Type != ResponseItemTypeOther {
			continue
		}
		if !strings.Contains(string(item.Raw), "\"type\":\"web_search_call\"") {
			t.Fatalf("unexpected passthrough raw item: %+v", item)
		}
		foundHosted = true
	}
	if !foundHosted {
		t.Fatalf("expected passthrough web_search_call in output items, got %+v", resp.OutputItems)
	}
	if !foundAssistant {
		t.Fatalf("expected assistant message in output items, got %+v", resp.OutputItems)
	}
}
