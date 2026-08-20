package client

import (
	"context"
	"fmt"

	"core/shared/protoapi"
	projectpb "core/shared/protoapi/gen/kent/api/project"
	sharedpb "core/shared/protoapi/gen/kent/api/shared"
	"core/shared/serverapi"

	"google.golang.org/protobuf/proto"
)

func (c *Remote) CreateProject(ctx context.Context, req serverapi.ProjectCreateRequest) (serverapi.ProjectCreateResponse, error) {
	request, err := protoapi.ProjectCreateRequestToProto(req)
	if err != nil {
		return serverapi.ProjectCreateResponse{}, err
	}
	result := &projectpb.CreateProjectResult{}
	if err := c.callBinary(ctx, projectCatalogMethod("Create"), request, result); err != nil {
		return serverapi.ProjectCreateResponse{}, err
	}
	if failure := result.GetError(); failure != nil {
		return serverapi.ProjectCreateResponse{}, projectCreateGeneratedError(failure)
	}
	return protoapi.ProjectCreateFromProto(result.GetSuccess())
}

func (c *Remote) UpdateProject(ctx context.Context, req serverapi.ProjectUpdateRequest) (serverapi.ProjectUpdateResponse, error) {
	request, err := protoapi.ProjectUpdateRequestToProto(req)
	if err != nil {
		return serverapi.ProjectUpdateResponse{}, err
	}
	result := &projectpb.UpdateProjectResult{}
	if err := c.callBinary(ctx, projectCatalogMethod("Update"), request, result); err != nil {
		return serverapi.ProjectUpdateResponse{}, err
	}
	if failure := result.GetError(); failure != nil {
		return serverapi.ProjectUpdateResponse{}, projectUpdateGeneratedError(failure)
	}
	return protoapi.ProjectUpdateFromProto(result.GetSuccess())
}

func (c *Remote) SetDefaultWorkspace(ctx context.Context, req serverapi.ProjectDefaultWorkspaceSetRequest) (serverapi.ProjectDefaultWorkspaceSetResponse, error) {
	request, err := protoapi.ProjectDefaultWorkspaceRequestToProto(req)
	if err != nil {
		return serverapi.ProjectDefaultWorkspaceSetResponse{}, err
	}
	result := &projectpb.SetDefaultWorkspaceResult{}
	if err := c.callBinary(ctx, projectCatalogMethod("SetDefaultWorkspace"), request, result); err != nil {
		return serverapi.ProjectDefaultWorkspaceSetResponse{}, err
	}
	if failure := result.GetError(); failure != nil {
		return serverapi.ProjectDefaultWorkspaceSetResponse{}, projectWorkspaceMutationGeneratedError(
			failure.Code,
			failure.GetProjectNotFound(),
			failure.GetWorkspaceNotRegistered(),
			failure.GetWorkspacePathIdentity(),
			nil,
			failure.GetWorkspaceMutationFailed(),
			failure.GetInternalFailure(),
		)
	}
	return protoapi.ProjectDefaultWorkspaceFromProto(result.GetSuccess())
}

func (c *Remote) UnlinkWorkspaceFromProject(ctx context.Context, req serverapi.ProjectWorkspaceUnlinkRequest) (serverapi.ProjectWorkspaceUnlinkResponse, error) {
	request, err := protoapi.ProjectWorkspaceUnlinkRequestToProto(req)
	if err != nil {
		return serverapi.ProjectWorkspaceUnlinkResponse{}, err
	}
	result := &projectpb.UnlinkWorkspaceResult{}
	if err := c.callBinary(ctx, projectCatalogMethod("UnlinkWorkspace"), request, result); err != nil {
		return serverapi.ProjectWorkspaceUnlinkResponse{}, err
	}
	if failure := result.GetError(); failure != nil {
		return serverapi.ProjectWorkspaceUnlinkResponse{}, projectWorkspaceMutationGeneratedError(
			failure.Code,
			failure.GetProjectNotFound(),
			failure.GetWorkspaceNotRegistered(),
			failure.GetWorkspacePathIdentity(),
			failure.GetWorkspaceDetachConflict(),
			failure.GetWorkspaceMutationFailed(),
			failure.GetInternalFailure(),
		)
	}
	return protoapi.ProjectWorkspaceUnlinkFromProto(result.GetSuccess())
}

