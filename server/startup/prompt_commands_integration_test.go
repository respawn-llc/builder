package startup

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	modelstub "core/internal/testharness/pty/blackbox"
	"core/server/metadata"
	"core/server/onboarding"
	"core/shared/client"
	"core/shared/clientui"
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
	var providerUUID *uuid.UUID
	for _, provider := range onboarding.ProductionProviderCatalog() {
		if provider.HomeEntry == ".claude" {
			value := provider.UUID
			providerUUID = &value
			break
		}
	}
	if providerUUID == nil {
		t.Fatal("Claude Code provider UUID is missing")
	}
	finalizer, err := onboarding.NewFinalizer(onboarding.Options{
		PersistenceRoot: cfg.PersistenceRoot,
		WorkspaceRoot:   workspaceA,
		HomeDir:         os.Getenv("HOME"),
		SettingsPath:    cfg.Source.HomeSettingsPath,
	})
	if err != nil {
		t.Fatalf("NewFinalizer: %v", err)
	}
	if _, err := finalizer.FinalizeOnboarding(context.Background(), serverapi.OnboardingFinalizeRequest{
		CommandsImport: &serverapi.OnboardingImportSelection{
			Mode:         serverapi.OnboardingImportModeSymlinkSource,
			ProviderUUID: providerUUID,
		},
	}); err != nil {
		t.Fatalf("FinalizeOnboarding: %v", err)
	}

	providerRequests := make(chan []byte, 1)
	responseServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if modelstub.HandleInputTokenCount(w, r, 11) {
			return
		}
		body, err := io.ReadAll(r.Body)
		if err == nil {
			providerRequests <- body
		}
		modelstub.WriteCompletedResponseStream(w, "accepted", 11, 7)
	}))
	defer responseServer.Close()
	workspaceConfigDir := filepath.Join(workspaceB, config.ConfigDirName)
	if err := os.MkdirAll(workspaceConfigDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspaceConfigDir, "config.toml"), []byte(
		"model = \"gpt-5\"\nopenai_base_url = \""+responseServer.URL+"\"\n",
	), 0o600); err != nil {
		t.Fatal(err)
	}

	server, err := StartWithOptions(context.Background(), Request{
		WorkspaceRoot:         workspaceA,
		WorkspaceRootExplicit: true,
		OpenAIBaseURL:         responseServer.URL,
		OpenAIBaseURLExplicit: true,
		LoadOptions: config.LoadOptions{
			Model:         "gpt-5",
			OpenAIBaseURL: responseServer.URL,
		},
	}, envAuthHandler{}, nil, Options{})
	if err != nil {
		t.Fatalf("StartWithOptions: %v", err)
	}
	if got := server.Config().Settings.OpenAIBaseURL; got != responseServer.URL {
		t.Fatalf("OpenAIBaseURL = %q, want %q", got, responseServer.URL)
	}
	releaseServeTestPortForConfig(server.Config())
	if err := server.ServeBackground(); err != nil {
		_ = server.Close()
		t.Fatalf("ServeBackground: %v", err)
	}
	defer func() { _ = server.Close() }()

	remote, err := client.DialRemoteURLForProjectWorkspace(
		context.Background(),
		config.ServerRPCURL(server.Config()),
		binding.ProjectID,
		workspaceB,
	)
	if err != nil {
		t.Fatalf("DialRemoteURLForProjectWorkspace: %v", err)
	}
	defer func() { _ = remote.Close() }()
	catalog, err := remote.GetPromptCommandCatalog(context.Background(), serverapi.PromptCommandCatalogRequest{})
	if err != nil {
		t.Fatalf("GetPromptCommandCatalog: %v", err)
	}
	foundRemoteCommand := false
	for _, command := range catalog.Commands {
		if command.Name == "prompt:remote_demo" && command.Preview == "server body $ARGUMENTS" {
			foundRemoteCommand = true
			break
		}
	}
	if !foundRemoteCommand {
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
		ClientRequestID: runtimeids.NewRuntimeClientRequestID().String(),
		SessionID:       plan.Plan.SessionID,
		ActiveSettings:  plan.Plan.ActiveSettings,
		EnabledToolIDs:  plan.Plan.EnabledToolIDs,
		Source:          plan.Plan.Source,
	})
	if err != nil {
		t.Fatalf("ActivateSessionRuntime: %v", err)
	}
	submitID := runtimeids.NewRuntimeClientRequestID()
	preSubmitID := runtimeids.NewRuntimeClientRequestID()
	if _, err := remote.SubmitUserTurn(context.Background(), serverapi.RuntimeSubmitUserTurnRequest{
		ClientRequestID: submitID.String(),
		SessionID:       plan.Plan.SessionID,
		Input:           runtimeinput.Command("prompt:remote_demo", "hello world"),
		OperationRef: clientui.RuntimeOperationRef{
			Kind:            clientui.RuntimeOperationKindSubmit,
			ClientRequestID: submitID,
		},
		PreSubmitCompactionOperationRef: clientui.RuntimeOperationRef{
			Kind:            clientui.RuntimeOperationKindPreSubmitCompact,
			ClientRequestID: preSubmitID,
		},
	}); err != nil {
		t.Fatalf("SubmitUserTurn: %v", err)
	}
	_, _ = remote.ReleaseSessionRuntime(context.Background(), serverapi.SessionRuntimeReleaseRequest{
		ClientRequestID: runtimeids.NewRuntimeClientRequestID().String(),
		Attachment:      attachment.Attachment,
		DropOwner:       true,
		ClosePolicy:     serverapi.SessionRuntimeReleaseClosePolicyDetachOnly,
	})
	select {
	case body := <-providerRequests:
		var payload struct {
			Input []struct {
				Type    string `json:"type"`
				Role    string `json:"role"`
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"input"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("decode provider request: %v", err)
		}
		found := false
		for _, item := range payload.Input {
			if item.Type != "message" || item.Role != "user" {
				continue
			}
			for _, content := range item.Content {
				if content.Type == "input_text" && content.Text == "server body hello world" {
					found = true
				}
			}
		}
		if !found {
			t.Fatalf("provider request omitted resolved prompt body: %+v", payload.Input)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for provider request")
	}
}
