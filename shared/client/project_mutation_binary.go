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

func (c *Remote) CreateProject(ctx context.Context, request *projectpb.CreateProjectRequest) (*projectpb.CreateProjectSuccess, error) {
	return callGeneratedBinary(c, ctx, projectCatalogMethod("Create"), request,
		&projectpb.CreateProjectResult{},
		projectCreateGeneratedError)
}

func (c *Remote) UpdateProject(ctx context.Context, request *projectpb.UpdateProjectRequest) (*projectpb.UpdateProjectSuccess, error) {
	return callGeneratedBinary(c, ctx, projectCatalogMethod("Update"), request,
		&projectpb.UpdateProjectResult{},
		projectUpdateGeneratedError)
}

func (c *Remote) SetDefaultWorkspace(ctx context.Context, request *projectpb.SetDefaultWorkspaceRequest) (*projectpb.SetDefaultWorkspaceSuccess, error) {
	return callGeneratedBinary(c, ctx, projectCatalogMethod("SetDefaultWorkspace"), request,
		&projectpb.SetDefaultWorkspaceResult{},
		func(failure *projectpb.SetDefaultWorkspaceError) error {
			return projectWorkspaceMutationGeneratedError(
				failure.Code,
				failure.GetProjectNotFound(),
				failure.GetWorkspaceNotRegistered(),
				failure.GetWorkspacePathIdentity(),
				nil,
				failure.GetWorkspaceMutationFailed(),
				failure.GetInternalFailure(),
			)
		})
}

func (c *Remote) UnlinkWorkspaceFromProject(ctx context.Context, request *projectpb.UnlinkWorkspaceRequest) (*projectpb.UnlinkWorkspaceSuccess, error) {
	response, err := callGeneratedBinary(c, ctx, projectCatalogMethod("UnlinkWorkspace"), request,
		&projectpb.UnlinkWorkspaceResult{},
		func(failure *projectpb.UnlinkWorkspaceError) error {
			return projectWorkspaceMutationGeneratedError(
				failure.Code,
				failure.GetProjectNotFound(),
				failure.GetWorkspaceNotRegistered(),
				failure.GetWorkspacePathIdentity(),
				failure.GetWorkspaceDetachConflict(),
				failure.GetWorkspaceMutationFailed(),
				failure.GetInternalFailure(),
			)
		})
	if err != nil {
		return nil, err
	}
	if response.ProjectId != request.ProjectId {
		return nil, fmt.Errorf(
			"Project Workspace unlink response project %q does not match request project %q",
			response.ProjectId,
			request.ProjectId,
		)
	}
	return response, nil
}

func (c *Remote) DeleteProject(ctx context.Context, request *projectpb.DeleteProjectRequest) (*projectpb.DeleteProjectSuccess, error) {
	response, err := callGeneratedBinary(c, ctx, projectCatalogMethod("Delete"), request,
		&projectpb.DeleteProjectResult{},
		func(failure *projectpb.DeleteProjectError) error {
			if failure.Code == "auth_required" {
				return serverapi.ErrServerAuthRequired
			}
			return projectNotFoundGeneratedError(
				failure.Code, failure.GetProjectNotFound(), failure.GetInternalFailure())
		})
	if err != nil {
		return nil, err
	}
	if response.ProjectId != request.ProjectId {
		return nil, fmt.Errorf(
			"Project delete response project %q does not match request project %q",
			response.ProjectId,
			request.ProjectId,
		)
	}
	return response, nil
}

func (c *Remote) AttachWorkspaceToProject(ctx context.Context, request *projectpb.AttachWorkspaceRequest) (*projectpb.AttachWorkspaceSuccess, error) {
	response, err := callGeneratedBinary(c, ctx, projectCatalogMethod("AttachWorkspace"), request,
		&projectpb.AttachWorkspaceResult{},
		projectAttachGeneratedError)
	if err != nil {
		return nil, err
	}
	if response.Binding.ProjectId != request.ProjectId {
		return nil, fmt.Errorf(
			"Project Workspace attach response project %q does not match request project %q",
			response.Binding.ProjectId,
			request.ProjectId,
		)
	}
	return response, nil
}

