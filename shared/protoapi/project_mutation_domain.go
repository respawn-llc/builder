package protoapi

import (
	"errors"
	"fmt"

	"core/shared/clientui"
	projectpb "core/shared/protoapi/gen/kent/api/project"
	"core/shared/serverapi"
)

func ProjectCreateRequestToProto(request serverapi.ProjectCreateRequest) (*projectpb.CreateProjectRequest, error) {
	if err := request.Validate(); err != nil {
		return nil, err
	}
	message := &projectpb.CreateProjectRequest{
		DisplayName:   request.DisplayName,
		WorkspaceRoot: request.WorkspaceRoot,
	}
	if request.ProjectKey != "" {
		message.ProjectKey = &request.ProjectKey
	}
	if err := Validate(message); err != nil {
		return nil, err
	}
	return message, nil
}

func ProjectCreateRequestFromProto(message *projectpb.CreateProjectRequest) (serverapi.ProjectCreateRequest, error) {
	request := serverapi.ProjectCreateRequest{
		DisplayName:   message.DisplayName,
		ProjectKey:    dereference(message.ProjectKey),
		WorkspaceRoot: message.WorkspaceRoot,
	}
	return request, nil
}

func ProjectCreateToProto(response serverapi.ProjectCreateResponse) (*projectpb.CreateProjectSuccess, error) {
	binding, err := projectMutationBindingToProto(response.Binding)
	if err != nil {
		return nil, err
	}
	success := &projectpb.CreateProjectSuccess{Binding: binding}
	return success, Validate(success)
}

func ProjectCreateFromProto(success *projectpb.CreateProjectSuccess) (serverapi.ProjectCreateResponse, error) {
	if err := Validate(success); err != nil {
		return serverapi.ProjectCreateResponse{}, err
	}
	binding, err := projectMutationBindingFromProto(success.Binding)
	if err != nil {
		return serverapi.ProjectCreateResponse{}, err
	}
	return serverapi.ProjectCreateResponse{Binding: binding}, nil
}

func ProjectUpdateRequestToProto(request serverapi.ProjectUpdateRequest) (*projectpb.UpdateProjectRequest, error) {
	if err := request.Validate(); err != nil {
		return nil, err
	}
	message := &projectpb.UpdateProjectRequest{
		ProjectId:   request.ProjectID,
		DisplayName: request.DisplayName,
	}
	if request.ProjectKey != "" {
		message.ProjectKey = &request.ProjectKey
	}
	if err := Validate(message); err != nil {
		return nil, err
	}
	return message, nil
}

func ProjectUpdateRequestFromProto(message *projectpb.UpdateProjectRequest) (serverapi.ProjectUpdateRequest, error) {
	request := serverapi.ProjectUpdateRequest{
		ProjectID:   message.ProjectId,
		DisplayName: message.DisplayName,
		ProjectKey:  dereference(message.ProjectKey),
	}
	return request, nil
}

func ProjectUpdateToProto(response serverapi.ProjectUpdateResponse) (*projectpb.UpdateProjectSuccess, error) {
	project, err := projectHomeSummaryToProto(response.Project)
	if err != nil {
		return nil, err
	}
	success := &projectpb.UpdateProjectSuccess{Project: project}
	return success, Validate(success)
}

func ProjectUpdateFromProto(success *projectpb.UpdateProjectSuccess) (serverapi.ProjectUpdateResponse, error) {
	if err := Validate(success); err != nil {
		return serverapi.ProjectUpdateResponse{}, err
	}
	project, err := projectHomeSummaryFromProto(success.Project)
	if err != nil {
		return serverapi.ProjectUpdateResponse{}, err
	}
	return serverapi.ProjectUpdateResponse{Project: project}, nil
}

func ProjectDefaultWorkspaceRequestToProto(request serverapi.ProjectDefaultWorkspaceSetRequest) (*projectpb.SetDefaultWorkspaceRequest, error) {
	if err := request.Validate(); err != nil {
		return nil, err
	}
	selector, err := projectWorkspaceSelectorToProto(request.ProjectWorkspaceSelector)
	if err != nil {
		return nil, err
	}
	message := &projectpb.SetDefaultWorkspaceRequest{ProjectId: request.ProjectID, Workspace: selector}
	return message, Validate(message)
}

