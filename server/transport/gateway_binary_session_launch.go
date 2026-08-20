package transport

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"core/shared/protoapi"
	projectpb "core/shared/protoapi/gen/kent/api/project"
	sessionlaunchpb "core/shared/protoapi/gen/kent/api/session_launch"
	"core/shared/serverapi"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

func registerSessionLaunchGatewayBinaryBindings(bindings map[string]gatewayBinaryBinding) error {
	service := sessionlaunchpb.File_kent_api_session_launch_session_launch_proto.Services().ByName("SessionLaunchService")
	if err := registerGatewayBinaryBinding(
		bindings,
		service,
		"Plan",
		gatewayBinaryCoreActiveOrdinary,
		func() proto.Message { return &sessionlaunchpb.SessionPlanRequest{} },
		nil,
		invokeBinarySessionPlan,
		binarySessionPlanInternalFailure,
	); err != nil {
		return err
	}
	operation, err := protoapi.OperationFromDescriptor(service.Methods().ByName("Plan"))
	if err != nil {
		return err
	}
	binding := bindings[operation.Name]
	binding.failure = binarySessionPlanFailure
	bindings[operation.Name] = binding
	if err := registerGatewayBinaryBinding(
		bindings,
		service,
		"WorkspaceChatDraft",
		gatewayBinaryCoreActiveOrdinary,
		func() proto.Message { return &sessionlaunchpb.WorkspaceChatDraftRequest{} },
		nil,
		invokeBinaryWorkspaceChatDraft,
		binaryWorkspaceChatDraftInternalFailure,
	); err != nil {
		return err
	}
	operation, err = protoapi.OperationFromDescriptor(service.Methods().ByName("WorkspaceChatDraft"))
	if err != nil {
		return err
	}
	binding = bindings[operation.Name]
	binding.failure = binaryWorkspaceChatDraftFailure
	bindings[operation.Name] = binding
	if err := registerGatewayBinaryBinding(
		bindings,
		service,
		"MaterializeWorkspaceChat",
		gatewayBinaryCoreActiveOrdinary,
		func() proto.Message { return &emptypb.Empty{} },
		nil,
		invokeBinaryMaterializeWorkspaceChat,
		binaryMaterializeWorkspaceChatInternalFailure,
	); err != nil {
		return err
	}
	operation, err = protoapi.OperationFromDescriptor(service.Methods().ByName("MaterializeWorkspaceChat"))
	if err != nil {
		return err
	}
	binding = bindings[operation.Name]
	binding.failure = binaryMaterializeWorkspaceChatFailure
	bindings[operation.Name] = binding
	return nil
}

func invokeBinarySessionPlan(
	g *Gateway,
	ctx context.Context,
	state *connectionState,
	message proto.Message,
) (proto.Message, error) {
	request, err := protoapi.SessionPlanRequestFromProto(message.(*sessionlaunchpb.SessionPlanRequest))
	if err != nil {
		return nil, err
	}
	client, err := g.sessionLaunchClientForState(ctx, state)
	if err != nil {
		return nil, err
	}
	response, err := client.PlanSession(ctx, request)
	if err != nil {
		return nil, err
	}
	success, err := protoapi.SessionPlanToProto(response)
	if err != nil {
		return nil, err
	}
	return &sessionlaunchpb.SessionPlanResult{
		Outcome: &sessionlaunchpb.SessionPlanResult_Success{Success: success},
	}, nil
}

func binarySessionPlanFailure(
	g *Gateway,
	state *connectionState,
	_ proto.Message,
	err error,
) proto.Message {
	failure, known, conversionErr := protoapi.SessionPlanErrorToProto(
		err,
		sessionLaunchWorkspaceNotRegisteredDetails(g, state),
	)
	if conversionErr != nil {
		return binarySessionPlanInternalFailure(fmt.Errorf("encode session plan failure: %w", conversionErr))
	}
	if !known {
		return binarySessionPlanInternalFailure(err)
	}
	return &sessionlaunchpb.SessionPlanResult{
		Outcome: &sessionlaunchpb.SessionPlanResult_Error{Error: failure},
	}
}

func binarySessionPlanInternalFailure(err error) proto.Message {
	return &sessionlaunchpb.SessionPlanResult{
		Outcome: &sessionlaunchpb.SessionPlanResult_Error{Error: &sessionlaunchpb.SessionPlanError{
			Code: "internal_failure",
			Detail: &sessionlaunchpb.SessionPlanError_InternalFailure{
				InternalFailure: binaryInternalFailure(err),
			},
		}},
	}
}