func (c *Remote) RebindWorkspace(ctx context.Context, request *projectpb.RebindWorkspaceRequest) (*projectpb.RebindWorkspaceSuccess, error) {
	return callGeneratedBinary(c, ctx, projectCatalogMethod("RebindWorkspace"), request,
		&projectpb.RebindWorkspaceResult{},
		func(failure *projectpb.RebindWorkspaceError) error {
			switch failure.Code {
			case "auth_required":
				return serverapi.ErrServerAuthRequired
			case "workspace_not_registered":
				return workspaceNotRegisteredError(failure.GetWorkspaceNotRegistered())
			case "workspace_binding_ambiguous":
				return workspaceBindingAmbiguousMutationError(failure.GetWorkspaceBindingAmbiguous())
			case "workspace_already_bound":
				return validateEmptyProjectMutationDetail(
					failure.GetWorkspaceAlreadyBound(), serverapi.ErrWorkspaceAlreadyBound)
			case "workspace_path_missing":
				return validateEmptyProjectMutationDetail(
					failure.GetWorkspacePathMissing(), serverapi.ErrWorkspacePathMissing)
			case "internal_failure":
				return protoapi.InternalFailureFromProto(failure.GetInternalFailure())
			default:
				return generatedOperationFailure(failure.Code)
			}
		})
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
		return workspaceNotRegisteredError(notRegistered)
	case "workspace_path_identity":
		return workspacePathIdentityError(pathIdentity)
	case "workspace_detach_conflict":
		return workspaceDetachConflictError(detachConflict)
	case "workspace_mutation_failed":
		return workspaceMutationError(mutation)
	case "internal_failure":
		return protoapi.InternalFailureFromProto(internal)
	default:
		return generatedOperationFailure(code)
	}
}

func workspaceNotRegisteredError(details *projectpb.WorkspaceNotRegisteredDetails) error {
	if err := protoapi.Validate(details); err != nil {
		return err
	}
	return serverapi.ErrWorkspaceNotRegistered
}

func workspaceBindingAmbiguousMutationError(details *projectpb.WorkspaceBindingAmbiguousMutationDetails) error {
	if err := protoapi.Validate(details); err != nil {
		return err
	}
	return serverapi.WorkspaceBindingAmbiguousError{ProjectIDs: append([]string(nil), details.ProjectIds...)}
}

func workspaceBindingAmbiguousError(details *projectpb.WorkspaceBindingAmbiguousDetails) error {
	if err := protoapi.Validate(details); err != nil {
		return err
	}
	return serverapi.WorkspaceBindingAmbiguousError{
		CanonicalRoot: details.CanonicalRoot,
		ProjectIDs:    append([]string(nil), details.ProjectIds...),
	}
}

func projectUnavailableError(details *projectpb.ProjectUnavailableDetails) error {
	if err := protoapi.Validate(details); err != nil {
		return err
	}
	availability, err := ProjectAvailabilityFromProto(details.Availability)
	if err != nil {
		return err
	}
	return serverapi.ProjectUnavailableError{
		ProjectID:    details.ProjectId,
		RootPath:     details.RootPath,
		Availability: availability,
	}
}

func workspacePathIdentityError(details *projectpb.WorkspacePathIdentityDetails) error {
	if err := protoapi.Validate(details); err != nil {
		return err
	}
	return serverapi.WorkspacePathIdentityError{WorkspaceRoot: details.WorkspaceRoot}
}

func workspaceDetachConflictError(details *projectpb.WorkspaceDetachConflictDetails) error {
	if err := protoapi.Validate(details); err != nil {
		return err
	}
	return &serverapi.WorkspaceDetachConflictError{ProjectID: details.ProjectId, WorkspaceID: details.WorkspaceId}
}

func workspaceMutationError(details *projectpb.WorkspaceMutationDetails) error {
	if err := protoapi.Validate(details); err != nil {
		return err
	}
	return &serverapi.WorkspaceMutationError{ProjectID: details.ProjectId, WorkspaceID: details.WorkspaceId}
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
