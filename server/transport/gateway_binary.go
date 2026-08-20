package transport

import (
	"context"
	"errors"
	"fmt"

	"core/shared/apicontract"
	"core/shared/invariant"
	"core/shared/protoapi"
	"core/shared/protocol"
	"core/shared/rpcwire"
	"core/shared/runtimeids"

	connectionpb "core/shared/protoapi/gen/kent/api/connection"
	sharedpb "core/shared/protoapi/gen/kent/api/shared"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

type gatewayBinaryBinding struct {
	operation  protoapi.Operation
	dependency apicontract.Dependency
	request    func() proto.Message
	scope      func(proto.Message) (routeScopeParams, error)
	invoke     func(*Gateway, context.Context, *connectionState, proto.Message) (proto.Message, error)
	failure    func(*Gateway, *connectionState, proto.Message, error) proto.Message
}

type gatewayBinaryRequest struct {
	binding gatewayBinaryBinding
	call    *sharedpb.Call
}

func productionGatewayBinaryBindings() (map[string]gatewayBinaryBinding, error) {
	service := connectionpb.File_kent_api_connection_connection_proto.Services().ByName("ConnectionService")
	if service == nil {
		return nil, fmt.Errorf("generated ConnectionService descriptor is required")
	}
	bindings := make(map[string]gatewayBinaryBinding, 11)
	if err := registerGatewayBinaryBinding(
		bindings,
		service,
		"Handshake",
		apicontract.DependencyProtocol,
		func() proto.Message { return &connectionpb.HandshakeRequest{} },
		nil,
		invokeBinaryHandshake,
		binaryHandshakeFailure,
	); err != nil {
		return nil, err
	}
	if err := registerGatewayBinaryBinding(
		bindings,
		service,
		"AttachProject",
		apicontract.DependencyProtocolAttach,
		func() proto.Message { return &connectionpb.AttachProjectRequest{} },
		nil,
		invokeBinaryAttachProject,
		binaryAttachProjectFailure,
	); err != nil {
		return nil, err
	}
	if err := registerGatewayBinaryBinding(
		bindings,
		service,
		"AttachSession",
		apicontract.DependencyProtocolAttach,
		func() proto.Message { return &connectionpb.AttachSessionRequest{} },
		func(message proto.Message) (routeScopeParams, error) {
			request, ok := message.(*connectionpb.AttachSessionRequest)
			if !ok {
				return routeScopeParams{}, fmt.Errorf("AttachSession request type is invalid")
			}
			return routeScopeParams{sessionID: request.SessionId}, nil
		},
		invokeBinaryAttachSession,
		binaryAttachSessionFailure,
	); err != nil {
		return nil, err
	}
	if err := registerBootstrapGatewayBinaryBindings(bindings); err != nil {
		return nil, err
	}
	if err := registerProjectReadGatewayBinaryBindings(bindings); err != nil {
		return nil, err
	}
	if err := registerProjectMutationGatewayBinaryBindings(bindings); err != nil {
		return nil, err
	}
	if err := registerSessionCatalogGatewayBinaryBindings(bindings); err != nil {
		return nil, err
	}
	if err := registerSessionLaunchGatewayBinaryBindings(bindings); err != nil {
		return nil, err
	}
	return bindings, nil
}

func registerGatewayBinaryBinding(
	bindings map[string]gatewayBinaryBinding,
	service protoreflect.ServiceDescriptor,
	methodName protoreflect.Name,
	dependency apicontract.Dependency,
	request func() proto.Message,
	scope func(proto.Message) (routeScopeParams, error),
	invoke func(*Gateway, context.Context, *connectionState, proto.Message) (proto.Message, error),
	failure func(error) proto.Message,
) error {
	if service == nil {
		return fmt.Errorf("generated service descriptor is required")
	}
	method := service.Methods().ByName(methodName)
	if method == nil {
		return fmt.Errorf("generated %s.%s descriptor is required", service.Name(), methodName)
	}
	operation, err := protoapi.OperationFromDescriptor(method)
	if err != nil {
		return err
	}
	bindings[operation.Name] = gatewayBinaryBinding{
		operation:  operation,
		dependency: dependency,
		request:    request,
		scope:      scope,
		invoke:     invoke,
		failure: func(_ *Gateway, _ *connectionState, _ proto.Message, err error) proto.Message {
			return failure(err)
		},
	}
	return nil
}

type protocolVersionMismatchError struct {
	required string
}

func (e protocolVersionMismatchError) Error() string {
	return fmt.Sprintf("unsupported protocol version; server requires %q", e.required)
}

func invokeBinaryHandshake(
	g *Gateway,
	_ context.Context,
	state *connectionState,
	message proto.Message,
) (proto.Message, error) {
	request := message.(*connectionpb.HandshakeRequest)
	if request.ProtocolVersion != protocol.Version {
		return nil, protocolVersionMismatchError{required: protocol.Version}
	}
	state.handshakeDone = true
	identity := &connectionpb.ServerIdentity{
		ProtocolVersion: g.identity.ProtocolVersion,
		ServerId:        g.identity.ServerID,
		Pid:             int32(g.identity.PID),
	}
	if g.identity.PersistenceRootID != "" {
		identity.PersistenceRootId = &g.identity.PersistenceRootID
	}
	return &connectionpb.HandshakeResult{
		Outcome: &connectionpb.HandshakeResult_Success{
			Success: &connectionpb.HandshakeSuccess{Identity: identity},
		},
	}, nil
}

func invokeBinaryAttachProject(
	g *Gateway,
	ctx context.Context,
	state *connectionState,
	message proto.Message,
) (proto.Message, error) {
	request := message.(*connectionpb.AttachProjectRequest)
	if err := g.deps.ProjectExists(ctx, request.ProjectId); err != nil {
		return nil, err
	}
	workspaceID, workspaceRoot, err := g.resolveAttachedProjectWorkspace(ctx, request)
	if err != nil {
		return nil, err
	}
	state.attachedProject = request.ProjectId
	state.attachedWorkspaceID = workspaceID
	state.attachedWorkspaceRoot = workspaceRoot
	state.attachedSession = nil
	attachment := &connectionpb.ProjectAttachment{
		ProjectId:     request.ProjectId,
		WorkspaceId:   workspaceID,
		WorkspaceRoot: workspaceRoot,
	}
	switch selection := request.GetWorkspace().(type) {
	case *connectionpb.AttachProjectRequest_WorkspaceId:
		attachment.WorkspaceSelection = &connectionpb.ProjectAttachment_SelectedById{
			SelectedById: &connectionpb.WorkspaceIDSelection{WorkspaceId: selection.WorkspaceId},
		}
	case *connectionpb.AttachProjectRequest_WorkspaceRoot:
		attachment.WorkspaceSelection = &connectionpb.ProjectAttachment_SelectedByRoot{
			SelectedByRoot: &connectionpb.WorkspaceRootSelection{
				RequestedRoot: selection.WorkspaceRoot,
				CanonicalRoot: workspaceRoot,
			},
		}
	}
	return &connectionpb.AttachProjectResult{
		Outcome: &connectionpb.AttachProjectResult_Success{
			Success: &connectionpb.AttachmentSuccess{
				Attachment: &connectionpb.AttachmentSuccess_Project{Project: attachment},
			},
		},
	}, nil
}

func invokeBinaryAttachSession(
	g *Gateway,
	ctx context.Context,
	state *connectionState,
	message proto.Message,
) (proto.Message, error) {
	request := message.(*connectionpb.AttachSessionRequest)
	binding, err := g.resolveSessionAttachment(ctx, state, request.SessionId)
	if err != nil {
		return nil, err
	}
	parsedSessionID, err := runtimeids.ParseSessionID(request.SessionId)
	if err != nil {
		return nil, err
	}
	state.attachedProject = binding.ProjectID
	state.attachedWorkspaceID = binding.WorkspaceID
	state.attachedWorkspaceRoot = binding.CanonicalRoot
	state.attachedSession = &parsedSessionID
	return &connectionpb.AttachSessionResult{
		Outcome: &connectionpb.AttachSessionResult_Success{
			Success: &connectionpb.AttachmentSuccess{
				Attachment: &connectionpb.AttachmentSuccess_Session{
					Session: &connectionpb.SessionAttachment{
						ProjectId:     binding.ProjectID,
						WorkspaceId:   binding.WorkspaceID,
						WorkspaceRoot: binding.CanonicalRoot,
						SessionId:     request.SessionId,
					},
				},
			},
		},
	}, nil
}

func binaryHandshakeFailure(err error) proto.Message {
	var mismatch protocolVersionMismatchError
	if errors.As(err, &mismatch) {
		return &connectionpb.HandshakeResult{
			Outcome: &connectionpb.HandshakeResult_Error{
				Error: &connectionpb.HandshakeError{
					Code: "protocol_version_mismatch",
					Detail: &connectionpb.HandshakeError_ProtocolVersionMismatch{
						ProtocolVersionMismatch: &connectionpb.ProtocolVersionMismatchDetails{
							RequiredProtocolVersion: mismatch.required,
						},
					},
				},
			},
		}
	}
	return &connectionpb.HandshakeResult{
		Outcome: &connectionpb.HandshakeResult_Error{
			Error: &connectionpb.HandshakeError{
				Code: "internal_failure",
				Detail: &connectionpb.HandshakeError_InternalFailure{
					InternalFailure: binaryInternalFailure(err),
				},
			},
		},
	}
}

func binaryAttachProjectFailure(err error) proto.Message {
	return &connectionpb.AttachProjectResult{
		Outcome: &connectionpb.AttachProjectResult_Error{
			Error: &connectionpb.AttachProjectError{
				Code: "internal_failure",
				Detail: &connectionpb.AttachProjectError_InternalFailure{
					InternalFailure: binaryInternalFailure(err),
				},
			},
		},
	}
}

func binaryAttachSessionFailure(err error) proto.Message {
	return &connectionpb.AttachSessionResult{
		Outcome: &connectionpb.AttachSessionResult_Error{
			Error: &connectionpb.AttachSessionError{
				Code: "internal_failure",
				Detail: &connectionpb.AttachSessionError_InternalFailure{
					InternalFailure: binaryInternalFailure(err),
				},
			},
		},
	}
}

func binaryInternalFailure(err error) *sharedpb.InternalFailureDetails {
	cause := err.Error()
	return &sharedpb.InternalFailureDetails{Cause: &cause}
}

func (g *Gateway) resolveBinaryRequest(encoded []byte) (*gatewayBinaryRequest, *sharedpb.TransportFailure) {
	envelope, err := protoapi.DecodeEnvelope(encoded)
	if err != nil {
		return nil, &sharedpb.TransportFailure{
			Code: sharedpb.TransportFailureCode_TRANSPORT_FAILURE_CODE_MALFORMED_ENVELOPE,
		}
	}
	call := envelope.GetCall()
	if call == nil {
		return nil, &sharedpb.TransportFailure{
			Code:        sharedpb.TransportFailureCode_TRANSPORT_FAILURE_CODE_WRONG_DIRECTION,
			Correlation: envelopeCorrelation(envelope),
		}
	}
	binding, exists := g.registration.BinaryBinding(call.Operation)
	if !exists {
		return nil, &sharedpb.TransportFailure{
			Code:        sharedpb.TransportFailureCode_TRANSPORT_FAILURE_CODE_UNKNOWN_OPERATION,
			Correlation: call.Correlation,
		}
	}
	if binding.operation.Options.Direction != sharedpb.Direction_DIRECTION_CLIENT_TO_SERVER {
		return nil, &sharedpb.TransportFailure{
			Code:        sharedpb.TransportFailureCode_TRANSPORT_FAILURE_CODE_WRONG_DIRECTION,
			Correlation: call.Correlation,
		}
	}
	return &gatewayBinaryRequest{binding: binding, call: call}, nil
}

func envelopeCorrelation(envelope *sharedpb.Envelope) *string {
	switch frame := envelope.GetFrame().(type) {
	case *sharedpb.Envelope_Call:
		return frame.Call.Correlation
	case *sharedpb.Envelope_Result:
		return frame.Result.Correlation
	case *sharedpb.Envelope_TransportFailure:
		return frame.TransportFailure.Correlation
	default:
		return nil
	}
}

func sendTransportFailure(ctx context.Context, conn rpcwire.Conn, failure *sharedpb.TransportFailure) bool {
	encoded, err := protoapi.EncodeEnvelope(&sharedpb.Envelope{
		Frame: &sharedpb.Envelope_TransportFailure{TransportFailure: failure},
	})
	if err != nil {
		return false
	}
	return conn.Send(ctx, rpcwire.Frame{Kind: rpcwire.FrameBinary, Payload: encoded}) == nil
}

func isGatewayExclusiveBinaryBinding(binding gatewayBinaryBinding) bool {
	switch binding.dependency {
	case apicontract.DependencyAuthBootstrap, apicontract.DependencyAuthStatus:
		return true
	}
	switch binding.operation.Options.ScopePolicy {
	case sharedpb.ScopePolicy_SCOPE_POLICY_ATTACH_PROJECT,
		sharedpb.ScopePolicy_SCOPE_POLICY_ATTACH_SESSION:
		return true
	default:
		return false
	}
}

func (g *Gateway) serveBinaryRequest(
	conn rpcwire.Conn,
	ctx context.Context,
	state *connectionState,
	request gatewayBinaryRequest,
) bool {
	result, transportFailure := g.dispatchBinary(ctx, state, request)
	if transportFailure != nil {
		return sendTransportFailure(ctx, conn, transportFailure)
	}
	encoded, err := encodeBinaryResult(request, result)
	if err != nil {
		invariant.NewPolicy().Check(false, invariant.FailureDiagnostic(
			invariant.ScopeServerAPIContract,
			request.binding.operation.Name,
			err,
		))
		result = request.binding.failure(g, state, nil, fmt.Errorf("encode operation result: %w", err))
		encoded, err = encodeBinaryResult(request, result)
		if err != nil {
			return false
		}
	}
	return conn.Send(ctx, rpcwire.Frame{Kind: rpcwire.FrameBinary, Payload: encoded}) == nil
}

func (g *Gateway) dispatchBinary(
	ctx context.Context,
	state *connectionState,
	request gatewayBinaryRequest,
) (proto.Message, *sharedpb.TransportFailure) {
	binding := request.binding
	var decoded proto.Message
	fail := func(err error) (proto.Message, *sharedpb.TransportFailure) {
		return binding.failure(g, state, decoded, err), nil
	}
	if availability, ok := g.deps.(GatewayDependencyAvailability); ok {
		if err := availability.RouteDependencyAvailable(binding.dependency); err != nil {
			return fail(err)
		}
	}
	if err := newRoutePolicyExecutor(g).requireAuthenticationStage(
		ctx,
		state,
		binding.operation.Options.AuthenticationStage,
	); err != nil {
		return fail(err)
	}
	payloadField := request.call.ProtoReflect().Descriptor().Fields().ByName("payload")
	if !request.call.ProtoReflect().Has(payloadField) {
		return nil, invalidPayloadFailure(request.call.Correlation)
	}
	message := binding.request()
	if err := protoapi.Decode(request.call.Payload, message); err != nil {
		return nil, invalidPayloadFailure(request.call.Correlation)
	}
	decoded = message
	var scopeFacts routeScopeParams
	if binding.scope != nil {
		facts, err := binding.scope(message)
		if err != nil {
			return fail(err)
		}
		scopeFacts = facts
	}
	if err := newRoutePolicyExecutor(g).authorizeScopeFacts(
		ctx,
		state,
		routeScopePolicy(binding.operation.Options.ScopePolicy),
		binding.operation.Name,
		scopeFacts,
	); err != nil {
		return fail(err)
	}
	result, err := binding.invoke(g, ctx, state, message)
	if err != nil {
		return fail(err)
	}
	return result, nil
}

func invalidPayloadFailure(correlation *string) *sharedpb.TransportFailure {
	return &sharedpb.TransportFailure{
		Code:        sharedpb.TransportFailureCode_TRANSPORT_FAILURE_CODE_INVALID_PAYLOAD,
		Correlation: correlation,
	}
}

func encodeBinaryResult(request gatewayBinaryRequest, result proto.Message) ([]byte, error) {
	if result == nil {
		return nil, fmt.Errorf("operation result is required")
	}
	if result.ProtoReflect().Descriptor().FullName() != request.binding.operation.Descriptor.Output().FullName() {
		return nil, fmt.Errorf(
			"operation result type %s does not match %s",
			result.ProtoReflect().Descriptor().FullName(),
			request.binding.operation.Descriptor.Output().FullName(),
		)
	}
	payload, err := protoapi.Encode(result)
	if err != nil {
		return nil, err
	}
	presentPayload := make([]byte, len(payload))
	copy(presentPayload, payload)
	return protoapi.EncodeEnvelope(&sharedpb.Envelope{
		Frame: &sharedpb.Envelope_Result{Result: &sharedpb.Result{
			Operation:   request.binding.operation.Name,
			Correlation: request.call.Correlation,
			Payload:     presentPayload,
		}},
	})
}
