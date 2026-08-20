package client

import (
	"context"
	"errors"
	"io"
	"testing"

	"core/shared/protoapi"
	authpb "core/shared/protoapi/gen/kent/api/auth"
	connectionpb "core/shared/protoapi/gen/kent/api/connection"
	projectpb "core/shared/protoapi/gen/kent/api/project"
	sharedpb "core/shared/protoapi/gen/kent/api/shared"
	"core/shared/protocol"
	"core/shared/rpcwire"
	"core/shared/serverapi"
	"golang.org/x/net/websocket"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type remoteTestSetupKind uint8

const (
	remoteTestSetupHandshake remoteTestSetupKind = iota + 1
	remoteTestSetupProject
	remoteTestSetupSession
)

type remoteTestSetupResponse struct {
	rootID        string
	projectID     string
	workspaceID   string
	workspaceRoot string
}

func testProjectAttachResponse(t testing.TB, projectID string, workspaceID string, workspaceRoot string) remoteAttachment {
	t.Helper()
	return remoteAttachment{project: &ProjectAttachment{
		ProjectID: projectID, WorkspaceID: workspaceID, WorkspaceRoot: workspaceRoot,
	}}
}

func testProjectAttachResponseForIntent(t testing.TB, intent *remoteAttachmentIntent, workspaceID string, workspaceRoot string) remoteAttachment {
	t.Helper()
	request, present := intent.projectRequest()
	if !present {
		t.Fatal("project attachment intent is required")
	}
	attachment := &ProjectAttachment{
		ProjectID: request.projectID, WorkspaceID: workspaceID, WorkspaceRoot: workspaceRoot,
	}
	if request.workspace != nil {
		if request.workspace.workspaceID != nil {
			value := *request.workspace.workspaceID
			attachment.selection = &remoteProjectAttachmentSelection{workspaceID: &value}
		} else {
			requestedRoot := *request.workspace.workspaceRoot
			attachment.selection = &remoteProjectAttachmentSelection{
				requestedRoot: &requestedRoot,
				canonicalRoot: &workspaceRoot,
			}
		}
	}
	return remoteAttachment{project: attachment}
}

func testSessionAttachResponse(t testing.TB, projectID string, workspaceID string, workspaceRoot string, sessionID string) remoteAttachment {
	t.Helper()
	return remoteAttachment{session: &remoteSessionAttachment{
		projectID: projectID, workspaceID: workspaceID, workspaceRoot: workspaceRoot, sessionID: sessionID,
	}}
}

func receiveRemoteDescriptorCall(
	t testing.TB,
	ws *websocket.Conn,
	method protoreflect.MethodDescriptor,
	request proto.Message,
) *string {
	t.Helper()
	var encoded []byte
	if err := websocket.Message.Receive(ws, &encoded); err != nil {
		t.Fatalf("receive %s: %v", method.Name(), err)
	}
	envelope, err := protoapi.DecodeEnvelope(encoded)
	if err != nil {
		t.Fatalf("decode %s envelope: %v", method.Name(), err)
	}
	call := envelope.GetCall()
	if call == nil || call.Correlation == nil {
		t.Fatalf("%s call is required", method.Name())
	}
	operation, err := protoapi.OperationFromDescriptor(method)
	if err != nil {
		t.Fatalf("%s operation: %v", method.Name(), err)
	}
	if call.Operation != operation.Name {
		t.Fatalf("operation = %q, want %q", call.Operation, operation.Name)
	}
	if err := protoapi.Decode(call.Payload, request); err != nil {
		t.Fatalf("decode %s request: %v", method.Name(), err)
	}
	return call.Correlation
}

func sendRemoteDescriptorResult(
	t testing.TB,
	ws *websocket.Conn,
	method protoreflect.MethodDescriptor,
	correlation *string,
	result proto.Message,
) {
	t.Helper()
	operation, err := protoapi.OperationFromDescriptor(method)
	if err != nil {
		t.Fatalf("%s operation: %v", method.Name(), err)
	}
	payload, err := protoapi.Encode(result)
	if err != nil {
		t.Fatalf("encode %s result: %v", method.Name(), err)
	}
	encoded, err := protoapi.EncodeEnvelope(&sharedpb.Envelope{
		Frame: &sharedpb.Envelope_Result{Result: &sharedpb.Result{
			Operation: operation.Name, Correlation: correlation, Payload: payload,
		}},
	})
	if err != nil {
		t.Fatalf("encode %s result envelope: %v", method.Name(), err)
	}
	if err := websocket.Message.Send(ws, encoded); err != nil {
		t.Fatalf("send %s result: %v", method.Name(), err)
	}
}

func handleRemoteProjectListFrame(
	ctx context.Context,
	conn rpcwire.Conn,
	frame rpcwire.Frame,
	projectID string,
) (bool, error) {
	if frame.Kind != rpcwire.FrameBinary {
		return false, nil
	}
	envelope, err := protoapi.DecodeEnvelope(frame.Payload)
	if err != nil {
		return false, err
	}
	call := envelope.GetCall()
	if call == nil {
		return false, nil
	}
	method := projectpb.File_kent_api_project_project_proto.Services().
		ByName("ProjectCatalogService").Methods().ByName("List")
	operation, err := protoapi.OperationFromDescriptor(method)
	if err != nil {
		return false, err
	}
	if call.Operation != operation.Name {
		return false, nil
	}
	if err := protoapi.Decode(call.Payload, &emptypb.Empty{}); err != nil {
		return true, err
	}
	resultFrame, err := remoteProjectListResultFrame(projectID, call.Correlation)
	if err != nil {
		return true, err
	}
	return true, conn.Send(ctx, resultFrame)
}

func remoteProjectListResultFrame(projectID string, correlation *string) (rpcwire.Frame, error) {
	method := projectpb.File_kent_api_project_project_proto.Services().
		ByName("ProjectCatalogService").Methods().ByName("List")
	operation, err := protoapi.OperationFromDescriptor(method)
	if err != nil {
		return rpcwire.Frame{}, err
	}
	result := &projectpb.ProjectListResult{
		Outcome: &projectpb.ProjectListResult_Success{Success: &projectpb.ProjectListSuccess{
			Projects: []*projectpb.ProjectSummary{{
				ProjectId: projectID, ProjectKey: "TST", DisplayName: "Test Project",
				RootPath: "/tmp/project", Availability: projectpb.ProjectAvailability_PROJECT_AVAILABILITY_AVAILABLE,
				UpdatedAt: timestamppb.Now(),
			}},
		}},
	}
	payload, err := protoapi.Encode(result)
	if err != nil {
		return rpcwire.Frame{}, err
	}
	encoded, err := protoapi.EncodeEnvelope(&sharedpb.Envelope{
		Frame: &sharedpb.Envelope_Result{Result: &sharedpb.Result{
			Operation: operation.Name, Correlation: correlation, Payload: payload,
		}},
	})
	if err != nil {
		return rpcwire.Frame{}, err
	}
	return rpcwire.Frame{Kind: rpcwire.FrameBinary, Payload: encoded}, nil
}

func sendRemoteDescriptorResultUnchecked(
	t testing.TB,
	ws *websocket.Conn,
	method protoreflect.MethodDescriptor,
	correlation *string,
	result proto.Message,
) {
	t.Helper()
	operation, err := protoapi.OperationFromDescriptor(method)
	if err != nil {
		t.Fatalf("%s operation: %v", method.Name(), err)
	}
	payload, err := proto.Marshal(result)
	if err != nil {
		t.Fatalf("marshal unchecked %s result: %v", method.Name(), err)
	}
	encoded, err := protoapi.EncodeEnvelope(&sharedpb.Envelope{
		Frame: &sharedpb.Envelope_Result{Result: &sharedpb.Result{
			Operation: operation.Name, Correlation: correlation, Payload: payload,
		}},
	})
	if err != nil {
		t.Fatalf("encode unchecked %s result envelope: %v", method.Name(), err)
	}
	if err := websocket.Message.Send(ws, encoded); err != nil {
		t.Fatalf("send unchecked %s result: %v", method.Name(), err)
	}
}

type remoteTestAuthKind uint8

const (
	remoteTestAuthAcknowledge remoteTestAuthKind = iota + 1
	remoteTestAuthComplete
)

type remoteTestAuthResponse struct {
	acknowledge *serverapi.AuthAcknowledgeNoAuthResponse
	complete    *serverapi.AuthCompleteBootstrapResponse
}

func handleRemoteTestAuthFrame(
	ctx context.Context,
	conn rpcwire.Conn,
	frame rpcwire.Frame,
	response remoteTestAuthResponse,
) (remoteTestAuthKind, bool, error) {
	if frame.Kind != rpcwire.FrameBinary {
		return 0, false, nil
	}
	envelope, err := protoapi.DecodeEnvelope(frame.Payload)
	if err != nil {
		return 0, true, err
	}
	call := envelope.GetCall()
	if call == nil || call.Correlation == nil {
		return 0, true, errors.New("correlated auth descriptor call is required")
	}
	service := authpb.File_kent_api_auth_auth_proto.Services().ByName("AuthService")
	for _, candidate := range []struct {
		name protoreflect.Name
		kind remoteTestAuthKind
	}{
		{name: "AcknowledgeNoAuth", kind: remoteTestAuthAcknowledge},
		{name: "CompleteBootstrap", kind: remoteTestAuthComplete},
	} {
		method := service.Methods().ByName(candidate.name)
		operation, operationErr := protoapi.OperationFromDescriptor(method)
		if operationErr != nil {
			return 0, true, operationErr
		}
		if call.Operation != operation.Name {
			continue
		}
		var result proto.Message
		switch candidate.kind {
		case remoteTestAuthAcknowledge:
			if response.acknowledge == nil {
				return 0, true, errors.New("no-auth acknowledgement response is required")
			}
			if err := protoapi.Decode(call.Payload, &emptypb.Empty{}); err != nil {
				return 0, true, err
			}
			success, err := protoapi.AuthNoAuthAcknowledgementToProto(*response.acknowledge)
			if err != nil {
				return 0, true, err
			}
			result = &authpb.AcknowledgeNoAuthResult{
				Outcome: &authpb.AcknowledgeNoAuthResult_Success{Success: success},
			}
		case remoteTestAuthComplete:
			if response.complete == nil {
				return 0, true, errors.New("auth completion response is required")
			}
			if err := protoapi.Decode(call.Payload, &authpb.CompleteBootstrapRequest{}); err != nil {
				return 0, true, err
			}
			success, err := protoapi.AuthBootstrapCompletionToProto(*response.complete)
			if err != nil {
				return 0, true, err
			}
			result = &authpb.CompleteBootstrapResult{
				Outcome: &authpb.CompleteBootstrapResult_Success{Success: success},
			}
		}
		payload, err := protoapi.Encode(result)
		if err != nil {
			return 0, true, err
		}
		encoded, err := protoapi.EncodeEnvelope(&sharedpb.Envelope{
			Frame: &sharedpb.Envelope_Result{Result: &sharedpb.Result{
				Operation: operation.Name, Correlation: call.Correlation, Payload: payload,
			}},
		})
		if err != nil {
			return 0, true, err
		}
		if err := conn.Send(ctx, rpcwire.Frame{Kind: rpcwire.FrameBinary, Payload: encoded}); err != nil {
			return 0, true, err
		}
		return candidate.kind, true, nil
	}
	return 0, false, nil
}

func sendRemoteDescriptorPayload(
	t testing.TB,
	ws *websocket.Conn,
	method protoreflect.MethodDescriptor,
	correlation *string,
	payload []byte,
) {
	t.Helper()
	operation, err := protoapi.OperationFromDescriptor(method)
	if err != nil {
		t.Fatalf("%s operation: %v", method.Name(), err)
	}
	encoded, err := protoapi.EncodeEnvelope(&sharedpb.Envelope{
		Frame: &sharedpb.Envelope_Result{Result: &sharedpb.Result{
			Operation: operation.Name, Correlation: correlation, Payload: payload,
		}},
	})
	if err != nil {
		t.Fatalf("encode %s result envelope: %v", method.Name(), err)
	}
	if err := websocket.Message.Send(ws, encoded); err != nil {
		t.Fatalf("send %s result: %v", method.Name(), err)
	}
}

func acceptRemoteProjectAttachment(
	t testing.TB,
	ws *websocket.Conn,
	workspaceID string,
	workspaceRoot string,
) *connectionpb.AttachProjectRequest {
	t.Helper()
	method := connectionpb.File_kent_api_connection_connection_proto.Services().
		ByName("ConnectionService").Methods().ByName("AttachProject")
	request := &connectionpb.AttachProjectRequest{}
	correlation := receiveRemoteDescriptorCall(t, ws, method, request)
	attachment := &connectionpb.ProjectAttachment{
		ProjectId: request.ProjectId, WorkspaceId: workspaceID, WorkspaceRoot: workspaceRoot,
	}
	switch selection := request.Workspace.(type) {
	case *connectionpb.AttachProjectRequest_WorkspaceId:
		attachment.WorkspaceSelection = &connectionpb.ProjectAttachment_SelectedById{
			SelectedById: &connectionpb.WorkspaceIDSelection{WorkspaceId: selection.WorkspaceId},
		}
	case *connectionpb.AttachProjectRequest_WorkspaceRoot:
		attachment.WorkspaceSelection = &connectionpb.ProjectAttachment_SelectedByRoot{
			SelectedByRoot: &connectionpb.WorkspaceRootSelection{
				RequestedRoot: selection.WorkspaceRoot, CanonicalRoot: workspaceRoot,
			},
		}
	}
	sendRemoteDescriptorResult(t, ws, method, correlation, &connectionpb.AttachProjectResult{
		Outcome: &connectionpb.AttachProjectResult_Success{Success: &connectionpb.AttachmentSuccess{
			Attachment: &connectionpb.AttachmentSuccess_Project{Project: attachment},
		}},
	})
	return request
}

func acceptRemoteSessionAttachment(
	t testing.TB,
	ws *websocket.Conn,
	projectID string,
	workspaceID string,
	workspaceRoot string,
) *connectionpb.AttachSessionRequest {
	t.Helper()
	method := connectionpb.File_kent_api_connection_connection_proto.Services().
		ByName("ConnectionService").Methods().ByName("AttachSession")
	request := &connectionpb.AttachSessionRequest{}
	correlation := receiveRemoteDescriptorCall(t, ws, method, request)
	sendRemoteDescriptorResult(t, ws, method, correlation, &connectionpb.AttachSessionResult{
		Outcome: &connectionpb.AttachSessionResult_Success{Success: &connectionpb.AttachmentSuccess{
			Attachment: &connectionpb.AttachmentSuccess_Session{Session: &connectionpb.SessionAttachment{
				ProjectId: projectID, WorkspaceId: workspaceID, WorkspaceRoot: workspaceRoot, SessionId: request.SessionId,
			}},
		}},
	})
	return request
}

func acceptRemoteSessionAttachmentOrClosed(
	t testing.TB,
	ws *websocket.Conn,
	projectID string,
	workspaceID string,
	workspaceRoot string,
) *connectionpb.AttachSessionRequest {
	t.Helper()
	var encoded []byte
	if err := websocket.Message.Receive(ws, &encoded); err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
		t.Fatalf("receive AttachSession: %v", err)
	}
	method := connectionpb.File_kent_api_connection_connection_proto.Services().
		ByName("ConnectionService").Methods().ByName("AttachSession")
	envelope, err := protoapi.DecodeEnvelope(encoded)
	if err != nil {
		t.Fatalf("decode AttachSession envelope: %v", err)
	}
	call := envelope.GetCall()
	if call == nil || call.Correlation == nil {
		t.Fatal("AttachSession call is required")
	}
	request := &connectionpb.AttachSessionRequest{}
	if err := protoapi.Decode(call.Payload, request); err != nil {
		t.Fatalf("decode AttachSession request: %v", err)
	}
	sendRemoteDescriptorResult(t, ws, method, call.Correlation, &connectionpb.AttachSessionResult{
		Outcome: &connectionpb.AttachSessionResult_Success{Success: &connectionpb.AttachmentSuccess{
			Attachment: &connectionpb.AttachmentSuccess_Session{Session: &connectionpb.SessionAttachment{
				ProjectId: projectID, WorkspaceId: workspaceID, WorkspaceRoot: workspaceRoot, SessionId: request.SessionId,
			}},
		}},
	})
	return request
}

