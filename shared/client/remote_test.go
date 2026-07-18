package client

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"

	"core/shared/clientui"
	"core/shared/llmerrors"
	"core/shared/protocol"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/sessioncontract"
	"golang.org/x/net/websocket"
)

type capturedRunPromptService struct {
	request serverapi.RunPromptRequest
}

func (s *capturedRunPromptService) RunPrompt(_ context.Context, req serverapi.RunPromptRequest, _ serverapi.RunPromptProgressSink) (serverapi.RunPromptResponse, error) {
	s.request = req
	return serverapi.RunPromptResponse{SessionID: "session-1", Result: "done"}, nil
}

func TestProtocolErrorReconstructsModelStreamStalled(t *testing.T) {
	err := protocolError(&protocol.ResponseError{Code: protocol.ErrCodeModelStreamStalled, Message: "model generation failed after retries: model stream stalled"})
	if !errors.Is(err, llmerrors.ErrModelStreamStalled) {
		t.Fatalf("reconstructed error = %v, want errors.Is ErrModelStreamStalled", err)
	}
	if llmerrors.UserFacingError(err) == "" {
		t.Fatal("expected reconstructed stall error to map to a user-facing message")
	}
}

func TestDialRemoteWithTransportRejectsBlankSessionID(t *testing.T) {
	if _, err := newRemoteSessionAttachmentIntent(" \t "); !errors.Is(err, errRemoteSessionIDRequired) {
		t.Fatalf("intent error = %v, want required session ID error", err)
	}
}

func newRemoteTestServer(t *testing.T, handle func(*websocket.Conn)) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(websocket.Handler(func(ws *websocket.Conn) {
		defer func() { _ = ws.Close() }()
		handle(ws)
	}))
	t.Cleanup(server.Close)
	return server
}

func acceptRemoteHandshake(t *testing.T, ws *websocket.Conn) protocol.Request {
	t.Helper()
	var req protocol.Request
	if err := websocket.JSON.Receive(ws, &req); err != nil {
		t.Fatalf("receive handshake: %v", err)
	}
	if req.Method != protocol.MethodHandshake {
		t.Fatalf("handshake method = %q", req.Method)
	}
	if err := websocket.JSON.Send(ws, protocol.NewSuccessResponse(req.ID, protocol.HandshakeResponse{Identity: protocol.ServerIdentity{ProtocolVersion: protocol.Version, ServerID: "server-1"}})); err != nil {
		t.Fatalf("send handshake response: %v", err)
	}
	return req
}

func TestRemotePersistInputDraftSendsInertRecoveryBuffers(t *testing.T) {
	server := newRemoteTestServer(t, func(ws *websocket.Conn) {
		acceptRemoteHandshake(t, ws)
		var request protocol.Request
		if err := websocket.JSON.Receive(ws, &request); err != nil {
			if errors.Is(err, io.EOF) {
				return
			}
			t.Fatalf("receive persist input draft: %v", err)
		}
		if request.Method != protocol.MethodSessionPersistInputDraft {
			t.Fatalf("method = %q, want %q", request.Method, protocol.MethodSessionPersistInputDraft)
		}
		var decoded serverapi.SessionPersistInputDraftRequest
		if err := json.Unmarshal(request.Params, &decoded); err != nil {
			t.Fatalf("decode persist input draft: %v", err)
		}
		want := []serverapi.SessionDraftRecoveryBuffer{{
			Kind: serverapi.SessionDraftRecoveryBufferPendingInjectedInput,
			Text: "pending steering",
		}}
		if !reflect.DeepEqual(decoded.RecoveryBuffers, want) {
			t.Fatalf("recovery buffers = %+v, want %+v", decoded.RecoveryBuffers, want)
		}
		if err := websocket.JSON.Send(ws, protocol.NewSuccessResponse(request.ID, serverapi.SessionPersistInputDraftResponse{})); err != nil {
			t.Fatalf("send persist input draft response: %v", err)
		}
	})
	remote, err := DialRemoteURL(context.Background(), "ws"+server.URL[len("http"):])
	if err != nil {
		t.Fatalf("DialRemoteURL: %v", err)
	}
	defer func() { _ = remote.Close() }()
	_, err = remote.PersistInputDraft(context.Background(), serverapi.SessionPersistInputDraftRequest{
		ClientRequestID: "draft-1",
		SessionID:       "session-1",
		Input:           "visible draft",
		RecoveryBuffers: []serverapi.SessionDraftRecoveryBuffer{{
			Kind: serverapi.SessionDraftRecoveryBufferPendingInjectedInput,
			Text: "pending steering",
		}},
	})
	if err != nil {
		t.Fatalf("PersistInputDraft: %v", err)
	}
}

func TestRemoteRunPromptPublishesProgressNotifications(t *testing.T) {
	server := newRemoteTestServer(t, func(ws *websocket.Conn) {
		req := acceptRemoteHandshake(t, ws)
		if err := websocket.JSON.Receive(ws, &req); err != nil {
			if errors.Is(err, io.EOF) {
				return
			}
			t.Fatalf("receive run prompt: %v", err)
		}
		if req.Method != protocol.MethodRunPrompt {
			t.Fatalf("run prompt method = %q", req.Method)
		}
		if err := websocket.JSON.Send(ws, protocol.Request{JSONRPC: protocol.JSONRPCVersion, Method: protocol.MethodRunPromptProgress, Params: mustJSON(t, serverapi.RunPromptProgress{
			Kind: serverapi.RunPromptProgressKindAssistantMessage,
			AssistantMessage: &serverapi.RunPromptVisibleResponse{
				Phase:   clientui.MessagePhaseCommentary,
				Content: "Checking the repository.",
			},
		})}); err != nil {
			t.Fatalf("send progress: %v", err)
		}
		if err := websocket.JSON.Send(ws, protocol.Request{JSONRPC: protocol.JSONRPCVersion, Method: protocol.MethodRunPromptProgress, Params: mustJSON(t, serverapi.RunPromptProgress{Kind: serverapi.RunPromptProgressKindCompactionStarted})}); err != nil {
			t.Fatalf("send progress: %v", err)
		}
		if err := websocket.JSON.Send(ws, protocol.NewSuccessResponse(req.ID, serverapi.RunPromptResponse{SessionID: "session-1", SessionName: "Session 1", Result: "done"})); err != nil {
			t.Fatalf("send response: %v", err)
		}
	})

	remote, err := DialRemoteURL(context.Background(), "ws"+server.URL[len("http"):])
	if err != nil {
		t.Fatalf("DialRemote: %v", err)
	}
	defer func() { _ = remote.Close() }()

	var updates []serverapi.RunPromptProgress
	resp, err := remote.RunPrompt(context.Background(), serverapi.RunPromptRequest{ClientRequestID: "req-1", Intent: serverapi.CreateNewSessionLaunchIntent(serverapi.IndependentSessionCreateOrigin()), Prompt: "hello"}, serverapi.RunPromptProgressFunc(func(progress serverapi.RunPromptProgress) {
		updates = append(updates, progress)
	}))
	if err != nil {
		t.Fatalf("RunPrompt: %v", err)
	}
	if resp.SessionID != "session-1" || resp.Result != "done" {
		t.Fatalf("unexpected run prompt response: %+v", resp)
	}
	if len(updates) != 2 ||
		updates[0].AssistantMessage == nil ||
		updates[0].AssistantMessage.Content != "Checking the repository." ||
		updates[1].Kind != serverapi.RunPromptProgressKindCompactionStarted {
		t.Fatalf("unexpected progress updates: %+v", updates)
	}
}

