package protoapi

import (
	"fmt"
	"math"
	"time"

	"core/shared/clientui"
	projectpb "core/shared/protoapi/gen/kent/api/project"
	"core/shared/runtimeids"
	"core/shared/serverapi"

	"google.golang.org/protobuf/types/known/timestamppb"
)

func ProjectListToProto(response serverapi.ProjectListResponse) (*projectpb.ProjectListSuccess, error) {
	projects, err := mapSliceError(response.Projects, projectSummaryToProto)
	if err != nil {
		return nil, err
	}
	success := &projectpb.ProjectListSuccess{Projects: projects}
	if err := Validate(success); err != nil {
		return nil, fmt.Errorf("convert project list to protobuf: %w", err)
	}
	return success, nil
}

func ProjectListFromProto(success *projectpb.ProjectListSuccess) (serverapi.ProjectListResponse, error) {
	if err := Validate(success); err != nil {
		return serverapi.ProjectListResponse{}, fmt.Errorf("convert project list from protobuf: %w", err)
	}
	projects, err := mapSliceError(success.Projects, projectSummaryFromProto)
	if err != nil {
		return serverapi.ProjectListResponse{}, err
	}
	return serverapi.ProjectListResponse{Projects: projects}, nil
}

func ProjectHomeListRequestToProto(request serverapi.ProjectHomeListRequest) (*projectpb.ProjectHomeListRequest, error) {
	if err := request.Validate(); err != nil {
		return nil, err
	}
	message := &projectpb.ProjectHomeListRequest{}
	if request.PageSize > 0 {
		value, err := projectInt32(request.PageSize, "project home page_size")
		if err != nil {
			return nil, err
		}
		message.PageSize = &value
	}
	if request.PageToken != "" {
		message.PageToken = &request.PageToken
	}
	if err := Validate(message); err != nil {
		return nil, fmt.Errorf("convert project home request to protobuf: %w", err)
	}
	return message, nil
}

func ProjectHomeListRequestFromProto(message *projectpb.ProjectHomeListRequest) (serverapi.ProjectHomeListRequest, error) {
	request := serverapi.ProjectHomeListRequest{PageToken: dereference(message.PageToken)}
	if message.PageSize != nil {
		request.PageSize = int(*message.PageSize)
	}
	return request, nil
}

func ProjectHomeListToProto(response serverapi.ProjectHomeListResponse) (*projectpb.ProjectHomeListSuccess, error) {
	projects, err := mapSliceError(response.Projects, projectHomeSummaryToProto)
	if err != nil {
		return nil, err
	}
	success := &projectpb.ProjectHomeListSuccess{
		Projects:    projects,
		GeneratedAt: timestamppb.New(time.UnixMilli(response.GeneratedAtUnixMs)),
	}
	if response.NextPageToken != "" {
		success.NextPageToken = &response.NextPageToken
	}
	if err := Validate(success); err != nil {
		return nil, fmt.Errorf("convert project home response to protobuf: %w", err)
	}
	return success, nil
}

func ProjectHomeListFromProto(success *projectpb.ProjectHomeListSuccess) (serverapi.ProjectHomeListResponse, error) {
	if err := Validate(success); err != nil {
		return serverapi.ProjectHomeListResponse{}, fmt.Errorf("convert project home response from protobuf: %w", err)
	}
	projects, err := mapSliceError(success.Projects, projectHomeSummaryFromProto)
	if err != nil {
		return serverapi.ProjectHomeListResponse{}, err
	}
	return serverapi.ProjectHomeListResponse{
		Projects:          projects,
		NextPageToken:     dereference(success.NextPageToken),
		GeneratedAtUnixMs: success.GeneratedAt.AsTime().UnixMilli(),
	}, nil
}

func ProjectResolvePathRequestToProto(request serverapi.ProjectResolvePathRequest) (*projectpb.ResolvePathRequest, error) {
	if err := request.Validate(); err != nil {
		return nil, err
	}
	return &projectpb.ResolvePathRequest{Path: request.Path}, nil
}

func ProjectResolvePathRequestFromProto(message *projectpb.ResolvePathRequest) (serverapi.ProjectResolvePathRequest, error) {
	request := serverapi.ProjectResolvePathRequest{Path: message.Path}
	return request, nil
}

func ProjectResolvePathToProto(response serverapi.ProjectResolvePathResponse) (*projectpb.ResolvePathSuccess, error) {
	availability, err := projectAvailabilityToProto(response.PathAvailability)
	if err != nil {
		return nil, err
	}
	success := &projectpb.ResolvePathSuccess{CanonicalRoot: response.CanonicalRoot, PathAvailability: availability}
	if response.Binding != nil {
		success.Binding, err = projectBindingToProto(*response.Binding)
		if err != nil {
			return nil, err
		}
	}
	if err := Validate(success); err != nil {
		return nil, fmt.Errorf("convert project path response to protobuf: %w", err)
	}
	return success, nil
}

