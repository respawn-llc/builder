package runtimewire

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"core/server/llm"
	"core/server/runtime"
	"core/server/session"
	"core/shared/config"
	"core/shared/toolspec"
)

func TestRuntimeClientFactoryCreatesMainAndReviewerClients(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	store := newRuntimeWireSession(t, root, "factory")
	var purposes []RuntimeClientPurpose
	factory := RuntimeClientFactoryFunc(func(_ context.Context, req RuntimeClientRequest) (llm.Client, error) {
		purposes = append(purposes, req.Purpose)
		return &runtimewireCaptureClient{responses: []llm.Response{{Assistant: llm.Message{Role: llm.RoleAssistant, Content: "ok", Phase: llm.MessagePhaseFinal}, Usage: llm.Usage{WindowTokens: 200000}}}}, nil
	})

	wiring, err := NewRuntimeWiringWithBackground(
		store,
		config.Settings{
			Model:              "gpt-5",
			ModelContextWindow: 200000,
			Reviewer:           config.ReviewerSettings{Frequency: "all", Model: "gpt-5"},
			Timeouts:           config.Timeouts{ModelRequestSeconds: 1},
		},
		[]toolspec.ID{toolspec.ToolExecCommand},
		root,
		nil,
		nil,
		nil,
		RuntimeWiringOptions{ClientFactory: factory},
	)
	if err != nil {
		t.Fatalf("NewRuntimeWiringWithBackground: %v", err)
	}
	t.Cleanup(func() { _ = wiring.Close() })
	if len(purposes) != 2 || purposes[0] != RuntimeClientPurposeMain || purposes[1] != RuntimeClientPurposeReviewer {
		t.Fatalf("factory purposes = %#v, want main then reviewer", purposes)
	}
}

func TestRuntimeClientFactoryRejectsDirectClientOverride(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	store := newRuntimeWireSession(t, root, "factory-conflict")
	_, err := NewRuntimeWiringWithBackground(
		store,
		config.Settings{Model: "gpt-5", ModelContextWindow: 200000, Timeouts: config.Timeouts{ModelRequestSeconds: 1}},
		nil,
		root,
		nil,
		nil,
		nil,
		RuntimeWiringOptions{
			Client:        &runtimewireCaptureClient{},
			ClientFactory: RuntimeClientFactoryFunc(func(context.Context, RuntimeClientRequest) (llm.Client, error) { return nil, nil }),
		},
	)
	if !errors.Is(err, ErrRuntimeClientFactoryConflict) {
		t.Fatalf("error = %v, want ErrRuntimeClientFactoryConflict", err)
	}
}

func TestReviewerRuntimeClientFactoryCanPairWithDirectMainClient(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	store := newRuntimeWireSession(t, root, "reviewer-factory")
	reviewerCalls := 0
	factory := RuntimeClientFactoryFunc(func(_ context.Context, req RuntimeClientRequest) (llm.Client, error) {
		reviewerCalls++
		if req.Purpose != RuntimeClientPurposeReviewer {
			t.Fatalf("factory purpose = %v, want reviewer", req.Purpose)
		}
		return &runtimewireCaptureClient{responses: []llm.Response{{Assistant: llm.Message{Role: llm.RoleAssistant, Content: "review", Phase: llm.MessagePhaseFinal}, Usage: llm.Usage{WindowTokens: 200000}}}}, nil
	})

	wiring, err := NewRuntimeWiringWithBackground(
		store,
		config.Settings{
			Model:              "gpt-5",
			ModelContextWindow: 200000,
			Reviewer:           config.ReviewerSettings{Frequency: "all", Model: "gpt-5"},
			Timeouts:           config.Timeouts{ModelRequestSeconds: 1},
		},
		nil,
		root,
		nil,
		nil,
		nil,
		RuntimeWiringOptions{
			Client:                &runtimewireCaptureClient{responses: []llm.Response{{Assistant: llm.Message{Role: llm.RoleAssistant, Content: "ok", Phase: llm.MessagePhaseFinal}, Usage: llm.Usage{WindowTokens: 200000}}}},
			ReviewerClientFactory: factory,
		},
	)
	if err != nil {
		t.Fatalf("NewRuntimeWiringWithBackground: %v", err)
	}
	t.Cleanup(func() { _ = wiring.Close() })
	if reviewerCalls != 1 {
		t.Fatalf("reviewer factory calls = %d, want 1", reviewerCalls)
	}
}

func TestRuntimeClientFactoryReceivesActivationContext(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	store := newRuntimeWireSession(t, root, "factory-context")
	type contextKey struct{}
	ctx := context.WithValue(context.Background(), contextKey{}, "activation")
	factory := RuntimeClientFactoryFunc(func(got context.Context, req RuntimeClientRequest) (llm.Client, error) {
		if got.Value(contextKey{}) != "activation" {
			t.Fatalf("factory context value = %v, want activation", got.Value(contextKey{}))
		}
		return &runtimewireCaptureClient{responses: []llm.Response{{Assistant: llm.Message{Role: llm.RoleAssistant, Content: "ok", Phase: llm.MessagePhaseFinal}, Usage: llm.Usage{WindowTokens: 200000}}}}, nil
	})

	wiring, err := NewRuntimeWiringWithBackground(
		store,
		config.Settings{
			Model:              "gpt-5",
			ModelContextWindow: 200000,
			Reviewer:           config.ReviewerSettings{Frequency: "off"},
			Timeouts:           config.Timeouts{ModelRequestSeconds: 1},
		},
		nil,
		root,
		nil,
		nil,
		nil,
		RuntimeWiringOptions{Context: ctx, ClientFactory: factory},
	)
	if err != nil {
		t.Fatalf("NewRuntimeWiringWithBackground: %v", err)
	}
	t.Cleanup(func() { _ = wiring.Close() })
}

