package client

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"core/shared/clientui"
	"core/shared/llmerrors"
	"core/shared/protoapi"
	connectionpb "core/shared/protoapi/gen/kent/api/connection"
	sharedpb "core/shared/protoapi/gen/kent/api/shared"
	"core/shared/protocol"
	"core/shared/runtimeids"
	"core/shared/runtimeinput"
	"core/shared/serverapi"
	"core/shared/transcript"
	"golang.org/x/net/websocket"
)

func TestNormalizeWorkflowTaskObservationRPCErrorClassifiesConnectionEOF(t *testing.T) {
	err := normalizeWorkflowTaskObservationRPCError(io.EOF)
	if !errors.Is(err, serverapi.ErrStreamFailed) {
		t.Fatalf("normalized error = %v, want stream failure", err)
	}
	if errors.Is(err, io.EOF) {
		t.Fatalf("normalized error = %v, must not remain raw EOF", err)
	}
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

func TestProtocolErrorDecodesWorktreeBlocked(t *testing.T) {
	err := protocolError(&protocol.ResponseError{
		Code: protocol.ErrCodeWorktreeBlocked,
	})
	if !errors.Is(err, serverapi.ErrWorktreeBlocked) {
		t.Fatalf("decoded error = %v, want ErrWorktreeBlocked", err)
	}
}

func TestProtocolErrorDecodesMalformedWorktreeCreateAsContractError(t *testing.T) {
	err := protocolError(&protocol.ResponseError{
		Code:    protocol.ErrCodeWorktreeCreate,
		Message: "worktree creation failed",
		Data:    json.RawMessage(`{"owner":"other","diagnostic":"bad owner"}`),
	})
	var contractErr *serverapi.WorktreeCreateContractError
	if !errors.As(err, &contractErr) {
		t.Fatalf("decoded error = %T %v, want WorktreeCreateContractError", err, err)
	}
	var typed *serverapi.WorktreeCreateError
	if errors.As(err, &typed) {
		t.Fatalf("malformed wire data decoded as typed create error: %+v", typed)
	}
}

func TestProtocolErrorMapsBlankWorktreeBlockedSentinel(t *testing.T) {
	err := protocolError(&protocol.ResponseError{Code: protocol.ErrCodeWorktreeBlocked})
	if !errors.Is(err, serverapi.ErrWorktreeBlocked) {
		t.Fatalf("decoded error = %v, want ErrWorktreeBlocked", err)
	}
}

func TestProtocolErrorMapsWorkspaceNotRegisteredSentinel(t *testing.T) {
	err := protocolError(&protocol.ResponseError{
		Code: protocol.ErrCodeWorkspaceNotRegistered,
	})
	if !errors.Is(err, serverapi.ErrWorkspaceNotRegistered) {
		t.Fatalf("decoded error = %v, want workspace-not-registered sentinel", err)
	}
}

func TestRemotePreviewWorktreeDeleteSendsRouteAndDecodesEveryCleanlinessVariant(t *testing.T) {
	tests := []struct {
		name  string
		state clientui.WorktreeDirtyState
	}{
		{name: "clean", state: clientui.WorktreeDirtyState{Kind: clientui.WorktreeDirtyStateClean}},
		{
			name: "dirty",
			state: func() clientui.WorktreeDirtyState {
				count := 3
				return clientui.WorktreeDirtyState{Kind: clientui.WorktreeDirtyStateDirty, DirtyFileCount: &count}
			}(),
		},
		{
			name: "unknown",
			state: func() clientui.WorktreeDirtyState {
				cause := "status inspection failed"
				return clientui.WorktreeDirtyState{Kind: clientui.WorktreeDirtyStateUnknown, UnknownCause: &cause}
			}(),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			entry := serverapi.WorktreeTopologyEntry{
				Variant: serverapi.WorktreeTopologyVariantRegistered,
				Registered: &serverapi.WorktreeRegisteredFacts{
					Git: serverapi.WorktreeGitFacts{
						CanonicalRoot: "/repo/feature",
						HeadObject:    "abc123",
						PathAvailable: true,
					},
					Kent: serverapi.WorktreeKentFacts{
						WorktreeID:    "c4aaf0cf-4c50-4560-b6a2-6c294d0b1495",
						CanonicalRoot: "/repo/feature",
						DisplayName:   "feature",
					},
				},
			}
			response := serverapi.WorktreeDeletePreviewResponse{
				Worktree:         entry,
				DeletionSelector: entry.Registered.Kent.WorktreeID,
				Cleanliness:      test.state,
			}
			server := newRemoteTestServer(t, func(ws *websocket.Conn) {
				acceptRemoteHandshake(t, ws)
				var request protocol.Request
				if err := websocket.JSON.Receive(ws, &request); err != nil {
					t.Errorf("receive delete preview: %v", err)
					return
				}
				if request.Method != protocol.MethodWorktreeDeletePreview {
					t.Errorf("method = %q, want %q", request.Method, protocol.MethodWorktreeDeletePreview)
					return
				}
				var params serverapi.WorktreeDeletePreviewRequest
				if err := json.Unmarshal(request.Params, &params); err != nil {
					t.Errorf("decode delete preview request: %v", err)
					return
				}
				if err := params.Validate(); err != nil {
					t.Errorf("delete preview request validation: %v", err)
					return
				}
				if err := websocket.JSON.Send(ws, protocol.NewSuccessResponse(request.ID, response)); err != nil {
					t.Errorf("send delete preview response: %v", err)
				}
			})
			remote, err := DialRemoteURL(context.Background(), "ws"+server.URL[len("http"):])
			if err != nil {
				t.Fatalf("DialRemoteURL: %v", err)
			}
			defer func() { _ = remote.Close() }()

			got, err := remote.PreviewWorktreeDelete(context.Background(), serverapi.WorktreeDeletePreviewRequest{
				SessionID: "session-1",
				Selector:  "feature",
			})
			if err != nil {
				t.Fatalf("PreviewWorktreeDelete: %v", err)
			}
			if got.Cleanliness.Kind != test.state.Kind {
				t.Fatalf("cleanliness kind = %q, want %q", got.Cleanliness.Kind, test.state.Kind)
			}
			if err := got.Validate(); err != nil {
				t.Fatalf("decoded response validation: %v", err)
			}
		})
	}
}

