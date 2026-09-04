package client

import (
	"context"

	"core/shared/protoapi"
	sessionlaunchpb "core/shared/protoapi/gen/kent/api/session_launch"

	"google.golang.org/protobuf/reflect/protoreflect"
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
