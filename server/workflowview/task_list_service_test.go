package workflowview

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"core/internal/testharness/testsetup"
	"core/server/workflow"
	"core/server/workflowstore"
	"core/shared/serverapi"
)

func workflowIDPointerForTest(value workflow.WorkflowID) *workflow.WorkflowID {
	return &value
}

func TestTaskListQueriesFilterAndSortProjectedRowsThroughFocusedModule(t *testing.T) {
	ctx, metadataStore, workflowStore, binding := newWorkflowViewTestContextStore(t)
	workflowID := createWorkflowViewValidWorkflow(t, ctx, workflowStore)
	if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, workflowID, true); err != nil {
		t.Fatalf("LinkWorkflow: %v", err)
	}
	projector := NewTaskProjector()
	taskList, err := NewTaskList(metadataStore, projector)
	if err != nil {
		t.Fatalf("NewTaskList: %v", err)
	}
	definitions, err := NewDefinitionProjection(workflowStore)
	if err != nil {
		t.Fatalf("NewDefinitionProjection: %v", err)
	}
	snapshot, err := definitions.snapshot(ctx, string(workflowID))
	if err != nil {
		t.Fatalf("load definition: %v", err)
	}
	columns := boardColumns(snapshot)

	backlog, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{
		ProjectID: binding.ProjectID,
		Title:     "Zulu backlog",
		Body:      "Body",
	})
	if err != nil {
		t.Fatalf("CreateTask backlog: %v", err)
	}
	running, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{
		ProjectID: binding.ProjectID,
		Title:     "Alpha running",
		Body:      "Body",
	})
	if err != nil {
		t.Fatalf("CreateTask running: %v", err)
	}
	runningStarted, err := workflowStore.StartTask(ctx, running.ID)
	if err != nil {
		t.Fatalf("StartTask running: %v", err)
	}
	if _, err := workflowStore.ClaimRun(ctx, runningStarted.RunID, 0); err != nil {
		t.Fatalf("ClaimRun running: %v", err)
	}
	done, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{
		ProjectID: binding.ProjectID,
		Title:     "Mike done",
		Body:      "Body",
	})
	if err != nil {
		t.Fatalf("CreateTask done: %v", err)
	}
	doneStarted, err := workflowStore.StartTask(ctx, done.ID)
	if err != nil {
		t.Fatalf("StartTask done: %v", err)
	}
	if _, err := workflowStore.ClaimRun(ctx, doneStarted.RunID, 0); err != nil {
		t.Fatalf("ClaimRun done: %v", err)
	}
	if _, err := workflowStore.CompleteRun(ctx, workflowstore.CompleteRunRequest{RunID: doneStarted.RunID, TransitionID: "done"}); err != nil {
		t.Fatalf("CompleteRun done: %v", err)
	}

	narrowed := &workflowTaskListNarrowedQueryFacts{
		workflowID:             string(workflowID),
		canceledTerminalNodeID: canceledBoardTerminalNodeID(snapshot.api),
		columns:                columns,
	}
	query := func(narrowedFacts *workflowTaskListNarrowedQueryFacts, statusKinds []serverapi.WorkflowTaskStatusKind, attentionKinds []serverapi.WorkflowTaskAttentionKind, sortSelectors ...serverapi.WorkflowTaskListSort) []workflowTaskListRow {
		t.Helper()
		rows, err := taskList.queryRows(ctx, workflowTaskListQueryRequest{
			projectID:      binding.ProjectID,
			narrowed:       narrowedFacts,
			statusKinds:    statusKinds,
			attentionKinds: attentionKinds,
			sortSelectors:  sortSelectors,
			limit:          100,
		})
		if err != nil {
			t.Fatalf("queryRows: %v", err)
		}
		return rows
	}

	backlogFilter := *narrowed
	backlogFilter.columnKeys = []string{"backlog"}
	backlogRows := query(&backlogFilter, []serverapi.WorkflowTaskStatusKind{serverapi.WorkflowTaskStatusKindBacklog}, nil)
	if len(backlogRows) != 1 || backlogRows[0].item.TaskID != string(backlog.ID) {
		t.Fatalf("backlog filter rows = %+v", backlogRows)
	}
	if backlogRows[0].item.ColumnKeys == nil || !slices.Equal(*backlogRows[0].item.ColumnKeys, []string{"backlog"}) {
		t.Fatalf("backlog column facts = %+v", backlogRows[0].item.ColumnKeys)
	}
	if backlogRows[0].item.Status.Kind != serverapi.WorkflowTaskStatusKindBacklog ||
		backlogRows[0].item.Status.NativeState != serverapi.WorkflowTaskNativeStateActive {
		t.Fatalf("backlog status = %+v", backlogRows[0].item.Status)
	}
	if rows := query(narrowed, []serverapi.WorkflowTaskStatusKind{serverapi.WorkflowTaskStatusKindRunning}, []serverapi.WorkflowTaskAttentionKind{serverapi.WorkflowTaskAttentionKindApproval}); len(rows) != 0 {
		t.Fatalf("combined unmatched filters = %+v, want none", rows)
	}

	assertOrder := func(field serverapi.WorkflowTaskListSortField, want ...workflow.TaskID) {
		t.Helper()
		rows := query(narrowed, nil, nil, serverapi.WorkflowTaskListSort{
			Field:     field,
			Direction: serverapi.WorkflowTaskListSortDirectionAsc,
		})
		got := make([]string, 0, len(rows))
		for _, row := range rows {
			got = append(got, row.item.TaskID)
			if row.item.ColumnKeys == nil {
				t.Fatalf("%s row omitted narrowed column facts: %+v", field, row.item)
			}
		}
		wantIDs := make([]string, 0, len(want))
		for _, taskID := range want {
			wantIDs = append(wantIDs, string(taskID))
		}
		if !slices.Equal(got, wantIDs) {
			t.Fatalf("%s order = %v, want %v", field, got, wantIDs)
		}
	}
	runCountRows := query(
		narrowed,
		nil,
		nil,
		serverapi.WorkflowTaskListSort{Field: serverapi.WorkflowTaskListSortFieldRunCount, Direction: serverapi.WorkflowTaskListSortDirectionAsc},
		serverapi.WorkflowTaskListSort{Field: serverapi.WorkflowTaskListSortFieldTitle, Direction: serverapi.WorkflowTaskListSortDirectionAsc},
	)
	if got := []string{runCountRows[0].item.TaskID, runCountRows[1].item.TaskID, runCountRows[2].item.TaskID}; !slices.Equal(got, []string{string(backlog.ID), string(running.ID), string(done.ID)}) {
		t.Fatalf("run_count order = %v, want backlog then title-ordered one-run tasks", got)
	}
	assertOrder(serverapi.WorkflowTaskListSortFieldTitle, running.ID, done.ID, backlog.ID)
	assertOrder(serverapi.WorkflowTaskListSortFieldStatus, done.ID, running.ID, backlog.ID)
	assertOrder(serverapi.WorkflowTaskListSortFieldColumn, backlog.ID, running.ID, done.ID)

	projectWideRows := query(nil, nil, nil, serverapi.WorkflowTaskListSort{
		Field:     serverapi.WorkflowTaskListSortFieldTitle,
		Direction: serverapi.WorkflowTaskListSortDirectionAsc,
	})
	if len(projectWideRows) != 3 {
		t.Fatalf("project-wide rows = %+v", projectWideRows)
	}
	for _, row := range projectWideRows {
		if row.item.ColumnKeys != nil || row.columnRank != nil {
			t.Fatalf("project-wide row retained nullable column facts: %+v", row)
		}
		if row.item.WorkflowName == nil || strings.TrimSpace(*row.item.WorkflowName) == "" {
			t.Fatalf("project-wide row omitted workflow name: %+v", row.item)
		}
	}
}