func TestRemotePreviewWorktreeDeleteRejectsMismatchedResponseSelector(t *testing.T) {
	entry := serverapi.WorktreeTopologyEntry{
		Variant: serverapi.WorktreeTopologyVariantExternal,
		External: &serverapi.WorktreeExternalFacts{
			Git: serverapi.WorktreeGitFacts{
				CanonicalRoot: "/repo/external",
				HeadObject:    "abc123",
				PathAvailable: true,
			},
		},
	}
	server := newRemoteTestServer(t, func(ws *websocket.Conn) {
		acceptRemoteHandshake(t, ws)
		var request protocol.Request
		if err := websocket.JSON.Receive(ws, &request); err != nil {
			t.Errorf("receive delete preview: %v", err)
			return
		}
		response := serverapi.WorktreeDeletePreviewResponse{
			Worktree:         entry,
			DeletionSelector: "/repo/other",
			Cleanliness:      clientui.WorktreeDirtyState{Kind: clientui.WorktreeDirtyStateClean},
		}
		if err := websocket.JSON.Send(ws, protocol.NewSuccessResponse(request.ID, response)); err != nil {
			t.Errorf("send delete preview response: %v", err)
		}
	})
	remote, err := DialRemoteURL(context.Background(), "ws"+server.URL[len("http"):])
	if err != nil {
		t.Fatalf("DialRemoteURL: %v", err)
	}
	defer func() { _ = remote.Close() }()

	_, err = remote.PreviewWorktreeDelete(context.Background(), serverapi.WorktreeDeletePreviewRequest{
		SessionID: "session-1",
		Selector:  "external",
	})
	if err == nil {
		t.Fatal("mismatched delete preview selector was accepted")
	}
}

func TestDialRemoteWithTransportRejectsBlankSessionID(t *testing.T) {
	if _, err := newRemoteSessionAttachmentIntent(" \t "); !errors.Is(err, errRemoteSessionIDRequired) {
		t.Fatalf("intent error = %v, want required session ID error", err)
	}
}

func TestRemoteLiveWatchRejectsMalformedResponse(t *testing.T) {
	server := newRemoteTestServer(t, func(ws *websocket.Conn) {
		acceptRemoteHandshake(t, ws)
		var request protocol.Request
		if err := websocket.JSON.Receive(ws, &request); err != nil {
			if errors.Is(err, io.EOF) {
				return
			}
			t.Errorf("receive live watch request: %v", err)
			return
		}
		if request.Method != protocol.MethodRuntimeLiveWatch {
			t.Errorf("method = %q, want %q", request.Method, protocol.MethodRuntimeLiveWatch)
			return
		}
		if err := websocket.JSON.Send(ws, protocol.NewSuccessResponse(request.ID, serverapi.RuntimeLiveWatchResponse{
			SessionID: "session-1",
			Outcome: serverapi.RuntimeLiveWatchOutcome{
				Kind: serverapi.RuntimeLiveWatchQuestion,
			},
		})); err != nil {
			t.Errorf("send malformed live watch response: %v", err)
		}
	})
	remote, err := DialRemoteURL(context.Background(), "ws"+server.URL[len("http"):])
	if err != nil {
		t.Fatalf("DialRemoteURL: %v", err)
	}
	defer func() { _ = remote.Close() }()

	_, err = remote.LiveWatch(context.Background(), serverapi.RuntimeLiveWatchRequest{SessionID: "session-1"})
	var invalidResponse *InvalidResponseError
	if err == nil || !strings.Contains(err.Error(), "validate runtime live watch response") || !errors.As(err, &invalidResponse) {
		t.Fatalf("LiveWatch error = %v, want response validation error", err)
	}
}

func TestRemoteLiveWaitRejectsMalformedResponse(t *testing.T) {
	server := newRemoteTestServer(t, func(ws *websocket.Conn) {
		acceptRemoteHandshake(t, ws)
		var request protocol.Request
		if err := websocket.JSON.Receive(ws, &request); err != nil {
			if errors.Is(err, io.EOF) {
				return
			}
			t.Errorf("receive live wait request: %v", err)
			return
		}
		if request.Method != protocol.MethodRuntimeLiveWait {
			t.Errorf("method = %q, want %q", request.Method, protocol.MethodRuntimeLiveWait)
			return
		}
		_ = websocket.JSON.Send(ws, protocol.NewSuccessResponse(request.ID, serverapi.RuntimeLiveWaitResponse{
			SessionID: "not-a-session",
		}))
	})
	remote, err := DialRemoteURL(context.Background(), "ws"+server.URL[len("http"):])
	if err != nil {
		t.Fatalf("DialRemoteURL: %v", err)
	}
	defer func() { _ = remote.Close() }()

	_, err = remote.LiveWait(context.Background(), serverapi.RuntimeLiveWaitRequest{
		SessionID: "018fdd67-89ab-4cde-8123-456789abcdef",
	})
	var invalidResponse *InvalidResponseError
	if err == nil || !errors.As(err, &invalidResponse) {
		t.Fatalf("LiveWait error = %v, want InvalidResponseError", err)
	}
}

