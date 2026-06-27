package app

import (
	"context"
	"core/cli/app/internal/startupconfig"
	"core/server/llm"
	"core/server/metadata"
	serverstartup "core/server/startup"
	askquestion "core/server/tools"
	"core/shared/client"
	"core/shared/clientui"
	"core/shared/config"
	"core/shared/serverapi"
	"errors"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestStartSessionServerHelperDaemonProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_DAEMON") != "1" {
		return
	}
	workspace := strings.TrimSpace(os.Getenv("GO_HELPER_WORKSPACE_ROOT"))
	if workspace == "" {
		t.Fatal("GO_HELPER_WORKSPACE_ROOT is required")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	srv, err := serverstartup.StartServeServer(context.Background(), serverstartup.Request{
		WorkspaceRoot:         workspace,
		WorkspaceRootExplicit: true,
		Model:                 "gpt-5",
	}, apiKeyMemoryAuthHandler("test-key"), autoOnboarding)
	if err != nil {
		t.Fatalf("serve.Start: %v", err)
	}
	defer func() { _ = srv.Close() }()
	if err := srv.Serve(ctx); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("Serve: %v", err)
	}
}

func TestStartSessionServerUsesConfiguredDaemonForInteractiveFlow(t *testing.T) {
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

	server, err := startSessionServer(context.Background(), Options{WorkspaceRoot: workspace, WorkspaceRootExplicit: true}, readyMemoryAuthHandler(), false)
	if err != nil {
		t.Fatalf("startSessionServer: %v", err)
	}
	defer func() { _ = server.Close() }()
	if _, ok := server.(*remoteAppServer); !ok {
		t.Fatalf("expected remote app server, got %T", server)
	}

	plan, runtimePlan := prepareAppRuntimePlan(t, server, sessionLaunchRequest{Mode: launchModeInteractive, ForceNewSession: true}, io.Discard, "test remote interactive runtime")
	defer runtimePlan.Close()

	submission, err := runtimePlan.Wiring.runtimeClient.SubmitUserMessage(context.Background(), "hello through interactive daemon")
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

	refreshed, err := server.SessionViewClient().GetSessionMainView(context.Background(), serverapi.SessionMainViewRequest{SessionID: plan.SessionID})
	if err != nil {
		t.Fatalf("GetSessionMainView: %v", err)
	}
	if refreshed.MainView.Session.Transcript.CommittedEntryCount == 0 {
		t.Fatalf("expected refreshed transcript metadata, got %+v", refreshed.MainView.Session.Transcript)
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
	planner := newSessionLaunchPlanner(server)
	plan, err := planner.PlanSession(context.Background(), sessionLaunchRequest{Mode: launchModeInteractive, ForceNewSession: true})
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

	plan, runtimePlan := prepareAppRuntimePlan(t, server, sessionLaunchRequest{Mode: launchModeInteractive, ForceNewSession: true}, io.Discard, "test daemon environment cwd")
	defer runtimePlan.Close()

	submission, err := runtimePlan.Wiring.runtimeClient.SubmitUserMessage(context.Background(), "hello through interactive daemon")
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

func TestRemoteInteractiveRuntimeTwoClientsConvergeOnSameSessionAcrossWorkspaces(t *testing.T) {
	fakeResponses, hits := newFakeResponsesServer(t, []string{"shared daemon reply"})
	defer fakeResponses.Close()
	fixture := startRemoteMultiClientRuntimeFixture(t, fakeResponses.URL)

	submission, err := fixture.runtimePlanA.Wiring.runtimeClient.SubmitUserMessage(context.Background(), "hello from client A")
	message := submission.Message
	if err != nil {
		t.Fatalf("SubmitUserMessage A: %v", err)
	}
	if message != "shared daemon reply" {
		t.Fatalf("assistant message = %q, want %q", message, "shared daemon reply")
	}
	if hits.Load() != 1 {
		t.Fatalf("expected one daemon-backed llm call, got %d", hits.Load())
	}

	pageA := waitForRemoteTranscriptPage(t, fixture.serverA.SessionViewClient(), fixture.planA.SessionID, func(page clientui.TranscriptPage) bool {
		return transcriptPageContainsAssistantText(page, "shared daemon reply")
	})
	pageB := waitForRemoteTranscriptPage(t, fixture.serverB.SessionViewClient(), fixture.planA.SessionID, func(page clientui.TranscriptPage) bool {
		return transcriptPageContainsAssistantText(page, "shared daemon reply")
	})

	if pageA.Revision != pageB.Revision {
		t.Fatalf("expected clients to converge on same transcript revision, a=%d b=%d", pageA.Revision, pageB.Revision)
	}
	if pageA.TotalEntries != pageB.TotalEntries {
		t.Fatalf("expected clients to converge on same transcript size, a=%d b=%d", pageA.TotalEntries, pageB.TotalEntries)
	}
	if !transcriptPageContainsAssistantText(pageA, "shared daemon reply") || !transcriptPageContainsAssistantText(pageB, "shared daemon reply") {
		t.Fatalf("expected both clients to hydrate assistant reply, pageA=%+v pageB=%+v", pageA, pageB)
	}
}

func TestRemoteReadOnlyClientHydratesCommittedTranscriptAcrossWorkspaces(t *testing.T) {
	fakeResponses, hits := newFakeResponsesServer(t, []string{"reply while client B disconnected", "reply after client B reconnects"})
	defer fakeResponses.Close()
	fixture := startRemoteMultiClientRuntimeFixture(t, fakeResponses.URL)

	firstSubmission, err := fixture.runtimePlanA.Wiring.runtimeClient.SubmitUserMessage(context.Background(), "message while client B is disconnected")
	firstMessage := firstSubmission.Message
	if err != nil {
		t.Fatalf("SubmitUserMessage before reconnect: %v", err)
	}
	if firstMessage != "reply while client B disconnected" {
		t.Fatalf("assistant message before reconnect = %q, want %q", firstMessage, "reply while client B disconnected")
	}
	if hits.Load() != 1 {
		t.Fatalf("expected one daemon-backed llm call before reconnect, got %d", hits.Load())
	}
	pageA1 := waitForRemoteTranscriptPage(t, fixture.serverA.SessionViewClient(), fixture.planA.SessionID, func(page clientui.TranscriptPage) bool {
		return transcriptPageContainsAssistantText(page, "reply while client B disconnected")
	})

	hydratedB := waitForRemoteTranscriptPage(t, fixture.serverB.SessionViewClient(), fixture.planA.SessionID, func(page clientui.TranscriptPage) bool {
		return transcriptPageContainsAssistantText(page, "reply while client B disconnected")
	})
	if !transcriptPageContainsAssistantText(hydratedB, "reply while client B disconnected") {
		t.Fatalf("expected reconnecting client to hydrate missed committed reply, got %+v", hydratedB)
	}
	if hydratedB.Revision != pageA1.Revision || hydratedB.TotalEntries != pageA1.TotalEntries {
		t.Fatalf("expected reconnect hydrate to match authoritative transcript head, hydrated=%+v pageA=%+v", hydratedB, pageA1)
	}

	secondSubmission, err := fixture.runtimePlanA.Wiring.runtimeClient.SubmitUserMessage(context.Background(), "message after client B reconnects")
	secondMessage := secondSubmission.Message
	if err != nil {
		t.Fatalf("SubmitUserMessage after reconnect: %v", err)
	}
	if secondMessage != "reply after client B reconnects" {
		t.Fatalf("assistant message after reconnect = %q, want %q", secondMessage, "reply after client B reconnects")
	}
	if hits.Load() != 2 {
		t.Fatalf("expected two daemon-backed llm calls after reconnect flow, got %d", hits.Load())
	}

	pageA2 := waitForRemoteTranscriptPage(t, fixture.serverA.SessionViewClient(), fixture.planA.SessionID, func(page clientui.TranscriptPage) bool {
		return transcriptPageContainsAssistantText(page, "reply after client B reconnects")
	})
	pageB2 := waitForRemoteTranscriptPage(t, fixture.serverB.SessionViewClient(), fixture.planA.SessionID, func(page clientui.TranscriptPage) bool {
		return transcriptPageContainsAssistantText(page, "reply after client B reconnects")
	})
	if pageA2.Revision != pageB2.Revision || pageA2.TotalEntries != pageB2.TotalEntries {
		t.Fatalf("expected both clients to converge after read-only hydrate, a=%+v b=%+v", pageA2, pageB2)
	}
}

func TestRemoteInteractiveRuntimeAskAnswersFromAnyAttachedClientAcrossWorkspaces(t *testing.T) {
	fixture := startRemoteMultiClientRuntimeFixture(t, "")

	askDone := make(chan struct {
		resp askquestion.AskQuestionResponse
		err  error
	}, 1)
	go func() {
		resp, err := fixture.daemon.AwaitPromptResponse(context.Background(), fixture.planA.SessionID, askquestion.AskQuestionRequest{
			ID:       "ask-race-1",
			Question: "Who answers first?",
		})
		askDone <- struct {
			resp askquestion.AskQuestionResponse
			err  error
		}{resp: resp, err: err}
	}()

	askEvtA := waitForRemoteAskEvent(t, fixture.runtimePlanA.Wiring.askEvents)
	if askEvtA.req.PromptID != "ask-race-1" || askEvtA.req.Question != "Who answers first?" || askEvtA.req.Approval {
		t.Fatalf("unexpected ask event: %+v", askEvtA.req)
	}
	runtimeClientsA := fixture.serverA.RuntimeAttachmentClients()
	runtimeClientsB := fixture.serverB.RuntimeAttachmentClients()
	waitForPendingAskResources(t, runtimeClientsB.AskViews, fixture.planA.SessionID, 1)

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
	waitForPendingAskResources(t, runtimeClientsA.AskViews, fixture.planA.SessionID, 0)
	waitForPendingAskResources(t, runtimeClientsB.AskViews, fixture.planA.SessionID, 0)
}

func TestRemoteInteractiveRuntimeApprovalAnswersFromAnyAttachedClientAcrossWorkspaces(t *testing.T) {
	fixture := startRemoteMultiClientRuntimeFixture(t, "")

	approvalDone := make(chan struct {
		resp askquestion.AskQuestionResponse
		err  error
	}, 1)
	go func() {
		resp, err := fixture.daemon.AwaitPromptResponse(context.Background(), fixture.planA.SessionID, askquestion.AskQuestionRequest{
			ID:              "approval-race-1",
			Question:        "Allow the command?",
			Approval:        true,
			ApprovalOptions: []askquestion.AskQuestionApprovalOption{{Decision: askquestion.AskQuestionApprovalDecisionAllowOnce, Label: "Allow once"}, {Decision: askquestion.AskQuestionApprovalDecisionDeny, Label: "Deny"}},
		})
		approvalDone <- struct {
			resp askquestion.AskQuestionResponse
			err  error
		}{resp: resp, err: err}
	}()

	approvalEvtA := waitForRemoteAskEvent(t, fixture.runtimePlanA.Wiring.askEvents)
	if approvalEvtA.req.PromptID != "approval-race-1" || approvalEvtA.req.Question != "Allow the command?" || !approvalEvtA.req.Approval {
		t.Fatalf("unexpected approval event: %+v", approvalEvtA.req)
	}
	runtimeClientsA := fixture.serverA.RuntimeAttachmentClients()
	runtimeClientsB := fixture.serverB.RuntimeAttachmentClients()
	waitForPendingApprovalResources(t, runtimeClientsB.ApprovalViews, fixture.planA.SessionID, 1)

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
	waitForPendingApprovalResources(t, runtimeClientsA.ApprovalViews, fixture.planA.SessionID, 0)
	waitForPendingApprovalResources(t, runtimeClientsB.ApprovalViews, fixture.planA.SessionID, 0)
}

func TestRemoteSessionActivityLaggingSubscriberHydratesAndResubscribesAcrossWorkspaces(t *testing.T) {
	fakeResponses, hits := newFakeResponsesServer(t, []string{"reply before remote gap", "reply after gap recovery"})
	defer fakeResponses.Close()
	fixture := startRemoteMultiClientRuntimeFixture(t, fakeResponses.URL)

	submission, err := fixture.runtimePlanA.Wiring.runtimeClient.SubmitUserMessage(context.Background(), "message before remote gap")
	message := submission.Message
	if err != nil {
		t.Fatalf("SubmitUserMessage before gap: %v", err)
	}
	if message != "reply before remote gap" {
		t.Fatalf("assistant message before gap = %q, want %q", message, "reply before remote gap")
	}

	runtimeClientsB := fixture.serverB.RuntimeAttachmentClients()
	if _, err := runtimeClientsB.SessionActivity.SubscribeSessionActivity(context.Background(), serverapi.SessionActivitySubscribeRequest{
		SessionID:     fixture.planA.SessionID,
		AfterSequence: ^uint64(0),
	}); !errors.Is(err, serverapi.ErrStreamGap) {
		t.Fatalf("expected remote stale cursor to fail with stream gap, got %v", err)
	}

	pageA := waitForRemoteTranscriptPage(t, fixture.serverA.SessionViewClient(), fixture.planA.SessionID, func(page clientui.TranscriptPage) bool {
		return transcriptPageContainsAssistantText(page, "reply before remote gap")
	})
	pageB := waitForRemoteTranscriptPage(t, fixture.serverB.SessionViewClient(), fixture.planA.SessionID, func(page clientui.TranscriptPage) bool {
		return page.Revision == pageA.Revision && page.TotalEntries == pageA.TotalEntries
	})
	if pageA.Revision != pageB.Revision || pageA.TotalEntries != pageB.TotalEntries {
		t.Fatalf("expected authoritative transcript hydrate to converge after stream gap, a=%+v b=%+v", pageA, pageB)
	}

	recoveredSub, err := runtimeClientsB.SessionActivity.SubscribeSessionActivity(context.Background(), serverapi.SessionActivitySubscribeRequest{SessionID: fixture.planA.SessionID})
	if err != nil {
		t.Fatalf("SubscribeSessionActivity recovered client: %v", err)
	}
	defer func() { _ = recoveredSub.Close() }()

	submission, err = fixture.runtimePlanA.Wiring.runtimeClient.SubmitUserMessage(context.Background(), "message after lagging subscriber recovers")
	message = submission.Message
	if err != nil {
		t.Fatalf("SubmitUserMessage after gap recovery: %v", err)
	}
	if message != "reply after gap recovery" {
		t.Fatalf("assistant message after gap recovery = %q, want %q", message, "reply after gap recovery")
	}
	if hits.Load() != 2 {
		t.Fatalf("expected two daemon-backed llm calls after recovery message, got %d", hits.Load())
	}

	assistantEvt := waitForSessionActivitySubscriptionEvent(t, recoveredSub, "assistant message after gap recovery", func(evt clientui.Event) bool {
		if evt.Kind != clientui.EventAssistantMessage {
			return false
		}
		for _, entry := range evt.TranscriptEntries {
			if entry.Role == "assistant" && entry.Text == "reply after gap recovery" {
				return true
			}
		}
		return false
	})
	if assistantEvt.StepID == "" {
		t.Fatalf("expected assistant event step id after gap recovery, got %+v", assistantEvt)
	}

	pageA2 := waitForRemoteTranscriptPage(t, fixture.serverA.SessionViewClient(), fixture.planA.SessionID, func(page clientui.TranscriptPage) bool {
		return transcriptPageContainsAssistantText(page, "reply after gap recovery")
	})
	pageB2 := waitForRemoteTranscriptPage(t, fixture.serverB.SessionViewClient(), fixture.planA.SessionID, func(page clientui.TranscriptPage) bool {
		return transcriptPageContainsAssistantText(page, "reply after gap recovery")
	})
	if pageA2.Revision != pageB2.Revision || pageA2.TotalEntries != pageB2.TotalEntries {
		t.Fatalf("expected both clients to converge after gap recovery, a=%+v b=%+v", pageA2, pageB2)
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

type promptAnswerResult struct {
	client string
	err    error
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
	remoteB, err := client.DialRemoteURLForProject(context.Background(), config.ServerRPCURL(cfgB), fixture.serverA.ProjectID())
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

	fixture.planA, fixture.runtimePlanA = prepareAppRuntimePlan(t, fixture.serverA, sessionLaunchRequest{Mode: launchModeInteractive, ForceNewSession: true}, io.Discard, "test remote multi-client runtime A")

	plannerB := newSessionLaunchPlanner(fixture.serverB)
	fixture.planB, err = plannerB.PlanSession(context.Background(), sessionLaunchRequest{Mode: launchModeInteractive, SelectedSessionID: fixture.planA.SessionID})
	if err != nil {
		t.Fatalf("PlanSession B: %v", err)
	}
	if fixture.planB.SessionID != fixture.planA.SessionID {
		t.Fatalf("expected second client to attach same session, a=%q b=%q", fixture.planA.SessionID, fixture.planB.SessionID)
	}

	return fixture
}

func TestShouldBypassRemoteStartupForInteractiveOnboardingOnFirstRun(t *testing.T) {
	_, workspace := newRegisteredAppWorkspace(t)

	cfg, err := startupconfig.ResolveSessionConfig(startupConfigRequest(Options{WorkspaceRoot: workspace, WorkspaceRootExplicit: true}))
	if err != nil {
		t.Fatalf("loadSessionServerConfig: %v", err)
	}
	bypass := shouldBypassRemoteStartupForInteractiveOnboardingWithConfig(cfg, true)
	if !bypass {
		t.Fatal("expected first-run interactive startup to bypass remote onboarding paths")
	}
}

func TestShouldBypassRemoteStartupForInteractiveOnboardingSkipsWhenConfigExists(t *testing.T) {
	_, workspace := newRegisteredAppWorkspace(t)
	if _, _, err := config.WriteDefaultSettingsFile(); err != nil {
		t.Fatalf("WriteDefaultSettingsFile: %v", err)
	}

	cfg, err := startupconfig.ResolveSessionConfig(startupConfigRequest(Options{WorkspaceRoot: workspace, WorkspaceRootExplicit: true}))
	if err != nil {
		t.Fatalf("loadSessionServerConfig: %v", err)
	}
	bypass := shouldBypassRemoteStartupForInteractiveOnboardingWithConfig(cfg, true)
	if bypass {
		t.Fatal("expected configured interactive startup to keep remote onboarding paths enabled")
	}
}

func TestStartSessionServerBypassesRemoteAndDaemonOnFirstInteractiveRun(t *testing.T) {
	_, workspace := newRegisteredAppWorkspace(t)

	originalDial := dialConfiguredProjectViewRemote
	originalLaunch := launchSessionServerDaemon
	originalEmbedded := startInteractiveEmbeddedSessionServer
	defer func() {
		dialConfiguredProjectViewRemote = originalDial
		launchSessionServerDaemon = originalLaunch
		startInteractiveEmbeddedSessionServer = originalEmbedded
	}()

	remoteCalled := false
	daemonCalled := false
	embeddedCalled := false
	startInteractiveEmbeddedSessionServer = func(_ context.Context, _ Options, _ authInteractor, _ bool) (*embeddedAppServer, error) {
		embeddedCalled = true
		return &embeddedAppServer{}, nil
	}
	dialConfiguredProjectViewRemote = func(context.Context, config.App) (configuredProjectViewRemote, error) {
		remoteCalled = true
		return nil, errors.New("configured remote should be skipped")
	}
	launchSessionServerDaemon = func(context.Context, Options) (*client.Remote, func() error, bool, error) {
		daemonCalled = true
		return nil, nil, false, nil
	}

	server, err := startSessionServer(context.Background(), Options{WorkspaceRoot: workspace, WorkspaceRootExplicit: true}, &stubAuthInteractor{}, true)
	if err != nil {
		t.Fatalf("startSessionServer: %v", err)
	}
	defer func() { _ = server.Close() }()
	if !embeddedCalled {
		t.Fatal("expected embedded startup path to be used")
	}
	if remoteCalled {
		t.Fatal("expected remote startup path to be skipped on first interactive run")
	}
	if daemonCalled {
		t.Fatal("expected daemon launch path to be skipped on first interactive run")
	}
	if _, ok := server.(*embeddedAppServer); !ok {
		t.Fatalf("expected embedded app server, got %T", server)
	}
}

func TestStartSessionServerUnregisteredWorkspaceStartsRegistrationCapableServer(t *testing.T) {
	newAppTestHome(t)
	workspace := t.TempDir()
	configureAppTestServerPort(t)

	server, err := startSessionServer(context.Background(), Options{WorkspaceRoot: workspace, WorkspaceRootExplicit: true}, readyMemoryAuthHandler(), false)
	if err != nil {
		t.Fatalf("startSessionServer: %v", err)
	}
	defer func() { _ = server.Close() }()

	if got := server.ProjectID(); got != "" {
		t.Fatalf("project id = %q, want empty for unregistered workspace", got)
	}
	resolved, err := server.ProjectViewClient().ResolveProjectPath(context.Background(), serverapi.ProjectResolvePathRequest{Path: workspace})
	if err != nil {
		t.Fatalf("ResolveProjectPath: %v", err)
	}
	if resolved.Binding != nil {
		t.Fatalf("expected unknown workspace resolution, got %+v", resolved.Binding)
	}
}
