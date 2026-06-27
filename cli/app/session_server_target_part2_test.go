package app

import (
	"context"
	"core/cli/app/internal/status"
	"core/server/auth"
	"core/server/authservice"
	serverstartup "core/server/startup"
	askquestion "core/server/tools"
	"core/shared/client"
	"core/shared/clientui"
	"core/shared/config"
	"core/shared/protocol"
	"core/shared/serverapi"
	"core/shared/toolspec"
	"errors"
	"io"
	"os"
	"path/filepath"
	goruntime "runtime"
	"testing"
	"time"
)

func TestStartEmbeddedServerUnknownWorkspaceCreateProjectFlowCanPlanSession(t *testing.T) {
	newAppTestHome(t)
	workspace := t.TempDir()
	cfg := loadAppTestConfig(t, workspace, config.LoadOptions{})
	store := auth.NewFileStore(config.GlobalAuthConfigPath(cfg))
	if err := store.Save(context.Background(), auth.State{
		Scope: auth.ScopeGlobal,
		Method: auth.Method{
			Type:   auth.MethodAPIKey,
			APIKey: &auth.APIKeyMethod{Key: "test-key"},
		},
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("save auth state: %v", err)
	}

	originalPicker := runProjectBindingPickerFlow
	originalPrompt := runProjectNamePromptFlow
	t.Cleanup(func() {
		runProjectBindingPickerFlow = originalPicker
		runProjectNamePromptFlow = originalPrompt
	})
	runProjectBindingPickerFlow = func(projects []clientui.ProjectSummary, theme string) (projectBindingPickerResult, error) {
		if len(projects) != 0 {
			t.Fatalf("expected no existing projects, got %+v", projects)
		}
		return projectBindingPickerResult{CreateNew: true}, nil
	}
	runProjectNamePromptFlow = func(defaultName string, theme string) (string, error) {
		if want := filepath.Base(workspace); defaultName != want {
			t.Fatalf("default project name = %q, want %q", defaultName, want)
		}
		return "Created From Startup", nil
	}

	t.Log("starting embedded server")
	server, err := startEmbeddedServer(context.Background(), Options{WorkspaceRoot: workspace, WorkspaceRootExplicit: true}, newHeadlessAuthInteractor(), false)
	if err != nil {
		t.Fatalf("startEmbeddedServer: %v", err)
	}
	defer func() { _ = server.Close() }()

	t.Log("binding unknown workspace")
	bound, err := ensureInteractiveProjectBinding(context.Background(), server)
	if err != nil {
		t.Fatalf("ensureInteractiveProjectBinding: %v", err)
	}
	if got := bound.ProjectID(); got == "" {
		t.Fatal("expected bound project id after create-project flow")
	}

	t.Log("planning interactive session")
	planner := newSessionLaunchPlanner(bound)
	plan, err := planner.PlanSession(context.Background(), sessionLaunchRequest{Mode: launchModeInteractive})
	if err != nil {
		t.Fatalf("PlanSession: %v", err)
	}
	canonicalWorkspace, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		t.Fatalf("EvalSymlinks workspace: %v", err)
	}
	if plan.WorkspaceRoot != canonicalWorkspace {
		t.Fatalf("plan workspace root = %q, want %q", plan.WorkspaceRoot, canonicalWorkspace)
	}
	resolved, err := bound.ProjectViewClient().ResolveProjectPath(context.Background(), serverapi.ProjectResolvePathRequest{Path: workspace})
	if err != nil {
		t.Fatalf("ResolveProjectPath: %v", err)
	}
	t.Log("resolved created binding")
	if resolved.Binding == nil || resolved.Binding.ProjectName != "Created From Startup" {
		t.Fatalf("expected created binding metadata, got %+v", resolved.Binding)
	}
}

