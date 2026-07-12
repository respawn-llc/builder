package client

import (
	"context"

	servicecontract "core/shared/apicontract"
	"core/shared/serverapi"
)

type WorktreeClient = servicecontract.WorktreeService

type loopbackWorktreeClient struct {
	loopbackClient[servicecontract.WorktreeService]
}

func NewLoopbackWorktreeClient(service servicecontract.WorktreeService) WorktreeClient {
	return &loopbackWorktreeClient{loopbackClient: newLoopbackClient(service)}
}

func (c *loopbackWorktreeClient) GetWorktreeStatus(ctx context.Context, req serverapi.WorktreeStatusRequest) (serverapi.WorktreeStatusResponse, error) {
	return callLoopbackClient(c, "worktree service is required", ctx, req, servicecontract.WorktreeService.GetWorktreeStatus)
}

func (c *loopbackWorktreeClient) ListWorktrees(ctx context.Context, req serverapi.WorktreeListRequest) (serverapi.WorktreeListResponse, error) {
	return callLoopbackClient(c, "worktree service is required", ctx, req, servicecontract.WorktreeService.ListWorktrees)
}

func (c *loopbackWorktreeClient) ResolveWorktreeSelector(ctx context.Context, req serverapi.WorktreeSelectorPreviewRequest) (serverapi.WorktreeSelectorPreviewResponse, error) {
	return callLoopbackClient(c, "worktree service is required", ctx, req, servicecontract.WorktreeService.ResolveWorktreeSelector)
}

func (c *loopbackWorktreeClient) ResolveWorktreeCreateTarget(ctx context.Context, req serverapi.WorktreeCreateTargetResolveRequest) (serverapi.WorktreeCreateTargetResolveResponse, error) {
	return callLoopbackClient(c, "worktree service is required", ctx, req, servicecontract.WorktreeService.ResolveWorktreeCreateTarget)
}

func (c *loopbackWorktreeClient) CreateWorktree(ctx context.Context, req serverapi.WorktreeCreateRequest) (serverapi.WorktreeCreateResponse, error) {
	return callLoopbackClient(c, "worktree service is required", ctx, req, servicecontract.WorktreeService.CreateWorktree)
}

func (c *loopbackWorktreeClient) EnterWorktree(ctx context.Context, req serverapi.WorktreeEnterRequest) (serverapi.WorktreeScheduledAcknowledgement, error) {
	return callLoopbackClient(c, "worktree service is required", ctx, req, servicecontract.WorktreeService.EnterWorktree)
}

func (c *loopbackWorktreeClient) LeaveWorktree(ctx context.Context, req serverapi.WorktreeLeaveRequest) (serverapi.WorktreeScheduledAcknowledgement, error) {
	return callLoopbackClient(c, "worktree service is required", ctx, req, servicecontract.WorktreeService.LeaveWorktree)
}

func (c *loopbackWorktreeClient) DeleteWorktree(ctx context.Context, req serverapi.WorktreeDeleteRequest) (serverapi.WorktreeDeleteResult, error) {
	return callLoopbackClient(c, "worktree service is required", ctx, req, servicecontract.WorktreeService.DeleteWorktree)
}

func (c *loopbackWorktreeClient) SubscribeWorktreeSetup(ctx context.Context, req serverapi.WorktreeSetupSubscribeRequest) (serverapi.WorktreeSetupSubscription, error) {
	return callLoopbackClient(c, "worktree service is required", ctx, req, servicecontract.WorktreeService.SubscribeWorktreeSetup)
}
