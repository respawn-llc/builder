package projectbinding

import (
	"context"
	"errors"
	"testing"

	"core/shared/client"
	"core/shared/clientui"
	"core/shared/config"
	"core/shared/serverapi"
)

type testServer struct {
	cfg       config.App
	client    client.ProjectViewClient
	bindCalls []serverapi.ProjectBinding
}

func (s *testServer) Config() config.App { return s.cfg }

func (s *testServer) PresentationTheme() string { return "dark" }
func (s *testServer) ProjectViewClient() client.ProjectViewClient {
	return s.client
}
func (s *testServer) BindProjectWorkspace(_ context.Context, projectID string, workspaceID string) (*testServer, error) {
	s.bindCalls = append(s.bindCalls, serverapi.ProjectBinding{ProjectID: projectID, WorkspaceID: workspaceID})
	return s, nil
}

type testProjectViewClient struct {
	plan       serverapi.ProjectBindingPlanResponse
	create     serverapi.ProjectCreateResponse
	attach     serverapi.ProjectAttachWorkspaceResponse
	overview   serverapi.ProjectGetOverviewResponse
	createReq  serverapi.ProjectCreateRequest
	attachReq  serverapi.ProjectAttachWorkspaceRequest
	planCalled bool
}

func (c *testProjectViewClient) ListProjects(context.Context, serverapi.ProjectListRequest) (serverapi.ProjectListResponse, error) {
	return serverapi.ProjectListResponse{}, nil
}
func (c *testProjectViewClient) ListProjectHome(context.Context, serverapi.ProjectHomeListRequest) (serverapi.ProjectHomeListResponse, error) {
	return serverapi.ProjectHomeListResponse{}, nil
}
func (c *testProjectViewClient) ResolveProjectPath(context.Context, serverapi.ProjectResolvePathRequest) (serverapi.ProjectResolvePathResponse, error) {
	return serverapi.ProjectResolvePathResponse{}, nil
}
func (c *testProjectViewClient) PlanWorkspaceBinding(context.Context, serverapi.ProjectBindingPlanRequest) (serverapi.ProjectBindingPlanResponse, error) {
	c.planCalled = true
	return c.plan, nil
}
func (c *testProjectViewClient) CreateProject(_ context.Context, req serverapi.ProjectCreateRequest) (serverapi.ProjectCreateResponse, error) {
	c.createReq = req
	return c.create, nil
}
func (c *testProjectViewClient) AttachWorkspaceToProject(_ context.Context, req serverapi.ProjectAttachWorkspaceRequest) (serverapi.ProjectAttachWorkspaceResponse, error) {
	c.attachReq = req
	return c.attach, nil
}
func (c *testProjectViewClient) ListProjectWorkspaces(context.Context, serverapi.ProjectWorkspaceListRequest) (serverapi.ProjectWorkspaceListResponse, error) {
	return serverapi.ProjectWorkspaceListResponse{}, nil
}
func (c *testProjectViewClient) RebindWorkspace(context.Context, serverapi.ProjectRebindWorkspaceRequest) (serverapi.ProjectRebindWorkspaceResponse, error) {
	return serverapi.ProjectRebindWorkspaceResponse{}, nil
}
func (c *testProjectViewClient) GetProjectOverview(context.Context, serverapi.ProjectGetOverviewRequest) (serverapi.ProjectGetOverviewResponse, error) {
	return c.overview, nil
}
func (c *testProjectViewClient) ListSessionPage(context.Context, serverapi.SessionPageRequest) (serverapi.SessionPageResponse, error) {
	return serverapi.SessionPageResponse{}, nil
}

func TestEnsureInteractiveBindsExistingPlan(t *testing.T) {
	projectClient := &testProjectViewClient{plan: serverapi.ProjectBindingPlanResponse{
		Kind:          serverapi.ProjectBindingPlanKindBound,
		CanonicalRoot: "/canonical",
		Binding:       &serverapi.ProjectBinding{ProjectID: "project-1", WorkspaceID: "workspace-1"},
	}}
	server := &testServer{cfg: config.App{WorkspaceRoot: "/workspace"}, client: projectClient}

	bound, err := EnsureInteractive[*testServer](context.Background(), Request[*testServer]{Server: server})
	if err != nil {
		t.Fatalf("ensure interactive: %v", err)
	}
	if bound != server {
		t.Fatal("expected bound server")
	}
	if !projectClient.planCalled {
		t.Fatal("expected binding plan request")
	}
	if len(server.bindCalls) != 1 || server.bindCalls[0].ProjectID != "project-1" || server.bindCalls[0].WorkspaceID != "workspace-1" {
		t.Fatalf("unexpected bind calls: %+v", server.bindCalls)
	}
}

func TestEnsureInteractiveCreatesProjectForLocalUnboundPath(t *testing.T) {
	projectClient := &testProjectViewClient{
		plan: serverapi.ProjectBindingPlanResponse{Kind: serverapi.ProjectBindingPlanKindLocalUnbound},
		create: serverapi.ProjectCreateResponse{Binding: serverapi.ProjectBinding{
			ProjectID:   "project-created",
			WorkspaceID: "workspace-created",
		}},
	}
	server := &testServer{cfg: config.App{WorkspaceRoot: "/tmp/workspace"}, client: projectClient}

	_, err := EnsureInteractive[*testServer](context.Background(), Request[*testServer]{
		Server: server,
		PickLocalProject: func([]clientui.ProjectSummary, string) (ProjectPickerResult, error) {
			return ProjectPickerResult{CreateNew: true}, nil
		},
		PromptProjectName: func(defaultName string, theme string) (string, error) {
			if defaultName != "workspace" {
				t.Fatalf("default name = %q, want workspace", defaultName)
			}
			return "Created Project", nil
		},
	})
	if err != nil {
		t.Fatalf("ensure interactive: %v", err)
	}
	if projectClient.createReq.DisplayName != "Created Project" || projectClient.createReq.WorkspaceRoot != "/tmp/workspace" {
		t.Fatalf("unexpected create request: %+v", projectClient.createReq)
	}
	if len(server.bindCalls) != 1 || server.bindCalls[0].ProjectID != "project-created" || server.bindCalls[0].WorkspaceID != "workspace-created" {
		t.Fatalf("unexpected bind calls: %+v", server.bindCalls)
	}
}

func TestEnsureInteractivePropagatesCanceledPicker(t *testing.T) {
	projectClient := &testProjectViewClient{plan: serverapi.ProjectBindingPlanResponse{Kind: serverapi.ProjectBindingPlanKindLocalUnbound}}
	server := &testServer{cfg: config.App{WorkspaceRoot: "/workspace"}, client: projectClient}

	_, err := EnsureInteractive[*testServer](context.Background(), Request[*testServer]{
		Server: server,
		PickLocalProject: func([]clientui.ProjectSummary, string) (ProjectPickerResult, error) {
			return ProjectPickerResult{Canceled: true}, nil
		},
	})
	if err == nil || !errors.Is(err, ErrStartupCanceledByUser) {
		t.Fatalf("expected canceled error, got %v", err)
	}
}

func TestFormatMutationErrorWrapsMissingWorkspace(t *testing.T) {
	err := FormatMutationError("/workspace", "project-1", serverapi.ErrWorkspaceNotRegistered)
	if !errors.Is(err, serverapi.ErrWorkspaceNotRegistered) {
		t.Fatalf("expected wrapped workspace registration error, got %v", err)
	}
	if err == nil || err.Error() == serverapi.ErrWorkspaceNotRegistered.Error() {
		t.Fatalf("expected contextual error, got %v", err)
	}
}
