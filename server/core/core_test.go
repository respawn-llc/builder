package core

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"core/server/auth"
	serverbootstrap "core/server/bootstrap"
	"core/server/metadata"
	"core/shared/clientui"
	brand "core/shared/config"
	"core/shared/protocol"
	"core/shared/serverapi"
)

func TestNewBuildsReusableServerCore(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	t.Setenv("HOME", home)

	resolved, err := serverbootstrap.ResolveConfig(serverbootstrap.Request{WorkspaceRoot: workspace})
	if err != nil {
		t.Fatalf("ResolveConfig: %v", err)
	}
	if _, err := metadata.RegisterBinding(context.Background(), resolved.Config.PersistenceRoot, resolved.Config.WorkspaceRoot); err != nil {
		t.Fatalf("RegisterBinding: %v", err)
	}
	authSupport, err := serverbootstrap.BuildAuthSupport(auth.NewMemoryStore(auth.EmptyState()), nil, nil)
	if err != nil {
		t.Fatalf("BuildAuthSupport: %v", err)
	}
	runtimeSupport, err := serverbootstrap.BuildRuntimeSupport(resolved.Config)
	if err != nil {
		t.Fatalf("BuildRuntimeSupport: %v", err)
	}
	t.Cleanup(func() { _ = runtimeSupport.Background.Close() })

	appCore, err := New(resolved.Config, authSupport, runtimeSupport)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if appCore.Config().WorkspaceRoot == "" {
		t.Fatal("expected workspace root")
	}
	if appCore.ContainerDir() == "" {
		t.Fatal("expected container dir")
	}
	if appCore.ProjectID() == "" {
		t.Fatal("expected project id")
	}
	if appCore.AuthManager() == nil {
		t.Fatal("expected auth manager")
	}
	if appCore.Background() == nil {
		t.Fatal("expected background manager")
	}
	if appCore.ProjectViewClient() == nil || appCore.ProcessViewClient() == nil || appCore.ProcessOutputClient() == nil || appCore.SessionLaunchClient() == nil || appCore.SessionViewClient() == nil || appCore.SessionLifecycleClient() == nil || appCore.SessionActivityClient() == nil || appCore.SessionTranscriptClient() == nil || appCore.RunPromptClient() == nil {
		t.Fatal("expected core clients to be wired")
	}
	if appCore.CapabilityFactsClient() == nil {
		t.Fatal("expected capability facts client to be wired")
	}
	if _, err := appCore.ProjectViewClient().ListProjects(context.Background(), serverapi.ProjectListRequest{}); err != nil {
		t.Fatalf("ListProjects via core client: %v", err)
	}
	facts, err := appCore.CapabilityFactsClient().GetCapabilityFacts(context.Background(), serverapi.CapabilityFactsRequest{})
	if err != nil {
		t.Fatalf("GetCapabilityFacts via core client: %v", err)
	}
	if facts.Defaults.PrimaryModelID == "" {
		t.Fatalf("capability facts missing defaults: %+v", facts)
	}
}

func TestProtocolIdentityHasNoCapabilityFactsFlag(t *testing.T) {
	if _, ok := reflect.TypeOf(protocol.CapabilityFlags{}).FieldByName("CapabilityFacts"); ok {
		t.Fatal("capability facts must be signaled by protocol version/route availability, not a handshake capability flag")
	}
}

