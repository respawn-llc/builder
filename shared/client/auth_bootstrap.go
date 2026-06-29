package client

import (
	"context"

	servicecontract "core/shared/apicontract"
	"core/shared/serverapi"
)

type AuthBootstrapClient interface {
	GetAuthBootstrapStatus(ctx context.Context, req serverapi.AuthGetBootstrapStatusRequest) (serverapi.AuthGetBootstrapStatusResponse, error)
	CompleteAuthBootstrap(ctx context.Context, req serverapi.AuthCompleteBootstrapRequest) (serverapi.AuthCompleteBootstrapResponse, error)
	AcknowledgeNoAuth(ctx context.Context, req serverapi.AuthAcknowledgeNoAuthRequest) (serverapi.AuthAcknowledgeNoAuthResponse, error)
}

type loopbackAuthBootstrapClient struct {
	loopbackClient[servicecontract.AuthBootstrapService]
}

func NewLoopbackAuthBootstrapClient(service servicecontract.AuthBootstrapService) AuthBootstrapClient {
	return &loopbackAuthBootstrapClient{loopbackClient: newLoopbackClient(service)}
}

func (c *loopbackAuthBootstrapClient) GetAuthBootstrapStatus(ctx context.Context, req serverapi.AuthGetBootstrapStatusRequest) (serverapi.AuthGetBootstrapStatusResponse, error) {
	return callLoopbackClient(c, "auth bootstrap service is required", ctx, req, servicecontract.AuthBootstrapService.GetBootstrapStatus)
}

func (c *loopbackAuthBootstrapClient) CompleteAuthBootstrap(ctx context.Context, req serverapi.AuthCompleteBootstrapRequest) (serverapi.AuthCompleteBootstrapResponse, error) {
	return callLoopbackClient(c, "auth bootstrap service is required", ctx, req, servicecontract.AuthBootstrapService.CompleteBootstrap)
}

func (c *loopbackAuthBootstrapClient) AcknowledgeNoAuth(ctx context.Context, req serverapi.AuthAcknowledgeNoAuthRequest) (serverapi.AuthAcknowledgeNoAuthResponse, error) {
	return callLoopbackClient(c, "auth bootstrap service is required", ctx, req, servicecontract.AuthBootstrapService.AcknowledgeNoAuth)
}
