package client

import (
	"context"
	"errors"
	"io"
	"reflect"
	"testing"

	"core/shared/protoapi"
	projectpb "core/shared/protoapi/gen/kent/api/project"
	sharedpb "core/shared/protoapi/gen/kent/api/shared"
	worktreepb "core/shared/protoapi/gen/kent/api/worktree"
	"core/shared/protocol"
	"core/shared/serverapi"
	"core/shared/worktreecontract"

	"golang.org/x/net/websocket"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
	"google.golang.org/protobuf/types/known/emptypb"
)

func TestRemoteWorktreeBinaryFailureClassification(t *testing.T) {
	diagnostic := "settings are incomplete"
	notReady := serverapi.NewServerNotReadyError(
		serverapi.ServerNotReadyOnboardingRequired,
		serverapi.ServerNotReadyDetails{OnboardingCompleted: false, Diagnostic: &diagnostic},
		nil,
	)
	createErr := worktreecontract.NewCreateError(
		worktreecontract.CreateErrorOwnerForm,
		"branch already exists",
		nil,
	)
	tests := []struct {
		name   string
		method protoreflect.MethodDescriptor
		detail proto.Message
		check  func(*testing.T, error)
	}{
		{
			name:   "domain error",
			method: worktreeBinaryMethod("CreateService", "Create"),
			detail: mustWorktreeFailureDetail(t, createErr, nil),
			check: func(t *testing.T, err error) {
				var typed *worktreecontract.CreateError
				if !errors.As(err, &typed) || typed.Owner != worktreecontract.CreateErrorOwnerForm {
					t.Fatalf("error = %T %v, want Worktree CreateError", err, err)
				}
			},
		},
		{
			name:   "auth required",
			method: worktreeBinaryMethod("StatusService", "Get"),
			detail: mustWorktreeFailureDetail(t, serverapi.ErrServerAuthRequired, nil),
			check: func(t *testing.T, err error) {
				if !errors.Is(err, serverapi.ErrServerAuthRequired) {
					t.Fatalf("error = %v, want auth required", err)
				}
			},
		},
		{
			name:   "server not ready",
			method: worktreeBinaryMethod("StatusService", "Get"),
			detail: mustWorktreeFailureDetail(t, notReady, nil),
			check: func(t *testing.T, err error) {
				var typed *serverapi.ServerNotReadyError
				if !errors.As(err, &typed) || typed.Reason != serverapi.ServerNotReadyOnboardingRequired {
					t.Fatalf("error = %T %v, want structured server-not-ready", err, err)
				}
			},
		},
		{
			name:   "workspace registration",
			method: worktreeBinaryMethod("ListService", "ListWorkspace"),
			detail: mustWorktreeFailureDetail(t, serverapi.ErrWorkspaceNotRegistered,
				&projectpb.WorkspaceNotRegisteredDetails{
					ProjectId: stringPointer("project-1"), WorkspaceId: stringPointer("workspace-1"),
				}),
			check: func(t *testing.T, err error) {
				if !errors.Is(err, serverapi.ErrWorkspaceNotRegistered) {
					t.Fatalf("error = %v, want workspace not registered", err)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := protoapi.FailureResult(test.method, test.detail)
			if err != nil {
				t.Fatalf("build failure result: %v", err)
			}
			success, err := classifyGeneratedWorktreeResult(test.method, result)
			if success || err == nil {
				t.Fatalf("classification = success:%t error:%v, want failure", success, err)
			}
			test.check(t, err)
		})
	}

	statusMethod := worktreeBinaryMethod("StatusService", "Get")
	t.Run("malformed known result", func(t *testing.T) {
		result := &worktreepb.StatusResult{
			Outcome: &worktreepb.StatusResult_Error{
				Error: &worktreepb.StatusError{Code: "auth_required"},
			},
		}
		if success, err := classifyGeneratedWorktreeResult(statusMethod, result); success || err == nil {
			t.Fatalf("classification = success:%t error:%v, want contract error", success, err)
		}
	})
	t.Run("unknown future result", func(t *testing.T) {
		result := &worktreepb.StatusResult{
			Outcome: &worktreepb.StatusResult_Error{
				Error: &worktreepb.StatusError{Code: "future_failure"},
			},
		}
		if success, err := classifyGeneratedWorktreeResult(statusMethod, result); success ||
			err == nil || errors.Is(err, serverapi.ErrServerAuthRequired) {
			t.Fatalf("classification = success:%t error:%v, want generic future failure", success, err)
		}
	})
}

func mustWorktreeFailureDetail(
	t *testing.T,
	err error,
	workspace *projectpb.WorkspaceNotRegisteredDetails,
) proto.Message {
	t.Helper()
	detail, known, conversionErr := protoapi.WorktreeErrorToProto(err, workspace)
	if conversionErr != nil {
		t.Fatalf("map Worktree failure: %v", conversionErr)
	}
	if !known {
		t.Fatalf("Worktree failure %T is not declared", err)
	}
	return detail
}

func TestRemoteWorktreeBinarySetupSubscription(t *testing.T) {
	setupID := worktreecontract.NewSetupOperationID()
	started := worktreecontract.SetupEvent{
		SetupOperationID: setupID,
		Phase:            worktreecontract.SetupPhaseStarted,
		Started: &worktreecontract.SetupStarted{
			SourceWorkspaceRoot: "/workspace",
			WorktreeRoot:        "/worktree",
			ScriptPath:          "/workspace/setup.sh",
		},
	}
	completed := worktreecontract.SetupEvent{
		SetupOperationID: setupID,
		Phase:            worktreecontract.SetupPhaseCompleted,
		Completed:        &worktreecontract.SetupCompleted{},
	}
	tests := []struct {
		name       string
		completion *worktreepb.SetupCompletion
		wantErr    error
	}{
		{name: "normal", completion: &worktreepb.SetupCompletion{}, wantErr: io.EOF},
		{
			name: "stream failure",
			completion: func() *worktreepb.SetupCompletion {
				params := protocol.StreamCompleteParams{
					Code: protocol.ErrCodeStreamFailed, Message: serverapi.ErrStreamFailed.Error(),
				}
				code := int32(params.Code)
				return &worktreepb.SetupCompletion{Code: &code, Diagnostic: &params.Message}
			}(),
			wantErr: serverapi.ErrStreamFailed,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := newRemoteTestServer(t, func(ws *websocket.Conn) {
				acceptRemoteHandshake(t, ws)
				subscribe := worktreeBinaryMethod("SetupService", "Subscribe")
				call, present := receiveWorktreeBinaryCallIfPresent(t, ws, subscribe)
				if !present {
					return
				}
				request := &worktreepb.SetupSubscribeRequest{}
				if err := protoapi.Decode(call.Payload, request); err != nil {
					t.Fatalf("decode setup subscription: %v", err)
				}
				if request.SetupOperationId != setupID.String() {
					t.Fatalf("setup operation ID = %q, want %s", request.SetupOperationId, setupID)
				}
				sendWorktreeBinaryResult(t, ws, subscribe, call.Correlation, &emptypb.Empty{})
				sendWorktreeBinaryNotification(
					t, ws, worktreeBinaryMethod("SetupService", "Event"),
					mustWorktreeSetupEvent(t, started),
				)
				sendWorktreeBinaryNotification(
					t, ws, worktreeBinaryMethod("SetupService", "Event"),
					mustWorktreeSetupEvent(t, completed),
				)
				sendWorktreeBinaryNotification(
					t, ws, worktreeBinaryMethod("SetupService", "Complete"), test.completion,
				)
			})
			remote, err := DialRemoteURL(context.Background(), "ws"+server.URL[len("http"):])
			if err != nil {
				t.Fatalf("DialRemoteURL: %v", err)
			}
			defer func() { _ = remote.Close() }()
			subscription, err := newRemoteWorktreeBinaryClient(remote).SubscribeWorktreeSetup(
				context.Background(),
				worktreecontract.SetupSubscribeRequest{SetupOperationID: setupID},
			)
			if err != nil {
				t.Fatalf("SubscribeWorktreeSetup: %v", err)
			}
			defer func() { _ = subscription.Close() }()
			for index, want := range []worktreecontract.SetupEvent{started, completed} {
				got, nextErr := subscription.Next(context.Background())
				if nextErr != nil {
					t.Fatalf("Next(%d): %v", index, nextErr)
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("Next(%d) = %#v, want %#v", index, got, want)
				}
			}
			if _, err := subscription.Next(context.Background()); !errors.Is(err, test.wantErr) {
				t.Fatalf("completion error = %v, want %v", err, test.wantErr)
			}
		})
	}
}

