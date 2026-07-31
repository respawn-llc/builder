package projectview

import (
	"context"
	"errors"
	"strings"

	"core/shared/serverapi"
)

func (s *Service) SetDefaultWorkspace(ctx context.Context, req serverapi.ProjectDefaultWorkspaceSetRequest) (serverapi.ProjectDefaultWorkspaceSetResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.ProjectDefaultWorkspaceSetResponse{}, err
	}
	if s == nil {
		return serverapi.ProjectDefaultWorkspaceSetResponse{}, errors.New("project service is required")
	}
	if err := s.requireProjectID(req.ProjectID); err != nil {
		return serverapi.ProjectDefaultWorkspaceSetResponse{}, err
	}
	lease, err := s.acquireProjectMutationLease(ctx, req.ProjectID)
	if err != nil {
		return serverapi.ProjectDefaultWorkspaceSetResponse{}, err
	}
	defer lease.Release()
	binding, err := s.metadata.ResolveProjectWorkspaceSelector(ctx, req.ProjectID, req.ProjectWorkspaceSelector)
	if err != nil {
		return serverapi.ProjectDefaultWorkspaceSetResponse{}, err
	}
	if err := s.metadata.SetProjectDefaultWorkspace(ctx, req.ProjectID, binding.WorkspaceID); err != nil {
		return serverapi.ProjectDefaultWorkspaceSetResponse{}, wrapWorkspaceMutationError(req.ProjectID, binding.WorkspaceID, err)
	}
	project, err := s.projectHomeSummary(ctx, req.ProjectID)
	if err != nil {
		return serverapi.ProjectDefaultWorkspaceSetResponse{}, wrapWorkspaceMutationError(req.ProjectID, binding.WorkspaceID, err)
	}
	return serverapi.ProjectDefaultWorkspaceSetResponse{Project: project}, nil
}

func (s *Service) UnlinkWorkspaceFromProject(ctx context.Context, req serverapi.ProjectWorkspaceUnlinkRequest) (serverapi.ProjectWorkspaceUnlinkResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.ProjectWorkspaceUnlinkResponse{}, err
	}
	if s == nil {
		return serverapi.ProjectWorkspaceUnlinkResponse{}, errors.New("project service is required")
	}
	if err := s.requireProjectID(req.ProjectID); err != nil {
		return serverapi.ProjectWorkspaceUnlinkResponse{}, err
	}
	lease, err := s.acquireProjectMutationLease(ctx, req.ProjectID)
	if err != nil {
		return serverapi.ProjectWorkspaceUnlinkResponse{}, err
	}
	defer lease.Release()
	binding, err := s.metadata.ResolveProjectWorkspaceSelector(ctx, req.ProjectID, req.ProjectWorkspaceSelector)
	if err != nil {
		return serverapi.ProjectWorkspaceUnlinkResponse{}, err
	}
	runtimeBlocker := func(ctx context.Context, sessionIDs []string) ([]serverapi.ProjectWorkspaceUnlinkBlocker, func(), error) {
		blockers, err := s.workspaceActiveSessionBlockers(ctx, sessionIDs)
		if err != nil || len(blockers) > 0 {
			return blockers, nil, err
		}
		release, err := s.blockSessionStarts(ctx, sessionIDs)
		if err != nil {
			return nil, nil, err
		}
		blockers, err = s.workspaceActiveSessionBlockers(ctx, sessionIDs)
		if err != nil {
			release()
			return nil, nil, err
		}
		return blockers, release, nil
	}
	blockers, err := s.metadata.UnlinkProjectWorkspaceWithRuntimeBlockers(ctx, req.ProjectID, binding.WorkspaceID, nil, runtimeBlocker)
	if err != nil {
		return serverapi.ProjectWorkspaceUnlinkResponse{}, wrapWorkspaceMutationError(req.ProjectID, binding.WorkspaceID, err)
	}
	resp := serverapi.ProjectWorkspaceUnlinkResponse{
		ProjectID:   strings.TrimSpace(req.ProjectID),
		WorkspaceID: binding.WorkspaceID,
		Blockers:    blockers,
		Unlinked:    len(blockers) == 0,
	}
	if !resp.Unlinked {
		return resp, nil
	}
	projects, err := s.metadata.ListProjectHomeSummaries(ctx, req.ProjectID, 1, 0)
	if err != nil {
		return serverapi.ProjectWorkspaceUnlinkResponse{}, wrapWorkspaceMutationError(req.ProjectID, binding.WorkspaceID, err)
	}
	if len(projects) > 0 {
		resp.Project = &projects[0]
	}
	return resp, nil
}

func wrapWorkspaceMutationError(projectID string, workspaceID string, err error) error {
	if err == nil {
		return nil
	}
	var conflict *serverapi.WorkspaceDetachConflictError
	if errors.As(err, &conflict) {
		return err
	}
	var mutation *serverapi.WorkspaceMutationError
	if errors.As(err, &mutation) {
		return err
	}
	return &serverapi.WorkspaceMutationError{
		ProjectID:   strings.TrimSpace(projectID),
		WorkspaceID: strings.TrimSpace(workspaceID),
		Cause:       err,
	}
}
