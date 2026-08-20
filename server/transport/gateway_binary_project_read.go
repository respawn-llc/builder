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
	return errors.Join(
		registerProjectViewUnary(bindings, service, "List",
			func() *emptypb.Empty { return &emptypb.Empty{} },
			apicontract.ProjectViewService.ListProjects, internalProjectFailure[*emptypb.Empty]),
		registerProjectViewUnary(bindings, service, "ListHome",
			func() *projectpb.ProjectHomeListRequest { return &projectpb.ProjectHomeListRequest{} },
			apicontract.ProjectViewService.ListProjectHome, internalProjectFailure[*projectpb.ProjectHomeListRequest]),
		registerProjectViewUnary(bindings, service, "ResolvePath",
			func() *projectpb.ResolvePathRequest { return &projectpb.ResolvePathRequest{} },
			apicontract.ProjectViewService.ResolveProjectPath, binaryProjectResolvePathFailure),
		registerProjectViewUnary(bindings, service, "PlanWorkspaceBinding",
			func() *projectpb.PlanWorkspaceBindingRequest { return &projectpb.PlanWorkspaceBindingRequest{} },
			apicontract.ProjectViewService.PlanWorkspaceBinding, internalProjectFailure[*projectpb.PlanWorkspaceBindingRequest]),
		registerProjectViewUnary(bindings, service, "GetEdit",
			func() *projectpb.ProjectEditGetRequest { return &projectpb.ProjectEditGetRequest{} },
			apicontract.ProjectViewService.GetProjectEdit, projectRequestNotFoundFailure[*projectpb.ProjectEditGetRequest]),
		registerProjectViewUnary(bindings, service, "ListWorkspaces",
			func() *projectpb.ProjectWorkspaceListRequest { return &projectpb.ProjectWorkspaceListRequest{} },
			apicontract.ProjectViewService.ListProjectWorkspaces, projectRequestNotFoundFailure[*projectpb.ProjectWorkspaceListRequest]),
		registerProjectViewUnary(bindings, service, "GetWorkspace",
			func() *projectpb.GetProjectWorkspaceRequest { return &projectpb.GetProjectWorkspaceRequest{} },
			apicontract.ProjectViewService.GetProjectWorkspace, projectRequestNotFoundFailure[*projectpb.GetProjectWorkspaceRequest]),
		registerProjectViewUnary(bindings, service, "GetOverview",
			func() *projectpb.GetOverviewRequest { return &projectpb.GetOverviewRequest{} },
			apicontract.ProjectViewService.GetProjectOverview, func(request *projectpb.GetOverviewRequest, err error) proto.Message {
				if errors.Is(err, serverapi.ErrProjectNotFound) {
					return &projectpb.ProjectNotFoundDetails{ProjectId: request.ProjectId}
				}
				if unavailable, ok := serverapi.AsProjectUnavailable(err); ok {
					if details, conversionErr := protoapi.ProjectUnavailableToProto(unavailable); conversionErr == nil {
						return details
					}
				}
				return binaryInternalFailure(err)
			}),
	)
}

func internalProjectFailure[Request proto.Message](_ Request, err error) proto.Message {
	return binaryInternalFailure(err)
}

func binaryProjectResolvePathFailure(_ *projectpb.ResolvePathRequest, err error) proto.Message {
	if ambiguous, ok := serverapi.AsWorkspaceBindingAmbiguous(err); ok {
		if details, conversionErr := protoapi.WorkspaceBindingAmbiguousToProto(ambiguous); conversionErr == nil {
			return details
		}
	}
	return binaryInternalFailure(err)
}

type projectIDRequest interface {
	proto.Message
	GetProjectId() string
}

func projectRequestNotFoundFailure[Request projectIDRequest](request Request, err error) proto.Message {
	return projectNotFoundFailure(request.GetProjectId(), err)
}

func registerProjectViewUnary[
	Request proto.Message,
	Success proto.Message,
](
	bindings map[string]gatewayBinaryBinding,
	service protoreflect.ServiceDescriptor,
	method protoreflect.Name,
	newRequest func() Request,
	invoke func(apicontract.ProjectViewService, context.Context, Request) (Success, error),
	failureDetail func(Request, error) proto.Message,
) error {
	return registerGatewayBinaryUnary(
		bindings,
		service,
		method,
		gatewayBinaryCoreActiveOrdinary,
		newRequest,
		nil,
		func(g *Gateway, ctx context.Context, _ *connectionState, request Request) (Success, error) {
			client := g.deps.ProjectViewClient()
			if client == nil {
				var zero Success
				return zero, errors.New("project view client is required")
			}
			return invoke(client, ctx, request)
		},
		func(_ *Gateway, _ *connectionState, request Request, err error) proto.Message {
			return failureDetail(request, err)
		},
	)
}

func projectNotFoundFailure(projectID string, err error) proto.Message {
	if errors.Is(err, serverapi.ErrProjectNotFound) {
		return &projectpb.ProjectNotFoundDetails{ProjectId: projectID}
	}
	return binaryInternalFailure(err)
}