func TestRemoteLiveResponsesRejectMismatchedSessionIDs(t *testing.T) {
	tests := []struct {
		name     string
		method   string
		response any
		call     func(context.Context, *Remote) error
	}{
		{
			name:   "live watch",
			method: protocol.MethodRuntimeLiveWatch,
			response: serverapi.RuntimeLiveWatchResponse{
				SessionID: "session-b",
				Outcome: serverapi.RuntimeLiveWatchOutcome{
					Kind: serverapi.RuntimeLiveWatchFinalAnswer,
					FinalAnswer: &serverapi.RuntimeLiveWatchFinal{
						SessionName: "Session", DurationMillis: 1,
					},
				},
			},
			call: func(ctx context.Context, remote *Remote) error {
				_, err := remote.LiveWatch(ctx, serverapi.RuntimeLiveWatchRequest{SessionID: "session-a"})
				return err
			},
		},
		{
			name:   "live wait",
			method: protocol.MethodRuntimeLiveWait,
			response: serverapi.RuntimeLiveWaitResponse{
				SessionID:      "018fdd67-89ab-4cde-8123-456789abcdee",
				SessionName:    "Session",
				Result:         stringPointer("done"),
				DurationMillis: 1,
				LiveRunGroupID: "018fdd67-89ab-4cde-8123-456789abcdef",
				TerminalRunID:  "018fdd67-89ab-4cde-8123-456789abcdef",
				TerminalStepID: "018fdd67-89ab-4cde-8123-456789abcdef",
				TerminalStatus: "completed",
				ResultKind:     serverapi.RuntimeLiveResultKindAssistantFinalAnswer,
			},
			call: func(ctx context.Context, remote *Remote) error {
				_, err := remote.LiveWait(ctx, serverapi.RuntimeLiveWaitRequest{
					SessionID: "018fdd67-89ab-4cde-8123-456789abcdef",
				})
				return err
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := newRemoteTestServer(t, func(ws *websocket.Conn) {
				acceptRemoteHandshake(t, ws)
				var request protocol.Request
				if err := websocket.JSON.Receive(ws, &request); err != nil {
					return
				}
				if request.Method != test.method {
					return
				}
				_ = websocket.JSON.Send(ws, protocol.NewSuccessResponse(request.ID, test.response))
			})
			remote, err := DialRemoteURL(context.Background(), "ws"+server.URL[len("http"):])
			if err != nil {
				t.Fatalf("DialRemoteURL: %v", err)
			}
			defer func() { _ = remote.Close() }()

			err = test.call(context.Background(), remote)
			var invalidResponse *InvalidResponseError
			if err == nil || !errors.As(err, &invalidResponse) {
				t.Fatalf("%s error = %v, want InvalidResponseError", test.name, err)
			}
		})
	}
}

func stringPointer(value string) *string {
	return &value
}

