package app

import (
	"context"
	"core/cli/app/internal/startupconfig"
	"core/server/llm"
	"core/server/metadata"
	"core/server/runtime"
	serverstartup "core/server/startup"
	askquestion "core/server/tools"
	"core/shared/client"
	"core/shared/clientui"
	"core/shared/config"
	"core/shared/serverapi"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestStartSessionServerConfiguredDaemonNoAuthSkipsLaterPrompt(t *testing.T) {
	_, workspace := newRegisteredAppWorkspace(t)
	fakeResponses, hits := newNoAuthFakeResponsesServer(t, []string{"first no-auth reply", "second no-auth reply"})
	defer fakeResponses.Close()

	srv, err := serverstartup.StartServeServer(context.Background(), serverstartup.Request{
		WorkspaceRoot:         workspace,
		WorkspaceRootExplicit: true,
		Model:                 "gpt-5",
		AllowUnauthenticated:  true,
	}, memoryAuthHandler{}, autoOnboarding)
	if err != nil {
		t.Fatalf("serve.Start: %v", err)
	}
	defer func() { _ = srv.Close() }()

	stopServing := serveAppServer(t, srv)
	defer stopServing()
	waitForConfiguredRemoteIdentity(t, workspace)

	pickerCalls := 0
	firstInteractor := &interactiveAuthInteractor{
		pickMethod: func(authInteraction) (authMethodPickerResult, error) {
			pickerCalls++
			if pickerCalls > 1 {
				t.Fatal("no-auth selection must not re-enter the auth picker")
			}
			return authMethodPickerResult{Choice: authMethodChoiceSkip}, nil
		},
	}
	firstServer, err := startSessionServer(context.Background(), Options{
		WorkspaceRoot:         workspace,
		WorkspaceRootExplicit: true,
		Model:                 "gpt-5",
	}, firstInteractor, true)
	if err != nil {
		t.Fatalf("first startSessionServer: %v", err)
	}
	_, firstRuntimePlan := prepareAppRuntimePlanWithOpenAIBaseURL(t, firstServer, sessionLaunchRequest{Mode: launchModeInteractive, Intent: serverapi.CreateNewSessionLaunchIntent(serverapi.IndependentSessionCreateOrigin())}, fakeResponses.URL, io.Discard, "test remote no-auth runtime")
	firstSubmission, err := submitRuntimeClientForTest(t, firstRuntimePlan.Wiring.runtimeClient, "hello after no auth")
	if err != nil {
		t.Fatalf("first SubmitUserMessage: %v", err)
	}
	if firstSubmission.Message != "first no-auth reply" {
		t.Fatalf("first assistant message = %q, want first no-auth reply", firstSubmission.Message)
	}
	firstRuntimePlan.Close()
	if err := firstServer.Close(); err != nil {
		t.Fatalf("first server close: %v", err)
	}

	secondInteractor := &interactiveAuthInteractor{
		pickMethod: func(authInteraction) (authMethodPickerResult, error) {
			t.Fatal("persisted no-auth must not open the auth picker on a later launch")
			return authMethodPickerResult{}, nil
		},
	}
	secondServer, err := startSessionServer(context.Background(), Options{
		WorkspaceRoot:         workspace,
		WorkspaceRootExplicit: true,
		Model:                 "gpt-5",
	}, secondInteractor, true)
	if err != nil {
		t.Fatalf("second startSessionServer: %v", err)
	}
	defer func() { _ = secondServer.Close() }()
	_, secondRuntimePlan := prepareAppRuntimePlanWithOpenAIBaseURL(t, secondServer, sessionLaunchRequest{Mode: launchModeInteractive, Intent: serverapi.CreateNewSessionLaunchIntent(serverapi.IndependentSessionCreateOrigin())}, fakeResponses.URL, io.Discard, "test remote persisted no-auth runtime")
	secondSubmission, err := submitRuntimeClientForTest(t, secondRuntimePlan.Wiring.runtimeClient, "hello after persisted no auth")
	if err != nil {
		t.Fatalf("second SubmitUserMessage: %v", err)
	}
	if secondSubmission.Message != "second no-auth reply" {
		t.Fatalf("second assistant message = %q, want second no-auth reply", secondSubmission.Message)
	}
	secondRuntimePlan.Close()
	if hits.Load() != 2 {
		t.Fatalf("expected fake LLM calls twice, got %d", hits.Load())
	}
}

func TestStartupReadinessAllowsActivatedNoAuthOnboarding(t *testing.T) {
	_, workspace := newRegisteredAppWorkspaceWithoutSettings(t)
	cfg := loadAppTestConfig(t, workspace, config.LoadOptions{})

	srv, err := serverstartup.StartServeServer(context.Background(), serverstartup.Request{
		WorkspaceRoot:         workspace,
		WorkspaceRootExplicit: true,
		AllowUnauthenticated:  true,
	}, memoryAuthHandler{}, nil)
	if err != nil {
		t.Fatalf("serve.Start: %v", err)
	}
	defer func() { _ = srv.Close() }()
	stopServing := serveAppServer(t, srv)
	defer stopServing()

	var remote *client.Remote
	deadline := time.Now().Add(5 * time.Second)
	var attachErr error
	for remote == nil && time.Now().Before(deadline) {
		remote, attachErr = attachConfiguredStartupRemote(context.Background(), cfg)
		if remote == nil {
			time.Sleep(10 * time.Millisecond)
		}
	}
	if remote == nil {
		t.Fatalf("attach configured startup remote: %v", attachErr)
	}
	defer func() { _ = remote.Close() }()
	bootstrap, err := remote.CompleteAuthBootstrap(context.Background(), serverapi.AuthCompleteBootstrapRequest{
		Mode: serverapi.AuthBootstrapModeNone,
	})
	if err != nil {
		t.Fatalf("CompleteAuthBootstrap: %v", err)
	}
	if !bootstrap.NoAuthSelected {
		t.Fatalf("bootstrap response = %+v, want no-auth selection", bootstrap)
	}
	if err := remote.EnableNoAuthBootstrapAcknowledgement(context.Background()); err != nil {
		t.Fatalf("EnableNoAuthBootstrapAcknowledgement: %v", err)
	}

	selectedTheme := serverapi.OnboardingThemeDark
	_, err = remote.FinalizeOnboarding(context.Background(), serverapi.OnboardingFinalizeRequest{
		Theme: &selectedTheme,
		CommandsImport: &serverapi.OnboardingImportSelection{
			Mode: serverapi.OnboardingImportModeNone,
		},
	})
	if err != nil {
		t.Fatalf("FinalizeOnboarding: %v", err)
	}
	readiness, err := remote.GetServerReadiness(context.Background(), serverapi.ServerReadinessRequest{})
	if err != nil {
		t.Fatalf("GetServerReadiness: %v", err)
	}
	if readiness.Ready {
		t.Fatal("no-auth startup readiness must remain false while server-managed auth is absent")
	}
	if !startupReadinessAllowsSession(remote, readiness) {
		t.Fatalf("activated no-auth readiness must allow startup: %+v", readiness)
	}
}

func TestConfiguredDaemonPlanSessionUsesSessionWorkspaceLocalConfig(t *testing.T) {
	home := newAppTestHome(t)
	workspace := t.TempDir()
	configureAppTestServerPort(t)
	if err := os.MkdirAll(filepath.Join(home, config.ConfigDirName), 0o755); err != nil {
		t.Fatalf("create home config dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, config.ConfigDirName, "config.toml"), []byte("model = \"home-model\"\nthinking_level = \"low\"\n"), 0o644); err != nil {
		t.Fatalf("write home config: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(workspace, config.ConfigDirName), 0o755); err != nil {
		t.Fatalf("create workspace config dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, config.ConfigDirName, "config.toml"), []byte("model = \"workspace-model\"\nthinking_level = \"high\"\n"), 0o644); err != nil {
		t.Fatalf("write workspace config: %v", err)
	}
	glob, err := config.LoadGlobal(config.LoadOptions{})
	if err != nil {
		t.Fatalf("LoadGlobal: %v", err)
	}
	if _, err := metadata.RegisterBinding(context.Background(), glob.PersistenceRoot, workspace); err != nil {
		t.Fatalf("RegisterBinding: %v", err)
	}

	srv, err := serverstartup.StartServeServer(context.Background(), serverstartup.Request{AllowUnauthenticated: true}, readyMemoryAuthHandler(), autoOnboarding)
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
	if _, ok := server.(*remoteAppServer); !ok {
		t.Fatalf("expected remote app server, got %T", server)
	}
	bound, err := ensureInteractiveProjectBinding(context.Background(), server)
	if err != nil {
		t.Fatalf("ensureInteractiveProjectBinding: %v", err)
	}
	defer func() { _ = bound.Close() }()
	planner := newSessionLaunchPlanner(bound)
	plan, err := planner.PlanSession(context.Background(), sessionLaunchRequest{Mode: launchModeInteractive, Intent: serverapi.CreateNewSessionLaunchIntent(serverapi.IndependentSessionCreateOrigin())})
	if err != nil {
		t.Fatalf("PlanSession: %v", err)
	}
	if plan.ActiveSettings.Model != "workspace-model" || plan.ActiveSettings.ThinkingLevel != "high" {
		t.Fatalf("active settings = %+v, want workspace-local model/thinking", plan.ActiveSettings)
	}
	if !plan.Source.WorkspaceSettingsFileExists {
		t.Fatalf("expected workspace settings source, got %+v", plan.Source)
	}
}

func TestConfiguredDaemonEnvironmentContextUsesSessionWorkspaceRootForCWD(t *testing.T) {
	_, workspace := newRegisteredAppWorkspace(t)

	fakeResponses, hits := newFakeResponsesServer(t, []string{"interactive daemon reply"})
	defer fakeResponses.Close()

	srv, err := serverstartup.StartServeServer(context.Background(), serverstartup.Request{
		WorkspaceRoot:         workspace,
		WorkspaceRootExplicit: true,
		Model:                 "gpt-5",
		OpenAIBaseURL:         fakeResponses.URL,
		OpenAIBaseURLExplicit: true,
	}, apiKeyMemoryAuthHandler("test-key"), autoOnboarding)
	if err != nil {
		t.Fatalf("serve.Start: %v", err)
	}
	defer func() { _ = srv.Close() }()

	stopServing := serveAppServer(t, srv)
	defer stopServing()
	waitForConfiguredRemoteIdentity(t, workspace)

	server, err := startSessionServer(context.Background(), Options{WorkspaceRoot: workspace, WorkspaceRootExplicit: true}, newHeadlessAuthInteractor(), false)
	if err != nil {
		t.Fatalf("startSessionServer: %v", err)
	}
	defer func() { _ = server.Close() }()

	plan, runtimePlan := prepareAppRuntimePlan(t, server, sessionLaunchRequest{Mode: launchModeInteractive, Intent: serverapi.CreateNewSessionLaunchIntent(serverapi.IndependentSessionCreateOrigin())}, io.Discard, "test daemon environment cwd")
	defer runtimePlan.Close()

	submission, err := submitRuntimeClientForTest(t, runtimePlan.Wiring.runtimeClient, "hello through interactive daemon")
	message := submission.Message
	if err != nil {
		t.Fatalf("SubmitUserMessage: %v", err)
	}
	if message != "interactive daemon reply" {
		t.Fatalf("assistant message = %q, want %q", message, "interactive daemon reply")
	}
	if hits.Load() != 1 {
		t.Fatalf("expected daemon-backed llm call once, got %d", hits.Load())
	}
	store := openAuthoritativeWorkspaceSessionStore(t, workspace, fakeResponses.URL, plan.SessionID)
	messages, err := readStoredMessages(store)
	if err != nil {
		t.Fatalf("readStoredMessages: %v", err)
	}
	authoritativeWorkspace := store.Meta().WorkspaceRoot
	if authoritativeWorkspace == "" {
		t.Fatal("expected authoritative workspace root in session metadata")
	}
	var envContent string
	processCWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd: %v", err)
	}
	for _, msg := range messages {
		if msg.Role == llm.RoleDeveloper && msg.MessageType == llm.MessageTypeEnvironment {
			envContent = msg.Content
			break
		}
	}
	if envContent == "" {
		t.Fatalf("expected persisted environment context message in %+v", messages)
	}
	if !strings.Contains(envContent, "\nCWD: "+authoritativeWorkspace+"\n") {
		t.Fatalf("expected environment context to use session workspace root %q, got %q", authoritativeWorkspace, envContent)
	}
	if processCWD != authoritativeWorkspace && strings.Contains(envContent, "\nCWD: "+processCWD+"\n") {
		t.Fatalf("expected environment context to avoid process cwd %q leak, got %q", processCWD, envContent)
	}

}

func TestRemoteInteractiveRuntimeAnswersPromptsFromAnyAttachedClientAcrossWorkspaces(t *testing.T) {
	fixture := startRemoteMultiClientRuntimeFixture(t, "")
	finishStep := beginAppTestModelPromptStep(t, fixture.daemon, fixture.planA.SessionID)

	askDone := make(chan struct {
		resp askquestion.AskQuestionResponse
		err  error
	}, 1)
	go func() {
		resp, err := fixture.daemon.AwaitPromptResponse(context.Background(), fixture.planA.SessionID, appTestModelPromptRequest("ask-race-1", "Who answers first?"))
		askDone <- struct {
			resp askquestion.AskQuestionResponse
			err  error
		}{resp: resp, err: err}
	}()

	askPrompt := waitForRemoteTranscriptPrompt(t, fixture.runtimePlanA.Wiring.transcriptEvents, "ask-race-1")
	if askPrompt.Kind != clientui.TranscriptPromptKindQuestion || askPrompt.Question != "Who answers first?" {
		t.Fatalf("unexpected ask prompt: %+v", askPrompt)
	}
	runtimeClientsB := fixture.serverB.RuntimeAttachmentClients()

	if err := runtimeClientsB.PromptControl.AnswerAsk(context.Background(), serverapi.AskAnswerRequest{
		ClientRequestID: uuid.NewString(),
		SessionID:       fixture.planA.SessionID,
		AskID:           "ask-race-1",
		Answer:          "answer from client B",
	}); err != nil {
		t.Fatalf("AnswerAsk from attached client B: %v", err)
	}

	select {
	case result := <-askDone:
		if result.err != nil {
			t.Fatalf("AwaitPromptResponse ask: %v", result.err)
		}
		if result.resp.RequestID != "ask-race-1" || result.resp.Answer != "answer from client B" {
			t.Fatalf("unexpected ask response: %+v", result.resp)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for ask response")
	}

	approvalDone := make(chan struct {
		resp askquestion.AskQuestionResponse
		err  error
	}, 1)
	go func() {
		request := appTestModelPromptRequest("approval-race-1", "Allow the command?")
		request.Approval = true
		request.ApprovalOptions = []askquestion.AskQuestionApprovalOption{{Decision: askquestion.AskQuestionApprovalDecisionAllowOnce, Label: "Allow once"}, {Decision: askquestion.AskQuestionApprovalDecisionDeny, Label: "Deny"}}
		resp, err := fixture.daemon.AwaitPromptResponse(context.Background(), fixture.planA.SessionID, request)
		approvalDone <- struct {
			resp askquestion.AskQuestionResponse
			err  error
		}{resp: resp, err: err}
	}()

	approvalPrompt := waitForRemoteTranscriptPrompt(t, fixture.runtimePlanA.Wiring.transcriptEvents, "approval-race-1")
	if approvalPrompt.Kind != clientui.TranscriptPromptKindApproval || approvalPrompt.Question != "Allow the command?" {
		t.Fatalf("unexpected approval prompt: %+v", approvalPrompt)
	}

	if err := runtimeClientsB.PromptControl.AnswerApproval(context.Background(), serverapi.ApprovalAnswerRequest{
		ClientRequestID: uuid.NewString(),
		SessionID:       fixture.planA.SessionID,
		ApprovalID:      "approval-race-1",
		Decision:        clientui.ApprovalDecisionAllowOnce,
		Commentary:      "approved by client B",
	}); err != nil {
		t.Fatalf("AnswerApproval from attached client B: %v", err)
	}

	select {
	case result := <-approvalDone:
		if result.err != nil {
			t.Fatalf("AwaitPromptResponse approval: %v", result.err)
		}
		if result.resp.RequestID != "approval-race-1" || result.resp.Approval == nil {
			t.Fatalf("unexpected approval response: %+v", result.resp)
		}
		if result.resp.Approval.Decision != askquestion.AskQuestionApprovalDecisionAllowOnce || result.resp.Approval.Commentary != "approved by client B" {
			t.Fatalf("unexpected approval response: %+v", result.resp)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for approval response")
	}
	finishStep()
}

func appTestModelPromptRequest(id, question string) askquestion.AskQuestionRequest {
	return askquestion.AskQuestionRequest{
		ID:         id,
		Question:   question,
		Origin:     askquestion.AskQuestionOriginModelTool,
		RunID:      ongoingTestRunID().String(),
		StepID:     ongoingTestStepID().String(),
		ToolCallID: id,
	}
}

type appTestRuntimeReadModelPublisher interface {
	RuntimeReadModelFeedSnapshot(context.Context, string, []clientui.RuntimeOperationRef) (clientui.RuntimeReadModelUpdate, error)
	PublishRuntimeReadModelUpdate(string, clientui.RuntimeReadModelUpdate)
}

func beginAppTestModelPromptStep(t *testing.T, server *serverstartup.ServeServer, sessionID string) func() {
	t.Helper()
	if server == nil {
		t.Fatal("model prompt test server is required")
	}
	publisher, ok := server.SessionTranscriptClient().(appTestRuntimeReadModelPublisher)
	if !ok {
		t.Fatalf("session transcript client %T cannot publish canonical runtime state", server.SessionTranscriptClient())
	}
	runID := ongoingTestRunID()
	stepID := ongoingTestStepID()
	startedAt := time.Now().UTC()
	update, err := publisher.RuntimeReadModelFeedSnapshot(context.Background(), sessionID, nil)
	if err != nil {
		t.Fatalf("build model prompt runtime read model: %v", err)
	}
	update.Activity = clientui.RuntimeActivity{
		State:          clientui.RuntimeActivityRunning,
		QueueAccepting: update.Activity.QueueAccepting,
		ActiveStep: &clientui.RuntimeActiveStep{
			RunID:      runID,
			StepID:     stepID,
			ActiveKind: clientui.RuntimeActivityActiveKindUserTurn,
		},
	}
	publisher.PublishRuntimeReadModelUpdate(sessionID, update)
	server.PublishRuntimeEvent(sessionID, runtime.Event{
		Kind:   runtime.EventRunStateChanged,
		StepID: stepID.String(),
		RunState: &runtime.RunState{
			Lifecycle:  runtime.RunningRunLifecycle(runtime.RunModeTurn),
			RunID:      runID.String(),
			ActiveKind: runtime.ActiveKindUserTurn,
			Status:     runtime.RunStatusRunning,
			StartedAt:  startedAt,
		},
	})
	return func() {
		finishedAt := time.Now().UTC()
		server.PublishRuntimeEvent(sessionID, runtime.Event{
			Kind:   runtime.EventRunStateChanged,
			StepID: stepID.String(),
			RunState: &runtime.RunState{
				Lifecycle:  runtime.FinishedRunLifecycle(runtime.RunModeTurn),
				RunID:      runID.String(),
				ActiveKind: runtime.ActiveKindUserTurn,
				Status:     runtime.RunStatusCompleted,
				StartedAt:  startedAt,
				FinishedAt: finishedAt,
			},
		})
		update, err := publisher.RuntimeReadModelFeedSnapshot(context.Background(), sessionID, nil)
		if err != nil {
			t.Errorf("build completed model prompt runtime read model: %v", err)
			return
		}
		publisher.PublishRuntimeReadModelUpdate(sessionID, update)
	}
}

type remoteMultiClientRuntimeFixture struct {
	daemon       *serverstartup.ServeServer
	workspaceA   string
	workspaceB   string
	serverA      remoteMultiClientServer
	serverB      remoteMultiClientServer
	planA        sessionLaunchPlan
	planB        sessionLaunchPlan
	runtimePlanA *runtimeLaunchPlan
	runtimePlanB *runtimeLaunchPlan
}

type remoteMultiClientServer interface {
	interactiveSessionServer
	RuntimeAttachmentClients() runtimeAttachmentClients
}

func startRemoteMultiClientRuntimeFixture(t *testing.T, openAIBaseURL string) *remoteMultiClientRuntimeFixture {
	t.Helper()

	fixture := &remoteMultiClientRuntimeFixture{
		workspaceA: t.TempDir(),
		workspaceB: t.TempDir(),
	}
	t.Setenv("HOME", t.TempDir())
	registerAppWorkspace(t, fixture.workspaceA)
	registerAppWorkspace(t, fixture.workspaceB)

	req := serverstartup.Request{
		WorkspaceRoot:         fixture.workspaceA,
		WorkspaceRootExplicit: true,
		Model:                 "gpt-5",
	}
	if strings.TrimSpace(openAIBaseURL) != "" {
		req.OpenAIBaseURL = openAIBaseURL
		req.OpenAIBaseURLExplicit = true
	}

	srv, err := serverstartup.StartServeServer(context.Background(), req, apiKeyMemoryAuthHandler("test-key"), autoOnboarding)
	if err != nil {
		t.Fatalf("serve.Start: %v", err)
	}
	fixture.daemon = srv

	serveCtx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.Serve(serveCtx)
	}()
	waitForConfiguredRemoteIdentity(t, fixture.workspaceA)

	t.Cleanup(func() {
		if fixture.runtimePlanB != nil {
			fixture.runtimePlanB.Close()
		}
		if fixture.runtimePlanA != nil {
			fixture.runtimePlanA.Close()
		}
		if fixture.serverB != nil {
			_ = fixture.serverB.Close()
		}
		if fixture.serverA != nil {
			_ = fixture.serverA.Close()
		}
		cancel()
		if serveErr := <-errCh; !errors.Is(serveErr, context.Canceled) && serveErr != nil {
			t.Errorf("Serve error = %v, want context canceled", serveErr)
		}
		_ = srv.Close()
	})

	serverA, err := startSessionServer(context.Background(), Options{WorkspaceRoot: fixture.workspaceA, WorkspaceRootExplicit: true}, newHeadlessAuthInteractor(), false)
	if err != nil {
		t.Fatalf("startSessionServer workspace A: %v", err)
	}
	serverAFull, ok := serverA.(remoteMultiClientServer)
	if !ok {
		t.Fatalf("expected remote multi-client server for workspace A, got %T", serverA)
	}
	fixture.serverA = serverAFull
	if _, ok := fixture.serverA.(*remoteAppServer); !ok {
		t.Fatalf("expected remote app server for workspace A, got %T", fixture.serverA)
	}

	cfgB, err := startupconfig.ResolveSessionConfig(startupConfigRequest(Options{WorkspaceRoot: fixture.workspaceB, WorkspaceRootExplicit: true}))
	if err != nil {
		t.Fatalf("loadSessionServerConfig workspace B: %v", err)
	}
	remoteB, err := client.DialRemoteURL(context.Background(), config.ServerRPCURL(cfgB))
	if err != nil {
		t.Fatalf("DialRemote workspace B: %v", err)
	}
	fixture.serverB = newRemoteAppServerWithAuth(remoteB, cfgB, nil, false)

	if got, want := fixture.serverA.ProjectID(), fixture.serverB.ProjectID(); got != want {
		t.Fatalf("project id mismatch across clients: a=%q b=%q", got, want)
	}
	if fixture.serverA.Config().WorkspaceRoot == fixture.serverB.Config().WorkspaceRoot {
		t.Fatalf("expected distinct workspace roots across clients, both=%q", fixture.serverA.Config().WorkspaceRoot)
	}

	fixture.planA, fixture.runtimePlanA = prepareAppRuntimePlan(t, fixture.serverA, sessionLaunchRequest{Mode: launchModeInteractive, Intent: serverapi.CreateNewSessionLaunchIntent(serverapi.IndependentSessionCreateOrigin())}, io.Discard, "test remote multi-client runtime A")

	plannerB := newSessionLaunchPlanner(fixture.serverB)
	fixture.planB, err = plannerB.PlanSession(context.Background(), sessionLaunchRequest{Mode: launchModeInteractive, Intent: serverapi.OpenExistingSessionLaunchIntent(sessionLifecycleSessionID(t, fixture.planA.SessionID))})
	if err != nil {
		t.Fatalf("PlanSession B: %v", err)
	}
	if fixture.planB.SessionID != fixture.planA.SessionID {
		t.Fatalf("expected second client to attach same session, a=%q b=%q", fixture.planA.SessionID, fixture.planB.SessionID)
	}

	return fixture
}