func TestStartSessionServerRejectsIncompatibleDiscoveredDaemonAndFallsBack(t *testing.T) {
	_, workspace := newRegisteredAppWorkspace(t)

	fakeResponses, hits := newFakeResponsesServer(t, []string{"embedded fallback reply"})
	defer fakeResponses.Close()

	cleanup := publishConfiguredRemoteForWorkspace(t, workspace, protocol.CapabilityFlags{
		JSONRPCWebSocket: true,
		ProjectAttach:    true,
		SessionAttach:    true,
		RunPrompt:        true,
		SessionActivity:  true,
		ProcessOutput:    true,
	})
	defer cleanup()

	server, err := startSessionServer(context.Background(), Options{
		WorkspaceRoot:         workspace,
		WorkspaceRootExplicit: true,
		Model:                 "gpt-5",
		OpenAIBaseURL:         fakeResponses.URL,
		OpenAIBaseURLExplicit: true,
	}, readyMemoryAuthHandler(), false)
	if err != nil {
		t.Fatalf("startSessionServer: %v", err)
	}
	defer func() { _ = server.Close() }()
	if _, ok := server.(*remoteAppServer); ok {
		t.Fatal("expected incompatible configured daemon to be rejected")
	}

	_, runtimePlan := prepareAppRuntimePlan(t, server, sessionLaunchRequest{Mode: launchModeInteractive, ForceNewSession: true}, io.Discard, "test embedded fallback runtime")
	defer runtimePlan.Close()

	submission, err := runtimePlan.Wiring.runtimeClient.SubmitUserMessage(context.Background(), "hello through embedded fallback")
	message := submission.Message
	if err != nil {
		t.Fatalf("SubmitUserMessage: %v", err)
	}
	if message != "embedded fallback reply" {
		t.Fatalf("assistant message = %q, want %q", message, "embedded fallback reply")
	}
	if hits.Load() != 1 {
		t.Fatalf("expected embedded fallback llm call once, got %d", hits.Load())
	}
}

func TestStartSessionServerRejectsDiscoveredDaemonWithoutProcessOutputCapability(t *testing.T) {
	_, workspace := newRegisteredAppWorkspace(t)

	fakeResponses, hits := newFakeResponsesServer(t, []string{"embedded fallback reply"})
	defer fakeResponses.Close()

	cleanup := publishConfiguredRemoteForWorkspace(t, workspace, protocol.CapabilityFlags{
		JSONRPCWebSocket: true,
		ProjectAttach:    true,
		SessionAttach:    true,
		SessionPlan:      true,
		SessionLifecycle: true,
		SessionRuntime:   true,
		RuntimeControl:   true,
		PromptControl:    true,
		PromptActivity:   true,
		SessionActivity:  true,
	})
	defer cleanup()

	server, err := startSessionServer(context.Background(), Options{
		WorkspaceRoot:         workspace,
		WorkspaceRootExplicit: true,
		Model:                 "gpt-5",
		OpenAIBaseURL:         fakeResponses.URL,
		OpenAIBaseURLExplicit: true,
	}, readyMemoryAuthHandler(), false)
	if err != nil {
		t.Fatalf("startSessionServer: %v", err)
	}
	defer func() { _ = server.Close() }()
	if _, ok := server.(*remoteAppServer); ok {
		t.Fatal("expected configured daemon without process capability to be rejected")
	}

	_, runtimePlan := prepareAppRuntimePlan(t, server, sessionLaunchRequest{Mode: launchModeInteractive, ForceNewSession: true}, io.Discard, "test embedded fallback runtime")
	defer runtimePlan.Close()

	submission, err := runtimePlan.Wiring.runtimeClient.SubmitUserMessage(context.Background(), "hello after capability fallback")
	message := submission.Message
	if err != nil {
		t.Fatalf("SubmitUserMessage: %v", err)
	}
	if message != "embedded fallback reply" {
		t.Fatalf("assistant message = %q, want %q", message, "embedded fallback reply")
	}
	if hits.Load() != 1 {
		t.Fatalf("expected embedded fallback llm call once, got %d", hits.Load())
	}
}

