package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type responseMetadataMode bool

const (
	responseMetadataNonStreaming responseMetadataMode = false
	responseMetadataStreaming    responseMetadataMode = true
)

func TestOpenAIClientSelectsServedModelMetadata(t *testing.T) {
	tests := []struct {
		name           string
		mode           responseMetadataMode
		createdModel   string
		completedModel string
		headers        http.Header
		want           *string
	}{
		{
			name:           "non-streaming standard model wins",
			mode:           responseMetadataNonStreaming,
			completedModel: " standard-model ",
			headers:        http.Header{"Openai-Model": {"header-model"}, "X-Openai-Model": {"fallback-model"}},
			want:           stringPointer("standard-model"),
		},
		{
			name:    "non-streaming provider header priority",
			mode:    responseMetadataNonStreaming,
			headers: http.Header{"Openai-Model": {" ", " routed-model "}, "X-Openai-Model": {"fallback-model"}},
			want:    stringPointer("routed-model"),
		},
		{
			name:    "non-streaming provider header fallback",
			mode:    responseMetadataNonStreaming,
			headers: http.Header{"Openai-Model": {" ", "\t"}, "X-Openai-Model": {" fallback-model "}},
			want:    stringPointer("fallback-model"),
		},
		{
			name:           "streaming first standard model wins",
			mode:           responseMetadataStreaming,
			createdModel:   " created-model ",
			completedModel: "completed-model",
			headers:        http.Header{"Openai-Model": {"header-model"}},
			want:           stringPointer("created-model"),
		},
		{
			name:           "streaming completed standard model wins",
			mode:           responseMetadataStreaming,
			completedModel: " completed-model ",
			headers:        http.Header{"Openai-Model": {"header-model"}},
			want:           stringPointer("completed-model"),
		},
		{name: "missing metadata is absent", mode: responseMetadataStreaming},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var capturedModel string
			transport := newResponseMetadataTransport(t, test.headers, func(w http.ResponseWriter, request *http.Request) {
				capturedModel = decodeRequestedModel(t, request)
				writeSuccessfulMetadataResponse(t, w, test.mode, test.createdModel, test.completedModel)
			})
			request := Request{Model: "requested-model", ToolChoiceMode: ToolChoiceModeAutomatic}

			client := NewOpenAIClient(transport)
			response, err := client.Generate(context.Background(), request)
			if test.mode == responseMetadataStreaming {
				response, err = client.GenerateStreamWithEvents(context.Background(), request, StreamCallbacks{})
			}
			if err != nil {
				t.Fatalf("generate: %v", err)
			}
			if (response.ServedModel == nil) != (test.want == nil) ||
				response.ServedModel != nil && *response.ServedModel != *test.want {
				t.Fatalf("served model = %#v, want %#v", response.ServedModel, test.want)
			}
			if request.Model != "requested-model" || capturedModel != "requested-model" {
				t.Fatalf("requested model changed: request=%q wire=%q", request.Model, capturedModel)
			}
		})
	}
}

func TestOpenAIClientParsesStrictReasoningIncludedHeader(t *testing.T) {
	headers := []struct {
		name  string
		value *string
		want  bool
	}{
		{name: "explicit true", value: stringPointer(" true "), want: true},
		{name: "malformed", value: stringPointer("1")},
		{name: "absent"},
	}
	for _, mode := range []responseMetadataMode{responseMetadataNonStreaming, responseMetadataStreaming} {
		for _, header := range headers {
			modeName := "non-streaming"
			if mode == responseMetadataStreaming {
				modeName = "streaming"
			}
			t.Run(modeName+"/"+header.name, func(t *testing.T) {
				responseHeaders := make(http.Header)
				if header.value != nil {
					responseHeaders.Set("x-reasoning-included", *header.value)
				}
				transport := newResponseMetadataTransport(t, responseHeaders, func(w http.ResponseWriter, _ *http.Request) {
					writeSuccessfulMetadataResponse(t, w, mode, "", "")
				})

				request := Request{
					Model:          "requested-model",
					ToolChoiceMode: ToolChoiceModeAutomatic,
				}
				client := NewOpenAIClient(transport)
				response, err := client.Generate(context.Background(), request)
				if mode == responseMetadataStreaming {
					response, err = client.GenerateStreamWithEvents(context.Background(), request, StreamCallbacks{})
				}
				if err != nil {
					t.Fatalf("generate: %v", err)
				}
				if response.ReasoningIncluded != header.want {
					t.Fatalf("reasoning included = %v, want %v", response.ReasoningIncluded, header.want)
				}
			})
		}
	}
}