func ProjectResolvePathFromProto(success *projectpb.ResolvePathSuccess) (serverapi.ProjectResolvePathResponse, error) {
	if err := Validate(success); err != nil {
		return serverapi.ProjectResolvePathResponse{}, err
	}
	availability, err := projectAvailabilityFromProto(success.PathAvailability)
	if err != nil {
		return serverapi.ProjectResolvePathResponse{}, err
	}
	response := serverapi.ProjectResolvePathResponse{
		CanonicalRoot: success.CanonicalRoot, PathAvailability: availability,
	}
	if success.Binding != nil {
		binding, err := projectBindingFromProto(success.Binding)
		if err != nil {
			return serverapi.ProjectResolvePathResponse{}, err
		}
		response.Binding = &binding
	}
	return response, nil
}

func ProjectBindingPlanRequestToProto(request serverapi.ProjectBindingPlanRequest) (*projectpb.PlanWorkspaceBindingRequest, error) {
	if err := request.Validate(); err != nil {
		return nil, err
	}
	mode, err := projectBindingPlanModeToProto(request.Mode)
	if err != nil {
		return nil, err
	}
	return &projectpb.PlanWorkspaceBindingRequest{Path: request.Path, Mode: mode}, nil
}

func ProjectBindingPlanRequestFromProto(message *projectpb.PlanWorkspaceBindingRequest) (serverapi.ProjectBindingPlanRequest, error) {
	mode, err := projectBindingPlanModeFromProto(message.Mode)
	if err != nil {
		return serverapi.ProjectBindingPlanRequest{}, err
	}
	request := serverapi.ProjectBindingPlanRequest{Path: message.Path, Mode: mode}
	return request, nil
}

func ProjectBindingPlanToProto(response serverapi.ProjectBindingPlanResponse) (*projectpb.PlanWorkspaceBindingSuccess, error) {
	kind, err := projectBindingPlanKindToProto(response.Kind)
	if err != nil {
		return nil, err
	}
	availability, err := projectAvailabilityToProto(response.PathAvailability)
	if err != nil {
		return nil, err
	}
	projects, err := mapSliceError(response.Projects, projectSummaryToProto)
	if err != nil {
		return nil, err
	}
	success := &projectpb.PlanWorkspaceBindingSuccess{
		Kind: kind, CanonicalRoot: response.CanonicalRoot, PathAvailability: availability, Projects: projects,
	}
	if response.Binding != nil {
		success.Binding, err = projectBindingToProto(*response.Binding)
		if err != nil {
			return nil, err
		}
	}
	if response.Workspace != nil {
		success.Workspace = &projectpb.SelectedProjectWorkspace{
			ProjectId: response.Workspace.ProjectID, WorkspaceId: response.Workspace.WorkspaceID,
		}
	}
	if err := Validate(success); err != nil {
		return nil, fmt.Errorf("convert project binding plan to protobuf: %w", err)
	}
	return success, nil
}

func ProjectBindingPlanFromProto(success *projectpb.PlanWorkspaceBindingSuccess) (serverapi.ProjectBindingPlanResponse, error) {
	if err := Validate(success); err != nil {
		return serverapi.ProjectBindingPlanResponse{}, err
	}
	kind, err := projectBindingPlanKindFromProto(success.Kind)
	if err != nil {
		return serverapi.ProjectBindingPlanResponse{}, err
	}
	availability, err := projectAvailabilityFromProto(success.PathAvailability)
	if err != nil {
		return serverapi.ProjectBindingPlanResponse{}, err
	}
	projects, err := mapSliceError(success.Projects, projectSummaryFromProto)
	if err != nil {
		return serverapi.ProjectBindingPlanResponse{}, err
	}
	response := serverapi.ProjectBindingPlanResponse{
		Kind: kind, CanonicalRoot: success.CanonicalRoot, PathAvailability: availability, Projects: projects,
	}
	if success.Binding != nil {
		binding, err := projectBindingFromProto(success.Binding)
		if err != nil {
			return serverapi.ProjectBindingPlanResponse{}, err
		}
		response.Binding = &binding
	}
	if success.Workspace != nil {
		response.Workspace = &serverapi.ProjectWorkspacePlanSelected{
			ProjectID: success.Workspace.ProjectId, WorkspaceID: success.Workspace.WorkspaceId,
		}
	}
	return response, nil
}