func TestRemoteWorktreeBinaryStatus(t *testing.T) {
	want := worktreeBinaryStatusResponse()
	server := newRemoteTestServer(t, func(ws *websocket.Conn) {
		acceptRemoteHandshake(t, ws)
		method := worktreeBinaryMethod("StatusService", "Get")
		call := receiveWorktreeBinaryCall(t, ws, method)
		request := &worktreepb.StatusRequest{}
		if err := protoapi.Decode(call.Payload, request); err != nil {
			t.Fatalf("decode Status request: %v", err)
		}
		if request.SessionId != "session-1" {
			t.Fatalf("Status session = %q, want session-1", request.SessionId)
		}
		success, err := protoapi.WorktreeStatusSuccessToProto(want)
		if err != nil {
			t.Fatalf("map Status success: %v", err)
		}
		sendWorktreeBinaryResult(t, ws, method, call.Correlation, success)
	})
	remote, err := DialRemoteURL(context.Background(), "ws"+server.URL[len("http"):])
	if err != nil {
		t.Fatalf("DialRemoteURL: %v", err)
	}
	defer func() { _ = remote.Close() }()

	got, err := newRemoteWorktreeBinaryClient(remote).GetWorktreeStatus(
		context.Background(),
		worktreecontract.StatusRequest{SessionID: "session-1"},
	)
	if err != nil {
		t.Fatalf("GetWorktreeStatus: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Status response = %#v, want %#v", got, want)
	}
}