func TestListTasksFiltersTypedStatusAndColumn(t *testing.T) {
	ctx, _, workflowStore, binding, view := newWorkflowViewTestContextService(t)
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
	backlogResp, err := view.ListTasks(ctx, serverapi.WorkflowTaskListRequest{
		ProjectID:   &projectID,
		WorkflowID:  &workflowIDString,
		ColumnKeys:  []string{"backlog"},
		StatusKinds: []serverapi.WorkflowTaskStatusKind{serverapi.WorkflowTaskStatusKindBacklog},
	}, testsetup.QuestionsEnabled("coder"))
	if err != nil {
		t.Fatalf("ListTasks backlog: %v", err)
	}
	if len(backlogResp.Tasks) != 1 || backlogResp.Tasks[0].TaskID != string(backlog.ID) {
		t.Fatalf("backlog tasks = %+v", backlogResp.Tasks)
	}

	runningResp, err := view.ListTasks(ctx, serverapi.WorkflowTaskListRequest{
		ProjectID:   &projectID,
		WorkflowID:  &workflowIDString,
		ColumnKeys:  []string{"agent"},
		StatusKinds: []serverapi.WorkflowTaskStatusKind{serverapi.WorkflowTaskStatusKindRunning},
	}, testsetup.QuestionsEnabled("coder"))
	if err != nil {
		t.Fatalf("ListTasks running: %v", err)
	}
	if len(runningResp.Tasks) != 1 || runningResp.Tasks[0].TaskID != string(running.ID) {
		t.Fatalf("running tasks = %+v", runningResp.Tasks)
	}
}

