package transport

import (
	"context"
	serverbootstrap "core/server/bootstrap"
	"core/server/core"
	"core/server/llm"
	"core/server/metadata"
	"core/server/runtime"
	"core/server/session"
	"core/server/tools"
	askquestion "core/server/tools"
	shelltool "core/server/tools/shell"
	remoteclient "core/shared/client"
	"core/shared/clientui"
	"core/shared/config"
	"core/shared/protocol"
	"core/shared/serverapi"
	"core/shared/toolspec"
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func newGatewayTestServerForConfig(t *testing.T, cfg config.App) (*core.Core, *httptest.Server) {
	t.Helper()
	authSupport := newGatewayTestAuthSupport(t, true)
	runtimeSupport, err := serverbootstrap.BuildRuntimeSupport(cfg)
	if err != nil {
		t.Fatalf("BuildRuntimeSupport: %v", err)
	}
	t.Cleanup(func() { _ = runtimeSupport.Background.Close() })
	appCore, err := core.New(cfg, authSupport, runtimeSupport)
	if err != nil {
		t.Fatalf("core.New: %v", err)
	}
	t.Cleanup(func() { _ = appCore.Close() })
	gateway, err := NewGateway(appCore, protocol.ServerIdentity{ProtocolVersion: protocol.Version, ServerID: "server-1"})
	if err != nil {
		t.Fatalf("NewGateway: %v", err)
	}
	server := httptest.NewServer(gateway.Handler())
	t.Cleanup(server.Close)
	return appCore, server
}

func resolveGatewayTestConfig(t *testing.T, workspace string) serverbootstrap.ConfigPlan {
	t.Helper()
	resolved, err := serverbootstrap.ResolveConfig(serverbootstrap.Request{WorkspaceRoot: workspace})
	if err != nil {
		t.Fatalf("ResolveConfig: %v", err)
	}
	return resolved
}

func registerGatewayTestBinding(t *testing.T, cfg config.App) metadata.Binding {
	t.Helper()
	binding, err := metadata.RegisterBinding(context.Background(), cfg.PersistenceRoot, cfg.WorkspaceRoot)
	if err != nil {
		t.Fatalf("RegisterBinding: %v", err)
	}
	return binding
}

func TestGatewayRequiresExplicitWorkspaceSelectionForMultiWorkspaceProject(t *testing.T) {
	home := t.TempDir()
	workspaceA := t.TempDir()
	workspaceB := t.TempDir()
	t.Setenv("HOME", home)
	configureGatewayTestServerPort(t)

	resolvedA := resolveGatewayTestConfig(t, workspaceA)
	bindingA := registerGatewayTestBinding(t, resolvedA.Config)
	metadataStore, err := metadata.Open(resolvedA.Config.PersistenceRoot)
	if err != nil {
		t.Fatalf("metadata.Open: %v", err)
	}
	defer func() { _ = metadataStore.Close() }()
	bindingB, err := metadataStore.AttachWorkspaceToProject(context.Background(), bindingA.ProjectID, workspaceB)
	if err != nil {
		t.Fatalf("AttachWorkspaceToProject B: %v", err)
	}

	_, server := newGatewayTestServerForConfig(t, resolvedA.Config)

	conn := dialGateway(t, server)
	defer func() { _ = conn.Close() }()
	handshakeGateway(t, conn)
	respErr := callGatewayExpectError(t, conn, "attach-project", protocol.MethodAttachProject, protocol.AttachProjectRequest{ProjectID: bindingA.ProjectID})
	if !strings.Contains(respErr.Message, "requires explicit workspace selection") {
		t.Fatalf("expected explicit workspace selection error, got %+v", respErr)
	}

	callGateway(t, conn, "attach-project-explicit", protocol.MethodAttachProject, protocol.AttachProjectRequest{ProjectID: bindingA.ProjectID, WorkspaceID: bindingB.WorkspaceID}, nil)
	var planResp serverapi.SessionPlanResponse
	callGateway(t, conn, "session-plan", protocol.MethodSessionPlan, serverapi.SessionPlanRequest{
		ClientRequestID: "plan-after-explicit-workspace",
		Mode:            serverapi.SessionLaunchModeInteractive,
		ForceNewSession: true,
	}, &planResp)
	if got, want := planResp.Plan.WorkspaceRoot, bindingB.CanonicalRoot; got != want {
		t.Fatalf("planned workspace root = %q, want %q", got, want)
	}
}

func TestGatewayAttachSessionClearsWorkspaceOverrideForLaterPlans(t *testing.T) {
	home := t.TempDir()
	workspaceA := t.TempDir()
	workspaceB := t.TempDir()
	t.Setenv("HOME", home)
	configureGatewayTestServerPort(t)

	resolvedB := resolveGatewayTestConfig(t, workspaceB)
	bindingB := registerGatewayTestBinding(t, resolvedB.Config)
	resolvedA := resolveGatewayTestConfig(t, workspaceA)
	metadataStore, err := metadata.Open(resolvedA.Config.PersistenceRoot)
	if err != nil {
		t.Fatalf("metadata.Open: %v", err)
	}
	defer func() { _ = metadataStore.Close() }()
	if _, err := metadataStore.AttachWorkspaceToProject(context.Background(), bindingB.ProjectID, resolvedA.Config.WorkspaceRoot); err != nil {
		t.Fatalf("AttachWorkspaceToProject: %v", err)
	}

	_, server := newGatewayTestServerForConfig(t, resolvedA.Config)

	storeB, err := session.Create(
		filepath.Join(filepath.Join(resolvedA.Config.PersistenceRoot, "projects"), bindingB.ProjectID, "sessions"),
		"workspace-b",
		resolvedB.Config.WorkspaceRoot,
		metadataStore.AuthoritativeSessionStoreOptions()...,
	)
	if err != nil {
		t.Fatalf("session.Create workspace B: %v", err)
	}
	if err := storeB.EnsureDurable(); err != nil {
		t.Fatalf("EnsureDurable workspace B: %v", err)
	}

	conn := dialGateway(t, server)
	defer func() { _ = conn.Close() }()
	handshakeGateway(t, conn)
	callGateway(t, conn, "attach-project", protocol.MethodAttachProject, protocol.AttachProjectRequest{ProjectID: bindingB.ProjectID, WorkspaceRoot: resolvedA.Config.WorkspaceRoot}, nil)
	callGateway(t, conn, "attach-session", protocol.MethodAttachSession, protocol.AttachSessionRequest{SessionID: storeB.Meta().SessionID}, nil)

	var planResp serverapi.SessionPlanResponse
	callGateway(t, conn, "session-plan", protocol.MethodSessionPlan, serverapi.SessionPlanRequest{
		ClientRequestID: "new-after-attach-session",
		Mode:            serverapi.SessionLaunchModeInteractive,
		ForceNewSession: true,
	}, &planResp)
	wantWorkspaceRoot, err := config.CanonicalWorkspaceRoot(resolvedB.Config.WorkspaceRoot)
	if err != nil {
		t.Fatalf("CanonicalWorkspaceRoot B: %v", err)
	}
	if got, want := planResp.Plan.WorkspaceRoot, wantWorkspaceRoot; got != want {
		t.Fatalf("planned workspace root = %q, want %q", got, want)
	}
}

func TestGatewayScopesProcessAPIsToAttachedProject(t *testing.T) {
	home := t.TempDir()
	workspaceA := t.TempDir()
	workspaceB := t.TempDir()
	t.Setenv("HOME", home)
	configureGatewayTestServerPort(t)

	resolvedA := resolveGatewayTestConfig(t, workspaceA)
	bindingA := registerGatewayTestBinding(t, resolvedA.Config)
	resolvedB := resolveGatewayTestConfig(t, workspaceB)
	bindingB := registerGatewayTestBinding(t, resolvedB.Config)
	metadataStore, err := metadata.Open(resolvedA.Config.PersistenceRoot)
	if err != nil {
		t.Fatalf("metadata.Open: %v", err)
	}
	defer func() { _ = metadataStore.Close() }()

	appCore, server := newGatewayTestServerForConfig(t, resolvedA.Config)
	appCore.Background().SetMinimumExecToBgTime(time.Millisecond)

	storeA := createGatewayAuthoritativeSession(t, appCore)
	appCore.RegisterSessionStore(storeA)
	storeB, err := session.Create(
		filepath.Join(filepath.Join(resolvedB.Config.PersistenceRoot, "projects"), bindingB.ProjectID, "sessions"),
		"workspace-b",
		resolvedB.Config.WorkspaceRoot,
		metadataStore.AuthoritativeSessionStoreOptions()...,
	)
	if err != nil {
		t.Fatalf("session.Create foreign: %v", err)
	}
	if err := storeB.EnsureDurable(); err != nil {
		t.Fatalf("EnsureDurable foreign: %v", err)
	}

	ownResult, err := appCore.Background().Start(context.Background(), shelltool.ExecRequest{
		Command:        []string{"/bin/sh", "-lc", "printf own\\n; sleep 1"},
		DisplayCommand: "printf own; sleep 1",
		OwnerSessionID: storeA.Meta().SessionID,
		Workdir:        appCore.Config().WorkspaceRoot,
		YieldTime:      time.Millisecond,
	})
	if err != nil {
		t.Fatalf("start own process: %v", err)
	}
	foreignResult, err := appCore.Background().Start(context.Background(), shelltool.ExecRequest{
		Command:        []string{"/bin/sh", "-lc", "printf foreign\\n; sleep 1"},
		DisplayCommand: "printf foreign; sleep 1",
		OwnerSessionID: storeB.Meta().SessionID,
		Workdir:        resolvedB.Config.WorkspaceRoot,
		YieldTime:      time.Millisecond,
	})
	if err != nil {
		t.Fatalf("start foreign process: %v", err)
	}

	remote, err := remoteclient.DialRemoteURLForProject(context.Background(), "ws"+server.URL[len("http"):], bindingA.ProjectID)
	if err != nil {
		t.Fatalf("DialRemoteURLForProject: %v", err)
	}
	defer func() { _ = remote.Close() }()

	listed, err := remote.ListProcesses(context.Background(), serverapi.ProcessListRequest{})
	if err != nil {
		t.Fatalf("ListProcesses: %v", err)
	}
	if len(listed.Processes) != 1 || listed.Processes[0].ID != ownResult.SessionID {
		t.Fatalf("expected only own project process, got %+v", listed.Processes)
	}
	if _, err := remote.GetProcess(context.Background(), serverapi.ProcessGetRequest{ProcessID: foreignResult.SessionID}); err == nil {
		t.Fatal("expected foreign process get to be rejected")
	}
	if _, err := remote.GetInlineOutput(context.Background(), serverapi.ProcessInlineOutputRequest{ProcessID: foreignResult.SessionID, MaxChars: 128}); err == nil {
		t.Fatal("expected foreign process inline output to be rejected")
	}
	if _, err := remote.KillProcess(context.Background(), serverapi.ProcessKillRequest{ClientRequestID: "kill-foreign", ProcessID: foreignResult.SessionID}); err == nil {
		t.Fatal("expected foreign process kill to be rejected")
	}
	if _, err := remote.SubscribeProcessOutput(context.Background(), serverapi.ProcessOutputSubscribeRequest{ProcessID: foreignResult.SessionID, OffsetBytes: 0}); err == nil {
		t.Fatal("expected foreign process output subscription to be rejected")
	}
	if _, err := remote.GetProcess(context.Background(), serverapi.ProcessGetRequest{ProcessID: ownResult.SessionID}); err != nil {
		t.Fatalf("expected own process get to succeed, got %v", err)
	}
	if bindingA.ProjectID == bindingB.ProjectID {
		t.Fatalf("expected distinct project ids, both=%q", bindingA.ProjectID)
	}
}

func TestGatewaySessionActivitySubscriptionStreamsEventsAndCompletion(t *testing.T) {
	appCore, server := newGatewayTestServer(t)
	defer server.Close()
	store := createGatewayAuthoritativeSession(t, appCore)
	appCore.RegisterSessionStore(store)

	engine := &runtime.Engine{}
	appCore.RegisterRuntime(store.Meta().SessionID, engine)
	defer appCore.UnregisterRuntime(store.Meta().SessionID, engine)

	conn := dialGateway(t, server)
	defer func() { _ = conn.Close() }()
	handshakeGateway(t, conn)
	callGateway(t, conn, "attach", protocol.MethodAttachSession, protocol.AttachSessionRequest{SessionID: store.Meta().SessionID}, nil)
	callGateway(t, conn, "subscribe", protocol.MethodSessionSubscribeActivity, serverapi.SessionActivitySubscribeRequest{SessionID: store.Meta().SessionID}, nil)

	appCore.PublishRuntimeEvent(store.Meta().SessionID, runtime.Event{Kind: runtime.EventConversationUpdated, StepID: "step-1"})
	var event protocol.SessionActivityEventParams
	receiveGatewayNotification(t, conn, protocol.MethodSessionActivityEvent, "notification", &event)
	if event.Event.Kind != "conversation_updated" || event.Event.StepID != "step-1" {
		t.Fatalf("unexpected event: %+v", event.Event)
	}

	appCore.PublishRuntimeEvent(store.Meta().SessionID, runtime.Event{
		Kind:     runtime.EventToolCallStarted,
		ToolCall: &llm.ToolCall{ID: "call-1", Name: "shell"},
	})
	receiveGatewayNotification(t, conn, protocol.MethodSessionActivityEvent, "tool event", &event)
	if len(event.Event.TranscriptEntries) != 1 {
		t.Fatalf("tool transcript entries len = %d, want 1", len(event.Event.TranscriptEntries))
	}
	if event.Event.TranscriptEntries[0].Role != "tool_call" {
		t.Fatalf("tool transcript role = %q, want tool_call", event.Event.TranscriptEntries[0].Role)
	}
	if event.Event.TranscriptEntries[0].Text != "tool call" {
		t.Fatalf("expected raw gateway passthrough to preserve unformatted tool call text, got %+v", event.Event.TranscriptEntries[0])
	}

	appCore.UnregisterRuntime(store.Meta().SessionID, engine)
	var closing protocol.SessionActivityEventParams
	receiveGatewayNotification(t, conn, protocol.MethodSessionActivityEvent, "closing external runtime", &closing)
	if closing.Event.Kind != clientui.EventExternalRuntimeStatus ||
		closing.Event.ExternalRuntimeStatus == nil ||
		closing.Event.ExternalRuntimeStatus.State != clientui.ExternalRuntimeStateClosing ||
		closing.Event.ExternalRuntimeStatus.QueueAccepting {
		t.Fatalf("unexpected closing external runtime event: %+v", closing.Event)
	}
	var closed protocol.SessionActivityEventParams
	receiveGatewayNotification(t, conn, protocol.MethodSessionActivityEvent, "closed external runtime", &closed)
	if closed.Event.Kind != clientui.EventExternalRuntimeStatus ||
		closed.Event.ExternalRuntimeStatus == nil ||
		closed.Event.ExternalRuntimeStatus.State != "" ||
		closed.Event.ExternalRuntimeStatus.QueueAccepting {
		t.Fatalf("unexpected closed external runtime event: %+v", closed.Event)
	}
	var complete protocol.StreamCompleteParams
	receiveGatewayNotification(t, conn, protocol.MethodSessionActivityComplete, "completion", &complete)
	if complete.Code != 0 || complete.Message != "" {
		t.Fatalf("unexpected completion params: %+v", complete)
	}
}

func TestGatewayRemoteSessionActivityRecoversToolCallTextWithoutPresentation(t *testing.T) {
	appCore, server := newGatewayTestServer(t)
	defer server.Close()
	store := createGatewayAuthoritativeSession(t, appCore)
	appCore.RegisterSessionStore(store)

	engine := &runtime.Engine{}
	appCore.RegisterRuntime(store.Meta().SessionID, engine)
	defer appCore.UnregisterRuntime(store.Meta().SessionID, engine)

	remote, err := remoteclient.DialRemoteURLForProject(context.Background(), "ws"+server.URL[len("http"):], appCore.ProjectID())
	if err != nil {
		t.Fatalf("DialRemote: %v", err)
	}
	defer func() { _ = remote.Close() }()

	sub, err := remote.SubscribeSessionActivity(context.Background(), serverapi.SessionActivitySubscribeRequest{SessionID: store.Meta().SessionID})
	if err != nil {
		t.Fatalf("SubscribeSessionActivity: %v", err)
	}
	defer func() { _ = sub.Close() }()

	appCore.PublishRuntimeEvent(store.Meta().SessionID, runtime.Event{
		Kind:     runtime.EventToolCallStarted,
		ToolCall: &llm.ToolCall{ID: "call-1", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"command":"pwd"}`)},
	})

	evt, err := sub.Next(context.Background())
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if evt.Kind != clientui.EventToolCallStarted {
		t.Fatalf("event kind = %q, want %q", evt.Kind, clientui.EventToolCallStarted)
	}
	if len(evt.TranscriptEntries) != 1 {
		t.Fatalf("transcript entries len = %d, want 1", len(evt.TranscriptEntries))
	}
	entry := evt.TranscriptEntries[0]
	if entry.Role != "tool_call" || entry.Text != "pwd" {
		t.Fatalf("unexpected remote transcript entry: %+v", entry)
	}
	if entry.ToolCall == nil || !entry.ToolCall.IsShell || entry.ToolCall.Command != "pwd" {
		t.Fatalf("expected recovered shell metadata, got %+v", entry.ToolCall)
	}
}

func TestGatewayRemoteResolveWorktreeCreateTarget(t *testing.T) {
	appCore, server := newGatewayTestServer(t)
	defer server.Close()
	initGatewayGitWorkspace(t, appCore.Config().WorkspaceRoot)

	store := createGatewayAuthoritativeSession(t, appCore)
	appCore.RegisterSessionStore(store)

	remote, err := remoteclient.DialRemoteURLForProject(context.Background(), "ws"+server.URL[len("http"):], appCore.ProjectID())
	if err != nil {
		t.Fatalf("DialRemote: %v", err)
	}
	defer func() { _ = remote.Close() }()

	resp, err := remote.ResolveWorktreeCreateTarget(context.Background(), serverapi.WorktreeCreateTargetResolveRequest{
		SessionID: store.Meta().SessionID,
		Target:    "HEAD",
	})
	if err != nil {
		t.Fatalf("ResolveWorktreeCreateTarget: %v", err)
	}
	if resp.Resolution.Kind != serverapi.WorktreeCreateTargetResolutionKindDetachedRef {
		t.Fatalf("resolution kind = %q, want detached_ref", resp.Resolution.Kind)
	}
	if strings.TrimSpace(resp.Resolution.ResolvedRef) == "" {
		t.Fatalf("expected resolved ref oid, got %+v", resp.Resolution)
	}
}

func initGatewayGitWorkspace(t *testing.T, workspaceRoot string) {
	t.Helper()
	runGatewayGit(t, workspaceRoot, "init", "-b", "main")
	readmePath := filepath.Join(workspaceRoot, "README.md")
	if err := os.WriteFile(readmePath, []byte("gateway test\n"), 0o644); err != nil {
		t.Fatalf("WriteFile README.md: %v", err)
	}
	runGatewayGit(t, workspaceRoot, "add", "README.md")
	runGatewayGit(t, workspaceRoot, "commit", "-m", "init")
}

func runGatewayGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = appendTestGitCommitIdentityEnv(sanitizeTestGitEnv(os.Environ()))
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return string(output)
}

func TestGatewayRemoteSessionActivityStreamsDirectSubmittedUserMessage(t *testing.T) {
	appCore, server := newGatewayTestServer(t)
	defer server.Close()

	store := createGatewayAuthoritativeSession(t, appCore)
	activateGatewayController(t, appCore, store.Meta().SessionID)
	defer releaseGatewayController(t, appCore, store.Meta().SessionID)
	eng, err := runtime.New(store, gatewayTestLLMClient{response: llm.Response{Assistant: llm.Message{Role: llm.RoleAssistant, Content: "done"}, Usage: llm.Usage{WindowTokens: 200000}}}, tools.NewRegistry(), runtime.Config{Model: "gpt-5", OnEvent: func(evt runtime.Event) {
		appCore.PublishRuntimeEvent(store.Meta().SessionID, evt)
	}})
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}
	appCore.RegisterSessionStore(store)
	appCore.RegisterRuntime(store.Meta().SessionID, eng)
	defer appCore.UnregisterRuntime(store.Meta().SessionID, eng)

	remote, err := remoteclient.DialRemoteURLForProject(context.Background(), "ws"+server.URL[len("http"):], appCore.ProjectID())
	if err != nil {
		t.Fatalf("DialRemote: %v", err)
	}
	defer func() { _ = remote.Close() }()

	sub, err := remote.SubscribeSessionActivity(context.Background(), serverapi.SessionActivitySubscribeRequest{SessionID: store.Meta().SessionID})
	if err != nil {
		t.Fatalf("SubscribeSessionActivity: %v", err)
	}
	defer func() { _ = sub.Close() }()

	if _, err := remote.SubmitUserMessage(context.Background(), serverapi.RuntimeSubmitUserMessageRequest{ClientRequestID: "submit-say-hi", SessionID: store.Meta().SessionID, Text: "say hi"}); err != nil {
		t.Fatalf("SubmitUserMessage: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var evt clientui.Event
	for {
		next, err := sub.Next(ctx)
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		if next.Kind != clientui.EventUserMessageFlushed {
			continue
		}
		evt = next
		break
	}
	if evt.UserMessage != "say hi" {
		t.Fatalf("user message = %q, want say hi", evt.UserMessage)
	}
	if len(evt.TranscriptEntries) != 1 {
		t.Fatalf("transcript entries len = %d, want 1", len(evt.TranscriptEntries))
	}
	if evt.TranscriptEntries[0].Role != "user" || evt.TranscriptEntries[0].Text != "say hi" {
		t.Fatalf("unexpected transcript entry: %+v", evt.TranscriptEntries[0])
	}
}

func TestGatewayRemoteSessionActivityPreservesActiveSubmitOrderingUsingAssistantDeltaProgress(t *testing.T) {
	appCore, server := newGatewayTestServer(t)
	defer server.Close()

	store := createGatewayAuthoritativeSession(t, appCore)
	activateGatewayController(t, appCore, store.Meta().SessionID)
	defer releaseGatewayController(t, appCore, store.Meta().SessionID)
	eng, err := runtime.New(store, &gatewayTestStreamingClient{}, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: gatewayTestShellTool{}}), runtime.Config{Model: "gpt-5", OnEvent: func(evt runtime.Event) {
		appCore.PublishRuntimeEvent(store.Meta().SessionID, evt)
	}})
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}
	appCore.RegisterSessionStore(store)
	appCore.RegisterRuntime(store.Meta().SessionID, eng)
	defer appCore.UnregisterRuntime(store.Meta().SessionID, eng)

	remote, err := remoteclient.DialRemoteURLForProject(context.Background(), "ws"+server.URL[len("http"):], appCore.ProjectID())
	if err != nil {
		t.Fatalf("DialRemote: %v", err)
	}
	defer func() { _ = remote.Close() }()

	sub, err := remote.SubscribeSessionActivity(context.Background(), serverapi.SessionActivitySubscribeRequest{SessionID: store.Meta().SessionID})
	if err != nil {
		t.Fatalf("SubscribeSessionActivity: %v", err)
	}
	defer func() { _ = sub.Close() }()

	submitDone := make(chan error, 1)
	go func() {
		_, submitErr := remote.SubmitUserMessage(context.Background(), serverapi.RuntimeSubmitUserMessageRequest{ClientRequestID: "submit-run-tools", SessionID: store.Meta().SessionID, Text: "run tools"})
		submitDone <- submitErr
	}()

	// Remote session activity exposes both assistant_delta progress and the persisted
	// commentary assistant transcript entry for the first assistant/tool-call turn.
	// The commentary assistant event must stay distinct from the tool call event and
	// must not carry duplicated tool calls.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sequence := make([]string, 0, 6)
	commentaryTranscriptSeen := false
	for len(sequence) < 6 {
		evt, err := sub.Next(ctx)
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		for _, entry := range evt.TranscriptEntries {
			if entry.Role == "assistant" && entry.Phase == string(llm.MessagePhaseCommentary) {
				commentaryTranscriptSeen = true
			}
		}
		switch evt.Kind {
		case clientui.EventUserMessageFlushed:
			if len(evt.TranscriptEntries) != 1 || evt.TranscriptEntries[0].Role != "user" || evt.TranscriptEntries[0].Text != "run tools" {
				t.Fatalf("unexpected flushed user transcript entries: %+v", evt.TranscriptEntries)
			}
			sequence = append(sequence, "user")
		case clientui.EventAssistantDelta:
			if evt.AssistantDelta == "" {
				continue
			}
			sequence = append(sequence, "assistant_progress")
		case clientui.EventAssistantMessage:
			if len(evt.TranscriptEntries) != 1 {
				t.Fatalf("assistant transcript entries len = %d, want 1", len(evt.TranscriptEntries))
			}
			entry := evt.TranscriptEntries[0]
			if entry.Phase == string(llm.MessagePhaseCommentary) {
				if entry.Role != "assistant" || entry.Text != "Inspecting now" {
					t.Fatalf("unexpected commentary assistant transcript entry: %+v", entry)
				}
				sequence = append(sequence, "commentary")
				continue
			}
			if entry.Role != "assistant" || entry.Text != "done" || entry.Phase != string(llm.MessagePhaseFinal) {
				t.Fatalf("unexpected final assistant transcript entry: %+v", entry)
			}
			sequence = append(sequence, "final")
		case clientui.EventToolCallStarted:
			if len(evt.TranscriptEntries) != 1 || evt.TranscriptEntries[0].Role != "tool_call" || evt.TranscriptEntries[0].Text != "pwd" {
				t.Fatalf("unexpected tool call transcript entries: %+v", evt.TranscriptEntries)
			}
			sequence = append(sequence, "tool_call")
		case clientui.EventToolCallCompleted:
			if len(evt.TranscriptEntries) != 1 || evt.TranscriptEntries[0].Role != "tool_result_ok" || evt.TranscriptEntries[0].ToolCallID != "call-1" {
				t.Fatalf("unexpected tool result transcript entries: %+v", evt.TranscriptEntries)
			}
			sequence = append(sequence, "tool_result")
		}
	}
	if !commentaryTranscriptSeen {
		t.Fatalf("expected remote session activity to include commentary transcript entry for the tool-call turn, got sequence=%v", sequence)
	}
	want := []string{"user", "assistant_progress", "commentary", "tool_call", "tool_result", "final"}
	if len(sequence) != len(want) {
		t.Fatalf("sequence len = %d, want %d (%v)", len(sequence), len(want), sequence)
	}
	for i := range want {
		if sequence[i] != want[i] {
			t.Fatalf("sequence[%d] = %q, want %q (full=%v)", i, sequence[i], want[i], sequence)
		}
	}
	select {
	case err := <-submitDone:
		if err != nil {
			t.Fatalf("SubmitUserMessage: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for submit to complete")
	}
}

type gatewayTestLLMClient struct {
	response llm.Response
}

func (c gatewayTestLLMClient) Generate(context.Context, llm.Request) (llm.Response, error) {
	return c.response, nil
}

type gatewayTestStreamingClient struct {
	mu    sync.Mutex
	calls int
}

func (c *gatewayTestStreamingClient) Generate(context.Context, llm.Request) (llm.Response, error) {
	return llm.Response{}, nil
}

func (c *gatewayTestStreamingClient) GenerateStreamWithEvents(_ context.Context, _ llm.Request, callbacks llm.StreamCallbacks) (llm.Response, error) {
	c.mu.Lock()
	call := c.calls
	c.calls++
	c.mu.Unlock()
	if call == 0 {
		if callbacks.OnAssistantDelta != nil {
			callbacks.OnAssistantDelta(llm.AssistantDelta{Text: "inspecting", Phase: llm.MessagePhaseCommentary})
		}
		return llm.Response{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "Inspecting now", Phase: llm.MessagePhaseCommentary},
			ToolCalls: []llm.ToolCall{{ID: "call-1", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"command":"pwd"}`)}},
			Usage:     llm.Usage{WindowTokens: 200000},
		}, nil
	}
	return llm.Response{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: "done", Phase: llm.MessagePhaseFinal},
		Usage:     llm.Usage{WindowTokens: 200000},
	}, nil
}