func TestRemoteWorktreeBinaryUnaryOperations(t *testing.T) {
	entry := worktreeBinaryEntry(t, true)
	workspaceEntry := worktreeBinaryEntry(t, false)
	target := worktreecontract.SessionExecutionTarget{
		WorkspaceID:           "workspace-1",
		WorkspaceName:         "Workspace",
		WorkspaceRoot:         "/repo",
		WorkspaceAvailability: worktreecontract.ProjectAvailabilityAvailable,
		EffectiveWorkdir:      "/repo",
	}
	operationID := worktreecontract.NewOperationID()
	setupID := worktreecontract.NewSetupOperationID()
	header := worktreecontract.TransitionHeader{OperationID: operationID, SessionID: "session-1"}
	ack := worktreecontract.ScheduledAcknowledgement{OperationID: operationID}
	branch := "feature"
	tests := []struct {
		name    string
		method  protoreflect.MethodDescriptor
		success proto.Message
		call    func(context.Context, *remoteWorktreeBinaryClient) (any, error)
		want    any
	}{
		worktreeBinaryCase(t, "list", "ListService", "List",
			worktreecontract.ListResponse{Target: target, Worktrees: []worktreecontract.ListEntry{entry}},
			protoapi.WorktreeListSuccessToProto,
			func(ctx context.Context, client *remoteWorktreeBinaryClient) (any, error) {
				return client.ListWorktrees(ctx, worktreecontract.ListRequest{SessionID: "session-1"})
			}),
		worktreeBinaryCase(t, "workspace list", "ListService", "ListWorkspace",
			worktreecontract.WorkspaceListResponse{
				WorkspaceID: "workspace-1",
				Worktrees:   []worktreecontract.ListEntry{workspaceEntry},
			},
			protoapi.WorktreeWorkspaceListSuccessToProto,
			func(ctx context.Context, client *remoteWorktreeBinaryClient) (any, error) {
				return client.ListWorkspaceWorktrees(ctx, worktreecontract.WorkspaceListRequest{
					ProjectID: "project-1", WorkspaceID: "workspace-1",
				})
			}),
		worktreeBinaryCase(t, "selector", "SelectorService", "Resolve",
			worktreecontract.SelectorResolveResponse{Worktree: entry},
			protoapi.WorktreeSelectorResolveSuccessToProto,
			func(ctx context.Context, client *remoteWorktreeBinaryClient) (any, error) {
				return client.ResolveWorktreeSelector(ctx, worktreecontract.SelectorResolveRequest{
					SessionID: "session-1", Selector: "feature",
				})
			}),
		worktreeBinaryCase(t, "delete preview", "DeletePreviewService", "Get",
			worktreecontract.DeletePreviewResponse{
				Worktree: entry.Topology, DeletionSelector: entry.Topology.Registered.Kent.WorktreeID,
				Cleanliness: worktreecontract.DirtyState{Kind: worktreecontract.DirtyStateClean},
			},
			protoapi.WorktreeDeletePreviewSuccessToProto,
			func(ctx context.Context, client *remoteWorktreeBinaryClient) (any, error) {
				return client.PreviewWorktreeDelete(ctx, worktreecontract.DeletePreviewRequest{
					SessionID: "session-1", Selector: "feature",
				})
			}),
		worktreeBinaryCase(t, "create target", "CreateTargetService", "Resolve",
			worktreecontract.CreateTargetResolveResponse{Resolution: worktreecontract.CreateTargetResolution{
				Input: "HEAD~1", Kind: worktreecontract.CreateTargetResolutionKindDetachedRef, ResolvedRef: "abc123",
			}},
			protoapi.WorktreeCreateTargetResolveSuccessToProto,
			func(ctx context.Context, client *remoteWorktreeBinaryClient) (any, error) {
				return client.ResolveWorktreeCreateTarget(ctx, worktreecontract.CreateTargetResolveRequest{
					SessionID: "session-1", Target: "HEAD~1",
				})
			}),
		worktreeBinaryCase(t, "create", "CreateService", "Create",
			worktreecontract.CreateResponse{Target: target, Worktree: entry},
			protoapi.WorktreeCreateSuccessToProto,
			func(ctx context.Context, client *remoteWorktreeBinaryClient) (any, error) {
				return client.CreateWorktree(ctx, worktreecontract.CreateRequest{
					SetupOperationID: setupID, SessionID: "session-1", BaseRef: "main",
				})
			}),
		worktreeBinaryCase(t, "enter", "TransitionService", "Enter", ack,
			protoapi.WorktreeScheduledAcknowledgementToProto,
			func(ctx context.Context, client *remoteWorktreeBinaryClient) (any, error) {
				return client.EnterWorktree(ctx, worktreecontract.EnterRequest{
					TransitionHeader: header, Selector: "feature",
				})
			}),
		worktreeBinaryCase(t, "leave", "TransitionService", "Leave", ack,
			protoapi.WorktreeScheduledAcknowledgementToProto,
			func(ctx context.Context, client *remoteWorktreeBinaryClient) (any, error) {
				return client.LeaveWorktree(ctx, worktreecontract.LeaveRequest{TransitionHeader: header})
			}),
		worktreeBinaryCase(t, "delete", "TransitionService", "Delete",
			worktreecontract.DeleteResult{
				Kind: worktreecontract.DeleteResultKindCompleted,
				Completed: &worktreecontract.DeleteCompletedResult{Cleanup: worktreecontract.BranchCleanupOutcome{
					Kind: worktreecontract.BranchCleanupOutcomeDeleted, BranchName: &branch,
				}},
			},
			protoapi.WorktreeDeleteSuccessToProto,
			func(ctx context.Context, client *remoteWorktreeBinaryClient) (any, error) {
				return client.DeleteWorktree(ctx, worktreecontract.DeleteRequest{
					TransitionHeader: header, Selector: "feature",
					BranchCleanupPolicy: worktreecontract.BranchCleanupModeDeleteSafe,
				})
			}),
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := newRemoteTestServer(t, func(ws *websocket.Conn) {
				acceptRemoteHandshake(t, ws)
				call := receiveWorktreeBinaryCall(t, ws, test.method)
				request := dynamicpb.NewMessage(test.method.Input())
				if err := protoapi.Decode(call.Payload, request); err != nil {
					t.Fatalf("decode %s request: %v", test.method.FullName(), err)
				}
				sendWorktreeBinaryResult(t, ws, test.method, call.Correlation, test.success)
			})
			remote, err := DialRemoteURL(context.Background(), "ws"+server.URL[len("http"):])
			if err != nil {
				t.Fatalf("DialRemoteURL: %v", err)
			}
			defer func() { _ = remote.Close() }()
			got, err := test.call(context.Background(), newRemoteWorktreeBinaryClient(remote))
			if err != nil {
				t.Fatalf("%s: %v", test.name, err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("%s response = %#v, want %#v", test.name, got, test.want)
			}
		})
	}
}

