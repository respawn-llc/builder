package serverapi

import (
	"testing"
)

func TestProjectWorkspaceCatalogHardCutoverContract(t *testing.T) {
	if err := (ProjectWorkspaceListRequest{ProjectID: "project-1", Offset: 500, Limit: MaxProjectWorkspacePageSize}).Validate(); err != nil {
		t.Fatalf("valid request: %v", err)
	}
	for _, request := range []ProjectWorkspaceListRequest{
		{ProjectID: "", Limit: 1},
		{ProjectID: "project-1", Offset: -1, Limit: 1}, {ProjectID: "project-1"},
		{ProjectID: "project-1", Limit: MaxProjectWorkspacePageSize + 1},
	} {
		if err := request.Validate(); err == nil {
			t.Fatalf("invalid request accepted: %+v", request)
		}
	}
	next := 600
	valid := ProjectWorkspaceListResponse{
		ProjectID: "project-1", Offset: 500, NextOffset: &next,
		Workspaces: []ProjectWorkspaceCatalogRow{{WorkspaceID: "workspace-1", DisplayName: "Workspace", RootPath: "/workspace", IsDefault: true}},
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid response: %v", err)
	}
	zero := 0
	for _, response := range []ProjectWorkspaceListResponse{
		{ProjectID: "project-1"},
		{ProjectID: "project-1", Workspaces: []ProjectWorkspaceCatalogRow{{}}},
		{ProjectID: "project-1", Workspaces: valid.Workspaces, NextOffset: &zero},
	} {
		if err := response.Validate(); err == nil {
			t.Fatalf("invalid response accepted: %+v", response)
		}
	}
}

func TestProjectWorkspaceAttachResponseRequiresTypedOutcomeAndBinding(t *testing.T) {
	valid := ProjectAttachWorkspaceResponse{
		Binding: ProjectMutationBinding{ProjectID: "project-1", WorkspaceID: "workspace-1"},
		Outcome: ProjectWorkspaceAttachOutcomeAlreadyAttached,
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid attach response: %v", err)
	}
	for _, response := range []ProjectAttachWorkspaceResponse{
		{Binding: valid.Binding},
		{Binding: ProjectMutationBinding{WorkspaceID: "workspace-1"}, Outcome: ProjectWorkspaceAttachOutcomeAttached},
		{Binding: ProjectMutationBinding{ProjectID: "project-1"}, Outcome: ProjectWorkspaceAttachOutcomeAttached},
	} {
		if err := response.Validate(); err == nil {
			t.Fatalf("invalid attach response accepted: %+v", response)
		}
	}
}
