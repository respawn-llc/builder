package projectbinding

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"core/cli/app/internal/remoteattach"
	"core/shared/apicontract"
	"core/shared/client"
	"core/shared/clientui"
	"core/shared/config"
	projectpb "core/shared/protoapi/gen/kent/api/project"
	"core/shared/serverapi"
)

// ErrStartupCanceledByUser is returned when interactive startup binding flows
// are canceled by the user. Callers and tests match this with errors.Is rather
// than comparing rendered message text.
var ErrStartupCanceledByUser = errors.New("startup canceled by user")

type ProjectPickerSnapshot struct {
	Cursor int
	Offset int
}

type ProjectPickerResult interface {
	isProjectPickerResult()
}

type ProjectPickerCreateNew struct{}

type ProjectPickerBack struct {
	Snapshot ProjectPickerSnapshot
}

type ProjectPickerExit struct{}

type ProjectPickerSelected struct {
	Project  clientui.ProjectSummary
	Snapshot ProjectPickerSnapshot
}

func (ProjectPickerCreateNew) isProjectPickerResult() {}
func (ProjectPickerBack) isProjectPickerResult()      {}
func (ProjectPickerExit) isProjectPickerResult()      {}
func (ProjectPickerSelected) isProjectPickerResult()  {}

type WorkspacePickerResult interface {
	isWorkspacePickerResult()
}

type WorkspacePickerSelected struct {
	Workspace projectpb.ProjectWorkspaceCatalogSummary
}

type WorkspacePickerBack struct{}
type WorkspacePickerExit struct{}

func (WorkspacePickerSelected) isWorkspacePickerResult() {}
func (WorkspacePickerBack) isWorkspacePickerResult()     {}
func (WorkspacePickerExit) isWorkspacePickerResult()     {}

type WorkspacePageLoader interface {
	ListProjectWorkspaces(context.Context, *projectpb.ProjectWorkspaceListRequest) (*projectpb.ListProjectWorkspacesSuccess, error)
}

type Server[T any] interface {
	Config() config.App
	PresentationTheme() string
	ProjectViewClient() apicontract.ProjectViewService
	BindProjectWorkspace(ctx context.Context, projectID string, workspaceID string) (T, error)
}

type Request[T any] struct {
	Server            Server[T]
	PickLocalProject  func(context.Context, []clientui.ProjectSummary, string, ProjectPickerSnapshot) (ProjectPickerResult, error)
	PickServerProject func(context.Context, []clientui.ProjectSummary, string, ProjectPickerSnapshot) (ProjectPickerResult, error)
	PickWorkspace     func(context.Context, WorkspacePageLoader, string, string) (WorkspacePickerResult, error)
	PromptProjectName func(defaultName string, theme string) (string, error)
}

func EnsureInteractive[T any](ctx context.Context, req Request[T]) (T, error) {
	var zero T
	if req.Server == nil || req.Server.ProjectViewClient() == nil {
		return zero, errors.New("project view client is required")
	}
	workspaceRoot := strings.TrimSpace(req.Server.Config().WorkspaceRoot)
	if workspaceRoot == "" {
		return zero, errors.New("workspace root is required")
	}
	plan, err := req.Server.ProjectViewClient().PlanWorkspaceBinding(ctx, &projectpb.PlanWorkspaceBindingRequest{
		Path: workspaceRoot,
		Mode: projectpb.WorkspaceBindingPlanMode_WORKSPACE_BINDING_PLAN_MODE_INTERACTIVE,
	})
	if err != nil {
		return zero, err
	}
	if canonicalRoot := strings.TrimSpace(plan.CanonicalRoot); canonicalRoot != "" {
		workspaceRoot = canonicalRoot
	}
	switch plan.Kind {
	case projectpb.WorkspaceBindingPlanKind_WORKSPACE_BINDING_PLAN_KIND_BOUND:
		if plan.Binding == nil {
			return zero, errors.New("resolved project binding is required")
		}
		projectID := strings.TrimSpace(plan.Binding.ProjectId)
		if projectID == "" {
			return zero, errors.New("resolved project id is required")
		}
		bound, bindErr := req.Server.BindProjectWorkspace(ctx, projectID, strings.TrimSpace(plan.Binding.WorkspaceId))
		if bindErr != nil {
			return zero, FormatStartupError(workspaceRoot, projectID, bindErr)
		}
		return bound, nil
	case projectpb.WorkspaceBindingPlanKind_WORKSPACE_BINDING_PLAN_KIND_SERVER_WORKSPACE_SELECTION:
		projects, err := client.ProjectSummariesFromProto(plan.Projects)
		if err != nil {
			return zero, err
		}
		return ensureServerBrowsingBinding(ctx, req, projects)
	case projectpb.WorkspaceBindingPlanKind_WORKSPACE_BINDING_PLAN_KIND_LOCAL_UNBOUND:
		projects, err := client.ProjectSummariesFromProto(plan.Projects)
		if err != nil {
			return zero, err
		}
		return ensureLocalPathBinding(ctx, req, workspaceRoot, projects)
	default:
		return zero, fmt.Errorf("unsupported interactive project binding plan %q", plan.Kind)
	}
}

