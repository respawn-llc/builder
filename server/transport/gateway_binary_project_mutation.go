package transport

import (
	"errors"

	"core/shared/apicontract"
	projectpb "core/shared/protoapi/gen/kent/api/project"
	"core/shared/serverapi"
	"google.golang.org/protobuf/proto"
)

func registerProjectMutationGatewayBinaryBindings(bindings map[string]gatewayBinaryBinding) error {
	service := projectpb.File_kent_api_project_project_proto.Services().ByName("ProjectCatalogService")
	return errors.Join(
		registerProjectViewUnary(bindings, service, "Create",
			func() *projectpb.CreateProjectRequest { return &projectpb.CreateProjectRequest{} },
			apicontract.ProjectViewService.CreateProject, binaryProjectCreateFailure),
		registerProjectViewUnary(bindings, service, "Update",
			func() *projectpb.UpdateProjectRequest { return &projectpb.UpdateProjectRequest{} },
			apicontract.ProjectViewService.UpdateProject, binaryProjectUpdateFailure),
		registerProjectViewUnary(bindings, service, "SetDefaultWorkspace",
			func() *projectpb.SetDefaultWorkspaceRequest { return &projectpb.SetDefaultWorkspaceRequest{} },
			apicontract.ProjectViewService.SetDefaultWorkspace, binaryProjectSetDefaultWorkspaceFailure),
		registerProjectViewUnary(bindings, service, "AttachWorkspace",
			func() *projectpb.AttachWorkspaceRequest { return &projectpb.AttachWorkspaceRequest{} },
			apicontract.ProjectViewService.AttachWorkspaceToProject, binaryProjectAttachWorkspaceFailure),
		registerProjectViewUnary(bindings, service, "RebindWorkspace",
			func() *projectpb.RebindWorkspaceRequest { return &projectpb.RebindWorkspaceRequest{} },
			apicontract.ProjectViewService.RebindWorkspace, binaryProjectRebindWorkspaceFailure),
		registerProjectViewUnary(bindings, service, "UnlinkWorkspace",
			func() *projectpb.UnlinkWorkspaceRequest { return &projectpb.UnlinkWorkspaceRequest{} },
			apicontract.ProjectViewService.UnlinkWorkspaceFromProject, binaryProjectUnlinkWorkspaceFailure),
		registerProjectViewUnary(bindings, service, "Delete",
			func() *projectpb.DeleteProjectRequest { return &projectpb.DeleteProjectRequest{} },
			apicontract.ProjectViewService.DeleteProject, binaryProjectDeleteFailure),
	)
}

func binaryProjectCreateFailure(_ *projectpb.CreateProjectRequest, err error) proto.Message {
	switch {
	case errors.Is(err, serverapi.ErrServerAuthRequired):
		return &projectpb.AuthRequiredDetails{}
	case errors.Is(err, serverapi.ErrProjectKeyConflict):
		if conflict, ok := serverapi.AsProjectKeyConflict(err); ok {
			return &projectpb.ProjectKeyConflictDetails{ProjectKey: conflict.ProjectKey}
		}
	case errors.Is(err, serverapi.ErrWorkspaceAlreadyBound):
		return &projectpb.WorkspaceAlreadyBoundDetails{}
	case errors.Is(err, serverapi.ErrWorkspacePathMissing):
		return &projectpb.WorkspacePathMissingDetails{}
	}
	return binaryInternalFailure(err)
}

func binaryProjectUpdateFailure(request *projectpb.UpdateProjectRequest, err error) proto.Message {
	switch {
	case errors.Is(err, serverapi.ErrServerAuthRequired):
		return &projectpb.AuthRequiredDetails{}
	case errors.Is(err, serverapi.ErrProjectNotFound) && request != nil:
		return &projectpb.ProjectNotFoundDetails{ProjectId: request.ProjectId}
	case errors.Is(err, serverapi.ErrProjectKeyConflict):
		if conflict, ok := serverapi.AsProjectKeyConflict(err); ok {
			return &projectpb.ProjectKeyConflictDetails{ProjectKey: conflict.ProjectKey}
		}
	}
	return binaryInternalFailure(err)
}

func binaryProjectSetDefaultWorkspaceFailure(request *projectpb.SetDefaultWorkspaceRequest, err error) proto.Message {
	switch {
	case errors.Is(err, serverapi.ErrServerAuthRequired):
		return &projectpb.AuthRequiredDetails{}
	case errors.Is(err, serverapi.ErrProjectNotFound) && request != nil:
		return &projectpb.ProjectNotFoundDetails{ProjectId: request.ProjectId}
	case errors.Is(err, serverapi.ErrWorkspaceNotRegistered) && request != nil:
		return workspaceNotRegisteredDetails(request.ProjectId, request.Workspace)
	case errors.Is(err, serverapi.ErrWorkspacePathIdentity):
		if identity, ok := serverapi.AsWorkspacePathIdentity(err); ok {
			return &projectpb.WorkspacePathIdentityDetails{WorkspaceRoot: identity.WorkspaceRoot}
		}
	case errors.Is(err, serverapi.ErrWorkspaceMutationFailed):
		if details := workspaceMutationDetails(err); details != nil {
			return details
		}
	}
	return binaryInternalFailure(err)
}

