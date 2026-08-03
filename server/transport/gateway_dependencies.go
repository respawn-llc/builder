package transport

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"core/server/metadata"
	"core/shared/apicontract"
	"core/shared/clientui"
	"core/shared/protocol"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

func (g *Gateway) resolveAttachedProjectWorkspace(ctx context.Context, request protocol.AttachProjectRequest) (string, string, error) {
	selector, selected := request.Workspace()
	if !selected {
		overview, err := g.deps.ProjectViewClient().GetProjectOverview(ctx, serverapi.ProjectGetOverviewRequest{ProjectID: request.ProjectID})
		if err != nil {
			return "", "", err
		}
		if len(overview.Overview.Workspaces) == 0 {
			return "", "", fmt.Errorf("project %q has no attached workspaces", request.ProjectID)
		}
		if len(overview.Overview.Workspaces) > 1 {
			return "", "", fmt.Errorf("project %q requires explicit workspace selection", request.ProjectID)
		}
		workspace := overview.Overview.Workspaces[0]
		return strings.TrimSpace(workspace.WorkspaceID), strings.TrimSpace(workspace.RootPath), nil
	}
	if workspaceID, selectedByID := selector.WorkspaceID(); selectedByID {
		binding, err := g.deps.MetadataStore().LookupWorkspaceBindingByID(ctx, workspaceID)
		if err != nil {
			return "", "", err
		}
		if strings.TrimSpace(binding.ProjectID) != request.ProjectID {
			return "", "", fmt.Errorf("workspace %q is not bound to project %q", binding.CanonicalRoot, request.ProjectID)
		}
		return binding.WorkspaceID, strings.TrimSpace(binding.CanonicalRoot), nil
	}
	workspaceRoot, _ := selector.WorkspaceRoot()
	resolved, err := g.deps.ProjectViewClient().ResolveProjectPath(ctx, serverapi.ProjectResolvePathRequest{Path: workspaceRoot})
	if err != nil {
		return "", "", err
	}
	if resolved.Binding == nil {
		return "", "", errors.Join(serverapi.ErrWorkspaceNotRegistered, fmt.Errorf("workspace %q is not registered", resolved.CanonicalRoot))
	}
	if strings.TrimSpace(resolved.Binding.ProjectID) != request.ProjectID {
		return "", "", fmt.Errorf("workspace %q is not bound to project %q", resolved.Binding.CanonicalRoot, request.ProjectID)
	}
	return strings.TrimSpace(resolved.Binding.WorkspaceID), strings.TrimSpace(resolved.Binding.CanonicalRoot), nil
}

func (g *Gateway) resolveSessionAttachmentTarget(ctx context.Context, state *connectionState, sessionID string) (clientui.SessionExecutionTarget, metadata.Binding, error) {
	trimmedSessionID := strings.TrimSpace(sessionID)
	if trimmedSessionID == "" {
		return clientui.SessionExecutionTarget{}, metadata.Binding{}, errors.New("session id is required")
	}
	metadataStore := g.deps.MetadataStore()
	if metadataStore == nil {
		return clientui.SessionExecutionTarget{}, metadata.Binding{}, errors.New("metadata store is required")
	}
	target, err := metadataStore.ResolveSessionExecutionTarget(ctx, trimmedSessionID)
	if err != nil {
		return clientui.SessionExecutionTarget{}, metadata.Binding{}, err
	}
	binding, err := metadataStore.LookupWorkspaceBindingByID(ctx, target.WorkspaceID)
	if err != nil {
		return clientui.SessionExecutionTarget{}, metadata.Binding{}, err
	}
	activeProjectID := strings.TrimSpace(g.deps.ProjectID())
	if state != nil && strings.TrimSpace(state.attachedProject) != "" {
		activeProjectID = strings.TrimSpace(state.attachedProject)
	}
	if activeProjectID != "" && strings.TrimSpace(binding.ProjectID) != activeProjectID {
		return clientui.SessionExecutionTarget{}, metadata.Binding{}, sessionOutsideActiveProjectError{sessionID: trimmedSessionID}
	}
	return target, binding, nil
}

func (g *Gateway) resolveSessionAttachment(ctx context.Context, state *connectionState, sessionID string) (metadata.Binding, error) {
	_, binding, err := g.resolveSessionAttachmentTarget(ctx, state, sessionID)
	return binding, err
}

func (g *Gateway) promptCommandWorkspaceRootForState(ctx context.Context, state *connectionState) (string, error) {
	if state == nil {
		return "", errors.New("connection state is required")
	}
	if state.attachedSession == nil {
		return state.attachedWorkspaceRoot, nil
	}
	target, binding, err := g.resolveSessionAttachmentTarget(ctx, state, state.attachedSession.String())
	if err != nil {
		return "", err
	}
	return clientui.SessionExecutionWorkspaceRoot(target, binding.CanonicalRoot)
}

func (g *Gateway) promptCommandWorkspaceRootForCatalog(ctx context.Context, state *connectionState, sessionID *runtimeids.SessionID) (string, error) {
	if sessionID != nil {
		target, binding, err := g.resolveSessionAttachmentTarget(ctx, state, sessionID.String())
		if err != nil {
			return "", err
		}
		return clientui.SessionExecutionWorkspaceRoot(target, binding.CanonicalRoot)
	}
	return g.promptCommandWorkspaceRootForState(ctx, state)
}

func (g *Gateway) sessionLaunchClientForState(ctx context.Context, state *connectionState) (apicontract.SessionLaunchService, error) {
	projectID, err := g.activeProjectID(ctx, state)
	if err != nil {
		return nil, err
	}
	var launchClient apicontract.SessionLaunchService
	if strings.TrimSpace(state.attachedWorkspaceID) == "" {
		launchClient, err = g.deps.SessionLaunchClientForProjectWorkspace(ctx, projectID, state.attachedWorkspaceRoot)
	} else {
		launchClient, err = g.deps.SessionLaunchClientForProjectWorkspaceID(ctx, projectID, state.attachedWorkspaceID)
	}
	if err != nil {
		return nil, err
	}
	return launchClient, nil
}

func (g *Gateway) runPromptClientForState(ctx context.Context, state *connectionState) (apicontract.RunPromptService, error) {
	projectID, err := g.activeProjectID(ctx, state)
	if err != nil {
		return nil, err
	}
	var runClient apicontract.RunPromptService
	if strings.TrimSpace(state.attachedWorkspaceID) == "" {
		runClient, err = g.deps.RunPromptClientForProjectWorkspace(ctx, projectID, state.attachedWorkspaceRoot)
	} else {
		runClient, err = g.deps.RunPromptClientForProjectWorkspaceID(ctx, projectID, state.attachedWorkspaceID)
	}
	if err != nil {
		return nil, err
	}
	return runClient, nil
}
