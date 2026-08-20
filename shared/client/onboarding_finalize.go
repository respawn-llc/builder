package client

import (
	"context"

	"core/shared/protoapi"
	onboardingpb "core/shared/protoapi/gen/kent/api/onboarding"
	"core/shared/serverapi"
)

func (c *Remote) FinalizeOnboarding(ctx context.Context, req serverapi.OnboardingFinalizeRequest) (serverapi.OnboardingFinalizeResponse, error) {
	request, err := protoapi.OnboardingFinalizeRequestToProto(req)
	if err != nil {
		return serverapi.OnboardingFinalizeResponse{}, err
	}
	result := &onboardingpb.FinalizeResult{}
	if err := c.callBinary(
		ctx,
		bootstrapMethod(onboardingpb.File_kent_api_onboarding_onboarding_proto, "OnboardingService", "Finalize"),
		request,
		result,
	); err != nil {
		return serverapi.OnboardingFinalizeResponse{}, err
	}
	if failure := result.GetError(); failure != nil {
		return serverapi.OnboardingFinalizeResponse{}, protoapi.OnboardingFinalizeErrorFromProto(failure)
	}
	return protoapi.OnboardingFinalizeSuccessFromProto(result.GetSuccess())
}
