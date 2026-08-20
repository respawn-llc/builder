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
	"google.golang.org/protobuf/types/known/emptypb"
)

func registerProjectReadGatewayBinaryBindings(bindings map[string]gatewayBinaryBinding) error {
	service := projectpb.File_kent_api_project_project_proto.Services().ByName("ProjectCatalogService")
	registrations := []struct {
		name    string
		request func() proto.Message
		invoke  func(*Gateway, context.Context, *connectionState, proto.Message) (proto.Message, error)
		failure func(error) proto.Message
		detail  func(proto.Message, error) proto.Message
	}{
		{name: "List", request: func() proto.Message { return &emptypb.Empty{} }, invoke: invokeBinaryProjectList, failure: binaryProjectListFailure},
		{name: "ListHome", request: func() proto.Message { return &projectpb.ProjectHomeListRequest{} }, invoke: invokeBinaryProjectHomeList, failure: binaryProjectHomeListFailure},
		{name: "ResolvePath", request: func() proto.Message { return &projectpb.ResolvePathRequest{} }, invoke: invokeBinaryProjectResolvePath, failure: binaryProjectResolvePathFailure},
		{name: "PlanWorkspaceBinding", request: func() proto.Message { return &projectpb.PlanWorkspaceBindingRequest{} }, invoke: invokeBinaryProjectPlanWorkspaceBinding, failure: binaryProjectPlanWorkspaceBindingFailure},
		{name: "GetEdit", request: func() proto.Message { return &projectpb.ProjectEditGetRequest{} }, invoke: invokeBinaryProjectGetEdit, failure: binaryProjectGetEditFailure, detail: binaryProjectGetEditRequestFailure},
		{name: "ListWorkspaces", request: func() proto.Message { return &projectpb.ProjectWorkspaceListRequest{} }, invoke: invokeBinaryProjectListWorkspaces, failure: binaryProjectListWorkspacesFailure, detail: binaryProjectListWorkspacesRequestFailure},
		{name: "GetWorkspace", request: func() proto.Message { return &projectpb.GetProjectWorkspaceRequest{} }, invoke: invokeBinaryProjectGetWorkspace, failure: binaryProjectGetWorkspaceFailure, detail: binaryProjectGetWorkspaceRequestFailure},
		{name: "GetOverview", request: func() proto.Message { return &projectpb.GetOverviewRequest{} }, invoke: invokeBinaryProjectGetOverview, failure: binaryProjectGetOverviewFailure, detail: binaryProjectGetOverviewRequestFailure},
	}
	for _, registration := range registrations {
		if err := registerGatewayBinaryBinding(
			bindings,
			service,
			protoreflect.Name(registration.name),
			apicontract.DependencyProjectView,
			registration.request,
			nil,
			registration.invoke,
			registration.failure,
		); err != nil {
			return err
		}
		if registration.detail != nil {
			operation, err := protoapi.OperationFromDescriptor(service.Methods().ByName(protoreflect.Name(registration.name)))
			if err != nil {
				return err
			}
			binding := bindings[operation.Name]
			binding.failure = func(_ *Gateway, _ *connectionState, message proto.Message, err error) proto.Message {
				return registration.detail(message, err)
			}
			bindings[operation.Name] = binding
		}
	}
	return nil
}

func projectViewClient(g *Gateway) (apicontract.ProjectViewService, error) {
	client := g.deps.ProjectViewClient()
	if client == nil {
		return nil, errors.New("project view client is required")
	}
	return client, nil
}

func invokeBinaryProjectList(g *Gateway, ctx context.Context, _ *connectionState, _ proto.Message) (proto.Message, error) {
	client, err := projectViewClient(g)
	if err != nil {
		return nil, err
	}
	response, err := client.ListProjects(ctx, serverapi.ProjectListRequest{})
	if err != nil {
		return nil, err
	}
	success, err := protoapi.ProjectListToProto(response)
	if err != nil {
		return nil, err
	}
	return &projectpb.ProjectListResult{Outcome: &projectpb.ProjectListResult_Success{Success: success}}, nil
}

