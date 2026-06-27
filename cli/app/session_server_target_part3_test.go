package app

import (
	"context"
	serverstartup "core/server/startup"
	askquestion "core/server/tools"
	shelltool "core/server/tools/shell"
	"core/shared/client"
	"core/shared/clientui"
	"core/shared/protocol"
	"core/shared/serverapi"
	"io"
	"net"
	"net/http/httptest"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/google/uuid"
	"golang.org/x/net/websocket"
)

func TestStartSessionServerUsesConfiguredDaemonForSessionLifecycleDraftPersistence(t *testing.T) {
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

	server, err := startSessionServer(context.Background(), Options{WorkspaceRoot: workspace, WorkspaceRootExplicit: true}, newHeadlessAuthInteractor(), false)
	if err != nil {
		t.Fatalf("startSessionServer: %v", err)
	}
	defer func() { _ = server.Close() }()

	plan, runtimePlan := prepareAppRuntimePlan(t, server, sessionLaunchRequest{Mode: launchModeInteractive, ForceNewSession: true}, io.Discard, "session lifecycle draft persistence")
	defer runtimePlan.Close()
	if _, err := server.SessionLifecycleClient().PersistInputDraft(context.Background(), serverapi.SessionPersistInputDraftRequest{ClientRequestID: uuid.NewString(), SessionID: plan.SessionID, Input: "saved draft"}); err != nil {
		t.Fatalf("PersistInputDraft: %v", err)
	}
	if got := sessionLaunchInitialInputFromServer(context.Background(), server, plan.SessionID, "transition draft"); got != "saved draft" {
		t.Fatalf("sessionLaunchInitialInputFromServer = %q, want saved draft", got)
	}
	resolved, err := server.SessionLifecycleClient().ResolveTransition(context.Background(), serverapi.SessionResolveTransitionRequest{
		ClientRequestID: uuid.NewString(),
		SessionID:       plan.SessionID,
		Transition: serverapi.SessionTransition{
			Action:          "open_session",
			TargetSessionID: plan.SessionID,
			InitialInput:    "transition draft",
		},
	})
	if err != nil {
		t.Fatalf("ResolveTransition: %v", err)
	}
	if !resolved.ShouldContinue || resolved.NextSessionID != plan.SessionID || resolved.InitialInput != "transition draft" {
		t.Fatalf("unexpected resolved transition: %+v", resolved)
	}

}

