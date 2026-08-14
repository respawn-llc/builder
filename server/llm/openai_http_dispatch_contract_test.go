package llm

import (
	"context"
	"core/internal/testharness/httpclient"
	"core/shared/textutil"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
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
	methods := map[string]func(*HTTPTransport, *string) error{
		"generate": func(transport *HTTPTransport, sessionID *string) error {
			_, err := transport.Generate(context.Background(), OpenAIRequest{Model: "gpt-5", ToolChoiceMode: ToolChoiceModeAutomatic, SessionID: sessionID})
			return err
		},
		"stream": func(transport *HTTPTransport, sessionID *string) error {
			_, err := transport.GenerateStreamWithEvents(context.Background(), OpenAIRequest{Model: "gpt-5", ToolChoiceMode: ToolChoiceModeAutomatic, SessionID: sessionID}, StreamCallbacks{})
			return err
		},
		"compact": func(transport *HTTPTransport, sessionID *string) error {
			_, err := transport.Compact(context.Background(), OpenAICompactionRequest{Model: "gpt-5", SessionID: sessionID})
			return err
		},
	}
	invalidSessionIDs := map[string]*string{
		"missing": nil, "present empty": textutil.Value(""), "leading SP": textutil.Value(" session-1"),
		"trailing HTAB": textutil.Value("session-1\t"), "control byte": textutil.Value("session-\n1"),
	}
	authModes := map[string]func() (*HTTPTransport, func() int32){
		"api key": func() (*HTTPTransport, func() int32) {
			auth := &countingAuth{}
			return NewHTTPTransport(auth), auth.calls.Load
		},
		"OAuth": func() (*HTTPTransport, func() int32) {
			auth := &countingOAuthAuth{}
			return NewHTTPTransport(auth), auth.calls.Load
		},
		"anonymous explicit base": func() (*HTTPTransport, func() int32) {
			transport := NewHTTPTransport(nil)
			transport.BaseURL, transport.BaseURLExplicit = "https://compatible.example/v1", true
			return transport, func() int32 { return 0 }
		},
	}
	for authName, newTransport := range authModes {
		for methodName, dispatch := range methods {
			for invalidName, sessionID := range invalidSessionIDs {
				t.Run(authName+"/"+methodName+"/"+invalidName, func(t *testing.T) {
					transport, authCalls := newTransport()
					networkCalls := atomic.Int32{}
					transport.Client = &http.Client{Transport: httpclient.RoundTripFunc(func(*http.Request) (*http.Response, error) {
						networkCalls.Add(1)
						return nil, errors.New("unexpected network request")
					})}
					err := dispatch(transport, sessionID)
					if !errors.Is(err, ErrInvalidRequest) {
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

func TestOAuthGenerateSendsCanonicalCodexIdentityAuthAndRoutingTiers(t *testing.T) {
	var capturedHeaders http.Header
	var capturedBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedHeaders = r.Header.Clone()
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		capturedBody = body
		writeCompletedResponseSSE(w)
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
	transport.BaseURL = "https://chatgpt.com/backend-api/codex"
	transport.BaseURLExplicit = true
	transport.Client = newRewritingHTTPClient(t, server)

	request := OpenAIRequest{
		Model:          "gpt-5.6-sol",
		ToolChoiceMode: ToolChoiceModeAutomatic,
		SessionID:      textutil.Value("session-1"),
		CodexDispatch:  dispatch,
	}
	_, err = transport.Generate(context.Background(), request)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if got := capturedHeaders.Get("x-codex-routing-hint"); got != "model=gpt-5.6-sol" {
		t.Fatalf("standard routing hint = %q, want model only", got)
	}
	if _, exists := capturedBody["service_tier"]; exists {
		t.Fatalf("standard service_tier = %#v, want omitted", capturedBody["service_tier"])
	}
	if capturedBody["stream"] != true {
		t.Fatalf("standard stream = %#v, want true", capturedBody["stream"])
	}
	assertCanonicalGenerationMetadata(t, capturedBody)
	request.FastMode = true
	if _, err = transport.Generate(context.Background(), request); err != nil {
		t.Fatalf("fast generate: %v", err)
	}
	if got := capturedHeaders.Get("session-id"); got != "session-1" {
		t.Fatalf("session-id = %q, want session-1", got)
	}
	if got := capturedHeaders.Get("session_id"); got != "" {
		t.Fatalf("legacy session_id = %q, want absent", got)
	}
	if got := capturedHeaders.Get("x-codex-routing-hint"); got != "model=gpt-5.6-sol;tier=priority" {
		t.Fatalf("routing hint = %q, want priority tier", got)
	}
	if authorization, account, originator, userAgent := capturedHeaders.Get("Authorization"),
		capturedHeaders.Get("ChatGPT-Account-Id"), capturedHeaders.Get("originator"), capturedHeaders.Get("User-Agent"); authorization != "Bearer token" || account != "acc-1" ||
		originator != transport.ProviderIdentifier || userAgent != transport.providerUserAgent() {
		t.Fatalf("OAuth identity headers = (%q, %q, %q, %q), want resolved auth, account, originator, and User-Agent",
			authorization, account, originator, userAgent)
	}
	if got := capturedBody["service_tier"]; got != "priority" {
		t.Fatalf("service_tier = %#v, want priority", got)
	}
	if capturedBody["stream"] != true {
		t.Fatalf("priority stream = %#v, want true", capturedBody["stream"])
	}
	assertCanonicalGenerationMetadata(t, capturedBody)
}

func TestOAuthExplicitCompatibleEndpointSendsCommonIdentityWithoutCodexMetadata(t *testing.T) {
	var capturedHeaders http.Header
	var capturedBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedHeaders = r.Header.Clone()
		if err := json.NewDecoder(r.Body).Decode(&capturedBody); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeCompletedResponseSSE(w)
	}))
	t.Cleanup(server.Close)

	transport := NewHTTPTransport(oauthStaticAuth{})
	transport.BaseURL = server.URL
	transport.BaseURLExplicit = true
	transport.Client = server.Client()

	if _, err := transport.Generate(context.Background(), OpenAIRequest{
		Model:          "gpt-5.6-sol",
		SessionID:      textutil.Value("session-1"),
		ToolChoiceMode: ToolChoiceModeAutomatic,
	}); err != nil {
		t.Fatalf("generate: %v", err)
	}
	if got := capturedHeaders.Get("session-id"); got != "session-1" {
		t.Fatalf("session-id = %q, want session-1", got)
	}
	if got := capturedHeaders.Get("x-codex-routing-hint"); got != "" {
		t.Fatalf("Codex routing hint = %q, want absent", got)
	}
	if got := capturedHeaders.Get(codexTurnStateHeader); got != "" {
		t.Fatalf("Codex turn state = %q, want absent", got)
	}
	if _, exists := capturedBody["client_metadata"]; exists {
		t.Fatalf("client_metadata = %#v, want absent", capturedBody["client_metadata"])
	}
}

func assertCanonicalGenerationMetadata(t *testing.T, body map[string]any) {
	t.Helper()
	metadata := requireCodexTurnMetadata(t, body)
	if metadata["session_id"] != "session-1" || metadata["thread_id"] != "session-1" ||
		metadata["turn_id"] != "run-1" || metadata["window_id"] != "session-1:2" ||
		metadata["request_kind"] != "turn" {
		t.Fatalf("turn metadata = %#v, want canonical identity", metadata)
	}
}

func requireCodexTurnMetadata(t *testing.T, body map[string]any) map[string]any {
	t.Helper()
	clientMetadata, ok := body["client_metadata"].(map[string]any)
	if !ok {
		t.Fatalf("client_metadata = %#v, want object", body["client_metadata"])
	}
	rawMetadata, ok := clientMetadata["x-codex-turn-metadata"].(string)
	if !ok {
		t.Fatalf("turn metadata = %#v, want JSON string", clientMetadata["x-codex-turn-metadata"])
	}
	var metadata map[string]any
	if err := json.Unmarshal([]byte(rawMetadata), &metadata); err != nil {
		t.Fatalf("decode turn metadata: %v", err)
	}
	return metadata
}

func TestOAuthDispatchRejectsUnrepresentableRoutingModelBeforeProviderHTTP(t *testing.T) {
	methods := map[string]func(*HTTPTransport, string, *CodexDispatchContext) error{
		"generate": func(transport *HTTPTransport, model string, dispatch *CodexDispatchContext) error {
			_, err := transport.Generate(context.Background(), OpenAIRequest{Model: model, ToolChoiceMode: ToolChoiceModeAutomatic, SessionID: textutil.Value("session-1"), CodexDispatch: dispatch})
			return err
		},
		"stream": func(transport *HTTPTransport, model string, dispatch *CodexDispatchContext) error {
			_, err := transport.GenerateStreamWithEvents(context.Background(), OpenAIRequest{Model: model, ToolChoiceMode: ToolChoiceModeAutomatic, SessionID: textutil.Value("session-1"), CodexDispatch: dispatch}, StreamCallbacks{})
			return err
		},
		"compact": func(transport *HTTPTransport, model string, dispatch *CodexDispatchContext) error {
			_, err := transport.Compact(context.Background(), OpenAICompactionRequest{Model: model, SessionID: textutil.Value("session-1"), CodexDispatch: dispatch})
			return err
		},
	}
	invalidModels := map[string]string{
		"empty": "", "leading SP": " gpt-5", "trailing HTAB": "gpt-5\t",
		"semicolon": "gpt-5;tier=priority", "control byte": "gpt-\n5",
	}
	for methodName, dispatchRequest := range methods {
		for invalidName, model := range invalidModels {
			t.Run(methodName+"/"+invalidName, func(t *testing.T) {
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
				transport.Client = &http.Client{Transport: httpclient.RoundTripFunc(func(*http.Request) (*http.Response, error) {
					networkCalls.Add(1)
					return nil, errors.New("unexpected network request")
				})}

				err = dispatchRequest(transport, model, dispatch)
				if !errors.Is(err, ErrInvalidRequest) {
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
	methods := map[string]func(*HTTPTransport) error{
		"generate": func(transport *HTTPTransport) error {
			_, err := transport.Generate(context.Background(), OpenAIRequest{Model: "unknown-model", ToolChoiceMode: ToolChoiceModeAutomatic, SessionID: textutil.Value("session-1")})
			return err
		},
		"stream": func(transport *HTTPTransport) error {
			_, err := transport.GenerateStreamWithEvents(context.Background(), OpenAIRequest{Model: "unknown-model", ToolChoiceMode: ToolChoiceModeAutomatic, SessionID: textutil.Value("session-1")}, StreamCallbacks{})
			return err
		},
		"compact": func(transport *HTTPTransport) error {
			_, err := transport.Compact(context.Background(), OpenAICompactionRequest{Model: "unknown-model", SessionID: textutil.Value("session-1")})
			return err
		},
	}
	for name, dispatch := range methods {
		t.Run(name, func(t *testing.T) {
			networkCalls := atomic.Int32{}
			transport := NewHTTPTransport(oauthStaticAuth{})
			transport.ContextWindowTokens = 0
			transport.Client = &http.Client{Transport: httpclient.RoundTripFunc(func(*http.Request) (*http.Response, error) {
				networkCalls.Add(1)
				return nil, errors.New("unexpected network request")
			})}

			err := dispatch(transport)
			if !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("error = %v, want missing Codex dispatch context", err)
			}
			if got := networkCalls.Load(); got != 0 {
				t.Fatalf("network calls = %d, want 0", got)
			}
		})
	}
}
