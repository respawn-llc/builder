package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOpenAIClientGenerateReturnsStandardServedModelWithoutChangingRequest(t *testing.T) {
	var capturedModel string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		capturedModel = request.Model
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"resp_1",
			"object":"response",
			"model":"served-model",
			"output":[{
				"type":"message",
				"id":"msg_1",
				"role":"assistant",
				"phase":"final_answer",
				"status":"completed",
				"content":[{"type":"output_text","text":"done"}]
			}],
			"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}
		}`))
	}))
	t.Cleanup(server.Close)

	transport := NewHTTPTransport(staticAuth{})
	transport.BaseURL = server.URL
	transport.BaseURLExplicit = true
	transport.Client = server.Client()
	client := NewOpenAIClient(transport)
	request := Request{Model: "requested-model", ToolChoiceMode: ToolChoiceModeAutomatic}

	response, err := client.Generate(context.Background(), request)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if response.ServedModel == nil || *response.ServedModel != "served-model" {
		t.Fatalf("served model = %#v, want served-model", response.ServedModel)
	}
	if request.Model != "requested-model" || capturedModel != "requested-model" {
		t.Fatalf("requested model changed: request=%q wire=%q", request.Model, capturedModel)
	}
}

func TestOpenAIClientGenerateSelectsServedModelMetadata(t *testing.T) {
	tests := []struct {
		name          string
		standardModel string
		headers       http.Header
		want          *string
	}{
		{
			name:          "standard model wins over both provider headers",
			standardModel: " standard-model ",
			headers: http.Header{
				"Openai-Model":   []string{"header-model"},
				"X-Openai-Model": []string{"fallback-model"},
			},
			want: stringPointer("standard-model"),
		},
		{
			name: "openai model wins over x openai model",
			headers: http.Header{
				"Openai-Model":   []string{" ", " routed-model "},
				"X-Openai-Model": []string{"fallback-model"},
			},
			want: stringPointer("routed-model"),
		},
		{
			name: "x openai model is fallback",
			headers: http.Header{
				"Openai-Model":   []string{" ", "\t"},
				"X-Openai-Model": []string{" fallback-model "},
			},
			want: stringPointer("fallback-model"),
		},
		{
			name: "blank reports are absent",
			headers: http.Header{
				"Openai-Model":   []string{" ", "\t"},
				"X-Openai-Model": []string{"\r\n"},
			},
		},
		{name: "missing metadata is absent"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				for name, values := range test.headers {
					for _, value := range values {
						w.Header().Add(name, value)
					}
				}
				w.Header().Set("Content-Type", "application/json")
				response := map[string]any{
					"id":     "resp_1",
					"object": "response",
					"output": []any{map[string]any{
						"type":   "message",
						"id":     "msg_1",
						"role":   "assistant",
						"phase":  "final_answer",
						"status": "completed",
						"content": []any{
							map[string]any{"type": "output_text", "text": "done"},
						},
					}},
					"usage": map[string]any{"input_tokens": 1, "output_tokens": 1, "total_tokens": 2},
				}
				if test.standardModel != "" {
					response["model"] = test.standardModel
				}
				if err := json.NewEncoder(w).Encode(response); err != nil {
					t.Errorf("encode response: %v", err)
				}
			}))
			t.Cleanup(server.Close)

			transport := NewHTTPTransport(staticAuth{})
			transport.BaseURL = server.URL
			transport.BaseURLExplicit = true
			transport.Client = server.Client()

			response, err := NewOpenAIClient(transport).Generate(context.Background(), Request{
				Model:          "requested-model",
				ToolChoiceMode: ToolChoiceModeAutomatic,
			})
			if err != nil {
				t.Fatalf("generate: %v", err)
			}
			if !equalOptionalStrings(response.ServedModel, test.want) {
				t.Fatalf("served model = %#v, want %#v", response.ServedModel, test.want)
			}
		})
	}
}

func TestOpenAIClientGenerateStreamSelectsServedModelMetadata(t *testing.T) {
	tests := []struct {
		name           string
		createdModel   string
		completedModel string
		headers        http.Header
		want           *string
	}{
		{
			name:           "created standard model is first at standard priority",
			createdModel:   " created-model ",
			completedModel: "completed-model",
			headers: http.Header{
				"Openai-Model": []string{"header-model"},
			},
			want: stringPointer("created-model"),
		},
		{
			name:           "completed standard model wins over provider header",
			completedModel: " completed-model ",
			headers: http.Header{
				"Openai-Model": []string{"header-model"},
			},
			want: stringPointer("completed-model"),
		},
		{
			name: "provider header fallback",
			headers: http.Header{
				"Openai-Model":   []string{" ", " header-model "},
				"X-Openai-Model": []string{"fallback-model"},
			},
			want: stringPointer("header-model"),
		},
		{name: "missing metadata is absent"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				for name, values := range test.headers {
					for _, value := range values {
						w.Header().Add(name, value)
					}
				}
				w.Header().Set("Content-Type", "text/event-stream")
				if test.createdModel != "" {
					_, _ = w.Write([]byte(`data: {"type":"response.created","response":{"model":` + mustJSONString(t, test.createdModel) + `}}` + "\n\n"))
				}
				completed := map[string]any{
					"type": "response.completed",
					"response": map[string]any{
						"usage": map[string]any{"input_tokens": 1, "output_tokens": 1, "total_tokens": 2},
						"output": []any{map[string]any{
							"type":  "message",
							"id":    "msg_1",
							"role":  "assistant",
							"phase": "final_answer",
							"content": []any{
								map[string]any{"type": "output_text", "text": "done"},
							},
						}},
					},
				}
				if test.completedModel != "" {
					completed["response"].(map[string]any)["model"] = test.completedModel
				}
				encoded, err := json.Marshal(completed)
				if err != nil {
					t.Errorf("encode completed response: %v", err)
					return
				}
				_, _ = w.Write([]byte("data: " + string(encoded) + "\n\ndata: [DONE]\n\n"))
			}))
			t.Cleanup(server.Close)

			transport := NewHTTPTransport(staticAuth{})
			transport.BaseURL = server.URL
			transport.BaseURLExplicit = true
			transport.Client = server.Client()

			response, err := NewOpenAIClient(transport).GenerateStreamWithEvents(context.Background(), Request{
				Model:          "requested-model",
				ToolChoiceMode: ToolChoiceModeAutomatic,
			}, StreamCallbacks{})
			if err != nil {
				t.Fatalf("generate stream: %v", err)
			}
			if !equalOptionalStrings(response.ServedModel, test.want) {
				t.Fatalf("served model = %#v, want %#v", response.ServedModel, test.want)
			}
		})
	}
}

func TestOpenAIClientGenerateParsesReasoningIncludedHeader(t *testing.T) {
	tests := []struct {
		name   string
		header *string
		want   bool
	}{
		{name: "explicit true", header: stringPointer(" true "), want: true},
		{name: "explicit false", header: stringPointer("false")},
		{name: "malformed", header: stringPointer("yes")},
		{name: "parse bool numeric alias is malformed", header: stringPointer("1")},
		{name: "parse bool short alias is malformed", header: stringPointer("t")},
		{name: "case variant is malformed", header: stringPointer("TRUE")},
		{name: "absent"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if test.header != nil {
					w.Header().Set("x-reasoning-included", *test.header)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{
					"id":"resp_1",
					"object":"response",
					"output":[{
						"type":"message",
						"id":"msg_1",
						"role":"assistant",
						"phase":"final_answer",
						"status":"completed",
						"content":[{"type":"output_text","text":"done"}]
					}],
					"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}
				}`))
			}))
			t.Cleanup(server.Close)

			transport := NewHTTPTransport(staticAuth{})
			transport.BaseURL = server.URL
			transport.BaseURLExplicit = true
			transport.Client = server.Client()

			response, err := NewOpenAIClient(transport).Generate(context.Background(), Request{
				Model:          "requested-model",
				ToolChoiceMode: ToolChoiceModeAutomatic,
			})
			if err != nil {
				t.Fatalf("generate: %v", err)
			}
			if response.ReasoningIncluded != test.want {
				t.Fatalf("reasoning included = %v, want %v", response.ReasoningIncluded, test.want)
			}
		})
	}
}

