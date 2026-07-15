package client

import (
	"context"

	"core/shared/protocol"
	"core/shared/serverapi"
)

func (c *Remote) GetCapabilityFacts(ctx context.Context, req serverapi.CapabilityFactsRequest) (serverapi.CapabilityFactsResponse, error) {
	return callUnscopedRPC[serverapi.CapabilityFactsRequest, serverapi.CapabilityFactsResponse](c, ctx, protocol.MethodCapabilityFactsGet, req)
}
