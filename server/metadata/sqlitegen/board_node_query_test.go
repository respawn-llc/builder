package sqlitegen

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"testing"

	"core/shared/runtimeids"
)

const boardQueryNodeID = "55555555-5555-4555-8555-555555555555"

func TestListBoardNodeTasksSortsAndOffsetsFilteredRows(t *testing.T) {
	db := openSQLiteFixture(t)
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`
CREATE TABLE tasks (
	id TEXT PRIMARY KEY,
	project_id TEXT NOT NULL,
	project_workflow_link_id TEXT NOT NULL,
	workflow_id BLOB NOT NULL,
	workflow_revision_seen INTEGER NOT NULL,
	task_seq INTEGER NOT NULL,
	short_id TEXT NOT NULL,
	title TEXT NOT NULL,
	body TEXT NOT NULL,
	source_url TEXT NOT NULL,
	source_workspace_id TEXT,
	managed_worktree_id TEXT,
	execution_target_mode TEXT,
	execution_target_requested_ref TEXT,
	execution_target_resolved_ref TEXT,
	execution_target_commit_oid TEXT,
	execution_target_provenance TEXT,
	created_at_unix_ms INTEGER NOT NULL,
	updated_at_unix_ms INTEGER NOT NULL,
	metadata_json TEXT NOT NULL
);
CREATE TABLE project_labels (
	id TEXT PRIMARY KEY,
	ordinal INTEGER NOT NULL
);
CREATE TABLE task_label_assignments (
	task_id TEXT NOT NULL,
	label_id TEXT NOT NULL,
	PRIMARY KEY (task_id, label_id)
);
CREATE INDEX task_label_assignments_label_task_idx
	ON task_label_assignments(label_id, task_id);
CREATE TABLE task_current_nodes (
	task_id TEXT NOT NULL,
	node_id BLOB NOT NULL
);
CREATE TABLE task_dependencies (
	blocker_task_id TEXT NOT NULL,
	blocked_task_id TEXT NOT NULL,
	PRIMARY KEY (blocker_task_id, blocked_task_id)
);
CREATE INDEX task_dependencies_reverse_idx
	ON task_dependencies(blocked_task_id, blocker_task_id);
CREATE TABLE workflow_task_status_records (
	task_id TEXT PRIMARY KEY,
	is_done INTEGER NOT NULL
);
CREATE VIEW task_records AS SELECT * FROM tasks;
`); err != nil {
		t.Fatalf("create board query fixture: %v", err)
	}

	workflowID := runtimeids.NewWorkflowID()
	workflowValue, err := workflowID.Value()
	if err != nil {
		t.Fatalf("workflow id value: %v", err)
	}
	nodeValue, err := runtimeids.GraphEntityIDBlob(boardQueryNodeID)
	if err != nil {
		t.Fatalf("Node id value: %v", err)
	}
	for _, label := range []struct {
		id      string
		ordinal int
	}{
		{id: "label-1", ordinal: 1},
		{id: "label-2", ordinal: 2},
		{id: "label-3", ordinal: 3},
	} {
		if _, err := db.Exec(`INSERT INTO project_labels (id, ordinal) VALUES (?, ?)`, label.id, label.ordinal); err != nil {
			t.Fatalf("insert label %s: %v", label.id, err)
		}
	}
	tasks := []struct {
		id       string
		seq      int
		created  int
		updated  int
		labelIDs []string
	}{
		{id: "task-1", seq: 1, created: 10, updated: 100, labelIDs: []string{"label-1", "label-2"}},
		{id: "task-2", seq: 2, created: 20, updated: 100, labelIDs: []string{"label-1"}},
		{id: "task-3", seq: 3, created: 30, updated: 90, labelIDs: []string{"label-2"}},
		{id: "task-4", seq: 4, created: 40, updated: 80},
		{id: "task-5", seq: 5, created: 50, updated: 70, labelIDs: []string{"label-1", "label-3"}},
		{id: "task-6", seq: 6, created: 60, updated: 60, labelIDs: []string{"label-1"}},
	}
	for _, task := range tasks {
		if _, err := db.Exec(`
INSERT INTO tasks (
	id, project_id, project_workflow_link_id, workflow_id, workflow_revision_seen,
	task_seq, short_id, title, body, source_url, created_at_unix_ms,
	updated_at_unix_ms, metadata_json
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			task.id, "project-1", "link-1", workflowValue, 1, task.seq, fmt.Sprintf("PROJ-%d", task.seq),
			task.id, task.id, "", task.created, task.updated, "{}",
		); err != nil {
			t.Fatalf("insert task %s: %v", task.id, err)
		}
		if _, err := db.Exec(`INSERT INTO task_current_nodes (task_id, node_id) VALUES (?, ?)`, task.id, nodeValue); err != nil {
			t.Fatalf("insert current node %s: %v", task.id, err)
		}
		for _, labelID := range task.labelIDs {
			if _, err := db.Exec(`INSERT INTO task_label_assignments (task_id, label_id) VALUES (?, ?)`, task.id, labelID); err != nil {
				t.Fatalf("insert task label %s/%s: %v", task.id, labelID, err)
			}
		}
	}
	if _, err := db.Exec(`
INSERT INTO task_dependencies (blocker_task_id, blocked_task_id)
VALUES (?, ?), (?, ?)`,
		"blocker-1", "task-1", "blocker-2", "task-3",
	); err != nil {
		t.Fatalf("insert dependency: %v", err)
	}
	if _, err := db.Exec(`
INSERT INTO workflow_task_status_records (task_id, is_done)
VALUES (?, ?), (?, ?)`,
		"blocker-1", 0, "blocker-2", 1,
	); err != nil {
		t.Fatalf("insert blocker statuses: %v", err)
	}

	queries := New(db)
	for _, testCase := range []struct {
		field     string
		direction string
		want      []string
	}{
		{field: "updated", direction: "asc", want: []string{"task-6", "task-5", "task-4", "task-3", "task-1", "task-2"}},
		{field: "updated", direction: "desc", want: []string{"task-2", "task-1", "task-3", "task-4", "task-5", "task-6"}},
		{field: "created", direction: "asc", want: []string{"task-1", "task-2", "task-3", "task-4", "task-5", "task-6"}},
		{field: "created", direction: "desc", want: []string{"task-6", "task-5", "task-4", "task-3", "task-2", "task-1"}},
		{field: "short_id", direction: "asc", want: []string{"task-1", "task-2", "task-3", "task-4", "task-5", "task-6"}},
		{field: "short_id", direction: "desc", want: []string{"task-6", "task-5", "task-4", "task-3", "task-2", "task-1"}},
		{field: "labels", direction: "asc", want: []string{"task-2", "task-6", "task-1", "task-5", "task-3", "task-4"}},
		{field: "labels", direction: "desc", want: []string{"task-3", "task-5", "task-1", "task-6", "task-2", "task-4"}},
	} {
		t.Run(testCase.field+"_"+testCase.direction, func(t *testing.T) {
			rows, err := queries.ListBoardNodeTasks(context.Background(), boardNodeTasksQueryParams(
				workflowID,
				testCase.field,
				testCase.direction,
				0,
				100,
				sql.NullInt64{},
			))
			if err != nil {
				t.Fatalf("list board node tasks: %v", err)
			}
			got := make([]string, 0, len(rows))
			for _, row := range rows {
				got = append(got, row.ID)
			}
			if !slices.Equal(got, testCase.want) {
				t.Fatalf("task order = %v, want %v", got, testCase.want)
			}
		})
	}

	rows, err := queries.ListBoardNodeTasks(context.Background(), boardNodeTasksQueryParams(
		workflowID,
		"updated",
		"desc",
		2,
		2,
		sql.NullInt64{},
	))
	if err != nil {
		t.Fatalf("list middle board page: %v", err)
	}
	if got := boardTaskIDs(rows); !slices.Equal(got, []string{"task-3", "task-4", "task-5"}) {
		t.Fatalf("middle page = %v, want [task-3 task-4 task-5]", got)
	}
	if rows[0].DependencySatisfiedCount.Int64 != 1 || rows[0].DependencyTotalCount.Int64 != 1 {
		t.Fatalf(
			"middle-page dependency progress = (%d/%d), want (1/1)",
			rows[0].DependencySatisfiedCount.Int64,
			rows[0].DependencyTotalCount.Int64,
		)
	}
	rows, err = queries.ListBoardNodeTasks(context.Background(), boardNodeTasksQueryParams(
		workflowID,
		"updated",
		"desc",
		4,
		2,
		sql.NullInt64{},
	))
	if err != nil {
		t.Fatalf("list final board page: %v", err)
	}
	if got := boardTaskIDs(rows); !slices.Equal(got, []string{"task-5", "task-6"}) {
		t.Fatalf("final page = %v, want [task-5 task-6]", got)
	}

	unblocked := sql.NullInt64{Int64: 1, Valid: true}
	rows, err = queries.ListBoardNodeTasks(context.Background(), boardNodeTasksQueryParams(
		workflowID,
		"labels",
		"asc",
		0,
		100,
		unblocked,
	))
	if err != nil {
		t.Fatalf("list unblocked board page: %v", err)
	}
	if got := boardTaskIDs(rows); !slices.Equal(got, []string{"task-2", "task-6", "task-5", "task-3", "task-4"}) {
		t.Fatalf("unblocked page = %v, want [task-2 task-6 task-5 task-3 task-4]", got)
	}

	params := boardNodeTasksQueryParams(workflowID, "labels", "asc", 0, 100, unblocked)
	params.LabelFilterKind = "named"
	params.LabelFilterMode = "all"
	params.LabelIdsJson = `["label-1"]`
	rows, err = queries.ListBoardNodeTasks(context.Background(), params)
	if err != nil {
		t.Fatalf("list labeled and unblocked board page: %v", err)
	}
	if got := boardTaskIDs(rows); !slices.Equal(got, []string{"task-2", "task-6", "task-5"}) {
		t.Fatalf("labeled and unblocked page = %v, want [task-2 task-6 task-5]", got)
	}
}

func boardTaskIDs(rows []ListBoardNodeTasksRow) []string {
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	return ids
}

func boardNodeTasksQueryParams(
	workflowID runtimeids.WorkflowID,
	sortField string,
	sortDirection string,
	offset int,
	limit int,
	dependencyFilter sql.NullInt64,
) ListBoardNodeTasksParams {
	return ListBoardNodeTasksParams{
		ProjectID:            "project-1",
		WorkflowID:           workflowID,
		LabelFilterKind:      "none",
		LabelFilterMode:      "",
		LabelIdsJson:         "[]",
		ExcludedLabelIdsJson: "[]",
		DependencyFilter:     dependencyFilter,
		NodeID:               boardQueryNodeID,
		SortField:            sortField,
		SortDirection:        sortDirection,
		OffsetRows:           int64(offset),
		LimitRows:            int64(limit),
	}
}
