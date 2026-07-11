package workflowrunner

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"core/server/launch"
	"core/server/llm"
	"core/server/metadata"
	"core/server/registry"
	"core/server/runtimewire"
	"core/server/session"
	"core/server/sessionruntime"
	"core/server/workflowstore"
	"core/shared/config"
	"core/shared/toolspec"
)

func TestWorkflowProviderCapabilitiesUseRuntimeClientFactory(t *testing.T) {
	t.Parallel()

	store := newWorkflowFactorySession(t)
	var got runtimewire.RuntimeClientRequest
	client := NewScriptedClient(llm.ProviderCapabilities{ProviderID: "scripted-workflow", SupportsResponsesAPI: true})
	starter := &Starter{runtimeClientFactory: runtimewire.RuntimeClientFactoryFunc(func(_ context.Context, req runtimewire.RuntimeClientRequest) (llm.Client, error) {
		got = req
		return client, nil
	})}

	caps, resolvedClient, err := starter.workflowProviderCapabilities(context.Background(), launch.SessionPlan{
		Store:          store,
		ActiveSettings: config.Settings{Model: "gpt-5", ModelContextWindow: 200000},
		EnabledTools:   []toolspec.ID{toolspec.ToolExecCommand},
		WorkspaceRoot:  t.TempDir(),
		Source:         config.SourceReport{Sources: map[string]string{"model": "test"}},
	}, nil)
	if err != nil {
		t.Fatalf("workflowProviderCapabilities: %v", err)
	}
	if caps.ProviderID != "scripted-workflow" || resolvedClient != client {
		t.Fatalf("caps/client = %+v/%T, want factory client", caps, resolvedClient)
	}
	if got.Purpose != runtimewire.RuntimeClientPurposeWorkflow || got.SessionID != store.Meta().SessionID || got.ProviderSettings.Model != "gpt-5" {
		t.Fatalf("factory request = %+v, want workflow purpose and plan data", got)
	}
}

func TestWorkflowRuntimeClientFactoryErrorDoesNotFallbackToProvider(t *testing.T) {
	t.Parallel()

	store := newWorkflowFactorySession(t)
	wantErr := errors.New("workflow factory failed")
	starter := &Starter{runtimeClientFactory: runtimewire.RuntimeClientFactoryFunc(func(context.Context, runtimewire.RuntimeClientRequest) (llm.Client, error) {
		return nil, wantErr
	})}

	_, _, err := starter.workflowProviderCapabilities(context.Background(), launch.SessionPlan{
		Store:          store,
		ActiveSettings: config.Settings{Model: "", ProviderOverride: "openai"},
	}, nil)
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want factory error", err)
	}
}

func TestWorkflowRuntimeClientFactoryRejectsNilClient(t *testing.T) {
	t.Parallel()

	store := newWorkflowFactorySession(t)
	starter := &Starter{runtimeClientFactory: runtimewire.RuntimeClientFactoryFunc(func(context.Context, runtimewire.RuntimeClientRequest) (llm.Client, error) {
		return nil, nil
	})}

	_, _, err := starter.workflowProviderCapabilities(context.Background(), launch.SessionPlan{
		Store:          store,
		ActiveSettings: config.Settings{Model: "gpt-5"},
	}, nil)
	if err == nil {
		t.Fatal("workflowProviderCapabilities succeeded with nil factory client")
	}
}

func TestWorkflowProviderClientPreservesLockedVerbosityAcrossConfigChanges(t *testing.T) {
	for _, tt := range []struct {
		name    string
		factory bool
	}{
		{name: "direct"},
		{name: "runtime client factory", factory: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			recorder := newWorkflowProviderPayloadRecorder(t)
			plan := newLockedWorkflowProviderVerbosityPlan(t, recorder.server.URL)
			starter := &Starter{}
			if tt.factory {
				starter.runtimeClientFactory = runtimewire.RuntimeClientFactoryFunc(func(_ context.Context, req runtimewire.RuntimeClientRequest) (llm.Client, error) {
					caps := req.ProviderSettings.ProviderCapabilitiesOverride
					if caps == nil || !caps.SupportsProviderVerbosity {
						t.Fatalf("factory capabilities = %+v, want locked verbosity support", caps)
					}
					return llm.NewProviderClient(llm.ProviderClientOptions{
						Provider:                     llm.Provider(req.ProviderSettings.ProviderOverride),
						Model:                        req.ProviderSettings.Model,
						HTTPClient:                   recorder.server.Client(),
						OpenAIBaseURL:                req.ProviderSettings.OpenAIBaseURL,
						ModelVerbosity:               string(req.ProviderSettings.ModelVerbosity),
						Store:                        req.ProviderSettings.Store,
						ContextWindowTokens:          req.ProviderSettings.ContextWindowTokens,
						ProviderCapabilitiesOverride: caps,
					})
				})
			}

			client, err := starter.newWorkflowProviderClient(context.Background(), plan)
			if err != nil {
				t.Fatalf("newWorkflowProviderClient: %v", err)
			}
			request := llm.Request{
				Model: "operator-alias",
				Items: []llm.ResponseItem{
					{Type: llm.ResponseItemTypeMessage, Role: llm.RoleUser, Content: "hello"},
				},
			}
			if _, err := client.Generate(context.Background(), request); err != nil {
				t.Fatalf("generate: %v", err)
			}
			counter, ok := client.(llm.RequestInputTokenCountClient)
			if !ok {
				t.Fatalf("workflow client does not support input token counting: %T", client)
			}
			if _, err := counter.CountRequestInputTokens(context.Background(), request); err != nil {
				t.Fatalf("count input tokens: %v", err)
			}
			recorder.assertVerbosity(t)
		})
	}
}

