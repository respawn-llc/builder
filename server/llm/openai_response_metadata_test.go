package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOpenAIClientSelectsServedModelMetadata(t *testing.T) {
	tests := []struct {
		name           string
		createdModel   string
		completedModel string
		headers        http.Header
		want           *string
	}{
		{
			name:           "standard model wins",
			completedModel: " standard-model ",
			headers:        http.Header{"Openai-Model": {"header-model"}, "X-Openai-Model": {"fallback-model"}},
			want:           stringPointer("standard-model"),
		},
		{
			name:    "provider header priority",
			headers: http.Header{"Openai-Model": {" ", " routed-model "}, "X-Openai-Model": {"fallback-model"}},
			want:    stringPointer("routed-model"),
		},
		{
			name:           "stream first standard model wins",
			createdModel:   " created-model ",
			completedModel: "completed-model",
			headers:        http.Header{"Openai-Model": {"header-model"}},
			want:           stringPointer("created-model"),
		},
		{
			name:           "stream completed standard model wins",
			completedModel: " completed-model ",
			headers:        http.Header{"Openai-Model": {"header-model"}},
			want:           stringPointer("completed-model"),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var capturedModel string
			transport := newResponseMetadataTransport(t, test.headers, func(w http.ResponseWriter, request *http.Request) {
				capturedModel = decodeRequestedModel(t, request)
				writeSuccessfulMetadataResponse(t, w, test.createdModel, test.completedModel)
			})
			request := Request{SessionID: stringPointer("metadata-session"), Model: "requested-model", ToolChoiceMode: ToolChoiceModeAutomatic}
			client := NewOpenAIClient(transport)
			response, err := client.Generate(context.Background(), request, StreamCallbacks{})
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
		{name: "absent"},
	}
	for _, header := range headers {
		t.Run(header.name, func(t *testing.T) {
			responseHeaders := make(http.Header)
			if header.value != nil {
				responseHeaders.Set("x-reasoning-included", *header.value)
			}
			transport := newResponseMetadataTransport(t, responseHeaders, func(w http.ResponseWriter, _ *http.Request) {
				writeSuccessfulMetadataResponse(t, w, "", "")
			})

			request := Request{SessionID: stringPointer("metadata-session"), Model: "requested-model", ToolChoiceMode: ToolChoiceModeAutomatic}
			client := NewOpenAIClient(transport)
			response, err := client.Generate(context.Background(), request, StreamCallbacks{})
			if err != nil {
				t.Fatalf("generate: %v", err)
			}
			if response.ReasoningIncluded != header.want {
				t.Fatalf("reasoning included = %v, want %v", response.ReasoningIncluded, header.want)
			}
		})
	}
}

func TestOpenAIResponseErrorsPreserveHeaderDiagnostics(t *testing.T) {
	diagnosticHeaders := http.Header{"X-Request-Id": {" ", " request-primary "}, "X-Oai-Request-Id": {"request-fallback"},
		"X-Openai-Authorization-Error": {" ", " token rejected "}}
	for _, test := range []struct {
		name     string
		err      error
		wantCode UnifiedErrorCode
	}{
		{name: "provider contract", err: newOpenAIProviderContractError("openai", &http.Response{
			StatusCode: http.StatusOK, Header: diagnosticHeaders.Clone(),
		}, errors.New("invalid response")), wantCode: UnifiedErrorCodeProviderContract},
	} {
		t.Run(test.name, func(t *testing.T) {
			var providerErr *ProviderAPIError
			if !errors.As(test.err, &providerErr) {
				t.Fatalf("error = %T %v, want ProviderAPIError", test.err, test.err)
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
		})
	}
}

func newResponseMetadataTransport(t *testing.T, headers http.Header, writeResponse func(http.ResponseWriter, *http.Request)) *HTTPTransport {
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

func writeSuccessfulMetadataResponse(t *testing.T, w http.ResponseWriter, createdModel string, completedModel string) {
	t.Helper()
	response := fmt.Sprintf(
		`{"id":"resp_1","object":"response","model":%q,"output":[{"type":"message","id":"msg_1","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"done"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`,
		completedModel,
	)
	w.Header().Set("Content-Type", "text/event-stream")
	if createdModel != "" {
		_, _ = fmt.Fprintf(w, "data: {\"type\":\"response.created\",\"response\":{\"model\":%q}}\n\n", createdModel)
	}
	_, _ = fmt.Fprintf(w, "data: {\"type\":\"response.completed\",\"response\":%s}\n\ndata: [DONE]\n\n", response)
}
func stringPointer(value string) *string {
	return &value
}
