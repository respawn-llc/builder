package client

import (
	"context"
	"errors"
	"io"
	"testing"

	"core/shared/protoapi"
	connectionpb "core/shared/protoapi/gen/kent/api/connection"
	projectpb "core/shared/protoapi/gen/kent/api/project"
	sharedpb "core/shared/protoapi/gen/kent/api/shared"
	"core/shared/protocol"
	"core/shared/rpcwire"
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
	frame, err := remoteDescriptorResultFrame(method, correlation, result, protoapi.Encode)
	if err != nil {
		t.Fatalf("encode %s result: %v", method.Name(), err)
	}
	if err := websocket.Message.Send(ws, frame.Payload); err != nil {
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
	result := &projectpb.ProjectListResult{
		Outcome: &projectpb.ProjectListResult_Success{Success: &projectpb.ProjectListSuccess{
			Projects: []*projectpb.ProjectSummary{{
				ProjectId: projectID, ProjectKey: "TST", DisplayName: "Test Project",
				RootPath: "/tmp/project", Availability: projectpb.ProjectAvailability_PROJECT_AVAILABILITY_AVAILABLE,
				UpdatedAt: timestamppb.Now(),
			}},
		}},
	}
	return remoteDescriptorResultFrame(method, correlation, result, protoapi.Encode)
}

func remoteDescriptorResultFrame(
	method protoreflect.MethodDescriptor,
	correlation *string,
	result proto.Message,
	encode func(proto.Message) ([]byte, error),
) (rpcwire.Frame, error) {
	payload, err := encode(result)
	if err != nil {
		return rpcwire.Frame{}, err
	}
	return remoteDescriptorPayloadFrame(method, correlation, payload)
}

func remoteDescriptorPayloadFrame(
	method protoreflect.MethodDescriptor,
	correlation *string,
	payload []byte,
) (rpcwire.Frame, error) {
	operation, err := protoapi.OperationFromDescriptor(method)
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
	sendRemoteDescriptorResult(t, ws, method, correlation, &connectionpb.AttachProjectResult{
		Outcome: &connectionpb.AttachProjectResult_Success{
			Success: remoteTestProjectAttachment(request, request.ProjectId, workspaceID, workspaceRoot),
		},
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
		Outcome: &connectionpb.AttachSessionResult_Success{
			Success: remoteTestSessionAttachment(request, projectID, workspaceID, workspaceRoot),
		},
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
			result = &connectionpb.AttachProjectResult{
				Outcome: &connectionpb.AttachProjectResult_Success{
					Success: remoteTestProjectAttachment(
						request, projectID, response.workspaceID, response.workspaceRoot,
					),
				},
			}
		case remoteTestSetupSession:
			request := &connectionpb.AttachSessionRequest{}
			if err := protoapi.Decode(call.Payload, request); err != nil {
				return 0, true, err
			}
			result = &connectionpb.AttachSessionResult{
				Outcome: &connectionpb.AttachSessionResult_Success{
					Success: remoteTestSessionAttachment(
						request, response.projectID, response.workspaceID, response.workspaceRoot,
					),
				},
			}
		}
		frame, err := remoteDescriptorResultFrame(method, call.Correlation, result, protoapi.Encode)
		if err != nil {
			return 0, true, err
		}
		if err := conn.Send(ctx, frame); err != nil {
			return 0, true, err
		}
		return candidate.kind, true, nil
	}
	return 0, false, nil
}

func remoteTestProjectAttachment(
	request *connectionpb.AttachProjectRequest,
	projectID string,
	workspaceID string,
	workspaceRoot string,
) *connectionpb.AttachmentSuccess {
	attachment := &connectionpb.ProjectAttachment{
		ProjectId: projectID, WorkspaceId: workspaceID, WorkspaceRoot: workspaceRoot,
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
	return &connectionpb.AttachmentSuccess{
		Attachment: &connectionpb.AttachmentSuccess_Project{Project: attachment},
	}
}

func remoteTestSessionAttachment(
	request *connectionpb.AttachSessionRequest,
	projectID string,
	workspaceID string,
	workspaceRoot string,
) *connectionpb.AttachmentSuccess {
	return &connectionpb.AttachmentSuccess{
		Attachment: &connectionpb.AttachmentSuccess_Session{Session: &connectionpb.SessionAttachment{
			ProjectId:          projectID,
			WorkspaceId:        workspaceID,
			WorkspaceRoot:      workspaceRoot,
			SessionId:          request.SessionId,
			ReattachCapability: "test-session-reattach-capability",
		}},
	}
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
	frame, frameErr := remoteDescriptorResultFrame(method, call.Correlation, &connectionpb.HandshakeResult{
		Outcome: &connectionpb.HandshakeResult_Error{Error: &connectionpb.HandshakeError{
			Code: "protocol_version_mismatch",
			Detail: &connectionpb.HandshakeError_ProtocolVersionMismatch{
				ProtocolVersionMismatch: &connectionpb.ProtocolVersionMismatchDetails{
					RequiredProtocolVersion: requiredVersion,
				},
			},
		}},
	}, protoapi.Encode)
	if frameErr != nil {
		return frameErr
	}
	return conn.Send(ctx, frame)
}
