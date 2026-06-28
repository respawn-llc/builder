package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"core/server/auth"
	"core/server/session"
	"core/shared/client"
	"core/shared/clientui"
	"core/shared/config"
	"core/shared/serverapi"
	"core/shared/toolspec"
)

type plannerOwnershipServer struct {
	*testEmbeddedServer
	owns bool
}

type plannerNoAuthStateServer struct {
	inner *testEmbeddedServer
}

func (s plannerNoAuthStateServer) OwnsServer() bool { return false }
func (s plannerNoAuthStateServer) Config() config.App {
	return s.inner.Config()
}
func (s plannerNoAuthStateServer) ProjectID() string {
	return s.inner.ProjectID()
}
func (s plannerNoAuthStateServer) AuthStatusClient() client.AuthStatusClient {
	return s.inner.AuthStatusClient()
}
func (s plannerNoAuthStateServer) ProjectViewClient() client.ProjectViewClient {
	return s.inner.ProjectViewClient()
}
func (s plannerNoAuthStateServer) SessionLaunchClient() client.SessionLaunchClient {
	return s.inner.SessionLaunchClient()
}
func (s plannerNoAuthStateServer) SessionViewClient() client.SessionViewClient {
	return s.inner.SessionViewClient()
}

type plannerAuthStatusClient struct {
	resp serverapi.AuthStatusResponse
}

func (c plannerAuthStatusClient) GetAuthStatus(context.Context, serverapi.AuthStatusRequest) (serverapi.AuthStatusResponse, error) {
	return c.resp, nil
}

type plannerAuthStatusServer struct {
	inner      *testEmbeddedServer
	authStatus client.AuthStatusClient
}

func (s plannerAuthStatusServer) OwnsServer() bool { return s.inner.OwnsServer() }
func (s plannerAuthStatusServer) Config() config.App {
	return s.inner.Config()
}
func (s plannerAuthStatusServer) ProjectID() string {
	return s.inner.ProjectID()
}
func (s plannerAuthStatusServer) AuthStatusClient() client.AuthStatusClient {
	return s.authStatus
}
func (s plannerAuthStatusServer) ProjectViewClient() client.ProjectViewClient {
	return s.inner.ProjectViewClient()
}
func (s plannerAuthStatusServer) SessionLaunchClient() client.SessionLaunchClient {
	return s.inner.SessionLaunchClient()
}
func (s plannerAuthStatusServer) SessionViewClient() client.SessionViewClient {
	return s.inner.SessionViewClient()
}

type stubSessionViewClient struct {
	getSessionMainView func(context.Context, serverapi.SessionMainViewRequest) (serverapi.SessionMainViewResponse, error)
}

func (s stubSessionViewClient) GetSessionMainView(ctx context.Context, req serverapi.SessionMainViewRequest) (serverapi.SessionMainViewResponse, error) {
	if s.getSessionMainView == nil {
		return serverapi.SessionMainViewResponse{}, errors.New("session view stub is required")
	}
	return s.getSessionMainView(ctx, req)
}

func (stubSessionViewClient) GetSessionTranscriptPage(context.Context, serverapi.SessionTranscriptPageRequest) (serverapi.SessionTranscriptPageResponse, error) {
	return serverapi.SessionTranscriptPageResponse{}, errors.New("unexpected GetSessionTranscriptPage call")
}

func (s *plannerOwnershipServer) OwnsServer() bool {
	return s != nil && s.owns
}

