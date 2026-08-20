package client

import (
	"context"

	"core/shared/protoapi"
	sessionlaunchpb "core/shared/protoapi/gen/kent/api/session_launch"
	"core/shared/serverapi"

	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/emptypb"
)

func sessionLaunchMethod(name string) protoreflect.MethodDescriptor {
	return bootstrapMethod(
		sessionlaunchpb.File_kent_api_session_launch_session_launch_proto,
		"SessionLaunchService",
		protoreflect.Name(name),
	)
}

func (c *Remote) PlanSession(
	ctx context.Context,
	request serverapi.SessionPlanRequest,
) (serverapi.SessionPlanResponse, error) {
	message, err := protoapi.SessionPlanRequestToProto(request)
	if err != nil {
		return serverapi.SessionPlanResponse{}, err
	}
	result := &sessionlaunchpb.SessionPlanResult{}
	if err := c.callBinary(ctx, sessionLaunchMethod("Plan"), message, result); err != nil {
		return serverapi.SessionPlanResponse{}, err
	}
	if failure := result.GetError(); failure != nil {
		return serverapi.SessionPlanResponse{}, protoapi.SessionPlanErrorFromProto(failure)
	}
	return protoapi.SessionPlanFromProto(result.GetSuccess())
}

func (c *Remote) WorkspaceChatDraft(
	ctx context.Context,
	request serverapi.WorkspaceChatDraftRequest,
) (serverapi.WorkspaceChatDraftResponse, error) {
	message, err := protoapi.WorkspaceChatDraftRequestToProto(request)
	if err != nil {
		return serverapi.WorkspaceChatDraftResponse{}, err
	}
	result := &sessionlaunchpb.WorkspaceChatDraftResult{}
	if err := c.callBinary(ctx, sessionLaunchMethod("WorkspaceChatDraft"), message, result); err != nil {
		return serverapi.WorkspaceChatDraftResponse{}, err
	}
	if failure := result.GetError(); failure != nil {
		if failure.GetAuthRequired() != nil {
			return serverapi.WorkspaceChatDraftResponse{}, serverapi.ErrServerAuthRequired
		}
		if failure.GetWorkspaceNotRegistered() != nil {
			return serverapi.WorkspaceChatDraftResponse{}, serverapi.ErrWorkspaceNotRegistered
		}
		return serverapi.WorkspaceChatDraftResponse{}, protoapi.InternalFailureFromProto(failure.GetInternalFailure())
	}
	return protoapi.WorkspaceChatDraftFromProto(result.GetSuccess())
}

func (c *Remote) MaterializeWorkspaceChat(
	ctx context.Context,
	request serverapi.WorkspaceChatMaterializeRequest,
) (serverapi.WorkspaceChatMaterializeResponse, error) {
	if err := request.Validate(); err != nil {
		return serverapi.WorkspaceChatMaterializeResponse{}, err
	}
	result := &sessionlaunchpb.MaterializeWorkspaceChatResult{}
	if err := c.callBinary(
		ctx,
		sessionLaunchMethod("MaterializeWorkspaceChat"),
		&emptypb.Empty{},
		result,
	); err != nil {
		return serverapi.WorkspaceChatMaterializeResponse{}, err
	}
	if failure := result.GetError(); failure != nil {
		if failure.GetAuthRequired() != nil {
			return serverapi.WorkspaceChatMaterializeResponse{}, serverapi.ErrServerAuthRequired
		}
		if failure.GetWorkspaceNotRegistered() != nil {
			return serverapi.WorkspaceChatMaterializeResponse{}, serverapi.ErrWorkspaceNotRegistered
		}
		return serverapi.WorkspaceChatMaterializeResponse{}, protoapi.InternalFailureFromProto(failure.GetInternalFailure())
	}
	return protoapi.WorkspaceChatMaterializationFromProto(result.GetSuccess())
}