func TestRemoteRunPromptCarriesTypedParentAgentOriginAndExplicitDefault(t *testing.T) {
	server := newRemoteTestServer(t, func(ws *websocket.Conn) {
		acceptRemoteHandshake(t, ws)
		var req protocol.Request
		if err := websocket.JSON.Receive(ws, &req); err != nil {
			if errors.Is(err, io.EOF) {
				return
			}
			t.Fatalf("receive run prompt: %v", err)
		}
		if req.Method != protocol.MethodRunPrompt {
			t.Fatalf("method = %q, want %q", req.Method, protocol.MethodRunPrompt)
		}
		var decoded serverapi.RunPromptRequest
		if err := json.Unmarshal(req.Params, &decoded); err != nil {
			t.Fatalf("decode run prompt params: %v", err)
		}
		origin, present := decoded.Intent.CreateOrigin()
		sourceID, hasSource := origin.SessionID()
		if decoded.CallerSessionID == nil || *decoded.CallerSessionID != "caller-session" ||
			!present || origin.Kind() != serverapi.SessionCreateOriginParentAgent ||
			!hasSource || sourceID.String() != "parent-session" ||
			decoded.Overrides.AgentRole == nil || *decoded.Overrides.AgentRole != "default" {
			t.Fatalf("decoded request = %+v, want caller/parent-agent origin/default selector", decoded)
		}
		if err := websocket.JSON.Send(ws, protocol.NewSuccessResponse(req.ID, serverapi.RunPromptResponse{SessionID: "session-1", Result: "done"})); err != nil {
			t.Fatalf("send response: %v", err)
		}
	})
	remote, err := DialRemoteURL(context.Background(), "ws"+server.URL[len("http"):])
	if err != nil {
		t.Fatalf("DialRemoteURL: %v", err)
	}
	defer func() { _ = remote.Close() }()
	caller := "caller-session"
	parent := "parent-session"
	role := "default"
	if _, err := remote.RunPrompt(context.Background(), serverapi.RunPromptRequest{
		ClientRequestID: "present",
		Intent:          serverapi.CreateNewSessionLaunchIntent(serverapi.ParentAgentSessionCreateOrigin(mustRemoteSessionID(t, parent))),
		CallerSessionID: &caller,
		Prompt:          "hello",
		Overrides:       serverapi.RunPromptOverrides{AgentRole: &role},
	}, nil); err != nil {
		t.Fatalf("RunPrompt present provenance: %v", err)
	}
}

func TestLoopbackAndRemoteRunPromptPreserveTypedIntentCallerAndSelectors(t *testing.T) {
	caller := "caller-session"
	parent := "parent-session"
	defaultRole := "default"
	worker := "worker"
	cases := []struct {
		name string
		req  serverapi.RunPromptRequest
	}{
		{name: "human independent", req: serverapi.RunPromptRequest{ClientRequestID: "human-omitted", Intent: serverapi.CreateNewSessionLaunchIntent(serverapi.IndependentSessionCreateOrigin()), Prompt: "hello"}},
		{name: "new child omitted selector", req: serverapi.RunPromptRequest{ClientRequestID: "new-omitted", Intent: serverapi.CreateNewSessionLaunchIntent(serverapi.ParentAgentSessionCreateOrigin(mustRemoteSessionID(t, parent))), CallerSessionID: &caller, Prompt: "hello"}},
		{name: "selected explicit default", req: serverapi.RunPromptRequest{ClientRequestID: "selected-default", Intent: serverapi.OpenExistingSessionLaunchIntent(mustRemoteSessionID(t, "selected-session")), CallerSessionID: &caller, Prompt: "hello", Overrides: serverapi.RunPromptOverrides{AgentRole: &defaultRole}}},
		{name: "selected custom", req: serverapi.RunPromptRequest{ClientRequestID: "selected-worker", Intent: serverapi.OpenExistingSessionLaunchIntent(mustRemoteSessionID(t, "selected-session")), CallerSessionID: &caller, Prompt: "hello", Overrides: serverapi.RunPromptOverrides{AgentRole: &worker}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			inProcess := &capturedRunPromptService{}
			if _, err := inProcess.RunPrompt(context.Background(), tc.req, nil); err != nil {
				t.Fatalf("in-process RunPrompt: %v", err)
			}
			assertSameRunPromptWireContract(t, inProcess.request, tc.req)

			server := newRemoteTestServer(t, func(ws *websocket.Conn) {
				acceptRemoteHandshake(t, ws)
				var request protocol.Request
				if err := websocket.JSON.Receive(ws, &request); err != nil {
					if errors.Is(err, io.EOF) {
						return
					}
					t.Fatalf("receive run prompt: %v", err)
				}
				var decoded serverapi.RunPromptRequest
				if err := json.Unmarshal(request.Params, &decoded); err != nil {
					t.Fatalf("decode run prompt: %v", err)
				}
				assertSameRunPromptWireContract(t, decoded, tc.req)
				if err := websocket.JSON.Send(ws, protocol.NewSuccessResponse(request.ID, serverapi.RunPromptResponse{SessionID: "session-1", Result: "done"})); err != nil {
					t.Fatalf("send response: %v", err)
				}
			})
			remote, err := DialRemoteURL(context.Background(), "ws"+server.URL[len("http"):])
			if err != nil {
				t.Fatalf("DialRemoteURL: %v", err)
			}
			defer func() { _ = remote.Close() }()
			if _, err := remote.RunPrompt(context.Background(), tc.req, nil); err != nil {
				t.Fatalf("remote RunPrompt: %v", err)
			}
		})
	}
}

func TestRemoteRunPromptDecodesTypedPolicyDenial(t *testing.T) {
	target := "hidden"
	server := newRemoteTestServer(t, func(ws *websocket.Conn) {
		acceptRemoteHandshake(t, ws)
		var request protocol.Request
		if err := websocket.JSON.Receive(ws, &request); err != nil {
			if errors.Is(err, io.EOF) {
				return
			}
			t.Fatalf("receive run prompt: %v", err)
		}
		data, err := json.Marshal(serverapi.SubagentLaunchDeniedError{
			Kind:   serverapi.SubagentLaunchDenialNotCallable,
			Target: &target,
		})
		if err != nil {
			t.Fatalf("marshal typed denial: %v", err)
		}
		if err := websocket.JSON.Send(ws, protocol.NewErrorResponseWithData(request.ID, protocol.ErrCodeSubagentLaunchDenied, "denied", data)); err != nil {
			t.Fatalf("send typed denial: %v", err)
		}
	})
	remote, err := DialRemoteURL(context.Background(), "ws"+server.URL[len("http"):])
	if err != nil {
		t.Fatalf("DialRemoteURL: %v", err)
	}
	defer func() { _ = remote.Close() }()
	_, err = remote.RunPrompt(context.Background(), serverapi.RunPromptRequest{ClientRequestID: "denied", Intent: serverapi.CreateNewSessionLaunchIntent(serverapi.IndependentSessionCreateOrigin()), Prompt: "hello"}, nil)
	var denied *serverapi.SubagentLaunchDeniedError
	if !errors.As(err, &denied) || denied.Kind != serverapi.SubagentLaunchDenialNotCallable || denied.Target == nil || *denied.Target != target {
		t.Fatalf("RunPrompt error = %T %v, want typed hidden-target denial", err, err)
	}
}

func TestRemoteRunPromptPreservesTypedSubagentDepthPolicyWithoutProgress(t *testing.T) {
	source := protocol.NewMaxDepthExceededSubagentLaunchPolicyError(1, 0)
	server := newRemoteTestServer(t, func(ws *websocket.Conn) {
		acceptRemoteHandshake(t, ws)
		var request protocol.Request
		if err := websocket.JSON.Receive(ws, &request); err != nil {
			if errors.Is(err, io.EOF) {
				return
			}
			t.Fatalf("receive run prompt: %v", err)
		}
		if err := websocket.JSON.Send(ws, protocol.NewErrorResponseWithData(
			request.ID,
			source.RPCErrorCode(),
			source.Error(),
			source.RPCErrorData(),
		)); err != nil {
			t.Fatalf("send typed policy error: %v", err)
		}
	})
	remote, err := DialRemoteURL(context.Background(), "ws"+server.URL[len("http"):])
	if err != nil {
		t.Fatalf("DialRemoteURL: %v", err)
	}
	defer func() { _ = remote.Close() }()

	var progresses []serverapi.RunPromptProgress
	_, err = remote.RunPrompt(
		context.Background(),
		serverapi.RunPromptRequest{
			ClientRequestID: "depth-policy",
			Intent:          serverapi.CreateNewSessionLaunchIntent(serverapi.IndependentSessionCreateOrigin()),
			Prompt:          "delegate",
		},
		serverapi.RunPromptProgressFunc(func(progress serverapi.RunPromptProgress) {
			progresses = append(progresses, progress)
		}),
	)
	var decoded *protocol.SubagentLaunchPolicyError
	if !errors.As(err, &decoded) ||
		decoded.Kind != protocol.SubagentLaunchPolicyMaxDepthExceeded ||
		decoded.AttemptedDepth == nil || *decoded.AttemptedDepth != 1 ||
		decoded.MaxDepth == nil || *decoded.MaxDepth != 0 {
		t.Fatalf("RunPrompt error = %T %+v, want typed maximum-depth policy", err, decoded)
	}
	if len(progresses) != 0 {
		t.Fatalf("rejected remote run published progress: %+v", progresses)
	}
}

