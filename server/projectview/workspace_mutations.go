package projectview

import (
	"context"
	"errors"
	"strings"

	servicecontract "core/shared/apicontract"
	"core/shared/serverapi"
)

func (s *Service) SetDefaultWorkspace(ctx context.Context, req serverapi.ProjectDefaultWorkspaceSetRequest) (serverapi.ProjectDefaultWorkspaceSetResponse, error) {
	if err := servicecontract.ClassifyRequestValidation(req.Validate()); err != nil {
		return serverapi.ProjectDefaultWorkspaceSetResponse{}, err
	}
	if s == nil {
		return serverapi.ProjectDefaultWorkspaceSetResponse{}, errors.New("project service is required")
	}
	binding, err := s.metadata.ResolveProjectWorkspaceSelector(ctx, req.ProjectID, req.ProjectWorkspaceSelector)
	if err != nil {
		return serverapi.ProjectDefaultWorkspaceSetResponse{}, err
	}
	project, err := s.metadata.SetProjectDefaultWorkspaceAndGetSummary(ctx, req.ProjectID, binding.WorkspaceID)
	if err != nil {
		return serverapi.ProjectDefaultWorkspaceSetResponse{}, wrapWorkspaceMutationError(req.ProjectID, binding.WorkspaceID, err)
	}
	return serverapi.ProjectDefaultWorkspaceSetResponse{Project: project}, nil
}

func (s *Service) UnlinkWorkspaceFromProject(ctx context.Context, req serverapi.ProjectWorkspaceUnlinkRequest) (serverapi.ProjectWorkspaceUnlinkResponse, error) {
	if err := servicecontract.ClassifyRequestValidation(req.Validate()); err != nil {
		return serverapi.ProjectWorkspaceUnlinkResponse{}, err
	}
	if s == nil {
		return serverapi.ProjectWorkspaceUnlinkResponse{}, errors.New("project service is required")
	}
	binding, err := s.metadata.ResolveProjectWorkspaceSelector(ctx, req.ProjectID, req.ProjectWorkspaceSelector)
	if err != nil {
		return serverapi.ProjectWorkspaceUnlinkResponse{}, err
	}
	runtimeBlocker := func(ctx context.Context, sessionIDs []string) ([]serverapi.ProjectWorkspaceUnlinkBlocker, func(), error) {
		return withRuntimeBlockers(ctx, sessionIDs, s.workspaceActiveSessionBlockers, s.blockSessionStarts)
	}
	blockers, err := s.metadata.UnlinkProjectWorkspaceWithRuntimeBlockers(ctx, req.ProjectID, binding.WorkspaceID, nil, runtimeBlocker)
	if err != nil {
		return serverapi.ProjectWorkspaceUnlinkResponse{}, wrapWorkspaceMutationError(req.ProjectID, binding.WorkspaceID, err)
	}
	resp := serverapi.ProjectWorkspaceUnlinkResponse{
		ProjectID:   strings.TrimSpace(req.ProjectID),
		WorkspaceID: binding.WorkspaceID,
		Blockers:    blockers,
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