func ProjectDefaultWorkspaceRequestFromProto(message *projectpb.SetDefaultWorkspaceRequest) (serverapi.ProjectDefaultWorkspaceSetRequest, error) {
	selector, err := projectWorkspaceSelectorFromProto(message.Workspace)
	if err != nil {
		return serverapi.ProjectDefaultWorkspaceSetRequest{}, err
	}
	request := serverapi.ProjectDefaultWorkspaceSetRequest{
		ProjectID:                message.ProjectId,
		ProjectWorkspaceSelector: selector,
	}
	return request, nil
}

func ProjectDefaultWorkspaceToProto(response serverapi.ProjectDefaultWorkspaceSetResponse) (*projectpb.SetDefaultWorkspaceSuccess, error) {
	if err := response.Validate(); err != nil {
		return nil, err
	}
	project, err := projectHomeSummaryToProto(response.Project)
	if err != nil {
		return nil, err
	}
	success := &projectpb.SetDefaultWorkspaceSuccess{Project: project}
	return success, Validate(success)
}

func ProjectDefaultWorkspaceFromProto(success *projectpb.SetDefaultWorkspaceSuccess) (serverapi.ProjectDefaultWorkspaceSetResponse, error) {
	if err := Validate(success); err != nil {
		return serverapi.ProjectDefaultWorkspaceSetResponse{}, err
	}
	project, err := projectHomeSummaryFromProto(success.Project)
	if err != nil {
		return serverapi.ProjectDefaultWorkspaceSetResponse{}, err
	}
	response := serverapi.ProjectDefaultWorkspaceSetResponse{Project: project}
	return response, response.Validate()
}

func ProjectWorkspaceUnlinkRequestToProto(request serverapi.ProjectWorkspaceUnlinkRequest) (*projectpb.UnlinkWorkspaceRequest, error) {
	if err := request.Validate(); err != nil {
		return nil, err
	}
	selector, err := projectWorkspaceSelectorToProto(request.ProjectWorkspaceSelector)
	if err != nil {
		return nil, err
	}
	message := &projectpb.UnlinkWorkspaceRequest{ProjectId: request.ProjectID, Workspace: selector}
	return message, Validate(message)
}

func ProjectWorkspaceUnlinkRequestFromProto(message *projectpb.UnlinkWorkspaceRequest) (serverapi.ProjectWorkspaceUnlinkRequest, error) {
	selector, err := projectWorkspaceSelectorFromProto(message.Workspace)
	if err != nil {
		return serverapi.ProjectWorkspaceUnlinkRequest{}, err
	}
	request := serverapi.ProjectWorkspaceUnlinkRequest{
		ProjectID:                message.ProjectId,
		ProjectWorkspaceSelector: selector,
	}
	return request, nil
}

func ProjectWorkspaceUnlinkToProto(response serverapi.ProjectWorkspaceUnlinkResponse) (*projectpb.UnlinkWorkspaceSuccess, error) {
	if err := response.Validate(); err != nil {
		return nil, err
	}
	blockers, err := mapSliceError(response.Blockers, workspaceUnlinkBlockerToProto)
	if err != nil {
		return nil, err
	}
	success := &projectpb.UnlinkWorkspaceSuccess{
		ProjectId: response.ProjectID, WorkspaceId: response.WorkspaceID, Blockers: blockers,
	}
	if response.Project != nil {
		success.Project, err = projectHomeSummaryToProto(*response.Project)
		if err != nil {
			return nil, err
		}
	}
	return success, Validate(success)
}

func ProjectWorkspaceUnlinkFromProto(success *projectpb.UnlinkWorkspaceSuccess) (serverapi.ProjectWorkspaceUnlinkResponse, error) {
	if err := Validate(success); err != nil {
		return serverapi.ProjectWorkspaceUnlinkResponse{}, err
	}
	response := serverapi.ProjectWorkspaceUnlinkResponse{
		ProjectID:   success.ProjectId,
		WorkspaceID: success.WorkspaceId,
		Blockers: mapSlice(success.Blockers, func(blocker *projectpb.WorkspaceUnlinkBlocker) serverapi.ProjectWorkspaceUnlinkBlocker {
			return serverapi.ProjectWorkspaceUnlinkBlocker{Code: blocker.Code, Count: int(blocker.GetCount())}
		}),
	}
	if success.Project != nil {
		project, err := projectHomeSummaryFromProto(success.Project)
		if err != nil {
			return serverapi.ProjectWorkspaceUnlinkResponse{}, err
		}
		response.Project = &project
	}
	return response, response.Validate()
}

