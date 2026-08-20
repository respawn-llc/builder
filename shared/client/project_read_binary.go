package client

import (
	"context"
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

func (c *Remote) ListProjects(ctx context.Context, _ serverapi.ProjectListRequest) (serverapi.ProjectListResponse, error) {
	result := &projectpb.ProjectListResult{}
	if err := c.callBinary(ctx, projectCatalogMethod("List"), &emptypb.Empty{}, result); err != nil {
		return serverapi.ProjectListResponse{}, err
	}
	if failure := result.GetError(); failure != nil {
		return serverapi.ProjectListResponse{}, projectInternalGeneratedError(failure.Code, failure.GetInternalFailure())
	}
	return protoapi.ProjectListFromProto(result.GetSuccess())
}

func (c *Remote) ListProjectHome(ctx context.Context, req serverapi.ProjectHomeListRequest) (serverapi.ProjectHomeListResponse, error) {
	request, err := protoapi.ProjectHomeListRequestToProto(req)
	if err != nil {
		return serverapi.ProjectHomeListResponse{}, err
	}
	result := &projectpb.ProjectHomeListResult{}
	if err := c.callBinary(ctx, projectCatalogMethod("ListHome"), request, result); err != nil {
		return serverapi.ProjectHomeListResponse{}, err
	}
	if failure := result.GetError(); failure != nil {
		return serverapi.ProjectHomeListResponse{}, projectInternalGeneratedError(failure.Code, failure.GetInternalFailure())
	}
	return protoapi.ProjectHomeListFromProto(result.GetSuccess())
}

func (c *Remote) ResolveProjectPath(ctx context.Context, req serverapi.ProjectResolvePathRequest) (serverapi.ProjectResolvePathResponse, error) {
	request, err := protoapi.ProjectResolvePathRequestToProto(req)
	if err != nil {
		return serverapi.ProjectResolvePathResponse{}, err
	}
	result := &projectpb.ResolvePathResult{}
	if err := c.callBinary(ctx, projectCatalogMethod("ResolvePath"), request, result); err != nil {
		return serverapi.ProjectResolvePathResponse{}, err
	}
	if failure := result.GetError(); failure != nil {
		switch failure.Code {
		case "workspace_binding_ambiguous":
			ambiguous, conversionErr := protoapi.WorkspaceBindingAmbiguousFromProto(failure.GetWorkspaceBindingAmbiguous())
			if conversionErr != nil {
				return serverapi.ProjectResolvePathResponse{}, conversionErr
			}
			return serverapi.ProjectResolvePathResponse{}, ambiguous
		case "internal_failure":
			return serverapi.ProjectResolvePathResponse{}, protoapi.InternalFailureFromProto(failure.GetInternalFailure())
		default:
			return serverapi.ProjectResolvePathResponse{}, generatedOperationFailure(failure.Code)
		}
	}
	return protoapi.ProjectResolvePathFromProto(result.GetSuccess())
}

func (c *Remote) PlanWorkspaceBinding(ctx context.Context, req serverapi.ProjectBindingPlanRequest) (serverapi.ProjectBindingPlanResponse, error) {
	request, err := protoapi.ProjectBindingPlanRequestToProto(req)
	if err != nil {
		return serverapi.ProjectBindingPlanResponse{}, err
	}
	result := &projectpb.PlanWorkspaceBindingResult{}
	if err := c.callBinary(ctx, projectCatalogMethod("PlanWorkspaceBinding"), request, result); err != nil {
		return serverapi.ProjectBindingPlanResponse{}, err
	}
	if failure := result.GetError(); failure != nil {
		return serverapi.ProjectBindingPlanResponse{}, projectInternalGeneratedError(failure.Code, failure.GetInternalFailure())
	}
	return protoapi.ProjectBindingPlanFromProto(result.GetSuccess())
}

func (c *Remote) GetProjectEdit(ctx context.Context, req serverapi.ProjectEditGetRequest) (serverapi.ProjectEditGetResponse, error) {
	request, err := protoapi.ProjectEditRequestToProto(req)
	if err != nil {
		return serverapi.ProjectEditGetResponse{}, err
	}
	result := &projectpb.GetProjectEditResult{}
	if err := c.callBinary(ctx, projectCatalogMethod("GetEdit"), request, result); err != nil {
		return serverapi.ProjectEditGetResponse{}, err
	}
	if failure := result.GetError(); failure != nil {
		return serverapi.ProjectEditGetResponse{}, projectNotFoundGeneratedError(
			failure.Code,
			failure.GetProjectNotFound(),
			failure.GetInternalFailure(),
		)
	}
	response, err := protoapi.ProjectEditFromProto(result.GetSuccess())
	if err != nil {
		return serverapi.ProjectEditGetResponse{}, err
	}
	if response.ProjectID != req.ProjectID {
		return serverapi.ProjectEditGetResponse{}, fmt.Errorf(
			"Project Settings response project %q does not match request project %q",
			response.ProjectID,
			req.ProjectID,
		)
	}
	return response, nil
}

func (c *Remote) ListProjectWorkspaces(ctx context.Context, req serverapi.ProjectWorkspaceListRequest) (serverapi.ProjectWorkspaceListResponse, error) {
	request, err := protoapi.ProjectWorkspaceListRequestToProto(req)
	if err != nil {
		return serverapi.ProjectWorkspaceListResponse{}, err
	}
	result := &projectpb.ListProjectWorkspacesResult{}
	if err := c.callBinary(ctx, projectCatalogMethod("ListWorkspaces"), request, result); err != nil {
		return serverapi.ProjectWorkspaceListResponse{}, err
	}
	if failure := result.GetError(); failure != nil {
		return serverapi.ProjectWorkspaceListResponse{}, projectNotFoundGeneratedError(
			failure.Code,
			failure.GetProjectNotFound(),
			failure.GetInternalFailure(),
		)
	}
	response, err := protoapi.ProjectWorkspaceListFromProto(result.GetSuccess())
	if err != nil {
		return serverapi.ProjectWorkspaceListResponse{}, fmt.Errorf("project workspace catalog response is invalid: %w", err)
	}
	if response.ProjectID != req.ProjectID {
		return serverapi.ProjectWorkspaceListResponse{}, fmt.Errorf(
			"project workspace catalog response project %q does not match request project %q",
			response.ProjectID,
			req.ProjectID,
		)
	}
	if response.Offset != req.Offset {
		return serverapi.ProjectWorkspaceListResponse{}, fmt.Errorf(
			"project workspace catalog response offset %d does not match request offset %d",
			response.Offset,
			req.Offset,
		)
	}
	if len(response.Workspaces) > req.Limit {
		return serverapi.ProjectWorkspaceListResponse{}, fmt.Errorf(
			"project workspace catalog response returned %d rows for limit %d",
			len(response.Workspaces),
			req.Limit,
		)
	}
	if response.NextOffset != nil {
		expected := req.Offset + req.Limit
		if len(response.Workspaces) != req.Limit || *response.NextOffset != expected {
			return serverapi.ProjectWorkspaceListResponse{}, fmt.Errorf(
				"project workspace catalog response next_offset %d does not continue request offset %d with limit %d",
				*response.NextOffset,
				req.Offset,
				req.Limit,
			)
		}
	}
	return response, nil
}

func (c *Remote) GetProjectWorkspace(ctx context.Context, req serverapi.ProjectWorkspaceGetRequest) (serverapi.ProjectWorkspaceGetResponse, error) {
	request, err := protoapi.ProjectWorkspaceGetRequestToProto(req)
	if err != nil {
		return serverapi.ProjectWorkspaceGetResponse{}, err
	}
	result := &projectpb.GetProjectWorkspaceResult{}
	if err := c.callBinary(ctx, projectCatalogMethod("GetWorkspace"), request, result); err != nil {
		return serverapi.ProjectWorkspaceGetResponse{}, err
	}
	if failure := result.GetError(); failure != nil {
		return serverapi.ProjectWorkspaceGetResponse{}, projectNotFoundGeneratedError(
			failure.Code,
			failure.GetProjectNotFound(),
			failure.GetInternalFailure(),
		)
	}
	response, err := protoapi.ProjectWorkspaceGetFromProto(result.GetSuccess())
	if err != nil {
		return serverapi.ProjectWorkspaceGetResponse{}, fmt.Errorf("exact Project Workspace response is invalid: %w", err)
	}
	if response.ProjectID != req.ProjectID {
		return serverapi.ProjectWorkspaceGetResponse{}, fmt.Errorf(
			"exact Project Workspace response project %q does not match request project %q",
			response.ProjectID,
			req.ProjectID,
		)
	}
	return response, nil
}

func (c *Remote) GetProjectOverview(ctx context.Context, req serverapi.ProjectGetOverviewRequest) (serverapi.ProjectGetOverviewResponse, error) {
	request, err := protoapi.ProjectOverviewRequestToProto(req)
	if err != nil {
		return serverapi.ProjectGetOverviewResponse{}, err
	}
	result := &projectpb.GetOverviewResult{}
	if err := c.callBinary(ctx, projectCatalogMethod("GetOverview"), request, result); err != nil {
		return serverapi.ProjectGetOverviewResponse{}, err
	}
	if failure := result.GetError(); failure != nil {
		switch failure.Code {
		case "project_not_found":
			return serverapi.ProjectGetOverviewResponse{}, projectNotFoundError(failure.GetProjectNotFound())
		case "project_unavailable":
			unavailable, conversionErr := protoapi.ProjectUnavailableFromProto(failure.GetProjectUnavailable())
			if conversionErr != nil {
				return serverapi.ProjectGetOverviewResponse{}, conversionErr
			}
			return serverapi.ProjectGetOverviewResponse{}, unavailable
		case "internal_failure":
			return serverapi.ProjectGetOverviewResponse{}, protoapi.InternalFailureFromProto(failure.GetInternalFailure())
		default:
			return serverapi.ProjectGetOverviewResponse{}, generatedOperationFailure(failure.Code)
		}
	}
	return protoapi.ProjectOverviewFromProto(result.GetSuccess())
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