func ProjectEditRequestToProto(request serverapi.ProjectEditGetRequest) (*projectpb.ProjectEditGetRequest, error) {
	if err := request.Validate(); err != nil {
		return nil, err
	}
	return &projectpb.ProjectEditGetRequest{ProjectId: request.ProjectID}, nil
}

func ProjectEditRequestFromProto(message *projectpb.ProjectEditGetRequest) (serverapi.ProjectEditGetRequest, error) {
	request := serverapi.ProjectEditGetRequest{ProjectID: message.ProjectId}
	return request, nil
}

func ProjectEditToProto(response serverapi.ProjectEditGetResponse) (*projectpb.GetProjectEditSuccess, error) {
	success := &projectpb.GetProjectEditSuccess{
		ProjectId: response.ProjectID, ProjectKey: response.ProjectKey, DisplayName: response.DisplayName,
	}
	if err := Validate(success); err != nil {
		return nil, fmt.Errorf("convert project edit response to protobuf: %w", err)
	}
	return success, nil
}

func ProjectEditFromProto(success *projectpb.GetProjectEditSuccess) (serverapi.ProjectEditGetResponse, error) {
	if err := Validate(success); err != nil {
		return serverapi.ProjectEditGetResponse{}, err
	}
	return serverapi.ProjectEditGetResponse{
		ProjectID: success.ProjectId, ProjectKey: success.ProjectKey, DisplayName: success.DisplayName,
	}, nil
}

func ProjectWorkspaceListRequestToProto(request serverapi.ProjectWorkspaceListRequest) (*projectpb.ProjectWorkspaceListRequest, error) {
	if err := request.Validate(); err != nil {
		return nil, err
	}
	offset, err := projectInt32(request.Offset, "project workspace offset")
	if err != nil {
		return nil, err
	}
	limit, err := projectInt32(request.Limit, "project workspace limit")
	if err != nil {
		return nil, err
	}
	return &projectpb.ProjectWorkspaceListRequest{ProjectId: request.ProjectID, Offset: offset, Limit: limit}, nil
}

func ProjectWorkspaceListRequestFromProto(message *projectpb.ProjectWorkspaceListRequest) (serverapi.ProjectWorkspaceListRequest, error) {
	request := serverapi.ProjectWorkspaceListRequest{
		ProjectID: message.ProjectId, Offset: int(message.Offset), Limit: int(message.Limit),
	}
	return request, nil
}

func ProjectWorkspaceListToProto(response serverapi.ProjectWorkspaceListResponse) (*projectpb.ListProjectWorkspacesSuccess, error) {
	if err := response.Validate(); err != nil {
		return nil, err
	}
	offset, err := projectInt32(response.Offset, "project workspace response offset")
	if err != nil {
		return nil, err
	}
	workspaces := mapSlice(response.Workspaces, projectWorkspaceCatalogToProto)
	success := &projectpb.ListProjectWorkspacesSuccess{
		ProjectId: response.ProjectID, Offset: offset, Workspaces: workspaces,
	}
	if response.NextOffset != nil {
		value, err := projectInt32(*response.NextOffset, "project workspace next_offset")
		if err != nil {
			return nil, err
		}
		success.NextOffset = &value
	}
	if err := Validate(success); err != nil {
		return nil, fmt.Errorf("convert project workspace list to protobuf: %w", err)
	}
	return success, nil
}

func ProjectWorkspaceListFromProto(success *projectpb.ListProjectWorkspacesSuccess) (serverapi.ProjectWorkspaceListResponse, error) {
	if err := Validate(success); err != nil {
		return serverapi.ProjectWorkspaceListResponse{}, err
	}
	response := serverapi.ProjectWorkspaceListResponse{
		ProjectID: success.ProjectId,
		Offset:    int(success.Offset),
		Workspaces: mapSlice(success.Workspaces, func(workspace *projectpb.ProjectWorkspaceCatalogSummary) serverapi.ProjectWorkspaceCatalogRow {
			return projectWorkspaceCatalogFromProto(workspace)
		}),
	}
	if success.NextOffset != nil {
		value := int(*success.NextOffset)
		response.NextOffset = &value
	}
	return response, response.Validate()
}

