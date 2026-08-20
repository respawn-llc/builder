package protoapi

import (
	"fmt"

	"core/shared/clientui"
	projectpb "core/shared/protoapi/gen/kent/api/project"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/sessioncontract"

	"google.golang.org/protobuf/types/known/timestamppb"
)

func SessionPageRequestToProto(request serverapi.SessionPageRequest) (*projectpb.SessionPageRequest, error) {
	if err := request.Validate(); err != nil {
		return nil, err
	}
	category, err := sessionCategoryToProto(request.Category)
	if err != nil {
		return nil, err
	}
	message := &projectpb.SessionPageRequest{ProjectId: request.ProjectID, Category: category}
	if request.Offset != nil {
		offset, err := projectInt32(*request.Offset, "session page offset")
		if err != nil {
			return nil, err
		}
		message.Offset = &offset
	}
	if request.Limit != nil {
		limit, err := projectInt32(*request.Limit, "session page limit")
		if err != nil {
			return nil, err
		}
		message.Limit = &limit
	}
	return message, Validate(message)
}

func SessionPageRequestFromProto(message *projectpb.SessionPageRequest) (serverapi.SessionPageRequest, error) {
	category, err := sessionCategoryFromProto(message.Category)
	if err != nil {
		return serverapi.SessionPageRequest{}, err
	}
	request := serverapi.SessionPageRequest{ProjectID: message.ProjectId, Category: category}
	if message.Offset != nil {
		value := int(*message.Offset)
		request.Offset = &value
	}
	if message.Limit != nil {
		value := int(*message.Limit)
		request.Limit = &value
	}
	return request, nil
}

func SessionPageToProto(response serverapi.SessionPageResponse) (*projectpb.SessionPageSuccess, error) {
	if err := response.Validate(); err != nil {
		return nil, err
	}
	category, err := sessionCategoryToProto(response.Category)
	if err != nil {
		return nil, err
	}
	sessions, err := mapSliceError(response.Sessions, sessionSummaryToProto)
	if err != nil {
		return nil, err
	}
	success := &projectpb.SessionPageSuccess{
		ProjectId: response.ProjectID, Category: category, Sessions: sessions,
	}
	if response.NextOffset != nil {
		value, err := projectInt32(*response.NextOffset, "session page next_offset")
		if err != nil {
			return nil, err
		}
		success.NextOffset = &value
	}
	return success, Validate(success)
}

func SessionPageFromProto(success *projectpb.SessionPageSuccess) (serverapi.SessionPageResponse, error) {
	if err := Validate(success); err != nil {
		return serverapi.SessionPageResponse{}, err
	}
	category, err := sessionCategoryFromProto(success.Category)
	if err != nil {
		return serverapi.SessionPageResponse{}, err
	}
	sessions, err := mapSliceError(success.Sessions, sessionSummaryFromProto)
	if err != nil {
		return serverapi.SessionPageResponse{}, err
	}
	response := serverapi.SessionPageResponse{
		ProjectID: success.ProjectId, Category: category, Sessions: sessions,
	}
	if success.NextOffset != nil {
		value := int(*success.NextOffset)
		response.NextOffset = &value
	}
	return response, response.Validate()
}

func sessionSummaryToProto(summary clientui.SessionSummary) (*projectpb.SessionSummary, error) {
	category, err := sessionCategoryToProto(summary.Category)
	if err != nil {
		return nil, err
	}
	message := &projectpb.SessionSummary{
		SessionId: summary.SessionID.String(), Category: category, UpdatedAt: timestamppb.New(summary.UpdatedAt),
	}
	if summary.Name != "" {
		message.Name = &summary.Name
	}
	if summary.FirstPromptPreview != "" {
		message.FirstPromptPreview = &summary.FirstPromptPreview
	}
	return message, Validate(message)
}

func sessionSummaryFromProto(message *projectpb.SessionSummary) (clientui.SessionSummary, error) {
	if err := Validate(message); err != nil {
		return clientui.SessionSummary{}, err
	}
	sessionID, err := runtimeids.ParseSessionID(message.SessionId)
	if err != nil {
		return clientui.SessionSummary{}, err
	}
	category, err := sessionCategoryFromProto(message.Category)
	if err != nil {
		return clientui.SessionSummary{}, err
	}
	return clientui.SessionSummary{
		SessionID: sessionID, Category: category, Name: dereference(message.Name),
		FirstPromptPreview: dereference(message.FirstPromptPreview), UpdatedAt: message.UpdatedAt.AsTime(),
	}, nil
}

func sessionCategoryToProto(category sessioncontract.SessionCategory) (projectpb.SessionCategory, error) {
	switch category {
	case sessioncontract.SessionCategoryMain:
		return projectpb.SessionCategory_SESSION_CATEGORY_MAIN, nil
	case sessioncontract.SessionCategorySubagent:
		return projectpb.SessionCategory_SESSION_CATEGORY_SUBAGENT, nil
	default:
		return 0, fmt.Errorf("session category %q is unsupported", category)
	}
}

func sessionCategoryFromProto(category projectpb.SessionCategory) (sessioncontract.SessionCategory, error) {
	switch category {
	case projectpb.SessionCategory_SESSION_CATEGORY_MAIN:
		return sessioncontract.SessionCategoryMain, nil
	case projectpb.SessionCategory_SESSION_CATEGORY_SUBAGENT:
		return sessioncontract.SessionCategorySubagent, nil
	default:
		return "", fmt.Errorf("protobuf session category %v is unsupported", category)
	}
}
