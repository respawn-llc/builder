package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"core/cli/app/internal/startupconfig"
	modelstub "core/internal/testharness/pty/blackbox"
	"core/internal/testharness/testsetup"
	"core/server/llm"
	"core/server/metadata"
	serverstartup "core/server/startup"
	"core/server/tools"
	"core/shared/client"
	"core/shared/clientui"
	"core/shared/config"
	authpb "core/shared/protoapi/gen/kent/api/auth"
	onboardingpb "core/shared/protoapi/gen/kent/api/onboarding"
	"core/shared/protocol"
	"core/shared/serverapi"
	"core/shared/toolspec"

	"google.golang.org/protobuf/types/known/emptypb"
)

type configuredDaemonFixture struct {
	daemon *serverstartup.ServeServer
}

type appTestModelToolCall struct {
	ID        string
	Name      toolspec.ID
	Arguments map[string]any
}

type appTestModelStep struct {
	Calls []appTestModelToolCall
	Final string
}

type appTestRuntimeSubmissionResult struct {
	submission clientui.UserTurnSubmission
	err        error
}

func newAppTestModelServer(t *testing.T, steps ...appTestModelStep) *modelstub.ResponsesStub {
	t.Helper()
	script := make([]modelstub.ScriptStep, len(steps))
	for index, step := range steps {
		script[index] = modelstub.FinalAnswer(step.Final)
		if len(step.Calls) > 0 {
			calls := make([]llm.ToolCall, len(step.Calls))
			for callIndex, call := range step.Calls {
				input, err := json.Marshal(call.Arguments)
				if err != nil {
					t.Fatalf("marshal %s tool arguments: %v", call.Name, err)
				}
				calls[callIndex] = llm.ToolCall{ID: call.ID, Name: string(call.Name), Input: input}
			}
			script[index] = modelstub.ToolBatch("", calls...)
		}
	}
	stub, err := modelstub.StartScriptedResponsesStub(modelstub.Script{Steps: script})
	if err != nil {
		t.Fatalf("StartScriptedResponsesStub: %v", err)
	}
	t.Cleanup(func() {
		if err := stub.Verify(); err != nil {
			t.Errorf("verify scripted Responses stub: %v", err)
		}
		if err := stub.Stop(); err != nil {
			t.Errorf("stop scripted Responses stub: %v", err)
		}
	})
	return stub
}

func appTestAskCall(id, question string, suggestions []string, recommended int) appTestModelToolCall {
	arguments := map[string]any{"question": question}
	if len(suggestions) > 0 {
		arguments["suggestions"] = suggestions
		arguments["recommended_option_index"] = recommended
	}
	return appTestModelToolCall{ID: id, Name: toolspec.ToolAskQuestion, Arguments: arguments}
}

func appTestOutsidePatchCall(id, path string) appTestModelToolCall {
	return appTestModelToolCall{ID: id, Name: toolspec.ToolPatch, Arguments: map[string]any{"patch": fmt.Sprintf(
		"*** Begin Patch\n*** Add File: %s\n+approved\n*** End Patch\n",
		path,
	)}}
}

func appTestOutsidePatchPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(
		testsetup.NonTemporaryDirectory(t, "kent-app-outside-", tools.IsPathInTemporaryDir),
		"approved.txt",
	)
}

func startAppTestRuntimeSubmission(t *testing.T, client clientui.RuntimeClient, text string) (<-chan appTestRuntimeSubmissionResult, <-chan error) {
	t.Helper()
	done := make(chan appTestRuntimeSubmissionResult, 1)
	failed := make(chan error, 1)
	go func() {
		submission, err := submitRuntimeClientForTest(t, client, text)
		if err != nil {
			failed <- err
		}
		done <- appTestRuntimeSubmissionResult{submission: submission, err: err}
	}()
	return done, failed
}