func TestRuntimeClientFactoryErrorDoesNotFallBackToProvider(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	store := newRuntimeWireSession(t, root, "factory-error")
	wantErr := errors.New("factory failed")
	calls := 0
	_, err := NewRuntimeWiringWithBackground(
		store,
		config.Settings{
			Model:              "",
			ProviderOverride:   "openai",
			ModelContextWindow: 200000,
			Timeouts:           config.Timeouts{ModelRequestSeconds: 1},
		},
		nil,
		root,
		nil,
		nil,
		nil,
		RuntimeWiringOptions{ClientFactory: RuntimeClientFactoryFunc(func(context.Context, RuntimeClientRequest) (llm.Client, error) {
			calls++
			return nil, wantErr
		})},
	)
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want factory error", err)
	}
	if calls != 1 {
		t.Fatalf("factory calls = %d, want 1", calls)
	}
	if errors.Is(err, runtime.ErrModelRequired) {
		t.Fatalf("factory error fell through to runtime provider/model validation: %v", err)
	}
}

func TestResumedMainClientUsesLockedProviderVerbosityForBothRequestPaths(t *testing.T) {
	root := t.TempDir()
	store := newRuntimeWireSession(t, root, "locked-provider-verbosity")
	lockedVerbosity := true
	if err := store.MarkModelDispatchLocked(session.LockedContract{
		Model: "operator-alias",
		ProviderContract: session.LockedProviderCapabilities{
			ProviderID:                        "custom-provider",
			SupportsResponsesAPI:              true,
			SupportsRequestInputTokenCount:    true,
			HasSupportsRequestInputTokenCount: true,
			SupportsProviderVerbosity:         &lockedVerbosity,
		},
	}); err != nil {
		t.Fatalf("lock session: %v", err)
	}

	var (
		mu       sync.Mutex
		payloads = make(map[string]map[string]any)
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		mu.Lock()
		payloads[r.URL.Path] = payload
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/responses":
			_, _ = w.Write([]byte(`{
				"id":"resp_locked_verbosity",
				"object":"response",
				"output":[{"type":"message","id":"msg_locked_verbosity","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok"}]}],
				"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}
			}`))
		case "/v1/responses/input_tokens":
			_, _ = w.Write([]byte(`{"object":"response.input_tokens","input_tokens":1}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	var mainClient llm.Client
	factory := RuntimeClientFactoryFunc(func(_ context.Context, req RuntimeClientRequest) (llm.Client, error) {
		if req.Purpose != RuntimeClientPurposeMain {
			t.Fatalf("factory purpose = %v, want main", req.Purpose)
		}
		caps := req.ProviderSettings.ProviderCapabilitiesOverride
		if caps == nil || !caps.SupportsProviderVerbosity {
			t.Fatalf("main client capabilities = %+v, want locked verbosity support", caps)
		}
		client, err := llm.NewProviderClient(llm.ProviderClientOptions{
			Provider:                     llm.Provider(req.ProviderSettings.ProviderOverride),
			Model:                        req.ProviderSettings.Model,
			Auth:                         nil,
			HTTPClient:                   server.Client(),
			OpenAIBaseURL:                req.ProviderSettings.OpenAIBaseURL,
			ModelVerbosity:               string(req.ProviderSettings.ModelVerbosity),
			Store:                        req.ProviderSettings.Store,
			ContextWindowTokens:          req.ProviderSettings.ContextWindowTokens,
			ProviderCapabilitiesOverride: caps,
		})
		if err != nil {
			return nil, err
		}
		mainClient = client
		return client, nil
	})

	wiring, err := NewRuntimeWiringWithBackground(
		store,
		config.Settings{
			Model:              "operator-alias",
			ProviderOverride:   "openai",
			OpenAIBaseURL:      server.URL + "/v1",
			ModelVerbosity:     config.ModelVerbosityHigh,
			ModelContextWindow: 200000,
			ProviderCapabilities: config.ProviderCapabilitiesOverride{
				ProviderID:                "custom-provider",
				SupportsResponsesAPI:      true,
				SupportsProviderVerbosity: false,
			},
			Reviewer: config.ReviewerSettings{Frequency: "off"},
			Timeouts: config.Timeouts{ModelRequestSeconds: 1},
		},
		nil,
		root,
		nil,
		nil,
		nil,
		RuntimeWiringOptions{ClientFactory: factory},
	)
	if err != nil {
		t.Fatalf("NewRuntimeWiringWithBackground: %v", err)
	}
	t.Cleanup(func() { _ = wiring.Close() })

	request := llm.Request{
		Model: "operator-alias",
		Items: []llm.ResponseItem{
			{Type: llm.ResponseItemTypeMessage, Role: llm.RoleUser, Content: "hello"},
		},
	}
	if _, err := mainClient.Generate(context.Background(), request); err != nil {
		t.Fatalf("generate through resumed main client: %v", err)
	}
	counter, ok := mainClient.(llm.RequestInputTokenCountClient)
	if !ok {
		t.Fatalf("main client does not support request input token counting: %T", mainClient)
	}
	if _, err := counter.CountRequestInputTokens(context.Background(), request); err != nil {
		t.Fatalf("count input tokens through resumed main client: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	for _, path := range []string{"/v1/responses", "/v1/responses/input_tokens"} {
		payload, ok := payloads[path]
		if !ok {
			t.Fatalf("expected request payload for %s", path)
		}
		text, ok := payload["text"].(map[string]any)
		if !ok {
			t.Fatalf("expected text config in %s payload, got %#v", path, payload)
		}
		if got := text["verbosity"]; got != "high" {
			t.Fatalf("%s text.verbosity = %#v, want high", path, got)
		}
	}
}
