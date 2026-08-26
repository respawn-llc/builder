package transport

import (
	"context"
	"errors"
	"fmt"

	"core/shared/apicontract"
	"core/shared/protoapi"
	projectpb "core/shared/protoapi/gen/kent/api/project"
	worktreepb "core/shared/protoapi/gen/kent/api/worktree"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func registerWorktreeGatewayBinaryBindings(bindings map[string]gatewayBinaryBinding) error {
	statusService := worktreepb.File_kent_api_worktree_worktree_proto.Services().ByName("StatusService")
	listService := worktreepb.File_kent_api_worktree_worktree_proto.Services().ByName("ListService")
	selectorService := worktreepb.File_kent_api_worktree_worktree_proto.Services().ByName("SelectorService")
	createTargetService := worktreepb.File_kent_api_worktree_worktree_proto.Services().ByName("CreateTargetService")
	deletePreviewService := worktreepb.File_kent_api_worktree_worktree_proto.Services().ByName("DeletePreviewService")
	createService := worktreepb.File_kent_api_worktree_worktree_proto.Services().ByName("CreateService")
	transitionService := worktreepb.File_kent_api_worktree_worktree_proto.Services().ByName("TransitionService")
	return errors.Join(
		registerWorktreeUnary(
			bindings, statusService, "Get",
			func() *worktreepb.StatusRequest { return &worktreepb.StatusRequest{} },
			worktreeSessionScope[*worktreepb.StatusRequest],
			func(client apicontract.WorktreeService, ctx context.Context, request *worktreepb.StatusRequest) (*worktreepb.StatusSuccess, error) {
				domain, err := protoapi.WorktreeStatusRequestFromProto(request)
				if err != nil {
					return nil, err
				}
				response, err := client.GetWorktreeStatus(ctx, domain)
				if err != nil {
					return nil, err
				}
				return protoapi.WorktreeStatusSuccessToProto(response)
			},
			worktreeFailure[*worktreepb.StatusRequest],
		),
		registerWorktreeUnary(
			bindings, listService, "List",
			func() *worktreepb.ListRequest { return &worktreepb.ListRequest{} },
			worktreeSessionScope[*worktreepb.ListRequest],
			func(client apicontract.WorktreeService, ctx context.Context, request *worktreepb.ListRequest) (*worktreepb.ListSuccess, error) {
				domain, err := protoapi.WorktreeListRequestFromProto(request)
				if err != nil {
					return nil, err
				}
				response, err := client.ListWorktrees(ctx, domain)
				if err != nil {
					return nil, err
				}
				return protoapi.WorktreeListSuccessToProto(response)
			},
			worktreeFailure[*worktreepb.ListRequest],
		),
		registerWorktreeUnary(
			bindings, listService, "ListWorkspace",
			func() *worktreepb.WorkspaceListRequest { return &worktreepb.WorkspaceListRequest{} },
			func(request *worktreepb.WorkspaceListRequest) (routeScopeParams, error) {
				return routeScopeParams{projectID: request.ProjectId, workspaceID: request.WorkspaceId}, nil
			},
			func(client apicontract.WorktreeService, ctx context.Context, request *worktreepb.WorkspaceListRequest) (*worktreepb.WorkspaceListSuccess, error) {
				domain, err := protoapi.WorktreeWorkspaceListRequestFromProto(request)
				if err != nil {
					return nil, err
				}
				response, err := client.ListWorkspaceWorktrees(ctx, domain)
				if err != nil {
					return nil, err
				}
				return protoapi.WorktreeWorkspaceListSuccessToProto(response)
			},
			func(
				_ *Gateway,
				_ *connectionState,
				request *worktreepb.WorkspaceListRequest,
				err error,
			) proto.Message {
				return worktreeFailureDetail(
					err,
					&projectpb.WorkspaceNotRegisteredDetails{
						ProjectId:   cloneStringPointer(request.GetProjectId()),
						WorkspaceId: cloneStringPointer(request.GetWorkspaceId()),
					},
				)
			},
		),
		registerWorktreeUnary(
			bindings, selectorService, "Resolve",
			func() *worktreepb.SelectorResolveRequest { return &worktreepb.SelectorResolveRequest{} },
			worktreeSessionScope[*worktreepb.SelectorResolveRequest],
			func(client apicontract.WorktreeService, ctx context.Context, request *worktreepb.SelectorResolveRequest) (*worktreepb.SelectorResolveSuccess, error) {
				domain, err := protoapi.WorktreeSelectorResolveRequestFromProto(request)
				if err != nil {
					return nil, err
				}
				response, err := client.ResolveWorktreeSelector(ctx, domain)
				if err != nil {
					return nil, err
				}
				return protoapi.WorktreeSelectorResolveSuccessToProto(response)
			},
			worktreeFailure[*worktreepb.SelectorResolveRequest],
		),
		registerWorktreeUnary(
			bindings, createTargetService, "Resolve",
			func() *worktreepb.CreateTargetResolveRequest { return &worktreepb.CreateTargetResolveRequest{} },
			worktreeSessionScope[*worktreepb.CreateTargetResolveRequest],
			func(client apicontract.WorktreeService, ctx context.Context, request *worktreepb.CreateTargetResolveRequest) (*worktreepb.CreateTargetResolveSuccess, error) {
				domain, err := protoapi.WorktreeCreateTargetResolveRequestFromProto(request)
				if err != nil {
					return nil, err
				}
				response, err := client.ResolveWorktreeCreateTarget(ctx, domain)
				if err != nil {
					return nil, err
				}
				return protoapi.WorktreeCreateTargetResolveSuccessToProto(response)
			},
			worktreeFailure[*worktreepb.CreateTargetResolveRequest],
		),
		registerWorktreeUnary(
			bindings, deletePreviewService, "Get",
			func() *worktreepb.DeletePreviewRequest { return &worktreepb.DeletePreviewRequest{} },
			worktreeSessionScope[*worktreepb.DeletePreviewRequest],
			func(client apicontract.WorktreeService, ctx context.Context, request *worktreepb.DeletePreviewRequest) (*worktreepb.DeletePreviewSuccess, error) {
				domain, err := protoapi.WorktreeDeletePreviewRequestFromProto(request)
				if err != nil {
					return nil, err
				}
				response, err := client.PreviewWorktreeDelete(ctx, domain)
				if err != nil {
					return nil, err
				}
				return protoapi.WorktreeDeletePreviewSuccessToProto(response)
			},
			worktreeFailure[*worktreepb.DeletePreviewRequest],
		),
		registerWorktreeUnary(
			bindings, createService, "Create",
			func() *worktreepb.CreateRequest { return &worktreepb.CreateRequest{} },
			worktreeSessionScope[*worktreepb.CreateRequest],
			func(client apicontract.WorktreeService, ctx context.Context, request *worktreepb.CreateRequest) (*worktreepb.CreateSuccess, error) {
				domain, err := protoapi.WorktreeCreateRequestFromProto(request)
				if err != nil {
					return nil, err
				}
				response, err := client.CreateWorktree(ctx, domain)
				if err != nil {
					return nil, err
				}
				return protoapi.WorktreeCreateSuccessToProto(response)
			},
			worktreeFailure[*worktreepb.CreateRequest],
		),
		registerWorktreeUnary(
			bindings, transitionService, "Enter",
			func() *worktreepb.EnterRequest { return &worktreepb.EnterRequest{} },
			worktreeTransitionScope[*worktreepb.EnterRequest],
			func(client apicontract.WorktreeService, ctx context.Context, request *worktreepb.EnterRequest) (*worktreepb.ScheduledAcknowledgement, error) {
				domain, err := protoapi.WorktreeEnterRequestFromProto(request)
				if err != nil {
					return nil, err
				}
				response, err := client.EnterWorktree(ctx, domain)
				if err != nil {
					return nil, err
				}
				return protoapi.WorktreeScheduledAcknowledgementToProto(response)
			},
			worktreeFailure[*worktreepb.EnterRequest],
		),
		registerWorktreeUnary(
			bindings, transitionService, "Leave",
			func() *worktreepb.LeaveRequest { return &worktreepb.LeaveRequest{} },
			worktreeTransitionScope[*worktreepb.LeaveRequest],
			func(client apicontract.WorktreeService, ctx context.Context, request *worktreepb.LeaveRequest) (*worktreepb.ScheduledAcknowledgement, error) {
				domain, err := protoapi.WorktreeLeaveRequestFromProto(request)
				if err != nil {
					return nil, err
				}
				response, err := client.LeaveWorktree(ctx, domain)
				if err != nil {
					return nil, err
				}
				return protoapi.WorktreeScheduledAcknowledgementToProto(response)
			},
			worktreeFailure[*worktreepb.LeaveRequest],
		),
		registerWorktreeUnary(
			bindings, transitionService, "Delete",
			func() *worktreepb.DeleteRequest { return &worktreepb.DeleteRequest{} },
			worktreeTransitionScope[*worktreepb.DeleteRequest],
			func(client apicontract.WorktreeService, ctx context.Context, request *worktreepb.DeleteRequest) (*worktreepb.DeleteSuccess, error) {
				domain, err := protoapi.WorktreeDeleteRequestFromProto(request)
				if err != nil {
					return nil, err
				}
				response, err := client.DeleteWorktree(ctx, domain)
				if err != nil {
					return nil, err
				}
				return protoapi.WorktreeDeleteSuccessToProto(response)
			},
			worktreeFailure[*worktreepb.DeleteRequest],
		),
	)
}

type worktreeSessionRequest interface {
	proto.Message
	GetSessionId() string
}

func worktreeSessionScope[Request worktreeSessionRequest](request Request) (routeScopeParams, error) {
	return routeScopeParams{sessionID: request.GetSessionId()}, nil
}

type worktreeTransitionRequest interface {
	proto.Message
	GetTransition() *worktreepb.TransitionHeader
}

func worktreeTransitionScope[Request worktreeTransitionRequest](request Request) (routeScopeParams, error) {
	transition := request.GetTransition()
	if transition == nil {
		return routeScopeParams{}, errors.New("worktree transition header is required")
	}
	return routeScopeParams{sessionID: transition.SessionId}, nil
}

func registerWorktreeUnary[
	Request proto.Message,
	Success proto.Message,
](
	bindings map[string]gatewayBinaryBinding,
	service protoreflect.ServiceDescriptor,
	method protoreflect.Name,
	newRequest func() Request,
	scope func(Request) (routeScopeParams, error),
	invoke func(apicontract.WorktreeService, context.Context, Request) (Success, error),
	failure func(*Gateway, *connectionState, Request, error) proto.Message,
) error {
	return registerGatewayBinaryUnary(
		bindings,
		service,
		method,
		gatewayBinaryCoreActiveOrdinary,
		newRequest,
		scope,
		func(g *Gateway, ctx context.Context, _ *connectionState, request Request) (Success, error) {
			client := g.deps.WorktreeClient()
			if client == nil {
				var zero Success
				return zero, errors.New("worktree client is required")
			}
			return invoke(client, ctx, request)
		},
		failure,
	)
}

func worktreeFailure[Request proto.Message](
	_ *Gateway,
	_ *connectionState,
	_ Request,
	err error,
) proto.Message {
	return worktreeFailureDetail(err, nil)
}

func worktreeFailureDetail(
	err error,
	workspace *projectpb.WorkspaceNotRegisteredDetails,
) proto.Message {
	detail, known, conversionErr := protoapi.WorktreeErrorToProto(err, workspace)
	if conversionErr != nil {
		return binaryInternalFailure(fmt.Errorf("encode Worktree failure: %w", conversionErr))
	}
	if known {
		return detail
	}
	return binaryInternalFailure(err)
}

func cloneStringPointer(value string) *string {
	cloned := value
	return &cloned
}

var _ worktreeSessionRequest = (*worktreepb.StatusRequest)(nil)
var _ worktreeSessionRequest = (*worktreepb.ListRequest)(nil)
var _ worktreeSessionRequest = (*worktreepb.SelectorResolveRequest)(nil)
var _ worktreeSessionRequest = (*worktreepb.CreateTargetResolveRequest)(nil)
var _ worktreeSessionRequest = (*worktreepb.DeletePreviewRequest)(nil)
var _ worktreeSessionRequest = (*worktreepb.CreateRequest)(nil)
var _ worktreeTransitionRequest = (*worktreepb.EnterRequest)(nil)
var _ worktreeTransitionRequest = (*worktreepb.LeaveRequest)(nil)
var _ worktreeTransitionRequest = (*worktreepb.DeleteRequest)(nil)