func invokeBinaryProjectHomeList(g *Gateway, ctx context.Context, _ *connectionState, message proto.Message) (proto.Message, error) {
	request, err := protoapi.ProjectHomeListRequestFromProto(message.(*projectpb.ProjectHomeListRequest))
	if err != nil {
		return nil, err
	}
	client, err := projectViewClient(g)
	if err != nil {
		return nil, err
	}
	response, err := client.ListProjectHome(ctx, request)
	if err != nil {
		return nil, err
	}
	success, err := protoapi.ProjectHomeListToProto(response)
	if err != nil {
		return nil, err
	}
	return &projectpb.ProjectHomeListResult{Outcome: &projectpb.ProjectHomeListResult_Success{Success: success}}, nil
}

func invokeBinaryProjectResolvePath(g *Gateway, ctx context.Context, _ *connectionState, message proto.Message) (proto.Message, error) {
	request, err := protoapi.ProjectResolvePathRequestFromProto(message.(*projectpb.ResolvePathRequest))
	if err != nil {
		return nil, err
	}
	client, err := projectViewClient(g)
	if err != nil {
		return nil, err
	}
	response, err := client.ResolveProjectPath(ctx, request)
	if err != nil {
		return nil, err
	}
	success, err := protoapi.ProjectResolvePathToProto(response)
	if err != nil {
		return nil, err
	}
	return &projectpb.ResolvePathResult{Outcome: &projectpb.ResolvePathResult_Success{Success: success}}, nil
}

func invokeBinaryProjectPlanWorkspaceBinding(g *Gateway, ctx context.Context, _ *connectionState, message proto.Message) (proto.Message, error) {
	request, err := protoapi.ProjectBindingPlanRequestFromProto(message.(*projectpb.PlanWorkspaceBindingRequest))
	if err != nil {
		return nil, err
	}
	client, err := projectViewClient(g)
	if err != nil {
		return nil, err
	}
	response, err := client.PlanWorkspaceBinding(ctx, request)
	if err != nil {
		return nil, err
	}
	success, err := protoapi.ProjectBindingPlanToProto(response)
	if err != nil {
		return nil, err
	}
	return &projectpb.PlanWorkspaceBindingResult{Outcome: &projectpb.PlanWorkspaceBindingResult_Success{Success: success}}, nil
}

func invokeBinaryProjectGetEdit(g *Gateway, ctx context.Context, _ *connectionState, message proto.Message) (proto.Message, error) {
	request, err := protoapi.ProjectEditRequestFromProto(message.(*projectpb.ProjectEditGetRequest))
	if err != nil {
		return nil, err
	}
	client, err := projectViewClient(g)
	if err != nil {
		return nil, err
	}
	response, err := client.GetProjectEdit(ctx, request)
	if err != nil {
		return nil, err
	}
	success, err := protoapi.ProjectEditToProto(response)
	if err != nil {
		return nil, err
	}
	return &projectpb.GetProjectEditResult{Outcome: &projectpb.GetProjectEditResult_Success{Success: success}}, nil
}

func invokeBinaryProjectListWorkspaces(g *Gateway, ctx context.Context, _ *connectionState, message proto.Message) (proto.Message, error) {
	request, err := protoapi.ProjectWorkspaceListRequestFromProto(message.(*projectpb.ProjectWorkspaceListRequest))
	if err != nil {
		return nil, err
	}
	client, err := projectViewClient(g)
	if err != nil {
		return nil, err
	}
	response, err := client.ListProjectWorkspaces(ctx, request)
	if err != nil {
		return nil, err
	}
	success, err := protoapi.ProjectWorkspaceListToProto(response)
	if err != nil {
		return nil, err
	}
	return &projectpb.ListProjectWorkspacesResult{Outcome: &projectpb.ListProjectWorkspacesResult_Success{Success: success}}, nil
}