func TestStartSessionServerListsPendingPromptSnapshotOverRemoteReads(t *testing.T) {
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

	server, err := startSessionServer(context.Background(), Options{WorkspaceRoot: workspace, WorkspaceRootExplicit: true}, newHeadlessAuthInteractor(), false)
	if err != nil {
		t.Fatalf("startSessionServer: %v", err)
	}
	defer func() { _ = server.Close() }()
	if _, ok := server.(*remoteAppServer); !ok {
		t.Fatalf("expected remote app server, got %T", server)
	}
	promptViews := requirePromptViewServer(t, server)

	plan, runtimePlan := prepareAppRuntimePlan(t, server, sessionLaunchRequest{Mode: launchModeInteractive, ForceNewSession: true}, io.Discard, "test remote prompt snapshot reads")
	defer runtimePlan.Close()

	askDone := make(chan error, 1)
	go func() {
		_, err := srv.AwaitPromptResponse(context.Background(), plan.SessionID, askquestion.AskQuestionRequest{ID: "ask-remote-1", Question: "Ask?"})
		askDone <- err
	}()
	approvalDone := make(chan error, 1)
	go func() {
		_, err := srv.AwaitPromptResponse(context.Background(), plan.SessionID, askquestion.AskQuestionRequest{
			ID:              "approval-remote-1",
			Question:        "Approve?",
			Approval:        true,
			ApprovalOptions: []askquestion.AskQuestionApprovalOption{{Decision: askquestion.AskQuestionApprovalDecisionAllowOnce, Label: "Allow once"}},
		})
		approvalDone <- err
	}()

	waitForPendingAskResources(t, promptViews.AskViewClient(), plan.SessionID, 1)
	waitForPendingApprovalResources(t, promptViews.ApprovalViewClient(), plan.SessionID, 1)

	first := waitForRemoteAskEvent(t, runtimePlan.Wiring.askEvents)
	second := waitForRemoteAskEvent(t, runtimePlan.Wiring.askEvents)
	for _, evt := range []askEvent{first, second} {
		switch evt.req.PromptID {
		case "ask-remote-1":
			evt.reply <- askReply{response: clientui.PromptAnswer{PromptID: evt.req.PromptID, Answer: "done"}}
		case "approval-remote-1":
			evt.reply <- askReply{response: clientui.PromptAnswer{PromptID: evt.req.PromptID, Approval: &clientui.ApprovalPromptAnswer{Decision: clientui.ApprovalDecisionAllowOnce}}}
		default:
			t.Fatalf("unexpected prompt event id %q", evt.req.PromptID)
		}
	}

	select {
	case err := <-askDone:
		if err != nil {
			t.Fatalf("AwaitPromptResponse ask: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for remote ask response")
	}
	select {
	case err := <-approvalDone:
		if err != nil {
			t.Fatalf("AwaitPromptResponse approval: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for remote approval response")
	}

	waitForPendingAskResources(t, promptViews.AskViewClient(), plan.SessionID, 0)
	waitForPendingApprovalResources(t, promptViews.ApprovalViewClient(), plan.SessionID, 0)

}

func TestStartSessionServerUsesConfiguredDaemonForProcessFlows(t *testing.T) {
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
	srv.Background().SetMinimumExecToBgTime(time.Millisecond)

	stopServing := serveAppServer(t, srv)
	defer stopServing()
	waitForConfiguredRemoteIdentity(t, workspace)

	server, err := startSessionServer(context.Background(), Options{WorkspaceRoot: workspace, WorkspaceRootExplicit: true}, newHeadlessAuthInteractor(), false)
	if err != nil {
		t.Fatalf("startSessionServer: %v", err)
	}
	defer func() { _ = server.Close() }()
	processes := requireProcessServer(t, server)

	planner := newSessionLaunchPlanner(server)
	plan, err := planner.PlanSession(context.Background(), sessionLaunchRequest{Mode: launchModeInteractive, ForceNewSession: true})
	if err != nil {
		t.Fatalf("PlanSession: %v", err)
	}

	result, err := srv.Background().Start(context.Background(), shelltool.ExecRequest{
		Command:        []string{"/bin/sh", "-lc", "printf 'daemon process output\n'; sleep 0.2"},
		DisplayCommand: "printf 'daemon process output'; sleep 0.2",
		Workdir:        workspace,
		YieldTime:      time.Millisecond,
		OwnerSessionID: plan.SessionID,
	})
	if err != nil {
		t.Fatalf("Background().Start: %v", err)
	}
	if !result.Backgrounded {
		t.Fatal("expected backgrounded process")
	}

	proc := waitForRemoteProcess(t, processes.ProcessViewClient(), plan.SessionID, result.SessionID)
	if proc.OwnerSessionID != plan.SessionID {
		t.Fatalf("unexpected process owner: %+v", proc)
	}

	getResp, err := processes.ProcessViewClient().GetProcess(context.Background(), serverapi.ProcessGetRequest{ProcessID: result.SessionID})
	if err != nil {
		t.Fatalf("GetProcess: %v", err)
	}
	if getResp.Process == nil || getResp.Process.ID != result.SessionID {
		t.Fatalf("unexpected get process response: %+v", getResp.Process)
	}

	outputSub, err := processes.ProcessOutputClient().SubscribeProcessOutput(context.Background(), serverapi.ProcessOutputSubscribeRequest{ProcessID: result.SessionID, OffsetBytes: 0})
	if err != nil {
		t.Fatalf("SubscribeProcessOutput: %v", err)
	}
	defer func() { _ = outputSub.Close() }()
	chunk, err := outputSub.Next(context.Background())
	if err != nil {
		t.Fatalf("ProcessOutput Next: %v", err)
	}
	if !strings.Contains(chunk.Text, "daemon process output") {
		t.Fatalf("unexpected process output chunk: %+v", chunk)
	}
	inlineResp := waitForRemoteInlineOutput(t, processes.ProcessControlClient(), result.SessionID)
	if !strings.Contains(inlineResp.Output, "daemon process output") {
		t.Fatalf("unexpected inline output: %q", inlineResp.Output)
	}

	if _, err := processes.ProcessControlClient().KillProcess(context.Background(), serverapi.ProcessKillRequest{ClientRequestID: uuid.NewString(), ProcessID: result.SessionID}); err != nil {
		t.Fatalf("KillProcess: %v", err)
	}
	waitForRemoteProcessExit(t, processes.ProcessViewClient(), result.SessionID)

}

func TestInteractiveSessionServerWorkflowParity(t *testing.T) {
	t.Run("embedded", func(t *testing.T) {
		_, workspace := newRegisteredAppWorkspace(t)
		fakeResponses, _ := newFakeResponsesServer(t, []string{"parity reply"})
		defer fakeResponses.Close()
		server, err := startEmbeddedServer(context.Background(), Options{
			WorkspaceRoot:         workspace,
			WorkspaceRootExplicit: true,
			Model:                 "gpt-5",
			OpenAIBaseURL:         fakeResponses.URL,
			OpenAIBaseURLExplicit: true,
		}, readyMemoryAuthHandler(), false)
		if err != nil {
			t.Fatalf("startEmbeddedServer: %v", err)
		}
		defer func() { _ = server.Close() }()
		runInteractiveWorkflowScenario(t, server, "parity reply")
	})

	t.Run("daemon", func(t *testing.T) {
		_, workspace := newRegisteredAppWorkspace(t)
		fakeResponses, _ := newFakeResponsesServer(t, []string{"parity reply"})
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
		runInteractiveWorkflowScenario(t, server, "parity reply")

	})
}

func waitForConfiguredRemoteIdentity(t *testing.T, workspace string) protocol.ServerIdentity {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	opts := Options{WorkspaceRoot: workspace, WorkspaceRootExplicit: true}
	for time.Now().Before(deadline) {
		remote, ok := tryDialMatchingConfiguredRemoteWithRequirement(context.Background(), opts, nil, nil, true)
		if ok {
			identity := remote.Identity()
			_ = remote.Close()
			return identity
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("configured daemon did not become reachable for workspace %s", workspace)
	return protocol.ServerIdentity{}
}

func waitForPIDExit(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		err := syscall.Kill(pid, 0)
		if err != nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("pid %d still running", pid)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func waitForRemoteAskEvent(t *testing.T, events <-chan askEvent) askEvent {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case evt, ok := <-events:
			if !ok {
				t.Fatal("ask event channel closed")
			}
			if evt.isResolution() {
				continue
			}
			return evt
		case <-deadline:
			t.Fatal("timed out waiting for ask event")
			return askEvent{}
		}
	}
}

func waitForSessionActivitySubscriptionEvent(t *testing.T, sub serverapi.SessionActivitySubscription, description string, predicate func(clientui.Event) bool) clientui.Event {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for {
		evt, err := sub.Next(ctx)
		if err != nil {
			t.Fatalf("session activity subscription failed while waiting for %s: %v", description, err)
		}
		if predicate == nil || predicate(evt) {
			return evt
		}
	}
}

func waitForRemoteTranscriptPage(t *testing.T, views client.SessionViewClient, sessionID string, predicate func(clientui.TranscriptPage) bool) clientui.TranscriptPage {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := views.GetSessionTranscriptPage(context.Background(), serverapi.SessionTranscriptPageRequest{SessionID: sessionID})
		if err != nil {
			t.Fatalf("GetSessionTranscriptPage: %v", err)
		}
		if predicate == nil || predicate(resp.Transcript) {
			return resp.Transcript
		}
		time.Sleep(10 * time.Millisecond)
	}
	resp, err := views.GetSessionTranscriptPage(context.Background(), serverapi.SessionTranscriptPageRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("GetSessionTranscriptPage final: %v", err)
	}
	t.Fatalf("timed out waiting for transcript page match for session %s: %+v", sessionID, resp.Transcript)
	return clientui.TranscriptPage{}
}

func transcriptPageContainsAssistantText(page clientui.TranscriptPage, want string) bool {
	for _, entry := range page.Entries {
		if entry.Role == "assistant" && entry.Text == want {
			return true
		}
	}
	return false
}

func waitForRemoteProcess(t *testing.T, views client.ProcessViewClient, sessionID string, processID string) clientui.BackgroundProcess {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := views.ListProcesses(context.Background(), serverapi.ProcessListRequest{OwnerSessionID: sessionID})
		if err != nil {
			t.Fatalf("ListProcesses: %v", err)
		}
		for _, proc := range resp.Processes {
			if proc.ID == processID {
				return proc
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for process %s in session %s", processID, sessionID)
	return clientui.BackgroundProcess{}
}

func waitForRemoteProcessExit(t *testing.T, views client.ProcessViewClient, processID string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := views.GetProcess(context.Background(), serverapi.ProcessGetRequest{ProcessID: processID})
		if err != nil {
			t.Fatalf("GetProcess: %v", err)
		}
		if resp.Process != nil && !resp.Process.Running {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for process %s to exit", processID)
}

func waitForRemoteInlineOutput(t *testing.T, controls client.ProcessControlClient, processID string) serverapi.ProcessInlineOutputResponse {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := controls.GetInlineOutput(context.Background(), serverapi.ProcessInlineOutputRequest{ProcessID: processID, MaxChars: 1024})
		if err != nil {
			t.Fatalf("GetInlineOutput: %v", err)
		}
		if strings.TrimSpace(resp.Output) != "" {
			return resp
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for inline output from %s", processID)
	return serverapi.ProcessInlineOutputResponse{}
}

func runInteractiveWorkflowScenario(t *testing.T, server interactiveSessionServer, wantReply string) {
	t.Helper()
	plan, runtimePlan := prepareAppRuntimePlan(t, server, sessionLaunchRequest{Mode: launchModeInteractive, ForceNewSession: true}, io.Discard, "workflow parity")
	defer runtimePlan.Close()

	message, err := runtimePlan.Wiring.runtimeClient.SubmitUserMessage(context.Background(), "hello parity")
	if err != nil {
		t.Fatalf("SubmitUserMessage: %v", err)
	}
	if message != wantReply {
		t.Fatalf("assistant message = %q, want %q", message, wantReply)
	}
	if _, err := server.SessionLifecycleClient().PersistInputDraft(context.Background(), serverapi.SessionPersistInputDraftRequest{ClientRequestID: uuid.NewString(), SessionID: plan.SessionID, Input: "workflow draft"}); err != nil {
		t.Fatalf("PersistInputDraft: %v", err)
	}
	if got := sessionLaunchInitialInputFromServer(context.Background(), server, plan.SessionID, "transition draft"); got != "workflow draft" {
		t.Fatalf("sessionLaunchInitialInputFromServer = %q, want workflow draft", got)
	}
	refreshed, err := server.SessionViewClient().GetSessionMainView(context.Background(), serverapi.SessionMainViewRequest{SessionID: plan.SessionID})
	if err != nil {
		t.Fatalf("GetSessionMainView: %v", err)
	}
	if refreshed.MainView.Session.Transcript.CommittedEntryCount == 0 {
		t.Fatalf("expected transcript metadata, got %+v", refreshed.MainView.Session.Transcript)
	}
}

type promptViewTestServer interface {
	AskViewClient() client.AskViewClient
	ApprovalViewClient() client.ApprovalViewClient
}

func requirePromptViewServer(t *testing.T, server any) promptViewTestServer {
	t.Helper()
	promptViews, ok := server.(promptViewTestServer)
	if !ok {
		runtimeSource, runtimeOK := server.(runtimeAttachmentSource)
		if !runtimeOK {
			t.Fatalf("server %T does not expose prompt view clients", server)
		}
		return runtimePromptViewTestServer{clients: runtimeSource.RuntimeAttachmentClients()}
	}
	return promptViews
}

type runtimePromptViewTestServer struct {
	clients runtimeAttachmentClients
}

func (s runtimePromptViewTestServer) AskViewClient() client.AskViewClient {
	return s.clients.AskViews
}

func (s runtimePromptViewTestServer) ApprovalViewClient() client.ApprovalViewClient {
	return s.clients.ApprovalViews
}

type processTestServer interface {
	ProcessControlClient() client.ProcessControlClient
	ProcessOutputClient() client.ProcessOutputClient
	ProcessViewClient() client.ProcessViewClient
}

func requireProcessServer(t *testing.T, server any) processTestServer {
	t.Helper()
	processes, ok := server.(processTestServer)
	if !ok {
		runtimeSource, runtimeOK := server.(runtimeAttachmentSource)
		if !runtimeOK {
			t.Fatalf("server %T does not expose process clients", server)
		}
		return runtimeProcessTestServer{clients: runtimeSource.RuntimeAttachmentClients()}
	}
	return processes
}

type runtimeProcessTestServer struct {
	clients runtimeAttachmentClients
}

func (s runtimeProcessTestServer) ProcessControlClient() client.ProcessControlClient {
	return s.clients.ProcessControls
}

func (s runtimeProcessTestServer) ProcessOutputClient() client.ProcessOutputClient {
	return s.clients.ProcessOutput
}

func (s runtimeProcessTestServer) ProcessViewClient() client.ProcessViewClient {
	return s.clients.ProcessViews
}

func publishConfiguredRemoteForWorkspace(t *testing.T, workspace string, caps protocol.CapabilityFlags) func() {
	t.Helper()
	identity := protocol.ServerIdentity{
		ProtocolVersion: protocol.Version,
		ServerID:        "stale-daemon",
		PID:             222,
		Capabilities:    caps,
	}
	server := httptest.NewServer(websocket.Handler(func(ws *websocket.Conn) {
		defer func() { _ = ws.Close() }()
		var req protocol.Request
		if err := websocket.JSON.Receive(ws, &req); err != nil {
			return
		}
		if req.Method != protocol.MethodHandshake {
			_ = websocket.JSON.Send(ws, protocol.NewErrorResponse(req.ID, protocol.ErrCodeInvalidRequest, "handshake required"))
			return
		}
		if err := websocket.JSON.Send(ws, protocol.NewSuccessResponse(req.ID, protocol.HandshakeResponse{Identity: identity})); err != nil {
			return
		}
		for {
			if err := websocket.JSON.Receive(ws, &req); err != nil {
				return
			}
			_ = websocket.JSON.Send(ws, protocol.NewErrorResponse(req.ID, protocol.ErrCodeMethodNotFound, "method not found"))
		}
	}))
	host, port, err := net.SplitHostPort(strings.TrimPrefix(server.URL, "http://"))
	if err != nil {
		server.Close()
		t.Fatalf("SplitHostPort: %v", err)
	}
	t.Setenv("KENT_SERVER_HOST", host)
	t.Setenv("KENT_SERVER_PORT", port)
	return func() {
		server.Close()
	}
}
