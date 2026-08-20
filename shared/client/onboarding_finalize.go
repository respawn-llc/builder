package client

import (
	"context"

	"core/shared/protoapi"
	onboardingpb "core/shared/protoapi/gen/kent/api/onboarding"
)

func (c *Remote) Finalize(ctx context.Context, req *onboardingpb.FinalizeRequest) (*onboardingpb.FinalizeSuccess, error) {
	return callGeneratedBinary(c, ctx,
		bootstrapMethod(onboardingpb.File_kent_api_onboarding_onboarding_proto, "OnboardingService", "Finalize"),
		req,
		&onboardingpb.FinalizeResult{},
		protoapi.OnboardingFinalizeErrorFromProto)
}
