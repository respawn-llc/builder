package workflowview

import (
	"errors"
	"testing"

	"core/server/workflow"
	"core/server/workflowstore"
	"core/shared/serverapi"
)

func TestTaskListProjectScopeFailuresAreTyped(t *testing.T) {
	t.Run("no linked workflows", func(t *testing.T) {
		ctx, _, _, binding, view := newWorkflowViewTestContextFixture(t)
		projectID := binding.ProjectID
		_, err := view.tasks(t).List(ctx, serverapi.WorkflowTaskListRequest{
			LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone}, ProjectID: &projectID})
		var scopeErr *serverapi.WorkflowTaskListScopeError
		if !errors.As(err, &scopeErr) ||
			scopeErr.Reason != serverapi.WorkflowTaskListScopeReasonNoLinkedWorkflows ||
			scopeErr.ProjectID == nil || *scopeErr.ProjectID != projectID ||
			scopeErr.WorkflowID != nil {
			t.Fatalf("ListTasks error = %+v, want typed no-links error for %q", err, projectID)
		}
	})

	t.Run("explicit workflow is not linked", func(t *testing.T) {
		ctx, _, workflowStore, binding, view := newWorkflowViewTestContextFixture(t)
		linkedWorkflowID := createWorkflowViewValidWorkflow(t, ctx, workflowStore)
		unlinkedWorkflowID := createWorkflowViewValidWorkflow(t, ctx, workflowStore)
		if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, linkedWorkflowID, true); err != nil {
			t.Fatalf("LinkWorkflow: %v", err)
		}
		projectID, workflowID := binding.ProjectID, string(unlinkedWorkflowID)
		_, err := view.tasks(t).List(ctx, serverapi.WorkflowTaskListRequest{
			LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone},
			ProjectID:   &projectID,
			WorkflowID:  &workflowID,
		})

		var scopeErr *serverapi.WorkflowTaskListScopeError
		if !errors.As(err, &scopeErr) ||
			scopeErr.Reason != serverapi.WorkflowTaskListScopeReasonWorkflowNotLinked ||
			scopeErr.ProjectID == nil || *scopeErr.ProjectID != projectID ||
			scopeErr.WorkflowID == nil || *scopeErr.WorkflowID != workflowID {
			t.Fatalf("ListTasks error = %+v, want typed not-linked error for %q/%q", err, projectID, workflowID)
		}
	})

	t.Run("column operation requires workflow", func(t *testing.T) {
		ctx, _, workflowStore, binding, view := newWorkflowViewTestContextFixture(t)
		workflowID := createWorkflowViewValidWorkflow(t, ctx, workflowStore)
		if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, workflowID, true); err != nil {
			t.Fatalf("LinkWorkflow: %v", err)
		}
		projectID := binding.ProjectID
		_, err := view.tasks(t).List(ctx, serverapi.WorkflowTaskListRequest{
			LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone},
			ProjectID:   &projectID,
			ColumnKeys:  []string{"backlog"},
		})

		var scopeErr *serverapi.WorkflowTaskListScopeError
		if !errors.As(err, &scopeErr) ||
			scopeErr.Reason != serverapi.WorkflowTaskListScopeReasonWorkflowRequiredColumns ||
			scopeErr.ProjectID == nil || *scopeErr.ProjectID != projectID ||
			scopeErr.WorkflowID != nil {
			t.Fatalf("ListTasks error = %+v, want typed workflow-required error for %q", err, projectID)
		}
	})
}