func TestOpenAIClientGenerateStreamParsesReasoningIncludedHeader(t *testing.T) {
	tests := []struct {
		name   string
		header *string
		want   bool
	}{
		{name: "explicit true", header: stringPointer("true"), want: true},
		{name: "explicit false", header: stringPointer("false")},
		{name: "malformed", header: stringPointer("included")},
		{name: "parse bool numeric alias is malformed", header: stringPointer("1")},
		{name: "parse bool short alias is malformed", header: stringPointer("t")},
		{name: "case variant is malformed", header: stringPointer("TRUE")},
		{name: "absent"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if test.header != nil {
					w.Header().Set("x-reasoning-included", *test.header)
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write([]byte(`data: {"type":"response.completed","response":{"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2},"output":[{"type":"message","id":"msg_1","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"done"}]}]}}` + "\n\ndata: [DONE]\n\n"))
			}))
			t.Cleanup(server.Close)

			transport := NewHTTPTransport(staticAuth{})
			transport.BaseURL = server.URL
			transport.BaseURLExplicit = true
			transport.Client = server.Client()

			response, err := NewOpenAIClient(transport).GenerateStreamWithEvents(context.Background(), Request{
				Model:          "requested-model",
				ToolChoiceMode: ToolChoiceModeAutomatic,
			}, StreamCallbacks{})
			if err != nil {
				t.Fatalf("generate stream: %v", err)
			}
			if response.ReasoningIncluded != test.want {
				t.Fatalf("reasoning included = %v, want %v", response.ReasoningIncluded, test.want)
			}
		})
	}
}