func TestSessionLaunchPlannerBuildsSessionPickerHeaderInfo(t *testing.T) {
	home := newAppTestHome(t)
	workspaceRoot := filepath.Join(home, "Developer", "kent-cli")
	if err := os.MkdirAll(workspaceRoot, 0o755); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	cfg := config.App{
		WorkspaceRoot: workspaceRoot,
		Settings: config.Settings{
			Model:         "gpt-5.1",
			ThinkingLevel: "high",
			ServerHost:    "127.0.0.1",
			ServerPort:    53082,
		},
	}
	planner := &launchPlanner{server: &testEmbeddedServer{cfg: cfg}}

	header := planner.sessionPickerHeaderInfo(cfg)
	if header.CWD != "" {
		t.Fatalf("header cwd = %q, want async lookup", header.CWD)
	}
	if header.Branch != "" {
		t.Fatalf("header branch = %q, want async lookup", header.Branch)
	}
	if header.StatusRequest.WorkspaceRoot != workspaceRoot {
		t.Fatalf("header status workspace root = %q, want %q", header.StatusRequest.WorkspaceRoot, workspaceRoot)
	}
	if header.StatusRequest.ModelName != "gpt-5.1" || header.StatusRequest.ThinkingLevel != "high" {
		t.Fatalf("header status model = %q thinking=%q", header.StatusRequest.ModelName, header.StatusRequest.ThinkingLevel)
	}
	if header.StatusRequest.AuthStatus != nil {
		t.Fatal("session picker header must not carry slow auth status client")
	}
	if !header.OwnsServer {
		t.Fatal("expected owned server header")
	}
	if header.ServerAddress != "127.0.0.1:53082" {
		t.Fatalf("header server address = %q", header.ServerAddress)
	}
}

func TestSessionLaunchPlannerPickerHeaderUsesRemoteAuthStatusWhenLocalAuthUnavailable(t *testing.T) {
	cfg := config.App{WorkspaceRoot: t.TempDir(), Settings: config.Settings{Model: "gpt-5"}}
	server := plannerAuthStatusServer{
		inner: &testEmbeddedServer{cfg: cfg},
		authStatus: plannerAuthStatusClient{resp: serverapi.AuthStatusResponse{
			Auth: serverapi.AuthStatusInfo{Summary: "user@example.com", Visible: true, Method: auth.MethodOAuth, Provider: "chatgpt-codex"},
		}},
	}
	planner := &launchPlanner{server: server}
	header := planner.sessionPickerHeaderInfo(cfg)
	if header.AuthManager != nil {
		t.Fatal("expected no local auth manager")
	}
	if header.StatusRequest.AuthStatus == nil {
		t.Fatal("expected remote auth status client in picker status request")
	}

	cmd := collectSessionPickerStatusCmd(header)
	if cmd == nil {
		t.Fatal("expected picker status command")
	}
	msg := cmd().(sessionPickerStatusMsg)
	if msg.auth != "OpenAI Subscription" {
		t.Fatalf("picker auth label = %q, want OpenAI Subscription", msg.auth)
	}
}

func TestSessionLaunchPlannerHeadlessCreatesNewSessionAndAppliesContinuationContext(t *testing.T) {
	root := t.TempDir()
	workspaceRoot := "/tmp/workspace-a"
	binding := mustRegisterAppBinding(t, root, workspaceRoot)
	containerDir := filepath.Join(filepath.Join(config.App{PersistenceRoot: root}.PersistenceRoot, "projects"), binding.ProjectID, "sessions")
	planner := newSessionLaunchPlanner(&testEmbeddedServer{
		cfg: config.App{
			WorkspaceRoot:   workspaceRoot,
			PersistenceRoot: root,
			Settings: config.Settings{
				OpenAIBaseURL: "http://headless.local/v1",
			},
		},
		containerDir: containerDir,
	})

	plan, err := planner.PlanSession(context.Background(), sessionLaunchRequest{Mode: launchModeHeadless})
	if err != nil {
		t.Fatalf("plan session: %v", err)
	}
	opened := openAuthoritativeAppSession(t, root, plan.SessionID)
	meta := opened.Meta()
	if meta.SessionID == "" {
		t.Fatal("expected session id")
	}
	if !strings.HasSuffix(meta.Name, " "+subagentSessionSuffix) {
		t.Fatalf("expected subagent session name, got %q", meta.Name)
	}
	if meta.Continuation == nil || meta.Continuation.OpenAIBaseURL != "http://headless.local/v1" {
		t.Fatalf("expected continuation base url applied, got %+v", meta.Continuation)
	}
	if plan.SessionName != meta.Name {
		t.Fatalf("expected plan session name %q, got %q", meta.Name, plan.SessionName)
	}
	if plan.WorkspaceRoot != "/tmp/workspace-a" {
		t.Fatalf("expected workspace root passthrough, got %q", plan.WorkspaceRoot)
	}
}

