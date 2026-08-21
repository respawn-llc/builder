package projectview

import (
	"context"
	"errors"
	"strings"

	projectpb "core/shared/protoapi/gen/kent/api/project"
	"core/shared/serverapi"
)

func (s *Service) SetDefaultWorkspace(ctx context.Context, req *projectpb.SetDefaultWorkspaceRequest) (*projectpb.SetDefaultWorkspaceSuccess, error) {
	if req == nil {
		return nil, errors.New("set default workspace request is required")
	}
	if s == nil {
		return nil, errors.New("project service is required")
	}
	selector, err := projectWorkspaceSelectorFromGenerated(req.Workspace)
	if err != nil {
		return nil, err
	}
	binding, err := s.metadata.ResolveProjectWorkspaceSelector(ctx, req.ProjectId, selector)
	if err != nil {
		return nil, err
	}
	project, err := s.metadata.SetProjectDefaultWorkspaceAndGetSummary(ctx, req.ProjectId, binding.WorkspaceID)
	if err != nil {
		return nil, wrapWorkspaceMutationError(req.ProjectId, binding.WorkspaceID, err)
	}
	generated, err := projectHomeSummaryToGenerated(project)
	if err != nil {
		return nil, err
	}
	return &projectpb.SetDefaultWorkspaceSuccess{Project: generated}, nil
}

func (s *Service) UnlinkWorkspaceFromProject(ctx context.Context, req *projectpb.UnlinkWorkspaceRequest) (*projectpb.UnlinkWorkspaceSuccess, error) {
	if req == nil {
		return nil, errors.New("unlink workspace request is required")
	}
	if s == nil {
		return nil, errors.New("project service is required")
	}
	selector, err := projectWorkspaceSelectorFromGenerated(req.Workspace)
	if err != nil {
		return nil, err
	}
	binding, err := s.metadata.ResolveProjectWorkspaceSelector(ctx, req.ProjectId, selector)
	if err != nil {
		return nil, err
	}
	runtimeBlocker := func(ctx context.Context, sessionIDs []string) ([]serverapi.ProjectWorkspaceUnlinkBlocker, func(), error) {
		return withRuntimeBlockers(ctx, sessionIDs, s.workspaceActiveSessionBlockers, s.blockSessionStarts)
	}
	blockers, err := s.metadata.UnlinkProjectWorkspaceWithRuntimeBlockers(ctx, req.ProjectId, binding.WorkspaceID, nil, runtimeBlocker)
	if err != nil {
		return nil, wrapWorkspaceMutationError(req.ProjectId, binding.WorkspaceID, err)
	}
	resp := &projectpb.UnlinkWorkspaceSuccess{
		ProjectId:   strings.TrimSpace(req.ProjectId),
		WorkspaceId: binding.WorkspaceID,
		Blockers:    make([]*projectpb.WorkspaceUnlinkBlocker, 0, len(blockers)),
	}
	for _, blocker := range blockers {
		generated := &projectpb.WorkspaceUnlinkBlocker{Code: blocker.Code}
		if blocker.Count > 0 {
			count, conversionErr := nonNegativeInt32(blocker.Count, "workspace unlink blocker count")
			if conversionErr != nil {
				return nil, conversionErr
			}
			generated.Count = &count
		}
		resp.Blockers = append(resp.Blockers, generated)
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
