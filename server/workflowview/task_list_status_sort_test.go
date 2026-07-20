package workflowview

import (
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"core/server/workflow"
	"core/server/workflowstore"
	"core/shared/serverapi"
)

func TestListTasksFiltersTypedStatusAndColumn(t *testing.T) {
	ctx, _, workflowStore, binding, view := newWorkflowViewTestContextFixture(t)
	workflowID := createWorkflowViewValidWorkflow(t, ctx, workflowStore)
	if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, workflowID, true); err != nil {
		t.Fatalf("LinkWorkflow: %v", err)
	}
	backlog, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{ProjectID: binding.ProjectID, Title: "Backlog", Body: "Body"})
	if err != nil {
		t.Fatalf("CreateTask backlog: %v", err)
	}
	running, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{ProjectID: binding.ProjectID, Title: "Running", Body: "Body"})
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

	projectID, workflowIDString := binding.ProjectID, string(workflowID)
	backlogResp, err := view.tasks(t).List(ctx, serverapi.WorkflowTaskListRequest{
		LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone},
		ProjectID:   &projectID,
		WorkflowID:  &workflowIDString,
		ColumnKeys:  []string{"backlog"},
		StatusKinds: []serverapi.WorkflowTaskStatusKind{serverapi.WorkflowTaskStatusKindBacklog},
	})

	if err != nil {
		t.Fatalf("ListTasks backlog: %v", err)
	}
	if len(backlogResp.Tasks) != 1 || backlogResp.Tasks[0].TaskID != string(backlog.ID) {
		t.Fatalf("backlog tasks = %+v", backlogResp.Tasks)
	}

	runningResp, err := view.tasks(t).List(ctx, serverapi.WorkflowTaskListRequest{
		LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone},
		ProjectID:   &projectID,
		WorkflowID:  &workflowIDString,
		ColumnKeys:  []string{"agent"},
		StatusKinds: []serverapi.WorkflowTaskStatusKind{serverapi.WorkflowTaskStatusKindRunning},
	})

	if err != nil {
		t.Fatalf("ListTasks running: %v", err)
	}
	if len(runningResp.Tasks) != 1 || runningResp.Tasks[0].TaskID != string(running.ID) {
		t.Fatalf("running tasks = %+v", runningResp.Tasks)
	}
}