func ProjectDeleteRequestToProto(request serverapi.ProjectDeleteRequest) (*projectpb.DeleteProjectRequest, error) {
	if err := request.Validate(); err != nil {
		return nil, err
	}
	return &projectpb.DeleteProjectRequest{ProjectId: request.ProjectID}, nil
}

func ProjectDeleteRequestFromProto(message *projectpb.DeleteProjectRequest) (serverapi.ProjectDeleteRequest, error) {
	request := serverapi.ProjectDeleteRequest{ProjectID: message.ProjectId}
	return request, nil
}

func ProjectDeleteToProto(response serverapi.ProjectDeleteResponse) (*projectpb.DeleteProjectSuccess, error) {
	blockers, err := mapSliceError(response.Blockers, projectDeleteBlockerToProto)
	if err != nil {
		return nil, err
	}
	success := &projectpb.DeleteProjectSuccess{
		ProjectId: response.ProjectID, Deleted: response.Deleted, Blockers: blockers,
	}
	return success, Validate(success)
}

func ProjectDeleteFromProto(success *projectpb.DeleteProjectSuccess) (serverapi.ProjectDeleteResponse, error) {
	if err := Validate(success); err != nil {
		return serverapi.ProjectDeleteResponse{}, err
	}
	return serverapi.ProjectDeleteResponse{
		ProjectID: success.ProjectId,
		Deleted:   success.Deleted,
		Blockers: mapSlice(success.Blockers, func(blocker *projectpb.ProjectDeleteBlocker) serverapi.ProjectDeleteBlocker {
			return serverapi.ProjectDeleteBlocker{Code: blocker.Code, Count: int(blocker.Count)}
		}),
	}, nil
}

func ProjectAttachWorkspaceRequestToProto(request serverapi.ProjectAttachWorkspaceRequest) (*projectpb.AttachWorkspaceRequest, error) {
	if err := request.Validate(); err != nil {
		return nil, err
	}
	return &projectpb.AttachWorkspaceRequest{ProjectId: request.ProjectID, WorkspaceRoot: request.WorkspaceRoot}, nil
}

func ProjectAttachWorkspaceRequestFromProto(message *projectpb.AttachWorkspaceRequest) (serverapi.ProjectAttachWorkspaceRequest, error) {
	request := serverapi.ProjectAttachWorkspaceRequest{ProjectID: message.ProjectId, WorkspaceRoot: message.WorkspaceRoot}
	return request, nil
}

func ProjectAttachWorkspaceToProto(response serverapi.ProjectAttachWorkspaceResponse) (*projectpb.AttachWorkspaceSuccess, error) {
	if err := response.Validate(); err != nil {
		return nil, err
	}
	binding, err := projectMutationBindingToProto(response.Binding)
	if err != nil {
		return nil, err
	}
	outcome, err := projectWorkspaceAttachOutcomeToProto(response.Outcome)
	if err != nil {
		return nil, err
	}
	success := &projectpb.AttachWorkspaceSuccess{Binding: binding, Outcome: outcome}
	return success, Validate(success)
}

func ProjectAttachWorkspaceFromProto(success *projectpb.AttachWorkspaceSuccess) (serverapi.ProjectAttachWorkspaceResponse, error) {
	if err := Validate(success); err != nil {
		return serverapi.ProjectAttachWorkspaceResponse{}, err
	}
	binding, err := projectMutationBindingFromProto(success.Binding)
	if err != nil {
		return serverapi.ProjectAttachWorkspaceResponse{}, err
	}
	outcome, err := projectWorkspaceAttachOutcomeFromProto(success.Outcome)
	if err != nil {
		return serverapi.ProjectAttachWorkspaceResponse{}, err
	}
	response := serverapi.ProjectAttachWorkspaceResponse{Binding: binding, Outcome: outcome}
	return response, response.Validate()
}