func TestNewProvidesRegistrationSafeClientsForUnregisteredWorkspace(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	t.Setenv("HOME", home)

	resolved, err := serverbootstrap.ResolveConfig(serverbootstrap.Request{WorkspaceRoot: workspace})
	if err != nil {
		t.Fatalf("ResolveConfig: %v", err)
	}
	authSupport, err := serverbootstrap.BuildAuthSupport(auth.NewMemoryStore(auth.EmptyState()), nil, nil)
	if err != nil {
		t.Fatalf("BuildAuthSupport: %v", err)
	}
	runtimeSupport, err := serverbootstrap.BuildRuntimeSupport(resolved.Config)
	if err != nil {
		t.Fatalf("BuildRuntimeSupport: %v", err)
	}
	t.Cleanup(func() { _ = runtimeSupport.Background.Close() })

	appCore, err := New(resolved.Config, authSupport, runtimeSupport)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if got := appCore.ProjectID(); got != "" {
		t.Fatalf("project id = %q, want empty for unregistered workspace", got)
	}
	if appCore.SessionLaunchClient() == nil {
		t.Fatal("expected session launch client stub")
	}
	if appCore.RunPromptClient() == nil {
		t.Fatal("expected run prompt client stub")
	}
	_, err = appCore.SessionLaunchClient().PlanSession(context.Background(), serverapi.SessionPlanRequest{})
	if !errors.Is(err, serverapi.ErrWorkspaceNotRegistered) {
		t.Fatalf("PlanSession error = %v, want ErrWorkspaceNotRegistered", err)
	}
	_, err = appCore.RunPromptClient().RunPrompt(context.Background(), serverapi.RunPromptRequest{}, nil)
	if !errors.Is(err, serverapi.ErrWorkspaceNotRegistered) {
		t.Fatalf("RunPrompt error = %v, want ErrWorkspaceNotRegistered", err)
	}
	if _, err := appCore.ProjectViewClient().ListProjects(context.Background(), serverapi.ProjectListRequest{}); err != nil {
		t.Fatalf("ListProjects via core client: %v", err)
	}
}

func TestNewRejectsSecondCoreForSamePersistenceRoot(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	t.Setenv("HOME", home)

	resolved, err := serverbootstrap.ResolveConfig(serverbootstrap.Request{WorkspaceRoot: workspace})
	if err != nil {
		t.Fatalf("ResolveConfig: %v", err)
	}
	authSupportA, err := serverbootstrap.BuildAuthSupport(auth.NewMemoryStore(auth.EmptyState()), nil, nil)
	if err != nil {
		t.Fatalf("BuildAuthSupport A: %v", err)
	}
	runtimeSupportA, err := serverbootstrap.BuildRuntimeSupport(resolved.Config)
	if err != nil {
		t.Fatalf("BuildRuntimeSupport A: %v", err)
	}
	t.Cleanup(func() { _ = runtimeSupportA.Background.Close() })

	first, err := New(resolved.Config, authSupportA, runtimeSupportA)
	if err != nil {
		t.Fatalf("New first: %v", err)
	}
	t.Cleanup(func() { _ = first.Close() })
	generatedSkillsRoot := filepath.Join(home, brand.ConfigDirName, ".generated", "skills")
	if entries, err := os.ReadDir(generatedSkillsRoot); err != nil {
		t.Fatalf("expected first core to seed generated skills: %v", err)
	} else if len(entries) == 0 {
		t.Fatal("expected first core to seed at least one generated skill")
	}

	authSupportB, err := serverbootstrap.BuildAuthSupport(auth.NewMemoryStore(auth.EmptyState()), nil, nil)
	if err != nil {
		t.Fatalf("BuildAuthSupport B: %v", err)
	}
	runtimeSupportB, err := serverbootstrap.BuildRuntimeSupport(resolved.Config)
	if err != nil {
		t.Fatalf("BuildRuntimeSupport B: %v", err)
	}
	t.Cleanup(func() { _ = runtimeSupportB.Background.Close() })

	_, err = New(resolved.Config, authSupportB, runtimeSupportB)
	if !errors.Is(err, ErrPersistenceRootBusy) {
		t.Fatalf("New second error = %v, want ErrPersistenceRootBusy", err)
	}
}