func TestMapOpenAIRequestErrorEnrichesProviderDiagnostics(t *testing.T) {
	reducers := []struct {
		name       string
		providerID string
		body       string
	}{
		{
			name:       "OpenAI compatible",
			providerID: "openai",
			body:       `{"error":{"type":"invalid_request_error","code":"invalid_api_key","message":"denied"}}`,
		},
		{
			name:       "opaque ChatGPT",
			providerID: "chatgpt-codex",
			body:       `{"detail":"denied"}`,
		},
	}
	headerCases := []struct {
		name              string
		headers           http.Header
		wantRequestID     *string
		wantAuthorization *string
	}{
		{
			name: "trimmed diagnostics and x request id precedence",
			headers: http.Header{
				"X-Request-Id":                 []string{" ", " request-primary "},
				"X-Oai-Request-Id":             []string{"request-fallback"},
				"X-Openai-Authorization-Error": []string{" ", " token rejected "},
			},
			wantRequestID:     stringPointer("request-primary"),
			wantAuthorization: stringPointer("token rejected"),
		},
		{
			name: "x oai request id fallback",
			headers: http.Header{
				"X-Request-Id":     []string{" ", "\t"},
				"X-Oai-Request-Id": []string{" request-fallback "},
			},
			wantRequestID: stringPointer("request-fallback"),
		},
		{
			name: "blank diagnostics are absent",
			headers: http.Header{
				"X-Request-Id":                 []string{" "},
				"X-Oai-Request-Id":             []string{"\t"},
				"X-Openai-Authorization-Error": []string{"\r\n"},
			},
		},
		{name: "ordinary authentication error has no diagnostics"},
	}

	for _, reducer := range reducers {
		for _, headerCase := range headerCases {
			t.Run(reducer.name+"/"+headerCase.name, func(t *testing.T) {
				rawResp := &http.Response{
					StatusCode: http.StatusUnauthorized,
					Header:     headerCase.headers.Clone(),
					Body:       io.NopCloser(strings.NewReader(reducer.body)),
				}
				err := newOpenAIRequestErrorMapper(reducer.providerID).Map(nil, rawResp, "request failed")
				var providerErr *ProviderAPIError
				if !errors.As(err, &providerErr) {
					t.Fatalf("error = %T %v, want ProviderAPIError", err, err)
				}
				if !equalOptionalStrings(providerErr.ProviderRequestID, headerCase.wantRequestID) {
					t.Fatalf("provider request ID = %#v, want %#v", providerErr.ProviderRequestID, headerCase.wantRequestID)
				}
				if !equalOptionalStrings(providerErr.AuthorizationDiagnostic, headerCase.wantAuthorization) {
					t.Fatalf("authorization diagnostic = %#v, want %#v", providerErr.AuthorizationDiagnostic, headerCase.wantAuthorization)
				}
				if !IsAuthenticationError(err) || !IsNonRetriableModelError(err) {
					t.Fatalf("classification changed for enriched authentication error: %v", err)
				}
			})
		}
	}
}

func mustJSONString(t *testing.T, value string) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal JSON string: %v", err)
	}
	return string(encoded)
}

func stringPointer(value string) *string {
	return &value
}

func equalOptionalStrings(got, want *string) bool {
	if got == nil || want == nil {
		return got == want
	}
	return *got == *want
}