func TestListTasksStatusAndFiltersMatchCanonicalDetail(t *testing.T) {
	ctx, store, workflowStore, binding, view := newWorkflowViewTestContextFixture(t)
	workflowID := createWorkflowViewValidWorkflow(t, ctx, workflowStore)
	if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, workflowID, true); err != nil {
		t.Fatalf("LinkWorkflow: %v", err)
	}
	createTask := func(title string) workflowstore.TaskRecord {
		t.Helper()
		task, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{ProjectID: binding.ProjectID, Title: title, Body: "Body"})
		if err != nil {
			t.Fatalf("CreateTask %s: %v", title, err)
		}
		return task
	}

	backlog := createTask("Backlog")
	active := createTask("Active")
	activeStarted, err := workflowStore.StartTask(ctx, active.ID)
	if err != nil {
		t.Fatalf("StartTask active: %v", err)
	}
	if _, err := store.DB().ExecContext(ctx, `DELETE FROM task_runs WHERE id = ?`, string(activeStarted.RunID)); err != nil {
		t.Fatalf("create active placement fixture: %v", err)
	}
	queued := createTask("Queued")
	if _, err := workflowStore.StartTask(ctx, queued.ID); err != nil {
		t.Fatalf("StartTask queued: %v", err)
	}
	running := createTask("Running")
	runningStarted, err := workflowStore.StartTask(ctx, running.ID)
	if err != nil {
		t.Fatalf("StartTask running: %v", err)
	}
	if _, err := workflowStore.ClaimRun(ctx, runningStarted.RunID, 0); err != nil {
		t.Fatalf("ClaimRun running: %v", err)
	}
	interrupted := createTask("Interrupted")
	interruptedStarted, err := workflowStore.StartTask(ctx, interrupted.ID)
	if err != nil {
		t.Fatalf("StartTask interrupted: %v", err)
	}
	interruptedClaimed, err := workflowStore.ClaimRun(ctx, interruptedStarted.RunID, 0)
	if err != nil {
		t.Fatalf("ClaimRun interrupted: %v", err)
	}
	if err := workflowStore.InterruptRunGeneration(ctx, interruptedStarted.RunID, interruptedClaimed.Generation, "manual", "{}"); err != nil {
		t.Fatalf("InterruptRunGeneration: %v", err)
	}
	question := createTask("Question")
	questionStarted, err := workflowStore.StartTask(ctx, question.ID)
	if err != nil {
		t.Fatalf("StartTask question: %v", err)
	}
	questionClaimed, err := workflowStore.ClaimRun(ctx, questionStarted.RunID, 0)
	if err != nil {
		t.Fatalf("ClaimRun question: %v", err)
	}
	if err := workflowStore.SetRunWaitingAsk(ctx, questionStarted.RunID, questionClaimed.Generation, "ask-list"); err != nil {
		t.Fatalf("SetRunWaitingAsk: %v", err)
	}
	done := createTask("Done")
	doneStarted, err := workflowStore.StartTask(ctx, done.ID)
	if err != nil {
		t.Fatalf("StartTask done: %v", err)
	}
	if _, err := workflowStore.CompleteRun(ctx, workflowstore.CompleteRunRequest{RunID: doneStarted.RunID, TransitionID: "done"}); err != nil {
		t.Fatalf("CompleteRun done: %v", err)
	}
	requireDoneTransitionApproval(t, ctx, store, workflowID)
	approval := createTask("Approval")
	approvalStarted, err := workflowStore.StartTask(ctx, approval.ID)
	if err != nil {
		t.Fatalf("StartTask approval: %v", err)
	}
	if _, err := workflowStore.CompleteRun(ctx, workflowstore.CompleteRunRequest{RunID: approvalStarted.RunID, TransitionID: "done"}); err != nil {
		t.Fatalf("CompleteRun approval: %v", err)
	}
	if _, err := store.DB().ExecContext(ctx, `UPDATE task_runs SET completed_at_unix_ms = NULL, interrupted_at_unix_ms = 1, waiting_ask_id = 'stale-list-ask' WHERE id = ?`, string(approvalStarted.RunID)); err != nil {
		t.Fatalf("create stale completed-placement run fixture: %v", err)
	}
	canceled := createTask("Canceled")
	if _, err := workflowStore.StartTask(ctx, canceled.ID); err != nil {
		t.Fatalf("StartTask canceled: %v", err)
	}
	if _, err := workflowStore.CancelTask(ctx, canceled.ID, "stop"); err != nil {
		t.Fatalf("CancelTask: %v", err)
	}
	forceCanceledBacklogPlacementWithoutTerminal(t, ctx, store, canceled.ID, workflowID)

	projectID, workflowIDString := binding.ProjectID, string(workflowID)
	list := func(request serverapi.WorkflowTaskListRequest) serverapi.WorkflowTaskListResponse {
		t.Helper()
		request.ProjectID = &projectID
		response, err := view.tasks(t).List(ctx, request)
		if err != nil {
			t.Fatalf("ListTasks %+v: %v", request, err)
		}
		return response
	}
	wantByStatus := map[serverapi.WorkflowTaskStatusKind]string{
		serverapi.WorkflowTaskStatusKindBacklog:         string(backlog.ID),
		serverapi.WorkflowTaskStatusKindActive:          string(active.ID),
		serverapi.WorkflowTaskStatusKindQueued:          string(queued.ID),
		serverapi.WorkflowTaskStatusKindRunning:         string(running.ID),
		serverapi.WorkflowTaskStatusKindInterrupted:     string(interrupted.ID),
		serverapi.WorkflowTaskStatusKindWaitingQuestion: string(question.ID),
		serverapi.WorkflowTaskStatusKindDone:            string(done.ID),
		serverapi.WorkflowTaskStatusKindWaitingApproval: string(approval.ID),
		serverapi.WorkflowTaskStatusKindCanceled:        string(canceled.ID),
	}

	all := list(serverapi.WorkflowTaskListRequest{
		LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone}})
	if len(all.Tasks) != len(wantByStatus) {
		t.Fatalf("all list tasks = %+v, want %d tasks", all.Tasks, len(wantByStatus))
	}
	for _, item := range all.Tasks {
		detail, err := view.detail(t).GetTask(ctx, item.TaskID)
		if err != nil {
			t.Fatalf("GetTask %s: %v", item.TaskID, err)
		}
		if !reflect.DeepEqual(item.Status, detail.Status) {
			t.Fatalf("list status for %s = %+v, want detail %+v", item.TaskID, item.Status, detail.Status)
		}
		if wantTaskID, ok := wantByStatus[item.Status.Kind]; !ok || wantTaskID != item.TaskID {
			t.Fatalf("list item = %+v, unexpected status/task pairing", item)
		}
	}

	for kind, taskID := range wantByStatus {
		response := list(serverapi.WorkflowTaskListRequest{
			LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone}, StatusKinds: []serverapi.WorkflowTaskStatusKind{kind}})
		if len(response.Tasks) != 1 || response.Tasks[0].TaskID != taskID || response.Tasks[0].Status.Kind != kind {
			t.Fatalf("status filter %q = %+v, want task %q", kind, response.Tasks, taskID)
		}
	}
	for attention, taskID := range map[serverapi.WorkflowTaskAttentionKind]string{
		serverapi.WorkflowTaskAttentionKindApproval:    string(approval.ID),
		serverapi.WorkflowTaskAttentionKindQuestion:    string(question.ID),
		serverapi.WorkflowTaskAttentionKindInterrupted: string(interrupted.ID),
	} {
		response := list(serverapi.WorkflowTaskListRequest{
			LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone}, AttentionKinds: []serverapi.WorkflowTaskAttentionKind{attention}})
		if len(response.Tasks) != 1 || response.Tasks[0].TaskID != taskID {
			t.Fatalf("attention filter %q = %+v, want task %q", attention, response.Tasks, taskID)
		}
	}
	for columnKey, taskIDs := range map[string]map[string]bool{
		"backlog": {string(backlog.ID): true},
		"done":    {string(done.ID): true, string(canceled.ID): true},
	} {
		response := list(serverapi.WorkflowTaskListRequest{
			LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone},
			WorkflowID:  &workflowIDString,
			ColumnKeys:  []string{columnKey},
		})
		if len(response.Tasks) != len(taskIDs) {
			t.Fatalf("column filter %q = %+v, want task IDs %v", columnKey, response.Tasks, taskIDs)
		}
		for _, item := range response.Tasks {
			if !taskIDs[item.TaskID] {
				t.Fatalf("column filter %q = %+v, want task IDs %v", columnKey, response.Tasks, taskIDs)
			}
		}
	}
	approvalResponse := list(serverapi.WorkflowTaskListRequest{
		LabelFilter:    serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone},
		StatusKinds:    []serverapi.WorkflowTaskStatusKind{serverapi.WorkflowTaskStatusKindWaitingApproval},
		AttentionKinds: []serverapi.WorkflowTaskAttentionKind{serverapi.WorkflowTaskAttentionKindApproval},
	})
	if len(approvalResponse.Tasks) != 1 || approvalResponse.Tasks[0].TaskID != string(approval.ID) || approvalResponse.Tasks[0].RunCount != 1 || slices.Contains(approvalResponse.Tasks[0].Status.RunIDs, string(approvalStarted.RunID)) || slices.Contains(approvalResponse.Tasks[0].Status.AttentionTypes, serverapi.WorkflowTaskAttentionKindInterrupted) {
		t.Fatalf("approval list item must preserve history count but exclude stale current facts: %+v", approvalResponse.Tasks[0])
	}
	for _, request := range []serverapi.WorkflowTaskListRequest{
		{LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone}, StatusKinds: []serverapi.WorkflowTaskStatusKind{serverapi.WorkflowTaskStatusKindWaitingApproval}, AttentionKinds: []serverapi.WorkflowTaskAttentionKind{serverapi.WorkflowTaskAttentionKindQuestion}},
		{LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone}, WorkflowID: &workflowIDString, StatusKinds: []serverapi.WorkflowTaskStatusKind{serverapi.WorkflowTaskStatusKindRunning}, ColumnKeys: []string{"backlog"}},
	} {
		if response := list(request); len(response.Tasks) != 0 {
			t.Fatalf("combined filters %+v = %+v, want no tasks", request, response.Tasks)
		}
	}
	for _, taskID := range []workflow.TaskID{approval.ID, question.ID, interrupted.ID} {
		if _, err := workflowStore.CancelTask(ctx, taskID, "stop"); err != nil {
			t.Fatalf("CancelTask %s: %v", taskID, err)
		}
		detail := mustTaskDetail(t, view, ctx, string(taskID))
		if detail.Status.Kind != serverapi.WorkflowTaskStatusKindCanceled || len(detail.Status.AttentionTypes) != 0 {
			t.Fatalf("canceled detail status = %+v, want no attention", detail.Status)
		}
	}
	for _, attention := range []serverapi.WorkflowTaskAttentionKind{
		serverapi.WorkflowTaskAttentionKindApproval,
		serverapi.WorkflowTaskAttentionKindQuestion,
		serverapi.WorkflowTaskAttentionKindInterrupted,
	} {
		if response := list(serverapi.WorkflowTaskListRequest{
			LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone}, AttentionKinds: []serverapi.WorkflowTaskAttentionKind{attention}}); len(response.Tasks) != 0 {
			t.Fatalf("%s attention after cancellation = %+v, want none", attention, response.Tasks)
		}
	}

	for _, request := range []serverapi.WorkflowTaskListRequest{
		{LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone}, StatusKinds: []serverapi.WorkflowTaskStatusKind{"invalid"}},
		{LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone}, AttentionKinds: []serverapi.WorkflowTaskAttentionKind{"invalid"}},
		{LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone}, WorkflowID: &workflowIDString, ColumnKeys: []string{"missing"}},
	} {
		request.ProjectID = &projectID
		_, err := view.tasks(t).List(ctx, request)
		var validationErr serverapi.WorkflowRequestValidationError
		if !errors.As(err, &validationErr) {
			t.Fatalf("ListTasks invalid request %+v error = %v, want validation error", request, err)
		}
	}
}

