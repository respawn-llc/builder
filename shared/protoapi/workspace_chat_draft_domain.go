package protoapi

import (
	"fmt"

	"core/shared/clientui"
	runtimepb "core/shared/protoapi/gen/kent/api/runtime"
	sessionlaunchpb "core/shared/protoapi/gen/kent/api/session_launch"
	"core/shared/serverapi"

	"google.golang.org/protobuf/types/known/emptypb"
)

func WorkspaceChatDraftRequestToProto(
	request serverapi.WorkspaceChatDraftRequest,
) (*sessionlaunchpb.WorkspaceChatDraftRequest, error) {
	if err := request.Operation.Validate(); err != nil {
		return nil, err
	}
	message := &sessionlaunchpb.WorkspaceChatDraftRequest{}
	switch request.Operation.Kind {
	case serverapi.WorkspaceChatDraftReadMessage:
		message.Operation = &sessionlaunchpb.WorkspaceChatDraftRequest_ReadMessage{ReadMessage: &emptypb.Empty{}}
	case serverapi.WorkspaceChatDraftUpdateMessage:
		message.Operation = &sessionlaunchpb.WorkspaceChatDraftRequest_UpdateMessage{
			UpdateMessage: *request.Operation.Message,
		}
	case serverapi.WorkspaceChatDraftClear:
		message.Operation = &sessionlaunchpb.WorkspaceChatDraftRequest_Clear{Clear: &emptypb.Empty{}}
	default:
		return nil, fmt.Errorf("workspace Chat draft operation %q is unsupported", request.Operation.Kind)
	}
	return message, Validate(message)
}

func WorkspaceChatDraftRequestFromProto(
	message *sessionlaunchpb.WorkspaceChatDraftRequest,
) (serverapi.WorkspaceChatDraftRequest, error) {
	request := serverapi.WorkspaceChatDraftRequest{}
	switch operation := message.Operation.(type) {
	case *sessionlaunchpb.WorkspaceChatDraftRequest_ReadMessage:
		request.Operation.Kind = serverapi.WorkspaceChatDraftReadMessage
	case *sessionlaunchpb.WorkspaceChatDraftRequest_UpdateMessage:
		request.Operation.Kind = serverapi.WorkspaceChatDraftUpdateMessage
		request.Operation.Message = &operation.UpdateMessage
	case *sessionlaunchpb.WorkspaceChatDraftRequest_Clear:
		request.Operation.Kind = serverapi.WorkspaceChatDraftClear
	default:
		return serverapi.WorkspaceChatDraftRequest{}, fmt.Errorf(
			"protobuf workspace Chat draft operation %T is unsupported",
			message.Operation,
		)
	}
	return request, nil
}

func WorkspaceChatDraftToProto(
	response serverapi.WorkspaceChatDraftResponse,
) (*sessionlaunchpb.WorkspaceChatDraftSuccess, error) {
	if err := response.Validate(); err != nil {
		return nil, err
	}
	availability, err := goalAvailabilityToProto(response.GoalAvailability)
	if err != nil {
		return nil, err
	}
	success := &sessionlaunchpb.WorkspaceChatDraftSuccess{
		Message: response.Message, GoalAvailability: availability,
	}
	return success, Validate(success)
}

func WorkspaceChatDraftFromProto(
	success *sessionlaunchpb.WorkspaceChatDraftSuccess,
) (serverapi.WorkspaceChatDraftResponse, error) {
	if err := Validate(success); err != nil {
		return serverapi.WorkspaceChatDraftResponse{}, err
	}
	availability, err := goalAvailabilityFromProto(success.GoalAvailability)
	if err != nil {
		return serverapi.WorkspaceChatDraftResponse{}, err
	}
	response := serverapi.WorkspaceChatDraftResponse{
		Message: success.Message, GoalAvailability: availability,
	}
	return response, response.Validate()
}

func goalAvailabilityToProto(value clientui.GoalAvailability) (runtimepb.GoalAvailability, error) {
	switch value {
	case clientui.GoalAvailabilityAvailable:
		return runtimepb.GoalAvailability_GOAL_AVAILABILITY_AVAILABLE, nil
	case clientui.GoalAvailabilityAgentCapabilityMissing:
		return runtimepb.GoalAvailability_GOAL_AVAILABILITY_AGENT_CAPABILITY_MISSING, nil
	default:
		return 0, fmt.Errorf("goal availability %q is unsupported", value)
	}
}

func goalAvailabilityFromProto(value runtimepb.GoalAvailability) (clientui.GoalAvailability, error) {
	switch value {
	case runtimepb.GoalAvailability_GOAL_AVAILABILITY_AVAILABLE:
		return clientui.GoalAvailabilityAvailable, nil
	case runtimepb.GoalAvailability_GOAL_AVAILABILITY_AGENT_CAPABILITY_MISSING:
		return clientui.GoalAvailabilityAgentCapabilityMissing, nil
	default:
		return "", fmt.Errorf("protobuf goal availability %v is unsupported", value)
	}
}
