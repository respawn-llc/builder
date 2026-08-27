package client

import (
	"context"
	"errors"
	"fmt"

	"core/shared/protoapi"
	projectpb "core/shared/protoapi/gen/kent/api/project"
	sharedpb "core/shared/protoapi/gen/kent/api/shared"
	"core/shared/serverapi"

	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/emptypb"
)

func projectCatalogMethod(name string) protoreflect.MethodDescriptor {
	return bootstrapMethod(
		projectpb.File_kent_api_project_project_proto,
		"ProjectCatalogService",
		protoreflect.Name(name),
	)
}

func (c *Remote) ListProjects(ctx context.Context, request *emptypb.Empty) (*projectpb.ProjectListSuccess, error) {
	return callGeneratedBinary(c, ctx, projectCatalogMethod("List"), request,
		&projectpb.ProjectListResult{},
		func(failure *projectpb.ProjectListError) error {
			return projectInternalGeneratedError(failure.Code, failure.GetInternalFailure())
		})
}

func (c *Remote) ListProjectHome(ctx context.Context, request *projectpb.ProjectHomeListRequest) (*projectpb.ProjectHomeListSuccess, error) {
	return callGeneratedBinary(c, ctx, projectCatalogMethod("ListHome"), request,
		&projectpb.ProjectHomeListResult{},
		func(failure *projectpb.ProjectHomeListError) error {
			return projectInternalGeneratedError(failure.Code, failure.GetInternalFailure())
		})
}

func (c *Remote) ResolveProjectPath(ctx context.Context, request *projectpb.ResolvePathRequest) (*projectpb.ResolvePathSuccess, error) {
	return callGeneratedBinary(c, ctx, projectCatalogMethod("ResolvePath"), request,
		&projectpb.ResolvePathResult{},
		func(failure *projectpb.ResolvePathError) error {
			switch failure.Code {
			case "workspace_binding_ambiguous":
				return workspaceBindingAmbiguousError(failure.GetWorkspaceBindingAmbiguous())
			case "internal_failure":
				return protoapi.InternalFailureFromProto(failure.GetInternalFailure())
			default:
				return generatedOperationFailure(failure.Code)
			}
		})
}

func (c *Remote) PlanWorkspaceBinding(ctx context.Context, request *projectpb.PlanWorkspaceBindingRequest) (*projectpb.PlanWorkspaceBindingSuccess, error) {
	return callGeneratedBinary(c, ctx, projectCatalogMethod("PlanWorkspaceBinding"), request,
		&projectpb.PlanWorkspaceBindingResult{},
		func(failure *projectpb.PlanWorkspaceBindingError) error {
			return projectInternalGeneratedError(failure.Code, failure.GetInternalFailure())
		})
}

func (c *Remote) GetProjectEdit(ctx context.Context, request *projectpb.ProjectEditGetRequest) (*projectpb.GetProjectEditSuccess, error) {
	response, err := callGeneratedBinary(c, ctx, projectCatalogMethod("GetEdit"), request,
		&projectpb.GetProjectEditResult{},
		func(failure *projectpb.GetProjectEditError) error {
			return projectNotFoundGeneratedError(
				failure.Code, failure.GetProjectNotFound(), failure.GetInternalFailure())
		})
	if err != nil {
		return nil, err
	}
	if response.ProjectId != request.ProjectId {
		return nil, fmt.Errorf(
			"Project Settings response project %q does not match request project %q",
			response.ProjectId,
			request.ProjectId,
		)
	}
	return response, nil
}

func (c *Remote) ListProjectWorkspaces(ctx context.Context, request *projectpb.ProjectWorkspaceListRequest) (*projectpb.ListProjectWorkspacesSuccess, error) {
	response, err := callGeneratedBinary(c, ctx, projectCatalogMethod("ListWorkspaces"), request,
		&projectpb.ListProjectWorkspacesResult{},
		func(failure *projectpb.ListProjectWorkspacesError) error {
			return projectNotFoundGeneratedError(
				failure.Code, failure.GetProjectNotFound(), failure.GetInternalFailure())
		})
	if err != nil {
		return nil, err
	}
	if err := ValidateProjectWorkspacePage(response, request); err != nil {
		return nil, err
	}
	return response, nil
}

