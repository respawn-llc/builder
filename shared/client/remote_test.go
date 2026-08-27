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
	authpb "core/shared/protoapi/gen/kent/api/auth"
	connectionpb "core/shared/protoapi/gen/kent/api/connection"
	projectpb "core/shared/protoapi/gen/kent/api/project"
	sharedpb "core/shared/protoapi/gen/kent/api/shared"
	worktreepb "core/shared/protoapi/gen/kent/api/worktree"
	"core/shared/protocol"
	"core/shared/runtimeids"
	"core/shared/runtimeinput"
	"core/shared/serverapi"
	"core/shared/transcript"
	"core/shared/worktreecontract"
	"golang.org/x/net/websocket"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
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
	_, err := decodeGeneratedResult(
		worktreeMethod("TransitionService", "Delete"),
		&worktreepb.DeleteResult{
			Outcome: &worktreepb.DeleteResult_Error{Error: &worktreepb.DeleteError{
				Code: "worktree_blocked",
				Detail: &worktreepb.DeleteError_WorktreeBlocked{
					WorktreeBlocked: &worktreepb.BlockedDetails{Diagnostic: "blocked"},
				},
			}},
		},
		worktreeError[*worktreepb.DeleteError],
	)
	if !errors.Is(err, worktreecontract.ErrWorktreeBlocked) {
		t.Fatalf("decoded error = %v, want ErrWorktreeBlocked", err)
	}
}

func TestProtocolErrorMapsWorkspaceNotRegisteredSentinel(t *testing.T) {
	workspaceID := "workspace"
	_, err := decodeGeneratedResult(
		worktreeMethod("ListService", "ListWorkspace"),
		&worktreepb.WorkspaceListResult{
			Outcome: &worktreepb.WorkspaceListResult_Error{Error: &worktreepb.WorkspaceListError{
				Code: "workspace_not_registered",
				Detail: &worktreepb.WorkspaceListError_WorkspaceNotRegistered{
					WorkspaceNotRegistered: &projectpb.WorkspaceNotRegisteredDetails{WorkspaceId: &workspaceID},
				},
			}},
		},
		worktreeError[*worktreepb.WorkspaceListError],
	)
	if !errors.Is(err, serverapi.ErrWorkspaceNotRegistered) {
		t.Fatalf("decoded error = %v, want workspace-not-registered sentinel", err)
	}
}

func TestRemotePreviewWorktreeDeleteSendsRouteAndDecodesEveryCleanlinessVariant(t *testing.T) {
	tests := []struct {
		name  string
		state *worktreepb.DirtyState
	}{
		{name: "clean", state: &worktreepb.DirtyState{Kind: worktreepb.DirtyStateKind_DIRTY_STATE_CLEAN}},
		{
			name: "dirty",
			state: func() *worktreepb.DirtyState {
				count := int32(3)
				return &worktreepb.DirtyState{Kind: worktreepb.DirtyStateKind_DIRTY_STATE_DIRTY, DirtyFileCount: &count}
			}(),
		},
		{
			name: "unknown",
			state: func() *worktreepb.DirtyState {
				cause := "status inspection failed"
				return &worktreepb.DirtyState{Kind: worktreepb.DirtyStateKind_DIRTY_STATE_UNKNOWN, UnknownCause: &cause}
			}(),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			entry := &worktreepb.TopologyEntry{
				Topology: &worktreepb.TopologyEntry_Registered{
					Registered: &worktreepb.RegisteredFacts{
						Git: &worktreepb.GitFacts{
							CanonicalRoot: "/repo/feature",
							HeadObject:    "abc123",
							PathAvailable: true,
						},
						Kent: &worktreepb.KentFacts{
							WorktreeId:    "c4aaf0cf-4c50-4560-b6a2-6c294d0b1495",
							CanonicalRoot: "/repo/feature",
							DisplayName:   "feature",
						},
					},
				},
			}
			response := &worktreepb.DeletePreviewSuccess{
				Worktree:         entry,
				DeletionSelector: entry.GetRegistered().Kent.WorktreeId,
				Cleanliness:      test.state,
			}
			server := newRemoteTestServer(t, func(ws *websocket.Conn) {
				acceptRemoteHandshake(t, ws)
				var request worktreepb.DeletePreviewRequest
				call := receiveRemoteGeneratedCall(t, ws, "DeletePreviewService", "Get", &request)
				sendRemoteGeneratedResult(t, ws, call, &worktreepb.DeletePreviewResult{
					Outcome: &worktreepb.DeletePreviewResult_Success{Success: response},
				})
			})
			remote, err := DialRemoteURL(context.Background(), "ws"+server.URL[len("http"):])
			if err != nil {
				t.Fatalf("DialRemoteURL: %v", err)
			}
			defer func() { _ = remote.Close() }()

			got, err := remote.PreviewWorktreeDelete(context.Background(), &worktreepb.DeletePreviewRequest{
				SessionId: "session-1",
				Selector:  "feature",
			})
			if err != nil {
				t.Fatalf("PreviewWorktreeDelete: %v", err)
			}
			if got.Cleanliness.Kind != test.state.Kind {
				t.Fatalf("cleanliness kind = %q, want %q", got.Cleanliness.Kind, test.state.Kind)
			}
			if err := protoapi.Validate(got); err != nil {
				t.Fatalf("decoded response validation: %v", err)
			}
		})
	}
}