func assertSameRunPromptWireContract(t *testing.T, got serverapi.RunPromptRequest, want serverapi.RunPromptRequest) {
	t.Helper()
	if got.ClientRequestID != want.ClientRequestID ||
		!got.Intent.Equal(want.Intent) ||
		got.Prompt != want.Prompt ||
		serverapi.CanonicalOptionalString(got.CallerSessionID) != serverapi.CanonicalOptionalString(want.CallerSessionID) {
		t.Fatalf("provenance request = %+v, want %+v", got, want)
	}
	gotOverrides, err := got.Overrides.CanonicalKey()
	if err != nil {
		t.Fatalf("got overrides canonical key: %v", err)
	}
	wantOverrides, err := want.Overrides.CanonicalKey()
	if err != nil {
		t.Fatalf("want overrides canonical key: %v", err)
	}
	if !reflect.DeepEqual(gotOverrides, wantOverrides) {
		t.Fatalf("selector/overrides = %+v, want %+v", gotOverrides, wantOverrides)
	}
}

func mustRemoteSessionID(t *testing.T, raw string) runtimeids.SessionID {
	t.Helper()
	id, err := runtimeids.ParseSessionID(raw)
	if err != nil {
		t.Fatalf("ParseSessionID(%q): %v", raw, err)
	}
	return id
}

func TestRemoteGetsLatestCommittedAssistantFinalAnswer(t *testing.T) {
	answer := "durable answer"
	server := newRemoteTestServer(t, func(ws *websocket.Conn) {
		req := acceptRemoteHandshake(t, ws)
		if err := websocket.JSON.Receive(ws, &req); err != nil {
			t.Fatalf("receive final-answer request: %v", err)
		}
		if req.Method != protocol.MethodSessionGetLatestCommittedAssistantFinalAnswer {
			t.Fatalf("method = %q, want %q", req.Method, protocol.MethodSessionGetLatestCommittedAssistantFinalAnswer)
		}
		if err := websocket.JSON.Send(ws, protocol.NewSuccessResponse(req.ID, serverapi.SessionLatestCommittedAssistantFinalAnswerResponse{Answer: &answer})); err != nil {
			t.Fatalf("send final-answer response: %v", err)
		}
	})

	remote, err := DialRemoteURL(context.Background(), "ws"+server.URL[len("http"):])
	if err != nil {
		t.Fatalf("DialRemoteURL: %v", err)
	}
	defer func() { _ = remote.Close() }()

	resp, err := remote.GetLatestCommittedAssistantFinalAnswer(context.Background(), serverapi.SessionLatestCommittedAssistantFinalAnswerRequest{SessionID: "session-1"})
	if err != nil {
		t.Fatalf("GetLatestCommittedAssistantFinalAnswer: %v", err)
	}
	if resp.Answer == nil || *resp.Answer != answer {
		t.Fatalf("answer = %v, want %q", resp.Answer, answer)
	}
}

func TestRemoteSessionTranscriptSubscriptionUsesSeparateRouteAndDecodesMessages(t *testing.T) {
	server := newRemoteTestServer(t, func(ws *websocket.Conn) {
		req := acceptRemoteHandshake(t, ws)
		if err := websocket.JSON.Receive(ws, &req); err != nil {
			if errors.Is(err, io.EOF) {
				return
			}
			t.Fatalf("receive attach session: %v", err)
		}
		if req.Method != protocol.MethodAttachSession {
			t.Fatalf("expected attach-session before transcript subscribe, got %q", req.Method)
		}
		if err := websocket.JSON.Send(ws, protocol.NewSuccessResponse(req.ID, testSessionAttachResponse(t, "project-1", "workspace-1", "/workspace", "session-1"))); err != nil {
			t.Fatalf("send attach response: %v", err)
		}
		if err := websocket.JSON.Receive(ws, &req); err != nil {
			t.Fatalf("receive transcript subscribe: %v", err)
		}
		if req.Method != protocol.MethodSessionSubscribeTranscript {
			t.Fatalf("subscribe method = %q, want %q", req.Method, protocol.MethodSessionSubscribeTranscript)
		}
		var params serverapi.TranscriptSubscribeRequest
		if err := json.Unmarshal(req.Params, &params); err != nil {
			t.Fatalf("decode transcript subscribe params: %v", err)
		}
		if params.SessionID != "session-1" {
			t.Fatalf("transcript subscribe params = %+v, want session-1", params)
		}
		if err := websocket.JSON.Send(ws, protocol.NewSuccessResponse(req.ID, protocol.SubscribeResponse{})); err != nil {
			t.Fatalf("send subscribe response: %v", err)
		}
		event := protocol.SessionTranscriptEventParams{Message: clientui.TranscriptMessage{
			Sequence: 2,
			Kind:     clientui.TranscriptMessageOperationalDiagnostic,
			Payload: clientui.TranscriptPayload{OperationalDiagnostic: &clientui.TranscriptOperationalDiagnostic{
				Code:   clientui.OperationalDiagnosticSleepGuardFailed,
				Detail: "sleep prevention failed",
			}},
		}}
		if err := websocket.JSON.Send(ws, protocol.Request{JSONRPC: protocol.JSONRPCVersion, Method: protocol.MethodSessionTranscriptEvent, Params: mustJSON(t, event)}); err != nil {
			t.Fatalf("send transcript event: %v", err)
		}
	})

	remote, err := DialRemoteURL(context.Background(), "ws"+server.URL[len("http"):])
	if err != nil {
		t.Fatalf("DialRemote: %v", err)
	}
	defer func() { _ = remote.Close() }()

	sub, err := remote.SubscribeSessionTranscript(context.Background(), serverapi.TranscriptSubscribeRequest{SessionID: "session-1"})
	if err != nil {
		t.Fatalf("SubscribeSessionTranscript: %v", err)
	}
	defer func() { _ = sub.Close() }()

	message, err := sub.Next(context.Background())
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if message.Sequence != 2 || message.Kind != clientui.TranscriptMessageOperationalDiagnostic || message.Payload.OperationalDiagnostic == nil {
		t.Fatalf("transcript message = %+v, want seq=2 operational diagnostic", message)
	}
}

func TestRemoteSessionTranscriptSubscriptionPreservesTypedCloseReason(t *testing.T) {
	server := newRemoteTestServer(t, func(ws *websocket.Conn) {
		req := acceptRemoteHandshake(t, ws)
		if err := websocket.JSON.Receive(ws, &req); err != nil {
			if errors.Is(err, io.EOF) {
				return
			}
			t.Fatalf("receive attach session: %v", err)
		}
		if err := websocket.JSON.Send(ws, protocol.NewSuccessResponse(req.ID, testSessionAttachResponse(t, "project-1", "workspace-1", "/workspace", "session-1"))); err != nil {
			t.Fatalf("send attach response: %v", err)
		}
		if err := websocket.JSON.Receive(ws, &req); err != nil {
			t.Fatalf("receive transcript subscribe: %v", err)
		}
		if err := websocket.JSON.Send(ws, protocol.NewSuccessResponse(req.ID, protocol.SubscribeResponse{})); err != nil {
			t.Fatalf("send subscribe response: %v", err)
		}
		complete := protocol.StreamCompleteParams{
			Code:                  protocol.ErrCodeStreamGap,
			Message:               serverapi.ErrStreamGap.Error(),
			TranscriptCloseReason: string(serverapi.TranscriptCloseReasonSubscriberOverflow),
		}
		if err := websocket.JSON.Send(ws, protocol.Request{JSONRPC: protocol.JSONRPCVersion, Method: protocol.MethodSessionTranscriptComplete, Params: mustJSON(t, complete)}); err != nil {
			t.Fatalf("send transcript complete: %v", err)
		}
	})

	remote, err := DialRemoteURL(context.Background(), "ws"+server.URL[len("http"):])
	if err != nil {
		t.Fatalf("DialRemote: %v", err)
	}
	defer func() { _ = remote.Close() }()

	sub, err := remote.SubscribeSessionTranscript(context.Background(), serverapi.TranscriptSubscribeRequest{SessionID: "session-1"})
	if err != nil {
		t.Fatalf("SubscribeSessionTranscript: %v", err)
	}
	defer func() { _ = sub.Close() }()

	_, err = sub.Next(context.Background())
	if !errors.Is(err, serverapi.ErrStreamGap) {
		t.Fatalf("Next error = %v, want stream gap", err)
	}
	reason, ok := serverapi.TranscriptCloseReasonOf(err)
	if !ok || reason != serverapi.TranscriptCloseReasonSubscriberOverflow {
		t.Fatalf("transcript close reason = %q ok=%t, want subscriber overflow", reason, ok)
	}
}