func TestListTasksSortAndCursorPagination(t *testing.T) {
	ctx, store, workflowStore, binding, view := newWorkflowViewTestContextFixture(t)
	workflowID := createWorkflowViewValidWorkflow(t, ctx, workflowStore)
	if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, workflowID, true); err != nil {
		t.Fatalf("LinkWorkflow: %v", err)
	}
	createTask := func(title string) workflowstore.TaskRecord {
		t.Helper()
		task, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{ProjectID: binding.ProjectID, Title: title, Body: "Body"})
		if err != nil {
			t.Fatalf("CreateTask %s: %v", title, err)
		}
		return task
	}

	backlog := createTask("Backlog")
	active := createTask("Active")
	activeStarted, err := workflowStore.StartTask(ctx, active.ID)
	if err != nil {
		t.Fatalf("StartTask active: %v", err)
	}
	if _, err := store.DB().ExecContext(ctx, `DELETE FROM task_runs WHERE id = ?`, string(activeStarted.RunID)); err != nil {
		t.Fatalf("create active placement fixture: %v", err)
	}
	queued := createTask("Queued")
	if _, err := workflowStore.StartTask(ctx, queued.ID); err != nil {
		t.Fatalf("StartTask queued: %v", err)
	}
	running := createTask("Running")
	runningStarted, err := workflowStore.StartTask(ctx, running.ID)
	if err != nil {
		t.Fatalf("StartTask running: %v", err)
	}
	if _, err := workflowStore.ClaimRun(ctx, runningStarted.RunID, 0); err != nil {
		t.Fatalf("ClaimRun running: %v", err)
	}
	done := createTask("Done")
	doneStarted, err := workflowStore.StartTask(ctx, done.ID)
	if err != nil {
		t.Fatalf("StartTask done: %v", err)
	}
	if _, err := workflowStore.CompleteRun(ctx, workflowstore.CompleteRunRequest{RunID: doneStarted.RunID, TransitionID: "done"}); err != nil {
		t.Fatalf("CompleteRun done: %v", err)
	}

	projectID, workflowIDString := binding.ProjectID, string(workflowID)
	baseRequest := serverapi.WorkflowTaskListRequest{
		LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone}, ProjectID: &projectID, PageSize: 2}
	listAllPages := func(request serverapi.WorkflowTaskListRequest) []serverapi.WorkflowTaskListItem {
		t.Helper()
		items := make([]serverapi.WorkflowTaskListItem, 0, 5)
		seen := map[string]bool{}
		for {
			response, err := view.tasks(t).List(ctx, request)
			if err != nil {
				t.Fatalf("ListTasks %+v: %v", request, err)
			}
			if len(response.Tasks) > request.PageSize {
				t.Fatalf("page has %d tasks, requested %d", len(response.Tasks), request.PageSize)
			}
			for _, item := range response.Tasks {
				if seen[item.TaskID] {
					t.Fatalf("pagination duplicated task %q in %+v", item.TaskID, items)
				}
				seen[item.TaskID] = true
				items = append(items, item)
			}
			if response.NextPageToken == nil {
				return items
			}
			request.PageToken = *response.NextPageToken
		}
	}
	sortedRequest := func(field serverapi.WorkflowTaskListSortField, direction serverapi.WorkflowTaskListSortDirection) serverapi.WorkflowTaskListRequest {
		request := serverapi.WorkflowTaskListRequest{
			ProjectID:   baseRequest.ProjectID,
			LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone},
			PageSize:    baseRequest.PageSize,
			Sort:        []serverapi.WorkflowTaskListSort{{Field: field, Direction: direction}},
		}
		if field == serverapi.WorkflowTaskListSortFieldColumn {
			request.WorkflowID = &workflowIDString
		}
		return request
	}
	for _, tt := range []struct {
		name      string
		direction serverapi.WorkflowTaskListSortDirection
		want      []serverapi.WorkflowTaskStatusKind
	}{
		{name: "ascending", direction: serverapi.WorkflowTaskListSortDirectionAsc, want: []serverapi.WorkflowTaskStatusKind{
			serverapi.WorkflowTaskStatusKindDone, serverapi.WorkflowTaskStatusKindRunning,
			serverapi.WorkflowTaskStatusKindQueued, serverapi.WorkflowTaskStatusKindBacklog,
			serverapi.WorkflowTaskStatusKindActive,
		}},
		{name: "descending", direction: serverapi.WorkflowTaskListSortDirectionDesc, want: []serverapi.WorkflowTaskStatusKind{
			serverapi.WorkflowTaskStatusKindActive, serverapi.WorkflowTaskStatusKindBacklog,
			serverapi.WorkflowTaskStatusKindQueued, serverapi.WorkflowTaskStatusKindRunning,
			serverapi.WorkflowTaskStatusKindDone,
		}},
	} {
		t.Run("status "+tt.name, func(t *testing.T) {
			got := taskStatusKinds(listAllPages(sortedRequest(serverapi.WorkflowTaskListSortFieldStatus, tt.direction)))
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("status sort = %v, want %v", got, tt.want)
			}
		})
	}
	for _, tt := range []struct {
		name          string
		direction     serverapi.WorkflowTaskListSortDirection
		first, lastID string
	}{
		{name: "ascending", direction: serverapi.WorkflowTaskListSortDirectionAsc, first: string(backlog.ID), lastID: string(done.ID)},
		{name: "descending", direction: serverapi.WorkflowTaskListSortDirectionDesc, first: string(done.ID), lastID: string(backlog.ID)},
	} {
		t.Run("column "+tt.name, func(t *testing.T) {
			items := listAllPages(sortedRequest(serverapi.WorkflowTaskListSortFieldColumn, tt.direction))
			if items[0].TaskID != tt.first || items[len(items)-1].TaskID != tt.lastID {
				t.Fatalf("column sort = %+v, want %s before %s", items, tt.first, tt.lastID)
			}
		})
	}

	firstPageRequest := sortedRequest(serverapi.WorkflowTaskListSortFieldStatus, serverapi.WorkflowTaskListSortDirectionAsc)
	firstPageRequest.WorkflowID = &workflowIDString
	firstPage, err := view.tasks(t).List(
		ctx,
		firstPageRequest)

	if err != nil {
		t.Fatalf("ListTasks first page: %v", err)
	}
	if firstPage.NextPageToken == nil {
		t.Fatal("first page must produce a continuation token")
	}
	token, ok, err := parseWorkflowTaskListPageToken(*firstPage.NextPageToken)
	if err != nil || !ok {
		t.Fatalf("parse page token = %+v/%t/%v", token, ok, err)
	}
	invalidVersionToken := token
	invalidVersionToken.Version = 1
	changedWorkflowVersionToken := token
	changedWorkflowVersionInvariants := *token.Scope.Narrowed
	changedWorkflowVersionInvariants.WorkflowVersion++
	changedWorkflowVersionToken.Scope.Narrowed = &changedWorkflowVersionInvariants
	changedColumnStructureToken := token
	changedColumnStructureInvariants := *token.Scope.Narrowed
	changedColumnStructureInvariants.ColumnStructureHash = "changed"
	changedColumnStructureToken.Scope.Narrowed = &changedColumnStructureInvariants
	changedStatusModelToken := token
	changedStatusModelToken.StatusModelVersion++
	changedProjectToken := token
	changedProjectToken.Scope.ProjectID = "project-conflict"
	invalidRequests := []serverapi.WorkflowTaskListRequest{
		{ProjectID: baseRequest.ProjectID, LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone}, PageSize: baseRequest.PageSize, PageToken: workflowTaskListPageTokenForTest(t, invalidVersionToken)},
		{ProjectID: baseRequest.ProjectID, LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone}, PageSize: baseRequest.PageSize, PageToken: *firstPage.NextPageToken, StatusKinds: []serverapi.WorkflowTaskStatusKind{serverapi.WorkflowTaskStatusKindBacklog}},
		{ProjectID: baseRequest.ProjectID, LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone}, PageSize: baseRequest.PageSize, PageToken: *firstPage.NextPageToken, Sort: []serverapi.WorkflowTaskListSort{{Field: serverapi.WorkflowTaskListSortFieldColumn, Direction: serverapi.WorkflowTaskListSortDirectionAsc}}},
		{ProjectID: baseRequest.ProjectID, LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone}, PageSize: baseRequest.PageSize, PageToken: workflowTaskListPageTokenForTest(t, changedWorkflowVersionToken)},
		{ProjectID: baseRequest.ProjectID, LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone}, PageSize: baseRequest.PageSize, PageToken: workflowTaskListPageTokenForTest(t, changedColumnStructureToken)},
		{ProjectID: baseRequest.ProjectID, LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone}, PageSize: baseRequest.PageSize, PageToken: workflowTaskListPageTokenForTest(t, changedStatusModelToken)},
		{ProjectID: baseRequest.ProjectID, LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone}, PageSize: baseRequest.PageSize, PageToken: workflowTaskListPageTokenForTest(t, changedProjectToken)},
	}
	for _, request := range invalidRequests {
		_, err := view.tasks(t).List(ctx, request)
		if !errors.Is(err, ErrInvalidPageToken) {
			t.Fatalf("ListTasks invalid continuation %+v error = %v, want ErrInvalidPageToken", request, err)
		}
	}
}

