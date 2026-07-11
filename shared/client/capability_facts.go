package client

import (
	"context"

	servicecontract "core/shared/apicontract"
	"core/shared/protocol"
	"core/shared/serverapi"
)

type CapabilityFactsClient = servicecontract.CapabilityFactsService

type loopbackCapabilityFactsClient struct {
	loopbackClient[servicecontract.CapabilityFactsService]
}

func NewLoopbackCapabilityFactsClient(service servicecontract.CapabilityFactsService) CapabilityFactsClient {
	return &loopbackCapabilityFactsClient{loopbackClient: newLoopbackClient(service)}
}

func (c *loopbackCapabilityFactsClient) GetCapabilityFacts(ctx context.Context, req serverapi.CapabilityFactsRequest) (serverapi.CapabilityFactsResponse, error) {
	return callLoopbackClient(c, "capability facts service is required", ctx, req, servicecontract.CapabilityFactsService.GetCapabilityFacts)
}

func (c *Remote) GetCapabilityFacts(ctx context.Context, req serverapi.CapabilityFactsRequest) (serverapi.CapabilityFactsResponse, error) {
	return callUnscopedRPC[serverapi.CapabilityFactsRequest, serverapi.CapabilityFactsResponse](c, ctx, protocol.MethodCapabilityFactsGet, req)
}