func TestRemoteDeleteWorktreeCarriesTypedCleanupPolicyAndResult(t *testing.T) {
	operationID := serverapi.NewWorktreeOperationID()
	server := newRemoteTestServer(t, func(ws *websocket.Conn) {
		req := acceptRemoteHandshake(t, ws)
		if err := websocket.JSON.Receive(ws, &req); err != nil {
			t.Fatalf("receive worktree delete: %v", err)
		}
		if req.Method != protocol.MethodWorktreeDelete {
			t.Fatalf("method = %q, want %q", req.Method, protocol.MethodWorktreeDelete)
		}
		var params serverapi.WorktreeDeleteRequest
		if err := json.Unmarshal(req.Params, &params); err != nil {
			t.Fatalf("unmarshal delete params: %v", err)
		}
		if params.OperationID != operationID ||
			params.Selector != "wt-1" ||
			params.BranchCleanupPolicy != serverapi.WorktreeBranchCleanupModeDeleteSafe {
			t.Fatalf("unexpected delete params: %+v", params)
		}
		branchName := "feature-a"
		if err := websocket.JSON.Send(ws, protocol.NewSuccessResponse(req.ID, serverapi.WorktreeDeleteResult{
			Kind: serverapi.WorktreeDeleteResultKindCompleted,
			Completed: &serverapi.WorktreeDeleteCompletedResult{
				Cleanup: serverapi.WorktreeBranchCleanupOutcome{
					Kind:       serverapi.WorktreeBranchCleanupOutcomeDeleted,
					BranchName: &branchName,
				},
			},
		})); err != nil {
			t.Fatalf("send delete response: %v", err)
		}
	})

	remote, err := DialRemoteURL(context.Background(), "ws"+server.URL[len("http"):])
	if err != nil {
		t.Fatalf("DialRemote: %v", err)
	}
	defer func() { _ = remote.Close() }()

	resp, err := remote.DeleteWorktree(context.Background(), serverapi.WorktreeDeleteRequest{
		OperationID:         operationID,
		SessionID:           "session-1",
		Selector:            "wt-1",
		BranchCleanupPolicy: serverapi.WorktreeBranchCleanupModeDeleteSafe,
	})
	if err != nil {
		t.Fatalf("DeleteWorktree: %v", err)
	}
	if resp.Completed == nil || resp.Completed.Cleanup.Kind != serverapi.WorktreeBranchCleanupOutcomeDeleted {
		t.Fatalf("unexpected delete response: %+v", resp)
	}
}

func TestRemoteResolveWorktreeCreateTargetCarriesMethodAndPayload(t *testing.T) {
	server := newRemoteTestServer(t, func(ws *websocket.Conn) {
		req := acceptRemoteHandshake(t, ws)
		if err := websocket.JSON.Receive(ws, &req); err != nil {
			t.Fatalf("receive worktree resolve: %v", err)
		}
		if req.Method != protocol.MethodWorktreeCreateTargetResolve {
			t.Fatalf("method = %q, want %q", req.Method, protocol.MethodWorktreeCreateTargetResolve)
		}
		var params serverapi.WorktreeCreateTargetResolveRequest
		if err := json.Unmarshal(req.Params, &params); err != nil {
			t.Fatalf("unmarshal resolve params: %v", err)
		}
		if params.SessionID != "session-1" || params.Target != "HEAD~1" {
			t.Fatalf("unexpected resolve params: %+v", params)
		}
		if err := websocket.JSON.Send(ws, protocol.NewSuccessResponse(req.ID, serverapi.WorktreeCreateTargetResolveResponse{
			Resolution: serverapi.WorktreeCreateTargetResolution{Input: "HEAD~1", Kind: serverapi.WorktreeCreateTargetResolutionKindDetachedRef, ResolvedRef: "abc123"},
		})); err != nil {
			t.Fatalf("send resolve response: %v", err)
		}
	})

	remote, err := DialRemoteURL(context.Background(), "ws"+server.URL[len("http"):])
	if err != nil {
		t.Fatalf("DialRemote: %v", err)
	}
	defer func() { _ = remote.Close() }()

	resp, err := remote.ResolveWorktreeCreateTarget(context.Background(), serverapi.WorktreeCreateTargetResolveRequest{SessionID: "session-1", Target: "HEAD~1"})
	if err != nil {
		t.Fatalf("ResolveWorktreeCreateTarget: %v", err)
	}
	if resp.Resolution.Kind != serverapi.WorktreeCreateTargetResolutionKindDetachedRef || resp.Resolution.ResolvedRef != "abc123" {
		t.Fatalf("unexpected resolve response: %+v", resp)
	}
}

func TestRemoteProcessOutputSubscriptionAttachesProjectBeforeSubscribe(t *testing.T) {
	server := newRemoteTestServer(t, func(ws *websocket.Conn) {
		var req protocol.Request
		if err := websocket.JSON.Receive(ws, &req); err != nil {
			return
		}
		if err := websocket.JSON.Send(ws, protocol.NewSuccessResponse(req.ID, protocol.HandshakeResponse{Identity: protocol.ServerIdentity{ProtocolVersion: protocol.Version, ServerID: "server-1"}})); err != nil {
			return
		}
		if err := websocket.JSON.Receive(ws, &req); err != nil {
			return
		}
		if req.Method != protocol.MethodAttachProject {
			t.Fatalf("expected attach-project before process output subscribe, got %q", req.Method)
		}
		var attach protocol.AttachProjectRequest
		if err := json.Unmarshal(req.Params, &attach); err != nil {
			t.Fatalf("decode attach-project: %v", err)
		}
		if attach.ProjectID != "project-1" {
			t.Fatalf("attach project id = %q, want project-1", attach.ProjectID)
		}
		if err := websocket.JSON.Send(ws, protocol.NewSuccessResponse(req.ID, testProjectAttachResponse(t, attach.ProjectID, "workspace-1", "/workspace"))); err != nil {
			return
		}
		if err := websocket.JSON.Receive(ws, &req); err != nil {
			return
		}
		if req.Method != protocol.MethodProcessSubscribeOutput {
			t.Fatalf("expected process output subscribe after attach-project, got %q", req.Method)
		}
		if err := websocket.JSON.Send(ws, protocol.NewSuccessResponse(req.ID, protocol.SubscribeResponse{})); err != nil {
			return
		}
		_ = websocket.JSON.Send(ws, protocol.Request{JSONRPC: protocol.JSONRPCVersion, Method: protocol.MethodProcessOutputComplete, Params: mustJSON(t, protocol.StreamCompleteParams{})})
	})

	remote, err := DialRemoteURLForProject(context.Background(), "ws"+server.URL[len("http"):], "project-1")
	if err != nil {
		t.Fatalf("DialRemoteURLForProject: %v", err)
	}
	defer func() { _ = remote.Close() }()

	sub, err := remote.SubscribeProcessOutput(context.Background(), serverapi.ProcessOutputSubscribeRequest{ProcessID: "proc-1"})
	if err != nil {
		t.Fatalf("SubscribeProcessOutput: %v", err)
	}
	defer func() { _ = sub.Close() }()
}