func ensureLocalPathBinding[T any](ctx context.Context, req Request[T], workspaceRoot string, projects []clientui.ProjectSummary) (T, error) {
	var zero T
	if req.PickLocalProject == nil {
		return zero, errors.New("project picker is required")
	}
	theme := req.Server.PresentationTheme()
	picked, err := req.PickLocalProject(ctx, projects, theme, ProjectPickerSnapshot{})
	if err != nil {
		return zero, err
	}
	switch selected := picked.(type) {
	case ProjectPickerExit:
		return zero, ErrStartupCanceledByUser
	case ProjectPickerBack:
		return zero, errors.New("local project picker cannot go back")
	case ProjectPickerCreateNew:
		if req.PromptProjectName == nil {
			return zero, errors.New("project name prompt is required")
		}
		projectName, err := req.PromptProjectName(filepath.Base(filepath.Clean(workspaceRoot)), theme)
		if err != nil {
			return zero, err
		}
		created, err := req.Server.ProjectViewClient().CreateProject(ctx, &projectpb.CreateProjectRequest{DisplayName: projectName, WorkspaceRoot: workspaceRoot})
		if err != nil {
			return zero, FormatMutationError(workspaceRoot, "", err)
		}
		bound, bindErr := req.Server.BindProjectWorkspace(ctx, created.Binding.ProjectId, created.Binding.WorkspaceId)
		if bindErr != nil {
			return zero, FormatStartupError(workspaceRoot, created.Binding.ProjectId, bindErr)
		}
		return bound, nil
	case ProjectPickerSelected:
		attached, err := req.Server.ProjectViewClient().AttachWorkspaceToProject(ctx, &projectpb.AttachWorkspaceRequest{ProjectId: selected.Project.ProjectID, WorkspaceRoot: workspaceRoot})
		if err != nil {
			return zero, FormatMutationError(workspaceRoot, selected.Project.ProjectID, err)
		}
		bound, bindErr := req.Server.BindProjectWorkspace(ctx, attached.Binding.ProjectId, attached.Binding.WorkspaceId)
		if bindErr != nil {
			return zero, FormatStartupError(workspaceRoot, attached.Binding.ProjectId, bindErr)
		}
		return bound, nil
	default:
		return zero, errors.New("no project selected")
	}
}