func TestStartSessionServerRejectsDiscoveredDaemonWithoutAuthBootstrapCapability(t *testing.T) {
	_, workspace := newRegisteredAppWorkspace(t)

	fakeResponses, hits := newFakeResponsesServer(t, []string{"embedded fallback reply"})
	defer fakeResponses.Close()

	cleanup := publishConfiguredRemoteForWorkspace(t, workspace, protocol.CapabilityFlags{
		JSONRPCWebSocket:        true,
		ProjectAttach:           true,
		SessionAttach:           true,
		SessionPlan:             true,
		SessionLifecycle:        true,
		SessionTranscriptPaging: true,
		SessionRuntime:          true,
		RuntimeControl:          true,
		PromptControl:           true,
		PromptActivity:          true,
		SessionActivity:         true,
		ProcessOutput:           true,
	})
	defer cleanup()

	server, err := startSessionServer(context.Background(), Options{
		WorkspaceRoot:         workspace,
		WorkspaceRootExplicit: true,
		Model:                 "gpt-5",
		OpenAIBaseURL:         fakeResponses.URL,
		OpenAIBaseURLExplicit: true,
	}, readyMemoryAuthHandler(), false)
	if err != nil {
		t.Fatalf("startSessionServer: %v", err)
	}
	defer func() { _ = server.Close() }()
	if _, ok := server.(*remoteAppServer); ok {
		t.Fatal("expected configured daemon without auth bootstrap capability to be rejected")
	}

	_, runtimePlan := prepareAppRuntimePlan(t, server, sessionLaunchRequest{Mode: launchModeInteractive, ForceNewSession: true}, io.Discard, "test embedded fallback runtime")
	defer runtimePlan.Close()

	submission, err := runtimePlan.Wiring.runtimeClient.SubmitUserMessage(context.Background(), "hello after auth bootstrap fallback")
	message := submission.Message
	if err != nil {
		t.Fatalf("SubmitUserMessage: %v", err)
	}
	if message != "embedded fallback reply" {
		t.Fatalf("assistant message = %q, want %q", message, "embedded fallback reply")
	}
	if hits.Load() != 1 {
		t.Fatalf("expected embedded fallback llm call once, got %d", hits.Load())
	}
}

func TestStartSessionServerRejectsDiscoveredDaemonWithoutProjectAttachCapability(t *testing.T) {
	_, workspace := newRegisteredAppWorkspace(t)

	fakeResponses, hits := newFakeResponsesServer(t, []string{"embedded fallback reply"})
	defer fakeResponses.Close()

	cleanup := publishConfiguredRemoteForWorkspace(t, workspace, protocol.CapabilityFlags{
		JSONRPCWebSocket:        true,
		AuthBootstrap:           true,
		SessionAttach:           true,
		SessionPlan:             true,
		SessionLifecycle:        true,
		SessionTranscriptPaging: true,
		SessionRuntime:          true,
		RuntimeControl:          true,
		PromptControl:           true,
		PromptActivity:          true,
		SessionActivity:         true,
		ProcessOutput:           true,
	})
	defer cleanup()

	server, err := startSessionServer(context.Background(), Options{
		WorkspaceRoot:         workspace,
		WorkspaceRootExplicit: true,
		Model:                 "gpt-5",
		OpenAIBaseURL:         fakeResponses.URL,
		OpenAIBaseURLExplicit: true,
	}, readyMemoryAuthHandler(), false)
	if err != nil {
		t.Fatalf("startSessionServer: %v", err)
	}
	defer func() { _ = server.Close() }()
	if _, ok := server.(*remoteAppServer); ok {
		t.Fatal("expected configured daemon without project attach capability to be rejected")
	}

	_, runtimePlan := prepareAppRuntimePlan(t, server, sessionLaunchRequest{Mode: launchModeInteractive, ForceNewSession: true}, io.Discard, "test project attach fallback runtime")
	defer runtimePlan.Close()

	submission, err := runtimePlan.Wiring.runtimeClient.SubmitUserMessage(context.Background(), "hello after project attach fallback")
	message := submission.Message
	if err != nil {
		t.Fatalf("SubmitUserMessage: %v", err)
	}
	if message != "embedded fallback reply" {
		t.Fatalf("assistant message = %q, want %q", message, "embedded fallback reply")
	}
	if hits.Load() != 1 {
		t.Fatalf("expected embedded fallback llm call once, got %d", hits.Load())
	}
}