func TestDialRemoteURLForProjectAttachesProjectAndReturnsRemote(t *testing.T) {
	server := newRemoteTestServer(t, func(ws *websocket.Conn) {
		var req protocol.Request
		if err := websocket.JSON.Receive(ws, &req); err != nil {
			return
		}
		if err := websocket.JSON.Send(ws, protocol.NewSuccessResponse(req.ID, protocol.HandshakeResponse{Identity: protocol.ServerIdentity{ProtocolVersion: protocol.Version, ServerID: "server-1"}})); err != nil {
			return
		}
		if err := websocket.JSON.Receive(ws, &req); err != nil {
			return
		}
		if req.Method != protocol.MethodAttachProject {
			t.Fatalf("expected attach-project during dial, got %q", req.Method)
		}
		var attach protocol.AttachProjectRequest
		if err := json.Unmarshal(req.Params, &attach); err != nil {
			t.Fatalf("decode attach-project: %v", err)
		}
		if attach.ProjectID != "project-1" {
			t.Fatalf("attach project id = %q, want project-1", attach.ProjectID)
		}
		_ = websocket.JSON.Send(ws, protocol.NewSuccessResponse(req.ID, testProjectAttachResponse(t, attach.ProjectID, "workspace-1", "/server/workspace")))
	})

	remote, err := DialRemoteURLForProject(context.Background(), "ws"+server.URL[len("http"):], "project-1")
	if err != nil {
		t.Fatalf("DialRemoteURLForProject: %v", err)
	}
	defer func() { _ = remote.Close() }()
	if got := remote.ProjectID(); got != "project-1" {
		t.Fatalf("ProjectID = %q, want project-1", got)
	}
	if got := remote.WorkspaceID(); got != "workspace-1" {
		t.Fatalf("WorkspaceID = %q, want workspace-1", got)
	}
	if got := remote.WorkspaceRoot(); got != "/server/workspace" {
		t.Fatalf("WorkspaceRoot = %q, want /server/workspace", got)
	}
	if got := remote.Identity().ServerID; got != "server-1" {
		t.Fatalf("server id = %q, want server-1", got)
	}
}

func TestDialRemoteURLForSessionAttachesSessionBeforeUnaryCalls(t *testing.T) {
	server := newRemoteTestServer(t, func(ws *websocket.Conn) {
		req := acceptRemoteHandshake(t, ws)
		if err := websocket.JSON.Receive(ws, &req); err != nil {
			t.Fatalf("receive attach session: %v", err)
		}
		if req.Method != protocol.MethodAttachSession {
			t.Fatalf("expected attach-session during dial, got %q", req.Method)
		}
		var attach protocol.AttachSessionRequest
		if err := json.Unmarshal(req.Params, &attach); err != nil {
			t.Fatalf("decode attach-session: %v", err)
		}
		if attach.SessionID != "session-1" {
			t.Fatalf("attach session id = %q, want session-1", attach.SessionID)
		}
		if err := websocket.JSON.Send(ws, protocol.NewSuccessResponse(req.ID, testSessionAttachResponse(t, "project-1", "workspace-1", "/workspace", attach.SessionID))); err != nil {
			t.Fatalf("send attach response: %v", err)
		}
		if err := websocket.JSON.Receive(ws, &req); err != nil {
			t.Fatalf("receive worktree status: %v", err)
		}
		if req.Method != protocol.MethodWorktreeStatus {
			t.Fatalf("method = %q, want %q", req.Method, protocol.MethodWorktreeStatus)
		}
		var statusRequest serverapi.WorktreeStatusRequest
		if err := json.Unmarshal(req.Params, &statusRequest); err != nil {
			t.Fatalf("decode worktree status: %v", err)
		}
		if statusRequest.SessionID != attach.SessionID {
			t.Fatalf("status session id = %q, want %q", statusRequest.SessionID, attach.SessionID)
		}
		if err := websocket.JSON.Send(ws, protocol.NewSuccessResponse(req.ID, serverapi.WorktreeStatusResponse{})); err != nil {
			t.Fatalf("send worktree status response: %v", err)
		}
	})

	remote, err := DialRemoteURLForSession(
		context.Background(),
		"ws"+server.URL[len("http"):],
		"session-1",
	)
	if err != nil {
		t.Fatalf("DialRemoteURLForSession: %v", err)
	}
	defer func() { _ = remote.Close() }()

	if _, err := remote.GetWorktreeStatus(
		context.Background(),
		serverapi.WorktreeStatusRequest{SessionID: "session-1"},
	); err != nil {
		t.Fatalf("GetWorktreeStatus: %v", err)
	}
}

func TestDialRemoteURLForProjectValidatesAttachProject(t *testing.T) {
	server := newRemoteTestServer(t, func(ws *websocket.Conn) {
		var req protocol.Request
		if err := websocket.JSON.Receive(ws, &req); err != nil {
			return
		}
		if err := websocket.JSON.Send(ws, protocol.NewSuccessResponse(req.ID, protocol.HandshakeResponse{Identity: protocol.ServerIdentity{ProtocolVersion: protocol.Version, ServerID: "server-1"}})); err != nil {
			return
		}
		if err := websocket.JSON.Receive(ws, &req); err != nil {
			return
		}
		if req.Method != protocol.MethodAttachProject {
			t.Fatalf("expected attach-project during dial, got %q", req.Method)
		}
		_ = websocket.JSON.Send(ws, protocol.NewErrorResponse(req.ID, protocol.ErrCodeInvalidParams, "project not available"))
	})

	remote, err := DialRemoteURLForProject(context.Background(), "ws"+server.URL[len("http"):], "project-missing")
	if err == nil {
		if remote != nil {
			_ = remote.Close()
		}
		t.Fatal("expected dial to fail when project attach is rejected")
	}
	if remote != nil {
		t.Fatalf("expected nil remote on attach failure, got %v", remote)
	}
}

func TestDialRemoteURLForProjectRejectsMalformedAttachmentResponse(t *testing.T) {
	server := newRemoteTestServer(t, func(ws *websocket.Conn) {
		req := acceptRemoteHandshake(t, ws)
		if err := websocket.JSON.Receive(ws, &req); err != nil {
			return
		}
		if req.Method != protocol.MethodAttachProject {
			t.Fatalf("method = %q, want attach project", req.Method)
		}
		if err := websocket.JSON.Send(ws, protocol.NewSuccessResponse(req.ID, map[string]any{
			"kind":           "project",
			"project_id":     "project-1",
			"workspace_id":   "workspace-1",
			"workspace_root": "",
		})); err != nil {
			t.Fatalf("send malformed attach response: %v", err)
		}
	})

	remote, err := DialRemoteURLForProject(context.Background(), "ws"+server.URL[len("http"):], "project-1")
	if err == nil {
		if remote != nil {
			_ = remote.Close()
		}
		t.Fatal("expected malformed attachment response error")
	}
}

func TestDialRemoteURLForProjectRejectsMismatchedAttachmentResponse(t *testing.T) {
	server := newRemoteTestServer(t, func(ws *websocket.Conn) {
		req := acceptRemoteHandshake(t, ws)
		if err := websocket.JSON.Receive(ws, &req); err != nil {
			return
		}
		if req.Method != protocol.MethodAttachProject {
			t.Fatalf("method = %q, want attach project", req.Method)
		}
		if err := websocket.JSON.Send(ws, protocol.NewSuccessResponse(req.ID, testProjectAttachResponse(t, "other-project", "workspace-1", "/workspace"))); err != nil {
			t.Fatalf("send mismatched attach response: %v", err)
		}
	})

	remote, err := DialRemoteURLForProject(context.Background(), "ws"+server.URL[len("http"):], "project-1")
	if err == nil {
		if remote != nil {
			_ = remote.Close()
		}
		t.Fatal("expected mismatched attachment response error")
	}
}