func TestNewStarterRejectsLegacyAndRuntimeClientFactoriesTogether(t *testing.T) {
	t.Parallel()

	metadataStore, workflowStore, sessionRuntime := newStarterFactoryStores(t)
	_, err := NewStarter(config.App{PersistenceRoot: t.TempDir()}, metadataStore, workflowStore, nil, nil, nil, StarterOptions{
		ClientFactory:        func(SchedulerStartRunRequest) llm.Client { return NewScriptedClient(llm.ProviderCapabilities{}) },
		RuntimeClientFactory: runtimewire.RuntimeClientFactoryFunc(func(context.Context, runtimewire.RuntimeClientRequest) (llm.Client, error) { return nil, nil }),
		SessionRuntime:       sessionRuntime,
	})
	if !errors.Is(err, runtimewire.ErrRuntimeClientFactoryConflict) {
		t.Fatalf("error = %v, want factory conflict", err)
	}
}

func newWorkflowFactorySession(t *testing.T) *session.Store {
	t.Helper()
	store, err := session.Create(t.TempDir(), "factory", t.TempDir())
	if err != nil {
		t.Fatalf("session.Create: %v", err)
	}
	return store
}

type workflowProviderPayloadRecorder struct {
	server   *httptest.Server
	mu       sync.Mutex
	payloads map[string]map[string]any
}

func newWorkflowProviderPayloadRecorder(t *testing.T) *workflowProviderPayloadRecorder {
	t.Helper()
	recorder := &workflowProviderPayloadRecorder{payloads: make(map[string]map[string]any)}
	recorder.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		recorder.mu.Lock()
		recorder.payloads[r.URL.Path] = payload
		recorder.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/responses":
			_, _ = w.Write([]byte(`{
				"id":"resp_workflow_locked_verbosity",
				"object":"response",
				"output":[{"type":"message","id":"msg_workflow_locked_verbosity","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok"}]}],
				"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}
			}`))
		case "/v1/responses/input_tokens":
			_, _ = w.Write([]byte(`{"object":"response.input_tokens","input_tokens":1}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(recorder.server.Close)
	return recorder
}

func (r *workflowProviderPayloadRecorder) assertVerbosity(t *testing.T) {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, path := range []string{"/v1/responses", "/v1/responses/input_tokens"} {
		payload, ok := r.payloads[path]
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

func newLockedWorkflowProviderVerbosityPlan(t *testing.T, baseURL string) launch.SessionPlan {
	t.Helper()
	store := newWorkflowFactorySession(t)
	lockedVerbosity := true
	if err := store.MarkModelDispatchLocked(session.LockedContract{
		Model: "operator-alias",
		ProviderContract: session.LockedProviderCapabilities{
			ProviderID:                        "workflow-provider",
			SupportsResponsesAPI:              true,
			SupportsRequestInputTokenCount:    true,
			HasSupportsRequestInputTokenCount: true,
			SupportsProviderVerbosity:         &lockedVerbosity,
		},
	}); err != nil {
		t.Fatalf("lock workflow session: %v", err)
	}
	return launch.SessionPlan{
		Store: store,
		ActiveSettings: config.Settings{
			Model:              "operator-alias",
			ProviderOverride:   "openai",
			OpenAIBaseURL:      baseURL + "/v1",
			ModelVerbosity:     config.ModelVerbosityHigh,
			ModelContextWindow: 200000,
			ProviderCapabilities: config.ProviderCapabilitiesOverride{
				ProviderID:                "workflow-provider",
				SupportsResponsesAPI:      true,
				SupportsProviderVerbosity: false,
			},
			Timeouts: config.Timeouts{ModelRequestSeconds: 1},
		},
	}
}

func newStarterFactoryStores(t *testing.T) (*metadata.Store, *workflowstore.Store, *sessionruntime.Service) {
	t.Helper()
	root := t.TempDir()
	metadataStore, err := metadata.Open(root)
	if err != nil {
		t.Fatalf("metadata.Open: %v", err)
	}
	t.Cleanup(func() { _ = metadataStore.Close() })
	workflowStore, err := workflowstore.New(metadataStore)
	if err != nil {
		t.Fatalf("workflowstore.New: %v", err)
	}
	sessionRuntime := sessionruntime.NewService(root, metadataStore, nil, nil, nil, nil, registry.NewRuntimeRegistry(), registry.NewSessionStoreRegistry(), metadataStore.AuthoritativeSessionStoreOptions()...)
	return metadataStore, workflowStore, sessionRuntime
}
