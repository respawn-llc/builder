package serverapi

import (
	"testing"

	"core/shared/runtimeids"
)

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

func TestProjectHomeSummaryValidateAllowsLegacyEmptyProjectKey(t *testing.T) {
	summary := validProjectHomeSummaryForResponseTest()
	summary.ProjectKey = ""
	if err := summary.Validate(); err != nil {
		t.Fatalf("legacy empty project key rejected: %v", err)
	}
}

func TestProjectHomeSummaryValidateRejectsMalformedOptionalWorkflowValues(t *testing.T) {
	t.Run("blank workflow ID", func(t *testing.T) {
		summary := validProjectHomeSummaryForResponseTest()
		var blank runtimeids.WorkflowID
		name := "Workflow"
		summary.DefaultWorkflowID = &blank
		summary.DefaultWorkflowName = name
		summary.DefaultWorkflowValid = true
		if err := summary.Validate(); err == nil {
			t.Fatal("blank workflow ID accepted")
		}
	})
	t.Run("blank workflow name", func(t *testing.T) {
		summary := validProjectHomeSummaryForResponseTest()
		id := runtimeids.NewWorkflowID()
		summary.DefaultWorkflowID = &id
		summary.DefaultWorkflowName = ""
		summary.DefaultWorkflowValid = true
		if err := summary.Validate(); err == nil {
			t.Fatal("blank workflow name accepted")
		}
	})
	t.Run("workflow name requires workflow ID", func(t *testing.T) {
		summary := validProjectHomeSummaryForResponseTest()
		summary.DefaultWorkflowName = "Workflow"
		if err := summary.Validate(); err == nil {
			t.Fatal("asymmetric workflow fields accepted")
		}
	})
	t.Run("workflow validity follows presence", func(t *testing.T) {
		summary := validProjectHomeSummaryForResponseTest()
		summary.DefaultWorkflowValid = true
		if err := summary.Validate(); err == nil {
			t.Fatal("valid workflow flag without a workflow accepted")
		}
	})
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