func TestDialRemoteURLForProjectWorkspaceRejectsDifferentRootAttachment(t *testing.T) {
	server := newRemoteTestServer(t, func(ws *websocket.Conn) {
		req := acceptRemoteHandshake(t, ws)
		if err := websocket.JSON.Receive(ws, &req); err != nil {
			return
		}
		var attach protocol.AttachProjectRequest
		if err := json.Unmarshal(req.Params, &attach); err != nil {
			t.Fatalf("decode attach-project: %v", err)
		}
		wrongRequest, err := protocol.AttachProjectRequestForWorkspaceRoot("project-1", "/workspace-b")
		if err != nil {
			t.Fatalf("wrong attach request: %v", err)
		}
		response, err := protocol.ProjectAttachResponseForRequest(wrongRequest, "workspace-b", "/workspace-b")
		if err != nil {
			t.Fatalf("wrong attach response: %v", err)
		}
		if err := websocket.JSON.Send(ws, protocol.NewSuccessResponse(req.ID, response)); err != nil {
			t.Fatalf("send mismatched attach response: %v", err)
		}
	})

	remote, err := DialRemoteURLForProjectWorkspace(
		context.Background(),
		"ws"+server.URL[len("http"):],
		"project-1",
		"/workspace-a",
	)
	if err == nil {
		if remote != nil {
			_ = remote.Close()
		}
		t.Fatal("expected mismatched workspace root response error")
	}
}

func TestDialRemoteURLForProjectWorkspaceAcceptsServerCanonicalRoot(t *testing.T) {
	server := newRemoteTestServer(t, func(ws *websocket.Conn) {
		req := acceptRemoteHandshake(t, ws)
		if err := websocket.JSON.Receive(ws, &req); err != nil {
			return
		}
		var attach protocol.AttachProjectRequest
		if err := json.Unmarshal(req.Params, &attach); err != nil {
			t.Fatalf("decode attach-project: %v", err)
		}
		response, err := protocol.ProjectAttachResponseForRequest(attach, "workspace-1", "/canonical/workspace")
		if err != nil {
			t.Fatalf("attach response: %v", err)
		}
		if err := websocket.JSON.Send(ws, protocol.NewSuccessResponse(req.ID, response)); err != nil {
			t.Fatalf("send attach response: %v", err)
		}
	})

	remote, err := DialRemoteURLForProjectWorkspace(
		context.Background(),
		"ws"+server.URL[len("http"):],
		"project-1",
		"/workspace-alias",
	)
	if err != nil {
		t.Fatalf("DialRemoteURLForProjectWorkspace: %v", err)
	}
	defer func() { _ = remote.Close() }()
	if got := remote.WorkspaceRoot(); got != "/canonical/workspace" {
		t.Fatalf("WorkspaceRoot = %q, want canonical root", got)
	}
}

func TestRemoteProjectViewCallsReuseInitialProjectAttach(t *testing.T) {
	var attachCount atomic.Int32
	server := newRemoteTestServer(t, func(ws *websocket.Conn) {
		req := acceptRemoteHandshake(t, ws)
		for {
			if err := websocket.JSON.Receive(ws, &req); err != nil {
				if errors.Is(err, io.EOF) {
					return
				}
				t.Fatalf("receive project view request: %v", err)
			}
			switch req.Method {
			case protocol.MethodAttachProject:
				attachCount.Add(1)
				var attach protocol.AttachProjectRequest
				if err := json.Unmarshal(req.Params, &attach); err != nil {
					t.Fatalf("decode attach-project: %v", err)
				}
				response, err := protocol.ProjectAttachResponseForRequest(attach, "workspace-1", "/tmp/attached")
				if err != nil {
					t.Fatalf("attach response: %v", err)
				}
				_ = websocket.JSON.Send(ws, protocol.NewSuccessResponse(req.ID, response))
			case protocol.MethodProjectResolvePath:
				_ = websocket.JSON.Send(ws, protocol.NewSuccessResponse(req.ID, serverapi.ProjectResolvePathResponse{CanonicalRoot: "/tmp/workspace-a"}))
			case protocol.MethodProjectPlanWorkspaceBinding:
				_ = websocket.JSON.Send(ws, protocol.NewSuccessResponse(req.ID, serverapi.ProjectBindingPlanResponse{Kind: serverapi.ProjectBindingPlanKindBound, Binding: &serverapi.ProjectBinding{ProjectID: "project-1", WorkspaceID: "workspace-1"}}))
			case protocol.MethodProjectCreate:
				_ = websocket.JSON.Send(ws, protocol.NewSuccessResponse(req.ID, serverapi.ProjectCreateResponse{Binding: serverapi.ProjectBinding{ProjectID: "project-1"}}))
			case protocol.MethodProjectAttachWorkspace:
				_ = websocket.JSON.Send(ws, protocol.NewSuccessResponse(req.ID, serverapi.ProjectAttachWorkspaceResponse{Binding: serverapi.ProjectBinding{ProjectID: "project-1"}}))
			case protocol.MethodProjectRebindWorkspace:
				_ = websocket.JSON.Send(ws, protocol.NewSuccessResponse(req.ID, serverapi.ProjectRebindWorkspaceResponse{Binding: serverapi.ProjectBinding{ProjectID: "project-1", WorkspaceID: "workspace-1"}}))
			case protocol.MethodProjectGetOverview:
				_ = websocket.JSON.Send(ws, protocol.NewSuccessResponse(req.ID, serverapi.ProjectGetOverviewResponse{}))
			case protocol.MethodSessionPage:
				_ = websocket.JSON.Send(ws, protocol.NewSuccessResponse(req.ID, serverapi.SessionPageResponse{ProjectID: "project-1", Category: sessioncontract.SessionCategoryMain}))
			case protocol.MethodProjectList:
				_ = websocket.JSON.Send(ws, protocol.NewSuccessResponse(req.ID, serverapi.ProjectListResponse{}))
			default:
				t.Fatalf("unexpected project view method %q", req.Method)
			}
		}
	})

	remote, err := DialRemoteURLForProjectWorkspace(context.Background(), "ws"+server.URL[len("http"):], "project-1", "/tmp/attached")
	if err != nil {
		t.Fatalf("DialRemoteURLForProjectWorkspace: %v", err)
	}
	defer func() { _ = remote.Close() }()
	if _, err := remote.ResolveProjectPath(context.Background(), serverapi.ProjectResolvePathRequest{Path: "/tmp/workspace-a"}); err != nil {
		t.Fatalf("ResolveProjectPath: %v", err)
	}
	if _, err := remote.PlanWorkspaceBinding(context.Background(), serverapi.ProjectBindingPlanRequest{Path: "/tmp/workspace-a", Mode: serverapi.ProjectBindingPlanModeInteractive}); err != nil {
		t.Fatalf("PlanWorkspaceBinding: %v", err)
	}
	if _, err := remote.CreateProject(context.Background(), serverapi.ProjectCreateRequest{DisplayName: "demo", WorkspaceRoot: "/tmp/workspace-a"}); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if _, err := remote.AttachWorkspaceToProject(context.Background(), serverapi.ProjectAttachWorkspaceRequest{ProjectID: "project-1", WorkspaceRoot: "/tmp/workspace-b"}); err != nil {
		t.Fatalf("AttachWorkspaceToProject: %v", err)
	}
	if _, err := remote.RebindWorkspace(context.Background(), serverapi.ProjectRebindWorkspaceRequest{OldWorkspaceRoot: "/tmp/workspace-a", NewWorkspaceRoot: "/tmp/workspace-b"}); err != nil {
		t.Fatalf("RebindWorkspace: %v", err)
	}
	if _, err := remote.GetProjectOverview(context.Background(), serverapi.ProjectGetOverviewRequest{ProjectID: "project-1"}); err != nil {
		t.Fatalf("GetProjectOverview: %v", err)
	}
	if _, err := remote.ListSessionPage(context.Background(), serverapi.SessionPageRequest{
		ProjectID: "project-1",
		Category:  sessioncontract.SessionCategoryMain,
		PageSize:  20,
		Position:  serverapi.NewestSessionPagePosition(),
	}); err != nil {
		t.Fatalf("ListSessionPage: %v", err)
	}
	if _, err := remote.ListProjects(context.Background(), serverapi.ProjectListRequest{}); err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if got := attachCount.Load(); got != 1 {
		t.Fatalf("attachCount = %d, want 1", got)
	}
}