func TestTaskListUsesTypedProjectWorkflowScope(t *testing.T) {
	ctx, _, workflowStore, binding, view := newWorkflowViewTestContextFixture(t)
	workflowID := createWorkflowViewValidWorkflow(t, ctx, workflowStore)
	if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, workflowID, true); err != nil {
		t.Fatalf("LinkWorkflow: %v", err)
	}
	projectID, selectedWorkflowID := binding.ProjectID, string(workflowID)
	response, err := view.tasks(t).List(ctx, serverapi.WorkflowTaskListRequest{
		LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone},
		ProjectID:   &projectID,
		WorkflowID:  &selectedWorkflowID,
	})

	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if response.Scope.ProjectID != projectID || response.Scope.WorkflowID == nil || *response.Scope.WorkflowID != selectedWorkflowID {
		t.Fatalf("response scope = %+v, want project/workflow scope", response.Scope)
	}
	if response.MatchingWorkflowCardinality != serverapi.WorkflowTaskListMatchingWorkflowCardinalityNone {
		t.Fatalf("matching workflow cardinality = %q, want none", response.MatchingWorkflowCardinality)
	}
}

func TestTaskListProjectScopeSpansLinkedWorkflowsWithoutColumns(t *testing.T) {
	ctx, _, workflowStore, binding, view := newWorkflowViewTestContextFixture(t)
	firstWorkflowID := createWorkflowViewValidWorkflow(t, ctx, workflowStore)
	secondWorkflowID := createWorkflowViewValidWorkflow(t, ctx, workflowStore)
	if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, firstWorkflowID, true); err != nil {
		t.Fatalf("LinkWorkflow first: %v", err)
	}
	if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, secondWorkflowID, false); err != nil {
		t.Fatalf("LinkWorkflow second: %v", err)
	}
	firstTask, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{
		ProjectID:  binding.ProjectID,
		WorkflowID: workflowIDPointerForTest(firstWorkflowID),
		Title:      "First",
		Body:       "Body",
	})
	if err != nil {
		t.Fatalf("CreateTask first: %v", err)
	}
	secondTask, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{
		ProjectID:  binding.ProjectID,
		WorkflowID: workflowIDPointerForTest(secondWorkflowID),
		Title:      "Second",
		Body:       "Body",
	})
	if err != nil {
		t.Fatalf("CreateTask second: %v", err)
	}

	projectID := binding.ProjectID
	projectWide, err := view.tasks(t).List(ctx, serverapi.WorkflowTaskListRequest{
		LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone}, ProjectID: &projectID})
	if err != nil {
		t.Fatalf("ListTasks project-wide: %v", err)
	}
	if projectWide.Scope.ProjectID != projectID || projectWide.Scope.WorkflowID != nil {
		t.Fatalf("project-wide scope = %+v, want project-only", projectWide.Scope)
	}
	if len(projectWide.Tasks) != 2 {
		t.Fatalf("project-wide tasks = %+v, want both linked workflows", projectWide.Tasks)
	}
	seen := map[string]bool{}
	for _, task := range projectWide.Tasks {
		seen[task.TaskID] = true
		if task.ColumnKeys != nil {
			t.Fatalf("project-wide task columns = %+v, want omitted", task.ColumnKeys)
		}
		if task.WorkflowName == nil || strings.TrimSpace(*task.WorkflowName) == "" {
			t.Fatalf("project-wide task = %+v, want workflow name", task)
		}
	}
	if !seen[string(firstTask.ID)] || !seen[string(secondTask.ID)] {
		t.Fatalf("project-wide task IDs = %+v, want %s and %s", seen, firstTask.ID, secondTask.ID)
	}

	selectedWorkflowID := string(secondWorkflowID)
	narrowed, err := view.tasks(t).List(ctx, serverapi.WorkflowTaskListRequest{
		LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone},
		ProjectID:   &projectID,
		WorkflowID:  &selectedWorkflowID,
	})

	if err != nil {
		t.Fatalf("ListTasks narrowed: %v", err)
	}
	if len(narrowed.Tasks) != 1 || narrowed.Tasks[0].TaskID != string(secondTask.ID) || narrowed.Tasks[0].ColumnKeys == nil || len(*narrowed.Tasks[0].ColumnKeys) == 0 {
		t.Fatalf("narrowed tasks = %+v, want second workflow with columns", narrowed.Tasks)
	}
}

