package client

import (
	"context"
	"errors"

	"core/shared/apicontract"
	"core/shared/protocol"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

var _ apicontract.PromptCommandCatalogService = (*Remote)(nil)

func (c *Remote) GetPromptCommandCatalog(ctx context.Context, req serverapi.PromptCommandCatalogRequest) (serverapi.PromptCommandCatalogResponse, error) {
	return callControlRPC[serverapi.PromptCommandCatalogRequest, serverapi.PromptCommandCatalogResponse](c, ctx, protocol.MethodPromptCommandCatalogGet, req)
}

type sessionPromptCommandCatalogClient struct {
	remote    *Remote
	sessionID runtimeids.SessionID
}

func (c sessionPromptCommandCatalogClient) GetPromptCommandCatalog(ctx context.Context, req serverapi.PromptCommandCatalogRequest) (serverapi.PromptCommandCatalogResponse, error) {
	sessionID := c.sessionID
	req.SessionID = &sessionID
	return c.remote.GetPromptCommandCatalog(ctx, req)
}

func (c *Remote) PromptCommandCatalogClientForSession(sessionID string) (apicontract.PromptCommandCatalogService, error) {
	if c == nil {
		return nil, errors.New("remote client is required")
	}
	parsed, err := runtimeids.ParseSessionID(sessionID)
	if err != nil {
		return nil, err
	}
	return sessionPromptCommandCatalogClient{remote: c, sessionID: parsed}, nil
}
