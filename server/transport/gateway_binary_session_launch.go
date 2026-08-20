package transport

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"core/shared/apicontract"
	"core/shared/protoapi"
	projectpb "core/shared/protoapi/gen/kent/api/project"
	sessionlaunchpb "core/shared/protoapi/gen/kent/api/session_launch"
	"core/shared/serverapi"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/emptypb"
)

func registerSessionLaunchGatewayBinaryBindings(bindings map[string]gatewayBinaryBinding) error {
	service := sessionlaunchpb.File_kent_api_session_launch_session_launch_proto.Services().ByName("SessionLaunchService")
	if err := registerSessionLaunchUnary(bindings, service, "Plan",
		func() *sessionlaunchpb.SessionPlanRequest { return &sessionlaunchpb.SessionPlanRequest{} },
		apicontract.SessionLaunchService.PlanSession,
		binarySessionPlanFailure,
	); err != nil {
		return err
	}
	if err := registerSessionLaunchUnary(bindings, service, "WorkspaceChatDraft",
		func() *sessionlaunchpb.WorkspaceChatDraftRequest {
			return &sessionlaunchpb.WorkspaceChatDraftRequest{}
		},
		apicontract.SessionLaunchService.WorkspaceChatDraft,
		binaryWorkspaceChatDraftFailure,
	); err != nil {
		return err
	}
	return registerSessionLaunchUnary(bindings, service, "MaterializeWorkspaceChat",
		func() *emptypb.Empty { return &emptypb.Empty{} },
		apicontract.SessionLaunchService.MaterializeWorkspaceChat,
		binaryMaterializeWorkspaceChatFailure,
	)
}

func registerSessionLaunchUnary[
	Request proto.Message,
	Success proto.Message,
](
	bindings map[string]gatewayBinaryBinding,
	service protoreflect.ServiceDescriptor,
	method protoreflect.Name,
	newRequest func() Request,
	invoke func(apicontract.SessionLaunchService, context.Context, Request) (Success, error),
	failureDetail func(*Gateway, *connectionState, Request, error) proto.Message,
) error {
	return registerGatewayBinaryUnary(
		bindings, service, method, gatewayBinaryCoreActiveOrdinary, newRequest, nil,
		func(g *Gateway, ctx context.Context, state *connectionState, request Request) (Success, error) {
			client, err := g.sessionLaunchClientForState(ctx, state)
			if err != nil {
				var zero Success
				return zero, err
			}
			return invoke(client, ctx, request)
		},
		failureDetail,
	)
}

func binarySessionPlanFailure(
	g *Gateway,
	state *connectionState,
	_ *sessionlaunchpb.SessionPlanRequest,
	err error,
) proto.Message {
	failure, known, conversionErr := protoapi.SessionPlanErrorToProto(
		err,
		sessionLaunchWorkspaceNotRegisteredDetails(g, state),
	)
	if conversionErr != nil {
		return binaryInternalFailure(fmt.Errorf("encode session plan failure: %w", conversionErr))
	}
	if !known {
		return binaryInternalFailure(err)
	}
	return failure
}

func binaryWorkspaceChatDraftFailure(
	g *Gateway,
	state *connectionState,
	_ *sessionlaunchpb.WorkspaceChatDraftRequest,
	err error,
) proto.Message {
	switch {
	case errors.Is(err, serverapi.ErrServerAuthRequired):
		return &sessionlaunchpb.AuthRequiredDetails{}
	case errors.Is(err, serverapi.ErrWorkspaceNotRegistered):
		return sessionLaunchWorkspaceNotRegisteredDetails(g, state)
	default:
		return binaryInternalFailure(err)
	}
}

func binaryMaterializeWorkspaceChatFailure(
	g *Gateway,
	state *connectionState,
	_ *emptypb.Empty,
	err error,
) proto.Message {
	switch {
	case errors.Is(err, serverapi.ErrServerAuthRequired):
		return &sessionlaunchpb.AuthRequiredDetails{}
	case errors.Is(err, serverapi.ErrWorkspaceNotRegistered):
		return sessionLaunchWorkspaceNotRegisteredDetails(g, state)
	default:
		return binaryInternalFailure(err)
	}
}

func sessionLaunchWorkspaceNotRegisteredDetails(
	g *Gateway,
	state *connectionState,
) *projectpb.WorkspaceNotRegisteredDetails {
	details := &projectpb.WorkspaceNotRegisteredDetails{}
	if state != nil {
		if projectID := strings.TrimSpace(state.attachedProject); projectID != "" {
			details.ProjectId = &projectID
		}
		if workspaceID := strings.TrimSpace(state.attachedWorkspaceID); workspaceID != "" {
			details.WorkspaceId = &workspaceID
		}
		if workspaceRoot := strings.TrimSpace(state.attachedWorkspaceRoot); workspaceRoot != "" {
			details.WorkspaceRoot = &workspaceRoot
		}
	}
	if details.ProjectId == nil && g != nil {
		if projectID := strings.TrimSpace(g.deps.ProjectID()); projectID != "" {
			details.ProjectId = &projectID
		}
	}
	return details
}
