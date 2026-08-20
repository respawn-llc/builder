package protoapi

import (
	"core/shared/protoapi/gen/kent/api/session_launch"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

func WorkspaceChatMaterializationToProto(
	response serverapi.WorkspaceChatMaterializeResponse,
) (*sessionlaunchpb.MaterializeWorkspaceChatSuccess, error) {
	if err := response.Validate(); err != nil {
		return nil, err
	}
	success := &sessionlaunchpb.MaterializeWorkspaceChatSuccess{
		SessionId: response.SessionID.String(),
	}
	return success, Validate(success)
}

func WorkspaceChatMaterializationFromProto(
	success *sessionlaunchpb.MaterializeWorkspaceChatSuccess,
) (serverapi.WorkspaceChatMaterializeResponse, error) {
	if err := Validate(success); err != nil {
		return serverapi.WorkspaceChatMaterializeResponse{}, err
	}
	sessionID, err := runtimeids.ParseSessionID(success.SessionId)
	if err != nil {
		return serverapi.WorkspaceChatMaterializeResponse{}, err
	}
	response := serverapi.WorkspaceChatMaterializeResponse{SessionID: sessionID}
	return response, response.Validate()
}