func TestRemoteObserveWorkflowTaskRejectsMalformedResponseAsInvalidResponse(t *testing.T) {
	server := newRemoteTestServer(t, func(ws *websocket.Conn) {
		acceptRemoteHandshake(t, ws)
		var request protocol.Request
		if err := websocket.JSON.Receive(ws, &request); err != nil {
			t.Errorf("receive task observation request: %v", err)
			return
		}
		if err := websocket.JSON.Send(ws, protocol.NewSuccessResponse(request.ID, serverapi.WorkflowTaskObservationResponse{
			TaskID: "task-1",
		})); err != nil {
			t.Errorf("send malformed task observation response: %v", err)
		}
	})
	remote, err := DialRemoteURL(context.Background(), "ws"+server.URL[len("http"):])
	if err != nil {
		t.Fatalf("DialRemoteURL: %v", err)
	}
	defer func() { _ = remote.Close() }()

	_, err = remote.ObserveWorkflowTask(context.Background(), serverapi.WorkflowTaskObservationRequest{
		TaskID: "task-1", ProjectID: "project-1", Mode: serverapi.WorkflowTaskObservationWait,
	})
	var invalidResponse *InvalidResponseError
	if err == nil || !errors.As(err, &invalidResponse) {
		t.Fatalf("ObserveWorkflowTask error = %v, want InvalidResponseError", err)
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
	var encoded []byte
	if err := websocket.Message.Receive(ws, &encoded); err != nil {
		t.Fatalf("receive handshake: %v", err)
	}
	envelope, err := protoapi.DecodeEnvelope(encoded)
	if err != nil {
		t.Fatalf("decode handshake envelope: %v", err)
	}
	call := envelope.GetCall()
	if call == nil {
		t.Fatal("handshake call is required")
	}
	method := connectionpb.File_kent_api_connection_connection_proto.Services().
		ByName("ConnectionService").
		Methods().
		ByName("Handshake")
	operation, err := protoapi.OperationFromDescriptor(method)
	if err != nil {
		t.Fatalf("Handshake operation: %v", err)
	}
	if call.Operation != operation.Name {
		t.Fatalf("handshake operation = %q, want %q", call.Operation, operation.Name)
	}
	var request connectionpb.HandshakeRequest
	if err := protoapi.Decode(call.Payload, &request); err != nil {
		t.Fatalf("decode handshake request: %v", err)
	}
	result := &connectionpb.HandshakeResult{
		Outcome: &connectionpb.HandshakeResult_Success{
			Success: &connectionpb.HandshakeSuccess{
				Identity: &connectionpb.ServerIdentity{
					ProtocolVersion: protocol.Version,
					ServerId:        "server-1",
					Pid:             1,
				},
			},
		},
	}
	payload, err := protoapi.Encode(result)
	if err != nil {
		t.Fatalf("encode handshake result: %v", err)
	}
	response, err := protoapi.EncodeEnvelope(&sharedpb.Envelope{
		Frame: &sharedpb.Envelope_Result{Result: &sharedpb.Result{
			Operation:   operation.Name,
			Correlation: call.Correlation,
			Payload:     payload,
		}},
	})
	if err != nil {
		t.Fatalf("encode handshake result envelope: %v", err)
	}
	if err := websocket.Message.Send(ws, response); err != nil {
		t.Fatalf("send handshake response: %v", err)
	}
	return protocol.Request{}
}

func TestRemotePersistInputDraftSendsComposerInput(t *testing.T) {
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
		if decoded.Input != "visible draft" {
			t.Fatalf("input = %q, want visible draft", decoded.Input)
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
	})
	if err != nil {
		t.Fatalf("PersistInputDraft: %v", err)
	}
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
		acceptRemoteHandshake(t, ws)
		if acceptRemoteSessionAttachmentOrClosed(t, ws, "project-1", "workspace-1", "/workspace") == nil {
			return
		}
		var req protocol.Request
		if err := websocket.JSON.Receive(ws, &req); err != nil {
			if errors.Is(err, io.EOF) {
				return
			}
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
		event := protocol.SessionTranscriptEventParams{Message: clientui.NewTranscriptMessage(2, clientui.NewTranscriptEvent(clientui.TranscriptOperationalDiagnostic{
			Code:   clientui.OperationalDiagnosticSleepGuardFailed,
			Detail: "sleep prevention failed",
		}))}
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
	if message.Sequence != 2 || message.Kind() != clientui.TranscriptMessageOperationalDiagnostic {
		t.Fatalf("transcript message = %+v, want seq=2 operational diagnostic", message)
	}
}

func TestRemoteQuestionHistorySubscriptionAttachesAndDecodesTerminalStream(t *testing.T) {
	at := transcript.CommittedAtUnixMs(1_723_456_789_012)
	server := newRemoteTestServer(t, func(ws *websocket.Conn) {
		acceptRemoteHandshake(t, ws)
		if acceptRemoteSessionAttachmentOrClosed(t, ws, "project-1", "workspace-1", "/workspace") == nil {
			return
		}
		var req protocol.Request
		if err := websocket.JSON.Receive(ws, &req); err != nil {
			t.Fatalf("receive Question-history subscribe: %v", err)
		}
		if req.Method != protocol.MethodSessionQuestionHistorySubscribe {
			t.Fatalf("subscribe method = %q, want %q", req.Method, protocol.MethodSessionQuestionHistorySubscribe)
		}
		var params serverapi.QuestionHistorySubscribeRequest
		if err := json.Unmarshal(req.Params, &params); err != nil {
			t.Fatalf("decode Question-history subscribe params: %v", err)
		}
		if params.SessionID != "session-1" || params.MaxHandoffs != 3 {
			t.Fatalf("Question-history subscribe params = %+v", params)
		}
		if err := websocket.JSON.Send(ws, protocol.NewSuccessResponse(req.ID, protocol.SubscribeResponse{})); err != nil {
			t.Fatalf("send subscribe response: %v", err)
		}
		large := false
		started := protocol.SessionQuestionHistoryEventParams{Event: protocol.SessionQuestionHistoryEvent{
			Kind: string(serverapi.QuestionHistoryEventStarted), LargeHistory: &large,
		}}
		if err := websocket.JSON.Send(ws, protocol.Request{JSONRPC: protocol.JSONRPCVersion, Method: protocol.MethodSessionQuestionHistoryEvent, Params: mustJSON(t, started)}); err != nil {
			t.Fatalf("send started event: %v", err)
		}
		selected := 2
		commentary := "context"
		question := protocol.SessionQuestionHistoryEventParams{Event: protocol.SessionQuestionHistoryEvent{
			Kind: string(serverapi.QuestionHistoryEventQuestion),
			Question: &protocol.SessionQuestionHistoryQuestion{
				Question: "choose",
				Answer:   "second", SelectedOptionNumber: &selected,
				Commentary: &commentary, At: &at,
			},
		}}
		if err := websocket.JSON.Send(ws, protocol.Request{JSONRPC: protocol.JSONRPCVersion, Method: protocol.MethodSessionQuestionHistoryEvent, Params: mustJSON(t, question)}); err != nil {
			t.Fatalf("send Question event: %v", err)
		}
		omitted := true
		completed := protocol.SessionQuestionHistoryEventParams{Event: protocol.SessionQuestionHistoryEvent{
			Kind: string(serverapi.QuestionHistoryEventCompleted), HistoryOmitted: &omitted,
		}}
		if err := websocket.JSON.Send(ws, protocol.Request{JSONRPC: protocol.JSONRPCVersion, Method: protocol.MethodSessionQuestionHistoryEvent, Params: mustJSON(t, completed)}); err != nil {
			t.Fatalf("send completed event: %v", err)
		}
		if err := websocket.JSON.Send(ws, protocol.Request{
			JSONRPC: protocol.JSONRPCVersion,
			Method:  protocol.MethodSessionQuestionHistoryComplete,
			Params:  mustJSON(t, protocol.StreamCompleteParams{Code: protocol.ErrCodeStreamFailed, Message: "terminal failure"}),
		}); err != nil {
			t.Fatalf("send terminal failure: %v", err)
		}
	})

	remote, err := DialRemoteURL(context.Background(), "ws"+server.URL[len("http"):])
	if err != nil {
		t.Fatalf("DialRemote: %v", err)
	}
	defer func() { _ = remote.Close() }()
	sub, err := remote.SubscribeQuestionHistory(context.Background(), serverapi.QuestionHistorySubscribeRequest{
		SessionID: "session-1", MaxHandoffs: 3,
	})
	if err != nil {
		t.Fatalf("SubscribeQuestionHistory: %v", err)
	}
	defer func() { _ = sub.Close() }()
	started, err := sub.Next(context.Background())
	if err != nil || started.Kind != serverapi.QuestionHistoryEventStarted {
		t.Fatalf("started = %+v error %v", started, err)
	}
	question, err := sub.Next(context.Background())
	if err != nil || question.Question == nil ||
		question.Question.SelectedOptionNumber == nil ||
		*question.Question.SelectedOptionNumber != 2 ||
		question.Question.Commentary == nil ||
		question.Question.At == nil ||
		question.Question.At.UnixMs() != at.UnixMs() {
		t.Fatalf("Question = %+v error %v", question, err)
	}
	completed, err := sub.Next(context.Background())
	if err != nil || completed.HistoryOmitted == nil || !*completed.HistoryOmitted {
		t.Fatalf("completed = %+v error %v", completed, err)
	}
	if _, err := sub.Next(context.Background()); !errors.Is(err, serverapi.ErrStreamFailed) {
		t.Fatalf("terminal error = %v, want stream failure", err)
	}
}

func TestRemoteSessionTranscriptSubscriptionPreservesTypedCloseReason(t *testing.T) {
	server := newRemoteTestServer(t, func(ws *websocket.Conn) {
		acceptRemoteHandshake(t, ws)
		if acceptRemoteSessionAttachmentOrClosed(t, ws, "project-1", "workspace-1", "/workspace") == nil {
			return
		}
		var req protocol.Request
		if err := websocket.JSON.Receive(ws, &req); err != nil {
			if errors.Is(err, io.EOF) {
				return
			}
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

func newRemoteWorkflowProjectSubscriptionServer(t *testing.T, event protocol.WorkflowProjectEvent) *httptest.Server {
	t.Helper()
	return newRemoteTestServer(t, func(ws *websocket.Conn) {
		req := acceptRemoteHandshake(t, ws)
		if err := websocket.JSON.Receive(ws, &req); err != nil {
			if errors.Is(err, io.EOF) {
				return
			}
			t.Fatalf("receive workflow project subscribe: %v", err)
		}
		if req.Method != protocol.MethodWorkflowSubscribeProject {
			t.Fatalf("subscribe method = %q, want %q", req.Method, protocol.MethodWorkflowSubscribeProject)
		}
		if err := websocket.JSON.Send(ws, protocol.NewSuccessResponse(req.ID, protocol.SubscribeResponse{})); err != nil {
			t.Fatalf("send subscribe response: %v", err)
		}
		params := protocol.WorkflowProjectEventParams{Event: event}
		if err := websocket.JSON.Send(ws, protocol.Request{JSONRPC: protocol.JSONRPCVersion, Method: protocol.MethodWorkflowProjectEvent, Params: mustJSON(t, params)}); err != nil {
			t.Fatalf("send workflow project event: %v", err)
		}
	})
}

func TestRemoteWorkflowProjectSubscriptionDecodesTypedEvent(t *testing.T) {
	server := newRemoteWorkflowProjectSubscriptionServer(t, protocol.WorkflowProjectEvent{
		ProjectID:        remoteTestStringPointer("project-1"),
		WorkflowID:       remoteTestWorkflowIDPointer("11111111-1111-4111-8111-111111111111"),
		Resource:         protocol.WorkflowProjectEventResourceTask,
		Action:           protocol.WorkflowProjectEventActionQuestionWaiting,
		PrimaryEntityID:  "task-1",
		RelatedIDs:       []string{"run-1", "ask-1"},
		OccurredAtUnixMs: 1,
	})

	remote, err := DialRemoteURL(context.Background(), "ws"+server.URL[len("http"):])
	if err != nil {
		t.Fatalf("DialRemote: %v", err)
	}
	defer func() { _ = remote.Close() }()
	sub, err := remote.SubscribeWorkflowProject(context.Background(), serverapi.WorkflowProjectSubscribeRequest{ProjectID: "project-1"})
	if err != nil {
		t.Fatalf("SubscribeWorkflowProject: %v", err)
	}
	defer func() { _ = sub.Close() }()

	event, err := sub.Next(context.Background())
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if event.Resource != serverapi.WorkflowProjectEventResourceTask ||
		event.Action != serverapi.WorkflowProjectEventActionQuestionWaiting ||
		event.PrimaryEntityID != "task-1" ||
		!reflect.DeepEqual(event.RelatedIDs, []string{"run-1", "ask-1"}) {
		t.Fatalf("event = %+v, want typed task question event", event)
	}
}

func TestRemoteWorkflowProjectSubscriptionRejectsInvalidResourceActionCombination(t *testing.T) {
	server := newRemoteWorkflowProjectSubscriptionServer(t, protocol.WorkflowProjectEvent{
		ProjectID:        remoteTestStringPointer("project-1"),
		WorkflowID:       remoteTestWorkflowIDPointer("11111111-1111-4111-8111-111111111111"),
		Resource:         protocol.WorkflowProjectEventResourceTask,
		Action:           protocol.WorkflowProjectEventActionLinked,
		PrimaryEntityID:  "task-1",
		OccurredAtUnixMs: 1,
	})

	remote, err := DialRemoteURL(context.Background(), "ws"+server.URL[len("http"):])
	if err != nil {
		t.Fatalf("DialRemote: %v", err)
	}
	defer func() { _ = remote.Close() }()
	sub, err := remote.SubscribeWorkflowProject(context.Background(), serverapi.WorkflowProjectSubscribeRequest{ProjectID: "project-1"})
	if err != nil {
		t.Fatalf("SubscribeWorkflowProject: %v", err)
	}
	defer func() { _ = sub.Close() }()

	if _, err := sub.Next(context.Background()); !errors.Is(err, serverapi.ErrStreamFailed) {
		t.Fatalf("Next error = %v, want stream failure", err)
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
		WorktreeTransitionHeader: serverapi.WorktreeTransitionHeader{
			OperationID: operationID,
			SessionID:   "session-1",
		},
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

func TestRemoteCreateWorktreeUsesOnlySetupOperationIdentity(t *testing.T) {
	setupID := serverapi.NewWorktreeSetupOperationID()
	worktree := remoteTestRegisteredWorktreeEntry(t, true)
	var requests atomic.Int64
	target := clientui.SessionExecutionTarget{
		WorkspaceID:           "workspace",
		WorkspaceName:         "Workspace",
		WorkspaceRoot:         "/repo",
		WorkspaceAvailability: clientui.ProjectAvailabilityAvailable,
		CwdRelpath:            ".",
		EffectiveWorkdir:      "/repo",
	}
	server := newRemoteTestServer(t, func(ws *websocket.Conn) {
		req := acceptRemoteHandshake(t, ws)
		if err := websocket.JSON.Receive(ws, &req); err != nil {
			t.Fatalf("receive worktree create: %v", err)
		}
		requests.Add(1)
		if req.Method != protocol.MethodWorktreeCreate {
			t.Fatalf("method = %q, want %q", req.Method, protocol.MethodWorktreeCreate)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(req.Params, &fields); err != nil {
			t.Fatalf("unmarshal create fields: %v", err)
		}
		if _, exists := fields["client_request_id"]; exists {
			t.Fatalf("create request retained generic identity: %s", req.Params)
		}
		var params serverapi.WorktreeCreateRequest
		if err := json.Unmarshal(req.Params, &params); err != nil {
			t.Fatalf("unmarshal create params: %v", err)
		}
		if params.SetupOperationID != setupID || params.SessionID != "session-1" {
			t.Fatalf("create params = %+v", params)
		}
		if err := websocket.JSON.Send(
			ws,
			protocol.NewSuccessResponse(req.ID, serverapi.WorktreeCreateResponse{Target: target, Worktree: worktree}),
		); err != nil {
			t.Fatalf("send create response: %v", err)
		}
	})

	remote, err := DialRemoteURL(context.Background(), "ws"+server.URL[len("http"):])
	if err != nil {
		t.Fatalf("DialRemote: %v", err)
	}
	defer func() { _ = remote.Close() }()
	response, err := remote.CreateWorktree(context.Background(), serverapi.WorktreeCreateRequest{
		SetupOperationID: setupID,
		SessionID:        "session-1",
		BaseRef:          "feature",
	})
	if err != nil {
		t.Fatalf("CreateWorktree: %v", err)
	}
	if response.Worktree.Projection.Selector != "feature" {
		t.Fatalf("CreateWorktree response = %+v, want populated Worktree", response)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("worktree create requests = %d, want one", got)
	}
}

func TestRemoteWorktreeProjectedResponsesRejectContradictoryScope(t *testing.T) {
	branchName := "feature"
	sessionEntry := remoteTestRegisteredWorktreeEntry(t, true)
	workspaceEntry := remoteTestRegisteredWorktreeEntry(t, false)
	tests := []struct {
		name     string
		response any
		call     func(context.Context, *Remote) error
	}{
		{
			name:     "session list",
			response: serverapi.WorktreeListResponse{Worktrees: []serverapi.WorktreeListEntry{workspaceEntry}},
			call: func(ctx context.Context, remote *Remote) error {
				_, err := remote.ListWorktrees(ctx, serverapi.WorktreeListRequest{SessionID: "session"})
				return err
			},
		},
		{
			name: "workspace list",
			response: serverapi.WorktreeWorkspaceListResponse{
				WorkspaceID: "workspace",
				Worktrees:   []serverapi.WorktreeListEntry{sessionEntry},
			},
			call: func(ctx context.Context, remote *Remote) error {
				_, err := remote.ListWorkspaceWorktrees(ctx, serverapi.WorktreeWorkspaceListRequest{
					ProjectID:   "project",
					WorkspaceID: "workspace",
				})
				return err
			},
		},
		{
			name:     "selector resolution",
			response: serverapi.WorktreeSelectorPreviewResponse{Worktree: workspaceEntry},
			call: func(ctx context.Context, remote *Remote) error {
				_, err := remote.ResolveWorktreeSelector(ctx, serverapi.WorktreeSelectorPreviewRequest{
					SessionID: "session",
					Selector:  branchName,
				})
				return err
			},
		},
		{
			name:     "create",
			response: serverapi.WorktreeCreateResponse{Worktree: workspaceEntry},
			call: func(ctx context.Context, remote *Remote) error {
				_, err := remote.CreateWorktree(ctx, serverapi.WorktreeCreateRequest{
					SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
					SessionID:        "session",
					BaseRef:          branchName,
				})
				return err
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := newRemoteTestServer(t, func(ws *websocket.Conn) {
				req := acceptRemoteHandshake(t, ws)
				if err := websocket.JSON.Receive(ws, &req); err != nil {
					t.Fatalf("receive request: %v", err)
				}
				if err := websocket.JSON.Send(ws, protocol.NewSuccessResponse(req.ID, test.response)); err != nil {
					t.Fatalf("send response: %v", err)
				}
			})
			defer server.Close()
			remote, err := DialRemoteURL(context.Background(), "ws"+server.URL[len("http"):])
			if err != nil {
				t.Fatalf("DialRemoteURL: %v", err)
			}
			defer func() { _ = remote.Close() }()
			if err := test.call(context.Background(), remote); err == nil {
				t.Fatal("Remote accepted contradictory Worktree projection scope")
			}
		})
	}
}

func remoteTestRegisteredWorktreeEntry(t *testing.T, sessionScoped bool) serverapi.WorktreeListEntry {
	t.Helper()
	branchName := "feature"
	entry, err := serverapi.ProjectWorktreeListEntry(serverapi.WorktreeTopologyEntry{
		Variant: serverapi.WorktreeTopologyVariantRegistered,
		Registered: &serverapi.WorktreeRegisteredFacts{
			Git: serverapi.WorktreeGitFacts{
				CanonicalRoot: "/repo/feature",
				HeadObject:    "abc123",
				BranchName:    &branchName,
				PathAvailable: true,
			},
			Kent: serverapi.WorktreeKentFacts{
				WorktreeID:    "worktree-id",
				CanonicalRoot: "/repo/feature",
				DisplayName:   "feature",
			},
		},
	}, branchName, false, sessionScoped)
	if err != nil {
		t.Fatalf("ProjectWorktreeListEntry: %v", err)
	}
	return entry
}

func TestRemoteReattachSessionUpdatesBindingOnSameControlConnection(t *testing.T) {
	server := newRemoteTestServer(t, func(ws *websocket.Conn) {
		acceptRemoteHandshake(t, ws)
		for index, expected := range []struct {
			sessionID     string
			projectID     string
			workspaceID   string
			workspaceRoot string
		}{
			{sessionID: "session-a", projectID: "project-a", workspaceID: "workspace-a", workspaceRoot: "/workspace-a"},
			{sessionID: "session-b", projectID: "project-b", workspaceID: "workspace-b", workspaceRoot: "/workspace-b"},
		} {
			request := acceptRemoteSessionAttachmentOrClosed(
				t,
				ws,
				expected.projectID,
				expected.workspaceID,
				expected.workspaceRoot,
			)
			if request == nil {
				t.Fatalf("attach Session %d closed unexpectedly", index)
			}
			if request.SessionId != expected.sessionID {
				t.Fatalf("attach Session %d = %q, want %q", index, request.SessionId, expected.sessionID)
			}
		}
	})

	remote, err := DialRemoteURLForSession(context.Background(), "ws"+server.URL[len("http"):], "session-a")
	if err != nil {
		t.Fatalf("DialRemoteURLForSession: %v", err)
	}
	defer func() { _ = remote.Close() }()

	binding, err := remote.ReattachSession(context.Background(), "session-b")
	if err != nil {
		t.Fatalf("ReattachSession: %v", err)
	}
	current, present := remote.ProjectBinding()
	if !present ||
		binding.ProjectID != "project-b" ||
		current.ProjectID != "project-b" ||
		current.WorkspaceRoot != "/workspace-b" {
		t.Fatalf("binding after ReattachSession = returned %+v current %+v/%t", binding, current, present)
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
	workflowID := runtimeids.NewWorkflowID()
	source := &serverapi.WorkflowTaskListScopeError{
		Reason:     serverapi.WorkflowTaskListScopeReasonWorkflowNotLinked,
		ProjectID:  &projectID,
		WorkflowID: &workflowID,
	}
	err := protocolError(&protocol.ResponseError{
		Code:    protocol.ErrCodeWorkflowTaskListScope,
		Message: "scope resolution failed",
		Data:    mustRPCErrorData(t, source),
	})
	var decoded *serverapi.WorkflowTaskListScopeError
	if !errors.As(err, &decoded) {
		t.Fatalf("decoded error = %T %v, want WorkflowTaskListScopeError", err, err)
	}
	if decoded.Reason != source.Reason || decoded.ProjectID == nil || *decoded.ProjectID != "project-1" || decoded.WorkflowID == nil || *decoded.WorkflowID != workflowID {
		t.Fatalf("decoded scope error = %+v, want %+v", decoded, source)
	}
}

func TestProtocolErrorDecodesTaskSearchError(t *testing.T) {
	source := &serverapi.TaskSearchError{Reason: serverapi.TaskSearchErrorReasonNormalizedTooShort}
	err := protocolError(&protocol.ResponseError{
		Code:    protocol.ErrCodeWorkflowTaskSearch,
		Message: source.Error(),
		Data:    mustRPCErrorData(t, source),
	})
	var decoded *serverapi.TaskSearchError
	if !errors.As(err, &decoded) || decoded.Reason != source.Reason {
		t.Fatalf("decoded error = %T %v, want %+v", err, err, source)
	}
	malformed := protocolError(&protocol.ResponseError{
		Code:    protocol.ErrCodeWorkflowTaskSearch,
		Message: "fallback",
		Data:    json.RawMessage(`{"type":"task_search_error","reason":"other"}`),
	})
	if errors.As(malformed, &decoded) {
		t.Fatalf("malformed task search error decoded as typed: %+v", decoded)
	}
}

func TestProtocolErrorDecodesWorkflowTaskCreateSelectionError(t *testing.T) {
	workflowID := runtimeids.NewWorkflowID()
	source := &serverapi.WorkflowTaskCreateSelectionError{
		Reason:     serverapi.WorkflowTaskCreateSelectionReasonWorkflowNotLinked,
		ProjectID:  "project-1",
		WorkflowID: &workflowID,
	}
	err := protocolError(&protocol.ResponseError{
		Code:    protocol.ErrCodeWorkflowTaskCreateSelection,
		Message: "selection failed",
		Data:    mustRPCErrorData(t, source),
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
		Data:    mustRPCErrorData(t, source),
	})
	var decoded *serverapi.WorkflowTaskCreateConflictError
	if !errors.As(err, &decoded) || decoded.Reason != source.Reason {
		t.Fatalf("decoded error = %T %v, want WorkflowTaskCreateConflictError", err, err)
	}
}

func TestProtocolErrorDecodesWorkflowTaskMutationSelfTargetError(t *testing.T) {
	source := &serverapi.WorkflowTaskMutationSelfTargetError{TaskID: "task-1"}
	err := protocolError(&protocol.ResponseError{
		Code:    protocol.ErrCodeWorkflowTaskMutationSelfTarget,
		Message: source.Error(),
		Data:    source.RPCErrorData(),
	})
	var decoded *serverapi.WorkflowTaskMutationSelfTargetError
	if !errors.As(err, &decoded) || decoded.TaskID != source.TaskID {
		t.Fatalf("decoded error = %T %v, want self-target task %q", err, err, source.TaskID)
	}
}

func TestProtocolErrorDecodesWorkflowLabelError(t *testing.T) {
	projectID := "project-1"
	labelID := "11111111-1111-4111-8111-111111111111"
	source := &serverapi.WorkflowLabelError{
		Reason:    serverapi.WorkflowLabelErrorReasonWrongProject,
		ProjectID: &projectID,
		LabelID:   &labelID,
	}
	err := protocolError(&protocol.ResponseError{
		Code:    protocol.ErrCodeWorkflowLabel,
		Message: "label does not belong to project",
		Data:    source.RPCErrorData(),
	})
	var decoded *serverapi.WorkflowLabelError
	if !errors.As(err, &decoded) {
		t.Fatalf("decoded error = %T %v, want WorkflowLabelError", err, err)
	}
	if !reflect.DeepEqual(decoded, source) {
		t.Fatalf("decoded error = %+v, want %+v", decoded, source)
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
		Data:    mustRPCErrorData(t, source),
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
		if err := websocket.JSON.Send(ws, protocol.NewErrorResponseWithData(req.ID, source.RPCErrorCode(), source.Error(), mustRPCErrorData(t, source))); err != nil {
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
				if err := websocket.JSON.Send(ws, protocol.NewErrorResponseWithData(req.ID, source.RPCErrorCode(), source.Error(), mustRPCErrorData(t, source))); err != nil {
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
			ScriptPath: "/repo/scripts/setup.sh",
			Diagnostic: "setup failed",
		},
		&serverapi.WorktreeDeletePreconditionError{
			DirtyState: clientui.WorktreeDirtyState{
				Kind:           clientui.WorktreeDirtyStateDirty,
				DirtyFileCount: remoteTestIntPointer(2),
			},
		},
		&serverapi.WorktreeCreateError{
			Owner: serverapi.WorktreeCreateErrorOwnerForm,
			Diagnostic: errors.Join(
				errors.New("worktree path already exists"),
				errors.New("cleanup failed"),
			).Error(),
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
	case *serverapi.WorktreeCreateError:
		var decoded *serverapi.WorktreeCreateError
		if !errors.As(err, &decoded) || decoded.Owner != source.(*serverapi.WorktreeCreateError).Owner || decoded.Diagnostic != source.(*serverapi.WorktreeCreateError).Diagnostic {
			t.Fatalf("decoded create error = %+v (%v)", decoded, err)
		}
	default:
		t.Fatalf("unsupported structured worktree error %T", source)
	}
}

func remoteTestIntPointer(value int) *int {
	return &value
}

func remoteTestStringPointer(value string) *string {
	return &value
}

func remoteTestWorkflowIDPointer(value string) *runtimeids.WorkflowID {
	id, err := runtimeids.ParseWorkflowID(value)
	if err != nil {
		panic(err)
	}
	return &id
}

func TestProtocolErrorDecodesWorkflowExecutionTargetResolutionError(t *testing.T) {
	source := &serverapi.WorkflowExecutionTargetResolutionError{
		Code:         serverapi.WorkflowExecutionTargetResolutionErrorInvalidRevision,
		RequestedRef: "missing-ref",
	}
	err := protocolError(&protocol.ResponseError{
		Code:    protocol.ErrCodeWorkflowExecutionTargetResolution,
		Message: "execution target resolution failed",
		Data:    mustRPCErrorData(t, source),
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
		Data:    mustRPCErrorData(t, source),
	})
	var decoded *serverapi.WorkflowLockedExecutionTargetError
	if !errors.As(err, &decoded) {
		t.Fatalf("decoded error = %T %v, want WorkflowLockedExecutionTargetError", err, err)
	}
	if decoded.Cause != source.Cause {
		t.Fatalf("decoded locked target error = %+v, want %+v", decoded, source)
	}
}

func TestProtocolErrorMapsEquivalentRuntimeSentinel(t *testing.T) {
	err := protocolError(&protocol.ResponseError{Code: protocol.ErrCodeRuntimeNoActiveRun, Message: serverapi.ErrRuntimeNoActiveRun.Error()})
	if !errors.Is(err, serverapi.ErrRuntimeNoActiveRun) {
		t.Fatalf("expected runtime no-active, got %v", err)
	}
}

func TestProtocolErrorDecodesRuntimeCommandNotAcceptedCauses(t *testing.T) {
	command := runtimeinput.PromptCommandReviewName
	promptCause := &serverapi.PromptCommandError{
		Kind:    serverapi.PromptCommandErrorKindCommandNotFound,
		Command: &command,
	}
	for _, test := range []struct {
		name  string
		cause error
		check func(*testing.T, error)
	}{
		{
			name:  "prompt command",
			cause: promptCause,
			check: func(t *testing.T, err error) {
				var decoded *serverapi.PromptCommandError
				if !errors.As(err, &decoded) || decoded.Kind != promptCause.Kind || decoded.Command == nil || *decoded.Command != command {
					t.Fatalf("decoded cause = %T %+v, want %+v", err, decoded, promptCause)
				}
			},
		},
		{name: "manual compaction too soon", cause: serverapi.ErrManualCompactionTooSoon, check: func(t *testing.T, err error) {
			if !errors.Is(err, serverapi.ErrManualCompactionTooSoon) {
				t.Fatalf("decoded cause = %v, want manual compaction too soon", err)
			}
		}},
		{name: "manual compaction disabled", cause: serverapi.ErrManualCompactionDisabled, check: func(t *testing.T, err error) {
			if !errors.Is(err, serverapi.ErrManualCompactionDisabled) {
				t.Fatalf("decoded cause = %v, want manual compaction disabled", err)
			}
		}},
		{name: "manual compaction active", cause: serverapi.ErrManualCompactionActive, check: func(t *testing.T, err error) {
			if !errors.Is(err, serverapi.ErrManualCompactionActive) {
				t.Fatalf("decoded cause = %v, want manual compaction active", err)
			}
		}},
		{name: "runtime unavailable", cause: serverapi.ErrRuntimeUnavailable, check: func(t *testing.T, err error) {
			if !errors.Is(err, serverapi.ErrRuntimeUnavailable) {
				t.Fatalf("decoded cause = %v, want runtime unavailable", err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := serverapi.NewRuntimeCommandNotAcceptedError(test.cause)
			decoded := protocolError(&protocol.ResponseError{
				Code:    source.RPCErrorCode(),
				Message: source.Error(),
				Data:    source.RPCErrorData(),
			})
			if !errors.Is(decoded, serverapi.ErrRuntimeCommandNotAccepted) {
				t.Fatalf("decoded error = %v, want runtime command not accepted", decoded)
			}
			test.check(t, decoded)
		})
	}
}

func TestProtocolErrorRejectsMalformedRuntimeCommandNotAcceptedCause(t *testing.T) {
	for _, data := range []json.RawMessage{
		nil,
		json.RawMessage(`{}`),
		mustJSON(t, struct {
			Cause protocol.ResponseError `json:"cause"`
		}{Cause: protocol.ResponseError{
			Code:    protocol.ErrCodeRuntimeCommandNotAccepted,
			Message: "nested self",
		}}),
		mustJSON(t, struct {
			Cause protocol.ResponseError `json:"cause"`
		}{Cause: protocol.ResponseError{
			Code:    protocol.ErrCodePromptCommands,
			Message: "invalid prompt cause",
			Data:    json.RawMessage(`{}`),
		}}),
		mustJSON(t, struct {
			Cause protocol.ResponseError `json:"cause"`
		}{Cause: protocol.ResponseError{
			Code:    protocol.ErrCodeManualCompactionTooSoon,
			Message: "invalid manual compaction cause",
			Data:    json.RawMessage(`{"reason":"active"}`),
		}}),
	} {
		err := protocolError(&protocol.ResponseError{
			Code:    protocol.ErrCodeRuntimeCommandNotAccepted,
			Message: "not accepted",
			Data:    data,
		})
		if errors.Is(err, serverapi.ErrRuntimeCommandNotAccepted) {
			t.Fatalf("malformed nested cause decoded as runtime command not accepted: %v", err)
		}
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
