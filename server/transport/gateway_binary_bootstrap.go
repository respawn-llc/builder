package transport

import (
	"context"
	"errors"

	"core/server/auth"
	"core/shared/protoapi"
	authpb "core/shared/protoapi/gen/kent/api/auth"
	capabilitypb "core/shared/protoapi/gen/kent/api/capability"
	onboardingpb "core/shared/protoapi/gen/kent/api/onboarding"
	serverpb "core/shared/protoapi/gen/kent/api/server"
	"core/shared/serverapi"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/emptypb"
)

func registerBootstrapGatewayBinaryBindings(bindings map[string]gatewayBinaryBinding) error {
	serverService := serverpb.File_kent_api_server_server_proto.Services().ByName("ServerService")
	authService := authpb.File_kent_api_auth_auth_proto.Services().ByName("AuthService")
	onboardingService := onboardingpb.File_kent_api_onboarding_onboarding_proto.Services().ByName("OnboardingService")
	capabilityService := capabilitypb.File_kent_api_capability_capability_proto.Services().ByName("CapabilityService")
	return errors.Join(
		registerBootstrapUnary(bindings, serverService, "GetReadiness", gatewayBinaryPreCoreOrdinary,
			func() *emptypb.Empty { return &emptypb.Empty{} }, invokeBinaryServerReadiness, binaryServerReadinessFailure),
		registerBootstrapUnary(bindings, serverService, "GetUpdateStatus", gatewayBinaryPreCoreOrdinary,
			func() *emptypb.Empty { return &emptypb.Empty{} }, invokeBinaryUpdateStatus, binaryUpdateStatusFailure),
		registerBootstrapUnary(bindings, authService, "GetBootstrapStatus", gatewayBinaryPreCoreExclusive,
			func() *emptypb.Empty { return &emptypb.Empty{} }, invokeBinaryAuthBootstrapStatus, binaryAuthFailure),
		registerBootstrapUnary(bindings, authService, "CompleteBootstrap", gatewayBinaryPreCoreExclusive,
			func() *authpb.CompleteBootstrapRequest { return &authpb.CompleteBootstrapRequest{} }, invokeBinaryAuthCompleteBootstrap, binaryAuthFailure),
		registerBootstrapUnary(bindings, authService, "AcknowledgeNoAuth", gatewayBinaryPreCoreExclusive,
			func() *emptypb.Empty { return &emptypb.Empty{} }, invokeBinaryAuthAcknowledgeNoAuth, binaryAuthFailure),
		registerBootstrapUnary(bindings, authService, "GetStatus", gatewayBinaryPreCoreExclusive,
			func() *authpb.GetStatusRequest { return &authpb.GetStatusRequest{} }, invokeBinaryAuthStatus, binaryAuthFailure),
		registerBootstrapUnary(bindings, onboardingService, "Finalize", gatewayBinaryPreCoreOrdinary,
			func() *onboardingpb.FinalizeRequest { return &onboardingpb.FinalizeRequest{} }, invokeBinaryOnboardingFinalize, binaryOnboardingFinalizeFailure),
		registerBootstrapUnary(bindings, capabilityService, "GetFacts", gatewayBinaryPreCoreOrdinary,
			func() *capabilitypb.GetFactsRequest { return &capabilitypb.GetFactsRequest{} }, invokeBinaryCapabilityFacts, binaryCapabilityFactsFailure),
	)
}

func registerBootstrapUnary[
	Request proto.Message,
	Success proto.Message,
](
	bindings map[string]gatewayBinaryBinding,
	service protoreflect.ServiceDescriptor,
	method protoreflect.Name,
	policy gatewayBinaryExecutionPolicy,
	newRequest func() Request,
	invoke func(*Gateway, context.Context, *connectionState, Request) (Success, error),
	failure func(error) proto.Message,
) error {
	return registerGatewayBinaryUnary(
		bindings, service, method, policy, newRequest, nil, invoke,
		func(_ *Gateway, _ *connectionState, _ Request, err error) proto.Message {
			return failure(err)
		},
	)
}

func invokeBinaryServerReadiness(
	g *Gateway,
	ctx context.Context,
	_ *connectionState,
	message *emptypb.Empty,
) (*serverpb.GetReadinessSuccess, error) {
	statusClient := g.deps.ServerStatusClient()
	if statusClient == nil {
		return nil, errors.New("server status client is required")
	}
	success, err := statusClient.GetReadiness(ctx, message)
	if err != nil {
		return nil, err
	}
	if success.GetReadiness() == nil {
		return nil, errors.New("server readiness is required")
	}
	success.Readiness.ServerId = g.identity.ServerID
	success.Readiness.ProtocolVersion = g.identity.ProtocolVersion
	return success, nil
}

func invokeBinaryUpdateStatus(
	g *Gateway,
	ctx context.Context,
	_ *connectionState,
	message *emptypb.Empty,
) (*serverpb.GetUpdateStatusSuccess, error) {
	statusClient := g.deps.ServerStatusClient()
	if statusClient == nil {
		return nil, errors.New("server status client is required")
	}
	success, err := statusClient.GetUpdateStatus(ctx, message)
	if err != nil {
		return nil, err
	}
	return success, nil
}