func invokeBinaryProjectGetWorkspace(g *Gateway, ctx context.Context, _ *connectionState, message proto.Message) (proto.Message, error) {
	request, err := protoapi.ProjectWorkspaceGetRequestFromProto(message.(*projectpb.GetProjectWorkspaceRequest))
	if err != nil {
		return nil, err
	}
	client, err := projectViewClient(g)
	if err != nil {
		return nil, err
	}
	response, err := client.GetProjectWorkspace(ctx, request)
	if err != nil {
		return nil, err
	}
	success, err := protoapi.ProjectWorkspaceGetToProto(response)
	if err != nil {
		return nil, err
	}
	return &projectpb.GetProjectWorkspaceResult{Outcome: &projectpb.GetProjectWorkspaceResult_Success{Success: success}}, nil
}

func invokeBinaryProjectGetOverview(g *Gateway, ctx context.Context, _ *connectionState, message proto.Message) (proto.Message, error) {
	request, err := protoapi.ProjectOverviewRequestFromProto(message.(*projectpb.GetOverviewRequest))
	if err != nil {
		return nil, err
	}
	client, err := projectViewClient(g)
	if err != nil {
		return nil, err
	}
	response, err := client.GetProjectOverview(ctx, request)
	if err != nil {
		return nil, err
	}
	success, err := protoapi.ProjectOverviewToProto(response)
	if err != nil {
		return nil, err
	}
	return &projectpb.GetOverviewResult{Outcome: &projectpb.GetOverviewResult_Success{Success: success}}, nil
}

func binaryProjectListFailure(err error) proto.Message {
	return &projectpb.ProjectListResult{Outcome: &projectpb.ProjectListResult_Error{Error: &projectpb.ProjectListError{
		Code: "internal_failure",
		Detail: &projectpb.ProjectListError_InternalFailure{
			InternalFailure: binaryInternalFailure(err),
		},
	}}}
}

func binaryProjectHomeListFailure(err error) proto.Message {
	return &projectpb.ProjectHomeListResult{Outcome: &projectpb.ProjectHomeListResult_Error{Error: &projectpb.ProjectHomeListError{
		Code: "internal_failure",
		Detail: &projectpb.ProjectHomeListError_InternalFailure{
			InternalFailure: binaryInternalFailure(err),
		},
	}}}
}

func binaryProjectResolvePathFailure(err error) proto.Message {
	failure := &projectpb.ResolvePathError{}
	if ambiguous, ok := serverapi.AsWorkspaceBindingAmbiguous(err); ok {
		if details, conversionErr := protoapi.WorkspaceBindingAmbiguousToProto(ambiguous); conversionErr == nil {
			failure.Code = "workspace_binding_ambiguous"
			failure.Detail = &projectpb.ResolvePathError_WorkspaceBindingAmbiguous{WorkspaceBindingAmbiguous: details}
		}
	}
	if failure.Detail == nil {
		failure.Code = "internal_failure"
		failure.Detail = &projectpb.ResolvePathError_InternalFailure{InternalFailure: binaryInternalFailure(err)}
	}
	return &projectpb.ResolvePathResult{Outcome: &projectpb.ResolvePathResult_Error{Error: failure}}
}

func binaryProjectPlanWorkspaceBindingFailure(err error) proto.Message {
	return &projectpb.PlanWorkspaceBindingResult{Outcome: &projectpb.PlanWorkspaceBindingResult_Error{Error: &projectpb.PlanWorkspaceBindingError{
		Code: "internal_failure",
		Detail: &projectpb.PlanWorkspaceBindingError_InternalFailure{
			InternalFailure: binaryInternalFailure(err),
		},
	}}}
}

func binaryProjectGetEditFailure(err error) proto.Message {
	return binaryProjectGetEditRequestFailure(nil, err)
}