// ValidateProjectWorkspacePage validates a catalog response against the request
// that produced it.
func ValidateProjectWorkspacePage(response *projectpb.ListProjectWorkspacesSuccess, request *projectpb.ProjectWorkspaceListRequest) error {
	if response == nil {
		return errors.New("project workspace catalog response is empty")
	}
	if request == nil {
		return errors.New("project workspace catalog request is empty")
	}
	if response.ProjectId != request.ProjectId {
		return fmt.Errorf(
			"project workspace catalog response project %q does not match request project %q",
			response.ProjectId,
			request.ProjectId,
		)
	}
	if response.Offset != request.Offset {
		return fmt.Errorf(
			"project workspace catalog response offset %d does not match request offset %d",
			response.Offset,
			request.Offset,
		)
	}
	if len(response.Workspaces) > int(request.Limit) {
		return fmt.Errorf(
			"project workspace catalog response returned %d rows for limit %d",
			len(response.Workspaces),
			request.Limit,
		)
	}
	if response.NextOffset != nil {
		expected := request.Offset + request.Limit
		if len(response.Workspaces) != int(request.Limit) || *response.NextOffset != expected {
			return fmt.Errorf(
				"project workspace catalog response next_offset %d does not continue request offset %d with limit %d",
				*response.NextOffset,
				request.Offset,
				request.Limit,
			)
		}
	}
	return nil
}

func (c *Remote) GetProjectWorkspace(ctx context.Context, request *projectpb.GetProjectWorkspaceRequest) (*projectpb.GetProjectWorkspaceSuccess, error) {
	response, err := callGeneratedBinary(c, ctx, projectCatalogMethod("GetWorkspace"), request,
		&projectpb.GetProjectWorkspaceResult{},
		func(failure *projectpb.GetProjectWorkspaceError) error {
			return projectNotFoundGeneratedError(
				failure.Code, failure.GetProjectNotFound(), failure.GetInternalFailure())
		})
	if err != nil {
		return nil, err
	}
	if response.ProjectId != request.ProjectId {
		return nil, fmt.Errorf(
			"exact Project Workspace response project %q does not match request project %q",
			response.ProjectId,
			request.ProjectId,
		)
	}
	return response, nil
}

func (c *Remote) GetProjectOverview(ctx context.Context, request *projectpb.GetOverviewRequest) (*projectpb.GetOverviewSuccess, error) {
	response, err := callGeneratedBinary(c, ctx, projectCatalogMethod("GetOverview"), request,
		&projectpb.GetOverviewResult{},
		func(failure *projectpb.GetOverviewError) error {
			switch failure.Code {
			case "project_not_found":
				return projectNotFoundError(failure.GetProjectNotFound())
			case "project_unavailable":
				return projectUnavailableError(failure.GetProjectUnavailable())
			case "internal_failure":
				return protoapi.InternalFailureFromProto(failure.GetInternalFailure())
			default:
				return generatedOperationFailure(failure.Code)
			}
		})
	if err != nil {
		return nil, err
	}
	if response.Overview.Project.ProjectId != request.ProjectId {
		return nil, fmt.Errorf(
			"project overview response project %q does not match request project %q",
			response.Overview.Project.ProjectId,
			request.ProjectId,
		)
	}
	return response, nil
}

func projectInternalGeneratedError(code string, internal *sharedpb.InternalFailureDetails) error {
	if code == "internal_failure" {
		return protoapi.InternalFailureFromProto(internal)
	}
	return generatedOperationFailure(code)
}

func projectNotFoundGeneratedError(
	code string,
	notFound *projectpb.ProjectNotFoundDetails,
	internal *sharedpb.InternalFailureDetails,
) error {
	switch code {
	case "project_not_found":
		return projectNotFoundError(notFound)
	case "internal_failure":
		return protoapi.InternalFailureFromProto(internal)
	default:
		return generatedOperationFailure(code)
	}
}

func projectNotFoundError(details *projectpb.ProjectNotFoundDetails) error {
	if err := protoapi.Validate(details); err != nil {
		return fmt.Errorf("decode project_not_found details: %w", err)
	}
	return fmt.Errorf("%w: %q", serverapi.ErrProjectNotFound, details.ProjectId)
}
