package transport

import (
	"context"
	"errors"

	"core/server/auth"
	"core/shared/apicontract"
	"core/shared/protoapi"
	authpb "core/shared/protoapi/gen/kent/api/auth"
	capabilitypb "core/shared/protoapi/gen/kent/api/capability"
	onboardingpb "core/shared/protoapi/gen/kent/api/onboarding"
	serverpb "core/shared/protoapi/gen/kent/api/server"
	sharedpb "core/shared/protoapi/gen/kent/api/shared"
	"core/shared/serverapi"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

func registerBootstrapGatewayBinaryBindings(bindings map[string]gatewayBinaryBinding) error {
	serverService := serverpb.File_kent_api_server_server_proto.Services().ByName("ServerService")
	authService := authpb.File_kent_api_auth_auth_proto.Services().ByName("AuthService")
	onboardingService := onboardingpb.File_kent_api_onboarding_onboarding_proto.Services().ByName("OnboardingService")
	capabilityService := capabilitypb.File_kent_api_capability_capability_proto.Services().ByName("CapabilityService")
	registrations := []struct {
		register func() error
	}{
		{register: func() error {
			return registerGatewayBinaryBinding(bindings, serverService, "GetReadiness", apicontract.DependencyServerStatus,
				func() proto.Message { return &emptypb.Empty{} }, nil, invokeBinaryServerReadiness, binaryServerReadinessFailure)
		}},
		{register: func() error {
			return registerGatewayBinaryBinding(bindings, serverService, "GetUpdateStatus", apicontract.DependencyServerStatus,
				func() proto.Message { return &emptypb.Empty{} }, nil, invokeBinaryUpdateStatus, binaryUpdateStatusFailure)
		}},
		{register: func() error {
			return registerGatewayBinaryBinding(bindings, authService, "GetBootstrapStatus", apicontract.DependencyAuthBootstrap,
				func() proto.Message { return &emptypb.Empty{} }, nil, invokeBinaryAuthBootstrapStatus, binaryAuthBootstrapStatusFailure)
		}},
		{register: func() error {
			return registerGatewayBinaryBinding(bindings, authService, "CompleteBootstrap", apicontract.DependencyAuthBootstrap,
				func() proto.Message { return &authpb.CompleteBootstrapRequest{} }, nil, invokeBinaryAuthCompleteBootstrap, binaryAuthCompleteBootstrapFailure)
		}},
		{register: func() error {
			return registerGatewayBinaryBinding(bindings, authService, "AcknowledgeNoAuth", apicontract.DependencyAuthBootstrap,
				func() proto.Message { return &emptypb.Empty{} }, nil, invokeBinaryAuthAcknowledgeNoAuth, binaryAuthAcknowledgeNoAuthFailure)
		}},
		{register: func() error {
			return registerGatewayBinaryBinding(bindings, authService, "GetStatus", apicontract.DependencyAuthStatus,
				func() proto.Message { return &authpb.GetStatusRequest{} }, nil, invokeBinaryAuthStatus, binaryAuthStatusFailure)
		}},
		{register: func() error {
			return registerGatewayBinaryBinding(bindings, onboardingService, "Finalize", apicontract.DependencyOnboardingFinalize,
				func() proto.Message { return &onboardingpb.FinalizeRequest{} }, nil, invokeBinaryOnboardingFinalize, binaryOnboardingFinalizeFailure)
		}},
		{register: func() error {
			return registerGatewayBinaryBinding(bindings, capabilityService, "GetFacts", apicontract.DependencyCapabilityFacts,
				func() proto.Message { return &capabilitypb.GetFactsRequest{} }, nil, invokeBinaryCapabilityFacts, binaryCapabilityFactsFailure)
		}},
	}
	for _, registration := range registrations {
		if err := registration.register(); err != nil {
			return err
		}
	}
	return nil
}

func invokeBinaryServerReadiness(
	g *Gateway,
	ctx context.Context,
	_ *connectionState,
	_ proto.Message,
) (proto.Message, error) {
	statusClient := g.deps.ServerStatusClient()
	if statusClient == nil {
		return nil, errors.New("server status client is required")
	}
	response, err := statusClient.GetServerReadiness(ctx, serverapi.ServerReadinessRequest{})
	if err != nil {
		return nil, err
	}
	response.ServerID = g.identity.ServerID
	response.ProtocolVersion = g.identity.ProtocolVersion
	success, err := protoapi.ServerReadinessToProto(response)
	if err != nil {
		return nil, err
	}
	return &serverpb.GetReadinessResult{
		Outcome: &serverpb.GetReadinessResult_Success{Success: success},
	}, nil
}

func invokeBinaryUpdateStatus(
	g *Gateway,
	ctx context.Context,
	_ *connectionState,
	_ proto.Message,
) (proto.Message, error) {
	statusClient := g.deps.ServerStatusClient()
	if statusClient == nil {
		return nil, errors.New("server status client is required")
	}
	response, err := statusClient.GetUpdateStatus(ctx, serverapi.UpdateStatusRequest{})
	if err != nil {
		return nil, err
	}
	success, err := protoapi.UpdateStatusToProto(response)
	if err != nil {
		return nil, err
	}
	return &serverpb.GetUpdateStatusResult{
		Outcome: &serverpb.GetUpdateStatusResult_Success{Success: success},
	}, nil
}

func invokeBinaryAuthBootstrapStatus(
	g *Gateway,
	ctx context.Context,
	_ *connectionState,
	_ proto.Message,
) (proto.Message, error) {
	client := g.deps.AuthBootstrapClient()
	if client == nil {
		return nil, serverapi.ErrServerAuthRequired
	}
	response, err := client.GetAuthBootstrapStatus(ctx, serverapi.AuthGetBootstrapStatusRequest{})
	if err != nil {
		return nil, err
	}
	response.AllowedPreAuthMethods = g.registration.AllowedPreAuthMethods()
	success, err := protoapi.AuthBootstrapStatusToProto(response)
	if err != nil {
		return nil, err
	}
	return &authpb.GetBootstrapStatusResult{
		Outcome: &authpb.GetBootstrapStatusResult_Success{Success: success},
	}, nil
}

func invokeBinaryAuthCompleteBootstrap(
	g *Gateway,
	ctx context.Context,
	state *connectionState,
	message proto.Message,
) (proto.Message, error) {
	request, err := protoapi.AuthCompleteBootstrapRequestFromProto(message.(*authpb.CompleteBootstrapRequest))
	if err != nil {
		return nil, err
	}
	client := g.deps.AuthBootstrapClient()
	if client == nil {
		return nil, serverapi.ErrServerAuthRequired
	}
	response, err := client.CompleteAuthBootstrap(ctx, request)
	if err != nil {
		return nil, err
	}
	state.noAuthAccepted = response.NoAuthSelected
	success, err := protoapi.AuthBootstrapCompletionToProto(response)
	if err != nil {
		return nil, err
	}
	return &authpb.CompleteBootstrapResult{
		Outcome: &authpb.CompleteBootstrapResult_Success{Success: success},
	}, nil
}

func invokeBinaryAuthAcknowledgeNoAuth(
	g *Gateway,
	ctx context.Context,
	state *connectionState,
	_ proto.Message,
) (proto.Message, error) {
	client := g.deps.AuthBootstrapClient()
	if client == nil {
		return nil, serverapi.ErrServerAuthRequired
	}
	response, err := client.AcknowledgeNoAuth(ctx, serverapi.AuthAcknowledgeNoAuthRequest{})
	if err != nil {
		return nil, err
	}
	state.noAuthAccepted = response.NoAuthSelected
	success, err := protoapi.AuthNoAuthAcknowledgementToProto(response)
	if err != nil {
		return nil, err
	}
	return &authpb.AcknowledgeNoAuthResult{
		Outcome: &authpb.AcknowledgeNoAuthResult_Success{Success: success},
	}, nil
}

func invokeBinaryAuthStatus(
	g *Gateway,
	ctx context.Context,
	_ *connectionState,
	message proto.Message,
) (proto.Message, error) {
	request, err := protoapi.AuthStatusRequestFromProto(message.(*authpb.GetStatusRequest))
	if err != nil {
		return nil, err
	}
	client := g.deps.AuthStatusClient()
	if client == nil {
		return nil, serverapi.ErrServerAuthRequired
	}
	response, err := client.GetAuthStatus(ctx, request)
	if err != nil {
		return nil, err
	}
	success, err := protoapi.AuthStatusToProto(response)
	if err != nil {
		return nil, err
	}
	return &authpb.GetStatusResult{
		Outcome: &authpb.GetStatusResult_Success{Success: success},
	}, nil
}

func invokeBinaryOnboardingFinalize(
	g *Gateway,
	ctx context.Context,
	_ *connectionState,
	message proto.Message,
) (proto.Message, error) {
	request, err := protoapi.OnboardingFinalizeRequestFromProto(message.(*onboardingpb.FinalizeRequest))
	if err != nil {
		return nil, err
	}
	client := g.deps.OnboardingFinalizeClient()
	if client == nil {
		return nil, serverapi.NewServerNotReadyError(serverapi.ServerNotReadyOnboardingRequired, nil, nil)
	}
	response, err := client.FinalizeOnboarding(ctx, request)
	if err != nil {
		return nil, err
	}
	success, err := protoapi.OnboardingFinalizeSuccessToProto(response)
	if err != nil {
		return nil, err
	}
	return &onboardingpb.FinalizeResult{
		Outcome: &onboardingpb.FinalizeResult_Success{Success: success},
	}, nil
}

func invokeBinaryCapabilityFacts(
	g *Gateway,
	ctx context.Context,
	_ *connectionState,
	message proto.Message,
) (proto.Message, error) {
	request, err := protoapi.CapabilityFactsRequestFromProto(message.(*capabilitypb.GetFactsRequest))
	if err != nil {
		return nil, err
	}
	client := g.deps.CapabilityFactsClient()
	if client == nil {
		return nil, errors.New("capability facts client is required")
	}
	response, err := client.GetCapabilityFacts(ctx, request)
	if err != nil {
		return nil, err
	}
	success, err := protoapi.CapabilityFactsToProto(response)
	if err != nil {
		return nil, err
	}
	return &capabilitypb.GetFactsResult{
		Outcome: &capabilitypb.GetFactsResult_Success{Success: success},
	}, nil
}

func binaryServerReadinessFailure(err error) proto.Message {
	return &serverpb.GetReadinessResult{
		Outcome: &serverpb.GetReadinessResult_Error{Error: &serverpb.GetReadinessError{
			Code: "internal_failure",
			Detail: &serverpb.GetReadinessError_InternalFailure{
				InternalFailure: binaryInternalFailure(err),
			},
		}},
	}
}

func binaryUpdateStatusFailure(err error) proto.Message {
	failure := &serverpb.GetUpdateStatusError{}
	var notReady *serverapi.ServerNotReadyError
	if errors.As(err, &notReady) {
		details, conversionErr := protoapi.ServerNotReadyToProto(notReady)
		if conversionErr == nil {
			failure.Code = "server_not_ready"
			failure.Detail = &serverpb.GetUpdateStatusError_ServerNotReady{ServerNotReady: details}
		}
	}
	if failure.Detail == nil {
		failure.Code = "internal_failure"
		failure.Detail = &serverpb.GetUpdateStatusError_InternalFailure{InternalFailure: binaryInternalFailure(err)}
	}
	return &serverpb.GetUpdateStatusResult{
		Outcome: &serverpb.GetUpdateStatusResult_Error{Error: failure},
	}
}

func binaryAuthBootstrapStatusFailure(err error) proto.Message {
	failure := &authpb.GetBootstrapStatusError{}
	mapAuthFailure(err, func(code string, detail proto.Message) {
		failure.Code = code
		switch value := detail.(type) {
		case *authpb.AuthRequiredDetails:
			failure.Detail = &authpb.GetBootstrapStatusError_AuthRequired{AuthRequired: value}
		case *sharedpb.InternalFailureDetails:
			failure.Detail = &authpb.GetBootstrapStatusError_InternalFailure{InternalFailure: value}
		}
	})
	return &authpb.GetBootstrapStatusResult{
		Outcome: &authpb.GetBootstrapStatusResult_Error{Error: failure},
	}
}

func binaryAuthCompleteBootstrapFailure(err error) proto.Message {
	failure := &authpb.CompleteBootstrapError{}
	mapAuthFailure(err, func(code string, detail proto.Message) {
		failure.Code = code
		switch value := detail.(type) {
		case *authpb.AuthRequiredDetails:
			failure.Detail = &authpb.CompleteBootstrapError_AuthRequired{AuthRequired: value}
		case *sharedpb.InternalFailureDetails:
			failure.Detail = &authpb.CompleteBootstrapError_InternalFailure{InternalFailure: value}
		}
	})
	return &authpb.CompleteBootstrapResult{
		Outcome: &authpb.CompleteBootstrapResult_Error{Error: failure},
	}
}

func binaryAuthAcknowledgeNoAuthFailure(err error) proto.Message {
	failure := &authpb.AcknowledgeNoAuthError{}
	mapAuthFailure(err, func(code string, detail proto.Message) {
		failure.Code = code
		switch value := detail.(type) {
		case *authpb.AuthRequiredDetails:
			failure.Detail = &authpb.AcknowledgeNoAuthError_AuthRequired{AuthRequired: value}
		case *sharedpb.InternalFailureDetails:
			failure.Detail = &authpb.AcknowledgeNoAuthError_InternalFailure{InternalFailure: value}
		}
	})
	return &authpb.AcknowledgeNoAuthResult{
		Outcome: &authpb.AcknowledgeNoAuthResult_Error{Error: failure},
	}
}

func binaryAuthStatusFailure(err error) proto.Message {
	failure := &authpb.GetStatusError{}
	mapAuthFailure(err, func(code string, detail proto.Message) {
		failure.Code = code
		switch value := detail.(type) {
		case *authpb.AuthRequiredDetails:
			failure.Detail = &authpb.GetStatusError_AuthRequired{AuthRequired: value}
		case *sharedpb.InternalFailureDetails:
			failure.Detail = &authpb.GetStatusError_InternalFailure{InternalFailure: value}
		}
	})
	return &authpb.GetStatusResult{
		Outcome: &authpb.GetStatusResult_Error{Error: failure},
	}
}

func mapAuthFailure(err error, set func(code string, detail proto.Message)) {
	if errors.Is(err, serverapi.ErrServerAuthRequired) || errors.Is(err, auth.ErrAuthNotConfigured) {
		set("auth_required", &authpb.AuthRequiredDetails{})
		return
	}
	set("internal_failure", binaryInternalFailure(err))
}

func binaryOnboardingFinalizeFailure(err error) proto.Message {
	var finalizeErr *serverapi.OnboardingFinalizeError
	if errors.As(err, &finalizeErr) {
		converted, conversionErr := protoapi.OnboardingFinalizeErrorToProto(finalizeErr)
		if conversionErr == nil {
			return &onboardingpb.FinalizeResult{
				Outcome: &onboardingpb.FinalizeResult_Error{Error: converted},
			}
		}
	}
	var notReady *serverapi.ServerNotReadyError
	if errors.As(err, &notReady) {
		details, conversionErr := protoapi.ServerNotReadyToProto(notReady)
		if conversionErr == nil {
			return &onboardingpb.FinalizeResult{
				Outcome: &onboardingpb.FinalizeResult_Error{Error: &onboardingpb.FinalizeError{
					Code:   "server_not_ready",
					Detail: &onboardingpb.FinalizeError_ServerNotReady{ServerNotReady: details},
				}},
			}
		}
	}
	return &onboardingpb.FinalizeResult{
		Outcome: &onboardingpb.FinalizeResult_Error{Error: &onboardingpb.FinalizeError{
			Code:   "internal_failure",
			Detail: &onboardingpb.FinalizeError_InternalFailure{InternalFailure: binaryInternalFailure(err)},
		}},
	}
}

func binaryCapabilityFactsFailure(err error) proto.Message {
	failure := &capabilitypb.GetFactsError{}
	var unsupported *serverapi.UnsupportedProviderError
	if errors.As(err, &unsupported) {
		failure.Code = "unsupported_provider"
		failure.Detail = &capabilitypb.GetFactsError_UnsupportedProvider{
			UnsupportedProvider: &capabilitypb.UnsupportedProviderDetails{ProviderId: unsupported.ProviderID},
		}
	} else {
		failure.Code = "internal_failure"
		failure.Detail = &capabilitypb.GetFactsError_InternalFailure{InternalFailure: binaryInternalFailure(err)}
	}
	return &capabilitypb.GetFactsResult{
		Outcome: &capabilitypb.GetFactsResult_Error{Error: failure},
	}
}
