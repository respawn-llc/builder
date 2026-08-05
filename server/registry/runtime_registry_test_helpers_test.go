package registry

import (
	"context"
	"io"
	"testing"

	testharness "core/internal/testharness/testsetup"
	"core/server/llm"
	"core/server/runtime"
	"core/server/sessionruntime"
	askquestion "core/server/tools"
	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

type registryRuntimeFakeClient struct{}
type registryRetention chan struct{}

func (retention registryRetention) Close() error {
	close(retention)
	return nil
}

const registryTestStepID = "22222222-2222-4222-8222-222222222222"

func registryTestResourceRef(sessionID string) runtimeids.SessionResourceRef {
	id, err := runtimeids.ParseSessionID(sessionID)
	if err != nil {
		panic(err)
	}
	ref, err := runtimeids.NewSessionResourceRef(id, 1)
	if err != nil {
		panic(err)
	}
	return ref
}

func registryTestResource(ref runtimeids.SessionResourceRef) sessionruntime.AgentResourceDescriptor {
	return sessionruntime.AgentResourceDescriptor{Ref: ref, State: sessionruntime.AgentResourceReady}
}

func (registryRuntimeFakeClient) Generate(context.Context, llm.Request) (llm.Response, error) {
	return llm.Response{}, nil
}

func (registryRuntimeFakeClient) ProviderCapabilities(context.Context) (llm.ProviderCapabilities, error) {
	return llm.ProviderCapabilities{ProviderID: "fake", SupportsResponsesAPI: true}, nil
}

func registerResource(t *testing.T, registry *RuntimeRegistry, ref runtimeids.SessionResourceRef, engine *runtime.Engine) {
	t.Helper()
	if err := registry.ResourceReady(context.Background(), registryTestResource(ref), engine, func() (io.Closer, error) {
		return make(registryRetention), nil
	}); err != nil {
		t.Fatalf("register authority runtime resource %v: %v", ref, err)
	}
}

func registerReady(t *testing.T, registry *RuntimeRegistry, sessionID string, engine *runtime.Engine) {
	registerResource(t, registry, registryTestResourceRef(sessionID), engine)
}

func closeRuntime(registry *RuntimeRegistry, sessionID string, _ *runtime.Engine) {
	_ = registry.ResourceDraining(context.Background(), registryTestResource(registryTestResourceRef(sessionID)))
}

func newRegistryTestRuntime(t *testing.T, onEvent func(runtime.Event)) *runtime.Engine {
	t.Helper()
	return newRegistryRuntime(t, registryRuntimeFakeClient{}, askquestion.NewRegistry(), runtime.Config{Model: "gpt-5", ThinkingLevel: "medium"}, func(_ *runtime.Engine, evt runtime.Event) {
		if onEvent != nil {
			onEvent(evt)
		}
	})
}

func newRegistryRuntime(t *testing.T, client llm.Client, toolRegistry *askquestion.Registry, cfg runtime.Config, onEvent func(*runtime.Engine, runtime.Event)) *runtime.Engine {
	t.Helper()
	store := newRegistryTestSession(t, t.TempDir(), "workspace", t.TempDir())
	eventLog, err := store.MaterializeEventLog()
	if err != nil {
		t.Fatalf("materialize event log: %v", err)
	}
	var engine *runtime.Engine
	cfg.OnEvent = func(evt runtime.Event) {
		if onEvent != nil {
			onEvent(engine, evt)
		}
	}
	engine, err = runtime.New(store, eventLog, client, toolRegistry, cfg)
	if err != nil {
		t.Fatalf("new runtime: %v", err)
	}
	t.Cleanup(func() { _ = engine.Close() })
	return engine
}

func nextRegistryAttentionEvent(t *testing.T, sub serverapi.AttentionNotificationSubscription) clientui.AttentionNotificationEvent {
	t.Helper()
	event, err := sub.Next(context.Background())
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	return event
}

func registryTestWorkflowID() *runtimeids.WorkflowID {
	workflowID := testharness.WorkflowIDValue("registry-workflow-task")
	return &workflowID
}
