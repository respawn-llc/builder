package transport

import (
	"context"
	"errors"
	"io"

	"core/shared/apicontract"
	"core/shared/protoapi"
	authpb "core/shared/protoapi/gen/kent/api/auth"
	projectpb "core/shared/protoapi/gen/kent/api/project"
	worktreepb "core/shared/protoapi/gen/kent/api/worktree"
	"core/shared/serverapi"
	"core/shared/worktreecontract"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/emptypb"
)

func registerWorktreeGatewayBinaryBindings(bindings map[string]gatewayBinaryBinding) error {
	status := worktreepb.File_kent_api_worktree_worktree_proto.Services().ByName("StatusService")
	list := worktreepb.File_kent_api_worktree_worktree_proto.Services().ByName("ListService")
	selector := worktreepb.File_kent_api_worktree_worktree_proto.Services().ByName("SelectorService")
	deletePreview := worktreepb.File_kent_api_worktree_worktree_proto.Services().ByName("DeletePreviewService")
	createTarget := worktreepb.File_kent_api_worktree_worktree_proto.Services().ByName("CreateTargetService")
	create := worktreepb.File_kent_api_worktree_worktree_proto.Services().ByName("CreateService")
	transition := worktreepb.File_kent_api_worktree_worktree_proto.Services().ByName("TransitionService")
	return errors.Join(
		registerWorktreeUnary(bindings, status, "Get", func() *worktreepb.StatusRequest { return &worktreepb.StatusRequest{} },
			worktreeSessionScope[*worktreepb.StatusRequest], apicontract.WorktreeService.GetWorktreeStatus, worktreePlatformFailure[*worktreepb.StatusRequest]),
		registerWorktreeUnary(bindings, list, "List", func() *worktreepb.ListRequest { return &worktreepb.ListRequest{} },
			worktreeSessionScope[*worktreepb.ListRequest], apicontract.WorktreeService.ListWorktrees, worktreePlatformFailure[*worktreepb.ListRequest]),
		registerWorktreeUnary(bindings, list, "ListWorkspace", func() *worktreepb.WorkspaceListRequest { return &worktreepb.WorkspaceListRequest{} },
			worktreeWorkspaceScope, apicontract.WorktreeService.ListWorkspaceWorktrees, worktreeWorkspaceListFailure),
		registerWorktreeUnary(bindings, selector, "Resolve", func() *worktreepb.SelectorResolveRequest { return &worktreepb.SelectorResolveRequest{} },
			worktreeSessionScope[*worktreepb.SelectorResolveRequest], apicontract.WorktreeService.ResolveWorktreeSelector, worktreeSelectorFailure[*worktreepb.SelectorResolveRequest]),
		registerWorktreeUnary(bindings, deletePreview, "Get", func() *worktreepb.DeletePreviewRequest { return &worktreepb.DeletePreviewRequest{} },
			worktreeSessionScope[*worktreepb.DeletePreviewRequest], apicontract.WorktreeService.PreviewWorktreeDelete, worktreeDeletePreviewFailure),
		registerWorktreeUnary(bindings, createTarget, "Resolve", func() *worktreepb.CreateTargetResolveRequest { return &worktreepb.CreateTargetResolveRequest{} },
			worktreeSessionScope[*worktreepb.CreateTargetResolveRequest], apicontract.WorktreeService.ResolveWorktreeCreateTarget, worktreePlatformFailure[*worktreepb.CreateTargetResolveRequest]),
		registerWorktreeUnary(bindings, create, "Create",
			func() *worktreepb.CreateRequest { return &worktreepb.CreateRequest{} },
			worktreeSessionScope[*worktreepb.CreateRequest],
			apicontract.WorktreeService.CreateWorktree,
			worktreeCreateFailure,
			func(_ *worktreepb.CreateRequest, err error) proto.Message {
				return worktreeCreateFailure(nil, protoapi.ClassifyWorktreeCreateValidation(err))
			}),
		registerWorktreeUnary(bindings, transition, "Enter", func() *worktreepb.EnterRequest { return &worktreepb.EnterRequest{} },
			worktreeSessionScope[*worktreepb.EnterRequest], apicontract.WorktreeService.EnterWorktree, worktreeTransitionFailure[*worktreepb.EnterRequest]),
		registerWorktreeUnary(bindings, transition, "Leave", func() *worktreepb.LeaveRequest { return &worktreepb.LeaveRequest{} },
			worktreeSessionScope[*worktreepb.LeaveRequest], apicontract.WorktreeService.LeaveWorktree, worktreeTransitionFailure[*worktreepb.LeaveRequest]),
		registerWorktreeUnary(bindings, transition, "Delete", func() *worktreepb.DeleteRequest { return &worktreepb.DeleteRequest{} },
			worktreeSessionScope[*worktreepb.DeleteRequest], apicontract.WorktreeService.DeleteWorktree, worktreeDeleteFailure),
	)
}