func TestRemotePreviewWorktreeDeleteRejectsMismatchedResponseSelector(t *testing.T) {
	entry := &worktreepb.TopologyEntry{
		Topology: &worktreepb.TopologyEntry_External{
			External: &worktreepb.ExternalFacts{
				Git: &worktreepb.GitFacts{
					CanonicalRoot: "/repo/external",
					HeadObject:    "abc123",
					PathAvailable: true,
				},
			},
		},
	}
	server := newRemoteTestServer(t, func(ws *websocket.Conn) {
		acceptRemoteHandshake(t, ws)
		var request worktreepb.DeletePreviewRequest
		call := receiveRemoteGeneratedCall(t, ws, "DeletePreviewService", "Get", &request)
		response := &worktreepb.DeletePreviewSuccess{
			Worktree:         entry,
			DeletionSelector: "/repo/other",
			Cleanliness:      &worktreepb.DirtyState{Kind: worktreepb.DirtyStateKind_DIRTY_STATE_CLEAN},
		}
		sendRemoteGeneratedResult(t, ws, call, &worktreepb.DeletePreviewResult{
			Outcome: &worktreepb.DeletePreviewResult_Success{Success: response},
		})
	})
	remote, err := DialRemoteURL(context.Background(), "ws"+server.URL[len("http"):])
	if err != nil {
		t.Fatalf("DialRemoteURL: %v", err)
	}
	defer func() { _ = remote.Close() }()

	_, err = remote.PreviewWorktreeDelete(context.Background(), &worktreepb.DeletePreviewRequest{
		SessionId: "session-1",
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

func receiveRemoteGeneratedCall(
	t *testing.T,
	ws *websocket.Conn,
	serviceName string,
	methodName string,
	request proto.Message,
) *sharedpb.Call {
	t.Helper()
	var encoded []byte
	if err := websocket.Message.Receive(ws, &encoded); err != nil {
		t.Fatalf("receive generated call: %v", err)
	}
	envelope, err := protoapi.DecodeEnvelope(encoded)
	if err != nil {
		t.Fatalf("decode generated call envelope: %v", err)
	}
	call := envelope.GetCall()
	if call == nil {
		t.Fatal("generated frame is not a call")
	}
	method := worktreeMethod(protoreflect.Name(serviceName), protoreflect.Name(methodName))
	operation, err := protoapi.OperationFromDescriptor(method)
	if err != nil {
		t.Fatalf("%s.%s operation: %v", serviceName, methodName, err)
	}
	if call.Operation != operation.Name {
		t.Fatalf("operation = %q, want %q", call.Operation, operation.Name)
	}
	if err := protoapi.Decode(call.Payload, request); err != nil {
		t.Fatalf("decode generated request: %v", err)
	}
	return call
}

func sendRemoteGeneratedResult(t *testing.T, ws *websocket.Conn, call *sharedpb.Call, result proto.Message) {
	t.Helper()
	payload, err := protoapi.Marshal(result)
	if err != nil {
		t.Fatalf("marshal generated result: %v", err)
	}
	encoded, err := protoapi.EncodeEnvelope(&sharedpb.Envelope{
		Frame: &sharedpb.Envelope_Result{Result: &sharedpb.Result{
			Operation:   call.Operation,
			Correlation: call.Correlation,
			Payload:     payload,
		}},
	})
	if err != nil {
		t.Fatalf("encode generated result envelope: %v", err)
	}
	if err := websocket.Message.Send(ws, encoded); err != nil {
		t.Fatalf("send generated result: %v", err)
	}
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
		SessionID: "session-1",
		Input:     "visible draft",
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
	server := newRemoteTestServer(t, func(ws *websocket.Conn) {
		acceptRemoteHandshake(t, ws)
		var params worktreepb.DeleteRequest
		call := receiveRemoteGeneratedCall(t, ws, "TransitionService", "Delete", &params)
		if params.SessionId != "session-1" ||
			params.Selector != "wt-1" ||
			params.BranchCleanupPolicy != worktreepb.BranchCleanupMode_WORKTREE_BRANCH_CLEANUP_MODE_DELETE_SAFE {
			t.Fatalf("unexpected delete params: %+v", params)
		}
		if params.ProtoReflect().Descriptor().Fields().ByName("operation_id") != nil {
			t.Fatal("delete request unexpectedly contains operation_id")
		}
		branchName := "feature-a"
		sendRemoteGeneratedResult(t, ws, call, &worktreepb.DeleteResult{
			Outcome: &worktreepb.DeleteResult_Success{Success: &worktreepb.DeleteSuccess{
				Cleanup: &worktreepb.BranchCleanupOutcome{
					Kind:       worktreepb.BranchCleanupOutcomeKind_WORKTREE_BRANCH_CLEANUP_OUTCOME_DELETED,
					BranchName: &branchName,
				},
			}},
		})
	})

	remote, err := DialRemoteURL(context.Background(), "ws"+server.URL[len("http"):])
	if err != nil {
		t.Fatalf("DialRemote: %v", err)
	}
	defer func() { _ = remote.Close() }()

	resp, err := remote.DeleteWorktree(context.Background(), &worktreepb.DeleteRequest{
		SessionId:           "session-1",
		Selector:            "wt-1",
		BranchCleanupPolicy: worktreepb.BranchCleanupMode_WORKTREE_BRANCH_CLEANUP_MODE_DELETE_SAFE,
	})
	if err != nil {
		t.Fatalf("DeleteWorktree: %v", err)
	}
	if resp.Cleanup.Kind != worktreepb.BranchCleanupOutcomeKind_WORKTREE_BRANCH_CLEANUP_OUTCOME_DELETED {
		t.Fatalf("unexpected delete response: %+v", resp)
	}
}

func TestRemoteResolveWorktreeCreateTargetCarriesMethodAndPayload(t *testing.T) {
	server := newRemoteTestServer(t, func(ws *websocket.Conn) {
		acceptRemoteHandshake(t, ws)
		var params worktreepb.CreateTargetResolveRequest
		call := receiveRemoteGeneratedCall(t, ws, "CreateTargetService", "Resolve", &params)
		if params.SessionId != "session-1" || params.Target != "HEAD~1" {
			t.Fatalf("unexpected resolve params: %+v", params)
		}
		resolvedRef := "abc123"
		sendRemoteGeneratedResult(t, ws, call, &worktreepb.CreateTargetResolveResult{
			Outcome: &worktreepb.CreateTargetResolveResult_Success{Success: &worktreepb.CreateTargetResolveSuccess{
				Resolution: &worktreepb.CreateTargetResolution{
					Input:       "HEAD~1",
					Kind:        worktreepb.CreateTargetResolutionKind_WORKTREE_CREATE_TARGET_RESOLUTION_KIND_DETACHED_REF,
					ResolvedRef: &resolvedRef,
				},
			}},
		})
	})

	remote, err := DialRemoteURL(context.Background(), "ws"+server.URL[len("http"):])
	if err != nil {
		t.Fatalf("DialRemote: %v", err)
	}
	defer func() { _ = remote.Close() }()

	resp, err := remote.ResolveWorktreeCreateTarget(context.Background(), &worktreepb.CreateTargetResolveRequest{SessionId: "session-1", Target: "HEAD~1"})
	if err != nil {
		t.Fatalf("ResolveWorktreeCreateTarget: %v", err)
	}
	if resp.Resolution.Kind != worktreepb.CreateTargetResolutionKind_WORKTREE_CREATE_TARGET_RESOLUTION_KIND_DETACHED_REF || resp.Resolution.GetResolvedRef() != "abc123" {
		t.Fatalf("unexpected resolve response: %+v", resp)
	}
}

func TestRemoteCreateWorktreeUsesOnlySetupOperationIdentity(t *testing.T) {
	setupID := worktreecontract.NewSetupOperationID().String()
	worktree := remoteTestRegisteredWorktreeEntry(t, true)
	var requests atomic.Int64
	target := remoteTestWorktreeExecutionTarget()
	server := newRemoteTestServer(t, func(ws *websocket.Conn) {
		acceptRemoteHandshake(t, ws)
		requests.Add(1)
		var params worktreepb.CreateRequest
		call := receiveRemoteGeneratedCall(t, ws, "CreateService", "Create", &params)
		if params.ProtoReflect().Descriptor().Fields().ByName("client_request_id") != nil {
			t.Fatal("create request retained generic identity")
		}
		if params.SetupOperationId != setupID || params.SessionId != "session-1" {
			t.Fatalf("create params = %+v", params)
		}
		sendRemoteGeneratedResult(t, ws, call, &worktreepb.CreateResult{
			Outcome: &worktreepb.CreateResult_Success{Success: &worktreepb.CreateSuccess{
				Target: target, Worktree: worktree,
			}},
		})
	})

	remote, err := DialRemoteURL(context.Background(), "ws"+server.URL[len("http"):])
	if err != nil {
		t.Fatalf("DialRemote: %v", err)
	}
	defer func() { _ = remote.Close() }()
	baseRef := "feature"
	response, err := remote.CreateWorktree(context.Background(), &worktreepb.CreateRequest{
		SetupOperationId: setupID,
		SessionId:        "session-1",
		Spec:             &worktreepb.CreateSpec{BaseRef: &baseRef},
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
		service  string
		method   string
		request  proto.Message
		response proto.Message
		call     func(context.Context, *Remote) error
	}{
		{
			name:    "session list",
			service: "ListService",
			method:  "List",
			request: &worktreepb.ListRequest{},
			response: &worktreepb.ListResult{
				Outcome: &worktreepb.ListResult_Success{Success: &worktreepb.ListSuccess{
					Target: remoteTestWorktreeExecutionTarget(), Worktrees: []*worktreepb.ListEntry{workspaceEntry},
				}},
			},
			call: func(ctx context.Context, remote *Remote) error {
				_, err := remote.ListWorktrees(ctx, &worktreepb.ListRequest{SessionId: "session"})
				return err
			},
		},
		{
			name:    "workspace list",
			service: "ListService",
			method:  "ListWorkspace",
			request: &worktreepb.WorkspaceListRequest{},
			response: &worktreepb.WorkspaceListResult{
				Outcome: &worktreepb.WorkspaceListResult_Success{Success: &worktreepb.WorkspaceListSuccess{
					WorkspaceId: "workspace",
					Worktrees:   []*worktreepb.ListEntry{sessionEntry},
				}},
			},
			call: func(ctx context.Context, remote *Remote) error {
				_, err := remote.ListWorkspaceWorktrees(ctx, &worktreepb.WorkspaceListRequest{
					ProjectId:   "project",
					WorkspaceId: "workspace",
				})
				return err
			},
		},
		{
			name:    "selector resolution",
			service: "SelectorService",
			method:  "Resolve",
			request: &worktreepb.SelectorResolveRequest{},
			response: &worktreepb.SelectorResolveResult{
				Outcome: &worktreepb.SelectorResolveResult_Success{Success: &worktreepb.SelectorResolveSuccess{Worktree: workspaceEntry}},
			},
			call: func(ctx context.Context, remote *Remote) error {
				_, err := remote.ResolveWorktreeSelector(ctx, &worktreepb.SelectorResolveRequest{
					SessionId: "session",
					Selector:  branchName,
				})
				return err
			},
		},
		{
			name:    "create",
			service: "CreateService",
			method:  "Create",
			request: &worktreepb.CreateRequest{},
			response: &worktreepb.CreateResult{
				Outcome: &worktreepb.CreateResult_Success{Success: &worktreepb.CreateSuccess{
					Target: remoteTestWorktreeExecutionTarget(), Worktree: workspaceEntry,
				}},
			},
			call: func(ctx context.Context, remote *Remote) error {
				_, err := remote.CreateWorktree(ctx, &worktreepb.CreateRequest{
					SetupOperationId: worktreecontract.NewSetupOperationID().String(),
					SessionId:        "session",
					Spec:             &worktreepb.CreateSpec{BaseRef: &branchName},
				})
				return err
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := newRemoteTestServer(t, func(ws *websocket.Conn) {
				acceptRemoteHandshake(t, ws)
				call := receiveRemoteGeneratedCall(t, ws, test.service, test.method, test.request)
				sendRemoteGeneratedResult(t, ws, call, test.response)
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

func remoteTestRegisteredWorktreeEntry(t *testing.T, sessionScoped bool) *worktreepb.ListEntry {
	t.Helper()
	branchName := "feature"
	entry := &worktreepb.ListEntry{
		Topology: &worktreepb.TopologyEntry{
			Topology: &worktreepb.TopologyEntry_Registered{
				Registered: remoteTestRegisteredWorktreeFacts(),
			},
		},
		Projection: &worktreepb.ListProjection{Selector: branchName},
	}
	if sessionScoped {
		entry.Projection.Switch = &worktreepb.SwitchOperation{
			Kind:     worktreepb.SwitchOperationKind_WORKTREE_SWITCH_OPERATION_ENTER,
			Selector: &branchName,
		}
		entry.Projection.DeletePreview = &worktreepb.DeletePreviewOperation{Selector: "worktree-id"}
	}
	if err := protoapi.Validate(entry); err != nil {
		t.Fatalf("validate Worktree list entry: %v", err)
	}
	return entry
}

func remoteTestWorktreeExecutionTarget() *worktreepb.SessionExecutionTarget {
	return &worktreepb.SessionExecutionTarget{
		WorkspaceId:           "workspace",
		WorkspaceName:         "Workspace",
		WorkspaceRoot:         "/repo",
		WorkspaceAvailability: projectpb.ProjectAvailability_PROJECT_AVAILABILITY_AVAILABLE,
		CwdRelpath:            ".",
		EffectiveWorkdir:      "/repo",
	}
}

func TestProtocolErrorMapsSentinelCodes(t *testing.T) {
	for _, tc := range []struct {
		name   string
		decode func() error
		want   error
	}{
		{name: "prompt not found", decode: func() error {
			return protocolError(&protocol.ResponseError{Code: protocol.ErrCodePromptNotFound, Message: "prompt not found"})
		}, want: serverapi.ErrPromptNotFound},
		{name: "prompt resolved", decode: func() error {
			return protocolError(&protocol.ResponseError{Code: protocol.ErrCodePromptResolved, Message: "prompt resolved"})
		}, want: serverapi.ErrPromptAlreadyResolved},
		{name: "prompt unsupported", decode: func() error {
			return protocolError(&protocol.ResponseError{Code: protocol.ErrCodePromptUnsupported, Message: "prompt unsupported"})
		}, want: serverapi.ErrPromptUnsupported},
		{name: "method not found", decode: func() error {
			return protocolError(&protocol.ResponseError{Code: protocol.ErrCodeMethodNotFound, Message: "method not found"})
		}, want: serverapi.ErrMethodNotFound},
		{name: "workflow task not found", decode: func() error {
			return protocolError(&protocol.ResponseError{Code: protocol.ErrCodeWorkflowTaskNotFound, Message: "workflow task not found"})
		}, want: serverapi.ErrWorkflowTaskNotFound},
		{name: "workflow completion ambiguous", decode: func() error {
			return protocolError(&protocol.ResponseError{Code: protocol.ErrCodeWorkflowTaskCompleteAmbiguous, Message: "workflow completion ambiguous"})
		}, want: serverapi.ErrWorkflowTaskCompleteSelectorAmbiguous},
		{name: "workflow completion target missing", decode: func() error {
			return protocolError(&protocol.ResponseError{Code: protocol.ErrCodeWorkflowTaskCompleteNotFound, Message: "workflow completion target missing"})
		}, want: serverapi.ErrWorkflowTaskCompleteTargetNotFound},
		{
			name: "auth required",
			decode: func() error {
				_, err := decodeGeneratedResult(
					worktreeMethod("ListService", "List"),
					&worktreepb.ListResult{
						Outcome: &worktreepb.ListResult_Error{Error: &worktreepb.ListError{
							Code:   "auth_required",
							Detail: &worktreepb.ListError_AuthRequired{AuthRequired: &authpb.AuthRequiredDetails{}},
						}},
					},
					worktreeError[*worktreepb.ListError],
				)
				return err
			},
			want: serverapi.ErrServerAuthRequired,
		},
		{name: "runtime unavailable", decode: func() error {
			return protocolError(&protocol.ResponseError{Code: protocol.ErrCodeRuntimeUnavailable, Message: "runtime unavailable"})
		}, want: serverapi.ErrRuntimeUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.decode(); !errors.Is(err, tc.want) {
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
		SessionID:     source.SessionID,
		WorkspaceRoot: source.TargetRoot,
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
	for _, test := range remoteTestWorktreeStructuredErrors() {
		t.Run(test.name, func(t *testing.T) {
			server := newRemoteTestServer(t, func(ws *websocket.Conn) {
				acceptRemoteHandshake(t, ws)
				call := receiveRemoteGeneratedCall(t, ws, test.service, test.method, test.request)
				sendRemoteGeneratedResult(t, ws, call, test.result)
			})
			defer server.Close()

			remote, err := DialRemoteURL(context.Background(), "ws"+server.URL[len("http"):])
			if err != nil {
				t.Fatalf("DialRemoteURL: %v", err)
			}
			defer func() { _ = remote.Close() }()

			err = test.call(context.Background(), remote)
			test.assert(t, err)
		})
	}
}

type remoteTestWorktreeStructuredError struct {
	name    string
	service string
	method  string
	request proto.Message
	result  proto.Message
	call    func(context.Context, *Remote) error
	assert  func(*testing.T, error)
}

func remoteTestWorktreeStructuredErrors() []remoteTestWorktreeStructuredError {
	selectorDetails := &worktreepb.SelectorErrorDetails{
		Kind:  worktreepb.SelectorErrorKind_WORKTREE_SELECTOR_ERROR_KIND_AMBIGUOUS,
		Input: "feature",
		Candidates: []*worktreepb.SelectorCandidate{{
			Variant:          worktreepb.TopologyVariant_WORKTREE_TOPOLOGY_VARIANT_EXTERNAL,
			Selector:         "feature",
			FallbackIdentity: "/repo/feature",
		}},
	}
	retainedWorktree := remoteTestRegisteredWorktreeEntryWithoutProjection()
	dirtyFileCount := int32(2)
	createDiagnostic := errors.Join(
		errors.New("worktree path already exists"),
		errors.New("cleanup failed"),
	).Error()
	return []remoteTestWorktreeStructuredError{
		{
			name:    "selector error",
			service: "SelectorService",
			method:  "Resolve",
			request: &worktreepb.SelectorResolveRequest{},
			result: &worktreepb.SelectorResolveResult{Outcome: &worktreepb.SelectorResolveResult_Error{
				Error: &worktreepb.SelectorResolveError{
					Code:   "selector_error",
					Detail: &worktreepb.SelectorResolveError_SelectorError{SelectorError: selectorDetails},
				},
			}},
			call: func(ctx context.Context, remote *Remote) error {
				_, err := remote.ResolveWorktreeSelector(ctx, &worktreepb.SelectorResolveRequest{
					SessionId: "session", Selector: "feature",
				})
				return err
			},
			assert: func(t *testing.T, err error) {
				var decoded *worktreecontract.SelectorError
				if !errors.As(err, &decoded) || len(decoded.Details.Candidates) != 1 ||
					decoded.Details.Candidates[0].FallbackIdentity != "/repo/feature" {
					t.Fatalf("decoded selector error = %+v (%v)", decoded, err)
				}
			},
		},
		{
			name:    "setup retained",
			service: "CreateService",
			method:  "Create",
			request: &worktreepb.CreateRequest{},
			result: &worktreepb.CreateResult{Outcome: &worktreepb.CreateResult_Error{
				Error: &worktreepb.CreateError{
					Code: "setup_retained",
					Detail: &worktreepb.CreateError_SetupRetained{SetupRetained: &worktreepb.SetupRetainedDetails{
						Worktree:   retainedWorktree,
						ScriptPath: "/repo/scripts/setup.sh",
						Diagnostic: "setup failed",
					}},
				},
			}},
			call: func(ctx context.Context, remote *Remote) error {
				baseRef := "feature"
				_, err := remote.CreateWorktree(ctx, &worktreepb.CreateRequest{
					SetupOperationId: worktreecontract.NewSetupOperationID().String(),
					SessionId:        "session",
					Spec:             &worktreepb.CreateSpec{BaseRef: &baseRef},
				})
				return err
			},
			assert: func(t *testing.T, err error) {
				var decoded *worktreecontract.SetupRetainedError
				if !errors.As(err, &decoded) || decoded.Details.Worktree.Kent.DisplayName != "feature" {
					t.Fatalf("decoded retained setup = %+v (%v)", decoded, err)
				}
			},
		},
		{
			name:    "delete precondition",
			service: "TransitionService",
			method:  "Delete",
			request: &worktreepb.DeleteRequest{},
			result: &worktreepb.DeleteResult{Outcome: &worktreepb.DeleteResult_Error{
				Error: &worktreepb.DeleteError{
					Code: "delete_precondition",
					Detail: &worktreepb.DeleteError_DeletePrecondition{DeletePrecondition: &worktreepb.DeletePreconditionDetails{
						DirtyState: &worktreepb.DirtyState{
							Kind:           worktreepb.DirtyStateKind_DIRTY_STATE_DIRTY,
							DirtyFileCount: &dirtyFileCount,
						},
					}},
				},
			}},
			call: func(ctx context.Context, remote *Remote) error {
				_, err := remote.DeleteWorktree(ctx, &worktreepb.DeleteRequest{
					SessionId:           "session",
					Selector:            "feature",
					BranchCleanupPolicy: worktreepb.BranchCleanupMode_WORKTREE_BRANCH_CLEANUP_MODE_RETAIN,
				})
				return err
			},
			assert: func(t *testing.T, err error) {
				var decoded *worktreecontract.DeletePreconditionError
				if !errors.As(err, &decoded) || decoded.Details.DirtyState.DirtyFileCount == nil ||
					*decoded.Details.DirtyState.DirtyFileCount != 2 {
					t.Fatalf("decoded delete precondition = %+v (%v)", decoded, err)
				}
			},
		},
		{
			name:    "create error",
			service: "CreateService",
			method:  "Create",
			request: &worktreepb.CreateRequest{},
			result: &worktreepb.CreateResult{Outcome: &worktreepb.CreateResult_Error{
				Error: &worktreepb.CreateError{
					Code: "create_failed",
					Detail: &worktreepb.CreateError_CreateFailed{CreateFailed: &worktreepb.CreateFailureDetails{
						Owner:      worktreepb.CreateErrorOwner_WORKTREE_CREATE_ERROR_OWNER_FORM,
						Diagnostic: createDiagnostic,
					}},
				},
			}},
			call: func(ctx context.Context, remote *Remote) error {
				baseRef := "feature"
				_, err := remote.CreateWorktree(ctx, &worktreepb.CreateRequest{
					SetupOperationId: worktreecontract.NewSetupOperationID().String(),
					SessionId:        "session",
					Spec:             &worktreepb.CreateSpec{BaseRef: &baseRef},
				})
				return err
			},
			assert: func(t *testing.T, err error) {
				var decoded *worktreecontract.CreateError
				if !errors.As(err, &decoded) || decoded.Owner != worktreecontract.CreateErrorOwnerForm ||
					decoded.Diagnostic != createDiagnostic {
					t.Fatalf("decoded create error = %+v (%v)", decoded, err)
				}
			},
		},
	}
}

func remoteTestRegisteredWorktreeEntryWithoutProjection() *worktreepb.RegisteredFacts {
	return remoteTestRegisteredWorktreeFacts()
}

func remoteTestRegisteredWorktreeFacts() *worktreepb.RegisteredFacts {
	branchName := "feature"
	return &worktreepb.RegisteredFacts{
		Git: &worktreepb.GitFacts{
			CanonicalRoot: "/repo/feature",
			HeadObject:    "abc123",
			BranchName:    &branchName,
			PathAvailable: true,
		},
		Kent: &worktreepb.KentFacts{
			WorktreeId:    "worktree-id",
			CanonicalRoot: "/repo/feature",
			DisplayName:   "feature",
		},
	}
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

func TestProtocolErrorDecodesPendingWorkNotPending(t *testing.T) {
	id := runtimeids.NewQueueItemID()
	source := &serverapi.PendingWorkNotPendingError{ItemID: id}
	decoded := protocolError(&protocol.ResponseError{Code: source.RPCErrorCode(), Message: source.Error(), Data: source.RPCErrorData()})
	var typed *serverapi.PendingWorkNotPendingError
	if !errors.Is(decoded, serverapi.ErrPendingWorkNotPending) || !errors.As(decoded, &typed) || typed.ItemID != id {
		t.Fatalf("decoded = %+v", decoded)
	}
	if err := serverapi.DecodePendingWorkNotPendingError((&serverapi.PendingWorkNotPendingError{}).RPCErrorData()); err == nil {
		t.Fatal("invalid internal item id encoded as typed not-pending")
	}
}

func TestProtocolErrorDecodesPendingWorkCapacityDirectly(t *testing.T) {
	source := &serverapi.PendingWorkCapacityError{}
	decoded := protocolError(&protocol.ResponseError{
		Code:    source.RPCErrorCode(),
		Message: source.Error(),
		Data:    source.RPCErrorData(),
	})
	var typed *serverapi.PendingWorkCapacityError
	if !errors.Is(decoded, serverapi.ErrPendingWorkCapacity) || !errors.As(decoded, &typed) {
		t.Fatalf("decoded = %T %v, want typed Pending Work capacity", decoded, decoded)
	}
	if err := serverapi.DecodePendingWorkCapacityError(json.RawMessage(`{"reason":"other"}`)); err == nil {
		t.Fatal("invalid Pending Work capacity reason decoded as typed capacity")
	}
}

func TestRemotePendingWorkContractsPreserveTypedResults(t *testing.T) {
	guidance, exact := "keep details", " /compact   keep details "
	requestID := runtimeids.NewCompactionRequestID()
	wire := mustJSON(t, serverapi.RuntimeCompactContextRequest{
		SessionID: "session-1", RequestID: requestID,
		Admission: serverapi.ManualCompactionAdmission{Guidance: &guidance, RestorationInput: exact}})
	var compactRequest serverapi.RuntimeCompactContextRequest
	if err := json.Unmarshal(wire, &compactRequest); err != nil {
		t.Fatal(err)
	}
	if compactRequest.RequestID != requestID ||
		compactRequest.Admission.Guidance == nil ||
		*compactRequest.Admission.Guidance != guidance ||
		compactRequest.Admission.RestorationInput != exact {
		t.Fatalf("compact request = %+v", compactRequest)
	}
	id := runtimeids.NewQueueItemID()
	list := serverapi.RuntimeListPendingWorkResponse{PendingWork: serverapi.PendingWork{Items: []serverapi.PendingWorkItem{{
		ID: id, Lane: serverapi.PendingWorkLaneSteer, Kind: serverapi.PendingWorkItemKindManualCompaction,
		State: serverapi.PendingWorkItemStatePending, ManualCompaction: &serverapi.PendingWorkManualCompaction{
			Guidance: &guidance, RestorationInput: exact},
	}}}}
	removed := serverapi.RuntimeRemovePendingWorkResponse{Restoration: serverapi.PendingWorkRestoration{
		Kind:             serverapi.PendingWorkItemKindManualCompaction,
		ManualCompaction: &serverapi.PendingWorkManualCompactionRestoration{Input: exact},
	}}
	invalid := list
	invalid.PendingWork.Items = []serverapi.PendingWorkItem{list.PendingWork.Items[0]}
	invalid.PendingWork.Items[0].ManualCompaction = nil
	methods := []string{protocol.MethodRuntimeListPendingWork, protocol.MethodRuntimeRemovePendingWork, protocol.MethodRuntimeListPendingWork}
	responses := []any{list, removed, invalid}
	server := newRemoteTestServer(t, func(ws *websocket.Conn) {
		acceptRemoteHandshake(t, ws)
		for index, response := range responses {
			var request protocol.Request
			if err := websocket.JSON.Receive(ws, &request); err != nil {
				t.Fatal(err)
			}
			if request.Method != methods[index] {
				t.Fatalf("method = %q, want %q", request.Method, methods[index])
			}
			if err := websocket.JSON.Send(ws, protocol.NewSuccessResponse(request.ID, response)); err != nil {
				t.Fatal(err)
			}
		}
	})
	remote, err := DialRemoteURL(context.Background(), "ws"+server.URL[len("http"):])
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = remote.Close() }()
	gotList, err := remote.ListPendingWork(context.Background(), serverapi.RuntimeListPendingWorkRequest{SessionID: "session-1"})
	if err != nil {
		t.Fatal(err)
	}
	compaction := gotList.PendingWork.Items[0].ManualCompaction
	if compaction == nil || compaction.Guidance == nil || *compaction.Guidance != guidance || compaction.RestorationInput != exact {
		t.Fatalf("listed compaction = %+v", compaction)
	}
	gotRemoval, err := remote.RemovePendingWork(context.Background(), serverapi.RuntimeRemovePendingWorkRequest{SessionID: "session-1", ItemID: id})
	if err != nil {
		t.Fatal(err)
	}
	if gotRemoval.Restoration.ManualCompaction == nil || gotRemoval.Restoration.ManualCompaction.Input != exact {
		t.Fatalf("removal = %+v", gotRemoval.Restoration)
	}
	if _, err := remote.ListPendingWork(context.Background(), serverapi.RuntimeListPendingWorkRequest{SessionID: "session-1"}); err == nil {
		t.Fatal("invalid list response was accepted")
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
		{name: "Pending Work capacity", cause: &serverapi.PendingWorkCapacityError{}, check: func(t *testing.T, err error) {
			var typed *serverapi.PendingWorkCapacityError
			if !errors.Is(err, serverapi.ErrPendingWorkCapacity) || !errors.As(err, &typed) {
				t.Fatalf("decoded cause = %T %v, want typed Pending Work capacity", err, err)
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