func TestSessionLaunchPlannerInteractiveUsesPickerSelection(t *testing.T) {
	_, workspace := newRegisteredAppWorkspace(t)

	cfg := loadAppTestConfig(t, workspace, config.LoadOptions{})
	binding := mustRegisterAppBinding(t, cfg.PersistenceRoot, cfg.WorkspaceRoot)
	containerDir := filepath.Join(filepath.Join(cfg.PersistenceRoot, "projects"), binding.ProjectID, "sessions")

	first := createAuthoritativeAppSession(t, cfg.PersistenceRoot, cfg.WorkspaceRoot)
	if err := first.SetName("first"); err != nil {
		t.Fatalf("persist first session meta: %v", err)
	}
	second := createAuthoritativeAppSession(t, cfg.PersistenceRoot, cfg.WorkspaceRoot)
	if err := second.SetName("second"); err != nil {
		t.Fatalf("persist second session meta: %v", err)
	}
	planner := &launchPlanner{
		server: &testEmbeddedServer{
			cfg: config.App{
				WorkspaceRoot:   cfg.WorkspaceRoot,
				PersistenceRoot: cfg.PersistenceRoot,
				Settings:        config.Settings{Theme: "dark"},
			},
			containerDir: containerDir,
			sessionViewClient: stubSessionViewClient{getSessionMainView: func(context.Context, serverapi.SessionMainViewRequest) (serverapi.SessionMainViewResponse, error) {
				return serverapi.SessionMainViewResponse{MainView: clientui.RuntimeMainView{Session: clientui.RuntimeSessionView{ExecutionTarget: clientui.SessionExecutionTarget{WorkspaceRoot: cfg.WorkspaceRoot}}}}, nil
			}},
		},
		pickSession: func(summaries []clientui.SessionSummary, theme string, header sessionPickerHeaderInfo) (sessionPickerResult, error) {
			if len(summaries) != 2 {
				t.Fatalf("expected two summaries, got %d", len(summaries))
			}
			for _, summary := range summaries {
				if summary.SessionID == second.Meta().SessionID {
					picked := summary
					return sessionPickerResult{Session: &picked}, nil
				}
			}
			t.Fatalf("expected picker summaries to include %q", second.Meta().SessionID)
			return sessionPickerResult{}, nil
		},
	}

	plan, err := planner.PlanSession(context.Background(), sessionLaunchRequest{Mode: launchModeInteractive})
	if err != nil {
		t.Fatalf("plan session: %v", err)
	}
	if plan.SessionID != second.Meta().SessionID {
		t.Fatalf("expected selected session %q, got %q", second.Meta().SessionID, plan.SessionID)
	}
	if plan.SessionID == first.Meta().SessionID {
		t.Fatalf("did not expect first session %q", first.Meta().SessionID)
	}
	if !plan.SelectedViaPicker {
		t.Fatal("expected picker-selected session to be marked as selected via picker")
	}
	if !plan.HasOtherSessionsKnown {
		t.Fatal("expected other-session availability to be known")
	}
	if !plan.HasOtherSessions {
		t.Fatal("expected selected session to report other sessions available")
	}
	if comparableWorkspaceChangeRoot(plan.SelectedSessionWorkspaceRoot) != comparableWorkspaceChangeRoot(cfg.WorkspaceRoot) {
		t.Fatalf("expected selected session workspace root %q, got %q", comparableWorkspaceChangeRoot(cfg.WorkspaceRoot), comparableWorkspaceChangeRoot(plan.SelectedSessionWorkspaceRoot))
	}
}