func (c *gatewayTestStreamingClient) ProviderCapabilities(context.Context) (llm.ProviderCapabilities, error) {
	return llm.ProviderCapabilities{
		ProviderID:                    "openai",
		SupportsResponsesAPI:          true,
		SupportsResponsesCompact:      true,
		SupportsReasoningEncrypted:    true,
		SupportsServerSideContextEdit: true,
		IsOpenAIFirstParty:            true,
	}, nil
}

type gatewayTestShellTool struct{}

func (gatewayTestShellTool) Call(_ context.Context, call tools.Call) (tools.Result, error) {
	return tools.Result{CallID: call.ID, Name: call.Name, Output: json.RawMessage(`{"output":"/tmp\n"}`)}, nil
}

func TestGatewayProcessOutputSubscriptionStreamsOutputAndCompletion(t *testing.T) {
	appCore, server := newGatewayTestServer(t)
	defer server.Close()
	appCore.Background().SetMinimumExecToBgTime(time.Millisecond)
	store := createGatewayAuthoritativeSession(t, appCore)
	appCore.RegisterSessionStore(store)

	result, err := appCore.Background().Start(context.Background(), shelltool.ExecRequest{
		Command:        []string{"/bin/sh", "-lc", "printf 'hello\\n'; sleep 0.05"},
		DisplayCommand: "printf 'hello\\n'; sleep 0.05",
		OwnerSessionID: store.Meta().SessionID,
		Workdir:        appCore.Config().WorkspaceRoot,
		YieldTime:      time.Millisecond,
	})
	if err != nil {
		t.Fatalf("start process: %v", err)
	}

	conn := dialGateway(t, server)
	defer func() { _ = conn.Close() }()
	handshakeGateway(t, conn)
	callGateway(t, conn, "subscribe", protocol.MethodProcessSubscribeOutput, serverapi.ProcessOutputSubscribeRequest{ProcessID: result.SessionID, OffsetBytes: 0}, nil)

	var chunk protocol.ProcessOutputEventParams
	receiveGatewayNotification(t, conn, protocol.MethodProcessOutputEvent, "output", &chunk)
	if chunk.Chunk.ProcessID != result.SessionID || chunk.Chunk.OffsetBytes != 0 || chunk.Chunk.Text != "hello\n" {
		t.Fatalf("unexpected process output chunk: %+v", chunk.Chunk)
	}

	var complete protocol.StreamCompleteParams
	receiveGatewayNotification(t, conn, protocol.MethodProcessOutputComplete, "completion", &complete)
	if complete.Code != 0 || complete.Message != "" {
		t.Fatalf("unexpected completion params: %+v", complete)
	}
}

