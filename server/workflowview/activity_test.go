package workflowview

import (
	"database/sql"
	"slices"
	"testing"

	"core/server/metadata/sqlitegen"
	"core/server/workflowstore"
	"core/shared/serverapi"
)

func TestActivityLoadsStableBoundedPagesAndOnlyReferencedSources(t *testing.T) {
	ctx, metadataStore, workflowStore, binding := newWorkflowViewTestContextStore(t)
	workflowID := createWorkflowViewValidWorkflow(t, ctx, workflowStore)
	if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, workflowID, true); err != nil {
		t.Fatalf("LinkWorkflow: %v", err)
	}
	task, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{
		ProjectID: binding.ProjectID,
		Title:     "Focused activity",
		Body:      "Body",
	})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	started, err := workflowStore.StartTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	firstComment, err := workflowStore.AddComment(ctx, task.ID, "first", "user", "nek")
	if err != nil {
		t.Fatalf("AddComment first: %v", err)
	}
	secondComment, err := workflowStore.AddComment(ctx, task.ID, "second", "user", "nek")
	if err != nil {
		t.Fatalf("AddComment second: %v", err)
	}
	claimed, err := workflowStore.ClaimRun(ctx, started.RunID, 0)
	if err != nil {
		t.Fatalf("ClaimRun: %v", err)
	}
	if err := workflowStore.InterruptRunGeneration(ctx, started.RunID, claimed.Generation, "manual", "{}"); err != nil {
		t.Fatalf("InterruptRunGeneration: %v", err)
	}
	if _, err := workflowStore.CancelTask(ctx, task.ID, "stop"); err != nil {
		t.Fatalf("CancelTask: %v", err)
	}

	const sharedTimestamp = int64(111)
	for _, update := range []struct {
		query string
		args  []any
	}{
		{
			query: `UPDATE task_comments SET created_at_unix_ms = ?, updated_at_unix_ms = ? WHERE id IN (?, ?)`,
			args:  []any{sharedTimestamp, sharedTimestamp, firstComment.ID, secondComment.ID},
		},
		{
			query: `UPDATE task_runs SET started_at_unix_ms = ?, interrupted_at_unix_ms = ?, updated_at_unix_ms = ? WHERE id = ?`,
			args:  []any{sharedTimestamp, sharedTimestamp, sharedTimestamp, string(started.RunID)},
		},
		{
			query: `UPDATE tasks SET canceled_at_unix_ms = ?, updated_at_unix_ms = ? WHERE id = ?`,
			args:  []any{sharedTimestamp, sharedTimestamp, string(task.ID)},
		},
		{
			query: `UPDATE task_transitions SET created_at_unix_ms = ?, applied_at_unix_ms = ? WHERE task_id = ?`,
			args:  []any{sharedTimestamp, sharedTimestamp, string(task.ID)},
		},
	} {
		if _, err := metadataStore.DB().ExecContext(ctx, update.query, update.args...); err != nil {
			t.Fatalf("force equal activity timestamps: %v", err)
		}
	}

	definitions, err := NewDefinitionProjection(workflowStore)
	if err != nil {
		t.Fatalf("NewDefinitionProjection: %v", err)
	}
	activity, err := NewActivity(metadataStore, definitions, NewTaskProjector())
	if err != nil {
		t.Fatalf("NewActivity: %v", err)
	}
	all, err := activity.loadPage(ctx, serverapi.WorkflowTaskActivityListRequest{
		TaskID:   string(task.ID),
		PageSize: 100,
	})
	if err != nil {
		t.Fatalf("load all activity: %v", err)
	}
	if len(all.rows) < 6 {
		t.Fatalf("activity rows = %d, want at least 6", len(all.rows))
	}

	var loadedIDs []string
	pageToken := ""
	for {
		page, err := activity.loadPage(ctx, serverapi.WorkflowTaskActivityListRequest{
			TaskID:    string(task.ID),
			PageSize:  2,
			PageToken: pageToken,
		})
		if err != nil {
			t.Fatalf("load activity page: %v", err)
		}
		if len(page.rows) > 2 {
			t.Fatalf("page rows = %d, want at most 2", len(page.rows))
		}
		assertActivityPageOrdering(t, page.rows)
		assertActivityPageSources(t, page)
		for _, row := range page.rows {
			if slices.Contains(loadedIDs, row.activityID) {
				t.Fatalf("duplicate activity across pages: %q", row.activityID)
			}
			loadedIDs = append(loadedIDs, row.activityID)
		}
		if len(loadedIDs) == 2 && page.nextPageToken == "" {
			t.Fatal("first bounded page omitted continuation token")
		}
		if page.nextPageToken == "" {
			break
		}
		pageToken = page.nextPageToken
	}

	wantIDs := make([]string, 0, len(all.rows))
	for _, row := range all.rows {
		wantIDs = append(wantIDs, row.activityID)
	}
	if !slices.Equal(loadedIDs, wantIDs) {
		t.Fatalf("paged activity ids = %v, want %v", loadedIDs, wantIDs)
	}
}