func TestSessionLaunchPlannerMarksNoOtherSessionsForDirectSingleSessionResume(t *testing.T) {
	_, workspace := newRegisteredAppWorkspace(t)

	cfg := loadAppTestConfig(t, workspace, config.LoadOptions{})
	binding := mustRegisterAppBinding(t, cfg.PersistenceRoot, cfg.WorkspaceRoot)
	containerDir := filepath.Join(filepath.Join(cfg.PersistenceRoot, "projects"), binding.ProjectID, "sessions")
	single := createAuthoritativeAppSession(t, cfg.PersistenceRoot, cfg.WorkspaceRoot)
	planner := newSessionLaunchPlanner(&testEmbeddedServer{
		cfg: config.App{
			WorkspaceRoot:   cfg.WorkspaceRoot,
			PersistenceRoot: cfg.PersistenceRoot,
			Settings:        config.Settings{Theme: "dark"},
		},
		containerDir: containerDir,
	})

	plan, err := planner.PlanSession(context.Background(), sessionLaunchRequest{Mode: launchModeInteractive, SelectedSessionID: single.Meta().SessionID})
	if err != nil {
		t.Fatalf("plan session: %v", err)
	}
	if !plan.HasOtherSessionsKnown {
		t.Fatal("expected other-session availability to be known")
	}
	if plan.HasOtherSessions {
		t.Fatal("did not expect other sessions for single-session project")
	}
}

func TestSessionLaunchPlannerPickerSelectionMissingMetadataMarksRecoveryInsteadOfFailing(t *testing.T) {
	root := t.TempDir()
	workspaceRoot := "/tmp/workspace-a"
	binding := mustRegisterAppBinding(t, root, workspaceRoot)
	planner := &launchPlanner{
		server: &testEmbeddedServer{
			cfg: config.App{
				WorkspaceRoot:   workspaceRoot,
				PersistenceRoot: root,
				Settings:        config.Settings{Theme: "dark"},
			},
			projectID: binding.ProjectID,
			projectViewClient: client.NewLoopbackProjectViewClient(projectBindingFlowStubProjectViewService{
				projectOverviewResp: serverapi.ProjectGetOverviewResponse{Overview: clientui.ProjectOverview{Sessions: []clientui.SessionSummary{{SessionID: "missing-session", UpdatedAt: time.Now().UTC()}}}},
			}),
			sessionViewClient: stubSessionViewClient{getSessionMainView: func(context.Context, serverapi.SessionMainViewRequest) (serverapi.SessionMainViewResponse, error) {
				return serverapi.SessionMainViewResponse{}, errors.New("missing selected session")
			}},
			sessionLaunch: stubSessionLaunchClient{planSession: func(context.Context, serverapi.SessionPlanRequest) (serverapi.SessionPlanResponse, error) {
				return serverapi.SessionPlanResponse{Plan: serverapi.SessionPlan{SessionID: "missing-session", WorkspaceRoot: workspaceRoot, ActiveSettings: config.Settings{Theme: "dark"}}}, nil
			}},
		},
		pickSession: func(summaries []clientui.SessionSummary, theme string, header sessionPickerHeaderInfo) (sessionPickerResult, error) {
			picked := summaries[0]
			return sessionPickerResult{Session: &picked}, nil
		},
	}

	plan, err := planner.PlanSession(context.Background(), sessionLaunchRequest{Mode: launchModeInteractive})
	if err != nil {
		t.Fatalf("plan session: %v", err)
	}
	if !plan.SelectedViaPicker {
		t.Fatal("expected picker-selected session to be marked as selected via picker")
	}
	if !plan.SelectedSessionWorkspaceLookupFailed {
		t.Fatal("expected missing selected-session metadata to mark picker recovery")
	}
	if plan.SelectedSessionWorkspaceRoot != "" {
		t.Fatalf("expected empty selected session workspace root after lookup failure, got %q", plan.SelectedSessionWorkspaceRoot)
	}
}

func TestSessionLaunchPlannerPropagatesServerOwnershipToStatusConfig(t *testing.T) {
	for _, tt := range []struct {
		name string
		owns bool
	}{
		{name: "owned", owns: true},
		{name: "attached", owns: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			workspaceRoot := "/tmp/workspace-a"
			binding := mustRegisterAppBinding(t, root, workspaceRoot)
			containerDir := filepath.Join(filepath.Join(config.App{PersistenceRoot: root}.PersistenceRoot, "projects"), binding.ProjectID, "sessions")
			planner := newSessionLaunchPlanner(&plannerOwnershipServer{
				testEmbeddedServer: &testEmbeddedServer{
					cfg: config.App{
						WorkspaceRoot:   workspaceRoot,
						PersistenceRoot: root,
						Settings:        config.Settings{Theme: "dark"},
					},
					containerDir: containerDir,
				},
				owns: tt.owns,
			})

			plan, err := planner.PlanSession(context.Background(), sessionLaunchRequest{Mode: launchModeHeadless})
			if err != nil {
				t.Fatalf("plan session: %v", err)
			}
			if plan.StatusConfig.OwnsServer != tt.owns {
				t.Fatalf("status config owns server = %t, want %t", plan.StatusConfig.OwnsServer, tt.owns)
			}
		})
	}
}