func TestStartSessionServerRejectsDiscoveredDaemonWithoutTranscriptPagingCapability(t *testing.T) {
	_, workspace := newRegisteredAppWorkspace(t)

	fakeResponses, hits := newFakeResponsesServer(t, []string{"embedded fallback reply"})
	defer fakeResponses.Close()

	cleanup := publishConfiguredRemoteForWorkspace(t, workspace, protocol.CapabilityFlags{
		JSONRPCWebSocket: true,
		ProjectAttach:    true,
		SessionAttach:    true,
		SessionPlan:      true,
		SessionLifecycle: true,
		SessionRuntime:   true,
		RuntimeControl:   true,
		PromptControl:    true,
		PromptActivity:   true,
		SessionActivity:  true,
		ProcessOutput:    true,
	})
	defer cleanup()

	server, err := startSessionServer(context.Background(), Options{
		WorkspaceRoot:         workspace,
		WorkspaceRootExplicit: true,
		Model:                 "gpt-5",
		OpenAIBaseURL:         fakeResponses.URL,
		OpenAIBaseURLExplicit: true,
	}, readyMemoryAuthHandler(), false)
	if err != nil {
		t.Fatalf("startSessionServer: %v", err)
	}
	defer func() { _ = server.Close() }()
	if _, ok := server.(*remoteAppServer); ok {
		t.Fatal("expected configured daemon without transcript paging capability to be rejected")
	}

	_, runtimePlan := prepareAppRuntimePlan(t, server, sessionLaunchRequest{Mode: launchModeInteractive, ForceNewSession: true}, io.Discard, "test embedded fallback runtime")
	defer runtimePlan.Close()

	submission, err := runtimePlan.Wiring.runtimeClient.SubmitUserMessage(context.Background(), "hello after transcript paging fallback")
	message := submission.Message
	if err != nil {
		t.Fatalf("SubmitUserMessage: %v", err)
	}
	if message != "embedded fallback reply" {
		t.Fatalf("assistant message = %q, want %q", message, "embedded fallback reply")
	}
	if hits.Load() != 1 {
		t.Fatalf("expected embedded fallback llm call once, got %d", hits.Load())
	}
}