func TestActivityProjectsEveryDurableTaskEventThroughFocusedInterface(t *testing.T) {
	ctx, metadataStore, workflowStore, binding := newWorkflowViewTestContextStore(t)
	workflowID := createWorkflowViewValidWorkflow(t, ctx, workflowStore)
	if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, workflowID, true); err != nil {
		t.Fatalf("LinkWorkflow: %v", err)
	}
	definitions, err := NewDefinitionProjection(workflowStore)
	if err != nil {
		t.Fatalf("NewDefinitionProjection: %v", err)
	}
	activity, err := NewActivity(metadataStore, definitions, NewTaskProjector())
	if err != nil {
		t.Fatalf("NewActivity: %v", err)
	}

	completedTask, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{
		ProjectID: binding.ProjectID,
		Title:     "Completed activity",
		Body:      "Body",
	})
	if err != nil {
		t.Fatalf("CreateTask completed: %v", err)
	}
	completedStarted, err := workflowStore.StartTask(ctx, completedTask.ID)
	if err != nil {
		t.Fatalf("StartTask completed: %v", err)
	}
	comment, err := workflowStore.AddComment(ctx, completedTask.ID, "note", "user", "nek")
	if err != nil {
		t.Fatalf("AddComment completed: %v", err)
	}
	if _, err := workflowStore.ClaimRun(ctx, completedStarted.RunID, 0); err != nil {
		t.Fatalf("ClaimRun completed: %v", err)
	}
	completed, err := workflowStore.CompleteRun(ctx, workflowstore.CompleteRunRequest{
		RunID:        completedStarted.RunID,
		TransitionID: "done",
		Commentary:   "finished",
		Actor:        "agent",
	})
	if err != nil {
		t.Fatalf("CompleteRun: %v", err)
	}
	completedPage, err := activity.List(ctx, serverapi.WorkflowTaskActivityListRequest{TaskID: string(completedTask.ID)})
	if err != nil {
		t.Fatalf("List completed activity: %v", err)
	}
	commentItem := requireActivityItem(t, completedPage.Items, "comment:"+comment.ID)
	if commentItem.Comment == nil || commentItem.Comment.ID != comment.ID || commentItem.Run != nil || commentItem.Transition != nil || commentItem.Attention != nil {
		t.Fatalf("comment activity = %+v", commentItem)
	}
	transitionItem := requireActivityItem(t, completedPage.Items, "transition:"+string(completed.Result.TransitionID))
	if transitionItem.Transition == nil ||
		transitionItem.Transition.ID != string(completed.Result.TransitionID) ||
		transitionItem.Transition.Actor != "agent" ||
		transitionItem.Transition.Commentary != "finished" ||
		len(transitionItem.Transition.Edges) != 1 ||
		transitionItem.Comment != nil ||
		transitionItem.Run != nil ||
		transitionItem.Attention != nil {
		t.Fatalf("transition activity = %+v", transitionItem)
	}
	startedItem := requireActivityItem(t, completedPage.Items, "run_started:"+string(completedStarted.RunID))
	if startedItem.Run == nil || startedItem.Run.ID != string(completedStarted.RunID) || startedItem.Type != "run_started" || startedItem.Attention != nil {
		t.Fatalf("run-started activity = %+v", startedItem)
	}
	completedItem := requireActivityItem(t, completedPage.Items, "run_completed:"+string(completedStarted.RunID))
	if completedItem.Run == nil || completedItem.Run.ID != string(completedStarted.RunID) || completedItem.Type != "run_completed" || completedItem.Attention != nil {
		t.Fatalf("run-completed activity = %+v", completedItem)
	}

	interruptedTask, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{
		ProjectID: binding.ProjectID,
		Title:     "Interrupted activity",
		Body:      "Body",
	})
	if err != nil {
		t.Fatalf("CreateTask interrupted: %v", err)
	}
	interruptedStarted, err := workflowStore.StartTask(ctx, interruptedTask.ID)
	if err != nil {
		t.Fatalf("StartTask interrupted: %v", err)
	}
	claimed, err := workflowStore.ClaimRun(ctx, interruptedStarted.RunID, 0)
	if err != nil {
		t.Fatalf("ClaimRun interrupted: %v", err)
	}
	if err := workflowStore.InterruptRunGeneration(ctx, interruptedStarted.RunID, claimed.Generation, "manual", `{"source":"operator"}`); err != nil {
		t.Fatalf("InterruptRunGeneration: %v", err)
	}
	interruptedPage, err := activity.List(ctx, serverapi.WorkflowTaskActivityListRequest{TaskID: string(interruptedTask.ID)})
	if err != nil {
		t.Fatalf("List interrupted activity: %v", err)
	}
	interruptedItem := requireActivityItem(t, interruptedPage.Items, "run_interrupted:"+string(interruptedStarted.RunID))
	if interruptedItem.Run == nil ||
		interruptedItem.Run.ID != string(interruptedStarted.RunID) ||
		interruptedItem.Attention == nil ||
		interruptedItem.Attention.ProjectID != binding.ProjectID ||
		interruptedItem.Attention.WorkflowID == nil ||
		*interruptedItem.Attention.WorkflowID != string(workflowID) ||
		interruptedItem.Attention.TaskID != string(interruptedTask.ID) ||
		!attentionPointerEquals(interruptedItem.Attention.RunID, string(interruptedStarted.RunID)) ||
		interruptedItem.Attention.Message != interruptedItem.Summary ||
		interruptedItem.Attention.OccurredAtUnixMs != interruptedItem.OccurredAtUnixMs {
		t.Fatalf("run-interrupted activity = %+v", interruptedItem)
	}

	canceledTask, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{
		ProjectID: binding.ProjectID,
		Title:     "Canceled activity",
		Body:      "Body",
	})
	if err != nil {
		t.Fatalf("CreateTask canceled: %v", err)
	}
	if _, err := workflowStore.CancelTask(ctx, canceledTask.ID, "stopped"); err != nil {
		t.Fatalf("CancelTask: %v", err)
	}
	canceledPage, err := activity.List(ctx, serverapi.WorkflowTaskActivityListRequest{TaskID: string(canceledTask.ID)})
	if err != nil {
		t.Fatalf("List canceled activity: %v", err)
	}
	canceledItem := requireActivityItem(t, canceledPage.Items, "task_canceled:"+string(canceledTask.ID))
	if canceledItem.Type != "task_canceled" ||
		canceledItem.Comment != nil ||
		canceledItem.Transition != nil ||
		canceledItem.Run != nil ||
		canceledItem.Attention != nil {
		t.Fatalf("task-canceled activity = %+v", canceledItem)
	}
}