func startConfiguredDaemonFixture(
	t *testing.T,
	workspace string,
	request serverstartup.Request,
	authHandler serverstartup.AuthHandler,
) *configuredDaemonFixture {
	t.Helper()
	daemon, err := serverstartup.StartServeServer(context.Background(), request, authHandler, autoOnboarding)
	if err != nil {
		t.Fatalf("StartServeServer: %v", err)
	}
	t.Cleanup(func() {
		if err := daemon.Close(); err != nil {
			t.Errorf("ServeServer.Close: %v", err)
		}
	})
	stopServing := serveAppServer(t, daemon)
	t.Cleanup(stopServing)
	waitForConfiguredRemoteIdentity(t, workspace)
	return &configuredDaemonFixture{daemon: daemon}
}

func (f *configuredDaemonFixture) attachRemoteSessionServer(
	t *testing.T,
	options Options,
	interactor authInteractor,
) *remoteAppServer {
	t.Helper()
	server, err := startSessionServer(context.Background(), options, interactor, false)
	if err != nil {
		t.Fatalf("startSessionServer: %v", err)
	}
	remote, ok := server.(*remoteAppServer)
	if !ok {
		closeInteractiveSessionServer(t, server)
		t.Fatalf("expected remote app server, got %T", server)
	}
	t.Cleanup(func() { closeInteractiveSessionServer(t, remote) })
	return remote
}

func closeInteractiveSessionServer(t *testing.T, server interactiveSessionServer) {
	t.Helper()
	if server == nil {
		return
	}
	if err := server.Close(); err != nil {
		t.Errorf("interactive session server close: %v", err)
	}
}

func closeRuntimeLaunchPlan(t *testing.T, plan *runtimeLaunchPlan) {
	t.Helper()
	if plan == nil {
		return
	}
	if err := plan.Close(); err != nil {
		t.Errorf("runtime launch plan close: %v", err)
	}
}

func waitForConfiguredRemoteIdentity(t *testing.T, workspace string) protocol.ServerIdentity {
	t.Helper()
	cfg, err := loadRemoteAttachConfig(Options{WorkspaceRoot: workspace, WorkspaceRootExplicit: true})
	if err != nil {
		t.Fatalf("resolve configured remote: %v", err)
	}
	var identity protocol.ServerIdentity
	testsetup.RequireUntil(t, time.Now().Add(5*time.Second), 10*time.Millisecond, func() bool {
		remote, err := attachConfiguredStartupRemote(context.Background(), cfg)
		if err == nil {
			identity = remote.Identity()
			_ = remote.Close()
			return true
		}
		return false
	}, "configured daemon did not become reachable for workspace %s", workspace)
	return identity
}

