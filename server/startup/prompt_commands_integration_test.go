package startup

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	modelstub "core/internal/testharness/pty/blackbox"
	"core/internal/testharness/testsetup"
	"core/server/metadata"
	"core/server/onboarding"
	"core/shared/client"
	"core/shared/config"
	"core/shared/runtimeids"
	"core/shared/runtimeinput"
	"core/shared/serverapi"

	"github.com/google/uuid"
)

func TestRemotePromptCommandStartupCatalogAndInvocationUseImportedServerContent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	configureServeTestServerPort(t)
	workspaceA := t.TempDir()
	workspaceB := t.TempDir()
	cfg, err := config.Load(workspaceA, config.LoadOptions{})
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	binding, err := metadata.RegisterBinding(context.Background(), cfg.PersistenceRoot, workspaceA)
	if err != nil {
		t.Fatalf("RegisterBinding: %v", err)
	}
	store, err := metadata.Open(cfg.PersistenceRoot)
	if err != nil {
		t.Fatalf("metadata.Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.AttachWorkspaceToProject(context.Background(), binding.ProjectID, workspaceB); err != nil {
		t.Fatalf("AttachWorkspaceToProject: %v", err)
	}

	sourceRoot := filepath.Join(os.Getenv("HOME"), ".claude", "commands")
	if err := os.MkdirAll(sourceRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceRoot, "remote_demo.md"), []byte("server body $ARGUMENTS"), 0o600); err != nil {
		t.Fatal(err)
	}
	providers := onboarding.ProductionProviderCatalog()
	providerIndex := slices.IndexFunc(providers, func(provider onboarding.Provider) bool { return provider.HomeEntry == ".claude" })
	if providerIndex < 0 {
		t.Fatal("Claude Code provider UUID is missing")
	}
	providerUUID := providers[providerIndex].UUID
	finalizer, err := onboarding.NewFinalizer(onboarding.Options{PersistenceRoot: cfg.PersistenceRoot, WorkspaceRoot: workspaceA, HomeDir: os.Getenv("HOME"), SettingsPath: cfg.Source.HomeSettingsPath})
	if err != nil {
		t.Fatalf("NewFinalizer: %v", err)
	}
	if _, err := finalizer.FinalizeOnboarding(context.Background(), serverapi.OnboardingFinalizeRequest{
		CommandsImport: &serverapi.OnboardingImportSelection{
			Mode:         serverapi.OnboardingImportModeSymlinkSource,
			ProviderUUID: &providerUUID,
		},
	}); err != nil {
		t.Fatalf("FinalizeOnboarding: %v", err)
	}

	output := "accepted"
	responseServer, err := modelstub.StartResponsesStub([]modelstub.RequiredOperation{{
		ID: uuid.New(), Route: modelstub.RouteResponses, Outcome: modelstub.OutcomeStream,
		Output: &output, ResponsePhase: modelstub.NewResponsePhase(modelstub.ResponsePhaseFinal),
	}})
	if err != nil {
		t.Fatalf("StartResponsesStub: %v", err)
	}
	t.Cleanup(func() { _ = responseServer.Stop() })
	workspaceConfigDir := filepath.Join(workspaceB, config.ConfigDirName)
	if err := os.MkdirAll(workspaceConfigDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspaceConfigDir, "config.toml"), []byte(
		"model = \"gpt-5\"\nopenai_base_url = \""+responseServer.URL()+"\"\n",
	), 0o600); err != nil {
		t.Fatal(err)
	}

	server := startServeTestServer(t, Request{
		WorkspaceRoot:         workspaceA,
		WorkspaceRootExplicit: true,
		OpenAIBaseURL:         responseServer.URL(),
		OpenAIBaseURLExplicit: true,
		LoadOptions: config.LoadOptions{
			Model:         "gpt-5",
			OpenAIBaseURL: responseServer.URL(),
		},
	}, envAuthHandler{}, nil)
	if got := server.Config().Settings.OpenAIBaseURL; got != responseServer.URL() {
		t.Fatalf("OpenAIBaseURL = %q, want %q", got, responseServer.URL())
	}
	startServingTestServer(t, server)

	var remote *client.Remote
	testsetup.RequireUntil(t, time.Now().Add(5*time.Second), 10*time.Millisecond, func() bool {
		remote, err = client.DialRemoteURLForProjectWorkspace(context.Background(), config.ServerRPCURL(server.Config()), binding.ProjectID, workspaceB)
		return err == nil
	}, "DialRemoteURLForProjectWorkspace")
	defer func() { _ = remote.Close() }()
	catalog, err := remote.GetPromptCommandCatalog(context.Background(), serverapi.PromptCommandCatalogRequest{})
	if err != nil {
		t.Fatalf("GetPromptCommandCatalog: %v", err)
	}
	if !slices.ContainsFunc(catalog.Commands, func(command serverapi.PromptCommandCatalogEntry) bool {
		return command.Name == "prompt:remote_demo" && command.Preview == "server body $ARGUMENTS"
	}) {
		t.Fatalf("catalog = %+v", catalog.Commands)
	}

	plan, err := remote.PlanSession(context.Background(), serverapi.SessionPlanRequest{
		ClientRequestID: runtimeids.NewRuntimeClientRequestID().String(),
		Mode:            serverapi.SessionLaunchModeInteractive,
		Intent:          serverapi.CreateNewSessionLaunchIntent(serverapi.IndependentSessionCreateOrigin()),
	})
	if err != nil {
		t.Fatalf("PlanSession: %v", err)
	}
	attachment, err := remote.ActivateSessionRuntime(context.Background(), serverapi.SessionRuntimeActivateRequest{
		ClientRequestID:       runtimeids.NewRuntimeClientRequestID().String(),
		SessionID:             plan.Plan.SessionID,
		ActiveSettings:        plan.Plan.ActiveSettings,
		EnabledToolIDs:        plan.Plan.EnabledToolIDs,
		QuestionsEnabled:      startupBoolPointer(plan.Plan.QuestionsEnabled),
		AutoCompactionEnabled: startupBoolPointer(plan.Plan.AutoCompactionEnabled),
		Source:                plan.Plan.Source,
	})
	if err != nil {
		t.Fatalf("ActivateSessionRuntime: %v", err)
	}
	submitID := runtimeids.NewRuntimeClientRequestID()
	if _, err := remote.SubmitUserTurn(context.Background(), serverapi.RuntimeSubmitUserTurnRequest{
		ClientRequestID: submitID.String(),
		SessionID:       plan.Plan.SessionID,
		Input:           runtimeinput.Command("prompt:remote_demo", "hello world"),
	}); err != nil {
		t.Fatalf("SubmitUserTurn: %v", err)
	}
	_, _ = remote.ReleaseSessionRuntime(context.Background(), serverapi.SessionRuntimeReleaseRequest{
		ClientRequestID: runtimeids.NewRuntimeClientRequestID().String(),
		Attachment:      attachment.Attachment,
		DropOwner:       true,
		ClosePolicy:     serverapi.SessionRuntimeReleaseClosePolicyDetachOnly,
	})
	var body json.RawMessage
	testsetup.RequireUntil(t, time.Now().Add(10*time.Second), 10*time.Millisecond, func() bool {
		for _, call := range responseServer.Snapshot().Observed {
			if call.Route == modelstub.RouteResponses {
				body = append(json.RawMessage(nil), call.Body...)
				return true
			}
		}
		return false
	}, "timed out waiting for provider request")
	type responseContent struct{ Type, Text string }
	type responseInput struct {
		Type, Role string
		Content    []responseContent
	}
	var payload struct{ Input []responseInput }
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode provider request: %v", err)
	}
	if !slices.ContainsFunc(payload.Input, func(item responseInput) bool {
		return item.Type == "message" && item.Role == "user" &&
			slices.ContainsFunc(item.Content, func(content responseContent) bool {
				return content.Type == "input_text" && content.Text == "server body hello world"
			})
	}) {
		t.Fatalf("provider request omitted resolved prompt body: %+v", payload.Input)
	}
}

func startupBoolPointer(value bool) *bool { return &value }