func ProjectRebindWorkspaceRequestToProto(request serverapi.ProjectRebindWorkspaceRequest) (*projectpb.RebindWorkspaceRequest, error) {
	if err := request.Validate(); err != nil {
		return nil, err
	}
	return &projectpb.RebindWorkspaceRequest{
		OldWorkspaceRoot: request.OldWorkspaceRoot,
		NewWorkspaceRoot: request.NewWorkspaceRoot,
	}, nil
}

func ProjectRebindWorkspaceRequestFromProto(message *projectpb.RebindWorkspaceRequest) (serverapi.ProjectRebindWorkspaceRequest, error) {
	request := serverapi.ProjectRebindWorkspaceRequest{
		OldWorkspaceRoot: message.OldWorkspaceRoot,
		NewWorkspaceRoot: message.NewWorkspaceRoot,
	}
	return request, nil
}

func ProjectRebindWorkspaceToProto(response serverapi.ProjectRebindWorkspaceResponse) (*projectpb.RebindWorkspaceSuccess, error) {
	binding, err := projectMutationBindingToProto(response.Binding)
	if err != nil {
		return nil, err
	}
	success := &projectpb.RebindWorkspaceSuccess{Binding: binding}
	return success, Validate(success)
}

func ProjectRebindWorkspaceFromProto(success *projectpb.RebindWorkspaceSuccess) (serverapi.ProjectRebindWorkspaceResponse, error) {
	if err := Validate(success); err != nil {
		return serverapi.ProjectRebindWorkspaceResponse{}, err
	}
	binding, err := projectMutationBindingFromProto(success.Binding)
	if err != nil {
		return serverapi.ProjectRebindWorkspaceResponse{}, err
	}
	return serverapi.ProjectRebindWorkspaceResponse{Binding: binding}, nil
}

func WorkspaceNotRegisteredFromProto(details *projectpb.WorkspaceNotRegisteredDetails) error {
	if err := Validate(details); err != nil {
		return err
	}
	return serverapi.ErrWorkspaceNotRegistered
}

func WorkspacePathIdentityFromProto(details *projectpb.WorkspacePathIdentityDetails) error {
	if err := Validate(details); err != nil {
		return err
	}
	return serverapi.WorkspacePathIdentityError{WorkspaceRoot: details.WorkspaceRoot}
}

func WorkspaceMutationFromProto(details *projectpb.WorkspaceMutationDetails) error {
	if err := Validate(details); err != nil {
		return err
	}
	return &serverapi.WorkspaceMutationError{ProjectID: details.ProjectId, WorkspaceID: details.WorkspaceId}
}

func WorkspaceDetachConflictFromProto(details *projectpb.WorkspaceDetachConflictDetails) error {
	if err := Validate(details); err != nil {
		return err
	}
	return &serverapi.WorkspaceDetachConflictError{ProjectID: details.ProjectId, WorkspaceID: details.WorkspaceId}
}

func WorkspaceBindingAmbiguousMutationFromProto(details *projectpb.WorkspaceBindingAmbiguousMutationDetails) error {
	if err := Validate(details); err != nil {
		return err
	}
	return serverapi.WorkspaceBindingAmbiguousError{ProjectIDs: append([]string(nil), details.ProjectIds...)}
}

func projectWorkspaceSelectorToProto(selector serverapi.ProjectWorkspaceSelector) (*projectpb.ProjectWorkspaceSelector, error) {
	if err := selector.Validate(); err != nil {
		return nil, err
	}
	message := &projectpb.ProjectWorkspaceSelector{}
	if workspaceID := selector.WorkspaceIDValue(); workspaceID != nil {
		message.Selector = &projectpb.ProjectWorkspaceSelector_WorkspaceId{WorkspaceId: *workspaceID}
	} else if workspaceRoot := selector.WorkspaceRootValue(); workspaceRoot != nil {
		message.Selector = &projectpb.ProjectWorkspaceSelector_WorkspaceRoot{WorkspaceRoot: *workspaceRoot}
	}
	return message, Validate(message)
}

func projectWorkspaceSelectorFromProto(message *projectpb.ProjectWorkspaceSelector) (serverapi.ProjectWorkspaceSelector, error) {
	if err := Validate(message); err != nil {
		return serverapi.ProjectWorkspaceSelector{}, err
	}
	switch selector := message.Selector.(type) {
	case *projectpb.ProjectWorkspaceSelector_WorkspaceId:
		return serverapi.NewProjectWorkspaceSelectorForID(selector.WorkspaceId)
	case *projectpb.ProjectWorkspaceSelector_WorkspaceRoot:
		return serverapi.NewProjectWorkspaceSelectorForRoot(selector.WorkspaceRoot)
	default:
		return serverapi.ProjectWorkspaceSelector{}, errors.New("project workspace selector is required")
	}
}

