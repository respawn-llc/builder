package llm

import (
	"context"
	"core/shared/textutil"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

type countingAuth struct {
	calls atomic.Int32
}

func (a *countingAuth) AuthorizationHeader(context.Context) (string, error) {
	a.calls.Add(1)
	return "Bearer token", nil
}

type countingOAuthAuth struct {
	countingAuth
}

func (*countingOAuthAuth) OpenAIAuthMetadata(context.Context) (string, string, error) {
	return "oauth", "account-1", nil
}

func TestOpenAIDispatchRejectsInvalidSessionBeforeAuth(t *testing.T) {
	methods := []struct {
		name     string
		dispatch func(*HTTPTransport, *string) error
	}{
		{
			name: "generate",
			dispatch: func(transport *HTTPTransport, sessionID *string) error {
				_, err := transport.Generate(context.Background(), OpenAIRequest{
					Model:          "gpt-5",
					ToolChoiceMode: ToolChoiceModeAutomatic,
					SessionID:      sessionID,
				})
				return err
			},
		},
		{
			name: "stream",
			dispatch: func(transport *HTTPTransport, sessionID *string) error {
				_, err := transport.GenerateStreamWithEvents(context.Background(), OpenAIRequest{
					Model:          "gpt-5",
					ToolChoiceMode: ToolChoiceModeAutomatic,
					SessionID:      sessionID,
				}, StreamCallbacks{})
				return err
			},
		},
		{
			name: "compact",
			dispatch: func(transport *HTTPTransport, sessionID *string) error {
				_, err := transport.Compact(context.Background(), OpenAICompactionRequest{
					Model:     "gpt-5",
					SessionID: sessionID,
				})
				return err
			},
		},
	}

	invalidSessionIDs := []struct {
		name  string
		value *string
	}{
		{name: "missing"},
		{name: "present empty", value: textutil.Value("")},
		{name: "leading SP", value: textutil.Value(" session-1")},
		{name: "trailing HTAB", value: textutil.Value("session-1\t")},
		{name: "control byte", value: textutil.Value("session-\n1")},
		{name: "oversized", value: textutil.Value(strings.Repeat("s", maxCodexHeaderValueBytes+1))},
	}
	authModes := []struct {
		name      string
		transport func() (*HTTPTransport, func() int32)
	}{
		{
			name: "api key",
			transport: func() (*HTTPTransport, func() int32) {
				auth := &countingAuth{}
				return NewHTTPTransport(auth), auth.calls.Load
			},
		},
		{
			name: "OAuth",
			transport: func() (*HTTPTransport, func() int32) {
				auth := &countingOAuthAuth{}
				return NewHTTPTransport(auth), auth.calls.Load
			},
		},
		{
			name: "anonymous explicit base",
			transport: func() (*HTTPTransport, func() int32) {
				transport := NewHTTPTransport(nil)
				transport.BaseURL = "https://compatible.example/v1"
				transport.BaseURLExplicit = true
				return transport, func() int32 { return 0 }
			},
		},
	}
	for _, authMode := range authModes {
		for _, method := range methods {
			for _, invalidSessionID := range invalidSessionIDs {
				t.Run(authMode.name+"/"+method.name+"/"+invalidSessionID.name, func(t *testing.T) {
					transport, authCalls := authMode.transport()
					networkCalls := atomic.Int32{}
					transport.Client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
						networkCalls.Add(1)
						return nil, errors.New("unexpected network request")
					})}

					err := method.dispatch(transport, invalidSessionID.value)
					if err == nil || !strings.Contains(strings.ToLower(err.Error()), "session id") {
						t.Fatalf("error = %v, want invalid Session ID", err)
					}
					if got := authCalls(); got != 0 {
						t.Fatalf("auth calls = %d, want 0", got)
					}
					if got := networkCalls.Load(); got != 0 {
						t.Fatalf("network calls = %d, want 0", got)
					}
				})
			}
		}
	}
}