func TestTaskListProjectScopeFiltersSortsAndBoundsAcrossLinkedWorkflows(t *testing.T) {
	ctx, store, workflowStore, binding, view := newWorkflowViewTestContextFixture(t)
	firstWorkflowID := createWorkflowViewValidWorkflow(t, ctx, workflowStore)
	secondWorkflowID := createWorkflowViewValidWorkflow(t, ctx, workflowStore)
	if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, firstWorkflowID, true); err != nil {
		t.Fatalf("LinkWorkflow first: %v", err)
	}
	if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, secondWorkflowID, false); err != nil {
		t.Fatalf("LinkWorkflow second: %v", err)
	}
	alpha, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{
		ProjectID:  binding.ProjectID,
		WorkflowID: workflowIDPointerForTest(firstWorkflowID),
		Title:      "Alpha",
		Body:       "Body",
	})
	if err != nil {
		t.Fatalf("CreateTask alpha: %v", err)
	}
	zulu, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{
		ProjectID:  binding.ProjectID,
		WorkflowID: workflowIDPointerForTest(secondWorkflowID),
		Title:      "Zulu",
		Body:       "Body",
	})
	if err != nil {
		t.Fatalf("CreateTask zulu: %v", err)
	}
	zuluStarted, err := workflowStore.StartTask(ctx, zulu.ID)
	if err != nil {
		t.Fatalf("StartTask zulu: %v", err)
	}
	zuluClaimed, err := workflowStore.ClaimRun(ctx, zuluStarted.RunID, 0)
	if err != nil {
		t.Fatalf("ClaimRun zulu: %v", err)
	}
	if err := workflowStore.SetRunWaitingAsk(ctx, zuluStarted.RunID, zuluClaimed.Generation, "ask-project-wide"); err != nil {
		t.Fatalf("SetRunWaitingAsk zulu: %v", err)
	}
	if _, err := store.DB().ExecContext(ctx, `
UPDATE tasks
SET created_at_unix_ms = CASE id WHEN ? THEN 100 ELSE 200 END,
    updated_at_unix_ms = CASE id WHEN ? THEN 300 ELSE 100 END
WHERE id IN (?, ?)`, string(alpha.ID), string(alpha.ID), string(alpha.ID), string(zulu.ID)); err != nil {
		t.Fatalf("set deterministic task times: %v", err)
	}

	projectID := binding.ProjectID
	list := func(request serverapi.WorkflowTaskListRequest) []serverapi.WorkflowTaskListItem {
		t.Helper()
		request.ProjectID = &projectID
		response, err := view.tasks(t).List(ctx, request)
		if err != nil {
			t.Fatalf("ListTasks %+v: %v", request, err)
		}
		for _, task := range response.Tasks {
			if task.ColumnKeys != nil {
				t.Fatalf("project-wide task columns = %+v, want omitted", task.ColumnKeys)
			}
		}
		return response.Tasks
	}
	statusFiltered := list(serverapi.WorkflowTaskListRequest{
		LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone},
		StatusKinds: []serverapi.WorkflowTaskStatusKind{serverapi.WorkflowTaskStatusKindBacklog},
	})
	if len(statusFiltered) != 1 || statusFiltered[0].TaskID != string(alpha.ID) {
		t.Fatalf("backlog project-wide tasks = %+v, want alpha", statusFiltered)
	}
	attentionFiltered := list(serverapi.WorkflowTaskListRequest{
		LabelFilter:    serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone},
		AttentionKinds: []serverapi.WorkflowTaskAttentionKind{serverapi.WorkflowTaskAttentionKindQuestion},
	})
	if len(attentionFiltered) != 1 || attentionFiltered[0].TaskID != string(zulu.ID) {
		t.Fatalf("question project-wide tasks = %+v, want zulu", attentionFiltered)
	}

	assertOrder := func(field serverapi.WorkflowTaskListSortField, direction serverapi.WorkflowTaskListSortDirection, want ...workflow.TaskID) {
		t.Helper()
		items := list(serverapi.WorkflowTaskListRequest{
			LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone},
			Sort:        []serverapi.WorkflowTaskListSort{{Field: field, Direction: direction}},
		})
		got := make([]string, 0, len(items))
		for _, item := range items {
			got = append(got, item.TaskID)
		}
		wantStrings := make([]string, 0, len(want))
		for _, taskID := range want {
			wantStrings = append(wantStrings, string(taskID))
		}
		if !reflect.DeepEqual(got, wantStrings) {
			t.Fatalf("%s %s order = %v, want %v", field, direction, got, wantStrings)
		}
	}
	assertOrder(serverapi.WorkflowTaskListSortFieldCreated, serverapi.WorkflowTaskListSortDirectionAsc, alpha.ID, zulu.ID)
	assertOrder(serverapi.WorkflowTaskListSortFieldUpdated, serverapi.WorkflowTaskListSortDirectionAsc, zulu.ID, alpha.ID)
	assertOrder(serverapi.WorkflowTaskListSortFieldRunCount, serverapi.WorkflowTaskListSortDirectionAsc, alpha.ID, zulu.ID)
	assertOrder(serverapi.WorkflowTaskListSortFieldTitle, serverapi.WorkflowTaskListSortDirectionAsc, alpha.ID, zulu.ID)

	statusAscending := list(serverapi.WorkflowTaskListRequest{
		LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone},
		Sort:        []serverapi.WorkflowTaskListSort{{Field: serverapi.WorkflowTaskListSortFieldStatus, Direction: serverapi.WorkflowTaskListSortDirectionAsc}},
	})
	statusDescending := list(serverapi.WorkflowTaskListRequest{
		LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone},
		Sort:        []serverapi.WorkflowTaskListSort{{Field: serverapi.WorkflowTaskListSortFieldStatus, Direction: serverapi.WorkflowTaskListSortDirectionDesc}},
	})
	if len(statusAscending) != 2 || len(statusDescending) != 2 ||
		statusAscending[0].TaskID != statusDescending[1].TaskID ||
		statusAscending[1].TaskID != statusDescending[0].TaskID {
		t.Fatalf("status orders = asc %+v desc %+v, want reversed tasks", statusAscending, statusDescending)
	}

	bounded := list(serverapi.WorkflowTaskListRequest{
		LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone}, PageSize: 1})
	if len(bounded) != 1 {
		t.Fatalf("bounded project-wide tasks = %+v, want one row", bounded)
	}
}
