package app

import (
	"context"
	"core/cli/app/internal/status"
	"core/server/auth"
	"core/server/authservice"
	serverstartup "core/server/startup"
	askquestion "core/server/tools"
	"core/shared/clientui"
	"core/shared/config"
	"core/shared/protocol"
	"core/shared/serverapi"
	"core/shared/toolspec"
	"errors"
	"io"
	"path/filepath"
	"testing"
	"time"
)

func TestStartEmbeddedServerUnknownWorkspaceCreateProjectFlowCanPlanSession(t *testing.T) {
	home := newAppTestHome(t)
	workspace := t.TempDir()
	if _, _, err := config.WriteDefaultSettingsFileAt(filepath.Join(home, config.ConfigDirName, "config.toml")); err != nil {
		t.Fatalf("write test settings: %v", err)
	}
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

func TestRemoteNoAuthUnregisteredWorkspaceBindingCanPrepareRuntime(t *testing.T) {
	newAppTestHome(t)
	workspace := t.TempDir()
	configureAppTestServerPort(t)
	fakeResponses, hits := newNoAuthFakeResponsesServer(t, []string{"rebound no-auth reply"})
	defer fakeResponses.Close()

	srv, err := serverstartup.StartServeServer(context.Background(), serverstartup.Request{
		Model:                "gpt-5",
		AllowUnauthenticated: true,
	}, memoryAuthHandler{}, autoOnboarding)
	if err != nil {
		t.Fatalf("serve.Start: %v", err)
	}
	defer func() { _ = srv.Close() }()
	stopServing := serveAppServer(t, srv)
	defer stopServing()
	waitForConfiguredRunPromptDaemon(t, workspace)

	originalPicker := runProjectBindingPickerFlow
	originalPrompt := runProjectNamePromptFlow
	t.Cleanup(func() {
		runProjectBindingPickerFlow = originalPicker
		runProjectNamePromptFlow = originalPrompt
	})
	runProjectBindingPickerFlow = func(projects []clientui.ProjectSummary, theme string) (projectBindingPickerResult, error) {
		return projectBindingPickerResult{CreateNew: true}, nil
	}
	runProjectNamePromptFlow = func(defaultName string, theme string) (string, error) {
		return "Remote No Auth Project", nil
	}

	authPickerCalls := 0
	interactor := &interactiveAuthInteractor{
		pickMethod: func(authInteraction) (authMethodPickerResult, error) {
			authPickerCalls++
			if authPickerCalls > 1 {
				t.Fatal("remote no-auth binding flow must not re-enter auth picker")
			}
			return authMethodPickerResult{Choice: authMethodChoiceSkip}, nil
		},
	}
	server, err := startSessionServer(context.Background(), Options{WorkspaceRoot: workspace, WorkspaceRootExplicit: true, Model: "gpt-5"}, interactor, true)
	if err != nil {
		t.Fatalf("startSessionServer: %v", err)
	}
	defer func() { _ = server.Close() }()
	bound, err := ensureInteractiveProjectBinding(context.Background(), server)
	if err != nil {
		t.Fatalf("ensureInteractiveProjectBinding: %v", err)
	}
	_, runtimePlan := prepareAppRuntimePlanWithOpenAIBaseURL(t, bound, sessionLaunchRequest{Mode: launchModeInteractive, ForceNewSession: true}, fakeResponses.URL, io.Discard, "test remote no-auth rebound runtime")
	submission, err := submitRuntimeClientForTest(t, runtimePlan.Wiring.runtimeClient, "hello after rebound no auth")
	if err != nil {
		t.Fatalf("SubmitUserMessage: %v", err)
	}
	if submission.Message != "rebound no-auth reply" {
		t.Fatalf("assistant message = %q, want rebound no-auth reply", submission.Message)
	}
	runtimePlan.Close()
	if hits.Load() != 1 {
		t.Fatalf("expected fake LLM call once, got %d", hits.Load())
	}
}

func TestStartSessionServerRejectsMissingStartupControlSurfaceCapabilities(t *testing.T) {
	tests := []struct {
		name  string
		flags protocol.CapabilityFlags
		issue startupRemoteCompatibilityIssue
	}{
		{
			name: "auth bootstrap",
			flags: protocol.CapabilityFlags{
				JSONRPCWebSocket:   true,
				OnboardingFinalize: true,
			},
			issue: startupRemoteAuthBootstrapUnavailable,
		},
		{
			name: "onboarding finalization",
			flags: protocol.CapabilityFlags{
				JSONRPCWebSocket: true,
				AuthBootstrap:    true,
			},
			issue: startupRemoteOnboardingFinalizeUnavailable,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, workspace := newRegisteredAppWorkspace(t)
			cleanup := publishConfiguredRemoteForWorkspace(t, workspace, test.flags)
			defer cleanup()

			server, err := startSessionServer(context.Background(), Options{
				WorkspaceRoot:         workspace,
				WorkspaceRootExplicit: true,
			}, readyMemoryAuthHandler(), false)
			if server != nil {
				_ = server.Close()
				t.Fatal("incompatible configured server must not start an interactive session server")
			}
			var preflight *configuredServerPreflightError
			if !errors.As(err, &preflight) {
				t.Fatalf("error = %v, want configured-server preflight error", err)
			}
			if preflight.operation != "validate compatibility" {
				t.Fatalf("preflight operation = %q, want compatibility validation", preflight.operation)
			}
			var compatibility *startupRemoteCompatibilityError
			if !errors.As(err, &compatibility) {
				t.Fatalf("error = %v, want typed compatibility cause", err)
			}
			if compatibility.issue != test.issue {
				t.Fatalf("compatibility issue = %d, want %d", compatibility.issue, test.issue)
			}
		})
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

	submission, err := submitRuntimeClientForTest(t, runtimePlan.Wiring.runtimeClient, "hello through interactive override")
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