func invokeBinaryWorkspaceChatDraft(
	g *Gateway,
	ctx context.Context,
	state *connectionState,
	message proto.Message,
) (proto.Message, error) {
	request, err := protoapi.WorkspaceChatDraftRequestFromProto(message.(*sessionlaunchpb.WorkspaceChatDraftRequest))
	if err != nil {
		return nil, err
	}
	client, err := g.sessionLaunchClientForState(ctx, state)
	if err != nil {
		return nil, err
	}
	response, err := client.WorkspaceChatDraft(ctx, request)
	if err != nil {
		return nil, err
	}
	success, err := protoapi.WorkspaceChatDraftToProto(response)
	if err != nil {
		return nil, err
	}
	return &sessionlaunchpb.WorkspaceChatDraftResult{
		Outcome: &sessionlaunchpb.WorkspaceChatDraftResult_Success{Success: success},
	}, nil
}

func binaryWorkspaceChatDraftFailure(
	g *Gateway,
	state *connectionState,
	_ proto.Message,
	err error,
) proto.Message {
	failure := &sessionlaunchpb.WorkspaceChatDraftError{}
	switch {
	case errors.Is(err, serverapi.ErrServerAuthRequired):
		failure.Code = "auth_required"
		failure.Detail = &sessionlaunchpb.WorkspaceChatDraftError_AuthRequired{
			AuthRequired: &sessionlaunchpb.AuthRequiredDetails{},
		}
	case errors.Is(err, serverapi.ErrWorkspaceNotRegistered):
		failure.Code = "workspace_not_registered"
		failure.Detail = &sessionlaunchpb.WorkspaceChatDraftError_WorkspaceNotRegistered{
			WorkspaceNotRegistered: sessionLaunchWorkspaceNotRegisteredDetails(g, state),
		}
	default:
		failure.Code = "internal_failure"
		failure.Detail = &sessionlaunchpb.WorkspaceChatDraftError_InternalFailure{
			InternalFailure: binaryInternalFailure(err),
		}
	}
	return &sessionlaunchpb.WorkspaceChatDraftResult{
		Outcome: &sessionlaunchpb.WorkspaceChatDraftResult_Error{Error: failure},
	}
}

func binaryWorkspaceChatDraftInternalFailure(err error) proto.Message {
	return &sessionlaunchpb.WorkspaceChatDraftResult{
		Outcome: &sessionlaunchpb.WorkspaceChatDraftResult_Error{Error: &sessionlaunchpb.WorkspaceChatDraftError{
			Code: "internal_failure",
			Detail: &sessionlaunchpb.WorkspaceChatDraftError_InternalFailure{
				InternalFailure: binaryInternalFailure(err),
			},
		}},
	}
}

func invokeBinaryMaterializeWorkspaceChat(
	g *Gateway,
	ctx context.Context,
	state *connectionState,
	_ proto.Message,
) (proto.Message, error) {
	client, err := g.sessionLaunchClientForState(ctx, state)
	if err != nil {
		return nil, err
	}
	response, err := client.MaterializeWorkspaceChat(ctx, serverapi.WorkspaceChatMaterializeRequest{})
	if err != nil {
		return nil, err
	}
	success, err := protoapi.WorkspaceChatMaterializationToProto(response)
	if err != nil {
		return nil, err
	}
	return &sessionlaunchpb.MaterializeWorkspaceChatResult{
		Outcome: &sessionlaunchpb.MaterializeWorkspaceChatResult_Success{Success: success},
	}, nil
}

func binaryMaterializeWorkspaceChatFailure(
	g *Gateway,
	state *connectionState,
	_ proto.Message,
	err error,
) proto.Message {
	failure := &sessionlaunchpb.MaterializeWorkspaceChatError{}
	switch {
	case errors.Is(err, serverapi.ErrServerAuthRequired):
		failure.Code = "auth_required"
		failure.Detail = &sessionlaunchpb.MaterializeWorkspaceChatError_AuthRequired{
			AuthRequired: &sessionlaunchpb.AuthRequiredDetails{},
		}
	case errors.Is(err, serverapi.ErrWorkspaceNotRegistered):
		failure.Code = "workspace_not_registered"
		failure.Detail = &sessionlaunchpb.MaterializeWorkspaceChatError_WorkspaceNotRegistered{
			WorkspaceNotRegistered: sessionLaunchWorkspaceNotRegisteredDetails(g, state),
		}
	default:
		failure.Code = "internal_failure"
		failure.Detail = &sessionlaunchpb.MaterializeWorkspaceChatError_InternalFailure{
			InternalFailure: binaryInternalFailure(err),
		}
	}
	return &sessionlaunchpb.MaterializeWorkspaceChatResult{
		Outcome: &sessionlaunchpb.MaterializeWorkspaceChatResult_Error{Error: failure},
	}
}

func binaryMaterializeWorkspaceChatInternalFailure(err error) proto.Message {
	return &sessionlaunchpb.MaterializeWorkspaceChatResult{
		Outcome: &sessionlaunchpb.MaterializeWorkspaceChatResult_Error{Error: &sessionlaunchpb.MaterializeWorkspaceChatError{
			Code: "internal_failure",
			Detail: &sessionlaunchpb.MaterializeWorkspaceChatError_InternalFailure{
				InternalFailure: binaryInternalFailure(err),
			},
		}},
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
