package workflowview

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"core/server/metadata"
	"core/server/workflow"
	"core/server/workflowstore"
	"core/shared/serverapi"
)

func TestListTasksUsesCanonicalStatusProjection(t *testing.T) {
	ctx, store, workflowStore, binding, view := newWorkflowViewTestContextService(t)
	workflowID := createWorkflowViewValidWorkflow(t, ctx, workflowStore)
	if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, workflowID, true); err != nil {
		t.Fatalf("LinkWorkflow: %v", err)
	}
	requireDoneTransitionApproval(t, ctx, store, workflowID)
	task, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{ProjectID: binding.ProjectID, Title: "Approval", Body: "Body"})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	started, err := workflowStore.StartTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	if _, err := workflowStore.CompleteRun(ctx, workflowstore.CompleteRunRequest{RunID: started.RunID, TransitionID: "done"}); err != nil {
		t.Fatalf("CompleteRun: %v", err)
	}
	if _, err := store.DB().ExecContext(ctx, `UPDATE task_runs SET completed_at_unix_ms = NULL, interrupted_at_unix_ms = 1 WHERE id = ?`, string(started.RunID)); err != nil {
		t.Fatalf("create stale historical run fixture: %v", err)
	}

	projectID := binding.ProjectID
	resp, err := view.ListTasks(ctx, serverapi.WorkflowTaskListRequest{
		ProjectID:      &projectID,
		StatusKinds:    []serverapi.WorkflowTaskStatusKind{serverapi.WorkflowTaskStatusKindWaitingApproval},
		AttentionKinds: []serverapi.WorkflowTaskAttentionKind{serverapi.WorkflowTaskAttentionKindApproval},
	}, workflow.StaticRoleResolver{"coder": true})
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if len(resp.Tasks) != 1 || resp.Tasks[0].TaskID != string(task.ID) {
		t.Fatalf("list tasks = %+v", resp.Tasks)
	}
	detail, err := view.GetTask(ctx, string(task.ID))
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if !reflect.DeepEqual(resp.Tasks[0].Status, detail.Status) || containsString(resp.Tasks[0].Status.RunIDs, string(started.RunID)) {
		t.Fatalf("list/detail status = %+v/%+v", resp.Tasks[0].Status, detail.Status)
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

	projectID := binding.ProjectID
	backlogResp, err := view.ListTasks(ctx, serverapi.WorkflowTaskListRequest{
		ProjectID:   &projectID,
		ColumnKeys:  []string{"backlog"},
		StatusKinds: []serverapi.WorkflowTaskStatusKind{serverapi.WorkflowTaskStatusKindBacklog},
	}, workflow.StaticRoleResolver{"coder": true})
	if err != nil {
		t.Fatalf("ListTasks backlog: %v", err)
	}
	if len(backlogResp.Tasks) != 1 || backlogResp.Tasks[0].TaskID != string(backlog.ID) {
		t.Fatalf("backlog tasks = %+v", backlogResp.Tasks)
	}

	runningResp, err := view.ListTasks(ctx, serverapi.WorkflowTaskListRequest{
		ProjectID:   &projectID,
		ColumnKeys:  []string{"agent"},
		StatusKinds: []serverapi.WorkflowTaskStatusKind{serverapi.WorkflowTaskStatusKindRunning},
	}, workflow.StaticRoleResolver{"coder": true})
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
	if err := workflowStore.CancelTask(ctx, canceled.ID, "stop"); err != nil {
		t.Fatalf("CancelTask: %v", err)
	}

	projectID := binding.ProjectID
	list := func(request serverapi.WorkflowTaskListRequest) serverapi.WorkflowTaskListResponse {
		t.Helper()
		request.ProjectID = &projectID
		response, err := view.ListTasks(ctx, request, workflow.StaticRoleResolver{"coder": true})
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
		response := list(serverapi.WorkflowTaskListRequest{ColumnKeys: []string{columnKey}})
		if len(response.Tasks) != len(taskIDs) {
			t.Fatalf("column filter %q = %+v, want task IDs %v", columnKey, response.Tasks, taskIDs)
		}
		for _, item := range response.Tasks {
			if !taskIDs[item.TaskID] {
				t.Fatalf("column filter %q = %+v, want task IDs %v", columnKey, response.Tasks, taskIDs)
			}
		}
	}
	approvalResponse := list(serverapi.WorkflowTaskListRequest{StatusKinds: []serverapi.WorkflowTaskStatusKind{serverapi.WorkflowTaskStatusKindWaitingApproval}})
	if approvalResponse.Tasks[0].RunCount != 1 || containsString(approvalResponse.Tasks[0].Status.RunIDs, string(approvalStarted.RunID)) || containsString(workflowTaskAttentionStrings(approvalResponse.Tasks[0].Status.AttentionTypes), string(serverapi.WorkflowTaskAttentionKindInterrupted)) {
		t.Fatalf("approval list item must preserve history count but exclude stale current facts: %+v", approvalResponse.Tasks[0])
	}

	for _, request := range []serverapi.WorkflowTaskListRequest{
		{StatusKinds: []serverapi.WorkflowTaskStatusKind{"invalid"}},
		{AttentionKinds: []serverapi.WorkflowTaskAttentionKind{"invalid"}},
		{ColumnKeys: []string{"missing"}},
	} {
		request.ProjectID = &projectID
		_, err := view.ListTasks(ctx, request, workflow.StaticRoleResolver{"coder": true})
		var validationErr serverapi.WorkflowRequestValidationError
		if !errors.As(err, &validationErr) {
			t.Fatalf("ListTasks invalid request %+v error = %v, want validation error", request, err)
		}
	}
}