func ProjectWorkspaceGetRequestToProto(request serverapi.ProjectWorkspaceGetRequest) (*projectpb.GetProjectWorkspaceRequest, error) {
	if err := request.Validate(); err != nil {
		return nil, err
	}
	message := &projectpb.GetProjectWorkspaceRequest{ProjectId: request.ProjectID}
	if workspaceID := request.WorkspaceIDValue(); workspaceID != nil {
		message.Selector = &projectpb.GetProjectWorkspaceRequest_WorkspaceId{WorkspaceId: *workspaceID}
	} else if workspaceRoot := request.WorkspaceRootValue(); workspaceRoot != nil {
		message.Selector = &projectpb.GetProjectWorkspaceRequest_WorkspaceRoot{WorkspaceRoot: *workspaceRoot}
	}
	if err := Validate(message); err != nil {
		return nil, err
	}
	return message, nil
}

func ProjectWorkspaceGetRequestFromProto(message *projectpb.GetProjectWorkspaceRequest) (serverapi.ProjectWorkspaceGetRequest, error) {
	var (
		selector serverapi.ProjectWorkspaceSelector
		err      error
	)
	switch value := message.Selector.(type) {
	case *projectpb.GetProjectWorkspaceRequest_WorkspaceId:
		selector, err = serverapi.NewProjectWorkspaceSelectorForID(value.WorkspaceId)
	case *projectpb.GetProjectWorkspaceRequest_WorkspaceRoot:
		selector, err = serverapi.NewProjectWorkspaceSelectorForRoot(value.WorkspaceRoot)
	default:
		err = fmt.Errorf("project workspace selector is required")
	}
	if err != nil {
		return serverapi.ProjectWorkspaceGetRequest{}, err
	}
	request := serverapi.ProjectWorkspaceGetRequest{ProjectID: message.ProjectId, ProjectWorkspaceSelector: selector}
	return request, nil
}

func ProjectWorkspaceGetToProto(response serverapi.ProjectWorkspaceGetResponse) (*projectpb.GetProjectWorkspaceSuccess, error) {
	if err := response.Validate(); err != nil {
		return nil, err
	}
	result, err := projectWorkspaceGetResultToProto(response.Result)
	if err != nil {
		return nil, err
	}
	success := &projectpb.GetProjectWorkspaceSuccess{ProjectId: response.ProjectID, Result: result}
	if response.Workspace != nil {
		success.Workspace = projectWorkspaceCatalogToProto(*response.Workspace)
	}
	if err := Validate(success); err != nil {
		return nil, err
	}
	return success, nil
}

func ProjectWorkspaceGetFromProto(success *projectpb.GetProjectWorkspaceSuccess) (serverapi.ProjectWorkspaceGetResponse, error) {
	if err := Validate(success); err != nil {
		return serverapi.ProjectWorkspaceGetResponse{}, err
	}
	result, err := projectWorkspaceGetResultFromProto(success.Result)
	if err != nil {
		return serverapi.ProjectWorkspaceGetResponse{}, err
	}
	response := serverapi.ProjectWorkspaceGetResponse{ProjectID: success.ProjectId, Result: result}
	if success.Workspace != nil {
		workspace := projectWorkspaceCatalogFromProto(success.Workspace)
		response.Workspace = &workspace
	}
	return response, response.Validate()
}

func ProjectOverviewRequestToProto(request serverapi.ProjectGetOverviewRequest) (*projectpb.GetOverviewRequest, error) {
	if err := request.Validate(); err != nil {
		return nil, err
	}
	return &projectpb.GetOverviewRequest{ProjectId: request.ProjectID}, nil
}

func ProjectOverviewRequestFromProto(message *projectpb.GetOverviewRequest) (serverapi.ProjectGetOverviewRequest, error) {
	request := serverapi.ProjectGetOverviewRequest{ProjectID: message.ProjectId}
	return request, nil
}

func ProjectOverviewToProto(response serverapi.ProjectGetOverviewResponse) (*projectpb.GetOverviewSuccess, error) {
	project, err := projectSummaryToProto(response.Overview.Project)
	if err != nil {
		return nil, err
	}
	workspaces, err := mapSliceError(response.Overview.Workspaces, projectWorkspaceSummaryToProto)
	if err != nil {
		return nil, err
	}
	success := &projectpb.GetOverviewSuccess{
		Overview: &projectpb.ProjectOverview{Project: project, Workspaces: workspaces},
	}
	if err := Validate(success); err != nil {
		return nil, fmt.Errorf("convert project overview to protobuf: %w", err)
	}
	return success, nil
}

func ProjectOverviewFromProto(success *projectpb.GetOverviewSuccess) (serverapi.ProjectGetOverviewResponse, error) {
	if err := Validate(success); err != nil {
		return serverapi.ProjectGetOverviewResponse{}, err
	}
	project, err := projectSummaryFromProto(success.Overview.Project)
	if err != nil {
		return serverapi.ProjectGetOverviewResponse{}, err
	}
	workspaces, err := mapSliceError(success.Overview.Workspaces, projectWorkspaceSummaryFromProto)
	if err != nil {
		return serverapi.ProjectGetOverviewResponse{}, err
	}
	return serverapi.ProjectGetOverviewResponse{
		Overview: clientui.ProjectOverview{Project: project, Workspaces: workspaces},
	}, nil
}

