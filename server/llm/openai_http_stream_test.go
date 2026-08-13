package llm

import (
	"context"
	"core/internal/testharness/pty"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"core/shared/textutil"
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

func TestGenerateStream_RejectsMalformedReasoningCoordinates(t *testing.T) {
	tests := []struct {
		name  string
		event string
	}{
		{
			name:  "negative output index",
			event: `{"type":"response.reasoning_summary_text.delta","output_index":-1,"summary_index":0,"item_id":"reason_1","delta":"plan"}`,
		},
		{
			name:  "missing output index",
			event: `{"type":"response.reasoning_summary_text.delta","summary_index":0,"item_id":"reason_1","delta":"plan"}`,
		},
		{
			name:  "missing summary index",
			event: `{"type":"response.reasoning_summary_text.delta","output_index":0,"item_id":"reason_1","delta":"plan"}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			transport := newOpenAIStreamTestTransport(t,
				test.event,
				`{"type":"response.completed","response":{"output":[]}}`,
				`[DONE]`,
			)
			_, err := transport.GenerateStreamWithEvents(context.Background(), OpenAIRequest{ToolChoiceMode: ToolChoiceModeAutomatic, Model: "gpt-5"}, StreamCallbacks{})
			if err == nil {
				t.Fatal("malformed reasoning coordinate was accepted")
			}
			var providerErr *ProviderAPIError
			if !errors.As(err, &providerErr) {
				t.Fatalf("error = %T %v, want ProviderAPIError", err, err)
			}
			if providerErr.Code != UnifiedErrorCodeProviderContract {
				t.Fatalf("provider error code = %q, want provider contract", providerErr.Code)
			}
		})
	}
}

func TestGenerateStream_RejectsConflictingReasoningIdentities(t *testing.T) {
	tests := []struct {
		name   string
		events []string
	}{
		{
			name: "one identity aliases two coordinates",
			events: []string{
				`{"type":"response.reasoning_summary_text.delta","output_index":0,"summary_index":0,"item_id":"reason_1","delta":"first"}`,
				`{"type":"response.reasoning_summary_text.delta","output_index":1,"summary_index":0,"item_id":"reason_1","delta":"second"}`,
			},
		},
		{
			name: "one coordinate receives two identities",
			events: []string{
				`{"type":"response.reasoning_summary_text.delta","output_index":0,"summary_index":0,"item_id":"reason_1","delta":"first"}`,
				`{"type":"response.reasoning_summary_text.done","output_index":0,"summary_index":0,"item_id":"reason_2","text":"second"}`,
			},
		},
		{
			name: "completed identity conflicts with streamed identity",
			events: []string{
				`{"type":"response.reasoning_summary_text.delta","output_index":0,"summary_index":0,"item_id":"reason_1","delta":"streamed"}`,
				`{"type":"response.completed","response":{"output":[{"type":"reasoning","id":"reason_2","summary":[{"type":"summary_text","text":"completed"}]}]}}`,
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			events := append([]string(nil), test.events...)
			if test.name != "completed identity conflicts with streamed identity" {
				events = append(events, `{"type":"response.completed","response":{"output":[]}}`)
			}
			events = append(events, `[DONE]`)
			transport := newOpenAIStreamTestTransport(t, events...)
			_, err := transport.GenerateStreamWithEvents(context.Background(), OpenAIRequest{ToolChoiceMode: ToolChoiceModeAutomatic, Model: "gpt-5"}, StreamCallbacks{})
			if err == nil {
				t.Fatal("conflicting reasoning identity was accepted")
			}
			var providerErr *ProviderAPIError
			if !errors.As(err, &providerErr) {
				t.Fatalf("error = %T %v, want ProviderAPIError", err, err)
			}
			if providerErr.Code != UnifiedErrorCodeProviderContract {
				t.Fatalf("provider error code = %q, want provider contract", providerErr.Code)
			}
		})
	}
}

func TestGenerateStreamPreservesDistinctReasoningTracesWithSameText(t *testing.T) {
	transport := newOpenAIStreamTestTransport(t,
		`{"type":"response.reasoning_summary_text.delta","output_index":0,"summary_index":0,"item_id":"reason_1","delta":"same"}`,
		`{"type":"response.reasoning_summary_text.delta","output_index":1,"summary_index":0,"item_id":"reason_2","delta":"same"}`,
		`{"type":"response.completed","response":{"output":[]}}`,
		`[DONE]`,
	)
	resp, err := transport.GenerateStreamWithEvents(context.Background(), OpenAIRequest{ToolChoiceMode: ToolChoiceModeAutomatic, Model: "gpt-5"}, StreamCallbacks{})
	if err != nil {
		t.Fatalf("GenerateStream failed: %v", err)
	}
	if len(resp.Reasoning) != 2 {
		t.Fatalf("reasoning traces = %+v, want two distinct traces", resp.Reasoning)
	}
	for index, entry := range resp.Reasoning {
		if entry.Text != "same" || entry.SourceCoordinate == nil ||
			entry.SourceCoordinate.OutputIndex == nil ||
			*entry.SourceCoordinate.OutputIndex != int64(index) {
			t.Fatalf("reasoning trace %d = %+v, want coordinate output=%d", index, entry, index)
		}
	}
}

func TestGenerateStreamCompletedReasoningTextOverridesStreamedPartial(t *testing.T) {
	transport := newOpenAIStreamTestTransport(t,
		`{"type":"response.reasoning_summary_text.delta","output_index":0,"summary_index":0,"item_id":"reason_1","delta":"streamed partial"}`,
		`{"type":"response.completed","response":{"output":[{"type":"reasoning","id":"reason_1","summary":[{"type":"summary_text","text":"completed final"}]}]}}`,
		`[DONE]`,
	)
	resp, err := transport.GenerateStreamWithEvents(context.Background(), OpenAIRequest{ToolChoiceMode: ToolChoiceModeAutomatic, Model: "gpt-5"}, StreamCallbacks{})
	if err != nil {
		t.Fatalf("GenerateStream failed: %v", err)
	}
	if len(resp.Reasoning) != 1 {
		t.Fatalf("reasoning traces = %+v, want one trace", resp.Reasoning)
	}
	if resp.Reasoning[0].Text != "completed final" {
		t.Fatalf("reasoning text = %q, want completed final", resp.Reasoning[0].Text)
	}
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

	if optionalStringValue(resp.AssistantText) != "Hello" {
		t.Fatalf("assistant text = %q, want Hello", optionalStringValue(resp.AssistantText))
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

	if optionalStringValue(resp.AssistantText) != "Done" {
		t.Fatalf("assistant text = %q, want Done", optionalStringValue(resp.AssistantText))
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
	transport.Client = &http.Client{Transport: pty.RoundTripFunc(func(*http.Request) (*http.Response, error) {
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
	if optionalStringValue(resp.AssistantText) != "Hello" {
		t.Fatalf("unexpected assistant text: %q", optionalStringValue(resp.AssistantText))
	}
	if !resp.ProviderPhase.Is(MessagePhaseCommentary) {
		t.Fatalf("unexpected provider phase: %#v", resp.ProviderPhase)
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
	if resp.Usage.CachedInputTokens == nil || *resp.Usage.CachedInputTokens != 4 {
		t.Fatalf("unexpected cached usage details: %+v", resp.Usage)
	}
	if len(resp.Reasoning) != 1 || resp.Reasoning[0].Role == nil || *resp.Reasoning[0].Role != "reasoning" || resp.Reasoning[0].Text != "Plan" {
		t.Fatalf("unexpected reasoning summary entries: %+v", resp.Reasoning)
	}
	if len(resp.ReasoningItems) != 1 || resp.ReasoningItems[0].ID != "rs_1" || resp.ReasoningItems[0].EncryptedContent != "enc_1" {
		t.Fatalf("unexpected reasoning items: %+v", resp.ReasoningItems)
	}
	if len(reasoning) != 1 || reasoning[0].SourceCoordinate == nil ||
		reasoning[0].SourceCoordinate.OutputIndex == nil ||
		reasoning[0].SourceCoordinate.PartIndex == nil ||
		reasoning[0].Role != "reasoning" || reasoning[0].Text != "Plan" {
		t.Fatalf("unexpected reasoning delta callbacks: %+v", reasoning)
	}
}

func TestGenerateStream_PreservesWhitespaceOnlyFunctionArgumentDelta(t *testing.T) {
	const inputPrefix = `{"session_id":1000,"chars":"`
	const inputSuffix = `","yield_time_ms":9223372036854775807}`
	transport := newOpenAIStreamTestTransport(t,
		`{"type":"response.output_item.added","item":{"id":"fc_1","type":"function_call","name":"write_stdin","call_id":"call_1","arguments":""}}`,
		fmt.Sprintf(`{"type":"response.function_call_arguments.delta","item_id":"fc_1","delta":%q}`, inputPrefix),
		fmt.Sprintf(`{"type":"response.function_call_arguments.delta","item_id":"fc_1","delta":%q}`, " "),
		fmt.Sprintf(`{"type":"response.function_call_arguments.delta","item_id":"fc_1","delta":%q}`, inputSuffix),
		`{"type":"response.completed","response":{"output":[]}}`,
		`[DONE]`,
	)

	resp, err := transport.GenerateStreamWithEvents(context.Background(), OpenAIRequest{ToolChoiceMode: ToolChoiceModeAutomatic, Model: "gpt-5"}, StreamCallbacks{})
	if err != nil {
		t.Fatalf("GenerateStream failed: %v", err)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("expected one tool call, got %+v", resp.ToolCalls)
	}
	var decodedInput struct {
		Chars       string `json:"chars"`
		YieldTimeMS int    `json:"yield_time_ms"`
	}
	if err := json.Unmarshal(resp.ToolCalls[0].Input, &decodedInput); err != nil {
		t.Fatalf("decode streamed write_stdin input %q: %v", resp.ToolCalls[0].Input, err)
	}
	if decodedInput.Chars != " " || decodedInput.YieldTimeMS != math.MaxInt {
		t.Fatalf("streamed write_stdin input = %+v", decodedInput)
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
	if !resp.ProviderPhase.Is(MessagePhaseFinal) {
		t.Fatalf("unexpected final provider phase: %#v", resp.ProviderPhase)
	}
}

func TestGenerateStreamRejectsUnmatchedAssistantDelta(t *testing.T) {
	transport := newOpenAIStreamTestTransport(t,
		`{"type":"response.output_text.delta","delta":"stray"}`,
		`{"type":"response.output_item.added","output_index":1,"item":{"id":"msg_1","type":"message","role":"assistant","phase":"final_answer","content":[]}}`,
		`{"type":"response.output_text.delta","output_index":1,"delta":"done"}`,
		`{"type":"response.completed","response":{"output":[{"id":"msg_1","type":"message","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"done"}]}]}}`,
		`[DONE]`,
	)

	var deltas []AssistantDelta
	_, err := transport.GenerateStreamWithEvents(context.Background(), OpenAIRequest{
		Model:          "gpt-5",
		ToolChoiceMode: ToolChoiceModeAutomatic,
	}, StreamCallbacks{
		OnAssistantDelta: func(delta AssistantDelta) {
			deltas = append(deltas, delta)
		},
	})
	if err == nil {
		t.Fatalf("unmatched assistant delta was accepted; emitted deltas = %+v", deltas)
	}
	var providerErr *ProviderAPIError
	if !errors.As(err, &providerErr) {
		t.Fatalf("error = %T %v, want ProviderAPIError", err, err)
	}
	if providerErr.Code != UnifiedErrorCodeProviderContract {
		t.Fatalf("provider error code = %q, want %q", providerErr.Code, UnifiedErrorCodeProviderContract)
	}
	if got := joinedAssistantDeltas(deltas); got != "straydone" {
		t.Fatalf("emitted assistant deltas = %q, want all provider bytes", got)
	}
}

func TestGenerateStream_RejectsCompletedMessageThatConflictsWithDisplayedDeltas(t *testing.T) {
	transport := newOpenAIStreamTestTransport(t,
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"message","role":"assistant","phase":"final_answer","content":[]}}`,
		`{"type":"response.output_text.delta","output_index":0,"delta":"Hello"}`,
		`{"type":"response.output_text.delta","output_index":0,"delta":"!"}`,
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
	if err == nil {
		t.Fatalf("GenerateStream response = %+v, want provider contract error", resp)
	}

	const streamed = "Hello!"
	if got := joinedAssistantDeltas(deltas); got != streamed {
		t.Fatalf("streamed deltas = %q, want %q", got, streamed)
	}
	var providerErr *ProviderAPIError
	if !errors.As(err, &providerErr) {
		t.Fatalf("error = %T %v, want ProviderAPIError", err, err)
	}
	if providerErr.Code != UnifiedErrorCodeProviderContract {
		t.Fatalf("provider code = %q, want %q", providerErr.Code, UnifiedErrorCodeProviderContract)
	}
}

func TestGenerateStream_OmitsNonFinalEmptyAssistantContentBeforeToolCall(t *testing.T) {
	transport := newOpenAIStreamTestTransport(t,
		`{"type":"response.output_item.added","output_index":1,"item":{"id":"msg_1","type":"message","role":"assistant","content":[]}}`,
		`{"type":"response.output_text.delta","item_id":"msg_1","output_index":1,"content_index":0,"delta":"\n\n"}`,
		`{"type":"response.output_text.done","item_id":"msg_1","output_index":1,"content_index":0,"text":""}`,
		`{"type":"response.output_item.done","output_index":1,"item":{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"output_text","text":""}]}}`,
		`{"type":"response.output_item.added","output_index":2,"item":{"id":"fc_1","type":"function_call","call_id":"call_1","name":"shell","arguments":""}}`,
		`{"type":"response.function_call_arguments.delta","item_id":"fc_1","output_index":2,"delta":"{\"command\":\"pwd\"}"}`,
		`{"type":"response.function_call_arguments.done","item_id":"fc_1","output_index":2,"arguments":"{\"command\":\"pwd\"}"}`,
		`{"type":"response.output_item.done","output_index":2,"item":{"id":"fc_1","type":"function_call","call_id":"call_1","name":"shell","arguments":"{\"command\":\"pwd\"}"}}`,
		`{"type":"response.completed","response":{"output":[{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"output_text","text":""}]},{"id":"fc_1","type":"function_call","call_id":"call_1","name":"shell","arguments":"{\"command\":\"pwd\"}"}]}}`,
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
	if len(deltas) != 0 {
		t.Fatalf("assistant deltas = %+v, want no semantic assistant output", deltas)
	}
	if resp.AssistantText != nil {
		t.Fatalf("assistant text = %#v, want omitted non-final content", resp.AssistantText)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].ID != "call_1" || resp.ToolCalls[0].Name != "shell" {
		t.Fatalf("tool calls = %+v, want shell call_1", resp.ToolCalls)
	}
}

func TestGenerateStream_UsesFinalizedOutputTextWhenProviderOmitsDeltas(t *testing.T) {
	transport := newOpenAIStreamTestTransport(t,
		`{"type":"response.output_text.done","item_id":"msg_1","output_index":0,"content_index":0,"text":"Compaction summary"}`,
		`{"type":"response.completed","response":{"output":[]}}`,
		`[DONE]`,
	)

	resp, err := transport.GenerateStreamWithEvents(context.Background(), OpenAIRequest{ToolChoiceMode: ToolChoiceModeAutomatic, Model: "gpt-5"}, StreamCallbacks{})
	if err != nil {
		t.Fatalf("GenerateStream failed: %v", err)
	}
	if optionalStringValue(resp.AssistantText) != "Compaction summary" {
		t.Fatalf("assistant text = %q, want finalized output text", optionalStringValue(resp.AssistantText))
	}
	if len(resp.OutputItems) != 1 || resp.OutputItems[0].Content == nil || *resp.OutputItems[0].Content != "Compaction summary" {
		t.Fatalf("output items = %+v, want synthesized finalized assistant output", resp.OutputItems)
	}
}

func TestGenerateStream_DeliversTrailingWhitespaceBeforeToolCallWithoutBuffering(t *testing.T) {
	transport := newOpenAIStreamTestTransport(t,
		`{"type":"response.output_item.added","output_index":1,"item":{"id":"msg_1","type":"message","role":"assistant","phase":"commentary","content":[]}}`,
		`{"type":"response.output_text.delta","item_id":"msg_1","output_index":1,"content_index":0,"delta":"I will run it.\n\n"}`,
		`{"type":"response.output_item.done","output_index":1,"item":{"id":"msg_1","type":"message","role":"assistant","phase":"commentary","content":[{"type":"output_text","text":"I will run it.\n\n"}]}}`,
		`{"type":"response.output_item.added","output_index":2,"item":{"id":"fc_1","type":"function_call","call_id":"call_1","name":"shell","arguments":""}}`,
		`{"type":"response.function_call_arguments.delta","item_id":"fc_1","output_index":2,"delta":"{\"command\":\"pwd\"}"}`,
		`{"type":"response.function_call_arguments.done","item_id":"fc_1","output_index":2,"arguments":"{\"command\":\"pwd\"}"}`,
		`{"type":"response.output_item.done","output_index":2,"item":{"id":"fc_1","type":"function_call","call_id":"call_1","name":"shell","arguments":"{\"command\":\"pwd\"}"}}`,
		`{"type":"response.completed","response":{"output":[{"id":"msg_1","type":"message","role":"assistant","phase":"commentary","content":[{"type":"output_text","text":"I will run it."}]},{"id":"fc_1","type":"function_call","call_id":"call_1","name":"shell","arguments":"{\"command\":\"pwd\"}"}]}}`,
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
	if got := joinedAssistantDeltas(deltas); got != "I will run it.\n\n" {
		t.Fatalf("assistant deltas = %q, want exact streamed content", got)
	}
	if optionalStringValue(resp.AssistantText) != "I will run it.\n\n" {
		t.Fatalf("assistant text = %q, want exact streamed content", optionalStringValue(resp.AssistantText))
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].ID != "call_1" || resp.ToolCalls[0].Name != "shell" {
		t.Fatalf("tool calls = %+v, want shell call_1", resp.ToolCalls)
	}
}

func TestGenerateStream_IgnoresStructuredTrailingWhitespaceShimWithoutDeltaConsumer(t *testing.T) {
	transport := newOpenAIStreamTestTransport(t,
		`{"type":"response.output_item.added","output_index":0,"item":{"id":"msg_1","type":"message","role":"assistant","phase":"final_answer","content":[]}}`,
		`{"type":"response.output_text.delta","item_id":"msg_1","output_index":0,"content_index":0,"delta":"Hello\n\n"}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"id":"msg_1","type":"message","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"Hello\n\n"}]}}`,
		`{"type":"response.completed","response":{"output":[{"id":"msg_1","type":"message","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"Hello"}]}]}}`,
		`[DONE]`,
	)

	resp, err := transport.GenerateStreamWithEvents(context.Background(), OpenAIRequest{ToolChoiceMode: ToolChoiceModeAutomatic, Model: "gpt-5"}, StreamCallbacks{})
	if err != nil {
		t.Fatalf("GenerateStream failed: %v", err)
	}
	if optionalStringValue(resp.AssistantText) != "Hello" {
		t.Fatalf("assistant text = %q, want finalized content", optionalStringValue(resp.AssistantText))
	}
}