func TestSessionLaunchClientForProjectWorkspaceRejectsMissingProject(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	t.Setenv("HOME", home)

	resolved, err := serverbootstrap.ResolveConfig(serverbootstrap.Request{WorkspaceRoot: workspace})
	if err != nil {
		t.Fatalf("ResolveConfig: %v", err)
	}
	authSupport, err := serverbootstrap.BuildAuthSupport(auth.NewMemoryStore(auth.EmptyState()), nil, nil)
	if err != nil {
		t.Fatalf("BuildAuthSupport: %v", err)
	}
	runtimeSupport, err := serverbootstrap.BuildRuntimeSupport(resolved.Config)
	if err != nil {
		t.Fatalf("BuildRuntimeSupport: %v", err)
	}
	t.Cleanup(func() { _ = runtimeSupport.Background.Close() })

	appCore, err := New(resolved.Config, authSupport, runtimeSupport)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = appCore.Close() })

	_, err = appCore.SessionLaunchClientForProjectWorkspace(context.Background(), "project-missing", workspace)
	if !errors.Is(err, serverapi.ErrProjectNotFound) {
		t.Fatalf("SessionLaunchClientForProjectWorkspace error = %v, want ErrProjectNotFound", err)
	}
}

func TestSessionLaunchClientForProjectWorkspaceRejectsUnavailableProjectRoot(t *testing.T) {
	home := t.TempDir()
	workspaceA := t.TempDir()
	workspaceB := t.TempDir()
	t.Setenv("HOME", home)

	resolvedA, err := serverbootstrap.ResolveConfig(serverbootstrap.Request{WorkspaceRoot: workspaceA})
	if err != nil {
		t.Fatalf("ResolveConfig A: %v", err)
	}
	binding, err := metadata.RegisterBinding(context.Background(), resolvedA.Config.PersistenceRoot, resolvedA.Config.WorkspaceRoot)
	if err != nil {
		t.Fatalf("RegisterBinding: %v", err)
	}
	missingRoot := filepath.Join(t.TempDir(), "workspace-moved")
	if err := os.Rename(workspaceA, missingRoot); err != nil {
		t.Fatalf("Rename workspaceA: %v", err)
	}

	resolvedB, err := serverbootstrap.ResolveConfig(serverbootstrap.Request{WorkspaceRoot: workspaceB})
	if err != nil {
		t.Fatalf("ResolveConfig B: %v", err)
	}
	authSupport, err := serverbootstrap.BuildAuthSupport(auth.NewMemoryStore(auth.EmptyState()), nil, nil)
	if err != nil {
		t.Fatalf("BuildAuthSupport: %v", err)
	}
	runtimeSupport, err := serverbootstrap.BuildRuntimeSupport(resolvedB.Config)
	if err != nil {
		t.Fatalf("BuildRuntimeSupport: %v", err)
	}
	t.Cleanup(func() { _ = runtimeSupport.Background.Close() })

	appCore, err := New(resolvedB.Config, authSupport, runtimeSupport)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = appCore.Close() })

	_, err = appCore.SessionLaunchClientForProjectWorkspace(context.Background(), binding.ProjectID, workspaceB)
	if !errors.Is(err, serverapi.ErrProjectUnavailable) {
		t.Fatalf("SessionLaunchClientForProjectWorkspace error = %v, want ErrProjectUnavailable", err)
	}
	unavailable, ok := serverapi.AsProjectUnavailable(err)
	if !ok {
		t.Fatalf("expected ProjectUnavailableError, got %v", err)
	}
	if unavailable.ProjectID != binding.ProjectID || unavailable.Availability != clientui.ProjectAvailabilityMissing {
		t.Fatalf("unexpected unavailable project: %+v", unavailable)
	}
}