func worktreeBinaryCase[Domain any, Success proto.Message](
	t *testing.T,
	name string,
	service string,
	method string,
	want Domain,
	toProto func(Domain) (Success, error),
	call func(context.Context, *remoteWorktreeBinaryClient) (any, error),
) struct {
	name    string
	method  protoreflect.MethodDescriptor
	success proto.Message
	call    func(context.Context, *remoteWorktreeBinaryClient) (any, error)
	want    any
} {
	t.Helper()
	success, err := toProto(want)
	if err != nil {
		t.Fatalf("map %s success: %v", name, err)
	}
	return struct {
		name    string
		method  protoreflect.MethodDescriptor
		success proto.Message
		call    func(context.Context, *remoteWorktreeBinaryClient) (any, error)
		want    any
	}{
		name: name, method: worktreeBinaryMethod(protoreflect.Name(service), protoreflect.Name(method)),
		success: success, call: call, want: want,
	}
}

func receiveWorktreeBinaryCall(
	t *testing.T,
	ws *websocket.Conn,
	method protoreflect.MethodDescriptor,
) *sharedpb.Call {
	t.Helper()
	var encoded []byte
	if err := websocket.Message.Receive(ws, &encoded); err != nil {
		t.Fatalf("receive %s: %v", method.FullName(), err)
	}
	return decodeWorktreeBinaryCall(t, encoded, method)
}