func TestListTasksStatusAndFiltersMatchCanonicalDetail(t *testing.T) {
	ctx, store, workflowStore, binding, view := newWorkflowViewTestContextService(t)
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
		response, err := view.ListTasks(ctx, request, testsetup.QuestionsEnabled("coder"))
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

	all := list(serverapi.WorkflowTaskListRequest{})
	if len(all.Tasks) != len(wantByStatus) {
		t.Fatalf("all list tasks = %+v, want %d tasks", all.Tasks, len(wantByStatus))
	}
	for _, item := range all.Tasks {
		detail, err := view.GetTask(ctx, item.TaskID)
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
		response := list(serverapi.WorkflowTaskListRequest{StatusKinds: []serverapi.WorkflowTaskStatusKind{kind}})
		if len(response.Tasks) != 1 || response.Tasks[0].TaskID != taskID || response.Tasks[0].Status.Kind != kind {
			t.Fatalf("status filter %q = %+v, want task %q", kind, response.Tasks, taskID)
		}
	}
	for attention, taskID := range map[serverapi.WorkflowTaskAttentionKind]string{
		serverapi.WorkflowTaskAttentionKindApproval:    string(approval.ID),
		serverapi.WorkflowTaskAttentionKindQuestion:    string(question.ID),
		serverapi.WorkflowTaskAttentionKindInterrupted: string(interrupted.ID),
	} {
		response := list(serverapi.WorkflowTaskListRequest{AttentionKinds: []serverapi.WorkflowTaskAttentionKind{attention}})
		if len(response.Tasks) != 1 || response.Tasks[0].TaskID != taskID {
			t.Fatalf("attention filter %q = %+v, want task %q", attention, response.Tasks, taskID)
		}
	}
	for columnKey, taskIDs := range map[string]map[string]bool{
		"backlog": {string(backlog.ID): true},
		"done":    {string(done.ID): true, string(canceled.ID): true},
	} {
		response := list(serverapi.WorkflowTaskListRequest{
			WorkflowID: &workflowIDString,
			ColumnKeys: []string{columnKey},
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
		StatusKinds:    []serverapi.WorkflowTaskStatusKind{serverapi.WorkflowTaskStatusKindWaitingApproval},
		AttentionKinds: []serverapi.WorkflowTaskAttentionKind{serverapi.WorkflowTaskAttentionKindApproval},
	})
	if len(approvalResponse.Tasks) != 1 || approvalResponse.Tasks[0].TaskID != string(approval.ID) || approvalResponse.Tasks[0].RunCount != 1 || slices.Contains(approvalResponse.Tasks[0].Status.RunIDs, string(approvalStarted.RunID)) || slices.Contains(approvalResponse.Tasks[0].Status.AttentionTypes, serverapi.WorkflowTaskAttentionKindInterrupted) {
		t.Fatalf("approval list item must preserve history count but exclude stale current facts: %+v", approvalResponse.Tasks[0])
	}
	for _, request := range []serverapi.WorkflowTaskListRequest{
		{StatusKinds: []serverapi.WorkflowTaskStatusKind{serverapi.WorkflowTaskStatusKindWaitingApproval}, AttentionKinds: []serverapi.WorkflowTaskAttentionKind{serverapi.WorkflowTaskAttentionKindQuestion}},
		{WorkflowID: &workflowIDString, StatusKinds: []serverapi.WorkflowTaskStatusKind{serverapi.WorkflowTaskStatusKindRunning}, ColumnKeys: []string{"backlog"}},
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
		if response := list(serverapi.WorkflowTaskListRequest{AttentionKinds: []serverapi.WorkflowTaskAttentionKind{attention}}); len(response.Tasks) != 0 {
			t.Fatalf("%s attention after cancellation = %+v, want none", attention, response.Tasks)
		}
	}

	for _, request := range []serverapi.WorkflowTaskListRequest{
		{StatusKinds: []serverapi.WorkflowTaskStatusKind{"invalid"}},
		{AttentionKinds: []serverapi.WorkflowTaskAttentionKind{"invalid"}},
		{WorkflowID: &workflowIDString, ColumnKeys: []string{"missing"}},
	} {
		request.ProjectID = &projectID
		_, err := view.ListTasks(ctx, request, testsetup.QuestionsEnabled("coder"))
		var validationErr serverapi.WorkflowRequestValidationError
		if !errors.As(err, &validationErr) {
			t.Fatalf("ListTasks invalid request %+v error = %v, want validation error", request, err)
		}
	}
}

func TestListTasksSortAndCursorPagination(t *testing.T) {
	ctx, store, workflowStore, binding, view := newWorkflowViewTestContextService(t)
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
	baseRequest := serverapi.WorkflowTaskListRequest{ProjectID: &projectID, PageSize: 2}
	listAllPages := func(request serverapi.WorkflowTaskListRequest) []serverapi.WorkflowTaskListItem {
		t.Helper()
		items := make([]serverapi.WorkflowTaskListItem, 0, 5)
		seen := map[string]bool{}
		for {
			response, err := view.ListTasks(ctx, request, testsetup.QuestionsEnabled("coder"))
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
			ProjectID: baseRequest.ProjectID,
			PageSize:  baseRequest.PageSize,
			Sort:      []serverapi.WorkflowTaskListSort{{Field: field, Direction: direction}},
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
	firstPage, err := view.ListTasks(
		ctx,
		firstPageRequest,
		testsetup.QuestionsEnabled("coder"),
	)
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
		{ProjectID: baseRequest.ProjectID, PageSize: baseRequest.PageSize, PageToken: workflowTaskListPageTokenForTest(t, invalidVersionToken)},
		{ProjectID: baseRequest.ProjectID, PageSize: baseRequest.PageSize, PageToken: *firstPage.NextPageToken, StatusKinds: []serverapi.WorkflowTaskStatusKind{serverapi.WorkflowTaskStatusKindBacklog}},
		{ProjectID: baseRequest.ProjectID, PageSize: baseRequest.PageSize, PageToken: *firstPage.NextPageToken, Sort: []serverapi.WorkflowTaskListSort{{Field: serverapi.WorkflowTaskListSortFieldColumn, Direction: serverapi.WorkflowTaskListSortDirectionAsc}}},
		{ProjectID: baseRequest.ProjectID, PageSize: baseRequest.PageSize, PageToken: workflowTaskListPageTokenForTest(t, changedWorkflowVersionToken)},
		{ProjectID: baseRequest.ProjectID, PageSize: baseRequest.PageSize, PageToken: workflowTaskListPageTokenForTest(t, changedColumnStructureToken)},
		{ProjectID: baseRequest.ProjectID, PageSize: baseRequest.PageSize, PageToken: workflowTaskListPageTokenForTest(t, changedStatusModelToken)},
		{ProjectID: baseRequest.ProjectID, PageSize: baseRequest.PageSize, PageToken: workflowTaskListPageTokenForTest(t, changedProjectToken)},
	}
	for _, request := range invalidRequests {
		_, err := view.ListTasks(ctx, request, testsetup.QuestionsEnabled("coder"))
		if !errors.Is(err, ErrInvalidPageToken) {
			t.Fatalf("ListTasks invalid continuation %+v error = %v, want ErrInvalidPageToken", request, err)
		}
	}
}

func TestTaskListUsesTypedProjectWorkflowScope(t *testing.T) {
	ctx, _, workflowStore, binding, view := newWorkflowViewTestContextService(t)
	workflowID := createWorkflowViewValidWorkflow(t, ctx, workflowStore)
	if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, workflowID, true); err != nil {
		t.Fatalf("LinkWorkflow: %v", err)
	}
	projectID, selectedWorkflowID := binding.ProjectID, string(workflowID)
	response, err := view.ListTasks(ctx, serverapi.WorkflowTaskListRequest{
		ProjectID:  &projectID,
		WorkflowID: &selectedWorkflowID,
	}, testsetup.QuestionsEnabled("coder"))
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
	ctx, _, workflowStore, binding, view := newWorkflowViewTestContextService(t)
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
	projectWide, err := view.ListTasks(ctx, serverapi.WorkflowTaskListRequest{ProjectID: &projectID}, nil)
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
	narrowed, err := view.ListTasks(ctx, serverapi.WorkflowTaskListRequest{
		ProjectID:  &projectID,
		WorkflowID: &selectedWorkflowID,
	}, testsetup.QuestionsEnabled("coder"))
	if err != nil {
		t.Fatalf("ListTasks narrowed: %v", err)
	}
	if len(narrowed.Tasks) != 1 || narrowed.Tasks[0].TaskID != string(secondTask.ID) || narrowed.Tasks[0].ColumnKeys == nil || len(*narrowed.Tasks[0].ColumnKeys) == 0 {
		t.Fatalf("narrowed tasks = %+v, want second workflow with columns", narrowed.Tasks)
	}
}

func TestTaskListProjectScopeFiltersSortsAndBoundsAcrossLinkedWorkflows(t *testing.T) {
	ctx, store, workflowStore, binding, view := newWorkflowViewTestContextService(t)
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
		response, err := view.ListTasks(ctx, request, nil)
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
		StatusKinds: []serverapi.WorkflowTaskStatusKind{serverapi.WorkflowTaskStatusKindBacklog},
	})
	if len(statusFiltered) != 1 || statusFiltered[0].TaskID != string(alpha.ID) {
		t.Fatalf("backlog project-wide tasks = %+v, want alpha", statusFiltered)
	}
	attentionFiltered := list(serverapi.WorkflowTaskListRequest{
		AttentionKinds: []serverapi.WorkflowTaskAttentionKind{serverapi.WorkflowTaskAttentionKindQuestion},
	})
	if len(attentionFiltered) != 1 || attentionFiltered[0].TaskID != string(zulu.ID) {
		t.Fatalf("question project-wide tasks = %+v, want zulu", attentionFiltered)
	}

	assertOrder := func(field serverapi.WorkflowTaskListSortField, direction serverapi.WorkflowTaskListSortDirection, want ...workflow.TaskID) {
		t.Helper()
		items := list(serverapi.WorkflowTaskListRequest{
			Sort: []serverapi.WorkflowTaskListSort{{Field: field, Direction: direction}},
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
		Sort: []serverapi.WorkflowTaskListSort{{Field: serverapi.WorkflowTaskListSortFieldStatus, Direction: serverapi.WorkflowTaskListSortDirectionAsc}},
	})
	statusDescending := list(serverapi.WorkflowTaskListRequest{
		Sort: []serverapi.WorkflowTaskListSort{{Field: serverapi.WorkflowTaskListSortFieldStatus, Direction: serverapi.WorkflowTaskListSortDirectionDesc}},
	})
	if len(statusAscending) != 2 || len(statusDescending) != 2 ||
		statusAscending[0].TaskID != statusDescending[1].TaskID ||
		statusAscending[1].TaskID != statusDescending[0].TaskID {
		t.Fatalf("status orders = asc %+v desc %+v, want reversed tasks", statusAscending, statusDescending)
	}

	bounded := list(serverapi.WorkflowTaskListRequest{PageSize: 1})
	if len(bounded) != 1 {
		t.Fatalf("bounded project-wide tasks = %+v, want one row", bounded)
	}
}