func TestOAuthGenerateSendsCanonicalCodexIdentityAndRouting(t *testing.T) {
	var capturedHeaders http.Header
	var capturedBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedHeaders = r.Header.Clone()
		if err := json.NewDecoder(r.Body).Decode(&capturedBody); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeCompletedResponseJSON(w)
	}))
	t.Cleanup(server.Close)

	dispatch, err := NewCodexDispatchContext(CodexDispatchFacts{
		SessionID:            "session-1",
		RunID:                "run-1",
		CompactionGeneration: 2,
		RequestKind:          CodexRequestKindTurn.Optional(),
	})
	if err != nil {
		t.Fatalf("dispatch context: %v", err)
	}
	transport := NewHTTPTransport(oauthStaticAuth{})
	transport.BaseURL = server.URL
	transport.BaseURLExplicit = true
	transport.Client = server.Client()

	_, err = transport.Generate(context.Background(), OpenAIRequest{
		Model:          "gpt-5.6-sol",
		ToolChoiceMode: ToolChoiceModeAutomatic,
		SessionID:      textutil.Value("session-1"),
		CodexDispatch:  dispatch,
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if got := capturedHeaders.Get("session-id"); got != "session-1" {
		t.Fatalf("session-id = %q, want session-1", got)
	}
	if got := capturedHeaders.Get("session_id"); got != "" {
		t.Fatalf("legacy session_id = %q, want absent", got)
	}
	if got := capturedHeaders.Get("x-codex-routing-hint"); got != "model=gpt-5.6-sol" {
		t.Fatalf("routing hint = %q, want model=gpt-5.6-sol", got)
	}
	if got := capturedHeaders.Get("Authorization"); got != "Bearer token" {
		t.Fatalf("Authorization = %q, want Bearer token", got)
	}
	if got := capturedHeaders.Get("ChatGPT-Account-Id"); got != "acc-1" {
		t.Fatalf("ChatGPT-Account-Id = %q, want acc-1", got)
	}
	if got := capturedHeaders.Get("originator"); got != transport.ProviderIdentifier {
		t.Fatalf("originator = %q, want %q", got, transport.ProviderIdentifier)
	}
	if got := capturedHeaders.Get("User-Agent"); got != transport.providerUserAgent() {
		t.Fatalf("User-Agent = %q, want %q", got, transport.providerUserAgent())
	}
	clientMetadata, ok := capturedBody["client_metadata"].(map[string]any)
	if !ok {
		t.Fatalf("client_metadata = %#v, want object", capturedBody["client_metadata"])
	}
	rawMetadata, ok := clientMetadata["x-codex-turn-metadata"].(string)
	if !ok {
		t.Fatalf("turn metadata = %#v, want JSON string", clientMetadata["x-codex-turn-metadata"])
	}
	var metadata map[string]any
	if err := json.Unmarshal([]byte(rawMetadata), &metadata); err != nil {
		t.Fatalf("decode turn metadata: %v", err)
	}
	if metadata["session_id"] != "session-1" || metadata["thread_id"] != "session-1" ||
		metadata["turn_id"] != "run-1" || metadata["window_id"] != "session-1:2" ||
		metadata["request_kind"] != "turn" {
		t.Fatalf("turn metadata = %#v, want canonical identity", metadata)
	}
}

func TestOAuthFastGenerationRoutingTierMatchesPayload(t *testing.T) {
	var capturedHeaders http.Header
	var capturedBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedHeaders = r.Header.Clone()
		if err := json.NewDecoder(r.Body).Decode(&capturedBody); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeCompletedResponseJSON(w)
	}))
	t.Cleanup(server.Close)

	dispatch, err := NewCodexDispatchContext(CodexDispatchFacts{SessionID: "session-1", RunID: "run-1"})
	if err != nil {
		t.Fatalf("dispatch context: %v", err)
	}
	transport := NewHTTPTransport(oauthStaticAuth{})
	transport.BaseURL = server.URL
	transport.BaseURLExplicit = true
	transport.Client = server.Client()

	_, err = transport.Generate(context.Background(), OpenAIRequest{
		Model:          "gpt-5.6-sol",
		ToolChoiceMode: ToolChoiceModeAutomatic,
		FastMode:       true,
		SessionID:      textutil.Value("session-1"),
		CodexDispatch:  dispatch,
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if got := capturedHeaders.Get("x-codex-routing-hint"); got != "model=gpt-5.6-sol;tier=priority" {
		t.Fatalf("routing hint = %q, want priority tier", got)
	}
	if got := capturedBody["service_tier"]; got != "priority" {
		t.Fatalf("service_tier = %#v, want priority", got)
	}
}

func TestNonOAuthDispatchOmitsCodexOnlyContract(t *testing.T) {
	var capturedHeaders http.Header
	var capturedBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedHeaders = r.Header.Clone()
		if err := json.NewDecoder(r.Body).Decode(&capturedBody); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeCompletedResponseJSON(w)
	}))
	t.Cleanup(server.Close)

	transport := NewHTTPTransport(staticAuth{})
	transport.BaseURL = server.URL
	transport.Client = server.Client()
	_, err := transport.Generate(context.Background(), OpenAIRequest{
		Model:          "gpt-5",
		ToolChoiceMode: ToolChoiceModeAutomatic,
		SessionID:      textutil.Value("session-1"),
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if got := capturedHeaders.Get("session-id"); got != "session-1" {
		t.Fatalf("session-id = %q, want session-1", got)
	}
	if got := capturedHeaders.Get("x-codex-routing-hint"); got != "" {
		t.Fatalf("routing hint = %q, want absent", got)
	}
	if got := capturedHeaders.Get("ChatGPT-Account-Id"); got != "" {
		t.Fatalf("ChatGPT-Account-Id = %q, want absent", got)
	}
	if _, exists := capturedBody["client_metadata"]; exists {
		t.Fatalf("client_metadata = %#v, want absent", capturedBody["client_metadata"])
	}
}

func TestAnonymousExplicitBaseDispatchSendsCommonIdentityWithoutAuthorization(t *testing.T) {
	var capturedHeaders http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedHeaders = r.Header.Clone()
		writeCompletedResponseJSON(w)
	}))
	t.Cleanup(server.Close)

	transport := NewHTTPTransport(nil)
	transport.BaseURL = server.URL
	transport.BaseURLExplicit = true
	transport.Client = server.Client()
	transport.ProviderIdentifier = "kent_test"
	_, err := transport.Generate(context.Background(), OpenAIRequest{
		Model:          "custom-model",
		ToolChoiceMode: ToolChoiceModeAutomatic,
		SessionID:      textutil.Value("session-1"),
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if got := capturedHeaders.Get("Authorization"); got != "" {
		t.Fatalf("Authorization = %q, want absent", got)
	}
	if got := capturedHeaders.Get("originator"); got != "kent_test" {
		t.Fatalf("originator = %q, want kent_test", got)
	}
	if got := capturedHeaders.Get("User-Agent"); got == "" {
		t.Fatal("User-Agent is absent")
	}
	if got := capturedHeaders.Get("session-id"); got != "session-1" {
		t.Fatalf("session-id = %q, want session-1", got)
	}
}

func TestOAuthDispatchRejectsUnrepresentableRoutingModelBeforeProviderHTTP(t *testing.T) {
	methods := []struct {
		name     string
		dispatch func(*HTTPTransport, string, *CodexDispatchContext) error
	}{
		{
			name: "generate",
			dispatch: func(transport *HTTPTransport, model string, dispatch *CodexDispatchContext) error {
				_, err := transport.Generate(context.Background(), OpenAIRequest{
					Model:          model,
					ToolChoiceMode: ToolChoiceModeAutomatic,
					SessionID:      textutil.Value("session-1"),
					CodexDispatch:  dispatch,
				})
				return err
			},
		},
		{
			name: "stream",
			dispatch: func(transport *HTTPTransport, model string, dispatch *CodexDispatchContext) error {
				_, err := transport.GenerateStreamWithEvents(context.Background(), OpenAIRequest{
					Model:          model,
					ToolChoiceMode: ToolChoiceModeAutomatic,
					SessionID:      textutil.Value("session-1"),
					CodexDispatch:  dispatch,
				}, StreamCallbacks{})
				return err
			},
		},
		{
			name: "compact",
			dispatch: func(transport *HTTPTransport, model string, dispatch *CodexDispatchContext) error {
				_, err := transport.Compact(context.Background(), OpenAICompactionRequest{
					Model:         model,
					SessionID:     textutil.Value("session-1"),
					CodexDispatch: dispatch,
				})
				return err
			},
		},
	}
	invalidModels := []struct {
		name  string
		value string
	}{
		{name: "empty"},
		{name: "leading SP", value: " gpt-5"},
		{name: "trailing HTAB", value: "gpt-5\t"},
		{name: "semicolon", value: "gpt-5;tier=priority"},
		{name: "control byte", value: "gpt-\n5"},
		{name: "oversized routing hint", value: strings.Repeat("m", maxCodexHeaderValueBytes-len("model=")+1)},
	}

	for _, method := range methods {
		for _, invalidModel := range invalidModels {
			t.Run(method.name+"/"+invalidModel.name, func(t *testing.T) {
				dispatch, err := NewCodexDispatchContext(CodexDispatchFacts{
					SessionID:   "session-1",
					RunID:       "run-1",
					RequestKind: CodexRequestKindTurn.Optional(),
				})
				if err != nil {
					t.Fatalf("dispatch context: %v", err)
				}
				networkCalls := atomic.Int32{}
				transport := NewHTTPTransport(oauthStaticAuth{})
				transport.ContextWindowTokens = 0
				transport.Client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
					networkCalls.Add(1)
					return nil, errors.New("unexpected network request")
				})}

				err = method.dispatch(transport, invalidModel.value, dispatch)
				if err == nil || !strings.Contains(strings.ToLower(err.Error()), "routing") {
					t.Fatalf("error = %v, want invalid routing model", err)
				}
				if got := networkCalls.Load(); got != 0 {
					t.Fatalf("network calls = %d, want 0", got)
				}
			})
		}
	}
}

