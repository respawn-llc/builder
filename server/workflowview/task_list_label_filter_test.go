package workflowview

import (
	"testing"

	"core/server/workflow/label"
	"core/server/workflowstore"
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

func TestCurrentNodeTaskListDependencyFilterRunsBeforeSortAndPagination(t *testing.T) {
	fixture := newCurrentNodeViewFixture(t, false)
	alpha, err := fixture.store.CreateProjectLabel(fixture.ctx, fixture.binding.ProjectID, "alpha")
	if err != nil {
		t.Fatalf("CreateProjectLabel: %v", err)
	}
	started := func(title string, labelIDs ...label.ID) startedCurrentNodeViewTask {
		t.Helper()
		taskRecord, err := fixture.store.CreateTask(fixture.ctx, workflowstore.CreateTaskRequest{
			ProjectID:  fixture.binding.ProjectID,
			WorkflowID: &fixture.workflowID,
			Title:      title,
			LabelIDs:   labelIDs,
		})
		if err != nil {
			t.Fatalf("CreateTask %q: %v", title, err)
		}
		return fixture.startExistingTask(t, taskRecord)
	}
	alphaFirst := started("Alpha first", alpha.ID)
	alphaSecond := started("Alpha second", alpha.ID)
	alphaBlocked := started("Alpha blocked", alpha.ID)
	betaUnblocked := started("Beta unblocked")
	for _, task := range []startedCurrentNodeViewTask{alphaFirst, alphaSecond, alphaBlocked, betaUnblocked} {
		fixture.setTaskUpdatedAt(t, task.task.ID, 1_000)
	}
	blocker, err := fixture.store.CreateTask(fixture.ctx, workflowstore.CreateTaskRequest{
		ProjectID:  fixture.binding.ProjectID,
		WorkflowID: &fixture.workflowID,
		Title:      "Open blocker",
	})
	if err != nil {
		t.Fatalf("CreateTask blocker: %v", err)
	}
	if _, err := fixture.store.AddTaskDependency(fixture.ctx, workflowstore.TaskDependencyAddRequest{
		BlockerTaskID: blocker.ID,
		BlockedTaskID: alphaBlocked.task.ID,
	}); err != nil {
		t.Fatalf("AddTaskDependency: %v", err)
	}

	projectID := fixture.binding.ProjectID
	workflowID := fixture.workflowID
	limit := 1
	unblocked := true
	alphaFilter := serverapi.WorkflowTaskLabelFilter{
		Kind: serverapi.WorkflowTaskLabelFilterKindNamed,
		Named: &serverapi.WorkflowTaskNamedLabelFilter{
			Mode:     serverapi.WorkflowTaskNamedLabelFilterModeAny,
			LabelIDs: []string{alpha.ID.String()},
		},
	}
	request := serverapi.WorkflowTaskListRequest{
		ProjectID:        &projectID,
		WorkflowID:       &workflowID,
		ColumnKeys:       []string{"agent"},
		StatusKinds:      []serverapi.WorkflowTaskStatusKind{serverapi.WorkflowTaskStatusKindActive},
		LabelFilter:      alphaFilter,
		DependencyFilter: &unblocked,
		Sort: []serverapi.WorkflowTaskListSort{{
			Field:     serverapi.WorkflowTaskListSortFieldTitle,
			Direction: serverapi.WorkflowTaskListSortDirectionAsc,
		}},
		Limit: &limit,
	}
	var got []string
	for {
		page, err := fixture.tasks.List(fixture.ctx, request)
		if err != nil {
			t.Fatalf("TaskList.List: %v", err)
		}
		for _, item := range page.Tasks {
			got = append(got, item.TaskID)
		}
		if page.NextOffset == nil {
			break
		}
		request.Offset = page.NextOffset
	}
	want := []string{string(alphaFirst.task.ID), string(alphaSecond.task.ID)}
	if !equalStrings(got, want) {
		t.Fatalf("filtered task-list pagination = %v, want %v", got, want)
	}

	blocked := false
	request.DependencyFilter = &blocked
	request.Offset = nil
	blockedPage, err := fixture.tasks.List(fixture.ctx, request)
	if err != nil {
		t.Fatalf("TaskList.List blocked: %v", err)
	}
	if len(blockedPage.Tasks) != 1 || blockedPage.Tasks[0].TaskID != string(alphaBlocked.task.ID) {
		t.Fatalf("blocked task-list page = %+v, want blocked Task %q", blockedPage.Tasks, alphaBlocked.task.ID)
	}

	request.DependencyFilter = &unblocked
	request.AttentionKinds = []serverapi.WorkflowTaskAttentionKind{serverapi.WorkflowTaskAttentionKindQuestion}
	noAttention, err := fixture.tasks.List(fixture.ctx, request)
	if err != nil {
		t.Fatalf("TaskList.List attention intersection: %v", err)
	}
	if len(noAttention.Tasks) != 0 {
		t.Fatalf("attention intersection = %+v, want no Tasks", noAttention.Tasks)
	}
}