func ensureServerBrowsingBinding[T any](ctx context.Context, req Request[T], projects []clientui.ProjectSummary) (T, error) {
	var zero T
	if len(projects) == 0 {
		return zero, errors.New("server has no registered projects. Create one with `kent project create --path <server-path> --name <project-name>` or attach an existing workspace with `kent attach --project <project-id> <server-path>`")
	}
	if req.PickServerProject == nil {
		return zero, errors.New("server project picker is required")
	}
	snapshot := ProjectPickerSnapshot{}
	for {
		picked, err := req.PickServerProject(ctx, projects, req.Server.PresentationTheme(), snapshot)
		if err != nil {
			return zero, err
		}
		switch selected := picked.(type) {
		case ProjectPickerExit, ProjectPickerBack:
			return zero, ErrStartupCanceledByUser
		case ProjectPickerCreateNew:
			return zero, errors.New("server project picker cannot create a project")
		case ProjectPickerSelected:
			workspace, err := SelectWorkspaceForStartup(ctx, WorkspaceSelectionRequest{
				Server:        req.Server,
				ProjectID:     selected.Project.ProjectID,
				PickWorkspace: req.PickWorkspace,
			})
			if err != nil {
				return zero, err
			}
			switch workspace := workspace.(type) {
			case WorkspacePickerBack:
				snapshot = selected.Snapshot
				continue
			case WorkspacePickerExit:
				return zero, ErrStartupCanceledByUser
			case WorkspacePickerSelected:
				bound, bindErr := req.Server.BindProjectWorkspace(ctx, selected.Project.ProjectID, workspace.Workspace.WorkspaceId)
				if bindErr != nil {
					return zero, FormatStartupError(workspace.Workspace.RootPath, selected.Project.ProjectID, bindErr)
				}
				return bound, nil
			default:
				return zero, errors.New("workspace picker exited without a result")
			}
		default:
			return zero, errors.New("no project selected")
		}
	}
}

type WorkspaceSelectionRequest struct {
	Server interface {
		Config() config.App
		PresentationTheme() string
		ProjectViewClient() apicontract.ProjectViewService
	}
	ProjectID     string
	PickWorkspace func(context.Context, WorkspacePageLoader, string, string) (WorkspacePickerResult, error)
}

func SelectWorkspaceForStartup(ctx context.Context, req WorkspaceSelectionRequest) (WorkspacePickerResult, error) {
	if req.Server == nil || req.Server.ProjectViewClient() == nil {
		return nil, errors.New("project view client is required")
	}
	if req.PickWorkspace == nil {
		return nil, errors.New("workspace picker is required")
	}
	projectID := strings.TrimSpace(req.ProjectID)
	if projectID == "" {
		return nil, errors.New("project id is required")
	}
	return req.PickWorkspace(ctx, req.Server.ProjectViewClient(), projectID, req.Server.PresentationTheme())
}

func FormatStartupError(workspaceRoot string, projectID string, err error) error {
	trimmedWorkspaceRoot := strings.TrimSpace(workspaceRoot)
	trimmedProjectID := strings.TrimSpace(projectID)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, serverapi.ErrProjectNotFound):
		return fmt.Errorf("workspace %q is attached to missing project %q. Repair the binding before continuing: %w", trimmedWorkspaceRoot, trimmedProjectID, err)
	case errors.Is(err, serverapi.ErrProjectUnavailable):
		if unavailable, ok := serverapi.AsProjectUnavailable(err); ok {
			switch unavailable.Availability {
			case clientui.ProjectAvailabilityMissing:
				return fmt.Errorf("project %q root %q is missing. Rebind affected sessions from their new workspace roots: %w", unavailable.ProjectID, unavailable.RootPath, err)
			case clientui.ProjectAvailabilityInaccessible:
				return fmt.Errorf("project %q root %q is inaccessible. Restore access or rebind affected sessions from another workspace root: %w", unavailable.ProjectID, unavailable.RootPath, err)
			}
		}
	}
	return err
}

func FormatMutationError(workspaceRoot string, projectID string, err error) error {
	trimmedWorkspaceRoot := strings.TrimSpace(workspaceRoot)
	trimmedProjectID := strings.TrimSpace(projectID)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, serverapi.ErrWorkspaceNotRegistered):
		return remoteattach.HeadlessWorkspaceRegistrationError(trimmedWorkspaceRoot)
	case errors.Is(err, serverapi.ErrProjectNotFound):
		return fmt.Errorf("project %q is no longer available. Restart Kent and choose another project: %w", trimmedProjectID, err)
	}
	return err
}