func handleRemoteTestSetupFrame(
	ctx context.Context,
	conn rpcwire.Conn,
	frame rpcwire.Frame,
	response remoteTestSetupResponse,
) (remoteTestSetupKind, bool, error) {
	if frame.Kind != rpcwire.FrameBinary {
		return 0, false, nil
	}
	envelope, err := protoapi.DecodeEnvelope(frame.Payload)
	if err != nil {
		return 0, true, err
	}
	call := envelope.GetCall()
	if call == nil || call.Correlation == nil {
		return 0, true, errors.New("correlated descriptor call is required")
	}
	service := connectionpb.File_kent_api_connection_connection_proto.Services().ByName("ConnectionService")
	for _, candidate := range []struct {
		name protoreflect.Name
		kind remoteTestSetupKind
	}{
		{name: "Handshake", kind: remoteTestSetupHandshake},
		{name: "AttachProject", kind: remoteTestSetupProject},
		{name: "AttachSession", kind: remoteTestSetupSession},
	} {
		method := service.Methods().ByName(candidate.name)
		operation, operationErr := protoapi.OperationFromDescriptor(method)
		if operationErr != nil {
			return 0, true, operationErr
		}
		if call.Operation != operation.Name {
			continue
		}
		var result proto.Message
		switch candidate.kind {
		case remoteTestSetupHandshake:
			result = &connectionpb.HandshakeResult{
				Outcome: &connectionpb.HandshakeResult_Success{Success: &connectionpb.HandshakeSuccess{
					Identity: &connectionpb.ServerIdentity{
						ProtocolVersion: protocol.Version,
						ServerId:        "server-1",
						Pid:             1,
						PersistenceRootId: func() *string {
							if response.rootID == "" {
								return nil
							}
							return &response.rootID
						}(),
					},
				}},
			}
		case remoteTestSetupProject:
			request := &connectionpb.AttachProjectRequest{}
			if err := protoapi.Decode(call.Payload, request); err != nil {
				return 0, true, err
			}
			projectID := response.projectID
			if projectID == "" {
				projectID = request.ProjectId
			}
			attachment := &connectionpb.ProjectAttachment{
				ProjectId: projectID, WorkspaceId: response.workspaceID, WorkspaceRoot: response.workspaceRoot,
			}
			switch selection := request.Workspace.(type) {
			case *connectionpb.AttachProjectRequest_WorkspaceId:
				attachment.WorkspaceSelection = &connectionpb.ProjectAttachment_SelectedById{
					SelectedById: &connectionpb.WorkspaceIDSelection{WorkspaceId: selection.WorkspaceId},
				}
			case *connectionpb.AttachProjectRequest_WorkspaceRoot:
				attachment.WorkspaceSelection = &connectionpb.ProjectAttachment_SelectedByRoot{
					SelectedByRoot: &connectionpb.WorkspaceRootSelection{
						RequestedRoot: selection.WorkspaceRoot, CanonicalRoot: response.workspaceRoot,
					},
				}
			}
			result = &connectionpb.AttachProjectResult{
				Outcome: &connectionpb.AttachProjectResult_Success{Success: &connectionpb.AttachmentSuccess{
					Attachment: &connectionpb.AttachmentSuccess_Project{Project: attachment},
				}},
			}
		case remoteTestSetupSession:
			request := &connectionpb.AttachSessionRequest{}
			if err := protoapi.Decode(call.Payload, request); err != nil {
				return 0, true, err
			}
			result = &connectionpb.AttachSessionResult{
				Outcome: &connectionpb.AttachSessionResult_Success{Success: &connectionpb.AttachmentSuccess{
					Attachment: &connectionpb.AttachmentSuccess_Session{Session: &connectionpb.SessionAttachment{
						ProjectId: response.projectID, WorkspaceId: response.workspaceID,
						WorkspaceRoot: response.workspaceRoot, SessionId: request.SessionId,
					}},
				}},
			}
		}
		payload, err := protoapi.Encode(result)
		if err != nil {
			return 0, true, err
		}
		encoded, err := protoapi.EncodeEnvelope(&sharedpb.Envelope{
			Frame: &sharedpb.Envelope_Result{Result: &sharedpb.Result{
				Operation: operation.Name, Correlation: call.Correlation, Payload: payload,
			}},
		})
		if err != nil {
			return 0, true, err
		}
		if err := conn.Send(ctx, rpcwire.Frame{Kind: rpcwire.FrameBinary, Payload: encoded}); err != nil {
			return 0, true, err
		}
		return candidate.kind, true, nil
	}
	return 0, false, nil
}