func TestGenerateStream_IgnoresLeadingWhitespaceAssistantShimBeforeContent(t *testing.T) {
	transport := newOpenAIStreamTestTransport(t,
		`{"type":"response.output_item.added","output_index":0,"item":{"id":"msg_1","type":"message","role":"assistant","phase":"final_answer","content":[]}}`,
		`{"type":"response.output_text.delta","item_id":"msg_1","output_index":0,"content_index":0,"delta":"\n\nHello"}`,
		`{"type":"response.output_text.delta","item_id":"msg_1","output_index":0,"content_index":0,"delta":" world"}`,
		`{"type":"response.output_text.done","item_id":"msg_1","output_index":0,"content_index":0,"text":"\n\nHello world"}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"id":"msg_1","type":"message","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"\n\nHello world"}]}}`,
		`{"type":"response.completed","response":{"output":[{"id":"msg_1","type":"message","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"\n\nHello world"}]}]}}`,
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
	if got := joinedAssistantDeltas(deltas); got != "Hello world" {
		t.Fatalf("assistant deltas = %q, want finalized content", got)
	}
	if optionalStringValue(resp.AssistantText) != "Hello world" {
		t.Fatalf("assistant text = %q, want finalized content", optionalStringValue(resp.AssistantText))
	}
}

func TestGenerateStream_PreservesResumedOutputWhitespaceAfterInterleavedOutput(t *testing.T) {
	transport := newOpenAIStreamTestTransport(t,
		`{"type":"response.output_text.delta","output_index":0,"delta":"\nfirst"}`,
		`{"type":"response.output_text.delta","output_index":1,"delta":"\nsecond"}`,
		`{"type":"response.output_text.delta","output_index":0,"delta":" continuation"}`,
		`{"type":"response.completed","response":{"output":[]}}`,
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
	if got := joinedAssistantDeltas(deltas); got != "firstsecond continuation" {
		t.Fatalf("assistant deltas = %q, want resumed output whitespace preserved", got)
	}
	if optionalStringValue(resp.AssistantText) != "firstsecond continuation" {
		t.Fatalf("assistant text = %q, want all interleaved unphased output", optionalStringValue(resp.AssistantText))
	}
}

func TestGenerateStream_PreservesPendingOutputIndexAfterFinalizedOutput(t *testing.T) {
	transport := newOpenAIStreamTestTransport(t,
		`{"type":"response.output_text.delta","output_index":0,"delta":"first"}`,
		`{"type":"response.output_text.delta","output_index":1,"delta":"second"}`,
		`{"type":"response.output_text.done","output_index":0,"text":"first"}`,
		`{"type":"response.completed","response":{"output":[]}}`,
		`[DONE]`,
	)

	resp, err := transport.GenerateStreamWithEvents(context.Background(), OpenAIRequest{ToolChoiceMode: ToolChoiceModeAutomatic, Model: "gpt-5"}, StreamCallbacks{})
	if err != nil {
		t.Fatalf("GenerateStream failed: %v", err)
	}
	if optionalStringValue(resp.AssistantText) != "firstsecond" {
		t.Fatalf("assistant text = %q, want finalized and pending output", optionalStringValue(resp.AssistantText))
	}
	if !resp.ProviderPhase.IsAbsent() {
		t.Fatalf("provider phase = %v, want structurally absent for unphased fallback", resp.ProviderPhase.Value())
	}
}

func TestGenerateStreamAcceptsCompletedEmptyFinalAfterDoneOnlyStream(t *testing.T) {
	transport := newOpenAIStreamTestTransport(t,
		`{"type":"response.output_text.done","output_index":0,"text":""}`,
		`{"type":"response.completed","response":{"output":[{"type":"message","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":""}]}]}}`,
		`[DONE]`,
	)

	resp, err := NewOpenAIClient(transport).GenerateStreamWithEvents(context.Background(), Request{
		Model:          "gpt-5",
		ToolChoiceMode: ToolChoiceModeAutomatic,
	}, StreamCallbacks{})
	if err != nil {
		t.Fatalf("generate stream: %v", err)
	}
	if resp.Assistant.Content == nil || *resp.Assistant.Content != "" {
		t.Fatalf("assistant content = %#v, want present empty content", resp.Assistant.Content)
	}
	if resp.Assistant.Phase == nil || *resp.Assistant.Phase != MessagePhaseFinal {
		t.Fatalf("assistant phase = %#v, want final", resp.Assistant.Phase)
	}
}

func TestGenerateStream_PreservesWhitespaceBetweenAssistantContent(t *testing.T) {
	transport := newOpenAIStreamTestTransport(t,
		`{"type":"response.output_item.added","output_index":0,"item":{"id":"msg_1","type":"message","role":"assistant","phase":"final_answer","content":[]}}`,
		`{"type":"response.output_text.delta","item_id":"msg_1","output_index":0,"content_index":0,"delta":"Hello "}`,
		`{"type":"response.output_text.delta","item_id":"msg_1","output_index":0,"content_index":0,"delta":"world"}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"id":"msg_1","type":"message","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"Hello world"}]}}`,
		`{"type":"response.completed","response":{"output":[{"id":"msg_1","type":"message","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"Hello world"}]}]}}`,
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
	if got := joinedAssistantDeltas(deltas); got != "Hello world" {
		t.Fatalf("assistant deltas = %q, want preserved interstitial whitespace", got)
	}
	if optionalStringValue(resp.AssistantText) != "Hello world" {
		t.Fatalf("assistant text = %q, want preserved interstitial whitespace", optionalStringValue(resp.AssistantText))
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

	if optionalStringValue(resp.AssistantText) != "AB" {
		t.Fatalf("assistant text = %q, want aggregate completed text", optionalStringValue(resp.AssistantText))
	}
	if len(resp.OutputItems) != 2 ||
		resp.OutputItems[0].Content == nil || *resp.OutputItems[0].Content != "A" ||
		resp.OutputItems[1].Content == nil || *resp.OutputItems[1].Content != "B" {
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

func TestGenerateStream_MapsProviderOverloadCodeWithoutMessageMatching(t *testing.T) {
	transport := newOpenAIStreamTestTransport(t,
		`{"type":"error","error":{"type":"server_error","code":"server_is_overloaded","param":"request","message":"not the overload wording"}}`,
		`[DONE]`,
	)
	_, err := transport.GenerateStreamWithEvents(context.Background(), OpenAIRequest{ToolChoiceMode: ToolChoiceModeAutomatic, Model: "gpt-5"}, StreamCallbacks{})
	var providerErr *ProviderAPIError
	if err == nil || !errors.As(err, &providerErr) {
		t.Fatalf("expected ProviderAPIError, got %T", err)
	}
	if providerErr.Code != UnifiedErrorCodeProviderOverload || providerErr.ProviderCode != "server_is_overloaded" || providerErr.StatusCode != http.StatusOK {
		t.Fatalf("unexpected provider overload: %+v", providerErr)
	}
	if got := classifyOpenAIUnifiedErrorCode(http.StatusUnauthorized, "server_is_overloaded"); got != UnifiedErrorCodeAuthentication || classifyOpenAIUnifiedErrorCode(http.StatusOK, "SERVER_IS_OVERLOADED") != UnifiedErrorCodeUnknown {
		t.Fatalf("terminal HTTP status classification = %q, want %q", got, UnifiedErrorCodeAuthentication)
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
	if !resp.ToolCalls[0].Custom || resp.ToolCalls[0].CustomInput == nil || *resp.ToolCalls[0].CustomInput != patchInput {
		t.Fatalf("unexpected custom patch tool call: %+v", resp.ToolCalls[0])
	}
	if len(resp.OutputItems) != 1 || resp.OutputItems[0].Type != ResponseItemTypeCustomToolCall || resp.OutputItems[0].CustomInput == nil || *resp.OutputItems[0].CustomInput != patchInput {
		t.Fatalf("unexpected custom output item: %+v", resp.OutputItems)
	}
}

func TestToolCallAccumulatorMergesCompletedCustomInputWithoutJSONInput(t *testing.T) {
	accumulator := newToolCallAccumulator()
	accumulator.Merge([]ToolCall{{ID: "call-1", Name: "patch", Custom: true, CustomInput: textutil.Value("partial")}})
	accumulator.Merge([]ToolCall{{ID: "call-1", Name: "patch", Custom: true, CustomInput: textutil.Value("complete")}})

	calls := accumulator.ToToolCalls()
	if len(calls) != 1 {
		t.Fatalf("expected one call, got %+v", calls)
	}
	if !calls[0].Custom || calls[0].CustomInput == nil || *calls[0].CustomInput != "complete" {
		t.Fatalf("expected completed custom input to replace partial input, got %+v", calls[0])
	}
}

func TestGenerateStream_CarriesReasoningStatusWithoutChangingSummaryText(t *testing.T) {
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
	if reasoning[0].CurrentStatus == nil || reasoning[0].CurrentStatus.Text != "Preparing patch" {
		t.Fatalf("unexpected current status: %+v", reasoning[0].CurrentStatus)
	}
	if len(resp.Reasoning) != 1 || resp.Reasoning[0].Text != "**Preparing patch**\n\nPlain summary text" {
		t.Fatalf("unexpected final reasoning summary entries: %+v", resp.Reasoning)
	}
}

func TestGenerateStream_ReasoningCallbacksCarryCurrentAccumulatedStatus(t *testing.T) {
	transport := newOpenAIStreamTestTransport(t,
		`{"type":"response.reasoning_summary_text.delta","item_id":"rs_1","output_index":0,"summary_index":0,"delta":"**Checking"}`,
		`{"type":"response.reasoning_summary_text.delta","item_id":"rs_1","output_index":0,"summary_index":0,"delta":" tests**"}`,
		`{"type":"response.reasoning_summary_text.delta","item_id":"rs_1","output_index":0,"summary_index":0,"delta":"\nDetails"}`,
		`{"type":"response.completed","response":{"output":[{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"**Checking tests**\nDetails"}],"encrypted_content":"enc_1"}]}}`,
		`[DONE]`,
	)

	var reasoning []ReasoningSummaryDelta
	_, err := transport.GenerateStreamWithEvents(context.Background(), OpenAIRequest{ToolChoiceMode: ToolChoiceModeAutomatic, Model: "gpt-5"}, StreamCallbacks{
		OnReasoningSummaryDelta: func(delta ReasoningSummaryDelta) {
			reasoning = append(reasoning, delta)
		},
	})
	if err != nil {
		t.Fatalf("GenerateStream failed: %v", err)
	}

	if len(reasoning) != 3 {
		t.Fatalf("expected 3 reasoning callbacks, got %+v", reasoning)
	}
	if reasoning[0].CurrentStatus != nil {
		t.Fatalf("incomplete callback status = %+v, want absence", reasoning[0].CurrentStatus)
	}
	for index := 1; index < len(reasoning); index++ {
		if reasoning[index].CurrentStatus == nil || reasoning[index].CurrentStatus.Text != "Checking tests" {
			t.Fatalf("callback %d status = %+v, want Checking tests", index, reasoning[index].CurrentStatus)
		}
	}
	if reasoning[2].Text != "**Checking tests**\nDetails" {
		t.Fatalf("final callback text = %q", reasoning[2].Text)
	}
}

func TestGenerateStream_ReasoningCallbackNilStatusMeansCurrentAbsence(t *testing.T) {
	transport := newOpenAIStreamTestTransport(t,
		`{"type":"response.reasoning_summary_text.delta","item_id":"rs_1","output_index":0,"summary_index":0,"delta":"**Checking tests**"}`,
		`{"type":"response.reasoning_summary_text.done","item_id":"rs_1","output_index":0,"summary_index":0,"text":"Plain summary"}`,
		`{"type":"response.completed","response":{"output":[{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"Plain summary"}],"encrypted_content":"enc_1"}]}}`,
		`[DONE]`,
	)

	var reasoning []ReasoningSummaryDelta
	_, err := transport.GenerateStreamWithEvents(context.Background(), OpenAIRequest{ToolChoiceMode: ToolChoiceModeAutomatic, Model: "gpt-5"}, StreamCallbacks{
		OnReasoningSummaryDelta: func(delta ReasoningSummaryDelta) {
			reasoning = append(reasoning, delta)
		},
	})
	if err != nil {
		t.Fatalf("GenerateStream failed: %v", err)
	}

	if len(reasoning) != 2 {
		t.Fatalf("expected 2 reasoning callbacks, got %+v", reasoning)
	}
	if reasoning[0].CurrentStatus == nil || reasoning[0].CurrentStatus.Text != "Checking tests" {
		t.Fatalf("first callback status = %+v, want Checking tests", reasoning[0].CurrentStatus)
	}
	if reasoning[1].CurrentStatus != nil {
		t.Fatalf("replacement callback status = %+v, want current absence", reasoning[1].CurrentStatus)
	}
}

func TestGenerateStream_EmptyReasoningSnapshotClearsCurrentStatus(t *testing.T) {
	transport := newOpenAIStreamTestTransport(t,
		`{"type":"response.reasoning_summary_text.delta","item_id":"rs_1","output_index":0,"summary_index":0,"delta":"**Checking tests**"}`,
		`{"type":"response.reasoning_summary_text.done","item_id":"rs_1","output_index":0,"summary_index":0,"text":""}`,
		`{"type":"response.completed","response":{"output":[]}}`,
		`[DONE]`,
	)

	var reasoning []ReasoningSummaryDelta
	_, err := transport.GenerateStreamWithEvents(context.Background(), OpenAIRequest{ToolChoiceMode: ToolChoiceModeAutomatic, Model: "gpt-5"}, StreamCallbacks{
		OnReasoningSummaryDelta: func(delta ReasoningSummaryDelta) {
			reasoning = append(reasoning, delta)
		},
	})
	if err != nil {
		t.Fatalf("GenerateStream failed: %v", err)
	}

	if len(reasoning) != 2 {
		t.Fatalf("expected 2 reasoning callbacks, got %+v", reasoning)
	}
	if reasoning[1].Text != "" {
		t.Fatalf("empty snapshot callback text = %q", reasoning[1].Text)
	}
	if reasoning[1].CurrentStatus != nil {
		t.Fatalf("empty snapshot callback status = %+v, want current absence", reasoning[1].CurrentStatus)
	}
}

func TestGenerateStream_RejectsEmptyCompletedMessageAfterAssistantDeltas(t *testing.T) {
	transport := newOpenAIStreamTestTransport(t,
		`{"type":"response.output_item.added","output_index":0,"item":{"id":"msg_1","type":"message","role":"assistant","phase":"commentary","content":[]}}`,
		`{"type":"response.output_text.delta","delta":"Hel"}`,
		`{"type":"response.output_text.delta","delta":"lo"}`,
		`{"type":"response.output_item.added","output_index":1,"item":{"id":"fc_1","type":"function_call","name":"shell","call_id":"call_1","arguments":"{\"command\":\"pwd\"}"}}`,
		`{"type":"response.completed","response":{"usage":{"input_tokens":3,"output_tokens":4,"total_tokens":7},"output":[{"id":"msg_1","type":"message","role":"assistant","content":[]},{"type":"function_call","id":"fc_1","name":"shell","call_id":"call_1","arguments":"{\"command\":\"pwd\"}"}]}}`,
		`[DONE]`,
	)

	resp, err := transport.GenerateStreamWithEvents(context.Background(), OpenAIRequest{ToolChoiceMode: ToolChoiceModeAutomatic, Model: "gpt-5"}, StreamCallbacks{})
	if err == nil {
		t.Fatalf("GenerateStream response = %+v, want provider contract error", resp)
	}
	var providerErr *ProviderAPIError
	if !errors.As(err, &providerErr) {
		t.Fatalf("error = %T %v, want ProviderAPIError", err, err)
	}
	if providerErr.Code != UnifiedErrorCodeProviderContract {
		t.Fatalf("provider code = %q, want %q", providerErr.Code, UnifiedErrorCodeProviderContract)
	}
}

func TestBuildOutputItemsFromStreamPreservesAbsentPhase(t *testing.T) {
	items := buildOutputItemsFromStream(textutil.Value("streamed text"), true, "", nil, nil, nil)
	if len(items) != 1 {
		t.Fatalf("output items = %+v, want one assistant message", items)
	}
	if items[0].Phase != nil {
		t.Fatalf("assistant output phase = %v, want absent", items[0].Phase)
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

	if optionalStringValue(resp.AssistantText) != "Done" {
		t.Fatalf("assistant text = %q, want Done", optionalStringValue(resp.AssistantText))
	}
	if !resp.ProviderPhase.Is(MessagePhaseFinal) {
		t.Fatalf("provider phase = %#v, want %q", resp.ProviderPhase, MessagePhaseFinal)
	}
	if len(resp.OutputItems) != 1 {
		t.Fatalf("expected 1 output item, got %+v", resp.OutputItems)
	}
	if resp.OutputItems[0].Phase == nil || *resp.OutputItems[0].Phase != MessagePhaseFinal {
		t.Fatalf("assistant output phase = %v, want %q", resp.OutputItems[0].Phase, MessagePhaseFinal)
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

	if optionalStringValue(resp.AssistantText) != "Done" {
		t.Fatalf("assistant text = %q, want Done", optionalStringValue(resp.AssistantText))
	}
	if !resp.ProviderPhase.Is(MessagePhaseFinal) {
		t.Fatalf("provider phase = %#v, want %q", resp.ProviderPhase, MessagePhaseFinal)
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
	if resp.OutputItems[2].Type != ResponseItemTypeMessage || resp.OutputItems[2].OutputIndex != 2 || resp.OutputItems[2].Content == nil || *resp.OutputItems[2].Content != "Done" {
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
	if optionalStringValue(resp.AssistantText) != "Done" {
		t.Fatalf("assistant text = %q, want Done", optionalStringValue(resp.AssistantText))
	}
	if len(resp.OutputItems) != 2 {
		t.Fatalf("expected hosted passthrough output item + assistant message, got %+v", resp.OutputItems)
	}
	foundAssistant := false
	foundHosted := false
	for _, item := range resp.OutputItems {
		if item.Type == ResponseItemTypeMessage && item.Content != nil && *item.Content == "Done" {
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
