package runtimecontrolfixture

import (
	"context"
	"testing"

	"core/internal/testharness/toolfixture"
	"core/server/llm"
	"core/server/metadata"
	"core/server/runtime"
	"core/server/runtimecontrol"
	"core/server/runtimewire"
	"core/server/session"
	"core/server/session/sessiontest"
	"core/server/sessionruntime"
	"core/server/tools"
	"core/shared/config"
	"core/shared/runtimeids"
	"core/shared/sessioncontract"
	"core/shared/textutil"
	"core/shared/toolspec"
)

type Options struct {
	Client       llm.Client
	Registry     *tools.Registry
	Runtime      runtime.Config
	Persistence  *sessiontest.Persistence
	StoreOptions []session.StoreOption
}

type Fixture struct {
	Store       *session.Store
	Engine      *runtime.Engine
	Service     *runtimecontrol.Service
	Persistence *sessiontest.Persistence
	Authority   *sessionruntime.Authority
}

func New(t *testing.T, options Options) *Fixture {
	t.Helper()
	persistence := options.Persistence
	if persistence == nil {
		persistence = sessiontest.NewPersistence()
	}
	store, err := session.Create(
		t.TempDir(),
		"workspace",
		t.TempDir(),
		sessioncontract.SessionCategoryMain,
		append(persistence.Options(), options.StoreOptions...)...,
	)
	if err != nil {
		t.Fatalf("create Session: %v", err)
	}
	client := options.Client
	if client == nil {
		client = inertClient{}
	}
	registry := options.Registry
	if registry == nil {
		registry = toolfixture.NewRegistry(t)
	}
	settings := config.DefaultOnboardingSettings()
	settings.ProviderOverride = "openai"
	settings.Reviewer.Frequency = "off"
	settings.CompactionMode = config.CompactionModeNative
	if options.Runtime.Model != "" {
		settings.Model = options.Runtime.Model
	}
	enabledTools := append([]toolspec.ID(nil), options.Runtime.EnabledTools...)
	if _, ok := registry.Get(toolspec.ToolExecCommand); ok {
		enabledTools = append(enabledTools, toolspec.ToolExecCommand)
	}
	filesystem, err := runtimewire.NewFilesystemContext(
		store.Meta().WorkspaceRoot,
		store.Meta().WorkspaceRoot,
		metadata.ProjectWorkspaceBoundary{ProjectID: "test-project"},
	)
	if err != nil {
		t.Fatalf("create filesystem context: %v", err)
	}
	plan, err := sessionruntime.NewAgentRuntimePlan(sessionruntime.AgentRuntimePlanOptions{
		Settings:                     settings,
		EnabledTools:                 enabledTools,
		QuestionsEnabled:             textutil.Value(runtime.DefaultQuestionsEnabled),
		AutoCompactionEnabled:        textutil.Value(runtime.DefaultAutoCompactionEnabled),
		FilesystemContext:            filesystem,
		Client:                       client,
		ProviderCapabilitiesOverride: options.Runtime.ProviderCapabilitiesOverride,
		CurrentNodeExecution:         options.Runtime.CurrentNodeExecution,
		OnEvent:                      options.Runtime.OnEvent,
	})
	if err != nil {
		t.Fatalf("create runtime plan: %v", err)
	}
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{
		PersistenceRoot: t.TempDir(),
		StoreOptions:    append(persistence.Options(), options.StoreOptions...),
	})
	sessionID, err := runtimeids.ParseSessionID(store.Meta().SessionID)
	if err != nil {
		t.Fatalf("parse Session ID: %v", err)
	}
	if _, err := authority.OpenRuntime(t.Context(), sessionruntime.RuntimeOpenRequest{
		SessionID: sessionID,
		OwnerID:   "runtime-control-contract",
		Runtime:   &plan,
	}); err != nil {
		t.Fatalf("open runtime: %v", err)
	}
	t.Cleanup(func() {
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close runtime authority: %v", err)
		}
	})
	var engine *runtime.Engine
	if err := authority.WithCurrentRuntime(t.Context(), sessionID, func(_ context.Context, current *runtime.Engine) error {
		engine = current
		return nil
	}); err != nil {
		t.Fatalf("resolve runtime: %v", err)
	}
	descriptor, err := session.NewOpenSessionDescriptor(sessionID)
	if err != nil {
		t.Fatalf("open Session descriptor: %v", err)
	}
	if err := authority.WithSessionStore(t.Context(), descriptor, func(_ context.Context, current *session.Store) error {
		store = current
		return nil
	}); err != nil {
		t.Fatalf("resolve authoritative Session Store: %v", err)
	}
	return &Fixture{
		Store:       store,
		Engine:      engine,
		Service:     runtimecontrol.NewService(authority),
		Persistence: persistence,
		Authority:   authority,
	}
}

type inertClient struct{}

func (inertClient) Generate(context.Context, llm.Request) (llm.Response, error) {
	return llm.Response{}, nil
}

func (inertClient) Compact(context.Context, llm.CompactionRequest) (llm.CompactionResponse, error) {
	return llm.CompactionResponse{}, nil
}

func (inertClient) ProviderCapabilities(context.Context) (llm.ProviderCapabilities, error) {
	return llm.ProviderCapabilities{ProviderID: "openai", SupportsResponsesAPI: true, SupportsResponsesCompact: true}, nil
}
