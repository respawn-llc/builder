package embeddedattach

import (
	"context"
	"errors"
	"strings"

	"core/shared/apicontract"
	"core/shared/config"
)

type BindingServer interface {
	Config() config.App
	SessionLaunchClientForProjectWorkspace(context.Context, string, string) (apicontract.SessionLaunchService, error)
	SessionLaunchClientForProjectWorkspaceID(context.Context, string, string) (apicontract.SessionLaunchService, error)
	RunPromptClientForProjectWorkspace(context.Context, string, string) (apicontract.RunPromptService, error)
	RunPromptClientForProjectWorkspaceID(context.Context, string, string) (apicontract.RunPromptService, error)
}

type WorkspaceBindingRequest struct {
	Server      BindingServer
	ProjectID   string
	WorkspaceID string
}

type WorkspaceBinding struct {
	ProjectID     string
	SessionLaunch apicontract.SessionLaunchService
	RunPrompt     apicontract.RunPromptService
}

func BindProjectWorkspace(ctx context.Context, req WorkspaceBindingRequest) (WorkspaceBinding, error) {
	if req.Server == nil {
		return WorkspaceBinding{}, errors.New("embedded server is required")
	}
	projectID := strings.TrimSpace(req.ProjectID)
	if projectID == "" {
		return WorkspaceBinding{}, errors.New("project id is required")
	}
	workspaceID := strings.TrimSpace(req.WorkspaceID)
	if workspaceID != "" {
		return bindWorkspaceID(ctx, req.Server, projectID, workspaceID)
	}
	return bindWorkspaceRoot(ctx, req.Server, projectID, req.Server.Config().WorkspaceRoot)
}

func bindWorkspaceRoot(ctx context.Context, server BindingServer, projectID string, workspaceRoot string) (WorkspaceBinding, error) {
	launchClient, err := server.SessionLaunchClientForProjectWorkspace(ctx, projectID, workspaceRoot)
	if err != nil {
		return WorkspaceBinding{}, err
	}
	runPromptClient, err := server.RunPromptClientForProjectWorkspace(ctx, projectID, workspaceRoot)
	if err != nil {
		return WorkspaceBinding{}, err
	}
	return WorkspaceBinding{ProjectID: projectID, SessionLaunch: launchClient, RunPrompt: runPromptClient}, nil
}

func bindWorkspaceID(ctx context.Context, server BindingServer, projectID string, workspaceID string) (WorkspaceBinding, error) {
	launchClient, err := server.SessionLaunchClientForProjectWorkspaceID(ctx, projectID, workspaceID)
	if err != nil {
		return WorkspaceBinding{}, err
	}
	runPromptClient, err := server.RunPromptClientForProjectWorkspaceID(ctx, projectID, workspaceID)
	if err != nil {
		return WorkspaceBinding{}, err
	}
	return WorkspaceBinding{ProjectID: projectID, SessionLaunch: launchClient, RunPrompt: runPromptClient}, nil
}