func TestTaskListMatchingWorkflowCardinalityUsesFullFilteredSet(t *testing.T) {
	t.Run("none", func(t *testing.T) {
		ctx, _, workflowStore, binding, view := newWorkflowViewTestContextFixture(t)
		createTwoLinkedWorkflowViewWorkflows(t, ctx, workflowStore, binding.ProjectID)
		projectID := binding.ProjectID
		response, err := view.tasks(t).List(ctx, serverapi.WorkflowTaskListRequest{
			LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone}, ProjectID: &projectID})
		if err != nil {
			t.Fatalf("ListTasks: %v", err)
		}
		if response.MatchingWorkflowCardinality != serverapi.WorkflowTaskListMatchingWorkflowCardinalityNone || len(response.Tasks) != 0 {
			t.Fatalf("empty filtered response = %+v, want none", response)
		}
	})

	t.Run("one after filtering", func(t *testing.T) {
		ctx, _, workflowStore, binding, view := newWorkflowViewTestContextFixture(t)
		firstWorkflowID, secondWorkflowID := createTwoLinkedWorkflowViewWorkflows(t, ctx, workflowStore, binding.ProjectID)
		if _, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{
			ProjectID:  binding.ProjectID,
			WorkflowID: workflowIDPointerForTest(firstWorkflowID),
			Title:      "Backlog",
			Body:       "Body",
		}); err != nil {
			t.Fatalf("CreateTask backlog: %v", err)
		}
		running, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{
			ProjectID:  binding.ProjectID,
			WorkflowID: workflowIDPointerForTest(secondWorkflowID),
			Title:      "Running",
			Body:       "Body",
		})
		if err != nil {
			t.Fatalf("CreateTask running: %v", err)
		}
		started, err := workflowStore.StartTask(ctx, running.ID)
		if err != nil {
			t.Fatalf("StartTask: %v", err)
		}
		if _, err := workflowStore.ClaimRun(ctx, started.RunID, 0); err != nil {
			t.Fatalf("ClaimRun: %v", err)
		}

		projectID := binding.ProjectID
		response, err := view.tasks(t).List(ctx, serverapi.WorkflowTaskListRequest{
			LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone},
			ProjectID:   &projectID,
			StatusKinds: []serverapi.WorkflowTaskStatusKind{serverapi.WorkflowTaskStatusKindBacklog},
		})

		if err != nil {
			t.Fatalf("ListTasks: %v", err)
		}
		if response.MatchingWorkflowCardinality != serverapi.WorkflowTaskListMatchingWorkflowCardinalityOne || len(response.Tasks) != 1 || response.Tasks[0].WorkflowID != string(firstWorkflowID) {
			t.Fatalf("filtered response = %+v, want one matching workflow", response)
		}
	})

	t.Run("multiple beyond first page", func(t *testing.T) {
		ctx, _, workflowStore, binding, view := newWorkflowViewTestContextFixture(t)
		firstWorkflowID, secondWorkflowID := createTwoLinkedWorkflowViewWorkflows(t, ctx, workflowStore, binding.ProjectID)
		for _, task := range []struct {
			workflowID workflow.WorkflowID
			title      string
		}{
			{workflowID: firstWorkflowID, title: "Alpha"},
			{workflowID: secondWorkflowID, title: "Zulu"},
		} {
			if _, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{
				ProjectID:  binding.ProjectID,
				WorkflowID: workflowIDPointerForTest(task.workflowID),
				Title:      task.title,
				Body:       "Body",
			}); err != nil {
				t.Fatalf("CreateTask %s: %v", task.title, err)
			}
		}
		projectID := binding.ProjectID
		response, err := view.tasks(t).List(ctx, serverapi.WorkflowTaskListRequest{
			LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone},
			ProjectID:   &projectID,
			PageSize:    1,
			Sort:        []serverapi.WorkflowTaskListSort{{Field: serverapi.WorkflowTaskListSortFieldTitle, Direction: serverapi.WorkflowTaskListSortDirectionAsc}},
		})

		if err != nil {
			t.Fatalf("ListTasks: %v", err)
		}
		if response.MatchingWorkflowCardinality != serverapi.WorkflowTaskListMatchingWorkflowCardinalityMultiple || len(response.Tasks) != 1 || response.NextPageToken == nil {
			t.Fatalf("first page = %+v, want multiple cardinality and continuation", response)
		}
	})
}