func receiveWorktreeBinaryCallIfPresent(
	t *testing.T,
	ws *websocket.Conn,
	method protoreflect.MethodDescriptor,
) (*sharedpb.Call, bool) {
	t.Helper()
	var encoded []byte
	if err := websocket.Message.Receive(ws, &encoded); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, false
		}
		t.Fatalf("receive %s: %v", method.FullName(), err)
	}
	return decodeWorktreeBinaryCall(t, encoded, method), true
}

func decodeWorktreeBinaryCall(
	t *testing.T,
	encoded []byte,
	method protoreflect.MethodDescriptor,
) *sharedpb.Call {
	t.Helper()
	envelope, err := protoapi.DecodeEnvelope(encoded)
	if err != nil {
		t.Fatalf("decode %s call: %v", method.FullName(), err)
	}
	call := envelope.GetCall()
	operation, err := protoapi.OperationFromDescriptor(method)
	if err != nil {
		t.Fatalf("resolve %s operation: %v", method.FullName(), err)
	}
	if call == nil || call.Operation != operation.Name || call.Correlation == nil {
		t.Fatalf("%s call = %+v", method.FullName(), call)
	}
	return call
}

func sendWorktreeBinaryResult(
	t *testing.T,
	ws *websocket.Conn,
	method protoreflect.MethodDescriptor,
	correlation *string,
	success proto.Message,
) {
	t.Helper()
	result, err := protoapi.SuccessResult(method, success)
	if err != nil {
		t.Fatalf("build %s result: %v", method.FullName(), err)
	}
	payload, err := protoapi.Encode(result)
	if err != nil {
		t.Fatalf("encode %s result: %v", method.FullName(), err)
	}
	operation, err := protoapi.OperationFromDescriptor(method)
	if err != nil {
		t.Fatalf("resolve %s operation: %v", method.FullName(), err)
	}
	encoded, err := protoapi.EncodeEnvelope(&sharedpb.Envelope{
		Frame: &sharedpb.Envelope_Result{Result: &sharedpb.Result{
			Operation:   operation.Name,
			Correlation: correlation,
			Payload:     payload,
		}},
	})
	if err != nil {
		t.Fatalf("encode %s result envelope: %v", method.FullName(), err)
	}
	if err := websocket.Message.Send(ws, encoded); err != nil {
		t.Fatalf("send %s result: %v", method.FullName(), err)
	}
}

