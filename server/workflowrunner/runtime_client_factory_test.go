package workflowrunner

import (
	"context"
	"errors"
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