func TestTaskListProjectWidePaginationFreezesCardinalityWhileRowsRemainLive(t *testing.T) {
	t.Run("one remains frozen after another workflow gains a match", func(t *testing.T) {
		ctx, _, workflowStore, binding, view := newWorkflowViewTestContextFixture(t)
		firstWorkflowID, secondWorkflowID := createTwoLinkedWorkflowViewWorkflows(t, ctx, workflowStore, binding.ProjectID)
		for _, title := range []string{"Alpha", "Bravo"} {
			if _, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{
				ProjectID:  binding.ProjectID,
				WorkflowID: workflowIDPointerForTest(firstWorkflowID),
				Title:      title,
				Body:       "Body",
			}); err != nil {
				t.Fatalf("CreateTask %s: %v", title, err)
			}
		}
		projectID := binding.ProjectID
		sortByTitle := []serverapi.WorkflowTaskListSort{{Field: serverapi.WorkflowTaskListSortFieldTitle, Direction: serverapi.WorkflowTaskListSortDirectionAsc}}
		firstPage, err := view.tasks(t).List(ctx, serverapi.WorkflowTaskListRequest{
			LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone},
			ProjectID:   &projectID,
			PageSize:    1,
			Sort:        sortByTitle,
		})

		if err != nil {
			t.Fatalf("ListTasks first page: %v", err)
		}
		if firstPage.MatchingWorkflowCardinality != serverapi.WorkflowTaskListMatchingWorkflowCardinalityOne || firstPage.NextPageToken == nil {
			t.Fatalf("first page = %+v, want one and continuation", firstPage)
		}
		if _, err := view.tasks(t).List(ctx, serverapi.WorkflowTaskListRequest{
			LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone},
			PageToken:   *firstPage.NextPageToken,
			StatusKinds: []serverapi.WorkflowTaskStatusKind{serverapi.WorkflowTaskStatusKindDone},
			Sort:        sortByTitle,
		}); !errors.Is(err, ErrInvalidPageToken) {
			t.Fatalf("conflicting continuation error = %v, want ErrInvalidPageToken", err)
		}
		selectedWorkflowID := string(secondWorkflowID)
		if _, err := view.tasks(t).List(ctx, serverapi.WorkflowTaskListRequest{
			LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone},
			WorkflowID:  &selectedWorkflowID,
			PageToken:   *firstPage.NextPageToken,
			Sort:        sortByTitle,
		}); !errors.Is(err, ErrInvalidPageToken) {
			t.Fatalf("conflicting continuation scope error = %v, want ErrInvalidPageToken", err)
		}
		if _, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{
			ProjectID:  binding.ProjectID,
			WorkflowID: workflowIDPointerForTest(secondWorkflowID),
			Title:      "Zulu",
			Body:       "Body",
		}); err != nil {
			t.Fatalf("CreateTask Zulu: %v", err)
		}
		nextPage, err := view.tasks(t).List(ctx, serverapi.WorkflowTaskListRequest{
			LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone},
			PageToken:   *firstPage.NextPageToken,
			PageSize:    10,
			Sort:        sortByTitle,
		})

		if err != nil {
			t.Fatalf("ListTasks continuation: %v", err)
		}
		if nextPage.MatchingWorkflowCardinality != serverapi.WorkflowTaskListMatchingWorkflowCardinalityOne ||
			nextPage.Scope.WorkflowID != nil ||
			len(nextPage.Tasks) != 2 ||
			nextPage.Tasks[0].Title != "Bravo" ||
			nextPage.Tasks[1].Title != "Zulu" {
			t.Fatalf("live continuation = %+v, want frozen one with Bravo and Zulu", nextPage)
		}
	})

	t.Run("multiple remains frozen after one workflow loses all matches", func(t *testing.T) {
		ctx, _, workflowStore, binding, view := newWorkflowViewTestContextFixture(t)
		firstWorkflowID, secondWorkflowID := createTwoLinkedWorkflowViewWorkflows(t, ctx, workflowStore, binding.ProjectID)
		for _, title := range []string{"Alpha", "Bravo"} {
			if _, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{
				ProjectID:  binding.ProjectID,
				WorkflowID: workflowIDPointerForTest(firstWorkflowID),
				Title:      title,
				Body:       "Body",
			}); err != nil {
				t.Fatalf("CreateTask %s: %v", title, err)
			}
		}
		zulu, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{
			ProjectID:  binding.ProjectID,
			WorkflowID: workflowIDPointerForTest(secondWorkflowID),
			Title:      "Zulu",
			Body:       "Body",
		})
		if err != nil {
			t.Fatalf("CreateTask Zulu: %v", err)
		}
		projectID := binding.ProjectID
		sortByTitle := []serverapi.WorkflowTaskListSort{{Field: serverapi.WorkflowTaskListSortFieldTitle, Direction: serverapi.WorkflowTaskListSortDirectionAsc}}
		statusFilter := []serverapi.WorkflowTaskStatusKind{serverapi.WorkflowTaskStatusKindBacklog}
		firstPage, err := view.tasks(t).List(ctx, serverapi.WorkflowTaskListRequest{
			LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone},
			ProjectID:   &projectID,
			StatusKinds: statusFilter,
			PageSize:    1,
			Sort:        sortByTitle,
		})

		if err != nil {
			t.Fatalf("ListTasks first page: %v", err)
		}
		if firstPage.MatchingWorkflowCardinality != serverapi.WorkflowTaskListMatchingWorkflowCardinalityMultiple || firstPage.NextPageToken == nil {
			t.Fatalf("first page = %+v, want multiple and continuation", firstPage)
		}
		if _, err := workflowStore.StartTask(ctx, zulu.ID); err != nil {
			t.Fatalf("StartTask Zulu: %v", err)
		}
		nextPage, err := view.tasks(t).List(ctx, serverapi.WorkflowTaskListRequest{
			LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone},
			StatusKinds: statusFilter,
			PageToken:   *firstPage.NextPageToken,
			PageSize:    10,
			Sort:        sortByTitle,
		})

		if err != nil {
			t.Fatalf("ListTasks continuation: %v", err)
		}
		if nextPage.MatchingWorkflowCardinality != serverapi.WorkflowTaskListMatchingWorkflowCardinalityMultiple ||
			len(nextPage.Tasks) != 1 ||
			nextPage.Tasks[0].Title != "Bravo" {
			t.Fatalf("live continuation = %+v, want frozen multiple with only Bravo", nextPage)
		}
	})
}
