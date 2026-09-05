package client

import (
	"context"
	"errors"
	"fmt"

	"core/shared/protoapi"
	"core/shared/protocol"
	"core/shared/rpcwire"
	"core/shared/serverapi"

	connectionpb "core/shared/protoapi/gen/kent/api/connection"
	projectpb "core/shared/protoapi/gen/kent/api/project"
	serverpb "core/shared/protoapi/gen/kent/api/server"
	sharedpb "core/shared/protoapi/gen/kent/api/shared"
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
	attachment, err := attachRemoteBinaryRPC(ctx, conn, s.attachmentIntent)
	if err != nil {
		return remoteConnectionState{}, err
	}
	if s.expectation != nil {
		if err := validateReattachedBinding(s.expectation.attachment, attachment); err != nil {
			return remoteConnectionState{}, err
		}
	}
	if s.additionalAttachmentIntent != nil {
		attachment, err = attachRemoteBinaryRPC(ctx, conn, s.additionalAttachmentIntent)
		if err != nil {
			return remoteConnectionState{}, err
		}
	}
	return remoteConnectionState{identity: identity, attachment: attachment}, nil
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
	if failure := result.GetError(); failure != nil {
		return protocol.ServerIdentity{}, fmt.Errorf("handshake: %w", handshakeGeneratedError(failure))
	}
	identity := result.GetSuccess().GetIdentity()
	return protocol.ServerIdentity{
		ProtocolVersion:   identity.GetProtocolVersion(),
		ServerID:          identity.GetServerId(),
		PID:               int(identity.GetPid()),
		PersistenceRootID: identity.GetPersistenceRootId(),
	}, nil
}

type protocolVersionMismatchError struct {
	clientVersion   string
	requiredVersion string
}

func (e *protocolVersionMismatchError) Error() string {
	return fmt.Sprintf(
		"server requires protocol version %q but this client uses %q; update the Kent client and server to the same build",
		e.requiredVersion,
		e.clientVersion,
	)
}

func handshakeGeneratedError(failure *connectionpb.HandshakeError) error {
	switch failure.Code {
	case "protocol_version_mismatch":
		details := failure.GetProtocolVersionMismatch()
		if err := protoapi.Validate(details); err != nil {
			return err
		}
		return &protocolVersionMismatchError{
			clientVersion:   protocol.Version,
			requiredVersion: details.RequiredProtocolVersion,
		}
	case "internal_failure":
		return protoapi.InternalFailureFromProto(failure.GetInternalFailure())
	default:
		return generatedOperationFailure(failure.Code)
	}
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
			&connectionpb.AttachSessionRequest{
				SessionId:          request.sessionID,
				ReattachCapability: request.reattachCapability,
			},
			result,
		); err != nil {
			return nil, err
		}
		if failure := result.GetError(); failure != nil {
			return nil, fmt.Errorf("attach session: %w", attachSessionGeneratedError(failure))
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
	if failure := result.GetError(); failure != nil {
		return nil, fmt.Errorf("attach project: %w", attachProjectGeneratedError(failure))
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

func attachProjectGeneratedError(failure *connectionpb.AttachProjectError) error {
	return connectionAttachmentGeneratedError(
		failure.Code,
		failure.GetProjectNotFound(),
		failure.GetWorkspaceNotRegistered(),
		failure.GetProjectUnavailable(),
		failure.GetServerNotReady(),
		failure.GetInternalFailure(),
	)
}

func attachSessionGeneratedError(failure *connectionpb.AttachSessionError) error {
	switch failure.Code {
	case "project_not_found":
		details := failure.GetProjectNotFound()
		if err := protoapi.Validate(details); err != nil {
			return err
		}
		return fmt.Errorf("%w: session %q", serverapi.ErrProjectNotFound, details.SessionId)
	case "workspace_not_registered":
		details := failure.GetWorkspaceNotRegistered()
		if err := protoapi.Validate(details); err != nil {
			return err
		}
		return fmt.Errorf("%w: session %q", serverapi.ErrWorkspaceNotRegistered, details.SessionId)
	case "project_unavailable":
		return projectUnavailableError(failure.GetProjectUnavailable())
	case "server_not_ready":
		return protoapi.ServerNotReadyFromProto(failure.GetServerNotReady())
	case "internal_failure":
		return protoapi.InternalFailureFromProto(failure.GetInternalFailure())
	default:
		return generatedOperationFailure(failure.Code)
	}
}

func connectionAttachmentGeneratedError(
	code string,
	notFound *projectpb.ProjectNotFoundDetails,
	notRegistered *projectpb.WorkspaceNotRegisteredDetails,
	unavailable *projectpb.ProjectUnavailableDetails,
	notReady *serverpb.ServerNotReadyDetails,
	internal *sharedpb.InternalFailureDetails,
) error {
	switch code {
	case "project_not_found":
		return projectNotFoundError(notFound)
	case "workspace_not_registered":
		return workspaceNotRegisteredError(notRegistered)
	case "project_unavailable":
		return projectUnavailableError(unavailable)
	case "server_not_ready":
		return protoapi.ServerNotReadyFromProto(notReady)
	case "internal_failure":
		return protoapi.InternalFailureFromProto(internal)
	default:
		return generatedOperationFailure(code)
	}
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
			projectID:          session.ProjectId,
			workspaceID:        session.WorkspaceId,
			workspaceRoot:      session.WorkspaceRoot,
			sessionID:          session.SessionId,
			reattachCapability: session.ReattachCapability,
		}}, nil
	}
	return remoteAttachment{}, fmt.Errorf("attachment success has no attachment")
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
