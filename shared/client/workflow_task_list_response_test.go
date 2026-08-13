package client

import (
	"testing"

	"core/shared/runtimeids"
	"core/shared/serverapi"
)

func TestValidateWorkflowTaskListResponseBindsRequestScope(t *testing.T) {
	workflowID := runtimeids.NewWorkflowID()
	projectID := "project-1"
	otherProjectID := "project-2"
	response := serverapi.WorkflowTaskListResponse{
		Scope: serverapi.WorkflowTaskListScope{
			ProjectID:  projectID,
			WorkflowID: &workflowID,
		},
		MatchingWorkflowCardinality: serverapi.WorkflowTaskListMatchingWorkflowCardinalityOne,
		Tasks: []serverapi.WorkflowTaskListItem{{
			TaskID:     "task-1",
			WorkflowID: workflowID,
			Status: serverapi.WorkflowTaskStatus{
				Kind:        serverapi.WorkflowTaskStatusKindBacklog,
				NativeState: serverapi.WorkflowTaskNativeStateActive,
			},
		}},
	}
	if _, err := validateWorkflowTaskListResponse(
		"list workflow tasks",
		serverapi.WorkflowTaskListRequest{ProjectID: &otherProjectID, WorkflowID: &workflowID},
		response,
		nil,
	); err == nil {
		t.Fatal("mismatched Project response accepted")
	}
	if _, err := validateWorkflowTaskListResponse(
		"list workflow tasks",
		serverapi.WorkflowTaskListRequest{ProjectID: &projectID},
		response,
		nil,
	); err == nil {
		t.Fatal("mismatched Workflow scope response accepted")
	}
}
