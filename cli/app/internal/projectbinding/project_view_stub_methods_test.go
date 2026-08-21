package projectbinding

import (
	"context"
	"errors"

	projectpb "core/shared/protoapi/gen/kent/api/project"
)

func (*testProjectViewClient) GetProjectEdit(context.Context, *projectpb.ProjectEditGetRequest) (*projectpb.GetProjectEditSuccess, error) {
	return nil, errors.New("unexpected GetProjectEdit call")
}

func (*testProjectViewClient) GetProjectWorkspace(context.Context, *projectpb.GetProjectWorkspaceRequest) (*projectpb.GetProjectWorkspaceSuccess, error) {
	return nil, errors.New("unexpected GetProjectWorkspace call")
}

func (*testProjectViewClient) UpdateProject(context.Context, *projectpb.UpdateProjectRequest) (*projectpb.UpdateProjectSuccess, error) {
	return nil, errors.New("unexpected UpdateProject call")
}

func (*testProjectViewClient) SetDefaultWorkspace(context.Context, *projectpb.SetDefaultWorkspaceRequest) (*projectpb.SetDefaultWorkspaceSuccess, error) {
	return nil, errors.New("unexpected SetDefaultWorkspace call")
}

func (*testProjectViewClient) UnlinkWorkspaceFromProject(context.Context, *projectpb.UnlinkWorkspaceRequest) (*projectpb.UnlinkWorkspaceSuccess, error) {
	return nil, errors.New("unexpected UnlinkWorkspaceFromProject call")
}

func (*testProjectViewClient) DeleteProject(context.Context, *projectpb.DeleteProjectRequest) (*projectpb.DeleteProjectSuccess, error) {
	return nil, errors.New("unexpected DeleteProject call")
}
