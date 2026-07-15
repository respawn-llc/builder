package client

import (
	"context"
	"fmt"

	servicecontract "core/shared/apicontract"
	"core/shared/serverapi"
)

type ProjectViewClient = servicecontract.ProjectViewService
type loopbackProjectViewClient struct {
	loopbackClient[servicecontract.ProjectViewService]
}

func NewLoopbackProjectViewClient(service servicecontract.ProjectViewService) ProjectViewClient {
	return &loopbackProjectViewClient{loopbackClient: newLoopbackClient(service)}
}

func (c *loopbackProjectViewClient) ListProjects(ctx context.Context, req serverapi.ProjectListRequest) (serverapi.ProjectListResponse, error) {
	return callLoopbackClient(c, "project view service is required", ctx, req, servicecontract.ProjectViewService.ListProjects)
}

func (c *loopbackProjectViewClient) ListProjectHome(ctx context.Context, req serverapi.ProjectHomeListRequest) (serverapi.ProjectHomeListResponse, error) {
	return callLoopbackClient(c, "project view service is required", ctx, req, servicecontract.ProjectViewService.ListProjectHome)
}

func (c *loopbackProjectViewClient) ResolveProjectPath(ctx context.Context, req serverapi.ProjectResolvePathRequest) (serverapi.ProjectResolvePathResponse, error) {
	return callLoopbackClient(c, "project view service is required", ctx, req, servicecontract.ProjectViewService.ResolveProjectPath)
}

func (c *loopbackProjectViewClient) PlanWorkspaceBinding(ctx context.Context, req serverapi.ProjectBindingPlanRequest) (serverapi.ProjectBindingPlanResponse, error) {
	return callLoopbackClient(c, "project view service is required", ctx, req, servicecontract.ProjectViewService.PlanWorkspaceBinding)
}

func (c *loopbackProjectViewClient) CreateProject(ctx context.Context, req serverapi.ProjectCreateRequest) (serverapi.ProjectCreateResponse, error) {
	return callLoopbackClient(c, "project view service is required", ctx, req, servicecontract.ProjectViewService.CreateProject)
}

func (c *loopbackProjectViewClient) GetProjectEdit(ctx context.Context, req serverapi.ProjectEditGetRequest) (serverapi.ProjectEditGetResponse, error) {
	return callLoopbackClient(c, "project view service is required", ctx, req, servicecontract.ProjectViewService.GetProjectEdit)
}

func (c *loopbackProjectViewClient) UpdateProject(ctx context.Context, req serverapi.ProjectUpdateRequest) (serverapi.ProjectUpdateResponse, error) {
	return callLoopbackClient(c, "project view service is required", ctx, req, servicecontract.ProjectViewService.UpdateProject)
}

func (c *loopbackProjectViewClient) SetDefaultWorkspace(ctx context.Context, req serverapi.ProjectDefaultWorkspaceSetRequest) (serverapi.ProjectDefaultWorkspaceSetResponse, error) {
	return callLoopbackClient(c, "project view service is required", ctx, req, servicecontract.ProjectViewService.SetDefaultWorkspace)
}

func (c *loopbackProjectViewClient) ListProjectWorkspaces(ctx context.Context, req serverapi.ProjectWorkspaceListRequest) (serverapi.ProjectWorkspaceListResponse, error) {
	return callLoopbackClient(c, "project view service is required", ctx, req, servicecontract.ProjectViewService.ListProjectWorkspaces)
}

func (c *loopbackProjectViewClient) UnlinkWorkspaceFromProject(ctx context.Context, req serverapi.ProjectWorkspaceUnlinkRequest) (serverapi.ProjectWorkspaceUnlinkResponse, error) {
	return callLoopbackClient(c, "project view service is required", ctx, req, servicecontract.ProjectViewService.UnlinkWorkspaceFromProject)
}

func (c *loopbackProjectViewClient) DeleteProject(ctx context.Context, req serverapi.ProjectDeleteRequest) (serverapi.ProjectDeleteResponse, error) {
	return callLoopbackClient(c, "project view service is required", ctx, req, servicecontract.ProjectViewService.DeleteProject)
}

func (c *loopbackProjectViewClient) AttachWorkspaceToProject(ctx context.Context, req serverapi.ProjectAttachWorkspaceRequest) (serverapi.ProjectAttachWorkspaceResponse, error) {
	return callLoopbackClient(c, "project view service is required", ctx, req, servicecontract.ProjectViewService.AttachWorkspaceToProject)
}

func (c *loopbackProjectViewClient) RebindWorkspace(ctx context.Context, req serverapi.ProjectRebindWorkspaceRequest) (serverapi.ProjectRebindWorkspaceResponse, error) {
	return callLoopbackClient(c, "project view service is required", ctx, req, servicecontract.ProjectViewService.RebindWorkspace)
}

func (c *loopbackProjectViewClient) GetProjectOverview(ctx context.Context, req serverapi.ProjectGetOverviewRequest) (serverapi.ProjectGetOverviewResponse, error) {
	return callLoopbackClient(c, "project view service is required", ctx, req, servicecontract.ProjectViewService.GetProjectOverview)
}

func (c *loopbackProjectViewClient) ListSessionPage(ctx context.Context, req serverapi.SessionPageRequest) (serverapi.SessionPageResponse, error) {
	response, err := callLoopbackClient(c, "project view service is required", ctx, req, servicecontract.ProjectViewService.ListSessionPage)
	if err != nil {
		return serverapi.SessionPageResponse{}, err
	}
	if err := validateSessionPageResponseIdentity(req, response); err != nil {
		return serverapi.SessionPageResponse{}, err
	}
	return response, nil
}

func validateSessionPageResponseIdentity(req serverapi.SessionPageRequest, response serverapi.SessionPageResponse) error {
	if err := response.Validate(); err != nil {
		return fmt.Errorf("session page response is invalid: %w", err)
	}
	if response.ProjectID != req.ProjectID {
		return fmt.Errorf("session page response project %q does not match request project %q", response.ProjectID, req.ProjectID)
	}
	if response.Category != req.Category {
		return fmt.Errorf("session page response category %q does not match request category %q", response.Category, req.Category)
	}
	return nil
}