func projectMutationBindingToProto(binding serverapi.ProjectBinding) (*projectpb.ProjectMutationBinding, error) {
	availability, err := projectAvailabilityToProto(clientui.ProjectAvailability(binding.WorkspaceStatus))
	if err != nil {
		return nil, err
	}
	message := &projectpb.ProjectMutationBinding{
		ProjectId:       binding.ProjectID,
		ProjectKey:      binding.ProjectKey,
		ProjectName:     binding.ProjectName,
		WorkspaceId:     binding.WorkspaceID,
		WorkspaceName:   binding.WorkspaceName,
		WorkspaceStatus: availability,
		CanonicalRoot:   binding.CanonicalRoot,
	}
	return message, Validate(message)
}

func projectMutationBindingFromProto(binding *projectpb.ProjectMutationBinding) (serverapi.ProjectBinding, error) {
	if err := Validate(binding); err != nil {
		return serverapi.ProjectBinding{}, err
	}
	availability, err := projectAvailabilityFromProto(binding.WorkspaceStatus)
	if err != nil {
		return serverapi.ProjectBinding{}, err
	}
	return serverapi.ProjectBinding{
		ProjectID: binding.ProjectId, ProjectKey: binding.ProjectKey, ProjectName: binding.ProjectName,
		WorkspaceID: binding.WorkspaceId, CanonicalRoot: binding.CanonicalRoot,
		WorkspaceName: binding.WorkspaceName, WorkspaceStatus: string(availability),
	}, nil
}

func workspaceUnlinkBlockerToProto(blocker serverapi.ProjectWorkspaceUnlinkBlocker) (*projectpb.WorkspaceUnlinkBlocker, error) {
	message := &projectpb.WorkspaceUnlinkBlocker{Code: blocker.Code}
	if blocker.Count > 0 {
		count, err := projectInt32(blocker.Count, "workspace unlink blocker count")
		if err != nil {
			return nil, err
		}
		message.Count = &count
	}
	return message, Validate(message)
}

func projectDeleteBlockerToProto(blocker serverapi.ProjectDeleteBlocker) (*projectpb.ProjectDeleteBlocker, error) {
	count, err := projectInt32(blocker.Count, "project delete blocker count")
	if err != nil {
		return nil, err
	}
	message := &projectpb.ProjectDeleteBlocker{Code: blocker.Code, Count: count}
	return message, Validate(message)
}

func projectWorkspaceAttachOutcomeToProto(value serverapi.ProjectWorkspaceAttachOutcome) (projectpb.ProjectWorkspaceAttachOutcome, error) {
	switch value {
	case serverapi.ProjectWorkspaceAttachOutcomeAttached:
		return projectpb.ProjectWorkspaceAttachOutcome_PROJECT_WORKSPACE_ATTACH_OUTCOME_ATTACHED, nil
	case serverapi.ProjectWorkspaceAttachOutcomeAlreadyAttached:
		return projectpb.ProjectWorkspaceAttachOutcome_PROJECT_WORKSPACE_ATTACH_OUTCOME_ALREADY_ATTACHED, nil
	default:
		return 0, fmt.Errorf("project workspace attach outcome %q is unsupported", value)
	}
}

func projectWorkspaceAttachOutcomeFromProto(value projectpb.ProjectWorkspaceAttachOutcome) (serverapi.ProjectWorkspaceAttachOutcome, error) {
	switch value {
	case projectpb.ProjectWorkspaceAttachOutcome_PROJECT_WORKSPACE_ATTACH_OUTCOME_ATTACHED:
		return serverapi.ProjectWorkspaceAttachOutcomeAttached, nil
	case projectpb.ProjectWorkspaceAttachOutcome_PROJECT_WORKSPACE_ATTACH_OUTCOME_ALREADY_ATTACHED:
		return serverapi.ProjectWorkspaceAttachOutcomeAlreadyAttached, nil
	default:
		return "", fmt.Errorf("protobuf project workspace attach outcome %v is unsupported", value)
	}
}