func sendWorktreeBinaryNotification(
	t *testing.T,
	ws *websocket.Conn,
	method protoreflect.MethodDescriptor,
	message proto.Message,
) {
	t.Helper()
	operation, err := protoapi.OperationFromDescriptor(method)
	if err != nil {
		t.Fatalf("resolve %s operation: %v", method.FullName(), err)
	}
	payload, err := protoapi.Encode(message)
	if err != nil {
		t.Fatalf("encode %s notification: %v", method.FullName(), err)
	}
	encoded, err := protoapi.EncodeEnvelope(&sharedpb.Envelope{
		Frame: &sharedpb.Envelope_NotificationEvent{NotificationEvent: &sharedpb.NotificationEvent{
			Operation: operation.Name,
			Payload:   payload,
		}},
	})
	if err != nil {
		t.Fatalf("encode %s notification envelope: %v", method.FullName(), err)
	}
	if err := websocket.Message.Send(ws, encoded); err != nil {
		t.Fatalf("send %s notification: %v", method.FullName(), err)
	}
}

func mustWorktreeSetupEvent(
	t *testing.T,
	event worktreecontract.SetupEvent,
) *worktreepb.SetupEvent {
	t.Helper()
	message, err := protoapi.WorktreeSetupEventToProto(event)
	if err != nil {
		t.Fatalf("map setup event: %v", err)
	}
	return message
}

func worktreeBinaryStatusResponse() worktreecontract.StatusResponse {
	root := "/repo"
	ref := "refs/heads/main"
	return worktreecontract.StatusResponse{
		Target: worktreecontract.SessionExecutionTarget{
			WorkspaceID:           "workspace-1",
			WorkspaceName:         "Workspace",
			WorkspaceRoot:         root,
			WorkspaceAvailability: worktreecontract.ProjectAvailabilityAvailable,
			EffectiveWorkdir:      root,
		},
		Worktree: worktreecontract.StatusTarget{
			RecordedRoot:      root,
			ObservedRoot:      &root,
			RecordedBranchRef: &ref,
			ObservedBranchRef: &ref,
		},
		Problems: []worktreecontract.StatusProblem{},
	}
}

func worktreeBinaryEntry(t *testing.T, sessionScoped bool) worktreecontract.ListEntry {
	t.Helper()
	branchRef := "refs/heads/feature"
	branchName := "feature"
	originSessionID := "session-1"
	topology := worktreecontract.TopologyEntry{
		Variant: worktreecontract.TopologyVariantRegistered,
		Registered: &worktreecontract.RegisteredFacts{
			Git: worktreecontract.GitFacts{
				CanonicalRoot: "/repo/feature",
				HeadObject:    "abc123",
				BranchRef:     &branchRef,
				BranchName:    &branchName,
				PathAvailable: true,
			},
			Kent: worktreecontract.KentFacts{
				WorktreeID:      "123e4567-e89b-42d3-a456-426614174000",
				CanonicalRoot:   "/repo/feature",
				DisplayName:     "feature",
				Managed:         true,
				OriginSessionID: &originSessionID,
			},
		},
	}
	entry, err := worktreecontract.ProjectListEntry(topology, "feature", false, sessionScoped)
	if err != nil {
		t.Fatalf("project Worktree entry: %v", err)
	}
	return entry
}
