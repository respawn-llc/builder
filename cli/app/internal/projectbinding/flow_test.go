package projectbinding

import (
	"context"
	"errors"
	"testing"

	"core/shared/apicontract"
	"core/shared/clientui"
	"core/shared/config"
	projectpb "core/shared/protoapi/gen/kent/api/project"
	"core/shared/serverapi"

	"google.golang.org/protobuf/types/known/emptypb"
)

type testServer struct {
	cfg       config.App
	client    apicontract.ProjectViewService
	bindCalls []serverapi.ProjectBinding
}

func (s *testServer) Config() config.App { return s.cfg }

func (s *testServer) PresentationTheme() string { return "dark" }
func (s *testServer) ProjectViewClient() apicontract.ProjectViewService {
	return s.client
}
func (s *testServer) BindProjectWorkspace(_ context.Context, projectID string, workspaceID string) (*testServer, error) {
	s.bindCalls = append(s.bindCalls, serverapi.ProjectBinding{ProjectID: projectID, WorkspaceID: workspaceID})
	return s, nil
}

type testProjectViewClient struct {
	plan          projectpb.PlanWorkspaceBindingSuccess
	create        projectpb.CreateProjectSuccess
	attach        projectpb.AttachWorkspaceSuccess
	overview      projectpb.GetOverviewSuccess
	createReq     *projectpb.CreateProjectRequest
	attachReq     *projectpb.AttachWorkspaceRequest
	planCalled    bool
	overviewCalls int
}

func (c *testProjectViewClient) ListProjects(context.Context, *emptypb.Empty) (*projectpb.ProjectListSuccess, error) {
	return &projectpb.ProjectListSuccess{}, nil
}
func (c *testProjectViewClient) ListProjectHome(context.Context, *projectpb.ProjectHomeListRequest) (*projectpb.ProjectHomeListSuccess, error) {
	return &projectpb.ProjectHomeListSuccess{}, nil
}
func (c *testProjectViewClient) ResolveProjectPath(context.Context, *projectpb.ResolvePathRequest) (*projectpb.ResolvePathSuccess, error) {
	return &projectpb.ResolvePathSuccess{}, nil
}
func (c *testProjectViewClient) PlanWorkspaceBinding(context.Context, *projectpb.PlanWorkspaceBindingRequest) (*projectpb.PlanWorkspaceBindingSuccess, error) {
	c.planCalled = true
	return &c.plan, nil
}
func (c *testProjectViewClient) CreateProject(_ context.Context, req *projectpb.CreateProjectRequest) (*projectpb.CreateProjectSuccess, error) {
	c.createReq = req
	return &c.create, nil
}
func (c *testProjectViewClient) AttachWorkspaceToProject(_ context.Context, req *projectpb.AttachWorkspaceRequest) (*projectpb.AttachWorkspaceSuccess, error) {
	c.attachReq = req
	return &c.attach, nil
}
func (c *testProjectViewClient) ListProjectWorkspaces(context.Context, *projectpb.ProjectWorkspaceListRequest) (*projectpb.ListProjectWorkspacesSuccess, error) {
	return &projectpb.ListProjectWorkspacesSuccess{}, nil
}
func (c *testProjectViewClient) RebindWorkspace(context.Context, *projectpb.RebindWorkspaceRequest) (*projectpb.RebindWorkspaceSuccess, error) {
	return &projectpb.RebindWorkspaceSuccess{}, nil
}
func (c *testProjectViewClient) GetProjectOverview(context.Context, *projectpb.GetOverviewRequest) (*projectpb.GetOverviewSuccess, error) {
	c.overviewCalls++
	return &c.overview, nil
}
func (c *testProjectViewClient) ListSessionPage(context.Context, *projectpb.SessionPageRequest) (*projectpb.SessionPageSuccess, error) {
	return &projectpb.SessionPageSuccess{}, nil
}