func TestSessionLaunchPlannerDefaultsMissingAuthStateProviderToEmptyStatusMetadata(t *testing.T) {
	root := t.TempDir()
	workspaceRoot := "/tmp/workspace-a"
	binding := mustRegisterAppBinding(t, root, workspaceRoot)
	planner := newSessionLaunchPlanner(plannerNoAuthStateServer{inner: &testEmbeddedServer{
		cfg: config.App{
			WorkspaceRoot:   workspaceRoot,
			PersistenceRoot: root,
			Settings:        config.Settings{Theme: "dark"},
		},
		containerDir: filepath.Join(filepath.Join(config.App{PersistenceRoot: root}.PersistenceRoot, "projects"), binding.ProjectID, "sessions"),
	}})

	plan, err := planner.PlanSession(context.Background(), sessionLaunchRequest{Mode: launchModeHeadless})
	if err != nil {
		t.Fatalf("plan session: %v", err)
	}
	if plan.StatusConfig.AuthStatePath != "" {
		t.Fatalf("auth state path = %q, want empty", plan.StatusConfig.AuthStatePath)
	}
	if plan.StatusConfig.AuthManager != nil {
		t.Fatalf("auth manager = %T, want nil", plan.StatusConfig.AuthManager)
	}
}

func TestSessionLaunchPlannerSelectedSessionIDBypassesPicker(t *testing.T) {
	root := t.TempDir()
	workspaceRoot := "/tmp/workspace-a"
	binding := mustRegisterAppBinding(t, root, workspaceRoot)
	containerDir := filepath.Join(filepath.Join(config.App{PersistenceRoot: root}.PersistenceRoot, "projects"), binding.ProjectID, "sessions")
	store := createAuthoritativeAppSession(t, root, workspaceRoot)
	if err := store.SetName("selected"); err != nil {
		t.Fatalf("persist selected session meta: %v", err)
	}
	if err := store.SetContinuationContext(session.ContinuationContext{OpenAIBaseURL: "http://session.local/v1"}); err != nil {
		t.Fatalf("persist continuation context: %v", err)
	}
	planner := &launchPlanner{
		server: &testEmbeddedServer{
			cfg: config.App{
				WorkspaceRoot:   "/tmp/workspace-a",
				PersistenceRoot: root,
				Settings:        config.Settings{Theme: "dark", OpenAIBaseURL: "http://config.local/v1"},
			},
			containerDir: containerDir,
			sessionViewClient: stubSessionViewClient{getSessionMainView: func(context.Context, serverapi.SessionMainViewRequest) (serverapi.SessionMainViewResponse, error) {
				t.Fatal("did not expect session view lookup for explicit session id")
				return serverapi.SessionMainViewResponse{}, nil
			}},
		},
		pickSession: func([]clientui.SessionSummary, string, sessionPickerHeaderInfo) (sessionPickerResult, error) {
			t.Fatal("did not expect picker for explicit session id")
			return sessionPickerResult{}, nil
		},
	}

	plan, err := planner.PlanSession(context.Background(), sessionLaunchRequest{Mode: launchModeInteractive, SelectedSessionID: store.Meta().SessionID})
	if err != nil {
		t.Fatalf("plan session: %v", err)
	}
	if plan.SessionID != store.Meta().SessionID {
		t.Fatalf("expected explicit session %q, got %q", store.Meta().SessionID, plan.SessionID)
	}
	if plan.SelectedViaPicker {
		t.Fatal("did not expect explicit session selection to be marked as picker-selected")
	}
	if plan.ActiveSettings.OpenAIBaseURL != "http://session.local/v1" {
		t.Fatalf("expected session continuation base url, got %q", plan.ActiveSettings.OpenAIBaseURL)
	}
	reopened := openAuthoritativeAppSession(t, root, plan.SessionID)
	if got := reopened.Meta().Continuation; got == nil || got.OpenAIBaseURL != "http://session.local/v1" {
		t.Fatalf("expected continuation base url preserved, got %+v", got)
	}
}

