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
	activity, err := NewActivity(metadataStore, definitions)
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