func TestGatewayPromptActivitySubscriptionStreamsPendingResolvedAndCompletion(t *testing.T) {
	appCore, server := newGatewayTestServer(t)
	defer server.Close()
	store := createGatewayAuthoritativeSession(t, appCore)
	appCore.RegisterSessionStore(store)

	engine := &runtime.Engine{}
	appCore.RegisterRuntime(store.Meta().SessionID, engine)
	defer appCore.UnregisterRuntime(store.Meta().SessionID, engine)
	appCore.BeginPendingPrompt(store.Meta().SessionID, askquestion.AskQuestionRequest{ID: "ask-1", Question: "Proceed?", Suggestions: []string{"Yes", "No"}})

	conn := dialGateway(t, server)
	defer func() { _ = conn.Close() }()
	handshakeGateway(t, conn)
	callGateway(t, conn, "attach", protocol.MethodAttachSession, protocol.AttachSessionRequest{SessionID: store.Meta().SessionID}, nil)
	callGateway(t, conn, "subscribe", protocol.MethodPromptSubscribeActivity, serverapi.PromptActivitySubscribeRequest{SessionID: store.Meta().SessionID}, nil)

	var pending protocol.PromptActivityEventParams
	receiveGatewayNotification(t, conn, protocol.MethodPromptActivityEvent, "prompt pending", &pending)
	if pending.Event.Type != clientui.PendingPromptEventPending || pending.Event.PromptID != "ask-1" || pending.Event.Question != "Proceed?" {
		t.Fatalf("unexpected pending prompt event: %+v", pending.Event)
	}

	var snapshot protocol.PromptActivityEventParams
	receiveGatewayNotification(t, conn, protocol.MethodPromptActivityEvent, "prompt snapshot", &snapshot)
	if snapshot.Event.Type != clientui.PendingPromptEventSnapshot {
		t.Fatalf("unexpected prompt snapshot event: %+v", snapshot.Event)
	}

	appCore.CompletePendingPrompt(store.Meta().SessionID, "ask-1")
	var resolved protocol.PromptActivityEventParams
	receiveGatewayNotification(t, conn, protocol.MethodPromptActivityEvent, "prompt resolved", &resolved)
	if resolved.Event.Type != clientui.PendingPromptEventResolved || resolved.Event.PromptID != "ask-1" {
		t.Fatalf("unexpected resolved prompt event: %+v", resolved.Event)
	}

	appCore.UnregisterRuntime(store.Meta().SessionID, engine)
	var complete protocol.StreamCompleteParams
	receiveGatewayNotification(t, conn, protocol.MethodPromptActivityComplete, "prompt completion", &complete)
	if complete.Code != 0 || complete.Message != "" {
		t.Fatalf("unexpected prompt completion params: %+v", complete)
	}
}
