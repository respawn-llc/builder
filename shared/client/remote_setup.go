package client

import (
	"context"
	"errors"
	"fmt"

	"core/shared/protoapi"
	"core/shared/protocol"
	"core/shared/rpcwire"

	connectionpb "core/shared/protoapi/gen/kent/api/connection"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

type remoteConnectionState struct {
	identity   protocol.ServerIdentity
	attachment *remoteAttachment
}

type remoteConnectionExpectation struct {
	rootID     string
	attachment *remoteAttachment
}

type remoteConnectionSetup struct {
	attachmentIntent           *remoteAttachmentIntent
	additionalAttachmentIntent *remoteAttachmentIntent
	expectation                *remoteConnectionExpectation
	acknowledgeNoAuth          func(context.Context, rpcwire.Conn) error
}

func (s remoteConnectionSetup) run(ctx context.Context, conn rpcwire.Conn) (remoteConnectionState, error) {
	identity, err := handshakeBinaryRPC(ctx, conn)
	if err != nil {
		return remoteConnectionState{}, err
	}
	if s.expectation != nil {
		if err := validateIdentityRoot(s.expectation.rootID, identity); err != nil {
			return remoteConnectionState{}, err
		}
	}
	if s.acknowledgeNoAuth != nil {
		if err := s.acknowledgeNoAuth(ctx, conn); err != nil {
			return remoteConnectionState{}, err
		}
	}
	attachment, err := s.attach(ctx, conn)
	if err != nil {
		return remoteConnectionState{}, err
	}
	if s.expectation != nil {
		if err := validateReattachedBinding(s.expectation.attachment, attachment); err != nil {
			return remoteConnectionState{}, err
		}
	}
	if s.additionalAttachmentIntent != nil {
		if _, err := s.attachIntent(ctx, conn, s.additionalAttachmentIntent); err != nil {
			return remoteConnectionState{}, err
		}
	}
	return remoteConnectionState{identity: identity, attachment: attachment}, nil
}

func (s remoteConnectionSetup) attach(ctx context.Context, conn rpcwire.Conn) (*remoteAttachment, error) {
	return s.attachIntent(ctx, conn, s.attachmentIntent)
}

func (s remoteConnectionSetup) attachIntent(
	ctx context.Context,
	conn rpcwire.Conn,
	intent *remoteAttachmentIntent,
) (*remoteAttachment, error) {
	return attachRemoteBinaryRPC(ctx, conn, intent)
}

func handshakeBinaryRPC(ctx context.Context, conn rpcwire.Conn) (protocol.ServerIdentity, error) {
	method, err := connectionMethod("Handshake")
	if err != nil {
		return protocol.ServerIdentity{}, err
	}
	result := &connectionpb.HandshakeResult{}
	if err := callBinaryRPC(
		ctx,
		conn,
		"handshake",
		method,
		&connectionpb.HandshakeRequest{ProtocolVersion: protocol.Version},
		result,
	); err != nil {
		return protocol.ServerIdentity{}, err
	}
	if err := requireGeneratedSuccess(result); err != nil {
		return protocol.ServerIdentity{}, fmt.Errorf("handshake: %w", err)
	}
	identity := result.GetSuccess().GetIdentity()
	return protocol.ServerIdentity{
		ProtocolVersion:   identity.GetProtocolVersion(),
		ServerID:          identity.GetServerId(),
		PID:               int(identity.GetPid()),
		PersistenceRootID: identity.GetPersistenceRootId(),
	}, nil
}

func attachRemoteBinaryRPC(
	ctx context.Context,
	conn rpcwire.Conn,
	intent *remoteAttachmentIntent,
) (*remoteAttachment, error) {
	if intent == nil {
		return nil, nil
	}
	if request, present := intent.sessionRequest(); present {
		method, err := connectionMethod("AttachSession")
		if err != nil {
			return nil, err
		}
		result := &connectionpb.AttachSessionResult{}
		if err := callBinaryRPC(
			ctx,
			conn,
			"attach-session",
			method,
			&connectionpb.AttachSessionRequest{SessionId: request.sessionID},
			result,
		); err != nil {
			return nil, err
		}
		if err := requireGeneratedSuccess(result); err != nil {
			return nil, fmt.Errorf("attach session: %w", err)
		}
		response, err := attachmentResponseFromGenerated(result.GetSuccess())
		if err != nil {
			return nil, err
		}
		if err := intent.validateResponse(response); err != nil {
			return nil, err
		}
		return &response, nil
	}
	request, present := intent.projectRequest()
	if !present {
		return nil, errors.New("remote attachment intent is invalid")
	}
	generatedRequest := &connectionpb.AttachProjectRequest{ProjectId: request.projectID}
	if request.workspace != nil {
		if request.workspace.workspaceID != nil {
			generatedRequest.Workspace = &connectionpb.AttachProjectRequest_WorkspaceId{WorkspaceId: *request.workspace.workspaceID}
		} else {
			generatedRequest.Workspace = &connectionpb.AttachProjectRequest_WorkspaceRoot{WorkspaceRoot: *request.workspace.workspaceRoot}
		}
	}
	method, err := connectionMethod("AttachProject")
	if err != nil {
		return nil, err
	}
	result := &connectionpb.AttachProjectResult{}
	if err := callBinaryRPC(ctx, conn, "attach-project", method, generatedRequest, result); err != nil {
		return nil, err
	}
	if err := requireGeneratedSuccess(result); err != nil {
		return nil, fmt.Errorf("attach project: %w", err)
	}
	response, err := attachmentResponseFromGenerated(result.GetSuccess())
	if err != nil {
		return nil, err
	}
	if err := intent.validateResponse(response); err != nil {
		return nil, err
	}
	return &response, nil
}

func attachmentResponseFromGenerated(success *connectionpb.AttachmentSuccess) (remoteAttachment, error) {
	if success == nil {
		return remoteAttachment{}, fmt.Errorf("attachment success is required")
	}
	if project := success.GetProject(); project != nil {
		attachment := ProjectAttachment{
			ProjectID:     project.ProjectId,
			WorkspaceID:   project.WorkspaceId,
			WorkspaceRoot: project.WorkspaceRoot,
		}
		switch selection := project.GetWorkspaceSelection().(type) {
		case *connectionpb.ProjectAttachment_SelectedById:
			workspaceID := selection.SelectedById.WorkspaceId
			attachment.selection = &remoteProjectAttachmentSelection{workspaceID: &workspaceID}
		case *connectionpb.ProjectAttachment_SelectedByRoot:
			requestedRoot := selection.SelectedByRoot.RequestedRoot
			canonicalRoot := selection.SelectedByRoot.CanonicalRoot
			attachment.selection = &remoteProjectAttachmentSelection{
				requestedRoot: &requestedRoot,
				canonicalRoot: &canonicalRoot,
			}
		case nil:
		default:
			return remoteAttachment{}, fmt.Errorf("attachment has an unknown project workspace selection")
		}
		return remoteAttachment{project: &attachment}, nil
	}
	if session := success.GetSession(); session != nil {
		return remoteAttachment{session: &remoteSessionAttachment{
			projectID:     session.ProjectId,
			workspaceID:   session.WorkspaceId,
			workspaceRoot: session.WorkspaceRoot,
			sessionID:     session.SessionId,
		}}, nil
	}
	return remoteAttachment{}, fmt.Errorf("attachment success has no attachment")
}

func requireGeneratedSuccess(result proto.Message) error {
	classified, err := protoapi.ClassifyResult(result)
	if err != nil {
		return err
	}
	if classified.Outcome == protoapi.OperationSuccess {
		return nil
	}
	return fmt.Errorf("operation failed with code %q", classified.Failure.Code)
}

func connectionMethod(name protoreflect.Name) (protoreflect.MethodDescriptor, error) {
	service := connectionpb.File_kent_api_connection_connection_proto.Services().ByName("ConnectionService")
	if service == nil {
		return nil, fmt.Errorf("generated ConnectionService descriptor is required")
	}
	method := service.Methods().ByName(name)
	if method == nil {
		return nil, fmt.Errorf("generated ConnectionService.%s descriptor is required", name)
	}
	return method, nil
}