func binaryProjectGetEditRequestFailure(message proto.Message, err error) proto.Message {
	failure := &projectpb.GetProjectEditError{}
	if errors.Is(err, serverapi.ErrProjectNotFound) {
		if request, ok := message.(*projectpb.ProjectEditGetRequest); ok {
			failure.Code = "project_not_found"
			failure.Detail = &projectpb.GetProjectEditError_ProjectNotFound{
				ProjectNotFound: &projectpb.ProjectNotFoundDetails{ProjectId: request.ProjectId},
			}
		}
	}
	if failure.Detail == nil {
		failure.Code = "internal_failure"
		failure.Detail = &projectpb.GetProjectEditError_InternalFailure{InternalFailure: binaryInternalFailure(err)}
	}
	return &projectpb.GetProjectEditResult{Outcome: &projectpb.GetProjectEditResult_Error{Error: failure}}
}

func binaryProjectListWorkspacesFailure(err error) proto.Message {
	return binaryProjectListWorkspacesRequestFailure(nil, err)
}

func binaryProjectListWorkspacesRequestFailure(message proto.Message, err error) proto.Message {
	failure := &projectpb.ListProjectWorkspacesError{}
	if errors.Is(err, serverapi.ErrProjectNotFound) {
		if request, ok := message.(*projectpb.ProjectWorkspaceListRequest); ok {
			failure.Code = "project_not_found"
			failure.Detail = &projectpb.ListProjectWorkspacesError_ProjectNotFound{
				ProjectNotFound: &projectpb.ProjectNotFoundDetails{ProjectId: request.ProjectId},
			}
		}
	}
	if failure.Detail == nil {
		failure.Code = "internal_failure"
		failure.Detail = &projectpb.ListProjectWorkspacesError_InternalFailure{InternalFailure: binaryInternalFailure(err)}
	}
	return &projectpb.ListProjectWorkspacesResult{Outcome: &projectpb.ListProjectWorkspacesResult_Error{Error: failure}}
}

func binaryProjectGetWorkspaceFailure(err error) proto.Message {
	return binaryProjectGetWorkspaceRequestFailure(nil, err)
}

func binaryProjectGetWorkspaceRequestFailure(message proto.Message, err error) proto.Message {
	failure := &projectpb.GetProjectWorkspaceError{}
	if errors.Is(err, serverapi.ErrProjectNotFound) {
		if request, ok := message.(*projectpb.GetProjectWorkspaceRequest); ok {
			failure.Code = "project_not_found"
			failure.Detail = &projectpb.GetProjectWorkspaceError_ProjectNotFound{
				ProjectNotFound: &projectpb.ProjectNotFoundDetails{ProjectId: request.ProjectId},
			}
		}
	}
	if failure.Detail == nil {
		failure.Code = "internal_failure"
		failure.Detail = &projectpb.GetProjectWorkspaceError_InternalFailure{InternalFailure: binaryInternalFailure(err)}
	}
	return &projectpb.GetProjectWorkspaceResult{Outcome: &projectpb.GetProjectWorkspaceResult_Error{Error: failure}}
}

func binaryProjectGetOverviewFailure(err error) proto.Message {
	return binaryProjectGetOverviewRequestFailure(nil, err)
}

func binaryProjectGetOverviewRequestFailure(message proto.Message, err error) proto.Message {
	failure := &projectpb.GetOverviewError{}
	switch {
	case errors.Is(err, serverapi.ErrProjectNotFound):
		if request, ok := message.(*projectpb.GetOverviewRequest); ok {
			failure.Code = "project_not_found"
			failure.Detail = &projectpb.GetOverviewError_ProjectNotFound{
				ProjectNotFound: &projectpb.ProjectNotFoundDetails{ProjectId: request.ProjectId},
			}
		}
	default:
		if unavailable, ok := serverapi.AsProjectUnavailable(err); ok {
			if details, conversionErr := protoapi.ProjectUnavailableToProto(unavailable); conversionErr == nil {
				failure.Code = "project_unavailable"
				failure.Detail = &projectpb.GetOverviewError_ProjectUnavailable{ProjectUnavailable: details}
			}
		}
	}
	if failure.Detail == nil {
		failure.Code = "internal_failure"
		failure.Detail = &projectpb.GetOverviewError_InternalFailure{InternalFailure: binaryInternalFailure(err)}
	}
	return &projectpb.GetOverviewResult{Outcome: &projectpb.GetOverviewResult_Error{Error: failure}}
}
