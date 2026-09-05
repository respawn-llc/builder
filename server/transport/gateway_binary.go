package transport

import (
	"context"
	"errors"
	"fmt"

	"core/shared/invariant"
	"core/shared/protoapi"
	"core/shared/protocol"
	"core/shared/rpcwire"
	"core/shared/runtimeids"
	"core/shared/serverapi"

	connectionpb "core/shared/protoapi/gen/kent/api/connection"
	projectpb "core/shared/protoapi/gen/kent/api/project"
	serverpb "core/shared/protoapi/gen/kent/api/server"
	sharedpb "core/shared/protoapi/gen/kent/api/shared"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

type gatewayBinaryBinding struct {
	operation         protoapi.Operation
	policy            gatewayBinaryExecutionPolicy
	request           func() proto.Message
	scope             func(proto.Message) (routeScopeParams, error)
	invoke            func(*Gateway, context.Context, *connectionState, proto.Message) (proto.Message, error)
	subscribe         func(*Gateway, context.Context, *connectionState, proto.Message) (gatewayBinarySubscriber, error)
	associated        *protoapi.SubscriptionOperations
	start             proto.Message
	complete          func(error) proto.Message
	failure           func(*Gateway, *connectionState, proto.Message, error) proto.Message
	validationFailure func(proto.Message, error) proto.Message
}

type gatewayBinaryExecutionPolicy uint8

const (
	gatewayBinaryExecutionPolicyUnspecified gatewayBinaryExecutionPolicy = iota
	gatewayBinaryPreCoreOrdinary
	gatewayBinaryPreCoreExclusive
	gatewayBinaryCoreActiveOrdinary
	gatewayBinaryCoreActiveExclusive
)

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
	if err := registerGatewayBinaryUnary(
		bindings,
		service,
		"Handshake",
		gatewayBinaryPreCoreExclusive,
		func() *connectionpb.HandshakeRequest { return &connectionpb.HandshakeRequest{} },
		nil,
		invokeBinaryHandshake,
		binaryHandshakeFailure,
	); err != nil {
		return nil, err
	}
	if err := registerGatewayBinaryUnary(
		bindings,
		service,
		"AttachProject",
		gatewayBinaryCoreActiveExclusive,
		func() *connectionpb.AttachProjectRequest { return &connectionpb.AttachProjectRequest{} },
		nil,
		invokeBinaryAttachProject,
		binaryAttachProjectFailure,
	); err != nil {
		return nil, err
	}
	if err := registerGatewayBinaryUnary(
		bindings,
		service,
		"AttachSession",
		gatewayBinaryCoreActiveExclusive,
		func() *connectionpb.AttachSessionRequest { return &connectionpb.AttachSessionRequest{} },
		func(request *connectionpb.AttachSessionRequest) (routeScopeParams, error) {
			return routeScopeParams{
				sessionID:                 request.SessionId,
				sessionReattachCapability: request.ReattachCapability,
			}, nil
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
	if err := registerSessionRemovalGatewayBinaryBindings(bindings); err != nil {
		return nil, err
	}
	if err := registerChatGatewayBinaryBindings(bindings); err != nil {
		return nil, err
	}
	if err := registerChatSettingsGatewayBinaryBindings(bindings); err != nil {
		return nil, err
	}
	if err := registerWorktreeGatewayBinaryBindings(bindings); err != nil {
		return nil, err
	}
	if err := registerWorktreeSetupGatewayBinaryBinding(bindings); err != nil {
		return nil, err
	}
	return bindings, nil
}

func registerGatewayBinaryUnary[
	Request proto.Message,
	Success proto.Message,
](
	bindings map[string]gatewayBinaryBinding,
	service protoreflect.ServiceDescriptor,
	methodName protoreflect.Name,
	policy gatewayBinaryExecutionPolicy,
	newRequest func() Request,
	scope func(Request) (routeScopeParams, error),
	invoke func(*Gateway, context.Context, *connectionState, Request) (Success, error),
	failureDetail func(*Gateway, *connectionState, Request, error) proto.Message,
	validationFailure ...func(Request, error) proto.Message,
) error {
	if service == nil {
		return fmt.Errorf("generated service descriptor is required")
	}
	if newRequest == nil || invoke == nil || failureDetail == nil {
		return fmt.Errorf("generated %s.%s unary binding is incomplete", service.Name(), methodName)
	}
	method := service.Methods().ByName(methodName)
	if method == nil {
		return fmt.Errorf("generated %s.%s descriptor is required", service.Name(), methodName)
	}
	operation, err := protoapi.OperationFromDescriptor(method)
	if err != nil {
		return err
	}
	binding := gatewayBinaryBinding{
		operation: operation,
		policy:    policy,
		request: func() proto.Message {
			return newRequest()
		},
		invoke: func(
			g *Gateway,
			ctx context.Context,
			state *connectionState,
			message proto.Message,
		) (proto.Message, error) {
			request, ok := message.(Request)
			if !ok {
				return nil, fmt.Errorf("%s request type is invalid", operation.Name)
			}
			response, err := invoke(g, ctx, state, request)
			if err != nil {
				return nil, err
			}
			return protoapi.SuccessResult(method, response)
		},
		failure: func(g *Gateway, state *connectionState, message proto.Message, err error) proto.Message {
			if details, ok := binaryServerNotReadyDetails(err); ok {
				return gatewayBinaryFailureResult(method, details)
			}
			var request Request
			if message != nil {
				typed, ok := message.(Request)
				if !ok {
					return gatewayBinaryFailureResult(
						method,
						failureDetail(g, state, request, fmt.Errorf("%s request type is invalid: %w", operation.Name, err)),
					)
				}
				request = typed
			}
			return gatewayBinaryFailureResult(method, failureDetail(g, state, request, err))
		},
	}
	if len(validationFailure) > 1 {
		return fmt.Errorf("generated %s.%s unary binding has multiple validation failure classifiers", service.Name(), methodName)
	}
	if len(validationFailure) == 1 {
		if validationFailure[0] == nil {
			return fmt.Errorf("generated %s.%s unary binding validation failure classifier is nil", service.Name(), methodName)
		}
		binding.validationFailure = func(message proto.Message, err error) proto.Message {
			request, ok := message.(Request)
			if !ok {
				return nil
			}
			detail := validationFailure[0](request, err)
			if detail == nil {
				return nil
			}
			return gatewayBinaryFailureResult(method, detail)
		}
	}
	bindings[operation.Name] = binding
	if scope != nil {
		binding = bindings[operation.Name]
		binding.scope = func(message proto.Message) (routeScopeParams, error) {
			request, ok := message.(Request)
			if !ok {
				return routeScopeParams{}, fmt.Errorf("%s request type is invalid", operation.Name)
			}
			return scope(request)
		}
		bindings[operation.Name] = binding
	}
	return nil
}

func gatewayBinaryFailureResult(method protoreflect.MethodDescriptor, detail proto.Message) proto.Message {
	result, err := protoapi.FailureResult(method, detail)
	if err == nil {
		return result
	}
	result, _ = protoapi.FailureResult(
		method,
		binaryInternalFailure(fmt.Errorf("encode operation failure: %w", err)),
	)
	return result
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
	request *connectionpb.HandshakeRequest,
) (*connectionpb.HandshakeSuccess, error) {
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
	return &connectionpb.HandshakeSuccess{Identity: identity}, nil
}

func invokeBinaryAttachProject(
	g *Gateway,
	ctx context.Context,
	state *connectionState,
	request *connectionpb.AttachProjectRequest,
) (*connectionpb.AttachmentSuccess, error) {
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
	return &connectionpb.AttachmentSuccess{
		Attachment: &connectionpb.AttachmentSuccess_Project{Project: attachment},
	}, nil
}

func invokeBinaryAttachSession(
	g *Gateway,
	ctx context.Context,
	state *connectionState,
	request *connectionpb.AttachSessionRequest,
) (*connectionpb.AttachmentSuccess, error) {
	_, binding, err := g.resolveSessionAttachmentTargetWithCapability(
		ctx,
		state,
		request.SessionId,
		request.ReattachCapability,
	)
	if err != nil {
		return nil, err
	}
	parsedSessionID, err := runtimeids.ParseSessionID(request.SessionId)
	if err != nil {
		return nil, err
	}
	authority, err := g.sessionReattachAuthority()
	if err != nil {
		return nil, err
	}
	reattachCapability, err := authority.issue(request.SessionId)
	if err != nil {
		return nil, err
	}
	state.attachedProject = binding.ProjectID
	state.attachedWorkspaceID = binding.WorkspaceID
	state.attachedWorkspaceRoot = binding.CanonicalRoot
	state.attachedSession = &parsedSessionID
	return &connectionpb.AttachmentSuccess{
		Attachment: &connectionpb.AttachmentSuccess_Session{
			Session: &connectionpb.SessionAttachment{
				ProjectId:          binding.ProjectID,
				WorkspaceId:        binding.WorkspaceID,
				WorkspaceRoot:      binding.CanonicalRoot,
				SessionId:          request.SessionId,
				ReattachCapability: reattachCapability,
			},
		},
	}, nil
}

func binaryHandshakeFailure(
	_ *Gateway,
	_ *connectionState,
	_ *connectionpb.HandshakeRequest,
	err error,
) proto.Message {
	var mismatch protocolVersionMismatchError
	if errors.As(err, &mismatch) {
		return &connectionpb.ProtocolVersionMismatchDetails{
			RequiredProtocolVersion: mismatch.required,
		}
	}
	return binaryInternalFailure(err)
}

func binaryAttachProjectFailure(
	_ *Gateway,
	_ *connectionState,
	request *connectionpb.AttachProjectRequest,
	err error,
) proto.Message {
	switch {
	case errors.Is(err, serverapi.ErrProjectNotFound) && request != nil:
		return &projectpb.ProjectNotFoundDetails{ProjectId: request.ProjectId}
	case errors.Is(err, serverapi.ErrWorkspaceNotRegistered) && request != nil:
		details := &projectpb.WorkspaceNotRegisteredDetails{ProjectId: proto.String(request.ProjectId)}
		switch workspace := request.GetWorkspace().(type) {
		case *connectionpb.AttachProjectRequest_WorkspaceId:
			details.WorkspaceId = proto.String(workspace.WorkspaceId)
		case *connectionpb.AttachProjectRequest_WorkspaceRoot:
			details.WorkspaceRoot = proto.String(workspace.WorkspaceRoot)
		}
		return details
	default:
		if unavailable, ok := serverapi.AsProjectUnavailable(err); ok {
			if details, conversionErr := protoapi.ProjectUnavailableToProto(unavailable); conversionErr == nil {
				return details
			}
		}
		return binaryInternalFailure(err)
	}
}

func binaryAttachSessionFailure(
	g *Gateway,
	state *connectionState,
	request *connectionpb.AttachSessionRequest,
	err error,
) proto.Message {
	switch {
	case errors.Is(err, serverapi.ErrProjectNotFound) && request != nil:
		details := &connectionpb.SessionAttachmentTargetDetails{SessionId: request.SessionId}
		if projectID := connectionAttachmentProjectID(g, state); projectID != "" {
			details.ProjectId = &projectID
		}
		return &connectionpb.AttachSessionError{
			Code: "project_not_found",
			Detail: &connectionpb.AttachSessionError_ProjectNotFound{
				ProjectNotFound: details,
			},
		}
	case errors.Is(err, serverapi.ErrWorkspaceNotRegistered) && request != nil:
		return &connectionpb.AttachSessionError{
			Code: "workspace_not_registered",
			Detail: &connectionpb.AttachSessionError_WorkspaceNotRegistered{
				WorkspaceNotRegistered: connectionSessionWorkspaceNotRegisteredDetails(
					g, state, request.SessionId, err,
				),
			},
		}
	default:
		if unavailable, ok := serverapi.AsProjectUnavailable(err); ok {
			if details, conversionErr := protoapi.ProjectUnavailableToProto(unavailable); conversionErr == nil {
				return details
			}
		}
		return binaryInternalFailure(err)
	}
}

func connectionAttachmentProjectID(g *Gateway, state *connectionState) string {
	if state != nil && state.attachedProject != "" {
		return state.attachedProject
	}
	if g != nil && g.deps != nil {
		return g.deps.ProjectID()
	}
	return ""
}

func binaryServerNotReadyDetails(err error) (*serverpb.ServerNotReadyDetails, bool) {
	var notReady *serverapi.ServerNotReadyError
	if !errors.As(err, &notReady) {
		return nil, false
	}
	details, conversionErr := protoapi.ServerNotReadyToProto(notReady)
	return details, conversionErr == nil
}

func connectionSessionWorkspaceNotRegisteredDetails(
	g *Gateway,
	state *connectionState,
	sessionID string,
	err error,
) *connectionpb.SessionAttachmentTargetDetails {
	details := &connectionpb.SessionAttachmentTargetDetails{SessionId: sessionID}
	var attachmentErr sessionWorkspaceNotRegisteredError
	if errors.As(err, &attachmentErr) {
		if attachmentErr.projectID != "" {
			details.ProjectId = &attachmentErr.projectID
		}
		if attachmentErr.workspaceID != "" {
			details.WorkspaceId = &attachmentErr.workspaceID
		}
		if attachmentErr.workspaceRoot != "" {
			details.WorkspaceRoot = &attachmentErr.workspaceRoot
		}
		return details
	}
	if projectID := connectionAttachmentProjectID(g, state); projectID != "" {
		details.ProjectId = &projectID
	}
	return details
}

func binaryInternalFailure(err error) *sharedpb.InternalFailureDetails {
	cause := err.Error()
	return &sharedpb.InternalFailureDetails{Cause: &cause}
}

func (g *Gateway) resolveBinaryRequest(
	encoded []byte,
) (*gatewayBinaryRequest, *sharedpb.TransportFailure) {
	envelope, err := protoapi.DecodeEnvelope(encoded)
	if err != nil {
		correlation := protoapi.DecodeEnvelopeCorrelation(encoded)
		var recoveredCorrelation *string
		if correlation != "" {
			recoveredCorrelation = &correlation
		}
		return nil, &sharedpb.TransportFailure{
			Code:        sharedpb.TransportFailureCode_TRANSPORT_FAILURE_CODE_MALFORMED_ENVELOPE,
			Correlation: recoveredCorrelation,
		}
	}
	call := envelope.GetCall()
	if call == nil {
		return nil, &sharedpb.TransportFailure{
			Code:        sharedpb.TransportFailureCode_TRANSPORT_FAILURE_CODE_WRONG_DIRECTION,
			Correlation: envelopeCorrelation(envelope),
		}
	}
	if binding, exists := g.registration.BinaryBinding(call.Operation); exists {
		if binding.operation.Options.Direction != sharedpb.Direction_DIRECTION_CLIENT_TO_SERVER {
			return nil, &sharedpb.TransportFailure{
				Code:        sharedpb.TransportFailureCode_TRANSPORT_FAILURE_CODE_WRONG_DIRECTION,
				Correlation: call.Correlation,
			}
		}
		return &gatewayBinaryRequest{binding: binding, call: call}, nil
	}
	if operation, exists := g.registration.operations[call.Operation]; exists {
		if operation.Options.Direction == sharedpb.Direction_DIRECTION_SERVER_TO_CLIENT {
			return nil, &sharedpb.TransportFailure{
				Code:        sharedpb.TransportFailureCode_TRANSPORT_FAILURE_CODE_WRONG_DIRECTION,
				Correlation: call.Correlation,
			}
		}
	}
	return nil, &sharedpb.TransportFailure{
		Code:        sharedpb.TransportFailureCode_TRANSPORT_FAILURE_CODE_UNKNOWN_OPERATION,
		Correlation: call.Correlation,
	}
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
	switch binding.policy {
	case gatewayBinaryPreCoreExclusive, gatewayBinaryCoreActiveExclusive:
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
	subscription, result, transportFailure := g.dispatchBinary(ctx, state, request)
	if transportFailure != nil {
		return sendTransportFailure(ctx, conn, transportFailure)
	}
	encoded, err := encodeBinaryResult(request.binding.operation, request.call.Correlation, result)
	if err != nil {
		invariant.NewPolicy().Check(false, invariant.FailureDiagnostic(
			invariant.ScopeServerAPIContract,
			request.binding.operation.Name,
			err,
		))
		result = request.binding.failure(g, state, nil, fmt.Errorf("encode operation result: %w", err))
		encoded, err = encodeBinaryResult(request.binding.operation, request.call.Correlation, result)
		if err != nil {
			return false
		}
	}
	if err := conn.Send(ctx, rpcwire.Frame{Kind: rpcwire.FrameBinary, Payload: encoded}); err != nil {
		if subscription != nil {
			_ = subscription.Close()
		}
		return false
	}
	if subscription == nil {
		return true
	}
	defer func() { _ = subscription.Close() }()
	for {
		event, err := subscription.Next(ctx)
		operation := request.binding.associated.Event
		payload := event
		if err != nil {
			operation = request.binding.associated.Completion
			payload = request.binding.complete(err)
		}
		if err := sendBinaryNotification(ctx, conn, operation, payload); err != nil || operation.Name == request.binding.associated.Completion.Name {
			return false
		}
	}
}

func (g *Gateway) dispatchBinary(
	ctx context.Context,
	state *connectionState,
	request gatewayBinaryRequest,
) (gatewayBinarySubscriber, proto.Message, *sharedpb.TransportFailure) {
	binding := request.binding
	var decoded proto.Message
	fail := func(err error) (gatewayBinarySubscriber, proto.Message, *sharedpb.TransportFailure) {
		return nil, binding.failure(g, state, decoded, err), nil
	}
	switch binding.policy {
	case gatewayBinaryCoreActiveOrdinary, gatewayBinaryCoreActiveExclusive:
		if err := g.requireCoreActive(); err != nil {
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
		return nil, nil, invalidPayloadFailure(request.call.Correlation)
	}
	message := binding.request()
	if err := protoapi.Unmarshal(request.call.Payload, message); err != nil {
		return nil, nil, invalidPayloadFailure(request.call.Correlation)
	}
	decoded = message
	if err := protoapi.Validate(message); err != nil {
		if binding.validationFailure != nil {
			if result := binding.validationFailure(message, err); result != nil {
				return nil, result, nil
			}
		}
		return nil, nil, invalidPayloadFailure(request.call.Correlation)
	}
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
	if binding.subscribe != nil {
		subscription, err := binding.subscribe(g, ctx, state, message)
		if err != nil {
			return fail(err)
		}
		return subscription, binding.start, nil
	}
	result, err := binding.invoke(g, ctx, state, message)
	if err != nil {
		return fail(err)
	}
	return nil, result, nil
}

func invalidPayloadFailure(correlation *string) *sharedpb.TransportFailure {
	return &sharedpb.TransportFailure{
		Code:        sharedpb.TransportFailureCode_TRANSPORT_FAILURE_CODE_INVALID_PAYLOAD,
		Correlation: correlation,
	}
}

func encodeBinaryResult(operation protoapi.Operation, correlation *string, result proto.Message) ([]byte, error) {
	if result == nil {
		return nil, fmt.Errorf("operation result is required")
	}
	if result.ProtoReflect().Descriptor().FullName() != operation.Descriptor.Output().FullName() {
		return nil, fmt.Errorf(
			"operation result type %s does not match %s",
			result.ProtoReflect().Descriptor().FullName(),
			operation.Descriptor.Output().FullName(),
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
			Operation:   operation.Name,
			Correlation: correlation,
			Payload:     presentPayload,
		}},
	})
}