func TestListTasksPreservesFanoutStatusUnions(t *testing.T) {
	ctx, _, workflowStore, binding, view := newWorkflowViewTestContextService(t)
	fixture := createWorkflowViewFanoutStatusFixture(t, ctx, workflowStore, binding)
	projectID := binding.ProjectID
	response, err := view.ListTasks(ctx, serverapi.WorkflowTaskListRequest{
		ProjectID:   &projectID,
		StatusKinds: []serverapi.WorkflowTaskStatusKind{serverapi.WorkflowTaskStatusKindWaitingQuestion},
	}, workflow.StaticRoleResolver{"coder": true})
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if len(response.Tasks) != 1 || response.Tasks[0].TaskID != string(fixture.task.ID) {
		t.Fatalf("fanout list tasks = %+v", response.Tasks)
	}
	detail, err := view.GetTask(ctx, string(fixture.task.ID))
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if !reflect.DeepEqual(response.Tasks[0].Status, detail.Status) || !reflect.DeepEqual(response.Tasks[0].Status.RunIDs, fixture.status.RunIDs) || !reflect.DeepEqual(response.Tasks[0].Status.AttentionTypes, fixture.status.AttentionTypes) {
		t.Fatalf("fanout list/detail status = %+v/%+v, want complete unions %+v", response.Tasks[0].Status, detail.Status, fixture.status)
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

	projectID := binding.ProjectID
	baseRequest := serverapi.WorkflowTaskListRequest{ProjectID: &projectID, PageSize: 2}
	listAllPages := func(request serverapi.WorkflowTaskListRequest) []serverapi.WorkflowTaskListItem {
		t.Helper()
		items := make([]serverapi.WorkflowTaskListItem, 0, 5)
		seen := map[string]bool{}
		for {
			response, err := view.ListTasks(ctx, request, workflow.StaticRoleResolver{"coder": true})
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
			if response.NextPageToken == "" {
				return items
			}
			request.PageToken = response.NextPageToken
		}
	}
	statusAsc := listAllPages(serverapi.WorkflowTaskListRequest{
		ProjectID: baseRequest.ProjectID,
		PageSize:  baseRequest.PageSize,
		Sort:      []serverapi.WorkflowTaskListSort{{Field: serverapi.WorkflowTaskListSortFieldStatus, Direction: serverapi.WorkflowTaskListSortDirectionAsc}},
	})
	if got, want := taskStatusKinds(statusAsc), []serverapi.WorkflowTaskStatusKind{
		serverapi.WorkflowTaskStatusKindDone,
		serverapi.WorkflowTaskStatusKindRunning,
		serverapi.WorkflowTaskStatusKindQueued,
		serverapi.WorkflowTaskStatusKindBacklog,
		serverapi.WorkflowTaskStatusKindActive,
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("status sort = %v, want %v", got, want)
	}
	statusDesc := listAllPages(serverapi.WorkflowTaskListRequest{
		ProjectID: baseRequest.ProjectID,
		PageSize:  baseRequest.PageSize,
		Sort:      []serverapi.WorkflowTaskListSort{{Field: serverapi.WorkflowTaskListSortFieldStatus, Direction: serverapi.WorkflowTaskListSortDirectionDesc}},
	})
	if got, want := taskStatusKinds(statusDesc), []serverapi.WorkflowTaskStatusKind{
		serverapi.WorkflowTaskStatusKindActive,
		serverapi.WorkflowTaskStatusKindBacklog,
		serverapi.WorkflowTaskStatusKindQueued,
		serverapi.WorkflowTaskStatusKindRunning,
		serverapi.WorkflowTaskStatusKindDone,
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("descending status sort = %v, want %v", got, want)
	}
	columnAsc := listAllPages(serverapi.WorkflowTaskListRequest{
		ProjectID: baseRequest.ProjectID,
		PageSize:  baseRequest.PageSize,
		Sort:      []serverapi.WorkflowTaskListSort{{Field: serverapi.WorkflowTaskListSortFieldColumn, Direction: serverapi.WorkflowTaskListSortDirectionAsc}},
	})
	if columnAsc[0].TaskID != string(backlog.ID) || columnAsc[len(columnAsc)-1].TaskID != string(done.ID) {
		t.Fatalf("column sort = %+v, want backlog before done", columnAsc)
	}
	columnDesc := listAllPages(serverapi.WorkflowTaskListRequest{
		ProjectID: baseRequest.ProjectID,
		PageSize:  baseRequest.PageSize,
		Sort:      []serverapi.WorkflowTaskListSort{{Field: serverapi.WorkflowTaskListSortFieldColumn, Direction: serverapi.WorkflowTaskListSortDirectionDesc}},
	})
	if columnDesc[0].TaskID != string(done.ID) || columnDesc[len(columnDesc)-1].TaskID != string(backlog.ID) {
		t.Fatalf("descending column sort = %+v, want done before backlog", columnDesc)
	}

	firstPage, err := view.ListTasks(ctx, serverapi.WorkflowTaskListRequest{
		ProjectID: baseRequest.ProjectID,
		PageSize:  baseRequest.PageSize,
		Sort:      []serverapi.WorkflowTaskListSort{{Field: serverapi.WorkflowTaskListSortFieldStatus, Direction: serverapi.WorkflowTaskListSortDirectionAsc}},
	}, workflow.StaticRoleResolver{"coder": true})
	if err != nil {
		t.Fatalf("ListTasks first page: %v", err)
	}
	if firstPage.NextPageToken == "" {
		t.Fatal("first page must produce a continuation token")
	}
	token, ok, err := parseWorkflowTaskListPageToken(firstPage.NextPageToken)
	if err != nil || !ok {
		t.Fatalf("parse page token = %+v/%t/%v", token, ok, err)
	}
	invalidRequests := []serverapi.WorkflowTaskListRequest{
		{ProjectID: baseRequest.ProjectID, PageSize: baseRequest.PageSize, PageToken: workflowTaskListPageToken(workflowTaskListPageTokenPayload{Version: 1, ProjectID: token.ProjectID, WorkflowID: token.WorkflowID, WorkflowVersion: token.WorkflowVersion, ColumnStructureHash: token.ColumnStructureHash, StatusModelVersion: token.StatusModelVersion, Fingerprint: token.Fingerprint, Cursor: token.Cursor})},
		{ProjectID: baseRequest.ProjectID, PageSize: baseRequest.PageSize, PageToken: firstPage.NextPageToken, StatusKinds: []serverapi.WorkflowTaskStatusKind{serverapi.WorkflowTaskStatusKindBacklog}},
		{ProjectID: baseRequest.ProjectID, PageSize: baseRequest.PageSize, PageToken: firstPage.NextPageToken, Sort: []serverapi.WorkflowTaskListSort{{Field: serverapi.WorkflowTaskListSortFieldColumn, Direction: serverapi.WorkflowTaskListSortDirectionAsc}}},
		{ProjectID: baseRequest.ProjectID, PageSize: baseRequest.PageSize, PageToken: workflowTaskListPageToken(workflowTaskListPageTokenPayload{Version: token.Version, ProjectID: token.ProjectID, WorkflowID: token.WorkflowID, WorkflowVersion: token.WorkflowVersion + 1, ColumnStructureHash: token.ColumnStructureHash, StatusModelVersion: token.StatusModelVersion, Fingerprint: token.Fingerprint, Cursor: token.Cursor})},
		{ProjectID: baseRequest.ProjectID, PageSize: baseRequest.PageSize, PageToken: workflowTaskListPageToken(workflowTaskListPageTokenPayload{Version: token.Version, ProjectID: token.ProjectID, WorkflowID: token.WorkflowID, WorkflowVersion: token.WorkflowVersion, ColumnStructureHash: "changed", StatusModelVersion: token.StatusModelVersion, Fingerprint: token.Fingerprint, Cursor: token.Cursor})},
		{ProjectID: baseRequest.ProjectID, PageSize: baseRequest.PageSize, PageToken: workflowTaskListPageToken(workflowTaskListPageTokenPayload{Version: token.Version, ProjectID: token.ProjectID, WorkflowID: token.WorkflowID, WorkflowVersion: token.WorkflowVersion, ColumnStructureHash: token.ColumnStructureHash, StatusModelVersion: token.StatusModelVersion + 1, Fingerprint: token.Fingerprint, Cursor: token.Cursor})},
		{ProjectID: baseRequest.ProjectID, PageSize: baseRequest.PageSize, PageToken: workflowTaskListPageToken(workflowTaskListPageTokenPayload{Version: token.Version, ProjectID: "project-conflict", WorkflowID: token.WorkflowID, WorkflowVersion: token.WorkflowVersion, ColumnStructureHash: token.ColumnStructureHash, StatusModelVersion: token.StatusModelVersion, Fingerprint: token.Fingerprint, Cursor: token.Cursor})},
	}
	for _, request := range invalidRequests {
		_, err := view.ListTasks(ctx, request, workflow.StaticRoleResolver{"coder": true})
		if !errors.Is(err, ErrInvalidPageToken) {
			t.Fatalf("ListTasks invalid continuation %+v error = %v, want ErrInvalidPageToken", request, err)
		}
	}
}

func TestTaskListResolvesExactProjectWorkflowScope(t *testing.T) {
	type scopeExpectation struct {
		kind         serverapi.WorkflowTaskListScopeErrorKind
		missingScope *serverapi.WorkflowTaskListScopeDimension
		projectIDs   map[string]bool
		workflowIDs  map[string]bool
	}
	for _, testCase := range []struct {
		name  string
		setup func(t *testing.T, ctx context.Context, store *metadata.Store, workflowStore *workflowstore.Store, binding metadata.Binding, firstWorkflowID workflow.WorkflowID, secondWorkflowID workflow.WorkflowID) (serverapi.WorkflowTaskListRequest, string, string, *scopeExpectation)
	}{
		{
			name: "exact pair",
			setup: func(t *testing.T, ctx context.Context, _ *metadata.Store, workflowStore *workflowstore.Store, binding metadata.Binding, firstWorkflowID workflow.WorkflowID, _ workflow.WorkflowID) (serverapi.WorkflowTaskListRequest, string, string, *scopeExpectation) {
				if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, firstWorkflowID, true); err != nil {
					t.Fatalf("LinkWorkflow: %v", err)
				}
				projectID, workflowID := binding.ProjectID, string(firstWorkflowID)
				return serverapi.WorkflowTaskListRequest{ProjectID: &projectID, WorkflowID: &workflowID}, projectID, workflowID, nil
			},
		},
		{
			name: "unique project inference",
			setup: func(t *testing.T, ctx context.Context, _ *metadata.Store, workflowStore *workflowstore.Store, binding metadata.Binding, firstWorkflowID workflow.WorkflowID, _ workflow.WorkflowID) (serverapi.WorkflowTaskListRequest, string, string, *scopeExpectation) {
				if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, firstWorkflowID, true); err != nil {
					t.Fatalf("LinkWorkflow: %v", err)
				}
				projectID := binding.ProjectID
				return serverapi.WorkflowTaskListRequest{ProjectID: &projectID}, projectID, string(firstWorkflowID), nil
			},
		},
		{
			name: "unique workflow inference",
			setup: func(t *testing.T, ctx context.Context, _ *metadata.Store, workflowStore *workflowstore.Store, binding metadata.Binding, firstWorkflowID workflow.WorkflowID, _ workflow.WorkflowID) (serverapi.WorkflowTaskListRequest, string, string, *scopeExpectation) {
				if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, firstWorkflowID, true); err != nil {
					t.Fatalf("LinkWorkflow: %v", err)
				}
				workflowID := string(firstWorkflowID)
				return serverapi.WorkflowTaskListRequest{WorkflowID: &workflowID}, binding.ProjectID, workflowID, nil
			},
		},
		{
			name: "project has no links",
			setup: func(_ *testing.T, _ context.Context, _ *metadata.Store, _ *workflowstore.Store, _ metadata.Binding, _ workflow.WorkflowID, _ workflow.WorkflowID) (serverapi.WorkflowTaskListRequest, string, string, *scopeExpectation) {
				projectID := "project-unlinked"
				return serverapi.WorkflowTaskListRequest{ProjectID: &projectID}, "", "", &scopeExpectation{kind: serverapi.WorkflowTaskListScopeErrorKindNotLinked, projectIDs: map[string]bool{projectID: true}}
			},
		},
		{
			name: "workflow has no links",
			setup: func(_ *testing.T, _ context.Context, _ *metadata.Store, _ *workflowstore.Store, _ metadata.Binding, _ workflow.WorkflowID, _ workflow.WorkflowID) (serverapi.WorkflowTaskListRequest, string, string, *scopeExpectation) {
				workflowID := "workflow-unlinked"
				return serverapi.WorkflowTaskListRequest{WorkflowID: &workflowID}, "", "", &scopeExpectation{kind: serverapi.WorkflowTaskListScopeErrorKindNotLinked, workflowIDs: map[string]bool{workflowID: true}}
			},
		},
		{
			name: "project inference is ambiguous",
			setup: func(t *testing.T, ctx context.Context, _ *metadata.Store, workflowStore *workflowstore.Store, binding metadata.Binding, firstWorkflowID workflow.WorkflowID, secondWorkflowID workflow.WorkflowID) (serverapi.WorkflowTaskListRequest, string, string, *scopeExpectation) {
				if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, firstWorkflowID, true); err != nil {
					t.Fatalf("LinkWorkflow first: %v", err)
				}
				if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, secondWorkflowID, false); err != nil {
					t.Fatalf("LinkWorkflow second: %v", err)
				}
				projectID := binding.ProjectID
				missing := serverapi.WorkflowTaskListScopeDimensionWorkflow
				return serverapi.WorkflowTaskListRequest{ProjectID: &projectID}, "", "", &scopeExpectation{kind: serverapi.WorkflowTaskListScopeErrorKindAmbiguous, missingScope: &missing, projectIDs: map[string]bool{projectID: true}, workflowIDs: map[string]bool{string(firstWorkflowID): true, string(secondWorkflowID): true}}
			},
		},
		{
			name: "workflow inference is ambiguous",
			setup: func(t *testing.T, ctx context.Context, store *metadata.Store, workflowStore *workflowstore.Store, binding metadata.Binding, firstWorkflowID workflow.WorkflowID, _ workflow.WorkflowID) (serverapi.WorkflowTaskListRequest, string, string, *scopeExpectation) {
				if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, firstWorkflowID, true); err != nil {
					t.Fatalf("LinkWorkflow first project: %v", err)
				}
				otherBinding, err := store.CreateProjectForWorkspace(ctx, t.TempDir(), "Other")
				if err != nil {
					t.Fatalf("CreateProjectForWorkspace: %v", err)
				}
				if _, err := workflowStore.LinkWorkflow(ctx, otherBinding.ProjectID, firstWorkflowID, true); err != nil {
					t.Fatalf("LinkWorkflow second project: %v", err)
				}
				workflowID := string(firstWorkflowID)
				missing := serverapi.WorkflowTaskListScopeDimensionProject
				return serverapi.WorkflowTaskListRequest{WorkflowID: &workflowID}, "", "", &scopeExpectation{kind: serverapi.WorkflowTaskListScopeErrorKindAmbiguous, missingScope: &missing, projectIDs: map[string]bool{binding.ProjectID: true, otherBinding.ProjectID: true}, workflowIDs: map[string]bool{workflowID: true}}
			},
		},
		{
			name: "explicit pair is not linked",
			setup: func(t *testing.T, ctx context.Context, _ *metadata.Store, workflowStore *workflowstore.Store, binding metadata.Binding, firstWorkflowID workflow.WorkflowID, secondWorkflowID workflow.WorkflowID) (serverapi.WorkflowTaskListRequest, string, string, *scopeExpectation) {
				if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, firstWorkflowID, true); err != nil {
					t.Fatalf("LinkWorkflow: %v", err)
				}
				projectID, workflowID := binding.ProjectID, string(secondWorkflowID)
				return serverapi.WorkflowTaskListRequest{ProjectID: &projectID, WorkflowID: &workflowID}, "", "", &scopeExpectation{kind: serverapi.WorkflowTaskListScopeErrorKindNotLinked, projectIDs: map[string]bool{projectID: true}, workflowIDs: map[string]bool{workflowID: true}}
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			ctx, store, workflowStore, binding, view := newWorkflowViewTestContextService(t)
			firstWorkflowID := createWorkflowViewValidWorkflow(t, ctx, workflowStore)
			secondWorkflowID := createWorkflowViewValidWorkflow(t, ctx, workflowStore)
			request, wantProjectID, wantWorkflowID, scopeErrWant := testCase.setup(t, ctx, store, workflowStore, binding, firstWorkflowID, secondWorkflowID)
			response, err := view.ListTasks(ctx, request, workflow.StaticRoleResolver{"coder": true})
			if scopeErrWant == nil {
				if err != nil {
					t.Fatalf("ListTasks: %v", err)
				}
				if response.ProjectID != wantProjectID || response.WorkflowID != wantWorkflowID {
					t.Fatalf("resolved scope = project %q workflow %q, want %q/%q", response.ProjectID, response.WorkflowID, wantProjectID, wantWorkflowID)
				}
				return
			}
			var scopeErr *serverapi.WorkflowTaskListScopeError
			if !errors.As(err, &scopeErr) {
				t.Fatalf("ListTasks error = %T %v, want scope error", err, err)
			}
			if scopeErr.Kind != scopeErrWant.kind || !reflect.DeepEqual(scopeIDSet(scopeErr.ProjectIDs), scopeErrWant.projectIDs) || !reflect.DeepEqual(scopeIDSet(scopeErr.WorkflowIDs), scopeErrWant.workflowIDs) {
				t.Fatalf("scope error = %+v, want %+v", scopeErr, scopeErrWant)
			}
			if scopeErrWant.missingScope == nil {
				if scopeErr.MissingScope != nil {
					t.Fatalf("scope error missing scope = %q, want nil", *scopeErr.MissingScope)
				}
			} else if scopeErr.MissingScope == nil || *scopeErr.MissingScope != *scopeErrWant.missingScope {
				t.Fatalf("scope error missing scope = %+v, want %q", scopeErr.MissingScope, *scopeErrWant.missingScope)
			}
		})
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
	firstPage, err := view.ListTasks(ctx, serverapi.WorkflowTaskListRequest{ProjectID: &projectID, WorkflowID: &workflowIDValue, PageSize: 1}, workflow.StaticRoleResolver{"coder": true})
	if err != nil {
		t.Fatalf("ListTasks first page: %v", err)
	}
	if firstPage.NextPageToken == "" {
		t.Fatal("expected continuation token")
	}
	nextPage, err := view.ListTasks(ctx, serverapi.WorkflowTaskListRequest{PageToken: firstPage.NextPageToken, PageSize: 1}, workflow.StaticRoleResolver{"coder": true})
	if err != nil {
		t.Fatalf("ListTasks token-only continuation: %v", err)
	}
	if nextPage.ProjectID != binding.ProjectID || nextPage.WorkflowID != string(workflowID) || len(nextPage.Tasks) != 1 || nextPage.Tasks[0].TaskID == firstPage.Tasks[0].TaskID {
		t.Fatalf("token-only continuation = %+v, want resolved second page", nextPage)
	}
	otherProjectID := "project-conflict"
	_, err = view.ListTasks(ctx, serverapi.WorkflowTaskListRequest{ProjectID: &otherProjectID, PageToken: firstPage.NextPageToken, PageSize: 1}, workflow.StaticRoleResolver{"coder": true})
	if !errors.Is(err, ErrInvalidPageToken) {
		t.Fatalf("ListTasks conflicting token scope error = %v, want ErrInvalidPageToken", err)
	}
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

func workflowTaskAttentionStrings(values []serverapi.WorkflowTaskAttentionKind) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, string(value))
	}
	return out
}