func TestSessionPlanOverridesFromConfigIncludesOnlyCLISources(t *testing.T) {
	cfg := config.App{
		Settings: config.Settings{
			Model:            "cli-model",
			ProviderOverride: "openai",
			ThinkingLevel:    "high",
			Theme:            "dark",
			OpenAIBaseURL:    "http://cli.local/v1",
			EnabledTools:     map[toolspec.ID]bool{toolspec.ToolExecCommand: true, toolspec.ToolPatch: true, toolspec.ToolEdit: false},
			Timeouts:         config.Timeouts{ModelRequestSeconds: 99},
		},
		Source: config.SourceReport{Sources: map[string]string{
			"model":                          "cli",
			"provider_override":              "env",
			"thinking_level":                 "cli",
			"theme":                          "file",
			"openai_base_url":                "cli",
			"timeouts.model_request_seconds": "cli",
			"tools.shell":                    "cli",
			"tools.patch":                    "cli",
			"tools.edit":                     "cli",
		}},
	}

	overrides := sessionPlanOverridesFromConfig(cfg)
	if overrides.Model != "cli-model" {
		t.Fatalf("model override = %q, want cli-model", overrides.Model)
	}
	if overrides.ProviderOverride != "" {
		t.Fatalf("provider override = %q, want empty", overrides.ProviderOverride)
	}
	if overrides.ThinkingLevel != "high" {
		t.Fatalf("thinking override = %q, want high", overrides.ThinkingLevel)
	}
	if overrides.Theme != "" {
		t.Fatalf("theme override = %q, want empty", overrides.Theme)
	}
	if overrides.OpenAIBaseURL != "http://cli.local/v1" {
		t.Fatalf("base url override = %q, want cli base url", overrides.OpenAIBaseURL)
	}
	if overrides.ModelTimeoutSeconds != 99 {
		t.Fatalf("timeout override = %d, want 99", overrides.ModelTimeoutSeconds)
	}
	if overrides.Tools != "shell,patch" {
		t.Fatalf("tools override = %q, want shell,patch", overrides.Tools)
	}
}

func TestSessionLaunchPlannerSendsCLIOverridesToServer(t *testing.T) {
	var got serverapi.SessionPlanRequest
	planner := &launchPlanner{
		server: &testEmbeddedServer{
			cfg: config.App{
				WorkspaceRoot:   "/tmp/workspace-a",
				PersistenceRoot: t.TempDir(),
				Settings: config.Settings{
					Model:        "cli-model",
					EnabledTools: map[toolspec.ID]bool{toolspec.ToolPatch: true},
				},
				Source: config.SourceReport{Sources: map[string]string{
					"model":       "cli",
					"tools.patch": "cli",
				}},
			},
			sessionLaunch: stubSessionLaunchClient{planSession: func(_ context.Context, req serverapi.SessionPlanRequest) (serverapi.SessionPlanResponse, error) {
				got = req
				return serverapi.SessionPlanResponse{Plan: serverapi.SessionPlan{
					SessionID:      "session-1",
					WorkspaceRoot:  "/tmp/workspace-a",
					ActiveSettings: config.Settings{Model: "server-model"},
				}}, nil
			}},
		},
	}

	plan, err := planner.PlanSession(context.Background(), sessionLaunchRequest{Mode: launchModeInteractive, ForceNewSession: true})
	if err != nil {
		t.Fatalf("PlanSession: %v", err)
	}
	if got.Overrides.Model != "cli-model" || got.Overrides.Tools != "patch" {
		t.Fatalf("server overrides = %+v, want cli model and patch tool", got.Overrides)
	}
	if plan.ActiveSettings.Model != "server-model" {
		t.Fatalf("plan model = %q, want server response without client mutation", plan.ActiveSettings.Model)
	}
}
