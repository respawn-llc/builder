package app

import (
	"context"
	"core/cli/app/internal/status"
	"core/server/auth"
	"core/server/authservice"
	serverstartup "core/server/startup"
	"core/shared/clientui"
	"core/shared/config"
	"core/shared/serverapi"
	"core/shared/toolspec"
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
	plan, err := planner.PlanSession(context.Background(), sessionLaunchRequest{Mode: launchModeInteractive, Intent: serverapi.CreateNewSessionLaunchIntent(serverapi.IndependentSessionCreateOrigin())})
	if err != nil {
		t.Fatalf("PlanSession: %v", err)
	}
	canonicalWorkspace, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		t.Fatalf("EvalSymlinks workspace: %v", err)
	}
	if plan.ExecutionTarget.EffectiveWorkdir != canonicalWorkspace {
		t.Fatalf("plan execution workdir = %q, want %q", plan.ExecutionTarget.EffectiveWorkdir, canonicalWorkspace)
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
	server, err := startSessionServerForTest(t, context.Background(), Options{WorkspaceRoot: workspace, WorkspaceRootExplicit: true, Model: "gpt-5"}, interactor, true)
	if err != nil {
		t.Fatalf("startSessionServer: %v", err)
	}
	defer func() { _ = server.Close() }()
	bound, err := ensureInteractiveProjectBinding(context.Background(), server)
	if err != nil {
		t.Fatalf("ensureInteractiveProjectBinding: %v", err)
	}
	_, runtimePlan := prepareAppRuntimePlanWithOpenAIBaseURL(t, bound, sessionLaunchRequest{Mode: launchModeInteractive, Intent: serverapi.CreateNewSessionLaunchIntent(serverapi.IndependentSessionCreateOrigin())}, fakeResponses.URL, io.Discard, "test remote no-auth rebound runtime")
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

func TestRemoteSessionStatusDoesNotReuseLocalAuthState(t *testing.T) {
	_, workspace := newRegisteredAppWorkspace(t)

	originalFetcher := authservice.DefaultUsagePayloadFetcher
	defer func() { authservice.DefaultUsagePayloadFetcher = originalFetcher }()
	called := false
	authservice.DefaultUsagePayloadFetcher = func(_ context.Context, baseURL string, state auth.State) (authservice.UsagePayload, error) {
		called = true
		return authservice.UsagePayload{PlanType: "pro"}, nil
	}

	startConfiguredDaemonFixture(t, workspace, serverstartup.Request{
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
	}})

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

	interactor := &interactiveAuthInteractor{
		pickMethod: func(authInteraction) (authMethodPickerResult, error) {
			t.Fatal("remote startup validation must not open auth picker when server auth is ready")
			return authMethodPickerResult{}, nil
		},
	}
	server, err := startSessionServerForTest(t, context.Background(), Options{WorkspaceRoot: workspace, WorkspaceRootExplicit: true}, interactor, true)
	if err != nil {
		t.Fatalf("startSessionServer: %v", err)
	}
	t.Cleanup(func() { closeInteractiveSessionServer(t, server) })
	if _, ok := server.(*remoteAppServer); !ok {
		t.Fatalf("expected remote app server, got %T", server)
	}

	planner := newSessionLaunchPlanner(server)
	plan, err := planner.PlanSession(context.Background(), sessionLaunchRequest{Mode: launchModeInteractive, Intent: serverapi.CreateNewSessionLaunchIntent(serverapi.IndependentSessionCreateOrigin())})
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

func TestStartSessionServerUsesInvocationOverridesWhenAttachingToDiscoveredDaemon(t *testing.T) {
	_, workspace := newRegisteredAppWorkspace(t)

	defaultResponses, defaultHits := newFakeResponsesServer(t, []string{"interactive daemon default"})
	defer defaultResponses.Close()
	overrideResponses, overrideHits := newFakeResponsesServer(t, []string{"interactive daemon override"})
	defer overrideResponses.Close()

	fixture := startConfiguredDaemonFixture(t, workspace, serverstartup.Request{
		WorkspaceRoot:         workspace,
		WorkspaceRootExplicit: true,
		Model:                 "gpt-5.4",
		OpenAIBaseURL:         defaultResponses.URL,
		OpenAIBaseURLExplicit: true,
	}, apiKeyMemoryAuthHandler("test-key"))

	server := fixture.attachRemoteSessionServer(t, Options{
		WorkspaceRoot:         workspace,
		WorkspaceRootExplicit: true,
		Model:                 "gpt-5.3-codex",
		Tools:                 "shell",
		OpenAIBaseURL:         overrideResponses.URL,
		OpenAIBaseURLExplicit: true,
	}, newHeadlessAuthInteractor())

	plan, runtimePlan := prepareAppRuntimePlan(t, server, sessionLaunchRequest{Mode: launchModeInteractive, Intent: serverapi.CreateNewSessionLaunchIntent(serverapi.IndependentSessionCreateOrigin())}, io.Discard, "test remote interactive runtime override")
	defer closeRuntimeLaunchPlan(t, runtimePlan)
	if plan.ActiveSettings.Model != "gpt-5.3-codex" {
		t.Fatalf("model = %q, want gpt-5.3-codex", plan.ActiveSettings.Model)
	}
	if len(plan.EnabledTools) != 1 || plan.EnabledTools[0] != toolspec.ToolExecCommand {
		t.Fatalf("enabled tools = %+v, want only shell", plan.EnabledTools)
	}

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

func TestStartSessionServerUsesConfiguredDaemonForPromptRoundTrip(t *testing.T) {
	_, workspace := newRegisteredAppWorkspace(t)
	t.Setenv("KENT_REVIEWER_FREQUENCY", "off")
	model := newAppTestModelServer(t,
		appTestModelStep{Calls: []appTestModelToolCall{
			appTestAskCall("ask-1", "Pick one", []string{"one", "two"}, 2),
		}},
		appTestModelStep{Calls: []appTestModelToolCall{
			appTestOutsidePatchCall("patch-1", appTestOutsidePatchPath(t)),
		}},
		appTestModelStep{Final: "prompt round trip complete"},
	)
	defer model.Close()

	fixture := startConfiguredDaemonFixture(t, workspace, serverstartup.Request{
		WorkspaceRoot:         workspace,
		WorkspaceRootExplicit: true,
		Model:                 "gpt-5",
		OpenAIBaseURL:         model.URL,
		OpenAIBaseURLExplicit: true,
	}, apiKeyMemoryAuthHandler("test-key"))

	server := fixture.attachRemoteSessionServer(t, Options{WorkspaceRoot: workspace, WorkspaceRootExplicit: true}, readyMemoryAuthHandler())
	_, runtimePlan := prepareAppRuntimePlan(t, server, sessionLaunchRequest{Mode: launchModeInteractive, Intent: serverapi.CreateNewSessionLaunchIntent(serverapi.IndependentSessionCreateOrigin())}, io.Discard, "test remote prompt round trip")
	defer closeRuntimeLaunchPlan(t, runtimePlan)

	submissionDone, submissionFailed := startAppTestRuntimeSubmission(t, runtimePlan.Wiring.runtimeClient, "start prompt round trip")
	askPrompt := waitForRemoteTranscriptPrompt(t, runtimePlan.Wiring.eventDispatcher.transcriptEvents, "ask-1", submissionFailed)
	if askPrompt.Kind != clientui.TranscriptPromptKindQuestion || askPrompt.Question != "Pick one" {
		t.Fatalf("unexpected ask prompt: %+v", askPrompt)
	}
	answerRemoteTranscriptPrompt(t, runtimePlan.Wiring.promptAnswers, askPrompt, clientui.PromptAnswer{
		PromptID:             string(askPrompt.PromptID),
		SelectedOptionNumber: func() *int { selected := 2; return &selected }(),
	})
	approvalPrompt := waitForRemoteTranscriptPrompt(t, runtimePlan.Wiring.eventDispatcher.transcriptEvents, "", submissionFailed)
	if approvalPrompt.Kind != clientui.TranscriptPromptKindApproval {
		t.Fatalf("unexpected approval prompt: %+v", approvalPrompt)
	}
	answerRemoteTranscriptPrompt(t, runtimePlan.Wiring.promptAnswers, approvalPrompt, clientui.PromptAnswer{
		PromptID: string(approvalPrompt.PromptID),
		Approval: &clientui.ApprovalPromptAnswer{
			Decision:   clientui.ApprovalDecisionAllowOnce,
			Commentary: "trusted",
		},
	})
	select {
	case result := <-submissionDone:
		if result.err != nil {
			t.Fatalf("SubmitUserMessage: %v", result.err)
		}
		if result.submission.Message != "prompt round trip complete" {
			t.Fatalf("assistant message = %q", result.submission.Message)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for prompt round trip")
	}
}
