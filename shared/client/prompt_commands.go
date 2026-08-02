package client

import (
	"context"

	"core/shared/protocol"
	"core/shared/serverapi"
)

func (c *Remote) GetPromptCommandCatalog(ctx context.Context, req serverapi.PromptCommandCatalogRequest) (serverapi.PromptCommandCatalogResponse, error) {
	return callControlRPC[serverapi.PromptCommandCatalogRequest, serverapi.PromptCommandCatalogResponse](c, ctx, protocol.MethodPromptCommandCatalogGet, req)
}
