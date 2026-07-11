package client

import (
	"context"

	servicecontract "core/shared/apicontract"
	"core/shared/serverapi"
)

type RuntimeLiveControlClient = servicecontract.RuntimeLiveControlService

type loopbackRuntimeLiveControlClient struct {
	loopbackClient[servicecontract.RuntimeLiveControlService]
}

func NewLoopbackRuntimeLiveControlClient(service servicecontract.RuntimeLiveControlService) RuntimeLiveControlClient {
	return &loopbackRuntimeLiveControlClient{loopbackClient: newLoopbackClient(service)}
}

func (c *loopbackRuntimeLiveControlClient) LiveSteer(ctx context.Context, req serverapi.RuntimeLiveSteerRequest) (serverapi.RuntimeLiveSteerResponse, error) {
	return callLoopbackClient(c, "runtime live-control service is required", ctx, req, servicecontract.RuntimeLiveControlService.LiveSteer)
}

func (c *loopbackRuntimeLiveControlClient) LiveStop(ctx context.Context, req serverapi.RuntimeLiveStopRequest) (serverapi.RuntimeLiveStopResponse, error) {
	return callLoopbackClient(c, "runtime live-control service is required", ctx, req, servicecontract.RuntimeLiveControlService.LiveStop)
}

func (c *loopbackRuntimeLiveControlClient) LiveWait(ctx context.Context, req serverapi.RuntimeLiveWaitRequest) (serverapi.RuntimeLiveWaitResponse, error) {
	return callLoopbackClient(c, "runtime live-control service is required", ctx, req, servicecontract.RuntimeLiveControlService.LiveWait)
}