func TestRemoteSessionStatusDoesNotReuseLocalAuthState(t *testing.T) {
	_, workspace := newRegisteredAppWorkspace(t)

	originalFetcher := authservice.DefaultUsagePayloadFetcher
	defer func() { authservice.DefaultUsagePayloadFetcher = originalFetcher }()
	called := false
	authservice.DefaultUsagePayloadFetcher = func(_ context.Context, baseURL string, state auth.State) (authservice.UsagePayload, error) {
		called = true
		return authservice.UsagePayload{PlanType: "pro"}, nil
	}

	srv, err := serverstartup.StartServeServer(context.Background(), serverstartup.Request{
		WorkspaceRoot:         workspace,
		WorkspaceRootExplicit: true,
		Model:                 "gpt-5",
	}, memoryAuthHandler{state: auth.State{
		Scope: auth.ScopeGlobal,
		Method: auth.Method{
			Type: auth.MethodOAuth,
			OAuth: &auth.OAuthMethod{
				AccessToken: "server-access-token",
				AccountID:   "server-acct",
				Email:       "user@example.com",
			},
		},
		UpdatedAt: time.Now().UTC(),
	}}, autoOnboarding)
	if err != nil {
		t.Fatalf("serve.Start: %v", err)
	}
	defer func() { _ = srv.Close() }()

	stopServing := serveAppServer(t, srv)
	defer stopServing()
	waitForConfiguredRemoteIdentity(t, workspace)

	loadCfg := loadAppTestConfig(t, workspace, config.LoadOptions{})
	store := auth.NewFileStore(config.GlobalAuthConfigPath(loadCfg))
	if err := store.Save(context.Background(), auth.State{
		Scope: auth.ScopeGlobal,
		Method: auth.Method{
			Type:   auth.MethodAPIKey,
			APIKey: &auth.APIKeyMethod{Key: "local-key"},
		},
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("save auth state: %v", err)
	}

	server, err := startSessionServer(context.Background(), Options{WorkspaceRoot: workspace, WorkspaceRootExplicit: true}, readyMemoryAuthHandler(), false)
	if err != nil {
		t.Fatalf("startSessionServer: %v", err)
	}
	defer func() { _ = server.Close() }()
	if _, ok := server.(*remoteAppServer); !ok {
		t.Fatalf("expected remote app server, got %T", server)
	}

	planner := newSessionLaunchPlanner(server)
	plan, err := planner.PlanSession(context.Background(), sessionLaunchRequest{Mode: launchModeInteractive, ForceNewSession: true})
	if err != nil {
		t.Fatalf("PlanSession: %v", err)
	}
	if plan.StatusConfig.OwnsServer {
		t.Fatal("expected attached configured service to be reported as not owned")
	}
	if plan.StatusConfig.AuthManager != nil {
		t.Fatal("expected remote session status to avoid local auth manager")
	}
	if plan.StatusConfig.AuthStatePath != "" {
		t.Fatalf("expected empty remote auth state path, got %q", plan.StatusConfig.AuthStatePath)
	}

	collector := defaultUIStatusCollector{authManager: plan.StatusConfig.AuthManager}
	snapshot, err := collector.Collect(context.Background(), populateStatusRequestCacheKeys(uiStatusRequest{
		WorkspaceRoot:     plan.StatusConfig.WorkspaceRoot,
		PersistenceRoot:   plan.StatusConfig.PersistenceRoot,
		Settings:          plan.StatusConfig.Settings,
		Source:            plan.StatusConfig.Source,
		AuthCacheIdentity: status.AuthCacheIdentity(plan.StatusConfig.AuthManager),
		AuthStatus:        plan.StatusConfig.AuthStatus,
		AuthStatePath:     plan.StatusConfig.AuthStatePath,
		OwnsServer:        plan.StatusConfig.OwnsServer,
	}))
	if err != nil {
		t.Fatalf("collect status: %v", err)
	}
	if got := snapshot.Auth.Summary; got != "user@example.com" {
		t.Fatalf("auth summary = %q", got)
	}
	if !snapshot.Subscription.Applicable || snapshot.Subscription.Summary != "Pro subscription" {
		t.Fatalf("expected remote status subscription to come from server auth, got %+v", snapshot.Subscription)
	}
	if !called {
		t.Fatal("expected remote session status to fetch subscription through server auth")
	}
}

func TestStartSessionServerRemoteReadyAuthDoesNotOpenStartupPicker(t *testing.T) {
	_, workspace := newRegisteredAppWorkspace(t)

	srv, err := serverstartup.StartServeServer(context.Background(), serverstartup.Request{
		WorkspaceRoot:         workspace,
		WorkspaceRootExplicit: true,
		Model:                 "gpt-5",
	}, memoryAuthHandler{state: auth.State{
		Scope: auth.ScopeGlobal,
		Method: auth.Method{
			Type: auth.MethodOAuth,
			OAuth: &auth.OAuthMethod{
				AccessToken: "server-access-token",
				AccountID:   "server-acct",
				Email:       "user@example.com",
			},
		},
		UpdatedAt: time.Now().UTC(),
	}}, autoOnboarding)
	if err != nil {
		t.Fatalf("serve.Start: %v", err)
	}
	defer func() { _ = srv.Close() }()

	stopServing := serveAppServer(t, srv)
	defer stopServing()
	waitForConfiguredRemoteIdentity(t, workspace)

	interactor := &interactiveAuthInteractor{
		pickMethod: func(authInteraction) (authMethodPickerResult, error) {
			t.Fatal("remote startup validation must not open auth picker when server auth is ready")
			return authMethodPickerResult{}, nil
		},
	}
	server, err := startSessionServer(context.Background(), Options{WorkspaceRoot: workspace, WorkspaceRootExplicit: true}, interactor, true)
	if err != nil {
		t.Fatalf("startSessionServer: %v", err)
	}
	defer func() { _ = server.Close() }()
	if _, ok := server.(*remoteAppServer); !ok {
		t.Fatalf("expected remote app server, got %T", server)
	}
}

func TestStartSessionServerOwnsLaunchedDaemonCloser(t *testing.T) {
	_, workspace := newRegisteredAppWorkspace(t)

	srv, err := serverstartup.StartServeServer(context.Background(), serverstartup.Request{
		WorkspaceRoot:         workspace,
		WorkspaceRootExplicit: true,
		Model:                 "gpt-5",
	}, apiKeyMemoryAuthHandler("test-key"), autoOnboarding)
	if err != nil {
		t.Fatalf("serve.Start: %v", err)
	}
	defer func() { _ = srv.Close() }()

	stopServing := serveAppServer(t, srv)
	defer stopServing()
	waitForConfiguredRemoteIdentity(t, workspace)

	loadCfg := loadAppTestConfig(t, workspace, config.LoadOptions{})

	called := false
	originalLaunch := launchSessionServerDaemon
	originalDial := dialConfiguredProjectViewRemote
	t.Cleanup(func() { launchSessionServerDaemon = originalLaunch })
	t.Cleanup(func() { dialConfiguredProjectViewRemote = originalDial })
	dialConfiguredProjectViewRemote = func(context.Context, config.App) (configuredProjectViewRemote, error) {
		return nil, errors.New("configured remote unavailable")
	}
	launchSessionServerDaemon = func(context.Context, Options) (*client.Remote, func() error, bool, error) {
		remote, err := client.DialRemoteURL(context.Background(), config.ServerRPCURL(loadCfg))
		if err != nil {
			return nil, nil, false, err
		}
		return remote, func() error {
			called = true
			return remote.Close()
		}, true, nil
	}

	server, err := startSessionServer(context.Background(), Options{WorkspaceRoot: workspace, WorkspaceRootExplicit: true}, readyMemoryAuthHandler(), false)
	if err != nil {
		t.Fatalf("startSessionServer: %v", err)
	}
	if _, ok := server.(*remoteAppServer); !ok {
		t.Fatalf("expected remote app server, got %T", server)
	}
	if err := server.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !called {
		t.Fatal("expected launched daemon closer to be invoked")
	}
}

func TestStartSessionServerLaunchedDaemonCloseStopsProcess(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("helper daemon process signal probe is unix-only")
	}
	_, workspace := newRegisteredAppWorkspace(t)
	t.Setenv("GO_WANT_HELPER_DAEMON", "1")
	t.Setenv("GO_HELPER_WORKSPACE_ROOT", workspace)

	originalExecPath := resolveDaemonExecutablePath
	originalServeArgs := buildServeArgsFunc
	t.Cleanup(func() {
		resolveDaemonExecutablePath = originalExecPath
		buildServeArgsFunc = originalServeArgs
	})
	resolveDaemonExecutablePath = func() (string, bool) {
		path, err := os.Executable()
		if err != nil {
			t.Fatalf("os.Executable: %v", err)
		}
		return path, true
	}
	buildServeArgsFunc = func(string, Options) []string {
		return []string{"-test.run=^TestStartSessionServerHelperDaemonProcess$"}
	}

	server, err := startSessionServer(context.Background(), Options{WorkspaceRoot: workspace, WorkspaceRootExplicit: true}, readyMemoryAuthHandler(), false)
	if err != nil {
		t.Fatalf("startSessionServer: %v", err)
	}
	remote, ok := server.(*remoteAppServer)
	if !ok {
		t.Fatalf("expected remote app server, got %T", server)
	}
	if remote.identity.PID == 0 {
		t.Fatal("expected launched daemon pid")
	}
	identity := waitForConfiguredRemoteIdentity(t, workspace)
	if identity.PID != remote.identity.PID {
		t.Fatalf("connected pid = %d, remote pid = %d", identity.PID, remote.identity.PID)
	}

	if err := server.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	waitForPIDExit(t, remote.identity.PID)
}

