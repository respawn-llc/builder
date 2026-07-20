package transport

import (
	"context"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"core/server/attentionnotify"
	"core/server/core"
	"core/server/registry"
	"core/server/runtime"
	"core/server/sessionruntime"
	askquestion "core/server/tools"
	"core/shared/apicontract"
	remoteclient "core/shared/client"
	"core/shared/clientui"
	"core/shared/protocol"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

func TestGatewayRemoteAttentionDesktopRouteIsRootGlobalAndLiveOnly(t *testing.T) {
	appCore, prompts, server := newGatewayAttentionTestServer(t)
	defer func() { _ = appCore.Close() }()
	defer server.Close()

	sessionOne := createGatewayAuthoritativeSession(t, appCore)
	activateGatewayController(t, appCore, sessionOne.Metadata().SessionID)
	registerGatewayPromptRuntime(t, prompts, sessionOne.Metadata().SessionID)
	sessionTwo := createGatewayAuthoritativeSession(t, appCore)
	activateGatewayController(t, appCore, sessionTwo.Metadata().SessionID)
	registerGatewayPromptRuntime(t, prompts, sessionTwo.Metadata().SessionID)

	beginGatewayPendingPrompt(t, prompts, sessionOne.Metadata().SessionID, gatewayTaskBatchAskRequest("old-ask", "project-old", "task-old", sessionOne.Metadata().SessionID))
	remote, err := remoteclient.DialRemoteURL(context.Background(), "ws"+server.URL[len("http"):])
	if err != nil {
		t.Fatalf("DialRemoteURL: %v", err)
	}
	defer func() { _ = remote.Close() }()
	desktop, err := remote.SubscribeAttentionNotifications(context.Background(), serverapi.AttentionNotificationSubscribeRequest{})
	if err != nil {
		t.Fatalf("SubscribeAttentionNotifications: %v", err)
	}
	if event, err := desktop.Next(shortGatewayAttentionContext(t)); err == nil {
		t.Fatalf("desktop subscription replayed old pending attention: %+v", event)
	}

	beginGatewayPendingPrompt(t, prompts, sessionOne.Metadata().SessionID, gatewayTaskBatchAskRequest("ask-a", "project-a", "task-a", sessionOne.Metadata().SessionID))
	beginGatewayPendingPrompt(t, prompts, sessionTwo.Metadata().SessionID, gatewayTaskBatchAskRequest("ask-b", "project-b", "task-b", sessionTwo.Metadata().SessionID))
	first := nextGatewayAttentionEvent(t, desktop)
	second := nextGatewayAttentionEvent(t, desktop)
	if first.Pending.Target.ProjectID != "project-a" || second.Pending.Target.ProjectID != "project-b" {
		t.Fatalf("desktop cross-project events = %+v then %+v", first, second)
	}

	beginGatewayPendingPrompt(t, prompts, sessionOne.Metadata().SessionID, askquestion.AskQuestionRequest{ID: "generic-ask", StepID: gatewayAttentionStepID, Question: "Generic?"})
	if event, err := desktop.Next(shortGatewayAttentionContext(t)); err == nil {
		t.Fatalf("desktop received generic session prompt: %+v", event)
	}
}

func TestGatewaySessionAttentionRouteRequiresAttachedSession(t *testing.T) {
	appCore, _, server := newGatewayAttentionTestServer(t)
	defer func() { _ = appCore.Close() }()
	defer server.Close()
	sessionOne := createGatewayAuthoritativeSession(t, appCore)
	activateGatewayController(t, appCore, sessionOne.Metadata().SessionID)
	sessionTwo := createGatewayAuthoritativeSession(t, appCore)
	activateGatewayController(t, appCore, sessionTwo.Metadata().SessionID)

	conn := dialGateway(t, server)
	defer func() { _ = conn.Close() }()
	handshakeGateway(t, conn)
	errResp := callGatewayExpectError(t, conn, "attention-without-attach", protocol.MethodAttentionSessionNotificationSubscribe, serverapi.AttentionSessionNotificationSubscribeRequest{SessionID: sessionOne.Metadata().SessionID})
	if errResp.Code != protocol.ErrCodeInvalidRequest {
		t.Fatalf("missing attach error = %+v", errResp)
	}
	_ = conn.Close()

	conn = dialGateway(t, server)
	defer func() { _ = conn.Close() }()
	handshakeGateway(t, conn)
	callGateway(t, conn, "attach-session-one", protocol.MethodAttachSession, protocol.AttachSessionRequest{SessionID: sessionOne.Metadata().SessionID}, nil)
	errResp = callGatewayExpectError(t, conn, "attention-wrong-session", protocol.MethodAttentionSessionNotificationSubscribe, serverapi.AttentionSessionNotificationSubscribeRequest{SessionID: sessionTwo.Metadata().SessionID})
	if errResp.Code != protocol.ErrCodeInvalidRequest {
		t.Fatalf("wrong attach error = %+v", errResp)
	}
}

func TestGatewayRemoteSessionAttentionReceivesAuthorizedGenericPrompt(t *testing.T) {
	appCore, prompts, server := newGatewayAttentionTestServer(t)
	defer func() { _ = appCore.Close() }()
	defer server.Close()
	sessionStore := createGatewayAuthoritativeSession(t, appCore)
	activateGatewayController(t, appCore, sessionStore.Metadata().SessionID)
	registerGatewayPromptRuntime(t, prompts, sessionStore.Metadata().SessionID)

	remote, err := remoteclient.DialRemoteURL(context.Background(), "ws"+server.URL[len("http"):])
	if err != nil {
		t.Fatalf("DialRemoteURL: %v", err)
	}
	defer func() { _ = remote.Close() }()
	sub, err := remote.SubscribeSessionAttentionNotifications(context.Background(), serverapi.AttentionSessionNotificationSubscribeRequest{SessionID: sessionStore.Metadata().SessionID})
	if err != nil {
		t.Fatalf("SubscribeSessionAttentionNotifications: %v", err)
	}
	beginGatewayPendingPrompt(t, prompts, sessionStore.Metadata().SessionID, askquestion.AskQuestionRequest{ID: "generic-ask", StepID: gatewayAttentionStepID, Question: "Generic?"})
	pending := nextGatewayAttentionEvent(t, sub)
	if pending.Pending.Target.Kind != clientui.AttentionNotificationTargetSessionPrompt || pending.Pending.Target.SessionID != sessionStore.Metadata().SessionID {
		t.Fatalf("session prompt pending = %+v", pending)
	}
}

type gatewayAttentionDependencies struct {
	*core.Core
	attention apicontract.AttentionNotificationService
}

func (d *gatewayAttentionDependencies) AttentionNotificationClient() apicontract.AttentionNotificationService {
	return d.attention
}

func newGatewayAttentionTestServer(t *testing.T) (*core.Core, *registry.RuntimeRegistry, *httptest.Server) {
	t.Helper()
	appCore, _ := newGatewayTestCore(t, true, true)
	prompts := registry.NewRuntimeRegistry().WithAttentionNotifications(attentionnotify.NewBroker())
	gateway, err := NewGateway(
		&gatewayAttentionDependencies{Core: appCore, attention: prompts},
		protocol.ServerIdentity{ProtocolVersion: protocol.Version, ServerID: "server-1"},
	)
	if err != nil {
		t.Fatalf("NewGateway: %v", err)
	}
	return appCore, prompts, httptest.NewServer(gateway.Handler())
}

func beginGatewayPendingPrompt(t *testing.T, prompts *registry.RuntimeRegistry, sessionID string, request askquestion.AskQuestionRequest) {
	t.Helper()
	prompts.PromptPending(gatewayPromptResource(sessionID), runtimeids.NewExecutionScopeID(), request, time.Now().UTC())
}

func registerGatewayPromptRuntime(t *testing.T, prompts *registry.RuntimeRegistry, sessionID string) {
	t.Helper()
	if err := prompts.ResourceReady(
		context.Background(),
		sessionruntime.AgentResourceDescriptor{Ref: gatewayPromptResource(sessionID), State: sessionruntime.AgentResourceReady},
		&runtime.Engine{},
		func() (io.Closer, error) { return io.NopCloser(strings.NewReader("")), nil },
	); err != nil {
		t.Fatalf("register prompt runtime: %v", err)
	}
}

func gatewayPromptResource(sessionID string) runtimeids.SessionResourceRef {
	id, err := runtimeids.ParseSessionID(sessionID)
	if err != nil {
		panic(err)
	}
	resource, err := runtimeids.NewSessionResourceRef(id, 1)
	if err != nil {
		panic(err)
	}
	return resource
}

func gatewayTaskBatchAskRequest(askID string, projectID string, taskID string, sessionID string) askquestion.AskQuestionRequest {
	return askquestion.AskQuestionRequest{
		ID:       askID,
		StepID:   gatewayAttentionStepID,
		Question: "Task question?",
		QuestionBatch: &askquestion.AskQuestionBatchMetadata{
			Origin:              askquestion.AskQuestionOriginModelTool,
			RunID:               "run-" + taskID,
			StepID:              gatewayAttentionStepID,
			BatchID:             "batch-" + taskID,
			PromptID:            askID,
			BatchPromptIDs:      []string{askID},
			CandidateOrdinal:    0,
			PreparedPromptCount: 1,
		},
		AttentionTarget: &clientui.AttentionNotificationTarget{
			Kind:      clientui.AttentionNotificationTargetWorkflowTask,
			ProjectID: projectID,
			TaskID:    taskID,
			SessionID: sessionID,
			RunID:     "run-" + taskID,
			Focus: &clientui.AttentionNotificationTaskDetailFocus{
				Kind:   clientui.AttentionNotificationFocusQuestion,
				AskIDs: []string{askID},
			},
		},
	}
}

const gatewayAttentionStepID = "11111111-1111-4111-8111-111111111111"

func nextGatewayAttentionEvent(t *testing.T, sub serverapi.AttentionNotificationSubscription) clientui.AttentionNotificationEvent {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	event, err := sub.Next(ctx)
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	return event
}

func shortGatewayAttentionContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	t.Cleanup(cancel)
	return ctx
}