func (c *Remote) DeleteProject(ctx context.Context, req serverapi.ProjectDeleteRequest) (serverapi.ProjectDeleteResponse, error) {
	request, err := protoapi.ProjectDeleteRequestToProto(req)
	if err != nil {
		return serverapi.ProjectDeleteResponse{}, err
	}
	result := &projectpb.DeleteProjectResult{}
	if err := c.callBinary(ctx, projectCatalogMethod("Delete"), request, result); err != nil {
		return serverapi.ProjectDeleteResponse{}, err
	}
	if failure := result.GetError(); failure != nil {
		return serverapi.ProjectDeleteResponse{}, projectNotFoundGeneratedError(
			failure.Code,
			failure.GetProjectNotFound(),
			failure.GetInternalFailure(),
		)
	}
	return protoapi.ProjectDeleteFromProto(result.GetSuccess())
}

func (c *Remote) AttachWorkspaceToProject(ctx context.Context, req serverapi.ProjectAttachWorkspaceRequest) (serverapi.ProjectAttachWorkspaceResponse, error) {
	request, err := protoapi.ProjectAttachWorkspaceRequestToProto(req)
	if err != nil {
		return serverapi.ProjectAttachWorkspaceResponse{}, err
	}
	result := &projectpb.AttachWorkspaceResult{}
	if err := c.callBinary(ctx, projectCatalogMethod("AttachWorkspace"), request, result); err != nil {
		return serverapi.ProjectAttachWorkspaceResponse{}, err
	}
	if failure := result.GetError(); failure != nil {
		return serverapi.ProjectAttachWorkspaceResponse{}, projectAttachGeneratedError(failure)
	}
	response, err := protoapi.ProjectAttachWorkspaceFromProto(result.GetSuccess())
	if err != nil {
		return serverapi.ProjectAttachWorkspaceResponse{}, fmt.Errorf("Project Workspace attach response is invalid: %w", err)
	}
	if response.Binding.ProjectID != req.ProjectID {
		return serverapi.ProjectAttachWorkspaceResponse{}, fmt.Errorf(
			"Project Workspace attach response project %q does not match request project %q",
			response.Binding.ProjectID,
			req.ProjectID,
		)
	}
	return response, nil
}

func (c *Remote) RebindWorkspace(ctx context.Context, req serverapi.ProjectRebindWorkspaceRequest) (serverapi.ProjectRebindWorkspaceResponse, error) {
	request, err := protoapi.ProjectRebindWorkspaceRequestToProto(req)
	if err != nil {
		return serverapi.ProjectRebindWorkspaceResponse{}, err
	}
	result := &projectpb.RebindWorkspaceResult{}
	if err := c.callBinary(ctx, projectCatalogMethod("RebindWorkspace"), request, result); err != nil {
		return serverapi.ProjectRebindWorkspaceResponse{}, err
	}
	if failure := result.GetError(); failure != nil {
		switch failure.Code {
		case "workspace_not_registered":
			return serverapi.ProjectRebindWorkspaceResponse{}, protoapi.WorkspaceNotRegisteredFromProto(failure.GetWorkspaceNotRegistered())
		case "workspace_binding_ambiguous":
			return serverapi.ProjectRebindWorkspaceResponse{}, protoapi.WorkspaceBindingAmbiguousMutationFromProto(failure.GetWorkspaceBindingAmbiguous())
		case "workspace_already_bound":
			return serverapi.ProjectRebindWorkspaceResponse{}, validateEmptyProjectMutationDetail(failure.GetWorkspaceAlreadyBound(), serverapi.ErrWorkspaceAlreadyBound)
		case "workspace_path_missing":
			return serverapi.ProjectRebindWorkspaceResponse{}, validateEmptyProjectMutationDetail(failure.GetWorkspacePathMissing(), serverapi.ErrWorkspacePathMissing)
		case "internal_failure":
			return serverapi.ProjectRebindWorkspaceResponse{}, protoapi.InternalFailureFromProto(failure.GetInternalFailure())
		default:
			return serverapi.ProjectRebindWorkspaceResponse{}, generatedOperationFailure(failure.Code)
		}
	}
	return protoapi.ProjectRebindWorkspaceFromProto(result.GetSuccess())
}

