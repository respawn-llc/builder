package client

import (
	"context"

	servicecontract "core/shared/apicontract"
	"core/shared/serverapi"
)

type SessionActivityClient = servicecontract.SessionActivityService

type SessionTranscriptClient = servicecontract.SessionTranscriptService

type loopbackSessionActivityClient struct {
	loopbackClient[servicecontract.SessionActivityService]
}

func NewLoopbackSessionActivityClient(service servicecontract.SessionActivityService) SessionActivityClient {
	return &loopbackSessionActivityClient{loopbackClient: newLoopbackClient(service)}
}

func (c *loopbackSessionActivityClient) SubscribeSessionActivity(ctx context.Context, req serverapi.SessionActivitySubscribeRequest) (serverapi.SessionActivitySubscription, error) {
	return callLoopbackClient(c, "session activity service is required", ctx, req, servicecontract.SessionActivityService.SubscribeSessionActivity)
}

type loopbackSessionTranscriptClient struct {
	loopbackClient[servicecontract.SessionTranscriptService]
}

func NewLoopbackSessionTranscriptClient(service servicecontract.SessionTranscriptService) SessionTranscriptClient {
	return &loopbackSessionTranscriptClient{loopbackClient: newLoopbackClient(service)}
}

func (c *loopbackSessionTranscriptClient) SubscribeSessionTranscript(ctx context.Context, req serverapi.TranscriptSubscribeRequest) (serverapi.TranscriptSubscription, error) {
	return callLoopbackClient(c, "session transcript service is required", ctx, req, servicecontract.SessionTranscriptService.SubscribeSessionTranscript)
}
