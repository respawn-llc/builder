package client

import (
	"context"
	"fmt"

	"core/shared/apicontract"
	"core/shared/protoapi"
	worktreepb "core/shared/protoapi/gen/kent/api/worktree"
	"core/shared/protocol"
	"core/shared/worktreecontract"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

type remoteWorktreeBinaryClient struct {
	remote *Remote
}

func newRemoteWorktreeBinaryClient(remote *Remote) *remoteWorktreeBinaryClient {
	return &remoteWorktreeBinaryClient{remote: remote}
}

func worktreeBinaryMethod(
	serviceName protoreflect.Name,
	methodName protoreflect.Name,
) protoreflect.MethodDescriptor {
	return bootstrapMethod(
		worktreepb.File_kent_api_worktree_worktree_proto,
		serviceName,
		methodName,
	)
}

type generatedWorktreeResult[Success proto.Message] interface {
	proto.Message
	GetSuccess() Success
}

func callGeneratedWorktree[
	Request proto.Message,
	Success proto.Message,
	Result generatedWorktreeResult[Success],
](
	c *Remote,
	ctx context.Context,
	method protoreflect.MethodDescriptor,
	request Request,
	result Result,
) (Success, error) {
	var zero Success
	if err := c.callBinary(ctx, method, request, result); err != nil {
		return zero, err
	}
	success, err := classifyGeneratedWorktreeResult(method, result)
	if err != nil {
		return zero, err
	}
	if !success {
		return zero, fmt.Errorf("%s returned no success or failure", method.FullName())
	}
	return result.GetSuccess(), nil
}

func classifyGeneratedWorktreeResult(
	method protoreflect.MethodDescriptor,
	result proto.Message,
) (bool, error) {
	classified, err := protoapi.ClassifyResult(result)
	if err != nil {
		return false, fmt.Errorf("classify %s result: %w", method.FullName(), err)
	}
	switch classified.Outcome {
	case protoapi.OperationSuccess:
		return true, nil
	case protoapi.OperationGenericFailure:
		return false, generatedOperationFailure(classified.Failure.Code)
	case protoapi.OperationKnownFailure:
		if classified.Failure == nil || !classified.Failure.Detail.IsValid() {
			return false, fmt.Errorf("%s classified a failure without typed details", method.FullName())
		}
		mapped, conversionErr := protoapi.WorktreeErrorFromProto(classified.Failure.Detail.Interface())
		if conversionErr != nil {
			return false, fmt.Errorf("decode %s failure: %w", method.FullName(), conversionErr)
		}
		return false, mapped
	default:
		return false, fmt.Errorf("%s has unsupported result outcome %d", method.FullName(), classified.Outcome)
	}
}

func callRemoteWorktree[
	Request any,
	WireRequest proto.Message,
	WireSuccess proto.Message,
	Result generatedWorktreeResult[WireSuccess],
	Response any,
](
	client *remoteWorktreeBinaryClient,
	ctx context.Context,
	serviceName protoreflect.Name,
	methodName protoreflect.Name,
	request Request,
	toProto func(Request) (WireRequest, error),
	result Result,
	fromProto func(WireSuccess) (Response, error),
) (Response, error) {
	var zero Response
	wireRequest, err := toProto(request)
	if err != nil {
		return zero, err
	}
	success, err := callGeneratedWorktree(
		client.remote,
		ctx,
		worktreeBinaryMethod(serviceName, methodName),
		wireRequest,
		result,
	)
	if err != nil {
		return zero, err
	}
	return fromProto(success)
}

func (c *remoteWorktreeBinaryClient) GetWorktreeStatus(
	ctx context.Context,
	request worktreecontract.StatusRequest,
) (worktreecontract.StatusResponse, error) {
	return callRemoteWorktree(c, ctx, "StatusService", "Get", request,
		protoapi.WorktreeStatusRequestToProto,
		&worktreepb.StatusResult{},
		protoapi.WorktreeStatusSuccessFromProto)
}

func (c *remoteWorktreeBinaryClient) ListWorktrees(
	ctx context.Context,
	request worktreecontract.ListRequest,
) (worktreecontract.ListResponse, error) {
	return callRemoteWorktree(c, ctx, "ListService", "List", request,
		protoapi.WorktreeListRequestToProto,
		&worktreepb.ListResult{},
		protoapi.WorktreeListSuccessFromProto)
}

func (c *remoteWorktreeBinaryClient) ListWorkspaceWorktrees(
	ctx context.Context,
	request worktreecontract.WorkspaceListRequest,
) (worktreecontract.WorkspaceListResponse, error) {
	return callRemoteWorktree(c, ctx, "ListService", "ListWorkspace", request,
		protoapi.WorktreeWorkspaceListRequestToProto,
		&worktreepb.WorkspaceListResult{},
		protoapi.WorktreeWorkspaceListSuccessFromProto)
}

func (c *remoteWorktreeBinaryClient) ResolveWorktreeSelector(
	ctx context.Context,
	request worktreecontract.SelectorResolveRequest,
) (worktreecontract.SelectorResolveResponse, error) {
	return callRemoteWorktree(c, ctx, "SelectorService", "Resolve", request,
		protoapi.WorktreeSelectorResolveRequestToProto,
		&worktreepb.SelectorResolveResult{},
		protoapi.WorktreeSelectorResolveSuccessFromProto)
}

func (c *remoteWorktreeBinaryClient) PreviewWorktreeDelete(
	ctx context.Context,
	request worktreecontract.DeletePreviewRequest,
) (worktreecontract.DeletePreviewResponse, error) {
	return callRemoteWorktree(c, ctx, "DeletePreviewService", "Get", request,
		protoapi.WorktreeDeletePreviewRequestToProto,
		&worktreepb.DeletePreviewResult{},
		protoapi.WorktreeDeletePreviewSuccessFromProto)
}

func (c *remoteWorktreeBinaryClient) ResolveWorktreeCreateTarget(
	ctx context.Context,
	request worktreecontract.CreateTargetResolveRequest,
) (worktreecontract.CreateTargetResolveResponse, error) {
	return callRemoteWorktree(c, ctx, "CreateTargetService", "Resolve", request,
		protoapi.WorktreeCreateTargetResolveRequestToProto,
		&worktreepb.CreateTargetResolveResult{},
		protoapi.WorktreeCreateTargetResolveSuccessFromProto)
}

func (c *remoteWorktreeBinaryClient) CreateWorktree(
	ctx context.Context,
	request worktreecontract.CreateRequest,
) (worktreecontract.CreateResponse, error) {
	return callRemoteWorktree(c, ctx, "CreateService", "Create", request,
		protoapi.WorktreeCreateRequestToProto,
		&worktreepb.CreateResult{},
		protoapi.WorktreeCreateSuccessFromProto)
}

func (c *remoteWorktreeBinaryClient) EnterWorktree(
	ctx context.Context,
	request worktreecontract.EnterRequest,
) (worktreecontract.ScheduledAcknowledgement, error) {
	return callRemoteWorktree(c, ctx, "TransitionService", "Enter", request,
		protoapi.WorktreeEnterRequestToProto,
		&worktreepb.EnterResult{},
		protoapi.WorktreeScheduledAcknowledgementFromProto)
}

func (c *remoteWorktreeBinaryClient) LeaveWorktree(
	ctx context.Context,
	request worktreecontract.LeaveRequest,
) (worktreecontract.ScheduledAcknowledgement, error) {
	return callRemoteWorktree(c, ctx, "TransitionService", "Leave", request,
		protoapi.WorktreeLeaveRequestToProto,
		&worktreepb.LeaveResult{},
		protoapi.WorktreeScheduledAcknowledgementFromProto)
}

func (c *remoteWorktreeBinaryClient) DeleteWorktree(
	ctx context.Context,
	request worktreecontract.DeleteRequest,
) (worktreecontract.DeleteResult, error) {
	return callRemoteWorktree(c, ctx, "TransitionService", "Delete", request,
		protoapi.WorktreeDeleteRequestToProto,
		&worktreepb.DeleteResult{},
		protoapi.WorktreeDeleteSuccessFromProto)
}

func (c *remoteWorktreeBinaryClient) SubscribeWorktreeSetup(
	ctx context.Context,
	request worktreecontract.SetupSubscribeRequest,
) (worktreecontract.SetupSubscription, error) {
	wireRequest, err := protoapi.WorktreeSetupSubscribeRequestToProto(request)
	if err != nil {
		return nil, err
	}
	method := worktreeBinaryMethod("SetupService", "Subscribe")
	return subscribeGeneratedBinary(
		c.remote,
		ctx,
		method,
		wireRequest,
		&worktreepb.SetupStartResult{},
		func() *worktreepb.SetupEvent { return &worktreepb.SetupEvent{} },
		protoapi.WorktreeSetupEventFromProto,
		func() *worktreepb.SetupCompletion { return &worktreepb.SetupCompletion{} },
		func(completion *worktreepb.SetupCompletion) (remoteDescriptorTerminalOutcome, error) {
			if completion.Code == nil {
				return remoteDescriptorTerminalOutcome{}, nil
			}
			return remoteDescriptorTerminalOutcome{err: protocolError(&protocol.ResponseError{
				Code: int(completion.GetCode()), Message: completion.GetDiagnostic(),
			})}, nil
		},
		func(result *worktreepb.SetupStartResult) error {
			success, resultErr := classifyGeneratedWorktreeResult(method, result)
			if resultErr != nil {
				return resultErr
			}
			if !success {
				return fmt.Errorf("%s returned no success or failure", method.FullName())
			}
			return nil
		},
	)
}

var _ apicontract.WorktreeService = (*remoteWorktreeBinaryClient)(nil)