func TestStartSessionServerUsesInvocationOverridesWhenAttachingToDiscoveredDaemon(t *testing.T) {
	_, workspace := newRegisteredAppWorkspace(t)

	defaultResponses, defaultHits := newFakeResponsesServer(t, []string{"interactive daemon default"})
	defer defaultResponses.Close()
	overrideResponses, overrideHits := newFakeResponsesServer(t, []string{"interactive daemon override"})
	defer overrideResponses.Close()

	srv, err := serverstartup.StartServeServer(context.Background(), serverstartup.Request{
		WorkspaceRoot:         workspace,
		WorkspaceRootExplicit: true,
		Model:                 "gpt-5",
		OpenAIBaseURL:         defaultResponses.URL,
		OpenAIBaseURLExplicit: true,
	}, apiKeyMemoryAuthHandler("test-key"), autoOnboarding)
	if err != nil {
		t.Fatalf("serve.Start: %v", err)
	}
	defer func() { _ = srv.Close() }()

	stopServing := serveAppServer(t, srv)
	defer stopServing()
	waitForConfiguredRemoteIdentity(t, workspace)

	server, err := startSessionServer(context.Background(), Options{
		WorkspaceRoot:         workspace,
		WorkspaceRootExplicit: true,
		Model:                 "gpt-5",
		OpenAIBaseURL:         overrideResponses.URL,
		OpenAIBaseURLExplicit: true,
	}, newHeadlessAuthInteractor(), false)
	if err != nil {
		t.Fatalf("startSessionServer: %v", err)
	}
	defer func() { _ = server.Close() }()

	_, runtimePlan := prepareAppRuntimePlan(t, server, sessionLaunchRequest{Mode: launchModeInteractive, ForceNewSession: true}, io.Discard, "test remote interactive runtime override")
	defer runtimePlan.Close()

	submission, err := runtimePlan.Wiring.runtimeClient.SubmitUserMessage(context.Background(), "hello through interactive override")
	message := submission.Message
	if err != nil {
		t.Fatalf("SubmitUserMessage: %v", err)
	}
	if message != "interactive daemon override" {
		t.Fatalf("assistant message = %q, want %q", message, "interactive daemon override")
	}
	if overrideHits.Load() != 1 {
		t.Fatalf("expected override llm call once, got %d", overrideHits.Load())
	}
	if defaultHits.Load() != 0 {
		t.Fatalf("expected daemon default llm endpoint unused, got %d", defaultHits.Load())
	}

}