func TestSessionLaunchClientForProjectWorkspaceReplaysForceNewSessionAcrossClientInstances(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	t.Setenv("HOME", home)

	resolved, err := serverbootstrap.ResolveConfig(serverbootstrap.Request{WorkspaceRoot: workspace})
	if err != nil {
		t.Fatalf("ResolveConfig: %v", err)
	}
	binding, err := metadata.RegisterBinding(context.Background(), resolved.Config.PersistenceRoot, resolved.Config.WorkspaceRoot)
	if err != nil {
		t.Fatalf("RegisterBinding: %v", err)
	}
	authSupport, err := serverbootstrap.BuildAuthSupport(auth.NewMemoryStore(auth.EmptyState()), nil, nil)
	if err != nil {
		t.Fatalf("BuildAuthSupport: %v", err)
	}
	runtimeSupport, err := serverbootstrap.BuildRuntimeSupport(resolved.Config)
	if err != nil {
		t.Fatalf("BuildRuntimeSupport: %v", err)
	}
	t.Cleanup(func() { _ = runtimeSupport.Background.Close() })

	appCore, err := New(resolved.Config, authSupport, runtimeSupport)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = appCore.Close() })

	firstClient, err := appCore.SessionLaunchClientForProjectWorkspace(context.Background(), binding.ProjectID, workspace)
	if err != nil {
		t.Fatalf("SessionLaunchClientForProjectWorkspace first: %v", err)
	}
	secondClient, err := appCore.SessionLaunchClientForProjectWorkspace(context.Background(), binding.ProjectID, workspace)
	if err != nil {
		t.Fatalf("SessionLaunchClientForProjectWorkspace second: %v", err)
	}
	req := serverapi.SessionPlanRequest{
		ClientRequestID: "req-1",
		Mode:            serverapi.SessionLaunchModeInteractive,
		ForceNewSession: true,
	}
	firstPlan, err := firstClient.PlanSession(context.Background(), req)
	if err != nil {
		t.Fatalf("PlanSession first: %v", err)
	}
	secondPlan, err := secondClient.PlanSession(context.Background(), req)
	if err != nil {
		t.Fatalf("PlanSession second: %v", err)
	}
	if firstPlan.Plan.SessionID != secondPlan.Plan.SessionID {
		t.Fatalf("session ids = %q and %q, want stable replay", firstPlan.Plan.SessionID, secondPlan.Plan.SessionID)
	}
}

func TestSessionLaunchClientForProjectWorkspaceUsesWorkspaceLocalConfig(t *testing.T) {
	home := t.TempDir()
	workspaceA := t.TempDir()
	workspaceB := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(workspaceB, brand.ConfigDirName), 0o755); err != nil {
		t.Fatalf("create workspace config dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspaceB, brand.ConfigDirName, "config.toml"), []byte("model = \"workspace-b-model\"\nthinking_level = \"high\"\n"), 0o644); err != nil {
		t.Fatalf("write workspace config: %v", err)
	}

	resolvedA, err := serverbootstrap.ResolveConfig(serverbootstrap.Request{WorkspaceRoot: workspaceA})
	if err != nil {
		t.Fatalf("ResolveConfig A: %v", err)
	}
	resolvedB, err := serverbootstrap.ResolveConfig(serverbootstrap.Request{WorkspaceRoot: workspaceB})
	if err != nil {
		t.Fatalf("ResolveConfig B: %v", err)
	}
	bindingB, err := metadata.RegisterBinding(context.Background(), resolvedB.Config.PersistenceRoot, resolvedB.Config.WorkspaceRoot)
	if err != nil {
		t.Fatalf("RegisterBinding B: %v", err)
	}
	authSupport, err := serverbootstrap.BuildAuthSupport(auth.NewMemoryStore(auth.EmptyState()), nil, nil)
	if err != nil {
		t.Fatalf("BuildAuthSupport: %v", err)
	}
	runtimeSupport, err := serverbootstrap.BuildRuntimeSupport(resolvedA.Config)
	if err != nil {
		t.Fatalf("BuildRuntimeSupport: %v", err)
	}
	t.Cleanup(func() { _ = runtimeSupport.Background.Close() })

	appCore, err := New(resolvedA.Config, authSupport, runtimeSupport)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = appCore.Close() })

	client, err := appCore.SessionLaunchClientForProjectWorkspace(context.Background(), bindingB.ProjectID, workspaceB)
	if err != nil {
		t.Fatalf("SessionLaunchClientForProjectWorkspace: %v", err)
	}
	plan, err := client.PlanSession(context.Background(), serverapi.SessionPlanRequest{ClientRequestID: "req-1", Mode: serverapi.SessionLaunchModeInteractive, ForceNewSession: true})
	if err != nil {
		t.Fatalf("PlanSession: %v", err)
	}
	if plan.Plan.ActiveSettings.Model != "workspace-b-model" || plan.Plan.ActiveSettings.ThinkingLevel != "high" {
		t.Fatalf("unexpected active settings: %+v", plan.Plan.ActiveSettings)
	}
}

