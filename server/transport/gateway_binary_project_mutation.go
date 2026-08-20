package transport

import (
	"context"
	"errors"

	"core/shared/apicontract"
	"core/shared/protoapi"
	projectpb "core/shared/protoapi/gen/kent/api/project"
	"core/shared/serverapi"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func registerProjectMutationGatewayBinaryBindings(bindings map[string]gatewayBinaryBinding) error {
	service := projectpb.File_kent_api_project_project_proto.Services().ByName("ProjectCatalogService")
	registrations := []struct {
		name    protoreflect.Name
		request func() proto.Message
		invoke  func(*Gateway, context.Context, *connectionState, proto.Message) (proto.Message, error)
		failure func(proto.Message, error) proto.Message
	}{
		{"Create", func() proto.Message { return &projectpb.CreateProjectRequest{} }, invokeBinaryProjectCreate, binaryProjectCreateFailure},
		{"Update", func() proto.Message { return &projectpb.UpdateProjectRequest{} }, invokeBinaryProjectUpdate, binaryProjectUpdateFailure},
		{"SetDefaultWorkspace", func() proto.Message { return &projectpb.SetDefaultWorkspaceRequest{} }, invokeBinaryProjectSetDefaultWorkspace, binaryProjectSetDefaultWorkspaceFailure},
		{"AttachWorkspace", func() proto.Message { return &projectpb.AttachWorkspaceRequest{} }, invokeBinaryProjectAttachWorkspace, binaryProjectAttachWorkspaceFailure},
		{"RebindWorkspace", func() proto.Message { return &projectpb.RebindWorkspaceRequest{} }, invokeBinaryProjectRebindWorkspace, binaryProjectRebindWorkspaceFailure},
		{"UnlinkWorkspace", func() proto.Message { return &projectpb.UnlinkWorkspaceRequest{} }, invokeBinaryProjectUnlinkWorkspace, binaryProjectUnlinkWorkspaceFailure},
		{"Delete", func() proto.Message { return &projectpb.DeleteProjectRequest{} }, invokeBinaryProjectDelete, binaryProjectDeleteFailure},
	}
	for _, registration := range registrations {
		if err := registerGatewayBinaryBinding(
			bindings,
			service,
			registration.name,
			apicontract.DependencyProjectView,
			registration.request,
			nil,
			registration.invoke,
			func(err error) proto.Message { return registration.failure(nil, err) },
		); err != nil {
			return err
		}
		operation, err := protoapi.OperationFromDescriptor(service.Methods().ByName(registration.name))
		if err != nil {
			return err
		}
		binding := bindings[operation.Name]
		binding.failure = func(_ *Gateway, _ *connectionState, message proto.Message, err error) proto.Message {
			return registration.failure(message, err)
		}
		bindings[operation.Name] = binding
	}
	return nil
}

func invokeBinaryProjectCreate(g *Gateway, ctx context.Context, _ *connectionState, message proto.Message) (proto.Message, error) {
	request, err := protoapi.ProjectCreateRequestFromProto(message.(*projectpb.CreateProjectRequest))
	if err != nil {
		return nil, err
	}
	client, err := projectViewClient(g)
	if err != nil {
		return nil, err
	}
	response, err := client.CreateProject(ctx, request)
	if err != nil {
		return nil, err
	}
	success, err := protoapi.ProjectCreateToProto(response)
	if err != nil {
		return nil, err
	}
	return &projectpb.CreateProjectResult{Outcome: &projectpb.CreateProjectResult_Success{Success: success}}, nil
}

func invokeBinaryProjectUpdate(g *Gateway, ctx context.Context, _ *connectionState, message proto.Message) (proto.Message, error) {
	request, err := protoapi.ProjectUpdateRequestFromProto(message.(*projectpb.UpdateProjectRequest))
	if err != nil {
		return nil, err
	}
	client, err := projectViewClient(g)
	if err != nil {
		return nil, err
	}
	response, err := client.UpdateProject(ctx, request)
	if err != nil {
		return nil, err
	}
	success, err := protoapi.ProjectUpdateToProto(response)
	if err != nil {
		return nil, err
	}
	return &projectpb.UpdateProjectResult{Outcome: &projectpb.UpdateProjectResult_Success{Success: success}}, nil
}

func invokeBinaryProjectSetDefaultWorkspace(g *Gateway, ctx context.Context, _ *connectionState, message proto.Message) (proto.Message, error) {
	request, err := protoapi.ProjectDefaultWorkspaceRequestFromProto(message.(*projectpb.SetDefaultWorkspaceRequest))
	if err != nil {
		return nil, err
	}
	client, err := projectViewClient(g)
	if err != nil {
		return nil, err
	}
	response, err := client.SetDefaultWorkspace(ctx, request)
	if err != nil {
		return nil, err
	}
	success, err := protoapi.ProjectDefaultWorkspaceToProto(response)
	if err != nil {
		return nil, err
	}
	return &projectpb.SetDefaultWorkspaceResult{Outcome: &projectpb.SetDefaultWorkspaceResult_Success{Success: success}}, nil
}

func invokeBinaryProjectAttachWorkspace(g *Gateway, ctx context.Context, _ *connectionState, message proto.Message) (proto.Message, error) {
	request, err := protoapi.ProjectAttachWorkspaceRequestFromProto(message.(*projectpb.AttachWorkspaceRequest))
	if err != nil {
		return nil, err
	}
	client, err := projectViewClient(g)
	if err != nil {
		return nil, err
	}
	response, err := client.AttachWorkspaceToProject(ctx, request)
	if err != nil {
		return nil, err
	}
	success, err := protoapi.ProjectAttachWorkspaceToProto(response)
	if err != nil {
		return nil, err
	}
	return &projectpb.AttachWorkspaceResult{Outcome: &projectpb.AttachWorkspaceResult_Success{Success: success}}, nil
}

func invokeBinaryProjectRebindWorkspace(g *Gateway, ctx context.Context, _ *connectionState, message proto.Message) (proto.Message, error) {
	request, err := protoapi.ProjectRebindWorkspaceRequestFromProto(message.(*projectpb.RebindWorkspaceRequest))
	if err != nil {
		return nil, err
	}
	client, err := projectViewClient(g)
	if err != nil {
		return nil, err
	}
	response, err := client.RebindWorkspace(ctx, request)
	if err != nil {
		return nil, err
	}
	success, err := protoapi.ProjectRebindWorkspaceToProto(response)
	if err != nil {
		return nil, err
	}
	return &projectpb.RebindWorkspaceResult{Outcome: &projectpb.RebindWorkspaceResult_Success{Success: success}}, nil
}

func invokeBinaryProjectUnlinkWorkspace(g *Gateway, ctx context.Context, _ *connectionState, message proto.Message) (proto.Message, error) {
	request, err := protoapi.ProjectWorkspaceUnlinkRequestFromProto(message.(*projectpb.UnlinkWorkspaceRequest))
	if err != nil {
		return nil, err
	}
	client, err := projectViewClient(g)
	if err != nil {
		return nil, err
	}
	response, err := client.UnlinkWorkspaceFromProject(ctx, request)
	if err != nil {
		return nil, err
	}
	success, err := protoapi.ProjectWorkspaceUnlinkToProto(response)
	if err != nil {
		return nil, err
	}
	return &projectpb.UnlinkWorkspaceResult{Outcome: &projectpb.UnlinkWorkspaceResult_Success{Success: success}}, nil
}

func invokeBinaryProjectDelete(g *Gateway, ctx context.Context, _ *connectionState, message proto.Message) (proto.Message, error) {
	request, err := protoapi.ProjectDeleteRequestFromProto(message.(*projectpb.DeleteProjectRequest))
	if err != nil {
		return nil, err
	}
	client, err := projectViewClient(g)
	if err != nil {
		return nil, err
	}
	response, err := client.DeleteProject(ctx, request)
	if err != nil {
		return nil, err
	}
	success, err := protoapi.ProjectDeleteToProto(response)
	if err != nil {
		return nil, err
	}
	return &projectpb.DeleteProjectResult{Outcome: &projectpb.DeleteProjectResult_Success{Success: success}}, nil
}

func binaryProjectCreateFailure(message proto.Message, err error) proto.Message {
	failure := &projectpb.CreateProjectError{}
	switch {
	case errors.Is(err, serverapi.ErrServerAuthRequired):
		failure.Code = "auth_required"
		failure.Detail = &projectpb.CreateProjectError_AuthRequired{AuthRequired: &projectpb.AuthRequiredDetails{}}
	case errors.Is(err, serverapi.ErrProjectKeyConflict):
		if conflict, ok := serverapi.AsProjectKeyConflict(err); ok {
			failure.Code = "project_key_conflict"
			failure.Detail = &projectpb.CreateProjectError_ProjectKeyConflict{
				ProjectKeyConflict: &projectpb.ProjectKeyConflictDetails{ProjectKey: conflict.ProjectKey},
			}
		}
	case errors.Is(err, serverapi.ErrWorkspaceAlreadyBound):
		failure.Code = "workspace_already_bound"
		failure.Detail = &projectpb.CreateProjectError_WorkspaceAlreadyBound{WorkspaceAlreadyBound: &projectpb.WorkspaceAlreadyBoundDetails{}}
	case errors.Is(err, serverapi.ErrWorkspacePathMissing):
		failure.Code = "workspace_path_missing"
		failure.Detail = &projectpb.CreateProjectError_WorkspacePathMissing{WorkspacePathMissing: &projectpb.WorkspacePathMissingDetails{}}
	}
	if failure.Detail == nil {
		failure.Code = "internal_failure"
		failure.Detail = &projectpb.CreateProjectError_InternalFailure{InternalFailure: binaryInternalFailure(err)}
	}
	return &projectpb.CreateProjectResult{Outcome: &projectpb.CreateProjectResult_Error{Error: failure}}
}

func binaryProjectUpdateFailure(message proto.Message, err error) proto.Message {
	failure := &projectpb.UpdateProjectError{}
	switch {
	case errors.Is(err, serverapi.ErrServerAuthRequired):
		failure.Code = "auth_required"
		failure.Detail = &projectpb.UpdateProjectError_AuthRequired{AuthRequired: &projectpb.AuthRequiredDetails{}}
	case errors.Is(err, serverapi.ErrProjectNotFound):
		if request, ok := message.(*projectpb.UpdateProjectRequest); ok {
			failure.Code = "project_not_found"
			failure.Detail = &projectpb.UpdateProjectError_ProjectNotFound{
				ProjectNotFound: &projectpb.ProjectNotFoundDetails{ProjectId: request.ProjectId},
			}
		}
	case errors.Is(err, serverapi.ErrProjectKeyConflict):
		if conflict, ok := serverapi.AsProjectKeyConflict(err); ok {
			failure.Code = "project_key_conflict"
			failure.Detail = &projectpb.UpdateProjectError_ProjectKeyConflict{
				ProjectKeyConflict: &projectpb.ProjectKeyConflictDetails{ProjectKey: conflict.ProjectKey},
			}
		}
	}
	if failure.Detail == nil {
		failure.Code = "internal_failure"
		failure.Detail = &projectpb.UpdateProjectError_InternalFailure{InternalFailure: binaryInternalFailure(err)}
	}
	return &projectpb.UpdateProjectResult{Outcome: &projectpb.UpdateProjectResult_Error{Error: failure}}
}

func binaryProjectSetDefaultWorkspaceFailure(message proto.Message, err error) proto.Message {
	failure := &projectpb.SetDefaultWorkspaceError{}
	request, _ := message.(*projectpb.SetDefaultWorkspaceRequest)
	switch {
	case errors.Is(err, serverapi.ErrServerAuthRequired):
		failure.Code = "auth_required"
		failure.Detail = &projectpb.SetDefaultWorkspaceError_AuthRequired{AuthRequired: &projectpb.AuthRequiredDetails{}}
	case errors.Is(err, serverapi.ErrProjectNotFound):
		if request != nil {
			failure.Code = "project_not_found"
			failure.Detail = &projectpb.SetDefaultWorkspaceError_ProjectNotFound{
				ProjectNotFound: &projectpb.ProjectNotFoundDetails{ProjectId: request.ProjectId},
			}
		}
	case errors.Is(err, serverapi.ErrWorkspaceNotRegistered):
		if request != nil {
			failure.Code = "workspace_not_registered"
			failure.Detail = &projectpb.SetDefaultWorkspaceError_WorkspaceNotRegistered{
				WorkspaceNotRegistered: workspaceNotRegisteredDetails(request.ProjectId, request.Workspace),
			}
		}
	case errors.Is(err, serverapi.ErrWorkspacePathIdentity):
		if identity, ok := serverapi.AsWorkspacePathIdentity(err); ok {
			failure.Code = "workspace_path_identity"
			failure.Detail = &projectpb.SetDefaultWorkspaceError_WorkspacePathIdentity{
				WorkspacePathIdentity: &projectpb.WorkspacePathIdentityDetails{WorkspaceRoot: identity.WorkspaceRoot},
			}
		}
	case errors.Is(err, serverapi.ErrWorkspaceMutationFailed):
		if details := workspaceMutationDetails(err); details != nil {
			failure.Code = "workspace_mutation_failed"
			failure.Detail = &projectpb.SetDefaultWorkspaceError_WorkspaceMutationFailed{WorkspaceMutationFailed: details}
		}
	}
	if failure.Detail == nil {
		failure.Code = "internal_failure"
		failure.Detail = &projectpb.SetDefaultWorkspaceError_InternalFailure{InternalFailure: binaryInternalFailure(err)}
	}
	return &projectpb.SetDefaultWorkspaceResult{Outcome: &projectpb.SetDefaultWorkspaceResult_Error{Error: failure}}
}

func binaryProjectUnlinkWorkspaceFailure(message proto.Message, err error) proto.Message {
	failure := &projectpb.UnlinkWorkspaceError{}
	request, _ := message.(*projectpb.UnlinkWorkspaceRequest)
	switch {
	case errors.Is(err, serverapi.ErrServerAuthRequired):
		failure.Code = "auth_required"
		failure.Detail = &projectpb.UnlinkWorkspaceError_AuthRequired{AuthRequired: &projectpb.AuthRequiredDetails{}}
	case errors.Is(err, serverapi.ErrProjectNotFound):
		if request != nil {
			failure.Code = "project_not_found"
			failure.Detail = &projectpb.UnlinkWorkspaceError_ProjectNotFound{
				ProjectNotFound: &projectpb.ProjectNotFoundDetails{ProjectId: request.ProjectId},
			}
		}
	case errors.Is(err, serverapi.ErrWorkspaceNotRegistered):
		if request != nil {
			failure.Code = "workspace_not_registered"
			failure.Detail = &projectpb.UnlinkWorkspaceError_WorkspaceNotRegistered{
				WorkspaceNotRegistered: workspaceNotRegisteredDetails(request.ProjectId, request.Workspace),
			}
		}
	case errors.Is(err, serverapi.ErrWorkspacePathIdentity):
		if identity, ok := serverapi.AsWorkspacePathIdentity(err); ok {
			failure.Code = "workspace_path_identity"
			failure.Detail = &projectpb.UnlinkWorkspaceError_WorkspacePathIdentity{
				WorkspacePathIdentity: &projectpb.WorkspacePathIdentityDetails{WorkspaceRoot: identity.WorkspaceRoot},
			}
		}
	case errors.Is(err, serverapi.ErrWorkspaceDetachConflict):
		var conflict *serverapi.WorkspaceDetachConflictError
		if errors.As(err, &conflict) && conflict != nil {
			failure.Code = "workspace_detach_conflict"
			failure.Detail = &projectpb.UnlinkWorkspaceError_WorkspaceDetachConflict{
				WorkspaceDetachConflict: &projectpb.WorkspaceDetachConflictDetails{
					ProjectId: conflict.ProjectID, WorkspaceId: conflict.WorkspaceID, Retryable: true,
				},
			}
		}
	case errors.Is(err, serverapi.ErrWorkspaceMutationFailed):
		if details := workspaceMutationDetails(err); details != nil {
			failure.Code = "workspace_mutation_failed"
			failure.Detail = &projectpb.UnlinkWorkspaceError_WorkspaceMutationFailed{WorkspaceMutationFailed: details}
		}
	}
	if failure.Detail == nil {
		failure.Code = "internal_failure"
		failure.Detail = &projectpb.UnlinkWorkspaceError_InternalFailure{InternalFailure: binaryInternalFailure(err)}
	}
	return &projectpb.UnlinkWorkspaceResult{Outcome: &projectpb.UnlinkWorkspaceResult_Error{Error: failure}}
}

func binaryProjectDeleteFailure(message proto.Message, err error) proto.Message {
	failure := &projectpb.DeleteProjectError{}
	if errors.Is(err, serverapi.ErrServerAuthRequired) {
		failure.Code = "auth_required"
		failure.Detail = &projectpb.DeleteProjectError_AuthRequired{AuthRequired: &projectpb.AuthRequiredDetails{}}
	} else if errors.Is(err, serverapi.ErrProjectNotFound) {
		if request, ok := message.(*projectpb.DeleteProjectRequest); ok {
			failure.Code = "project_not_found"
			failure.Detail = &projectpb.DeleteProjectError_ProjectNotFound{
				ProjectNotFound: &projectpb.ProjectNotFoundDetails{ProjectId: request.ProjectId},
			}
		}
	}
	if failure.Detail == nil {
		failure.Code = "internal_failure"
		failure.Detail = &projectpb.DeleteProjectError_InternalFailure{InternalFailure: binaryInternalFailure(err)}
	}
	return &projectpb.DeleteProjectResult{Outcome: &projectpb.DeleteProjectResult_Error{Error: failure}}
}

func binaryProjectAttachWorkspaceFailure(message proto.Message, err error) proto.Message {
	failure := &projectpb.AttachWorkspaceError{}
	switch {
	case errors.Is(err, serverapi.ErrServerAuthRequired):
		failure.Code = "auth_required"
		failure.Detail = &projectpb.AttachWorkspaceError_AuthRequired{AuthRequired: &projectpb.AuthRequiredDetails{}}
	case errors.Is(err, serverapi.ErrProjectNotFound):
		if request, ok := message.(*projectpb.AttachWorkspaceRequest); ok {
			failure.Code = "project_not_found"
			failure.Detail = &projectpb.AttachWorkspaceError_ProjectNotFound{
				ProjectNotFound: &projectpb.ProjectNotFoundDetails{ProjectId: request.ProjectId},
			}
		}
	case errors.Is(err, serverapi.ErrWorkspaceAlreadyBound):
		failure.Code = "workspace_already_bound"
		failure.Detail = &projectpb.AttachWorkspaceError_WorkspaceAlreadyBound{WorkspaceAlreadyBound: &projectpb.WorkspaceAlreadyBoundDetails{}}
	case errors.Is(err, serverapi.ErrWorkspacePathMissing):
		failure.Code = "workspace_path_missing"
		failure.Detail = &projectpb.AttachWorkspaceError_WorkspacePathMissing{WorkspacePathMissing: &projectpb.WorkspacePathMissingDetails{}}
	}
	if failure.Detail == nil {
		failure.Code = "internal_failure"
		failure.Detail = &projectpb.AttachWorkspaceError_InternalFailure{InternalFailure: binaryInternalFailure(err)}
	}
	return &projectpb.AttachWorkspaceResult{Outcome: &projectpb.AttachWorkspaceResult_Error{Error: failure}}
}

func binaryProjectRebindWorkspaceFailure(message proto.Message, err error) proto.Message {
	failure := &projectpb.RebindWorkspaceError{}
	request, _ := message.(*projectpb.RebindWorkspaceRequest)
	switch {
	case errors.Is(err, serverapi.ErrServerAuthRequired):
		failure.Code = "auth_required"
		failure.Detail = &projectpb.RebindWorkspaceError_AuthRequired{AuthRequired: &projectpb.AuthRequiredDetails{}}
	case errors.Is(err, serverapi.ErrWorkspaceNotRegistered):
		if request != nil {
			root := request.OldWorkspaceRoot
			failure.Code = "workspace_not_registered"
			failure.Detail = &projectpb.RebindWorkspaceError_WorkspaceNotRegistered{
				WorkspaceNotRegistered: &projectpb.WorkspaceNotRegisteredDetails{WorkspaceRoot: &root},
			}
		}
	case errors.Is(err, serverapi.ErrWorkspaceBindingAmbiguous):
		if ambiguous, ok := serverapi.AsWorkspaceBindingAmbiguous(err); ok {
			failure.Code = "workspace_binding_ambiguous"
			failure.Detail = &projectpb.RebindWorkspaceError_WorkspaceBindingAmbiguous{
				WorkspaceBindingAmbiguous: &projectpb.WorkspaceBindingAmbiguousMutationDetails{
					ProjectIds: append([]string(nil), ambiguous.ProjectIDs...),
				},
			}
		}
	case errors.Is(err, serverapi.ErrWorkspaceAlreadyBound):
		failure.Code = "workspace_already_bound"
		failure.Detail = &projectpb.RebindWorkspaceError_WorkspaceAlreadyBound{WorkspaceAlreadyBound: &projectpb.WorkspaceAlreadyBoundDetails{}}
	case errors.Is(err, serverapi.ErrWorkspacePathMissing):
		failure.Code = "workspace_path_missing"
		failure.Detail = &projectpb.RebindWorkspaceError_WorkspacePathMissing{WorkspacePathMissing: &projectpb.WorkspacePathMissingDetails{}}
	}
	if failure.Detail == nil {
		failure.Code = "internal_failure"
		failure.Detail = &projectpb.RebindWorkspaceError_InternalFailure{InternalFailure: binaryInternalFailure(err)}
	}
	return &projectpb.RebindWorkspaceResult{Outcome: &projectpb.RebindWorkspaceResult_Error{Error: failure}}
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