func TestStartSessionServerPreservesExplicitCLIToolsWithCLIModelOverride(t *testing.T) {
	_, workspace := newRegisteredAppWorkspace(t)

	srv, err := serverstartup.StartServeServer(context.Background(), serverstartup.Request{
		WorkspaceRoot:         workspace,
		WorkspaceRootExplicit: true,
		Model:                 "gpt-5.4",
	}, apiKeyMemoryAuthHandler("test-key"), autoOnboarding)
	if err != nil {
		t.Fatalf("serve.Start: %v", err)
	}
	defer func() { _ = srv.Close() }()

	stopServing := serveAppServer(t, srv)
	defer stopServing()
	waitForConfiguredRemoteIdentity(t, workspace)

	server, err := startSessionServer(context.Background(), Options{
		WorkspaceRoot:         workspace,
		WorkspaceRootExplicit: true,
		Model:                 "gpt-5.3-codex",
		Tools:                 "shell",
	}, newHeadlessAuthInteractor(), false)
	if err != nil {
		t.Fatalf("startSessionServer: %v", err)
	}
	defer func() { _ = server.Close() }()

	planner := newSessionLaunchPlanner(server)
	plan, err := planner.PlanSession(context.Background(), sessionLaunchRequest{Mode: launchModeInteractive, ForceNewSession: true})
	if err != nil {
		t.Fatalf("PlanSession: %v", err)
	}
	if plan.ActiveSettings.Model != "gpt-5.3-codex" {
		t.Fatalf("model = %q, want gpt-5.3-codex", plan.ActiveSettings.Model)
	}
	if len(plan.EnabledTools) != 1 || plan.EnabledTools[0] != toolspec.ToolExecCommand {
		t.Fatalf("enabled tools = %+v, want only shell", plan.EnabledTools)
	}

}

