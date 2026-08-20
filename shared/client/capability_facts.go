package client

import (
	"context"

	"core/shared/protoapi"
	capabilitypb "core/shared/protoapi/gen/kent/api/capability"
	"core/shared/serverapi"
)

func (c *Remote) GetCapabilityFacts(ctx context.Context, req serverapi.CapabilityFactsRequest) (serverapi.CapabilityFactsResponse, error) {
	request, err := protoapi.CapabilityFactsRequestToProto(req)
	if err != nil {
		return serverapi.CapabilityFactsResponse{}, err
	}
	result := &capabilitypb.GetFactsResult{}
	if err := c.callBinary(
		ctx,
		bootstrapMethod(capabilitypb.File_kent_api_capability_capability_proto, "CapabilityService", "GetFacts"),
		request,
		result,
	); err != nil {
		return serverapi.CapabilityFactsResponse{}, err
	}
	if failure := result.GetError(); failure != nil {
		switch failure.Code {
		case "unsupported_provider":
			return serverapi.CapabilityFactsResponse{}, &serverapi.UnsupportedProviderError{
				ProviderID: failure.GetUnsupportedProvider().ProviderId,
			}
		case "internal_failure":
			return serverapi.CapabilityFactsResponse{}, protoapi.InternalFailureFromProto(failure.GetInternalFailure())
		default:
			return serverapi.CapabilityFactsResponse{}, generatedOperationFailure(failure.Code)
		}
	}
	response, err := protoapi.CapabilityFactsFromProto(result.GetSuccess())
	if err != nil {
		return serverapi.CapabilityFactsResponse{}, err
	}
	return response, nil
}
