package workflowview

import (
	"testing"

	"core/shared/serverapi"
)

func requireCurrentNodeTaskListIDs(t *testing.T, items []serverapi.WorkflowTaskListItem, want []string) {
	t.Helper()
	got := make(map[string]bool, len(items))
	for _, item := range items {
		got[item.TaskID] = true
	}
	if len(got) != len(want) {
		t.Fatalf("task list IDs = %v, want %v", got, want)
	}
	for _, taskID := range want {
		if !got[taskID] {
			t.Fatalf("task list IDs = %v, missing %s", got, taskID)
		}
	}
}

func TestCurrentNodeTaskListLabelExclusionsFilterTasks(t *testing.T) {
	fixture := newCurrentNodeLabelFilterFixture(t)
	projectID := fixture.binding.ProjectID
	for _, tt := range fixture.exclusionCases() {
		t.Run(tt.name, func(t *testing.T) {
			response, err := fixture.tasks.List(fixture.ctx, serverapi.WorkflowTaskListRequest{
				ProjectID:   &projectID,
				WorkflowID:  &fixture.workflowID,
				LabelFilter: tt.filter,
			})
			if err != nil {
				t.Fatalf("TaskList.List: %v", err)
			}
			requireCurrentNodeTaskListIDs(t, response.Tasks, tt.want)
		})
	}
}