func TestOAuthDispatchRejectsMissingContextBeforeContextWindowHTTP(t *testing.T) {
	methods := []struct {
		name     string
		dispatch func(*HTTPTransport) error
	}{
		{
			name: "generate",
			dispatch: func(transport *HTTPTransport) error {
				_, err := transport.Generate(context.Background(), OpenAIRequest{
					Model:          "unknown-model",
					ToolChoiceMode: ToolChoiceModeAutomatic,
					SessionID:      textutil.Value("session-1"),
				})
				return err
			},
		},
		{
			name: "stream",
			dispatch: func(transport *HTTPTransport) error {
				_, err := transport.GenerateStreamWithEvents(context.Background(), OpenAIRequest{
					Model:          "unknown-model",
					ToolChoiceMode: ToolChoiceModeAutomatic,
					SessionID:      textutil.Value("session-1"),
				}, StreamCallbacks{})
				return err
			},
		},
		{
			name: "compact",
			dispatch: func(transport *HTTPTransport) error {
				_, err := transport.Compact(context.Background(), OpenAICompactionRequest{
					Model:     "unknown-model",
					SessionID: textutil.Value("session-1"),
				})
				return err
			},
		},
	}
	for _, method := range methods {
		t.Run(method.name, func(t *testing.T) {
			networkCalls := atomic.Int32{}
			transport := NewHTTPTransport(oauthStaticAuth{})
			transport.ContextWindowTokens = 0
			transport.Client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				networkCalls.Add(1)
				return nil, errors.New("unexpected network request")
			})}

			err := method.dispatch(transport)
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), "dispatch context") {
				t.Fatalf("error = %v, want missing Codex dispatch context", err)
			}
			if got := networkCalls.Load(); got != 0 {
				t.Fatalf("network calls = %d, want 0", got)
			}
		})
	}
}