func projectCreateGeneratedError(failure *projectpb.CreateProjectError) error {
	switch failure.Code {
	case "auth_required":
		return serverapi.ErrServerAuthRequired
	case "project_key_conflict":
		return projectKeyConflictError(failure.GetProjectKeyConflict())
	case "workspace_already_bound":
		return validateEmptyProjectMutationDetail(failure.GetWorkspaceAlreadyBound(), serverapi.ErrWorkspaceAlreadyBound)
	case "workspace_path_missing":
		return validateEmptyProjectMutationDetail(failure.GetWorkspacePathMissing(), serverapi.ErrWorkspacePathMissing)
	case "internal_failure":
		return protoapi.InternalFailureFromProto(failure.GetInternalFailure())
	default:
		return generatedOperationFailure(failure.Code)
	}
}

func projectUpdateGeneratedError(failure *projectpb.UpdateProjectError) error {
	switch failure.Code {
	case "auth_required":
		return serverapi.ErrServerAuthRequired
	case "project_not_found":
		return projectNotFoundError(failure.GetProjectNotFound())
	case "project_key_conflict":
		return projectKeyConflictError(failure.GetProjectKeyConflict())
	case "internal_failure":
		return protoapi.InternalFailureFromProto(failure.GetInternalFailure())
	default:
		return generatedOperationFailure(failure.Code)
	}
}

func projectAttachGeneratedError(failure *projectpb.AttachWorkspaceError) error {
	switch failure.Code {
	case "auth_required":
		return serverapi.ErrServerAuthRequired
	case "project_not_found":
		return projectNotFoundError(failure.GetProjectNotFound())
	case "workspace_already_bound":
		return validateEmptyProjectMutationDetail(failure.GetWorkspaceAlreadyBound(), serverapi.ErrWorkspaceAlreadyBound)
	case "workspace_path_missing":
		return validateEmptyProjectMutationDetail(failure.GetWorkspacePathMissing(), serverapi.ErrWorkspacePathMissing)
	case "internal_failure":
		return protoapi.InternalFailureFromProto(failure.GetInternalFailure())
	default:
		return generatedOperationFailure(failure.Code)
	}
}

func projectWorkspaceMutationGeneratedError(
	code string,
	notFound *projectpb.ProjectNotFoundDetails,
	notRegistered *projectpb.WorkspaceNotRegisteredDetails,
	pathIdentity *projectpb.WorkspacePathIdentityDetails,
	detachConflict *projectpb.WorkspaceDetachConflictDetails,
	mutation *projectpb.WorkspaceMutationDetails,
	internal *sharedpb.InternalFailureDetails,
) error {
	switch code {
	case "auth_required":
		return serverapi.ErrServerAuthRequired
	case "project_not_found":
		return projectNotFoundError(notFound)
	case "workspace_not_registered":
		return protoapi.WorkspaceNotRegisteredFromProto(notRegistered)
	case "workspace_path_identity":
		return protoapi.WorkspacePathIdentityFromProto(pathIdentity)
	case "workspace_detach_conflict":
		return protoapi.WorkspaceDetachConflictFromProto(detachConflict)
	case "workspace_mutation_failed":
		return protoapi.WorkspaceMutationFromProto(mutation)
	case "internal_failure":
		return protoapi.InternalFailureFromProto(internal)
	default:
		return generatedOperationFailure(code)
	}
}

func projectKeyConflictError(details *projectpb.ProjectKeyConflictDetails) error {
	if err := protoapi.Validate(details); err != nil {
		return err
	}
	return serverapi.ProjectKeyConflictError{ProjectKey: details.ProjectKey}
}

func validateEmptyProjectMutationDetail(detail proto.Message, sentinel error) error {
	if err := protoapi.Validate(detail); err != nil {
		return err
	}
	return sentinel
}