func TestStartSessionServerConfiguredDaemonNoAuthSkipsLaterPrompt(t *testing.T) {
	_, workspace := newRegisteredAppWorkspace(t)
	fakeResponses, hits := newNoAuthFakeResponsesServer(t, []string{"first no-auth reply", "second no-auth reply"})
	defer fakeResponses.Close()

	startConfiguredDaemonFixture(t, workspace, serverstartup.Request{
		WorkspaceRoot:         workspace,
		WorkspaceRootExplicit: true,
		Model:                 "gpt-5",
		AllowUnauthenticated:  true,
	}, memoryAuthHandler{})

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
	requireQueuedAppTestUserTurn(t, firstSubmission, err)
	waitForRemoteTranscriptAssistantFinal(
		t,
		firstRuntimePlan.Wiring.eventDispatcher.transcriptEvents,
		"first no-auth reply",
	)
	closeRuntimeLaunchPlan(t, firstRuntimePlan)
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
	t.Cleanup(func() { closeInteractiveSessionServer(t, secondServer) })
	_, secondRuntimePlan := prepareAppRuntimePlanWithOpenAIBaseURL(t, secondServer, sessionLaunchRequest{Mode: launchModeInteractive, Intent: serverapi.CreateNewSessionLaunchIntent(serverapi.IndependentSessionCreateOrigin())}, fakeResponses.URL, io.Discard, "test remote persisted no-auth runtime")
	secondSubmission, err := submitRuntimeClientForTest(t, secondRuntimePlan.Wiring.runtimeClient, "hello after persisted no auth")
	requireQueuedAppTestUserTurn(t, secondSubmission, err)
	waitForRemoteTranscriptAssistantFinal(
		t,
		secondRuntimePlan.Wiring.eventDispatcher.transcriptEvents,
		"second no-auth reply",
	)
	closeRuntimeLaunchPlan(t, secondRuntimePlan)
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
	bootstrap, err := remote.CompleteBootstrap(context.Background(), &authpb.CompleteBootstrapRequest{
		Mode: authpb.BootstrapMode_BOOTSTRAP_MODE_NONE,
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

	selectedTheme := onboardingpb.Theme_THEME_DARK
	_, err = remote.Finalize(context.Background(), &onboardingpb.FinalizeRequest{
		Theme: &selectedTheme,
		CommandsImport: &onboardingpb.ImportSelection{
			Mode: onboardingpb.ImportMode_IMPORT_MODE_NONE,
		},
	})
	if err != nil {
		t.Fatalf("FinalizeOnboarding: %v", err)
	}
	readinessResponse, err := remote.GetReadiness(context.Background(), &emptypb.Empty{})
	if err != nil {
		t.Fatalf("GetReadiness: %v", err)
	}
	readiness := readinessResponse.GetReadiness()
	if readiness.GetReady() {
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

	fixture := startConfiguredDaemonFixture(t, workspace, serverstartup.Request{AllowUnauthenticated: true}, readyMemoryAuthHandler())
	server := fixture.attachRemoteSessionServer(t, Options{WorkspaceRoot: workspace, WorkspaceRootExplicit: true}, readyMemoryAuthHandler())
	bound, err := ensureInteractiveProjectBinding(context.Background(), server)
	if err != nil {
		t.Fatalf("ensureInteractiveProjectBinding: %v", err)
	}
	t.Cleanup(func() { closeInteractiveSessionServer(t, bound) })
	planner := newSessionLaunchPlanner(bound)
	plan, err := planner.PlanSession(context.Background(), sessionLaunchRequest{Mode: launchModeInteractive, Intent: serverapi.CreateNewSessionLaunchIntent(serverapi.IndependentSessionCreateOrigin())})
	if err != nil {
		t.Fatalf("PlanSession: %v", err)
	}
	if plan.ActiveSettings.Model != "workspace-model" || plan.ActiveSettings.ThinkingLevel != "high" {
		t.Fatalf("active settings = %+v, want workspace-local model/thinking", plan.ActiveSettings)
	}
	if plan.ConfiguredModelName == nil || *plan.ConfiguredModelName != "workspace-model" {
		t.Fatalf("configured model = %v, want workspace-model", plan.ConfiguredModelName)
	}
	if !plan.Source.WorkspaceSettingsFileExists {
		t.Fatalf("expected workspace settings source, got %+v", plan.Source)
	}
}

func TestConfiguredDaemonEnvironmentContextUsesSessionWorkspaceRootForCWD(t *testing.T) {
	_, workspace := newRegisteredAppWorkspace(t)

	fakeResponses, hits := newFakeResponsesServer(t, []string{"interactive daemon reply"})
	defer fakeResponses.Close()

	fixture := startConfiguredDaemonFixture(t, workspace, serverstartup.Request{
		WorkspaceRoot:         workspace,
		WorkspaceRootExplicit: true,
		Model:                 "gpt-5",
		OpenAIBaseURL:         fakeResponses.URL,
		OpenAIBaseURLExplicit: true,
	}, apiKeyMemoryAuthHandler("test-key"))
	server := fixture.attachRemoteSessionServer(t, Options{WorkspaceRoot: workspace, WorkspaceRootExplicit: true}, newHeadlessAuthInteractor())

	plan, runtimePlan := prepareAppRuntimePlan(t, server, sessionLaunchRequest{Mode: launchModeInteractive, Intent: serverapi.CreateNewSessionLaunchIntent(serverapi.IndependentSessionCreateOrigin())}, io.Discard, "test daemon environment cwd")
	defer closeRuntimeLaunchPlan(t, runtimePlan)

	submission, err := submitRuntimeClientForTest(t, runtimePlan.Wiring.runtimeClient, "hello through interactive daemon")
	requireQueuedAppTestUserTurn(t, submission, err)
	waitForRemoteTranscriptAssistantFinal(
		t,
		runtimePlan.Wiring.eventDispatcher.transcriptEvents,
		"interactive daemon reply",
	)
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
		if msg.Role == llm.RoleDeveloper && msg.MessageType != nil && *msg.MessageType == llm.MessageTypeEnvironment {
			if msg.Content != nil {
				envContent = *msg.Content
			}
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
	t.Setenv("KENT_REVIEWER_FREQUENCY", "off")
	model := newAppTestModelServer(t,
		appTestModelStep{Calls: []appTestModelToolCall{
			appTestAskCall("ask-race-1", "Who answers first?", nil, 0),
		}},
		appTestModelStep{Calls: []appTestModelToolCall{
			appTestOutsidePatchCall("patch-race-1", appTestOutsidePatchPath(t)),
		}},
		appTestModelStep{Final: "multi-client prompt flow complete"},
	)
	defer model.Close()
	fixture := startRemoteMultiClientRuntimeFixture(t, model.URL())
	if got, want := fixture.serverA.ProjectID(), fixture.serverB.ProjectID(); got != want {
		t.Fatalf("project id mismatch across clients: a=%q b=%q", got, want)
	}
	if fixture.serverA.Config().WorkspaceRoot == fixture.serverB.Config().WorkspaceRoot {
		t.Fatalf("expected distinct workspace roots across clients, both=%q", fixture.serverA.Config().WorkspaceRoot)
	}
	if fixture.planB.SessionID != fixture.planA.SessionID {
		t.Fatalf("expected second client to attach same session, a=%q b=%q", fixture.planA.SessionID, fixture.planB.SessionID)
	}
	submissionDone, submissionFailed := startAppTestRuntimeSubmission(t, fixture.runtimePlanA.Wiring.runtimeClient, "start prompt flow")
	requireQueuedAppTestRuntimeSubmission(t, submissionDone)
	askPrompt := waitForRemoteTranscriptPrompt(t, fixture.runtimePlanA.Wiring.eventDispatcher.transcriptEvents, "ask-race-1", submissionFailed)
	if askPrompt.Kind != clientui.TranscriptPromptKindQuestion || askPrompt.Question != "Who answers first?" {
		t.Fatalf("unexpected ask prompt: %+v", askPrompt)
	}
	runtimeClientsB := fixture.serverB.RuntimeAttachmentClients()

	askAnswer := "answer from client B"
	if _, err := runtimeClientsB.PromptControl.AnswerPromptBatch(context.Background(), serverapi.PromptAnswerBatchRequest{
		SessionID: askPrompt.SessionID,
		StepID:    askPrompt.StepID,
		Entries: []serverapi.PromptAnswerBatchEntry{{
			PromptID:       askPrompt.PromptID,
			QuestionAnswer: &serverapi.PromptQuestionAnswer{Freeform: &askAnswer},
		}},
	}); err != nil {
		t.Fatalf("AnswerPromptBatch Question from attached client B: %v", err)
	}

	approvalPrompt := waitForRemoteTranscriptPrompt(t, fixture.runtimePlanA.Wiring.eventDispatcher.transcriptEvents, "", submissionFailed)
	if approvalPrompt.Kind != clientui.TranscriptPromptKindApproval {
		t.Fatalf("unexpected approval prompt: %+v", approvalPrompt)
	}

	commentary := "approved by client B"
	if _, err := runtimeClientsB.PromptControl.AnswerPromptBatch(context.Background(), serverapi.PromptAnswerBatchRequest{
		SessionID: approvalPrompt.SessionID,
		StepID:    approvalPrompt.StepID,
		Entries: []serverapi.PromptAnswerBatchEntry{{
			PromptID: approvalPrompt.PromptID,
			ApprovalAnswer: &serverapi.PromptApprovalAnswer{
				Decision:   clientui.ApprovalDecisionAllowOnce,
				Commentary: &commentary,
			},
		}},
	}); err != nil {
		t.Fatalf("AnswerPromptBatch Approval from attached client B: %v", err)
	}

	waitForRemoteTranscriptAssistantFinal(
		t,
		fixture.runtimePlanA.Wiring.eventDispatcher.transcriptEvents,
		"multi-client prompt flow complete",
		submissionFailed,
	)
}

type remoteMultiClientRuntimeFixture struct {
	daemon       *serverstartup.ServeServer
	serverA      *remoteAppServer
	serverB      *remoteAppServer
	planA        sessionLaunchPlan
	planB        sessionLaunchPlan
	runtimePlanA *runtimeLaunchPlan
}

func startRemoteMultiClientRuntimeFixture(t *testing.T, openAIBaseURL string) *remoteMultiClientRuntimeFixture {
	t.Helper()

	fixture := &remoteMultiClientRuntimeFixture{}
	workspaceA := t.TempDir()
	workspaceB := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	registerAppWorkspace(t, workspaceA)
	registerAppWorkspace(t, workspaceB)

	configured := startConfiguredDaemonFixture(t, workspaceA, serverstartup.Request{
		WorkspaceRoot:         workspaceA,
		WorkspaceRootExplicit: true,
		Model:                 "gpt-5",
		OpenAIBaseURL:         openAIBaseURL,
		OpenAIBaseURLExplicit: true,
	}, apiKeyMemoryAuthHandler("test-key"))
	fixture.daemon = configured.daemon
	fixture.serverA = configured.attachRemoteSessionServer(t, Options{WorkspaceRoot: workspaceA, WorkspaceRootExplicit: true}, newHeadlessAuthInteractor())

	resolvedB, err := startupconfig.ResolveSessionConfig(startupConfigRequest(Options{WorkspaceRoot: workspaceB, WorkspaceRootExplicit: true}))
	if err != nil {
		t.Fatalf("loadSessionServerConfig workspace B: %v", err)
	}
	cfgB := resolvedB.Config
	remoteB, err := client.DialRemoteURL(context.Background(), config.ServerRPCURL(cfgB))
	if err != nil {
		t.Fatalf("DialRemote workspace B: %v", err)
	}
	fixture.serverB = newRemoteAppServerWithAuth(remoteB, cfgB)
	t.Cleanup(func() { closeInteractiveSessionServer(t, fixture.serverB) })

	fixture.planA, fixture.runtimePlanA = prepareAppRuntimePlan(t, fixture.serverA, sessionLaunchRequest{Mode: launchModeInteractive, Intent: serverapi.CreateNewSessionLaunchIntent(serverapi.IndependentSessionCreateOrigin())}, io.Discard, "test remote multi-client runtime A")
	t.Cleanup(func() { closeRuntimeLaunchPlan(t, fixture.runtimePlanA) })

	plannerB := newSessionLaunchPlanner(fixture.serverB)
	fixture.planB, err = plannerB.PlanSession(context.Background(), sessionLaunchRequest{Mode: launchModeInteractive, Intent: serverapi.OpenExistingSessionLaunchIntent(sessionLifecycleSessionID(t, fixture.planA.SessionID))})
	if err != nil {
		t.Fatalf("PlanSession B: %v", err)
	}

	return fixture
}