func WorkspaceBindingAmbiguousToProto(err serverapi.WorkspaceBindingAmbiguousError) (*projectpb.WorkspaceBindingAmbiguousDetails, error) {
	details := &projectpb.WorkspaceBindingAmbiguousDetails{
		CanonicalRoot: err.CanonicalRoot,
		ProjectIds:    append([]string(nil), err.ProjectIDs...),
	}
	if err := Validate(details); err != nil {
		return nil, fmt.Errorf("convert ambiguous workspace binding to protobuf: %w", err)
	}
	return details, nil
}

func WorkspaceBindingAmbiguousFromProto(details *projectpb.WorkspaceBindingAmbiguousDetails) (serverapi.WorkspaceBindingAmbiguousError, error) {
	if err := Validate(details); err != nil {
		return serverapi.WorkspaceBindingAmbiguousError{}, fmt.Errorf("convert ambiguous workspace binding from protobuf: %w", err)
	}
	return serverapi.WorkspaceBindingAmbiguousError{
		CanonicalRoot: details.CanonicalRoot,
		ProjectIDs:    append([]string(nil), details.ProjectIds...),
	}, nil
}

func ProjectUnavailableToProto(err serverapi.ProjectUnavailableError) (*projectpb.ProjectUnavailableDetails, error) {
	availability, conversionErr := projectAvailabilityToProto(err.Availability)
	if conversionErr != nil {
		return nil, conversionErr
	}
	details := &projectpb.ProjectUnavailableDetails{
		ProjectId:    err.ProjectID,
		RootPath:     err.RootPath,
		Availability: availability,
	}
	if conversionErr := Validate(details); conversionErr != nil {
		return nil, fmt.Errorf("convert unavailable project to protobuf: %w", conversionErr)
	}
	return details, nil
}

func ProjectUnavailableFromProto(details *projectpb.ProjectUnavailableDetails) (serverapi.ProjectUnavailableError, error) {
	if err := Validate(details); err != nil {
		return serverapi.ProjectUnavailableError{}, fmt.Errorf("convert unavailable project from protobuf: %w", err)
	}
	availability, err := projectAvailabilityFromProto(details.Availability)
	if err != nil {
		return serverapi.ProjectUnavailableError{}, err
	}
	return serverapi.ProjectUnavailableError{
		ProjectID:    details.ProjectId,
		RootPath:     details.RootPath,
		Availability: availability,
	}, nil
}

func projectSummaryToProto(summary clientui.ProjectSummary) (*projectpb.ProjectSummary, error) {
	availability, err := projectAvailabilityToProto(summary.Availability)
	if err != nil {
		return nil, err
	}
	sessionCount, err := projectInt32(summary.SessionCount, "project session_count")
	if err != nil {
		return nil, err
	}
	return &projectpb.ProjectSummary{
		ProjectId: summary.ProjectID, ProjectKey: summary.ProjectKey, DisplayName: summary.DisplayName,
		RootPath: summary.RootPath, Availability: availability, SessionCount: sessionCount,
		UpdatedAt: timestamppb.New(summary.UpdatedAt),
	}, nil
}

func projectSummaryFromProto(summary *projectpb.ProjectSummary) (clientui.ProjectSummary, error) {
	availability, err := projectAvailabilityFromProto(summary.Availability)
	if err != nil {
		return clientui.ProjectSummary{}, err
	}
	return clientui.ProjectSummary{
		ProjectID: summary.ProjectId, ProjectKey: summary.ProjectKey, DisplayName: summary.DisplayName,
		RootPath: summary.RootPath, Availability: availability, SessionCount: int(summary.SessionCount),
		UpdatedAt: summary.UpdatedAt.AsTime(),
	}, nil
}

func projectWorkspaceSummaryToProto(summary clientui.ProjectWorkspaceSummary) (*projectpb.ProjectWorkspaceSummary, error) {
	availability, err := projectAvailabilityToProto(summary.Availability)
	if err != nil {
		return nil, err
	}
	sessionCount, err := projectInt32(summary.SessionCount, "project workspace session_count")
	if err != nil {
		return nil, err
	}
	return &projectpb.ProjectWorkspaceSummary{
		WorkspaceId: summary.WorkspaceID, DisplayName: summary.DisplayName, RootPath: summary.RootPath,
		Availability: availability, IsPrimary: summary.IsPrimary, SessionCount: sessionCount,
		UpdatedAt: timestamppb.New(summary.UpdatedAt),
	}, nil
}