func TestRunPromptClientForProjectWorkspaceReplaysHeadlessRunAcrossClientInstances(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	t.Setenv("HOME", home)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if handleTestOpenAIInputTokenCount(w, r, 1) {
			return
		}
		if r.URL.Path != "/responses" {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got == "" {
			t.Fatal("expected authorization header")
		}
		writeTestOpenAICompletedResponseStream(w, "ok", 1, 1)
	}))
	defer server.Close()

	resolved, err := serverbootstrap.ResolveConfig(serverbootstrap.Request{WorkspaceRoot: workspace})
	if err != nil {
		t.Fatalf("ResolveConfig: %v", err)
	}
	resolved.Config.Settings.Model = "gpt-5"
	resolved.Config.Settings.OpenAIBaseURL = server.URL
	binding, err := metadata.RegisterBinding(context.Background(), resolved.Config.PersistenceRoot, resolved.Config.WorkspaceRoot)
	if err != nil {
		t.Fatalf("RegisterBinding: %v", err)
	}
	authSupport, err := serverbootstrap.BuildAuthSupport(auth.NewMemoryStore(auth.State{
		Scope:  auth.ScopeGlobal,
		Method: auth.Method{Type: auth.MethodAPIKey, APIKey: &auth.APIKeyMethod{Key: "test-key"}},
	}), nil, nil)
	if err != nil {
		t.Fatalf("BuildAuthSupport: %v", err)
	}
	runtimeSupport, err := serverbootstrap.BuildRuntimeSupport(resolved.Config)
	if err != nil {
		t.Fatalf("BuildRuntimeSupport: %v", err)
	}
	t.Cleanup(func() { _ = runtimeSupport.Background.Close() })

	appCore, err := New(resolved.Config, authSupport, runtimeSupport)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = appCore.Close() })

	firstClient, err := appCore.RunPromptClientForProjectWorkspace(context.Background(), binding.ProjectID, workspace)
	if err != nil {
		t.Fatalf("RunPromptClientForProjectWorkspace first: %v", err)
	}
	secondClient, err := appCore.RunPromptClientForProjectWorkspace(context.Background(), binding.ProjectID, workspace)
	if err != nil {
		t.Fatalf("RunPromptClientForProjectWorkspace second: %v", err)
	}
	req := serverapi.RunPromptRequest{ClientRequestID: "req-1", Prompt: "hello"}
	firstRun, err := firstClient.RunPrompt(context.Background(), req, nil)
	if err != nil {
		t.Fatalf("RunPrompt first: %v", err)
	}
	secondRun, err := secondClient.RunPrompt(context.Background(), req, nil)
	if err != nil {
		t.Fatalf("RunPrompt second: %v", err)
	}
	if firstRun.SessionID != secondRun.SessionID {
		t.Fatalf("session ids = %q and %q, want stable replay", firstRun.SessionID, secondRun.SessionID)
	}
	if firstRun.Result != "ok" || secondRun.Result != "ok" {
		t.Fatalf("results = (%q, %q), want both ok", firstRun.Result, secondRun.Result)
	}
	overview, err := appCore.ProjectViewClient().GetProjectOverview(context.Background(), serverapi.ProjectGetOverviewRequest{ProjectID: binding.ProjectID})
	if err != nil {
		t.Fatalf("GetProjectOverview: %v", err)
	}
	if len(overview.Overview.Sessions) != 1 {
		t.Fatalf("session count = %d, want 1", len(overview.Overview.Sessions))
	}
	if overview.Overview.Sessions[0].SessionID != firstRun.SessionID {
		t.Fatalf("persisted session id = %q, want %q", overview.Overview.Sessions[0].SessionID, firstRun.SessionID)
	}
}