func TestStartSessionServerUsesConfiguredDaemonForPromptRoundTrip(t *testing.T) {
	_, workspace := newRegisteredAppWorkspace(t)

	srv, err := serverstartup.StartServeServer(context.Background(), serverstartup.Request{
		WorkspaceRoot:         workspace,
		WorkspaceRootExplicit: true,
		Model:                 "gpt-5",
	}, apiKeyMemoryAuthHandler("test-key"), autoOnboarding)
	if err != nil {
		t.Fatalf("serve.Start: %v", err)
	}
	defer func() { _ = srv.Close() }()

	stopServing := serveAppServer(t, srv)
	defer stopServing()
	waitForConfiguredRemoteIdentity(t, workspace)

	server, err := startSessionServer(context.Background(), Options{WorkspaceRoot: workspace, WorkspaceRootExplicit: true}, readyMemoryAuthHandler(), false)
	if err != nil {
		t.Fatalf("startSessionServer: %v", err)
	}
	defer func() { _ = server.Close() }()
	promptViews := requirePromptViewServer(t, server)

	plan, runtimePlan := prepareAppRuntimePlan(t, server, sessionLaunchRequest{Mode: launchModeInteractive, ForceNewSession: true}, io.Discard, "test remote prompt round trip")
	defer runtimePlan.Close()

	askDone := make(chan struct {
		resp askquestion.AskQuestionResponse
		err  error
	}, 1)
	go func() {
		resp, err := srv.AwaitPromptResponse(context.Background(), plan.SessionID, askquestion.AskQuestionRequest{
			ID:                     "ask-1",
			Question:               "Pick one",
			Suggestions:            []string{"one", "two"},
			RecommendedOptionIndex: 2,
		})
		askDone <- struct {
			resp askquestion.AskQuestionResponse
			err  error
		}{resp: resp, err: err}
	}()
	waitForPendingAskResources(t, promptViews.AskViewClient(), plan.SessionID, 1)
	askEvt := waitForRemoteAskEvent(t, runtimePlan.Wiring.askEvents)
	if askEvt.req.PromptID != "ask-1" || askEvt.req.Question != "Pick one" {
		t.Fatalf("unexpected ask event: %+v", askEvt.req)
	}
	askEvt.reply <- askReply{response: clientui.PromptAnswer{PromptID: askEvt.req.PromptID, SelectedOptionNumber: 2}}
	select {
	case result := <-askDone:
		if result.err != nil {
			t.Fatalf("AwaitPromptResponse ask: %v", result.err)
		}
		if result.resp.RequestID != "ask-1" || result.resp.SelectedOptionNumber != 2 {
			t.Fatalf("unexpected ask response: %+v", result.resp)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for ask response")
	}
	waitForPendingAskResources(t, promptViews.AskViewClient(), plan.SessionID, 0)

	approvalDone := make(chan struct {
		resp askquestion.AskQuestionResponse
		err  error
	}, 1)
	go func() {
		resp, err := srv.AwaitPromptResponse(context.Background(), plan.SessionID, askquestion.AskQuestionRequest{
			ID:              "approval-1",
			Question:        "Approve it?",
			Approval:        true,
			ApprovalOptions: []askquestion.AskQuestionApprovalOption{{Decision: askquestion.AskQuestionApprovalDecisionAllowOnce, Label: "Allow once"}, {Decision: askquestion.AskQuestionApprovalDecisionDeny, Label: "Deny"}},
		})
		approvalDone <- struct {
			resp askquestion.AskQuestionResponse
			err  error
		}{resp: resp, err: err}
	}()
	waitForPendingApprovalResources(t, promptViews.ApprovalViewClient(), plan.SessionID, 1)
	approvalEvt := waitForRemoteAskEvent(t, runtimePlan.Wiring.askEvents)
	if !approvalEvt.req.Approval || approvalEvt.req.PromptID != "approval-1" {
		t.Fatalf("unexpected approval event: %+v", approvalEvt.req)
	}
	approvalEvt.reply <- askReply{response: clientui.PromptAnswer{PromptID: approvalEvt.req.PromptID, Approval: &clientui.ApprovalPromptAnswer{Decision: clientui.ApprovalDecisionAllowOnce, Commentary: "trusted"}}}
	select {
	case result := <-approvalDone:
		if result.err != nil {
			t.Fatalf("AwaitPromptResponse approval: %v", result.err)
		}
		if result.resp.RequestID != "approval-1" || result.resp.Approval == nil || result.resp.Approval.Decision != askquestion.AskQuestionApprovalDecisionAllowOnce || result.resp.Approval.Commentary != "trusted" {
			t.Fatalf("unexpected approval response: %+v", result.resp)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for approval response")
	}
	waitForPendingApprovalResources(t, promptViews.ApprovalViewClient(), plan.SessionID, 0)

}
