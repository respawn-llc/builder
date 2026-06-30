package transport

import (
	"context"
	"testing"
	"time"

	askquestion "core/server/tools"
	remoteclient "core/shared/client"
	"core/shared/clientui"
	"core/shared/protocol"
	"core/shared/serverapi"
)

func TestGatewayRemoteAttentionDesktopRouteIsRootGlobalAndLiveOnly(t *testing.T) {
	appCore, server := newGatewayTestServer(t)
	defer func() { _ = appCore.Close() }()
	defer server.Close()

	sessionOne := createGatewayAuthoritativeSession(t, appCore)
	appCore.RegisterSessionStore(sessionOne)
	activateGatewayController(t, appCore, sessionOne.Meta().SessionID)
	sessionTwo := createGatewayAuthoritativeSession(t, appCore)
	appCore.RegisterSessionStore(sessionTwo)
	activateGatewayController(t, appCore, sessionTwo.Meta().SessionID)

	appCore.BeginPendingPrompt(sessionOne.Meta().SessionID, gatewayTaskBatchAskRequest("old-ask", "project-old", "task-old", sessionOne.Meta().SessionID))
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

	appCore.BeginPendingPrompt(sessionOne.Meta().SessionID, gatewayTaskBatchAskRequest("ask-a", "project-a", "task-a", sessionOne.Meta().SessionID))
	appCore.BeginPendingPrompt(sessionTwo.Meta().SessionID, gatewayTaskBatchAskRequest("ask-b", "project-b", "task-b", sessionTwo.Meta().SessionID))
	first := nextGatewayAttentionEvent(t, desktop)
	second := nextGatewayAttentionEvent(t, desktop)
	if first.Pending.Target.ProjectID != "project-a" || second.Pending.Target.ProjectID != "project-b" {
		t.Fatalf("desktop cross-project events = %+v then %+v", first, second)
	}

	appCore.BeginPendingPrompt(sessionOne.Meta().SessionID, askquestion.AskQuestionRequest{ID: "generic-ask", Question: "Generic?"})
	if event, err := desktop.Next(shortGatewayAttentionContext(t)); err == nil {
		t.Fatalf("desktop received generic session prompt: %+v", event)
	}
}

func TestGatewaySessionAttentionRouteRequiresAttachedSession(t *testing.T) {
	appCore, server := newGatewayTestServer(t)
	defer func() { _ = appCore.Close() }()
	defer server.Close()
	sessionOne := createGatewayAuthoritativeSession(t, appCore)
	appCore.RegisterSessionStore(sessionOne)
	activateGatewayController(t, appCore, sessionOne.Meta().SessionID)
	sessionTwo := createGatewayAuthoritativeSession(t, appCore)
	appCore.RegisterSessionStore(sessionTwo)
	activateGatewayController(t, appCore, sessionTwo.Meta().SessionID)

	conn := dialGateway(t, server)
	defer func() { _ = conn.Close() }()
	handshakeGateway(t, conn)
	errResp := callGatewayExpectError(t, conn, "attention-without-attach", protocol.MethodAttentionSessionNotificationSubscribe, serverapi.AttentionSessionNotificationSubscribeRequest{SessionID: sessionOne.Meta().SessionID})
	if errResp.Code != protocol.ErrCodeInvalidRequest {
		t.Fatalf("missing attach error = %+v", errResp)
	}
	_ = conn.Close()

	conn = dialGateway(t, server)
	defer func() { _ = conn.Close() }()
	handshakeGateway(t, conn)
	callGateway(t, conn, "attach-session-one", protocol.MethodAttachSession, protocol.AttachSessionRequest{SessionID: sessionOne.Meta().SessionID}, nil)
	errResp = callGatewayExpectError(t, conn, "attention-wrong-session", protocol.MethodAttentionSessionNotificationSubscribe, serverapi.AttentionSessionNotificationSubscribeRequest{SessionID: sessionTwo.Meta().SessionID})
	if errResp.Code != protocol.ErrCodeInvalidRequest {
		t.Fatalf("wrong attach error = %+v", errResp)
	}
}

func TestGatewayRemoteSessionAttentionReceivesAuthorizedGenericPrompt(t *testing.T) {
	appCore, server := newGatewayTestServer(t)
	defer func() { _ = appCore.Close() }()
	defer server.Close()
	sessionStore := createGatewayAuthoritativeSession(t, appCore)
	appCore.RegisterSessionStore(sessionStore)
	activateGatewayController(t, appCore, sessionStore.Meta().SessionID)

	remote, err := remoteclient.DialRemoteURL(context.Background(), "ws"+server.URL[len("http"):])
	if err != nil {
		t.Fatalf("DialRemoteURL: %v", err)
	}
	defer func() { _ = remote.Close() }()
	sub, err := remote.SubscribeSessionAttentionNotifications(context.Background(), serverapi.AttentionSessionNotificationSubscribeRequest{SessionID: sessionStore.Meta().SessionID})
	if err != nil {
		t.Fatalf("SubscribeSessionAttentionNotifications: %v", err)
	}
	appCore.BeginPendingPrompt(sessionStore.Meta().SessionID, askquestion.AskQuestionRequest{ID: "generic-ask", Question: "Generic?"})
	pending := nextGatewayAttentionEvent(t, sub)
	if pending.Pending.Target.Kind != clientui.AttentionNotificationTargetSessionPrompt || pending.Pending.Target.SessionID != sessionStore.Meta().SessionID {
		t.Fatalf("session prompt pending = %+v", pending)
	}
}

func gatewayTaskBatchAskRequest(askID string, projectID string, taskID string, sessionID string) askquestion.AskQuestionRequest {
	return askquestion.AskQuestionRequest{
		ID:       askID,
		Question: "Task question?",
		QuestionBatch: &askquestion.AskQuestionBatchMetadata{
			Origin:              askquestion.AskQuestionOriginModelTool,
			RunID:               "run-" + taskID,
			StepID:              "step-" + taskID,
			BatchID:             "batch-" + taskID,
			PromptID:            askID,
			BatchPromptIDs:      []string{askID},
			CandidateOrdinal:    0,
			PreparedPromptCount: 1,
		},
		AttentionTarget: &clientui.AttentionNotificationTarget{
			Kind:      clientui.AttentionNotificationTargetTaskDetail,
			ProjectID: projectID,
			TaskID:    taskID,
			SessionID: sessionID,
			RunID:     "run-" + taskID,
			Focus: &clientui.AttentionNotificationTaskDetailFocus{
				Kind:   clientui.AttentionNotificationFocusQuestion,
				AskIDs: []string{askID},
			},
		},
		AttentionPresentation: &clientui.AttentionNotificationPresentation{
			Title: "Task question",
			Body:  "Task question?",
			Count: 1,
		},
	}
}

func nextGatewayAttentionEvent(t *testing.T, sub serverapi.AttentionNotificationSubscription) clientui.AttentionNotificationEvent {
	t.Helper()
	event, err := sub.Next(context.Background())
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