func TestSessionLaunchClientForProjectWorkspaceRejectsInaccessibleProjectRoot(t *testing.T) {
	home := t.TempDir()
	parent := filepath.Join(t.TempDir(), "blocked-parent")
	workspaceA := filepath.Join(parent, "workspace-a")
	workspaceB := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(workspaceA, 0o755); err != nil {
		t.Fatalf("create workspace A: %v", err)
	}

	resolvedA, err := serverbootstrap.ResolveConfig(serverbootstrap.Request{WorkspaceRoot: workspaceA})
	if err != nil {
		t.Fatalf("ResolveConfig A: %v", err)
	}
	binding, err := metadata.RegisterBinding(context.Background(), resolvedA.Config.PersistenceRoot, resolvedA.Config.WorkspaceRoot)
	if err != nil {
		t.Fatalf("RegisterBinding: %v", err)
	}
	if err := os.Chmod(parent, 0); err != nil {
		t.Fatalf("make workspace parent inaccessible: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(parent, 0o755); err != nil {
			t.Fatalf("restore workspace parent permissions: %v", err)
		}
	})
	if _, err := os.Stat(workspaceA); err == nil {
		t.Skip("filesystem permissions do not prevent stat for current user")
	}
	metadataStore, err := metadata.Open(resolvedA.Config.PersistenceRoot)
	if err != nil {
		t.Fatalf("metadata.Open: %v", err)
	}
	t.Cleanup(func() { _ = metadataStore.Close() })
	overview, err := metadataStore.GetProjectOverview(context.Background(), binding.ProjectID)
	if err != nil {
		t.Fatalf("GetProjectOverview: %v", err)
	}
	if overview.Project.RootPath != binding.CanonicalRoot || overview.Project.Availability != clientui.ProjectAvailabilityInaccessible {
		t.Fatalf("overview = %+v, want inaccessible root %q", overview.Project, binding.CanonicalRoot)
	}

	resolvedB, err := serverbootstrap.ResolveConfig(serverbootstrap.Request{WorkspaceRoot: workspaceB})
	if err != nil {
		t.Fatalf("ResolveConfig B: %v", err)
	}
	authSupport, err := serverbootstrap.BuildAuthSupport(auth.NewMemoryStore(auth.EmptyState()), nil, nil)
	if err != nil {
		t.Fatalf("BuildAuthSupport: %v", err)
	}
	runtimeSupport, err := serverbootstrap.BuildRuntimeSupport(resolvedB.Config)
	if err != nil {
		t.Fatalf("BuildRuntimeSupport: %v", err)
	}
	t.Cleanup(func() { _ = runtimeSupport.Background.Close() })

	appCore, err := New(resolvedB.Config, authSupport, runtimeSupport)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = appCore.Close() })

	_, err = appCore.SessionLaunchClientForProjectWorkspace(context.Background(), binding.ProjectID, workspaceB)
	if !errors.Is(err, serverapi.ErrProjectUnavailable) {
		t.Fatalf("SessionLaunchClientForProjectWorkspace error = %v, want ErrProjectUnavailable", err)
	}
	unavailable, ok := serverapi.AsProjectUnavailable(err)
	if !ok {
		t.Fatalf("expected ProjectUnavailableError, got %v", err)
	}
	if unavailable.ProjectID != binding.ProjectID || unavailable.Availability != clientui.ProjectAvailabilityInaccessible {
		t.Fatalf("unexpected unavailable project: %+v", unavailable)
	}
}
