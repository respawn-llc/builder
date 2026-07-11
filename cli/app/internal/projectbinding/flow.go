package projectbinding

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"core/cli/app/internal/remoteattach"
	"core/shared/client"
	"core/shared/clientui"
	"core/shared/config"
	"core/shared/serverapi"
)

// ErrStartupCanceledByUser is returned when interactive startup binding flows
// are canceled by the user. Callers and tests match this with errors.Is rather
// than comparing rendered message text.
var ErrStartupCanceledByUser = errors.New("startup canceled by user")

type ProjectPickerResult struct {
	CreateNew bool
	Project   *clientui.ProjectSummary
	Canceled  bool
}

type WorkspacePickerResult struct {
	Workspace *clientui.ProjectWorkspaceSummary
	Canceled  bool
}

type Server[T any] interface {
	Config() config.App
	PresentationTheme() string
	ProjectViewClient() client.ProjectViewClient
	BindProjectWorkspace(ctx context.Context, projectID string, workspaceID string) (T, error)
}

type Request[T any] struct {
	Server            Server[T]
	PickLocalProject  func([]clientui.ProjectSummary, string) (ProjectPickerResult, error)
	PickServerProject func([]clientui.ProjectSummary, string) (ProjectPickerResult, error)
	PickWorkspace     func([]clientui.ProjectWorkspaceSummary, string) (WorkspacePickerResult, error)
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
	plan, err := req.Server.ProjectViewClient().PlanWorkspaceBinding(ctx, serverapi.ProjectBindingPlanRequest{Path: workspaceRoot, Mode: serverapi.ProjectBindingPlanModeInteractive})
	if err != nil {
		return zero, err
	}
	if canonicalRoot := strings.TrimSpace(plan.CanonicalRoot); canonicalRoot != "" {
		workspaceRoot = canonicalRoot
	}
	switch plan.Kind {
	case serverapi.ProjectBindingPlanKindBound:
		if plan.Binding == nil {
			return zero, errors.New("resolved project binding is required")
		}
		projectID := strings.TrimSpace(plan.Binding.ProjectID)
		if projectID == "" {
			return zero, errors.New("resolved project id is required")
		}
		bound, bindErr := req.Server.BindProjectWorkspace(ctx, projectID, strings.TrimSpace(plan.Binding.WorkspaceID))
		if bindErr != nil {
			return zero, FormatStartupError(workspaceRoot, projectID, bindErr)
		}
		return bound, nil
	case serverapi.ProjectBindingPlanKindServerWorkspaceSelection:
		return ensureServerBrowsingBinding(ctx, req, plan.Projects)
	case serverapi.ProjectBindingPlanKindLocalUnbound:
		return ensureLocalPathBinding(ctx, req, workspaceRoot, plan.Projects)
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
	picked, err := req.PickLocalProject(projects, theme)
	if err != nil {
		return zero, err
	}
	if picked.Canceled {
		return zero, ErrStartupCanceledByUser
	}
	if picked.CreateNew {
		if req.PromptProjectName == nil {
			return zero, errors.New("project name prompt is required")
		}
		projectName, err := req.PromptProjectName(filepath.Base(filepath.Clean(workspaceRoot)), theme)
		if err != nil {
			return zero, err
		}
		created, err := req.Server.ProjectViewClient().CreateProject(ctx, serverapi.ProjectCreateRequest{DisplayName: projectName, WorkspaceRoot: workspaceRoot})
		if err != nil {
			return zero, FormatMutationError(workspaceRoot, "", err)
		}
		bound, bindErr := req.Server.BindProjectWorkspace(ctx, created.Binding.ProjectID, created.Binding.WorkspaceID)
		if bindErr != nil {
			return zero, FormatStartupError(workspaceRoot, created.Binding.ProjectID, bindErr)
		}
		return bound, nil
	}
	if picked.Project == nil {
		return zero, errors.New("no project selected")
	}
	attached, err := req.Server.ProjectViewClient().AttachWorkspaceToProject(ctx, serverapi.ProjectAttachWorkspaceRequest{ProjectID: picked.Project.ProjectID, WorkspaceRoot: workspaceRoot})
	if err != nil {
		return zero, FormatMutationError(workspaceRoot, picked.Project.ProjectID, err)
	}
	bound, bindErr := req.Server.BindProjectWorkspace(ctx, attached.Binding.ProjectID, attached.Binding.WorkspaceID)
	if bindErr != nil {
		return zero, FormatStartupError(workspaceRoot, attached.Binding.ProjectID, bindErr)
	}
	return bound, nil
}

func ensureServerBrowsingBinding[T any](ctx context.Context, req Request[T], projects []clientui.ProjectSummary) (T, error) {
	var zero T
	if len(projects) == 0 {
		return zero, errors.New("server has no registered projects. Create one with `kent project create --path <server-path> --name <project-name>` or attach an existing workspace with `kent attach --project <project-id> <server-path>`")
	}
	if req.PickServerProject == nil {
		return zero, errors.New("server project picker is required")
	}
	picked, err := req.PickServerProject(projects, req.Server.PresentationTheme())
	if err != nil {
		return zero, err
	}
	if picked.Canceled {
		return zero, ErrStartupCanceledByUser
	}
	if picked.Project == nil {
		return zero, errors.New("no project selected")
	}
	workspace, err := SelectWorkspaceForStartup(ctx, WorkspaceSelectionRequest{
		Server:        req.Server,
		ProjectID:     picked.Project.ProjectID,
		PickWorkspace: req.PickWorkspace,
	})
	if err != nil {
		return zero, err
	}
	bound, bindErr := req.Server.BindProjectWorkspace(ctx, picked.Project.ProjectID, workspace.WorkspaceID)
	if bindErr != nil {
		return zero, FormatStartupError(workspace.RootPath, picked.Project.ProjectID, bindErr)
	}
	return bound, nil
}

func EnsureServerBrowsing[T any](ctx context.Context, req Request[T], projects []clientui.ProjectSummary) (T, error) {
	return ensureServerBrowsingBinding(ctx, req, projects)
}

type WorkspaceSelectionRequest struct {
	Server interface {
		Config() config.App
		PresentationTheme() string
		ProjectViewClient() client.ProjectViewClient
	}
	ProjectID     string
	PickWorkspace func([]clientui.ProjectWorkspaceSummary, string) (WorkspacePickerResult, error)
}

func SelectWorkspaceForStartup(ctx context.Context, req WorkspaceSelectionRequest) (clientui.ProjectWorkspaceSummary, error) {
	if req.Server == nil || req.Server.ProjectViewClient() == nil {
		return clientui.ProjectWorkspaceSummary{}, errors.New("project view client is required")
	}
	overview, err := req.Server.ProjectViewClient().GetProjectOverview(ctx, serverapi.ProjectGetOverviewRequest{ProjectID: req.ProjectID})
	if err != nil {
		return clientui.ProjectWorkspaceSummary{}, err
	}
	if len(overview.Overview.Workspaces) == 0 {
		return clientui.ProjectWorkspaceSummary{}, fmt.Errorf("project %q has no attached workspaces", strings.TrimSpace(req.ProjectID))
	}
	if len(overview.Overview.Workspaces) == 1 {
		return overview.Overview.Workspaces[0], nil
	}
	if req.PickWorkspace == nil {
		return clientui.ProjectWorkspaceSummary{}, errors.New("workspace picker is required")
	}
	picked, err := req.PickWorkspace(overview.Overview.Workspaces, req.Server.PresentationTheme())
	if err != nil {
		return clientui.ProjectWorkspaceSummary{}, err
	}
	if picked.Canceled {
		return clientui.ProjectWorkspaceSummary{}, ErrStartupCanceledByUser
	}
	if picked.Workspace == nil {
		return clientui.ProjectWorkspaceSummary{}, errors.New("no workspace selected")
	}
	return *picked.Workspace, nil
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