func rejectRemoteTestHandshake(
	ctx context.Context,
	conn rpcwire.Conn,
	frame rpcwire.Frame,
	requiredVersion string,
) error {
	envelope, err := protoapi.DecodeEnvelope(frame.Payload)
	if err != nil {
		return err
	}
	call := envelope.GetCall()
	if call == nil || call.Correlation == nil {
		return errors.New("correlated handshake call is required")
	}
	method := connectionpb.File_kent_api_connection_connection_proto.Services().
		ByName("ConnectionService").Methods().ByName("Handshake")
	operation, err := protoapi.OperationFromDescriptor(method)
	if err != nil {
		return err
	}
	if call.Operation != operation.Name {
		return errors.New("client sent application traffic before protocol handshake succeeded")
	}
	request := &connectionpb.HandshakeRequest{}
	if err := protoapi.Decode(call.Payload, request); err != nil {
		return err
	}
	if request.ProtocolVersion != protocol.Version {
		return errors.New("client handshake did not use the current protocol version")
	}
	payload, err := protoapi.Encode(&connectionpb.HandshakeResult{
		Outcome: &connectionpb.HandshakeResult_Error{Error: &connectionpb.HandshakeError{
			Code: "protocol_version_mismatch",
			Detail: &connectionpb.HandshakeError_ProtocolVersionMismatch{
				ProtocolVersionMismatch: &connectionpb.ProtocolVersionMismatchDetails{
					RequiredProtocolVersion: requiredVersion,
				},
			},
		}},
	})
	if err != nil {
		return err
	}
	encoded, err := protoapi.EncodeEnvelope(&sharedpb.Envelope{
		Frame: &sharedpb.Envelope_Result{Result: &sharedpb.Result{
			Operation: operation.Name, Correlation: call.Correlation, Payload: payload,
		}},
	})
	if err != nil {
		return err
	}
	return conn.Send(ctx, rpcwire.Frame{Kind: rpcwire.FrameBinary, Payload: encoded})
}