func projectWorkspaceSummaryFromProto(summary *projectpb.ProjectWorkspaceSummary) (clientui.ProjectWorkspaceSummary, error) {
	availability, err := projectAvailabilityFromProto(summary.Availability)
	if err != nil {
		return clientui.ProjectWorkspaceSummary{}, err
	}
	return clientui.ProjectWorkspaceSummary{
		WorkspaceID: summary.WorkspaceId, DisplayName: summary.DisplayName, RootPath: summary.RootPath,
		Availability: availability, IsPrimary: summary.IsPrimary, SessionCount: int(summary.SessionCount),
		UpdatedAt: summary.UpdatedAt.AsTime(),
	}, nil
}

func projectBindingToProto(binding serverapi.ProjectBinding) (*projectpb.ProjectBinding, error) {
	availability, err := projectAvailabilityToProto(clientui.ProjectAvailability(binding.WorkspaceStatus))
	if err != nil {
		return nil, err
	}
	return &projectpb.ProjectBinding{
		ProjectId: binding.ProjectID, ProjectKey: binding.ProjectKey, ProjectName: binding.ProjectName,
		WorkspaceId: binding.WorkspaceID, CanonicalRoot: binding.CanonicalRoot,
		WorkspaceName: binding.WorkspaceName, WorkspaceStatus: availability,
	}, nil
}