func invokeBinaryAuthBootstrapStatus(
	g *Gateway,
	ctx context.Context,
	_ *connectionState,
	message *emptypb.Empty,
) (*authpb.BootstrapStatus, error) {
	client := g.deps.AuthBootstrapClient()
	if client == nil {
		return nil, serverapi.ErrServerAuthRequired
	}
	success, err := client.GetBootstrapStatus(ctx, message)
	if err != nil {
		return nil, err
	}
	success.AllowedPreAuthMethods = g.registration.AllowedPreAuthMethods()
	return success, nil
}

func invokeBinaryAuthCompleteBootstrap(
	g *Gateway,
	ctx context.Context,
	state *connectionState,
	message *authpb.CompleteBootstrapRequest,
) (*authpb.BootstrapCompletion, error) {
	client := g.deps.AuthBootstrapClient()
	if client == nil {
		return nil, serverapi.ErrServerAuthRequired
	}
	success, err := client.CompleteBootstrap(ctx, message)
	if err != nil {
		return nil, err
	}
	state.noAuthAccepted = success.GetNoAuthSelected()
	return success, nil
}

func invokeBinaryAuthAcknowledgeNoAuth(
	g *Gateway,
	ctx context.Context,
	state *connectionState,
	message *emptypb.Empty,
) (*authpb.NoAuthAcknowledgement, error) {
	client := g.deps.AuthBootstrapClient()
	if client == nil {
		return nil, serverapi.ErrServerAuthRequired
	}
	success, err := client.AcknowledgeNoAuth(ctx, message)
	if err != nil {
		return nil, err
	}
	state.noAuthAccepted = success.GetNoAuthSelected()
	return success, nil
}

func invokeBinaryAuthStatus(
	g *Gateway,
	ctx context.Context,
	_ *connectionState,
	message *authpb.GetStatusRequest,
) (*authpb.Status, error) {
	client := g.deps.AuthStatusClient()
	if client == nil {
		return nil, serverapi.ErrServerAuthRequired
	}
	success, err := client.GetStatus(ctx, message)
	if err != nil {
		return nil, err
	}
	return success, nil
}

func invokeBinaryOnboardingFinalize(
	g *Gateway,
	ctx context.Context,
	_ *connectionState,
	message *onboardingpb.FinalizeRequest,
) (*onboardingpb.FinalizeSuccess, error) {
	client := g.deps.OnboardingFinalizeClient()
	if client == nil {
		return nil, serverapi.NewServerNotReadyError(serverapi.ServerNotReadyOnboardingRequired, nil, nil)
	}
	success, err := client.Finalize(ctx, message)
	if err != nil {
		return nil, err
	}
	return success, nil
}

func invokeBinaryCapabilityFacts(
	g *Gateway,
	ctx context.Context,
	_ *connectionState,
	message *capabilitypb.GetFactsRequest,
) (*capabilitypb.Facts, error) {
	client := g.deps.CapabilityFactsClient()
	if client == nil {
		return nil, errors.New("capability facts client is required")
	}
	success, err := client.GetFacts(ctx, message)
	if err != nil {
		return nil, err
	}
	return success, nil
}

func binaryServerReadinessFailure(err error) proto.Message {
	return binaryInternalFailure(err)
}

func binaryUpdateStatusFailure(err error) proto.Message {
	var notReady *serverapi.ServerNotReadyError
	if errors.As(err, &notReady) {
		details, conversionErr := protoapi.ServerNotReadyToProto(notReady)
		if conversionErr == nil {
			return details
		}
	}
	return binaryInternalFailure(err)
}

func binaryAuthFailure(err error) proto.Message {
	if errors.Is(err, serverapi.ErrServerAuthRequired) || errors.Is(err, auth.ErrAuthNotConfigured) {
		return &authpb.AuthRequiredDetails{}
	}
	return binaryInternalFailure(err)
}

func binaryOnboardingFinalizeFailure(err error) proto.Message {
	var finalizeErr *serverapi.OnboardingFinalizeError
	if errors.As(err, &finalizeErr) {
		converted, conversionErr := protoapi.OnboardingFinalizeErrorToProto(finalizeErr)
		if conversionErr == nil {
			return converted
		}
	}
	var notReady *serverapi.ServerNotReadyError
	if errors.As(err, &notReady) {
		details, conversionErr := protoapi.ServerNotReadyToProto(notReady)
		if conversionErr == nil {
			return details
		}
	}
	return binaryInternalFailure(err)
}

func binaryCapabilityFactsFailure(err error) proto.Message {
	var unsupported *serverapi.UnsupportedProviderError
	if errors.As(err, &unsupported) {
		return &capabilitypb.UnsupportedProviderDetails{ProviderId: unsupported.ProviderID}
	}
	return binaryInternalFailure(err)
}