func requireActivityItem(t *testing.T, items []serverapi.WorkflowTaskActivityItem, activityID string) serverapi.WorkflowTaskActivityItem {
	t.Helper()
	for _, item := range items {
		if item.ActivityID == activityID {
			return item
		}
	}
	t.Fatalf("activity %q not found in %+v", activityID, items)
	return serverapi.WorkflowTaskActivityItem{}
}

func assertActivityPageOrdering(t *testing.T, rows []taskActivityRow) {
	t.Helper()
	for index := 1; index < len(rows); index++ {
		previous := rows[index-1]
		current := rows[index]
		if previous.occurredAtUnixMs < current.occurredAtUnixMs ||
			(previous.occurredAtUnixMs == current.occurredAtUnixMs && previous.activityID <= current.activityID) {
			t.Fatalf("activity rows are not newest-first: %+v", rows)
		}
	}
}

func assertActivityPageSources(t *testing.T, page activityPage) {
	t.Helper()
	commentIDs := sourceIDsByType(page.rows, "comment")
	transitionIDs := sourceIDsByType(page.rows, "transition")
	runIDs := sourceIDsByTypes(page.rows, "run_started", "run_completed", "run_interrupted")
	assertActivitySourceKeys(t, page.comments, commentIDs)
	assertActivitySourceKeys(t, page.transitions, transitionIDs)
	assertActivitySourceKeys(t, page.runs, runIDs)
	for transitionID := range page.edges {
		if !slices.Contains(transitionIDs, transitionID) {
			t.Fatalf("loaded edges for unreferenced transition %q", transitionID)
		}
	}
	for sessionID := range page.sessionNames {
		referenced := false
		for _, run := range page.runs {
			if run.SessionID == (sql.NullString{String: sessionID, Valid: true}) {
				referenced = true
				break
			}
		}
		if !referenced {
			t.Fatalf("loaded name for unreferenced session %q", sessionID)
		}
	}
}

func assertActivitySourceKeys[T sqlitegen.TaskComment | sqlitegen.TaskTransitionRecord | sqlitegen.TaskRunRecord](t *testing.T, sources map[string]T, wantIDs []string) {
	t.Helper()
	if len(sources) != len(wantIDs) {
		t.Fatalf("loaded source count = %d, want %d for %v", len(sources), len(wantIDs), wantIDs)
	}
	for sourceID := range sources {
		if !slices.Contains(wantIDs, sourceID) {
			t.Fatalf("loaded unreferenced source %q; want one of %v", sourceID, wantIDs)
		}
	}
}
