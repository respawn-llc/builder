package client

import (
	"context"

	servicecontract "core/shared/apicontract"
	"core/shared/protocol"
	"core/shared/serverapi"
)

type OnboardingFinalizeClient = servicecontract.OnboardingFinalizeService

type loopbackOnboardingFinalizeClient struct {
	loopbackClient[servicecontract.OnboardingFinalizeService]
}

func NewLoopbackOnboardingFinalizeClient(service servicecontract.OnboardingFinalizeService) OnboardingFinalizeClient {
	return &loopbackOnboardingFinalizeClient{loopbackClient: newLoopbackClient(service)}
}

func (c *loopbackOnboardingFinalizeClient) FinalizeOnboarding(ctx context.Context, req serverapi.OnboardingFinalizeRequest) (serverapi.OnboardingFinalizeResponse, error) {
	return callLoopbackClient(c, "onboarding finalize service is required", ctx, req, servicecontract.OnboardingFinalizeService.FinalizeOnboarding)
}

func (c *Remote) FinalizeOnboarding(ctx context.Context, req serverapi.OnboardingFinalizeRequest) (serverapi.OnboardingFinalizeResponse, error) {
	return callUnscopedRPC[serverapi.OnboardingFinalizeRequest, serverapi.OnboardingFinalizeResponse](c, ctx, protocol.MethodOnboardingFinalize, req)
}