func projectBindingFromProto(binding *projectpb.ProjectBinding) (serverapi.ProjectBinding, error) {
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

func projectHomeSummaryToProto(summary serverapi.ProjectHomeSummary) (*projectpb.ProjectHomeSummary, error) {
	if err := summary.Validate(); err != nil {
		return nil, err
	}
	taskCount, err := projectInt32(summary.TaskCount, "project task_count")
	if err != nil {
		return nil, err
	}
	attentionCount, err := projectInt32(summary.AttentionCount, "project attention_count")
	if err != nil {
		return nil, err
	}
	workflowCount, err := projectInt32(summary.WorkflowCount, "project workflow_count")
	if err != nil {
		return nil, err
	}
	message := &projectpb.ProjectHomeSummary{
		ProjectId: summary.ProjectID, ProjectKey: summary.ProjectKey, DisplayName: summary.DisplayName,
		DefaultWorkflowValid: summary.DefaultWorkflowValid,
		UpdatedAt:            timestamppb.New(time.UnixMilli(summary.UpdatedAtUnixMs)),
		TaskCount:            taskCount, AttentionCount: attentionCount, WorkflowCount: workflowCount,
	}
	availability, err := projectAvailabilityToProto(clientui.ProjectAvailability(summary.PrimaryWorkspace.Availability))
	if err != nil {
		return nil, err
	}
	message.PrimaryWorkspace = &projectpb.ProjectHomeWorkspaceSummary{
		WorkspaceId: summary.PrimaryWorkspace.WorkspaceID, DisplayName: summary.PrimaryWorkspace.DisplayName,
		RootPath: summary.PrimaryWorkspace.RootPath, Availability: availability,
		IsPrimary: summary.PrimaryWorkspace.IsPrimary,
		UpdatedAt: timestamppb.New(time.UnixMilli(summary.PrimaryWorkspace.UpdatedAtUnixMs)),
	}
	if summary.DefaultWorkflowID != nil {
		value := summary.DefaultWorkflowID.String()
		message.DefaultWorkflowId = &value
		message.DefaultWorkflowName = &summary.DefaultWorkflowName
	}
	return message, nil
}

func projectHomeSummaryFromProto(summary *projectpb.ProjectHomeSummary) (serverapi.ProjectHomeSummary, error) {
	availability, err := projectAvailabilityFromProto(summary.PrimaryWorkspace.Availability)
	if err != nil {
		return serverapi.ProjectHomeSummary{}, err
	}
	result := serverapi.ProjectHomeSummary{
		ProjectID: summary.ProjectId, ProjectKey: summary.ProjectKey, DisplayName: summary.DisplayName,
		PrimaryWorkspace: serverapi.ProjectWorkspaceSummary{
			WorkspaceID: summary.PrimaryWorkspace.WorkspaceId, DisplayName: summary.PrimaryWorkspace.DisplayName,
			RootPath: summary.PrimaryWorkspace.RootPath, Availability: string(availability),
			IsPrimary:       summary.PrimaryWorkspace.IsPrimary,
			UpdatedAtUnixMs: summary.PrimaryWorkspace.UpdatedAt.AsTime().UnixMilli(),
		},
		DefaultWorkflowName: summary.GetDefaultWorkflowName(), DefaultWorkflowValid: summary.DefaultWorkflowValid,
		UpdatedAtUnixMs: summary.UpdatedAt.AsTime().UnixMilli(), TaskCount: int(summary.TaskCount),
		AttentionCount: int(summary.AttentionCount), WorkflowCount: int(summary.WorkflowCount),
	}
	if summary.DefaultWorkflowId != nil {
		id, err := runtimeids.ParseWorkflowID(*summary.DefaultWorkflowId)
		if err != nil {
			return serverapi.ProjectHomeSummary{}, err
		}
		result.DefaultWorkflowID = &id
	}
	return result, result.Validate()
}

func projectWorkspaceCatalogToProto(workspace serverapi.ProjectWorkspaceCatalogRow) *projectpb.ProjectWorkspaceCatalogSummary {
	return &projectpb.ProjectWorkspaceCatalogSummary{
		WorkspaceId: workspace.WorkspaceID, DisplayName: workspace.DisplayName,
		RootPath: workspace.RootPath, IsDefault: workspace.IsDefault,
	}
}

func projectWorkspaceCatalogFromProto(workspace *projectpb.ProjectWorkspaceCatalogSummary) serverapi.ProjectWorkspaceCatalogRow {
	return serverapi.ProjectWorkspaceCatalogRow{
		WorkspaceID: workspace.WorkspaceId, DisplayName: workspace.DisplayName,
		RootPath: workspace.RootPath, IsDefault: workspace.IsDefault,
	}
}

func projectAvailabilityToProto(value clientui.ProjectAvailability) (projectpb.ProjectAvailability, error) {
	switch value {
	case clientui.ProjectAvailabilityAvailable:
		return projectpb.ProjectAvailability_PROJECT_AVAILABILITY_AVAILABLE, nil
	case clientui.ProjectAvailabilityMissing:
		return projectpb.ProjectAvailability_PROJECT_AVAILABILITY_MISSING, nil
	case clientui.ProjectAvailabilityInaccessible:
		return projectpb.ProjectAvailability_PROJECT_AVAILABILITY_INACCESSIBLE, nil
	case clientui.ProjectAvailabilityUnlinked:
		return projectpb.ProjectAvailability_PROJECT_AVAILABILITY_UNLINKED, nil
	default:
		return 0, fmt.Errorf("project availability %q is unsupported", value)
	}
}

func projectAvailabilityFromProto(value projectpb.ProjectAvailability) (clientui.ProjectAvailability, error) {
	switch value {
	case projectpb.ProjectAvailability_PROJECT_AVAILABILITY_AVAILABLE:
		return clientui.ProjectAvailabilityAvailable, nil
	case projectpb.ProjectAvailability_PROJECT_AVAILABILITY_MISSING:
		return clientui.ProjectAvailabilityMissing, nil
	case projectpb.ProjectAvailability_PROJECT_AVAILABILITY_INACCESSIBLE:
		return clientui.ProjectAvailabilityInaccessible, nil
	case projectpb.ProjectAvailability_PROJECT_AVAILABILITY_UNLINKED:
		return clientui.ProjectAvailabilityUnlinked, nil
	default:
		return "", fmt.Errorf("protobuf project availability %v is unsupported", value)
	}
}

func projectBindingPlanModeToProto(value serverapi.ProjectBindingPlanMode) (projectpb.WorkspaceBindingPlanMode, error) {
	switch value {
	case serverapi.ProjectBindingPlanModeInteractive:
		return projectpb.WorkspaceBindingPlanMode_WORKSPACE_BINDING_PLAN_MODE_INTERACTIVE, nil
	case serverapi.ProjectBindingPlanModeHeadless:
		return projectpb.WorkspaceBindingPlanMode_WORKSPACE_BINDING_PLAN_MODE_HEADLESS, nil
	default:
		return 0, fmt.Errorf("project binding plan mode %q is unsupported", value)
	}
}

func projectBindingPlanModeFromProto(value projectpb.WorkspaceBindingPlanMode) (serverapi.ProjectBindingPlanMode, error) {
	switch value {
	case projectpb.WorkspaceBindingPlanMode_WORKSPACE_BINDING_PLAN_MODE_INTERACTIVE:
		return serverapi.ProjectBindingPlanModeInteractive, nil
	case projectpb.WorkspaceBindingPlanMode_WORKSPACE_BINDING_PLAN_MODE_HEADLESS:
		return serverapi.ProjectBindingPlanModeHeadless, nil
	default:
		return "", fmt.Errorf("protobuf project binding plan mode %v is unsupported", value)
	}
}

func projectBindingPlanKindToProto(value serverapi.ProjectBindingPlanKind) (projectpb.WorkspaceBindingPlanKind, error) {
	switch value {
	case serverapi.ProjectBindingPlanKindBound:
		return projectpb.WorkspaceBindingPlanKind_WORKSPACE_BINDING_PLAN_KIND_BOUND, nil
	case serverapi.ProjectBindingPlanKindLocalUnbound:
		return projectpb.WorkspaceBindingPlanKind_WORKSPACE_BINDING_PLAN_KIND_LOCAL_UNBOUND, nil
	case serverapi.ProjectBindingPlanKindServerWorkspaceSelection:
		return projectpb.WorkspaceBindingPlanKind_WORKSPACE_BINDING_PLAN_KIND_SERVER_WORKSPACE_SELECTION, nil
	case serverapi.ProjectBindingPlanKindHeadlessRemoteSelected:
		return projectpb.WorkspaceBindingPlanKind_WORKSPACE_BINDING_PLAN_KIND_HEADLESS_REMOTE_SELECTED, nil
	case serverapi.ProjectBindingPlanKindHeadlessRemoteAmbiguous:
		return projectpb.WorkspaceBindingPlanKind_WORKSPACE_BINDING_PLAN_KIND_HEADLESS_REMOTE_AMBIGUOUS, nil
	default:
		return 0, fmt.Errorf("project binding plan kind %q is unsupported", value)
	}
}

func projectBindingPlanKindFromProto(value projectpb.WorkspaceBindingPlanKind) (serverapi.ProjectBindingPlanKind, error) {
	switch value {
	case projectpb.WorkspaceBindingPlanKind_WORKSPACE_BINDING_PLAN_KIND_BOUND:
		return serverapi.ProjectBindingPlanKindBound, nil
	case projectpb.WorkspaceBindingPlanKind_WORKSPACE_BINDING_PLAN_KIND_LOCAL_UNBOUND:
		return serverapi.ProjectBindingPlanKindLocalUnbound, nil
	case projectpb.WorkspaceBindingPlanKind_WORKSPACE_BINDING_PLAN_KIND_SERVER_WORKSPACE_SELECTION:
		return serverapi.ProjectBindingPlanKindServerWorkspaceSelection, nil
	case projectpb.WorkspaceBindingPlanKind_WORKSPACE_BINDING_PLAN_KIND_HEADLESS_REMOTE_SELECTED:
		return serverapi.ProjectBindingPlanKindHeadlessRemoteSelected, nil
	case projectpb.WorkspaceBindingPlanKind_WORKSPACE_BINDING_PLAN_KIND_HEADLESS_REMOTE_AMBIGUOUS:
		return serverapi.ProjectBindingPlanKindHeadlessRemoteAmbiguous, nil
	default:
		return "", fmt.Errorf("protobuf project binding plan kind %v is unsupported", value)
	}
}

func projectWorkspaceGetResultToProto(value serverapi.ProjectWorkspaceGetResult) (projectpb.ProjectWorkspaceGetResult, error) {
	switch value {
	case serverapi.ProjectWorkspaceGetResultAttached:
		return projectpb.ProjectWorkspaceGetResult_PROJECT_WORKSPACE_GET_RESULT_ATTACHED, nil
	case serverapi.ProjectWorkspaceGetResultNotAttached:
		return projectpb.ProjectWorkspaceGetResult_PROJECT_WORKSPACE_GET_RESULT_NOT_ATTACHED, nil
	default:
		return 0, fmt.Errorf("project workspace result %q is unsupported", value)
	}
}

func projectWorkspaceGetResultFromProto(value projectpb.ProjectWorkspaceGetResult) (serverapi.ProjectWorkspaceGetResult, error) {
	switch value {
	case projectpb.ProjectWorkspaceGetResult_PROJECT_WORKSPACE_GET_RESULT_ATTACHED:
		return serverapi.ProjectWorkspaceGetResultAttached, nil
	case projectpb.ProjectWorkspaceGetResult_PROJECT_WORKSPACE_GET_RESULT_NOT_ATTACHED:
		return serverapi.ProjectWorkspaceGetResultNotAttached, nil
	default:
		return "", fmt.Errorf("protobuf project workspace result %v is unsupported", value)
	}
}

func projectInt32(value int, field string) (int32, error) {
	if value < math.MinInt32 || value > math.MaxInt32 {
		return 0, fmt.Errorf("%s is out of int32 range", field)
	}
	return int32(value), nil
}
