package app

import (
	"context"
	"errors"

	"core/shared/serverapi"
)

func (*configuredProjectViewRemoteStub) GetProjectEdit(context.Context, serverapi.ProjectEditGetRequest) (serverapi.ProjectEditGetResponse, error) {
	return serverapi.ProjectEditGetResponse{}, errors.New("unexpected GetProjectEdit call")
}

func (*configuredProjectViewRemoteStub) UpdateProject(context.Context, serverapi.ProjectUpdateRequest) (serverapi.ProjectUpdateResponse, error) {
	return serverapi.ProjectUpdateResponse{}, errors.New("unexpected UpdateProject call")
}

func (*configuredProjectViewRemoteStub) SetDefaultWorkspace(context.Context, serverapi.ProjectDefaultWorkspaceSetRequest) (serverapi.ProjectDefaultWorkspaceSetResponse, error) {
	return serverapi.ProjectDefaultWorkspaceSetResponse{}, errors.New("unexpected SetDefaultWorkspace call")
}

func (*configuredProjectViewRemoteStub) UnlinkWorkspaceFromProject(context.Context, serverapi.ProjectWorkspaceUnlinkRequest) (serverapi.ProjectWorkspaceUnlinkResponse, error) {
	return serverapi.ProjectWorkspaceUnlinkResponse{}, errors.New("unexpected UnlinkWorkspaceFromProject call")
}

func (*configuredProjectViewRemoteStub) DeleteProject(context.Context, serverapi.ProjectDeleteRequest) (serverapi.ProjectDeleteResponse, error) {
	return serverapi.ProjectDeleteResponse{}, errors.New("unexpected DeleteProject call")
}

func (headlessProjectViewStubService) GetProjectEdit(context.Context, serverapi.ProjectEditGetRequest) (serverapi.ProjectEditGetResponse, error) {
	return serverapi.ProjectEditGetResponse{}, errors.New("unexpected GetProjectEdit call")
}

func (headlessProjectViewStubService) UpdateProject(context.Context, serverapi.ProjectUpdateRequest) (serverapi.ProjectUpdateResponse, error) {
	return serverapi.ProjectUpdateResponse{}, errors.New("unexpected UpdateProject call")
}

func (headlessProjectViewStubService) SetDefaultWorkspace(context.Context, serverapi.ProjectDefaultWorkspaceSetRequest) (serverapi.ProjectDefaultWorkspaceSetResponse, error) {
	return serverapi.ProjectDefaultWorkspaceSetResponse{}, errors.New("unexpected SetDefaultWorkspace call")
}

func (headlessProjectViewStubService) UnlinkWorkspaceFromProject(context.Context, serverapi.ProjectWorkspaceUnlinkRequest) (serverapi.ProjectWorkspaceUnlinkResponse, error) {
	return serverapi.ProjectWorkspaceUnlinkResponse{}, errors.New("unexpected UnlinkWorkspaceFromProject call")
}

func (headlessProjectViewStubService) DeleteProject(context.Context, serverapi.ProjectDeleteRequest) (serverapi.ProjectDeleteResponse, error) {
	return serverapi.ProjectDeleteResponse{}, errors.New("unexpected DeleteProject call")
}

func (projectBindingFlowStubProjectViewService) GetProjectEdit(context.Context, serverapi.ProjectEditGetRequest) (serverapi.ProjectEditGetResponse, error) {
	return serverapi.ProjectEditGetResponse{}, errors.New("unexpected GetProjectEdit call")
}

func (projectBindingFlowStubProjectViewService) UpdateProject(context.Context, serverapi.ProjectUpdateRequest) (serverapi.ProjectUpdateResponse, error) {
	return serverapi.ProjectUpdateResponse{}, errors.New("unexpected UpdateProject call")
}

func (projectBindingFlowStubProjectViewService) SetDefaultWorkspace(context.Context, serverapi.ProjectDefaultWorkspaceSetRequest) (serverapi.ProjectDefaultWorkspaceSetResponse, error) {
	return serverapi.ProjectDefaultWorkspaceSetResponse{}, errors.New("unexpected SetDefaultWorkspace call")
}

func (projectBindingFlowStubProjectViewService) UnlinkWorkspaceFromProject(context.Context, serverapi.ProjectWorkspaceUnlinkRequest) (serverapi.ProjectWorkspaceUnlinkResponse, error) {
	return serverapi.ProjectWorkspaceUnlinkResponse{}, errors.New("unexpected UnlinkWorkspaceFromProject call")
}

func (projectBindingFlowStubProjectViewService) DeleteProject(context.Context, serverapi.ProjectDeleteRequest) (serverapi.ProjectDeleteResponse, error) {
	return serverapi.ProjectDeleteResponse{}, errors.New("unexpected DeleteProject call")
}
