package workflowview

import (
	"errors"
	"slices"
	"strings"
	"testing"

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
	definitions, err := NewDefinitionProjection(workflowStore)
	if err != nil {
		t.Fatalf("NewDefinitionProjection: %v", err)
	}
	taskList, err := NewTaskList(metadataStore, definitions, projector)
	if err != nil {
		t.Fatalf("NewTaskList: %v", err)
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
			labelFilter: workflowTaskLabelFilterFacts{
				Kind:     serverapi.WorkflowTaskLabelFilterKindNone,
				LabelIDs: []string{},
			},
			sortSelectors: sortSelectors,
			limit:         100,
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

func TestTaskListScopesAndContinuationsThroughFocusedInterface(t *testing.T) {
	ctx, metadataStore, workflowStore, binding := newWorkflowViewTestContextStore(t)
	firstWorkflowID := createWorkflowViewValidWorkflow(t, ctx, workflowStore)
	secondWorkflowID := createWorkflowViewValidWorkflow(t, ctx, workflowStore)
	if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, firstWorkflowID, true); err != nil {
		t.Fatalf("LinkWorkflow first: %v", err)
	}
	if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, secondWorkflowID, false); err != nil {
		t.Fatalf("LinkWorkflow second: %v", err)
	}
	for _, task := range []struct {
		workflowID workflow.WorkflowID
		title      string
	}{
		{workflowID: firstWorkflowID, title: "Alpha"},
		{workflowID: firstWorkflowID, title: "Bravo"},
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
	definitions, err := NewDefinitionProjection(workflowStore)
	if err != nil {
		t.Fatalf("NewDefinitionProjection: %v", err)
	}
	taskList, err := NewTaskList(metadataStore, definitions, NewTaskProjector())
	if err != nil {
		t.Fatalf("NewTaskList: %v", err)
	}
	projectID := binding.ProjectID
	sortByTitle := []serverapi.WorkflowTaskListSort{{
		Field:     serverapi.WorkflowTaskListSortFieldTitle,
		Direction: serverapi.WorkflowTaskListSortDirectionAsc,
	}}

	projectWide, err := taskList.List(ctx, serverapi.WorkflowTaskListRequest{
		LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone},
		ProjectID:   &projectID,
		PageSize:    1,
		Sort:        sortByTitle,
	})
	if err != nil {
		t.Fatalf("List project-wide: %v", err)
	}
	if projectWide.Scope.ProjectID != projectID ||
		projectWide.Scope.WorkflowID != nil ||
		projectWide.MatchingWorkflowCardinality != serverapi.WorkflowTaskListMatchingWorkflowCardinalityMultiple ||
		len(projectWide.Tasks) != 1 ||
		projectWide.Tasks[0].Title != "Alpha" ||
		projectWide.Tasks[0].WorkflowName == nil ||
		projectWide.NextPageToken == nil {
		t.Fatalf("project-wide first page = %+v", projectWide)
	}
	continued, err := taskList.List(ctx, serverapi.WorkflowTaskListRequest{
		LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone},
		PageToken:   *projectWide.NextPageToken,
		PageSize:    10,
		Sort:        sortByTitle,
	})
	if err != nil {
		t.Fatalf("List inferred continuation scope: %v", err)
	}
	if continued.Scope.ProjectID != projectID ||
		continued.Scope.WorkflowID != nil ||
		continued.MatchingWorkflowCardinality != serverapi.WorkflowTaskListMatchingWorkflowCardinalityMultiple ||
		len(continued.Tasks) != 2 ||
		continued.Tasks[0].Title != "Bravo" ||
		continued.Tasks[1].Title != "Zulu" {
		t.Fatalf("project-wide continuation = %+v", continued)
	}
	if _, err := taskList.List(ctx, serverapi.WorkflowTaskListRequest{
		LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone},
		PageToken:   *projectWide.NextPageToken,
		PageSize:    10,
		Sort:        sortByTitle,
		StatusKinds: []serverapi.WorkflowTaskStatusKind{serverapi.WorkflowTaskStatusKindDone},
	}); !errors.Is(err, ErrInvalidPageToken) {
		t.Fatalf("changed-filter continuation error = %v, want ErrInvalidPageToken", err)
	}

	firstWorkflowIDString := string(firstWorkflowID)
	narrowed, err := taskList.List(ctx, serverapi.WorkflowTaskListRequest{
		LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone},
		ProjectID:   &projectID,
		WorkflowID:  &firstWorkflowIDString,
		ColumnKeys:  []string{"backlog"},
		PageSize:    1,
		Sort:        sortByTitle,
	})
	if err != nil {
		t.Fatalf("List narrowed: %v", err)
	}
	if narrowed.Scope.WorkflowID == nil ||
		*narrowed.Scope.WorkflowID != firstWorkflowIDString ||
		narrowed.MatchingWorkflowCardinality != serverapi.WorkflowTaskListMatchingWorkflowCardinalityOne ||
		len(narrowed.Tasks) != 1 ||
		narrowed.Tasks[0].ColumnKeys == nil ||
		!slices.Equal(*narrowed.Tasks[0].ColumnKeys, []string{"backlog"}) ||
		narrowed.NextPageToken == nil {
		t.Fatalf("narrowed first page = %+v", narrowed)
	}
	if _, err := workflowStore.AddNode(ctx, workflowstore.NodeRecord{
		ID:          workflow.NodeID("node-stale-token-" + firstWorkflowIDString),
		WorkflowID:  firstWorkflowID,
		Key:         "archive",
		Kind:        workflow.NodeKindTerminal,
		DisplayName: "Archive",
	}); err != nil {
		t.Fatalf("AddNode stale token mutation: %v", err)
	}
	if _, err := taskList.List(ctx, serverapi.WorkflowTaskListRequest{
		LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone},
		PageToken:   *narrowed.NextPageToken,
		PageSize:    10,
		Sort:        sortByTitle,
	}); !errors.Is(err, ErrInvalidPageToken) {
		t.Fatalf("stale narrowed continuation error = %v, want ErrInvalidPageToken", err)
	}

	unlinkedWorkflowID := createWorkflowViewValidWorkflow(t, ctx, workflowStore)
	unlinkedWorkflowIDString := string(unlinkedWorkflowID)
	_, err = taskList.List(ctx, serverapi.WorkflowTaskListRequest{
		LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone},
		ProjectID:   &projectID,
		WorkflowID:  &unlinkedWorkflowIDString,
	})
	var scopeErr *serverapi.WorkflowTaskListScopeError
	if !errors.As(err, &scopeErr) || scopeErr.Reason != serverapi.WorkflowTaskListScopeReasonWorkflowNotLinked {
		t.Fatalf("unlinked workflow error = %+v", err)
	}
	_, err = taskList.List(ctx, serverapi.WorkflowTaskListRequest{
		LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone},
		ProjectID:   &projectID,
		WorkflowID:  &firstWorkflowIDString,
		ColumnKeys:  []string{"missing"},
	})
	var validationErr serverapi.WorkflowRequestValidationError
	if !errors.As(err, &validationErr) || validationErr.Field != "column_keys[0]" {
		t.Fatalf("invalid column error = %+v", err)
	}
}
