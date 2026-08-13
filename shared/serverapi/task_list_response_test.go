package serverapi

import (
	"testing"

	"core/shared/runtimeids"
)

func TestWorkflowTaskListResponseValidatesIntrinsicScopeAndRows(t *testing.T) {
	workflowID := runtimeids.NewWorkflowID()
	otherWorkflowID := runtimeids.NewWorkflowID()
	valid := validWorkflowTaskListResponseForTest(workflowID)

	tests := []struct {
		name     string
		mutate   func(*WorkflowTaskListResponse)
		wantFail bool
	}{
		{name: "valid"},
		{name: "missing project", mutate: func(response *WorkflowTaskListResponse) {
			response.Scope.ProjectID = ""
		}, wantFail: true},
		{name: "inexact project", mutate: func(response *WorkflowTaskListResponse) {
			response.Scope.ProjectID = " project-1"
		}, wantFail: true},
		{name: "zero scoped workflow", mutate: func(response *WorkflowTaskListResponse) {
			zero := runtimeids.WorkflowID{}
			response.Scope.WorkflowID = &zero
		}, wantFail: true},
		{name: "invalid cardinality", mutate: func(response *WorkflowTaskListResponse) {
			response.MatchingWorkflowCardinality = "many"
		}, wantFail: true},
		{name: "none with tasks", mutate: func(response *WorkflowTaskListResponse) {
			response.MatchingWorkflowCardinality = WorkflowTaskListMatchingWorkflowCardinalityNone
		}, wantFail: true},
		{name: "narrowed multiple", mutate: func(response *WorkflowTaskListResponse) {
			response.MatchingWorkflowCardinality = WorkflowTaskListMatchingWorkflowCardinalityMultiple
		}, wantFail: true},
		{name: "zero task workflow", mutate: func(response *WorkflowTaskListResponse) {
			response.Tasks[0].WorkflowID = runtimeids.WorkflowID{}
		}, wantFail: true},
		{name: "narrowed task workflow mismatch", mutate: func(response *WorkflowTaskListResponse) {
			response.Tasks[0].WorkflowID = otherWorkflowID
		}, wantFail: true},
		{name: "one mixes workflows", mutate: func(response *WorkflowTaskListResponse) {
			response.Scope.WorkflowID = nil
			second := response.Tasks[0]
			second.TaskID = "task-2"
			second.WorkflowID = otherWorkflowID
			response.Tasks = append(response.Tasks, second)
		}, wantFail: true},
		{name: "invalid status kind", mutate: func(response *WorkflowTaskListResponse) {
			response.Tasks[0].Status.Kind = "future"
		}, wantFail: true},
		{name: "mismatched native state", mutate: func(response *WorkflowTaskListResponse) {
			response.Tasks[0].Status.NativeState = WorkflowTaskNativeStateTerminal
		}, wantFail: true},
		{name: "zero total dependency progress", mutate: func(response *WorkflowTaskListResponse) {
			response.Tasks[0].DependencyProgress = &WorkflowTaskDependencyProgress{}
		}, wantFail: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := valid
			response.Tasks = append([]WorkflowTaskListItem(nil), valid.Tasks...)
			if test.mutate != nil {
				test.mutate(&response)
			}
			err := response.Validate()
			if (err != nil) != test.wantFail {
				t.Fatalf("Validate() error = %v, wantFail = %t", err, test.wantFail)
			}
		})
	}
}

func TestWorkflowTaskListResponseBindsOriginatingRequestScope(t *testing.T) {
	workflowID := runtimeids.NewWorkflowID()
	otherWorkflowID := runtimeids.NewWorkflowID()
	projectID := "project-1"
	otherProjectID := "project-2"
	response := validWorkflowTaskListResponseForTest(workflowID)

	for _, test := range []struct {
		name    string
		request WorkflowTaskListRequest
	}{
		{name: "matching narrowed", request: WorkflowTaskListRequest{ProjectID: &projectID, WorkflowID: &workflowID}},
		{name: "different project", request: WorkflowTaskListRequest{ProjectID: &otherProjectID, WorkflowID: &workflowID}},
		{name: "different workflow", request: WorkflowTaskListRequest{ProjectID: &projectID, WorkflowID: &otherWorkflowID}},
		{name: "project-wide request received narrowed response", request: WorkflowTaskListRequest{ProjectID: &projectID}},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := response.ValidateForRequest(test.request)
			wantFail := test.name != "matching narrowed"
			if (err != nil) != wantFail {
				t.Fatalf("ValidateForRequest() error = %v, wantFail = %t", err, wantFail)
			}
		})
	}
}

func validWorkflowTaskListResponseForTest(workflowID runtimeids.WorkflowID) WorkflowTaskListResponse {
	return WorkflowTaskListResponse{
		Scope: WorkflowTaskListScope{
			ProjectID:  "project-1",
			WorkflowID: &workflowID,
		},
		MatchingWorkflowCardinality: WorkflowTaskListMatchingWorkflowCardinalityOne,
		Tasks: []WorkflowTaskListItem{{
			TaskID:     "task-1",
			WorkflowID: workflowID,
			Status: WorkflowTaskStatus{
				Kind:        WorkflowTaskStatusKindBacklog,
				NativeState: WorkflowTaskNativeStateActive,
			},
		}},
	}
}