func registerWorktreeSetupGatewayBinaryBinding(bindings map[string]gatewayBinaryBinding) error {
	service := worktreepb.File_kent_api_worktree_worktree_proto.Services().ByName("SetupService")
	if service == nil {
		return errors.New("generated SetupService descriptor is required")
	}
	method := service.Methods().ByName("Subscribe")
	associated, err := protoapi.ResolveSubscriptionOperations(method)
	if err != nil {
		return err
	}
	start, err := protoapi.SuccessResult(method, &emptypb.Empty{})
	if err != nil {
		return err
	}
	bindings[associated.Subscribe.Name] = gatewayBinaryBinding{
		operation:  associated.Subscribe,
		associated: &associated,
		policy:     gatewayBinaryCoreActiveOrdinary,
		request:    func() proto.Message { return &worktreepb.SetupSubscribeRequest{} },
		subscribe: func(
			g *Gateway,
			ctx context.Context,
			_ *connectionState,
			message proto.Message,
		) (gatewayBinarySubscriber, error) {
			request, ok := message.(*worktreepb.SetupSubscribeRequest)
			if !ok {
				return nil, errors.New("worktree setup request type is invalid")
			}
			client := g.deps.WorktreeClient()
			if client == nil {
				return nil, errors.New("worktree client is required")
			}
			subscription, err := client.SubscribeWorktreeSetup(ctx, request)
			return worktreeSetupGatewaySubscriber{WorktreeSetupSubscription: subscription}, err
		},
		failure: func(_ *Gateway, _ *connectionState, _ proto.Message, err error) proto.Message {
			if details, ok := binaryServerNotReadyDetails(err); ok {
				return gatewayBinaryFailureResult(method, details)
			}
			return gatewayBinaryFailureResult(
				method,
				worktreePlatformFailure[*worktreepb.SetupSubscribeRequest](nil, err),
			)
		},
		start:    start,
		complete: func(err error) proto.Message { return worktreeSetupCompletion(err) },
	}
	return nil
}

type worktreeSetupGatewaySubscriber struct {
	apicontract.WorktreeSetupSubscription
}

func (s worktreeSetupGatewaySubscriber) Next(ctx context.Context) (proto.Message, error) {
	return s.WorktreeSetupSubscription.Next(ctx)
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
	failure func(Request, error) proto.Message,
	validationFailure ...func(Request, error) proto.Message,
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
		func(_ *Gateway, _ *connectionState, request Request, err error) proto.Message {
			return failure(request, err)
		},
		validationFailure...,
	)
}

type worktreeSessionRequest interface {
	proto.Message
	GetSessionId() string
}

func worktreeSessionScope[Request worktreeSessionRequest](request Request) (routeScopeParams, error) {
	return routeScopeParams{sessionID: request.GetSessionId()}, nil
}

func worktreeWorkspaceScope(request *worktreepb.WorkspaceListRequest) (routeScopeParams, error) {
	return routeScopeParams{projectID: request.ProjectId, workspaceID: request.WorkspaceId}, nil
}

func worktreePlatformFailure[Request proto.Message](_ Request, err error) proto.Message {
	if errors.Is(err, serverapi.ErrServerAuthRequired) {
		return &authpb.AuthRequiredDetails{}
	}
	return binaryInternalFailure(err)
}

func worktreeWorkspaceListFailure(request *worktreepb.WorkspaceListRequest, err error) proto.Message {
	if errors.Is(err, serverapi.ErrWorkspaceNotRegistered) && request != nil {
		return &projectpb.WorkspaceNotRegisteredDetails{
			ProjectId:   proto.String(request.ProjectId),
			WorkspaceId: proto.String(request.WorkspaceId),
		}
	}
	return worktreePlatformFailure(request, err)
}

func worktreeSelectorFailure[Request proto.Message](request Request, err error) proto.Message {
	var selector *worktreecontract.SelectorError
	if errors.As(err, &selector) && selector != nil && selector.Details != nil {
		return selector.Details
	}
	return worktreePlatformFailure(request, err)
}

func worktreeTransitionFailure[Request proto.Message](request Request, err error) proto.Message {
	if errors.Is(err, serverapi.ErrPendingWorkCapacity) {
		return &worktreepb.PendingWorkCapacityDetails{}
	}
	return worktreeSelectorFailure(request, err)
}

func worktreeDeletePreviewFailure(request *worktreepb.DeletePreviewRequest, err error) proto.Message {
	if errors.Is(err, worktreecontract.ErrWorktreeBlocked) {
		return &worktreepb.BlockedDetails{}
	}
	return worktreeSelectorFailure(request, err)
}

func worktreeCreateFailure(request *worktreepb.CreateRequest, err error) proto.Message {
	var retained *worktreecontract.SetupRetainedError
	if errors.As(err, &retained) && retained != nil && retained.Details != nil {
		return retained.Details
	}
	var create *worktreecontract.CreateError
	if errors.As(err, &create) && create != nil {
		owner := worktreepb.CreateErrorOwner_WORKTREE_CREATE_ERROR_OWNER_FORM
		if create.Owner == worktreecontract.CreateErrorOwnerBaseRef {
			owner = worktreepb.CreateErrorOwner_WORKTREE_CREATE_ERROR_OWNER_BASE_REF
		}
		return &worktreepb.CreateFailureDetails{Owner: owner, Diagnostic: create.Diagnostic}
	}
	return worktreePlatformFailure(request, err)
}

func worktreeDeleteFailure(request *worktreepb.DeleteRequest, err error) proto.Message {
	var precondition *worktreecontract.DeletePreconditionError
	if errors.As(err, &precondition) && precondition != nil && precondition.Details != nil {
		return precondition.Details
	}
	if errors.Is(err, worktreecontract.ErrWorktreeBlocked) {
		return &worktreepb.BlockedDetails{}
	}
	return worktreeSelectorFailure(request, err)
}

func worktreeSetupCompletion(err error) *worktreepb.SetupCompletion {
	if err == nil || errors.Is(err, io.EOF) {
		return &worktreepb.SetupCompletion{}
	}
	rawCode, diagnostic := protocolError(err)
	code := int32(rawCode)
	return &worktreepb.SetupCompletion{Code: &code, Diagnostic: &diagnostic}
}
