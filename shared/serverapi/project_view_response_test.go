package serverapi

import "testing"

func validProjectHomeSummaryForResponseTest() ProjectHomeSummary {
	return ProjectHomeSummary{
		ProjectID:   "project-1",
		ProjectKey:  "project",
		DisplayName: "Project",
		PrimaryWorkspace: ProjectWorkspaceSummary{
			WorkspaceID:  "workspace-1",
			DisplayName:  "Workspace",
			RootPath:     "/workspace",
			Availability: "available",
		},
	}
}

func TestProjectHomeSummaryValidateRequiresAuthoritativeIdentity(t *testing.T) {
	valid := validProjectHomeSummaryForResponseTest()
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid summary rejected: %v", err)
	}
	for name, mutate := range map[string]func(*ProjectHomeSummary){
		"project ID":             func(summary *ProjectHomeSummary) { summary.ProjectID = "" },
		"project key":            func(summary *ProjectHomeSummary) { summary.ProjectKey = " " },
		"project name":           func(summary *ProjectHomeSummary) { summary.DisplayName = "" },
		"workspace ID":           func(summary *ProjectHomeSummary) { summary.PrimaryWorkspace.WorkspaceID = "" },
		"workspace name":         func(summary *ProjectHomeSummary) { summary.PrimaryWorkspace.DisplayName = "" },
		"workspace root":         func(summary *ProjectHomeSummary) { summary.PrimaryWorkspace.RootPath = "" },
		"workspace availability": func(summary *ProjectHomeSummary) { summary.PrimaryWorkspace.Availability = "" },
	} {
		t.Run(name, func(t *testing.T) {
			summary := valid
			mutate(&summary)
			if err := summary.Validate(); err == nil {
				t.Fatal("malformed summary accepted")
			}
		})
	}
}

func TestProjectWorkspaceMutationResponseValidateRejectsMalformedResponses(t *testing.T) {
	summary := validProjectHomeSummaryForResponseTest()
	if err := (ProjectDefaultWorkspaceSetResponse{Project: summary}).Validate(); err != nil {
		t.Fatalf("valid default response rejected: %v", err)
	}
	if err := (ProjectWorkspaceUnlinkResponse{
		ProjectID:   "project-1",
		WorkspaceID: "workspace-1",
		Unlinked:    true,
		Project:     &summary,
	}).Validate(); err != nil {
		t.Fatalf("valid unlink response rejected: %v", err)
	}
	for name, response := range map[string]ProjectWorkspaceUnlinkResponse{
		"missing project ID":   {WorkspaceID: "workspace-1", Unlinked: true},
		"missing workspace ID": {ProjectID: "project-1", Unlinked: true},
		"blocked response marked unlinked": {
			ProjectID: "project-1", WorkspaceID: "workspace-1", Unlinked: true,
			Blockers: []ProjectWorkspaceUnlinkBlocker{{Code: "blocked", Message: "blocked"}},
		},
		"successful response marked blocked": {
			ProjectID: "project-1", WorkspaceID: "workspace-1", Unlinked: false,
		},
		"blank blocker code": {
			ProjectID: "project-1", WorkspaceID: "workspace-1",
			Blockers: []ProjectWorkspaceUnlinkBlocker{{Message: "blocked"}},
		},
		"blank blocker message": {
			ProjectID: "project-1", WorkspaceID: "workspace-1",
			Blockers: []ProjectWorkspaceUnlinkBlocker{{Code: "blocked"}},
		},
		"negative blocker count": {
			ProjectID: "project-1", WorkspaceID: "workspace-1",
			Blockers: []ProjectWorkspaceUnlinkBlocker{{Code: "blocked", Message: "blocked", Count: -1}},
		},
		"malformed optional project": {
			ProjectID: "project-1", WorkspaceID: "workspace-1", Unlinked: true,
			Project: &ProjectHomeSummary{},
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := response.Validate(); err == nil {
				t.Fatal("malformed response accepted")
			}
		})
	}
}