func TestEnsureInteractiveBindsExistingPlan(t *testing.T) {
	projectClient := &testProjectViewClient{plan: projectpb.PlanWorkspaceBindingSuccess{
		Kind:          projectpb.WorkspaceBindingPlanKind_WORKSPACE_BINDING_PLAN_KIND_BOUND,
		CanonicalRoot: "/canonical",
		Binding: &projectpb.ProjectBinding{
			ProjectId:       "project-1",
			WorkspaceId:     "workspace-1",
			WorkspaceStatus: projectpb.ProjectAvailability_PROJECT_AVAILABILITY_AVAILABLE,
		},
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
		plan: projectpb.PlanWorkspaceBindingSuccess{Kind: projectpb.WorkspaceBindingPlanKind_WORKSPACE_BINDING_PLAN_KIND_LOCAL_UNBOUND},
		create: projectpb.CreateProjectSuccess{Binding: &projectpb.ProjectMutationBinding{
			ProjectId:       "project-created",
			WorkspaceId:     "workspace-created",
			WorkspaceStatus: projectpb.ProjectAvailability_PROJECT_AVAILABILITY_AVAILABLE,
		}},
	}
	server := &testServer{cfg: config.App{WorkspaceRoot: "/tmp/workspace"}, client: projectClient}

	_, err := EnsureInteractive[*testServer](context.Background(), Request[*testServer]{
		Server: server,
		PickLocalProject: func(context.Context, []clientui.ProjectSummary, string, ProjectPickerSnapshot) (ProjectPickerResult, error) {
			return ProjectPickerCreateNew{}, nil
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
	if projectClient.createReq == nil ||
		projectClient.createReq.DisplayName != "Created Project" ||
		projectClient.createReq.WorkspaceRoot != "/tmp/workspace" {
		t.Fatalf("unexpected create request: %+v", projectClient.createReq)
	}
	if len(server.bindCalls) != 1 || server.bindCalls[0].ProjectID != "project-created" || server.bindCalls[0].WorkspaceID != "workspace-created" {
		t.Fatalf("unexpected bind calls: %+v", server.bindCalls)
	}
}

func TestEnsureInteractivePropagatesCanceledPicker(t *testing.T) {
	projectClient := &testProjectViewClient{plan: projectpb.PlanWorkspaceBindingSuccess{Kind: projectpb.WorkspaceBindingPlanKind_WORKSPACE_BINDING_PLAN_KIND_LOCAL_UNBOUND}}
	server := &testServer{cfg: config.App{WorkspaceRoot: "/workspace"}, client: projectClient}

	_, err := EnsureInteractive[*testServer](context.Background(), Request[*testServer]{
		Server: server,
		PickLocalProject: func(context.Context, []clientui.ProjectSummary, string, ProjectPickerSnapshot) (ProjectPickerResult, error) {
			return ProjectPickerExit{}, nil
		},
	})
	if err == nil || !errors.Is(err, ErrStartupCanceledByUser) {
		t.Fatalf("expected canceled error, got %v", err)
	}
}

func TestSelectWorkspaceForStartupUsesCatalogLoader(t *testing.T) {
	projectClient := &testProjectViewClient{}
	server := &testServer{client: projectClient}
	seen := false
	result, err := SelectWorkspaceForStartup(context.Background(), WorkspaceSelectionRequest{
		Server:    server,
		ProjectID: "project-1",
		PickWorkspace: func(ctx context.Context, loader WorkspacePageLoader, projectID string, theme string) (WorkspacePickerResult, error) {
			seen = ctx != nil && loader != nil && projectID == "project-1" && theme == "dark"
			return WorkspacePickerSelected{Workspace: &projectpb.ProjectWorkspaceCatalogSummary{
				WorkspaceId: "workspace-1", RootPath: "/workspace-1",
			}}, nil
		},
	})
	if err != nil {
		t.Fatalf("select workspace: %v", err)
	}
	if !seen || projectClient.overviewCalls != 0 {
		t.Fatalf("catalog selection used stale overview path: loader=%t overview_calls=%d", seen, projectClient.overviewCalls)
	}
	if selected, ok := result.(WorkspacePickerSelected); !ok || selected.Workspace.WorkspaceId != "workspace-1" {
		t.Fatalf("selection result = %#v", result)
	}
}

func TestEnsureServerBrowsingBindingRestoresProjectPickerSnapshotAfterWorkspaceBack(t *testing.T) {
	projectClient := &testProjectViewClient{}
	server := &testServer{client: projectClient}
	pickerCalls := 0
	var restored ProjectPickerSnapshot
	workspaceCalls := 0
	bound, err := ensureServerBrowsingBinding[*testServer](context.Background(), Request[*testServer]{
		Server: server,
		PickServerProject: func(_ context.Context, _ []clientui.ProjectSummary, _ string, snapshot ProjectPickerSnapshot) (ProjectPickerResult, error) {
			pickerCalls++
			if pickerCalls == 1 {
				return ProjectPickerSelected{
					Project:  clientui.ProjectSummary{ProjectID: "project-1"},
					Snapshot: ProjectPickerSnapshot{Cursor: 7, Offset: 3},
				}, nil
			}
			restored = snapshot
			return ProjectPickerSelected{
				Project:  clientui.ProjectSummary{ProjectID: "project-1"},
				Snapshot: snapshot,
			}, nil
		},
		PickWorkspace: func(context.Context, WorkspacePageLoader, string, string) (WorkspacePickerResult, error) {
			workspaceCalls++
			if workspaceCalls == 1 {
				return WorkspacePickerBack{}, nil
			}
			return WorkspacePickerSelected{Workspace: &projectpb.ProjectWorkspaceCatalogSummary{
				WorkspaceId: "workspace-1", RootPath: "/workspace-1",
			}}, nil
		},
	}, []clientui.ProjectSummary{{ProjectID: "project-1"}})
	if err != nil || bound != server {
		t.Fatalf("binding after workspace back = %v/%v", bound, err)
	}
	if restored != (ProjectPickerSnapshot{Cursor: 7, Offset: 3}) {
		t.Fatalf("restored project picker snapshot = %+v", restored)
	}
	if len(server.bindCalls) != 1 || server.bindCalls[0].WorkspaceID != "workspace-1" {
		t.Fatalf("bind calls = %+v", server.bindCalls)
	}
}

func TestFormatMutationErrorWrapsMissingWorkspace(t *testing.T) {
	err := FormatMutationError("/workspace", "project-1", serverapi.ErrWorkspaceNotRegistered)
	if !errors.Is(err, serverapi.ErrWorkspaceNotRegistered) {
		t.Fatalf("expected wrapped workspace registration error, got %v", err)
	}
	if err == nil || err == serverapi.ErrWorkspaceNotRegistered {
		t.Fatalf("expected contextual error, got %v", err)
	}
}