func TestTaskListProjectScopeFailuresAreTyped(t *testing.T) {
	t.Run("no linked workflows", func(t *testing.T) {
		ctx, _, _, binding, view := newWorkflowViewTestContextService(t)
		projectID := binding.ProjectID
		_, err := view.ListTasks(ctx, serverapi.WorkflowTaskListRequest{ProjectID: &projectID}, nil)
		var scopeErr *serverapi.WorkflowTaskListScopeError
		if !errors.As(err, &scopeErr) ||
			scopeErr.Reason != serverapi.WorkflowTaskListScopeReasonNoLinkedWorkflows ||
			scopeErr.ProjectID == nil || *scopeErr.ProjectID != projectID ||
			scopeErr.WorkflowID != nil {
			t.Fatalf("ListTasks error = %+v, want typed no-links error for %q", err, projectID)
		}
	})

	t.Run("explicit workflow is not linked", func(t *testing.T) {
		ctx, _, workflowStore, binding, view := newWorkflowViewTestContextService(t)
		linkedWorkflowID := createWorkflowViewValidWorkflow(t, ctx, workflowStore)
		unlinkedWorkflowID := createWorkflowViewValidWorkflow(t, ctx, workflowStore)
		if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, linkedWorkflowID, true); err != nil {
			t.Fatalf("LinkWorkflow: %v", err)
		}
		projectID, workflowID := binding.ProjectID, string(unlinkedWorkflowID)
		_, err := view.ListTasks(ctx, serverapi.WorkflowTaskListRequest{
			ProjectID:  &projectID,
			WorkflowID: &workflowID,
		}, nil)
		var scopeErr *serverapi.WorkflowTaskListScopeError
		if !errors.As(err, &scopeErr) ||
			scopeErr.Reason != serverapi.WorkflowTaskListScopeReasonWorkflowNotLinked ||
			scopeErr.ProjectID == nil || *scopeErr.ProjectID != projectID ||
			scopeErr.WorkflowID == nil || *scopeErr.WorkflowID != workflowID {
			t.Fatalf("ListTasks error = %+v, want typed not-linked error for %q/%q", err, projectID, workflowID)
		}
	})

	t.Run("column operation requires workflow", func(t *testing.T) {
		ctx, _, workflowStore, binding, view := newWorkflowViewTestContextService(t)
		workflowID := createWorkflowViewValidWorkflow(t, ctx, workflowStore)
		if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, workflowID, true); err != nil {
			t.Fatalf("LinkWorkflow: %v", err)
		}
		projectID := binding.ProjectID
		_, err := view.ListTasks(ctx, serverapi.WorkflowTaskListRequest{
			ProjectID:  &projectID,
			ColumnKeys: []string{"backlog"},
		}, nil)
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
		ctx, _, workflowStore, binding, view := newWorkflowViewTestContextService(t)
		createTwoLinkedWorkflowViewWorkflows(t, ctx, workflowStore, binding.ProjectID)
		projectID := binding.ProjectID
		response, err := view.ListTasks(ctx, serverapi.WorkflowTaskListRequest{ProjectID: &projectID}, nil)
		if err != nil {
			t.Fatalf("ListTasks: %v", err)
		}
		if response.MatchingWorkflowCardinality != serverapi.WorkflowTaskListMatchingWorkflowCardinalityNone || len(response.Tasks) != 0 {
			t.Fatalf("empty filtered response = %+v, want none", response)
		}
	})

	t.Run("one after filtering", func(t *testing.T) {
		ctx, _, workflowStore, binding, view := newWorkflowViewTestContextService(t)
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
		response, err := view.ListTasks(ctx, serverapi.WorkflowTaskListRequest{
			ProjectID:   &projectID,
			StatusKinds: []serverapi.WorkflowTaskStatusKind{serverapi.WorkflowTaskStatusKindBacklog},
		}, nil)
		if err != nil {
			t.Fatalf("ListTasks: %v", err)
		}
		if response.MatchingWorkflowCardinality != serverapi.WorkflowTaskListMatchingWorkflowCardinalityOne || len(response.Tasks) != 1 || response.Tasks[0].WorkflowID != string(firstWorkflowID) {
			t.Fatalf("filtered response = %+v, want one matching workflow", response)
		}
	})

	t.Run("multiple beyond first page", func(t *testing.T) {
		ctx, _, workflowStore, binding, view := newWorkflowViewTestContextService(t)
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
		response, err := view.ListTasks(ctx, serverapi.WorkflowTaskListRequest{
			ProjectID: &projectID,
			PageSize:  1,
			Sort:      []serverapi.WorkflowTaskListSort{{Field: serverapi.WorkflowTaskListSortFieldTitle, Direction: serverapi.WorkflowTaskListSortDirectionAsc}},
		}, nil)
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
		ctx, _, workflowStore, binding, view := newWorkflowViewTestContextService(t)
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
		firstPage, err := view.ListTasks(ctx, serverapi.WorkflowTaskListRequest{
			ProjectID: &projectID,
			PageSize:  1,
			Sort:      sortByTitle,
		}, nil)
		if err != nil {
			t.Fatalf("ListTasks first page: %v", err)
		}
		if firstPage.MatchingWorkflowCardinality != serverapi.WorkflowTaskListMatchingWorkflowCardinalityOne || firstPage.NextPageToken == nil {
			t.Fatalf("first page = %+v, want one and continuation", firstPage)
		}
		if _, err := view.ListTasks(ctx, serverapi.WorkflowTaskListRequest{
			PageToken:   *firstPage.NextPageToken,
			StatusKinds: []serverapi.WorkflowTaskStatusKind{serverapi.WorkflowTaskStatusKindDone},
			Sort:        sortByTitle,
		}, nil); !errors.Is(err, ErrInvalidPageToken) {
			t.Fatalf("conflicting continuation error = %v, want ErrInvalidPageToken", err)
		}
		selectedWorkflowID := string(secondWorkflowID)
		if _, err := view.ListTasks(ctx, serverapi.WorkflowTaskListRequest{
			WorkflowID: &selectedWorkflowID,
			PageToken:  *firstPage.NextPageToken,
			Sort:       sortByTitle,
		}, nil); !errors.Is(err, ErrInvalidPageToken) {
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
		nextPage, err := view.ListTasks(ctx, serverapi.WorkflowTaskListRequest{
			PageToken: *firstPage.NextPageToken,
			PageSize:  10,
			Sort:      sortByTitle,
		}, nil)
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
		ctx, _, workflowStore, binding, view := newWorkflowViewTestContextService(t)
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
		firstPage, err := view.ListTasks(ctx, serverapi.WorkflowTaskListRequest{
			ProjectID:   &projectID,
			StatusKinds: statusFilter,
			PageSize:    1,
			Sort:        sortByTitle,
		}, nil)
		if err != nil {
			t.Fatalf("ListTasks first page: %v", err)
		}
		if firstPage.MatchingWorkflowCardinality != serverapi.WorkflowTaskListMatchingWorkflowCardinalityMultiple || firstPage.NextPageToken == nil {
			t.Fatalf("first page = %+v, want multiple and continuation", firstPage)
		}
		if _, err := workflowStore.StartTask(ctx, zulu.ID); err != nil {
			t.Fatalf("StartTask Zulu: %v", err)
		}
		nextPage, err := view.ListTasks(ctx, serverapi.WorkflowTaskListRequest{
			StatusKinds: statusFilter,
			PageToken:   *firstPage.NextPageToken,
			PageSize:    10,
			Sort:        sortByTitle,
		}, nil)
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

func TestWorkflowTaskListPageTokenUsesTypedModeInvariants(t *testing.T) {
	const tokenWorkflowID = "workflow-7e8d24d2-8a98-4dcf-a197-6214db1cb3c0"
	base := workflowTaskListPageTokenPayload{
		Version:                     workflowTaskListPageTokenVersion,
		MatchingWorkflowCardinality: serverapi.WorkflowTaskListMatchingWorkflowCardinalityMultiple,
		StatusModelVersion:          workflowTaskStatusModelVersion,
		Fingerprint:                 "fingerprint",
		Cursor:                      workflowTaskListCursor{TaskID: "task-1"},
	}
	projectWide := base
	projectWide.Scope = workflowTaskListPageTokenScope{
		ProjectID:   "project-1",
		ProjectWide: &workflowTaskListProjectWidePageTokenInvariants{},
	}
	parsed, ok, err := parseWorkflowTaskListPageToken(workflowTaskListPageTokenForTest(t, projectWide))
	if err != nil || !ok || parsed.Scope.ProjectWide == nil || parsed.Scope.Narrowed != nil {
		t.Fatalf("parse project-wide token = %+v/%t/%v", parsed, ok, err)
	}
	raw, err := json.Marshal(projectWide)
	if err != nil {
		t.Fatalf("marshal project-wide token: %v", err)
	}
	var envelope struct {
		Scope map[string]json.RawMessage `json:"scope"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("decode project-wide token shape: %v", err)
	}
	if _, exists := envelope.Scope["narrowed"]; exists {
		t.Fatalf("project-wide token scope = %s, want no narrowed invariant block", raw)
	}

	narrowed := base
	narrowed.MatchingWorkflowCardinality = serverapi.WorkflowTaskListMatchingWorkflowCardinalityOne
	narrowed.Scope = workflowTaskListPageTokenScope{
		ProjectID: "project-1",
		Narrowed: &workflowTaskListNarrowedPageTokenInvariants{
			WorkflowID:          tokenWorkflowID,
			WorkflowVersion:     2,
			ColumnStructureHash: "columns",
		},
	}
	parsed, ok, err = parseWorkflowTaskListPageToken(workflowTaskListPageTokenForTest(t, narrowed))
	if err != nil || !ok || parsed.Scope.ProjectWide != nil || parsed.Scope.Narrowed == nil {
		t.Fatalf("parse narrowed token = %+v/%t/%v", parsed, ok, err)
	}
	raw, err = json.Marshal(narrowed)
	if err != nil {
		t.Fatalf("marshal narrowed token: %v", err)
	}
	envelope = struct {
		Scope map[string]json.RawMessage `json:"scope"`
	}{}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("decode narrowed token shape: %v", err)
	}
	if _, exists := envelope.Scope["project_wide"]; exists {
		t.Fatalf("narrowed token scope = %s, want no project-wide invariant block", raw)
	}
}

func TestWorkflowTaskListPageTokenRejectsMalformedModeAndCardinality(t *testing.T) {
	const tokenWorkflowID = "workflow-7e8d24d2-8a98-4dcf-a197-6214db1cb3c0"
	valid := workflowTaskListPageTokenPayload{
		Version: workflowTaskListPageTokenVersion,
		Scope: workflowTaskListPageTokenScope{
			ProjectID:   "project-1",
			ProjectWide: &workflowTaskListProjectWidePageTokenInvariants{},
		},
		MatchingWorkflowCardinality: serverapi.WorkflowTaskListMatchingWorkflowCardinalityOne,
		StatusModelVersion:          workflowTaskStatusModelVersion,
		Fingerprint:                 "fingerprint",
		Cursor:                      workflowTaskListCursor{TaskID: "task-1"},
	}
	neitherMode := valid
	neitherMode.Scope.ProjectWide = nil
	bothModes := valid
	bothModes.Scope.Narrowed = &workflowTaskListNarrowedPageTokenInvariants{
		WorkflowID:          tokenWorkflowID,
		WorkflowVersion:     1,
		ColumnStructureHash: "columns",
	}
	invalidCardinality := valid
	invalidCardinality.MatchingWorkflowCardinality = serverapi.WorkflowTaskListMatchingWorkflowCardinalityNone
	missingWorkflowID := valid
	missingWorkflowID.Scope.ProjectWide = nil
	missingWorkflowID.Scope.Narrowed = &workflowTaskListNarrowedPageTokenInvariants{
		WorkflowVersion:     1,
		ColumnStructureHash: "columns",
	}
	missingWorkflowVersion := valid
	missingWorkflowVersion.Scope.ProjectWide = nil
	missingWorkflowVersion.Scope.Narrowed = &workflowTaskListNarrowedPageTokenInvariants{
		WorkflowID:          tokenWorkflowID,
		ColumnStructureHash: "columns",
	}
	missingColumnStructure := valid
	missingColumnStructure.Scope.ProjectWide = nil
	missingColumnStructure.Scope.Narrowed = &workflowTaskListNarrowedPageTokenInvariants{
		WorkflowID:      tokenWorkflowID,
		WorkflowVersion: 1,
	}
	paddedProjectID := valid
	paddedProjectID.Scope.ProjectID = " project-1"
	malformedWorkflowID := valid
	malformedWorkflowID.Scope.ProjectWide = nil
	malformedWorkflowID.Scope.Narrowed = &workflowTaskListNarrowedPageTokenInvariants{
		WorkflowID:          "workflow-1",
		WorkflowVersion:     1,
		ColumnStructureHash: "columns",
	}
	for name, payload := range map[string]workflowTaskListPageTokenPayload{
		"neither mode":             neitherMode,
		"both modes":               bothModes,
		"none cardinality":         invalidCardinality,
		"missing workflow id":      missingWorkflowID,
		"missing workflow version": missingWorkflowVersion,
		"missing column structure": missingColumnStructure,
		"padded project id":        paddedProjectID,
		"malformed workflow id":    malformedWorkflowID,
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := parseWorkflowTaskListPageToken(workflowTaskListPageTokenForTest(t, payload)); !errors.Is(err, ErrInvalidPageToken) {
				t.Fatalf("parse malformed token error = %v, want ErrInvalidPageToken", err)
			}
		})
	}
}

func workflowTaskListPageTokenForTest(t *testing.T, payload workflowTaskListPageTokenPayload) string {
	t.Helper()
	token, err := workflowTaskListPageToken(payload)
	if err != nil {
		t.Fatalf("workflowTaskListPageToken: %v", err)
	}
	return token
}

func TestWorkflowTaskListRequestFingerprintCanonicalizesSetFilters(t *testing.T) {
	sortSelectors := []serverapi.WorkflowTaskListSort{
		{Field: serverapi.WorkflowTaskListSortFieldStatus, Direction: serverapi.WorkflowTaskListSortDirectionAsc},
	}
	first, err := workflowTaskListRequestFingerprint(serverapi.WorkflowTaskListRequest{
		ColumnKeys:     []string{"done", "backlog", "done"},
		StatusKinds:    []serverapi.WorkflowTaskStatusKind{serverapi.WorkflowTaskStatusKindRunning, serverapi.WorkflowTaskStatusKindBacklog, serverapi.WorkflowTaskStatusKindRunning},
		AttentionKinds: []serverapi.WorkflowTaskAttentionKind{serverapi.WorkflowTaskAttentionKindQuestion, serverapi.WorkflowTaskAttentionKindApproval},
	}, sortSelectors, workflowTaskListFingerprintScope{
		Narrowed: &workflowTaskListNarrowedFingerprintInvariants{ColumnStructureHash: "columns"},
	})
	if err != nil {
		t.Fatalf("first fingerprint: %v", err)
	}
	second, err := workflowTaskListRequestFingerprint(serverapi.WorkflowTaskListRequest{
		ColumnKeys:     []string{"backlog", "done"},
		StatusKinds:    []serverapi.WorkflowTaskStatusKind{serverapi.WorkflowTaskStatusKindBacklog, serverapi.WorkflowTaskStatusKindRunning},
		AttentionKinds: []serverapi.WorkflowTaskAttentionKind{serverapi.WorkflowTaskAttentionKindApproval, serverapi.WorkflowTaskAttentionKindQuestion},
	}, sortSelectors, workflowTaskListFingerprintScope{
		Narrowed: &workflowTaskListNarrowedFingerprintInvariants{ColumnStructureHash: "columns"},
	})
	if err != nil {
		t.Fatalf("second fingerprint: %v", err)
	}
	if first != second {
		t.Fatalf("canonical fingerprints differ: %q != %q", first, second)
	}
}

func TestWorkflowTaskListRequestFingerprintRequiresOneTypedScope(t *testing.T) {
	request := serverapi.WorkflowTaskListRequest{}
	sortSelectors := normalizeWorkflowTaskListSort(nil)
	for name, scope := range map[string]workflowTaskListFingerprintScope{
		"missing mode": {},
		"both modes": {
			ProjectWide: &workflowTaskListProjectWideFingerprintInvariants{},
			Narrowed:    &workflowTaskListNarrowedFingerprintInvariants{ColumnStructureHash: "columns"},
		},
		"blank narrowed hash": {
			Narrowed: &workflowTaskListNarrowedFingerprintInvariants{},
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := workflowTaskListRequestFingerprint(request, sortSelectors, scope); err == nil {
				t.Fatalf("workflowTaskListRequestFingerprint accepted %s", name)
			}
		})
	}
}

func TestCanceledBoardTerminalNodeIDUsesTypedAbsence(t *testing.T) {
	if got := canceledBoardTerminalNodeID(serverapi.WorkflowDefinition{}); got != nil {
		t.Fatalf("canceledBoardTerminalNodeID without terminal = %v, want nil", got)
	}
	def := serverapi.WorkflowDefinition{Nodes: []serverapi.WorkflowNode{
		{ID: "node-terminal", Key: "archive", Kind: string(workflow.NodeKindTerminal)},
		{ID: "node-done", Key: "done", Kind: string(workflow.NodeKindTerminal)},
	}}
	got := canceledBoardTerminalNodeID(def)
	if got == nil || *got != "node-done" {
		t.Fatalf("canceledBoardTerminalNodeID = %v, want done terminal", got)
	}
}

func TestTaskListInfersScopeFromContinuationToken(t *testing.T) {
	ctx, _, workflowStore, binding, view := newWorkflowViewTestContextService(t)
	workflowID := createWorkflowViewValidWorkflow(t, ctx, workflowStore)
	if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, workflowID, true); err != nil {
		t.Fatalf("LinkWorkflow: %v", err)
	}
	for index := 0; index < 2; index++ {
		if _, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{ProjectID: binding.ProjectID, Title: "Task", Body: "Body"}); err != nil {
			t.Fatalf("CreateTask %d: %v", index, err)
		}
	}
	projectID, workflowIDValue := binding.ProjectID, string(workflowID)
	firstPage, err := view.ListTasks(ctx, serverapi.WorkflowTaskListRequest{ProjectID: &projectID, WorkflowID: &workflowIDValue, PageSize: 1}, testsetup.QuestionsEnabled("coder"))
	if err != nil {
		t.Fatalf("ListTasks first page: %v", err)
	}
	if firstPage.NextPageToken == nil {
		t.Fatal("expected continuation token")
	}
	nextPage, err := view.ListTasks(ctx, serverapi.WorkflowTaskListRequest{PageToken: *firstPage.NextPageToken, PageSize: 1}, testsetup.QuestionsEnabled("coder"))
	if err != nil {
		t.Fatalf("ListTasks token-only continuation: %v", err)
	}
	if nextPage.Scope.ProjectID != binding.ProjectID || nextPage.Scope.WorkflowID == nil || *nextPage.Scope.WorkflowID != string(workflowID) || len(nextPage.Tasks) != 1 || nextPage.Tasks[0].TaskID == firstPage.Tasks[0].TaskID {
		t.Fatalf("token-only continuation = %+v, want resolved second page", nextPage)
	}
	otherProjectID := "project-conflict"
	_, err = view.ListTasks(ctx, serverapi.WorkflowTaskListRequest{ProjectID: &otherProjectID, PageToken: *firstPage.NextPageToken, PageSize: 1}, testsetup.QuestionsEnabled("coder"))
	if !errors.Is(err, ErrInvalidPageToken) {
		t.Fatalf("ListTasks conflicting token scope error = %v, want ErrInvalidPageToken", err)
	}
}

func createTwoLinkedWorkflowViewWorkflows(t *testing.T, ctx context.Context, store *workflowstore.Store, projectID string) (workflow.WorkflowID, workflow.WorkflowID) {
	t.Helper()
	firstWorkflowID := createWorkflowViewValidWorkflow(t, ctx, store)
	secondWorkflowID := createWorkflowViewValidWorkflow(t, ctx, store)
	if _, err := store.LinkWorkflow(ctx, projectID, firstWorkflowID, true); err != nil {
		t.Fatalf("LinkWorkflow first: %v", err)
	}
	if _, err := store.LinkWorkflow(ctx, projectID, secondWorkflowID, false); err != nil {
		t.Fatalf("LinkWorkflow second: %v", err)
	}
	return firstWorkflowID, secondWorkflowID
}

func scopeIDSet(values []string) map[string]bool {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]bool, len(values))
	for _, value := range values {
		out[value] = true
	}
	return out
}

func taskStatusKinds(items []serverapi.WorkflowTaskListItem) []serverapi.WorkflowTaskStatusKind {
	kinds := make([]serverapi.WorkflowTaskStatusKind, 0, len(items))
	for _, item := range items {
		kinds = append(kinds, item.Status.Kind)
	}
	return kinds
}