func TestProtocolErrorMapsSentinelCodes(t *testing.T) {
	for _, tc := range []struct {
		name string
		code int
		want error
	}{
		{name: "prompt not found", code: protocol.ErrCodePromptNotFound, want: serverapi.ErrPromptNotFound},
		{name: "prompt resolved", code: protocol.ErrCodePromptResolved, want: serverapi.ErrPromptAlreadyResolved},
		{name: "prompt unsupported", code: protocol.ErrCodePromptUnsupported, want: serverapi.ErrPromptUnsupported},
		{name: "method not found", code: protocol.ErrCodeMethodNotFound, want: serverapi.ErrMethodNotFound},
		{name: "workflow task not found", code: protocol.ErrCodeWorkflowTaskNotFound, want: serverapi.ErrWorkflowTaskNotFound},
		{name: "workflow completion ambiguous", code: protocol.ErrCodeWorkflowTaskCompleteAmbiguous, want: serverapi.ErrWorkflowTaskCompleteSelectorAmbiguous},
		{name: "workflow completion target missing", code: protocol.ErrCodeWorkflowTaskCompleteNotFound, want: serverapi.ErrWorkflowTaskCompleteTargetNotFound},
		{name: "auth required", code: protocol.ErrCodeAuthRequired, want: serverapi.ErrServerAuthRequired},
		{name: "runtime unavailable", code: protocol.ErrCodeRuntimeUnavailable, want: serverapi.ErrRuntimeUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := protocolError(&protocol.ResponseError{Code: tc.code, Message: tc.name}); !errors.Is(err, tc.want) {
				t.Fatalf("protocol error = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestProtocolErrorDecodesWorkflowTaskListScopeError(t *testing.T) {
	projectID := "project-1"
	workflowID := "workflow-7e8d24d2-8a98-4dcf-a197-6214db1cb3c0"
	source := &serverapi.WorkflowTaskListScopeError{
		Reason:     serverapi.WorkflowTaskListScopeReasonWorkflowNotLinked,
		ProjectID:  &projectID,
		WorkflowID: &workflowID,
	}
	err := protocolError(&protocol.ResponseError{
		Code:    protocol.ErrCodeWorkflowTaskListScope,
		Message: "scope resolution failed",
		Data:    source.RPCErrorData(),
	})
	var decoded *serverapi.WorkflowTaskListScopeError
	if !errors.As(err, &decoded) {
		t.Fatalf("decoded error = %T %v, want WorkflowTaskListScopeError", err, err)
	}
	if decoded.Reason != source.Reason || decoded.ProjectID == nil || *decoded.ProjectID != "project-1" || decoded.WorkflowID == nil || *decoded.WorkflowID != workflowID {
		t.Fatalf("decoded scope error = %+v, want %+v", decoded, source)
	}
}

func TestProtocolErrorDecodesWorkflowTaskCreateSelectionError(t *testing.T) {
	workflowID := "workflow-7e8d24d2-8a98-4dcf-a197-6214db1cb3c0"
	source := &serverapi.WorkflowTaskCreateSelectionError{
		Reason:     serverapi.WorkflowTaskCreateSelectionReasonWorkflowNotLinked,
		ProjectID:  "project-1",
		WorkflowID: &workflowID,
	}
	err := protocolError(&protocol.ResponseError{
		Code:    protocol.ErrCodeWorkflowTaskCreateSelection,
		Message: "selection failed",
		Data:    source.RPCErrorData(),
	})
	var decoded *serverapi.WorkflowTaskCreateSelectionError
	if !errors.As(err, &decoded) {
		t.Fatalf("decoded error = %T %v, want WorkflowTaskCreateSelectionError", err, err)
	}
	if decoded.Reason != source.Reason ||
		decoded.ProjectID != source.ProjectID ||
		decoded.WorkflowID == nil ||
		*decoded.WorkflowID != workflowID {
		t.Fatalf("decoded selection error = %+v, want %+v", decoded, source)
	}
	malformed := protocolError(&protocol.ResponseError{
		Code:    protocol.ErrCodeWorkflowTaskCreateSelection,
		Message: "selection failed",
		Data:    json.RawMessage(`{"type":"workflow_task_create_selection_error","reason":"workflow_not_linked","project_id":"project-1","workflow_id":"workflow-1"}`),
	})
	if errors.As(malformed, &decoded) {
		t.Fatalf("malformed selection payload decoded as typed error: %+v", decoded)
	}
}

func TestProtocolErrorDecodesWorkflowTaskCreateConflictError(t *testing.T) {
	source := &serverapi.WorkflowTaskCreateConflictError{
		Reason: serverapi.WorkflowTaskCreateConflictReasonSerialization,
	}
	err := protocolError(&protocol.ResponseError{
		Code:    protocol.ErrCodeWorkflowTaskCreateConflict,
		Message: "task create conflicted",
		Data:    source.RPCErrorData(),
	})
	var decoded *serverapi.WorkflowTaskCreateConflictError
	if !errors.As(err, &decoded) || decoded.Reason != source.Reason {
		t.Fatalf("decoded error = %T %v, want WorkflowTaskCreateConflictError", err, err)
	}
}

func TestProtocolErrorDecodesSessionRetargetError(t *testing.T) {
	source := &serverapi.SessionRetargetError{
		Reason:        serverapi.SessionRetargetTargetProjectRequired,
		SessionID:     "session-1",
		SourceProject: serverapi.ProjectReference{ID: "project-source", Name: "Source"},
		TargetRoot:    "/work/target",
		CandidateProjects: []serverapi.ProjectReference{{
			ID:   "project-target",
			Name: "Target",
		}},
	}
	err := protocolError(&protocol.ResponseError{
		Code:    protocol.ErrCodeSessionRetarget,
		Message: source.Error(),
		Data:    source.RPCErrorData(),
	})
	assertRemoteSessionRetargetError(t, err, source)
}

func TestRemoteSessionRetargetErrorRoundTrip(t *testing.T) {
	source := &serverapi.SessionRetargetError{
		Reason:        serverapi.SessionRetargetTargetProjectRequired,
		SessionID:     "session-1",
		SourceProject: serverapi.ProjectReference{ID: "project-source", Name: "Source"},
		TargetRoot:    "/work/target",
		CandidateProjects: []serverapi.ProjectReference{{
			ID:   "project-target",
			Name: "Target",
		}},
	}
	server := newRemoteTestServer(t, func(ws *websocket.Conn) {
		req := acceptRemoteHandshake(t, ws)
		if err := websocket.JSON.Receive(ws, &req); err != nil {
			t.Fatalf("receive session retarget request: %v", err)
		}
		if err := websocket.JSON.Send(ws, protocol.NewErrorResponseWithData(req.ID, source.RPCErrorCode(), source.Error(), source.RPCErrorData())); err != nil {
			t.Fatalf("send session retarget error: %v", err)
		}
	})
	defer server.Close()

	remote, err := DialRemoteURL(context.Background(), "ws"+server.URL[len("http"):])
	if err != nil {
		t.Fatalf("DialRemoteURL: %v", err)
	}
	defer func() { _ = remote.Close() }()
	_, err = remote.RetargetSessionWorkspace(context.Background(), serverapi.SessionRetargetWorkspaceRequest{
		ClientRequestID: "request-1",
		SessionID:       source.SessionID,
		WorkspaceRoot:   source.TargetRoot,
	})
	assertRemoteSessionRetargetError(t, err, source)
}

func assertRemoteSessionRetargetError(t *testing.T, err error, source *serverapi.SessionRetargetError) {
	t.Helper()
	var decoded *serverapi.SessionRetargetError
	if !errors.As(err, &decoded) {
		t.Fatalf("decoded error = %T %v, want SessionRetargetError", err, err)
	}
	if decoded.Reason != source.Reason ||
		decoded.SessionID != source.SessionID ||
		decoded.SourceProject != source.SourceProject ||
		decoded.TargetRoot != source.TargetRoot ||
		len(decoded.CandidateProjects) != len(source.CandidateProjects) ||
		decoded.CandidateProjects[0] != source.CandidateProjects[0] {
		t.Fatalf("decoded session retarget error = %+v, want %+v", decoded, source)
	}
}

func TestRemoteWorktreeStructuredErrorsRoundTrip(t *testing.T) {
	operationID := serverapi.NewWorktreeOperationID()
	for _, source := range remoteTestWorktreeStructuredErrors(operationID) {
		t.Run(source.Error(), func(t *testing.T) {
			server := newRemoteTestServer(t, func(ws *websocket.Conn) {
				req := acceptRemoteHandshake(t, ws)
				if err := websocket.JSON.Receive(ws, &req); err != nil {
					t.Fatalf("receive worktree request: %v", err)
				}
				if err := websocket.JSON.Send(ws, protocol.NewErrorResponseWithData(req.ID, source.RPCErrorCode(), source.Error(), source.RPCErrorData())); err != nil {
					t.Fatalf("send structured worktree error: %v", err)
				}
			})
			defer server.Close()

			remote, err := DialRemoteURL(context.Background(), "ws"+server.URL[len("http"):])
			if err != nil {
				t.Fatalf("DialRemoteURL: %v", err)
			}
			defer func() { _ = remote.Close() }()

			_, err = remote.ListWorktrees(context.Background(), serverapi.WorktreeListRequest{SessionID: "session"})
			assertRemoteWorktreeStructuredError(t, err, source, operationID)
		})
	}
}

func remoteTestWorktreeStructuredErrors(operationID serverapi.WorktreeOperationID) []protocol.StructuredRPCError {
	return []protocol.StructuredRPCError{
		&serverapi.WorktreeSelectorError{
			Kind:  serverapi.WorktreeSelectorErrorKindAmbiguous,
			Input: "feature",
			Candidates: []serverapi.WorktreeSelectorCandidate{{
				Variant:          serverapi.WorktreeTopologyVariantExternal,
				Selector:         "feature",
				FallbackIdentity: "/repo/feature",
			}},
		},
		&serverapi.WorktreeTransitionPendingError{
			SessionID:          "session",
			PendingOperationID: operationID,
		},
		serverapi.NewWorktreeImmediateTransitionError(
			serverapi.WorktreeImmediateTransitionOriginInactive,
			errors.New("originating model step ended"),
		),
		&serverapi.WorktreeSetupRetainedError{
			Worktree: serverapi.WorktreeTopologyEntry{
				Variant: serverapi.WorktreeTopologyVariantRegistered,
				Registered: &serverapi.WorktreeRegisteredFacts{
					Git: serverapi.WorktreeGitFacts{CanonicalRoot: "/repo/feature", HeadObject: "abc123", PathAvailable: true},
					Kent: serverapi.WorktreeKentFacts{
						WorktreeID:    "c4aaf0cf-4c50-4560-b6a2-6c294d0b1495",
						CanonicalRoot: "/repo/feature",
						DisplayName:   "feature",
					},
				},
			},
			Diagnostic: "setup failed",
		},
		&serverapi.WorktreeDeletePreconditionError{
			DirtyState: serverapi.WorktreeDirtyState{
				Kind:           serverapi.WorktreeDirtyStateDirty,
				DirtyFileCount: remoteTestIntPointer(2),
			},
		},
	}
}

func assertRemoteWorktreeStructuredError(t *testing.T, err error, source protocol.StructuredRPCError, operationID serverapi.WorktreeOperationID) {
	t.Helper()
	switch source.(type) {
	case *serverapi.WorktreeSelectorError:
		var decoded *serverapi.WorktreeSelectorError
		if !errors.As(err, &decoded) || len(decoded.Candidates) != 1 || decoded.Candidates[0].FallbackIdentity != "/repo/feature" {
			t.Fatalf("decoded selector error = %+v (%v)", decoded, err)
		}
	case *serverapi.WorktreeTransitionPendingError:
		var decoded *serverapi.WorktreeTransitionPendingError
		if !errors.As(err, &decoded) || decoded.PendingOperationID != operationID || decoded.SessionID != "session" {
			t.Fatalf("decoded pending transition = %+v (%v)", decoded, err)
		}
	case *serverapi.WorktreeImmediateTransitionError:
		var decoded *serverapi.WorktreeImmediateTransitionError
		if !errors.As(err, &decoded) || decoded.Kind != serverapi.WorktreeImmediateTransitionOriginInactive {
			t.Fatalf("decoded immediate transition = %+v (%v)", decoded, err)
		}
	case *serverapi.WorktreeSetupRetainedError:
		var decoded *serverapi.WorktreeSetupRetainedError
		if !errors.As(err, &decoded) || decoded.Worktree.Registered == nil || decoded.Worktree.Registered.Kent.DisplayName != "feature" {
			t.Fatalf("decoded retained setup = %+v (%v)", decoded, err)
		}
	case *serverapi.WorktreeDeletePreconditionError:
		var decoded *serverapi.WorktreeDeletePreconditionError
		if !errors.As(err, &decoded) || decoded.DirtyState.DirtyFileCount == nil || *decoded.DirtyState.DirtyFileCount != 2 {
			t.Fatalf("decoded delete precondition = %+v (%v)", decoded, err)
		}
	default:
		t.Fatalf("unsupported structured worktree error %T", source)
	}
}

func remoteTestIntPointer(value int) *int {
	return &value
}

func TestProtocolErrorDecodesWorkflowExecutionTargetResolutionError(t *testing.T) {
	source := &serverapi.WorkflowExecutionTargetResolutionError{
		Code:         serverapi.WorkflowExecutionTargetResolutionErrorInvalidRevision,
		RequestedRef: "missing-ref",
	}
	err := protocolError(&protocol.ResponseError{
		Code:    protocol.ErrCodeWorkflowExecutionTargetResolution,
		Message: "execution target resolution failed",
		Data:    source.RPCErrorData(),
	})
	var decoded *serverapi.WorkflowExecutionTargetResolutionError
	if !errors.As(err, &decoded) {
		t.Fatalf("decoded error = %T %v, want WorkflowExecutionTargetResolutionError", err, err)
	}
	if decoded.Code != source.Code || decoded.RequestedRef != source.RequestedRef {
		t.Fatalf("decoded execution target error = %+v, want %+v", decoded, source)
	}
}

func TestProtocolErrorDecodesWorkflowLockedExecutionTargetError(t *testing.T) {
	source := &serverapi.WorkflowLockedExecutionTargetError{
		Cause: serverapi.WorkflowLockedExecutionTargetCauseMissingBranch,
	}
	err := protocolError(&protocol.ResponseError{
		Code:    protocol.ErrCodeWorkflowLockedExecutionTarget,
		Message: "locked execution target is unavailable",
		Data:    source.RPCErrorData(),
	})
	var decoded *serverapi.WorkflowLockedExecutionTargetError
	if !errors.As(err, &decoded) {
		t.Fatalf("decoded error = %T %v, want WorkflowLockedExecutionTargetError", err, err)
	}
	if decoded.Cause != source.Cause {
		t.Fatalf("decoded locked target error = %+v, want %+v", decoded, source)
	}
}

func TestProtocolErrorAvoidsDuplicatingRuntimeSentinelMessage(t *testing.T) {
	err := protocolError(&protocol.ResponseError{Code: protocol.ErrCodeRuntimeNoActiveRun, Message: serverapi.ErrRuntimeNoActiveRun.Error()})
	if !errors.Is(err, serverapi.ErrRuntimeNoActiveRun) {
		t.Fatalf("expected runtime no-active, got %v", err)
	}
	if err.Error() != serverapi.ErrRuntimeNoActiveRun.Error() {
		t.Fatalf("runtime no-active error text = %q, want %q", err.Error(), serverapi.ErrRuntimeNoActiveRun.Error())
	}
}

func TestProtocolErrorMapsRequestCanceledCodeToClearMessage(t *testing.T) {
	err := protocolError(&protocol.ResponseError{Code: protocol.ErrCodeRequestCanceled, Message: "context canceled"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if !errors.Is(err, errRequestCanceledByClient) {
		t.Fatalf("expected normalized request-canceled error, got %v", err)
	}
}

func TestProtocolErrorMapsEmptyRequestCanceledCodeToClearMessage(t *testing.T) {
	err := protocolError(&protocol.ResponseError{Code: protocol.ErrCodeRequestCanceled})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if !errors.Is(err, errRequestCanceledByClient) {
		t.Fatalf("expected normalized request-canceled error, got %v", err)
	}
}

func mustJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return data
}
