package client

import (
	"context"

	"core/shared/apicontract"
	"core/shared/protocol"
	"core/shared/serverapi"
)

var _ apicontract.PromptCommandCatalogService = (*Remote)(nil)

func (c *Remote) GetPromptCommandCatalog(ctx context.Context, req serverapi.PromptCommandCatalogRequest) (serverapi.PromptCommandCatalogResponse, error) {
	return callControlRPC[serverapi.PromptCommandCatalogRequest, serverapi.PromptCommandCatalogResponse](c, ctx, protocol.MethodPromptCommandCatalogGet, req)
}