func binaryProjectUnlinkWorkspaceFailure(request *projectpb.UnlinkWorkspaceRequest, err error) proto.Message {
	switch {
	case errors.Is(err, serverapi.ErrServerAuthRequired):
		return &projectpb.AuthRequiredDetails{}
	case errors.Is(err, serverapi.ErrProjectNotFound) && request != nil:
		return &projectpb.ProjectNotFoundDetails{ProjectId: request.ProjectId}
	case errors.Is(err, serverapi.ErrWorkspaceNotRegistered) && request != nil:
		return workspaceNotRegisteredDetails(request.ProjectId, request.Workspace)
	case errors.Is(err, serverapi.ErrWorkspacePathIdentity):
		if identity, ok := serverapi.AsWorkspacePathIdentity(err); ok {
			return &projectpb.WorkspacePathIdentityDetails{WorkspaceRoot: identity.WorkspaceRoot}
		}
	case errors.Is(err, serverapi.ErrWorkspaceDetachConflict):
		var conflict *serverapi.WorkspaceDetachConflictError
		if errors.As(err, &conflict) && conflict != nil {
			return &projectpb.WorkspaceDetachConflictDetails{
				ProjectId: conflict.ProjectID, WorkspaceId: conflict.WorkspaceID, Retryable: true,
			}
		}
	case errors.Is(err, serverapi.ErrWorkspaceMutationFailed):
		if details := workspaceMutationDetails(err); details != nil {
			return details
		}
	}
	return binaryInternalFailure(err)
}

func binaryProjectDeleteFailure(request *projectpb.DeleteProjectRequest, err error) proto.Message {
	if errors.Is(err, serverapi.ErrServerAuthRequired) {
		return &projectpb.AuthRequiredDetails{}
	}
	if errors.Is(err, serverapi.ErrProjectNotFound) && request != nil {
		return &projectpb.ProjectNotFoundDetails{ProjectId: request.ProjectId}
	}
	return binaryInternalFailure(err)
}

func binaryProjectAttachWorkspaceFailure(request *projectpb.AttachWorkspaceRequest, err error) proto.Message {
	switch {
	case errors.Is(err, serverapi.ErrServerAuthRequired):
		return &projectpb.AuthRequiredDetails{}
	case errors.Is(err, serverapi.ErrProjectNotFound) && request != nil:
		return &projectpb.ProjectNotFoundDetails{ProjectId: request.ProjectId}
	case errors.Is(err, serverapi.ErrWorkspaceAlreadyBound):
		return &projectpb.WorkspaceAlreadyBoundDetails{}
	case errors.Is(err, serverapi.ErrWorkspacePathMissing):
		return &projectpb.WorkspacePathMissingDetails{}
	}
	return binaryInternalFailure(err)
}

func binaryProjectRebindWorkspaceFailure(request *projectpb.RebindWorkspaceRequest, err error) proto.Message {
	switch {
	case errors.Is(err, serverapi.ErrServerAuthRequired):
		return &projectpb.AuthRequiredDetails{}
	case errors.Is(err, serverapi.ErrWorkspaceNotRegistered) && request != nil:
		root := request.OldWorkspaceRoot
		return &projectpb.WorkspaceNotRegisteredDetails{WorkspaceRoot: &root}
	case errors.Is(err, serverapi.ErrWorkspaceBindingAmbiguous):
		if ambiguous, ok := serverapi.AsWorkspaceBindingAmbiguous(err); ok {
			return &projectpb.WorkspaceBindingAmbiguousMutationDetails{
				ProjectIds: append([]string(nil), ambiguous.ProjectIDs...),
			}
		}
	case errors.Is(err, serverapi.ErrWorkspaceAlreadyBound):
		return &projectpb.WorkspaceAlreadyBoundDetails{}
	case errors.Is(err, serverapi.ErrWorkspacePathMissing):
		return &projectpb.WorkspacePathMissingDetails{}
	}
	return binaryInternalFailure(err)
}

func workspaceNotRegisteredDetails(projectID string, selector *projectpb.ProjectWorkspaceSelector) *projectpb.WorkspaceNotRegisteredDetails {
	details := &projectpb.WorkspaceNotRegisteredDetails{ProjectId: &projectID}
	if selector == nil {
		return details
	}
	switch value := selector.Selector.(type) {
	case *projectpb.ProjectWorkspaceSelector_WorkspaceId:
		details.WorkspaceId = &value.WorkspaceId
	case *projectpb.ProjectWorkspaceSelector_WorkspaceRoot:
		details.WorkspaceRoot = &value.WorkspaceRoot
	}
	return details
}

func workspaceMutationDetails(err error) *projectpb.WorkspaceMutationDetails {
	var mutation *serverapi.WorkspaceMutationError
	if !errors.As(err, &mutation) || mutation == nil {
		return nil
	}
	return &projectpb.WorkspaceMutationDetails{ProjectId: mutation.ProjectID, WorkspaceId: mutation.WorkspaceID}
}
