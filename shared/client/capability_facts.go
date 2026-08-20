package client

import (
	"context"

	"core/shared/protoapi"
	capabilitypb "core/shared/protoapi/gen/kent/api/capability"
	"core/shared/serverapi"
)

func (c *Remote) GetFacts(ctx context.Context, req *capabilitypb.GetFactsRequest) (*capabilitypb.Facts, error) {
	return callGeneratedBinary(c, ctx,
		bootstrapMethod(capabilitypb.File_kent_api_capability_capability_proto, "CapabilityService", "GetFacts"),
		req,
		&capabilitypb.GetFactsResult{},
		func(failure *capabilitypb.GetFactsError) error {
			switch failure.Code {
			case "unsupported_provider":
				return &serverapi.UnsupportedProviderError{
					ProviderID: failure.GetUnsupportedProvider().ProviderId,
				}
			case "internal_failure":
				return protoapi.InternalFailureFromProto(failure.GetInternalFailure())
			default:
				return generatedOperationFailure(failure.Code)
			}
		})
}
