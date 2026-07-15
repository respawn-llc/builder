package client

import (
	"context"

	"core/shared/protocol"
	"core/shared/serverapi"
)

func (c *Remote) FinalizeOnboarding(ctx context.Context, req serverapi.OnboardingFinalizeRequest) (serverapi.OnboardingFinalizeResponse, error) {
	return callUnscopedRPC[serverapi.OnboardingFinalizeRequest, serverapi.OnboardingFinalizeResponse](c, ctx, protocol.MethodOnboardingFinalize, req)
}
