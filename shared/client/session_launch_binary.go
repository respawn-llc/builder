package client

import (
	"context"

	"core/shared/protoapi"
	authpb "core/shared/protoapi/gen/kent/api/auth"
	projectpb "core/shared/protoapi/gen/kent/api/project"
	sessionlaunchpb "core/shared/protoapi/gen/kent/api/session_launch"
	sharedpb "core/shared/protoapi/gen/kent/api/shared"
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
	request *sessionlaunchpb.SessionPlanRequest,
) (*sessionlaunchpb.SessionPlanSuccess, error) {
	return callGeneratedBinary(c, ctx, sessionLaunchMethod("Plan"), request,
		&sessionlaunchpb.SessionPlanResult{},
		protoapi.SessionPlanErrorFromProto)
}

func (c *Remote) WorkspaceChatDraft(
	ctx context.Context,
	request *sessionlaunchpb.WorkspaceChatDraftRequest,
) (*sessionlaunchpb.WorkspaceChatDraftSuccess, error) {
	return callGeneratedBinary(c, ctx, sessionLaunchMethod("WorkspaceChatDraft"), request,
		&sessionlaunchpb.WorkspaceChatDraftResult{},
		workspaceChatGeneratedError[*sessionlaunchpb.WorkspaceChatDraftError])
}

func (c *Remote) MaterializeWorkspaceChat(
	ctx context.Context,
	request *emptypb.Empty,
) (*sessionlaunchpb.MaterializeWorkspaceChatSuccess, error) {
	return callGeneratedBinary(c, ctx, sessionLaunchMethod("MaterializeWorkspaceChat"), request,
		&sessionlaunchpb.MaterializeWorkspaceChatResult{},
		workspaceChatGeneratedError[*sessionlaunchpb.MaterializeWorkspaceChatError])
}

type workspaceChatFailure interface {
	GetAuthRequired() *authpb.AuthRequiredDetails
	GetWorkspaceNotRegistered() *projectpb.WorkspaceNotRegisteredDetails
	GetInternalFailure() *sharedpb.InternalFailureDetails
}

func workspaceChatGeneratedError[Failure workspaceChatFailure](failure Failure) error {
	if failure.GetAuthRequired() != nil {
		return serverapi.ErrServerAuthRequired
	}
	if failure.GetWorkspaceNotRegistered() != nil {
		return serverapi.ErrWorkspaceNotRegistered
	}
	return protoapi.InternalFailureFromProto(failure.GetInternalFailure())
}