func TestOpenAIResponseErrorsPreserveHeaderDiagnostics(t *testing.T) {
	tests := []struct {
		name          string
		providerID    string
		statusCode    int
		body          string
		streamEvent   string
		wantCode      UnifiedErrorCode
		wantRetriable bool
	}{
		{name: "OpenAI-compatible reducer", providerID: "openai", statusCode: http.StatusUnauthorized, body: `{"error":{"type":"invalid_request_error","code":"invalid_api_key","message":"denied"}}`, wantCode: UnifiedErrorCodeAuthentication},
		{name: "ChatGPT reducer", providerID: "chatgpt-codex", statusCode: http.StatusUnauthorized, body: `{"detail":"denied"}`, wantCode: UnifiedErrorCodeAuthentication},
		{name: "normalized response failed fallback", streamEvent: `{"type":"response.failed","sequence_number":1,"response":{"id":"resp_1","created_at":1,"error":{"code":"context_length_exceeded","message":"too many tokens"}}}`, wantCode: UnifiedErrorCodeContextLengthOverflow, wantRetriable: true},
		{name: "normalized response incomplete fallback", streamEvent: `{"type":"response.incomplete","sequence_number":1,"response":{"id":"resp_1","created_at":1,"incomplete_details":{"reason":"max_output_tokens"},"output":[]}}`, wantCode: UnifiedErrorCodeUnknown},
		{name: "normalized structured error fallback", streamEvent: `{"type":"error","error":{"type":"server_error","code":"server_is_overloaded","param":"request","message":"overloaded"}}`, wantCode: UnifiedErrorCodeProviderOverload, wantRetriable: true},
		{name: "direct provider contract error", streamEvent: `{"type":"response.output_text.delta","output_index":0,"delta":"partial"}`, wantCode: UnifiedErrorCodeProviderContract},
	}
	diagnosticHeaders := http.Header{
		"X-Request-Id":                 {" ", " request-primary "},
		"X-Oai-Request-Id":             {"request-fallback"},
		"X-Openai-Authorization-Error": {" ", " token rejected "},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var err error
			if test.streamEvent == "" {
				rawResp := &http.Response{
					StatusCode: test.statusCode,
					Header:     diagnosticHeaders.Clone(),
					Body:       io.NopCloser(strings.NewReader(test.body)),
				}
				err = newOpenAIRequestErrorMapper(test.providerID).Map(nil, rawResp, "request failed")
			} else {
				transport := newResponseMetadataTransport(t, diagnosticHeaders, func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = fmt.Fprintf(w, "data: %s\n\n", test.streamEvent)
					if test.wantCode != UnifiedErrorCodeProviderContract {
						_, _ = w.Write([]byte("data: [DONE]\n\n"))
					}
				})
				_, err = NewOpenAIClient(transport).GenerateStreamWithEvents(context.Background(), Request{
					Model:          "requested-model",
					ToolChoiceMode: ToolChoiceModeAutomatic,
				}, StreamCallbacks{})
			}

			var providerErr *ProviderAPIError
			if err == nil || !errors.As(err, &providerErr) {
				t.Fatalf("error = %T %v, want ProviderAPIError", err, err)
			}
			if providerErr.Code != test.wantCode {
				t.Fatalf("unified code = %q, want %q", providerErr.Code, test.wantCode)
			}
			if providerErr.ProviderRequestID == nil || *providerErr.ProviderRequestID != "request-primary" {
				t.Fatalf("provider request ID = %#v, want request-primary", providerErr.ProviderRequestID)
			}
			if providerErr.AuthorizationDiagnostic == nil || *providerErr.AuthorizationDiagnostic != "token rejected" {
				t.Fatalf("authorization diagnostic = %#v, want token rejected", providerErr.AuthorizationDiagnostic)
			}
			if got := !IsNonRetriableModelError(err); got != test.wantRetriable {
				t.Fatalf("retriable = %t, want %t", got, test.wantRetriable)
			}
		})
	}
}

func newResponseMetadataTransport(
	t *testing.T,
	headers http.Header,
	writeResponse func(http.ResponseWriter, *http.Request),
) *HTTPTransport {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		for name, values := range headers {
			for _, value := range values {
				w.Header().Add(name, value)
			}
		}
		writeResponse(w, request)
	}))
	t.Cleanup(server.Close)
	transport := NewHTTPTransport(staticAuth{})
	transport.BaseURL, transport.BaseURLExplicit, transport.Client = server.URL, true, server.Client()
	return transport
}

func decodeRequestedModel(t *testing.T, request *http.Request) string {
	t.Helper()
	var payload struct {
		Model string `json:"model"`
	}
	if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	return payload.Model
}

func writeSuccessfulMetadataResponse(
	t *testing.T,
	w http.ResponseWriter,
	mode responseMetadataMode,
	createdModel string,
	completedModel string,
) {
	t.Helper()
	response := fmt.Sprintf(
		`{"id":"resp_1","object":"response","model":%q,"output":[{"type":"message","id":"msg_1","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"done"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`,
		completedModel,
	)
	if mode == responseMetadataNonStreaming {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(response))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	if createdModel != "" {
		_, _ = fmt.Fprintf(w, "data: {\"type\":\"response.created\",\"response\":{\"model\":%q}}\n\n", createdModel)
	}
	_, _ = fmt.Fprintf(w, "data: {\"type\":\"response.completed\",\"response\":%s}\n\ndata: [DONE]\n\n", response)
}

func stringPointer(value string) *string {
	return &value
}
