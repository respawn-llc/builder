package client

import (
	"context"

	servicecontract "core/shared/apicontract"
	"core/shared/serverapi"
)

type AttentionNotificationClient = servicecontract.AttentionNotificationService

type loopbackAttentionNotificationClient struct {
	loopbackClient[servicecontract.AttentionNotificationService]
}

func NewLoopbackAttentionNotificationClient(service servicecontract.AttentionNotificationService) AttentionNotificationClient {
	if service == nil {
		return nil
	}
	return &loopbackAttentionNotificationClient{loopbackClient: newLoopbackClient(service)}
}

func (c *loopbackAttentionNotificationClient) SubscribeAttentionNotifications(ctx context.Context, req serverapi.AttentionNotificationSubscribeRequest) (serverapi.AttentionNotificationSubscription, error) {
	return callLoopbackClient(c, "attention notification service is required", ctx, req, servicecontract.AttentionNotificationService.SubscribeAttentionNotifications)
}

func (c *loopbackAttentionNotificationClient) SubscribeSessionAttentionNotifications(ctx context.Context, req serverapi.AttentionSessionNotificationSubscribeRequest) (serverapi.AttentionNotificationSubscription, error) {
	return callLoopbackClient(c, "attention notification service is required", ctx, req, servicecontract.AttentionNotificationService.SubscribeSessionAttentionNotifications)
}
