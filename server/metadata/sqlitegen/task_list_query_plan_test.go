package sqlitegen

import (
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

func TestListWorkflowTaskListRowsUsesProjectLinkAndTaskIndexes(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`
CREATE TABLE workflows (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL
);
CREATE TABLE project_workflow_links (
	id TEXT PRIMARY KEY,
	project_id TEXT NOT NULL,
	workflow_id TEXT NOT NULL,
	UNIQUE (project_id, workflow_id)
);
CREATE TABLE tasks (
	id TEXT PRIMARY KEY,
	project_workflow_link_id TEXT NOT NULL,
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
CREATE INDEX tasks_project_workflow_link_idx
	ON tasks(project_workflow_link_id);
CREATE VIEW task_records AS
SELECT
	t.id,
	pwl.project_id,
	t.project_workflow_link_id,
	pwl.workflow_id,
	t.workflow_revision_seen,
	t.task_seq,
	t.short_id,
	t.title,
	t.body,
	t.source_url,
	t.source_workspace_id,
	t.managed_worktree_id,
	t.execution_target_mode,
	t.execution_target_requested_ref,
	t.execution_target_resolved_ref,
	t.execution_target_commit_oid,
	t.execution_target_provenance,
	t.created_at_unix_ms,
	t.updated_at_unix_ms,
	t.metadata_json
FROM tasks t
JOIN project_workflow_links pwl ON pwl.id = t.project_workflow_link_id;
CREATE TABLE workflow_task_status_records (
	task_id TEXT PRIMARY KEY,
	is_done INTEGER NOT NULL,
	kind TEXT NOT NULL,
	primary_status_rank INTEGER NOT NULL,
	node_ids_json TEXT NOT NULL,
	attention_types_json TEXT NOT NULL
);
CREATE TABLE task_current_nodes (
	task_id TEXT NOT NULL,
	node_id TEXT NOT NULL
);
CREATE TABLE task_label_assignments (
	task_id TEXT NOT NULL,
	label_id TEXT NOT NULL,
	PRIMARY KEY (task_id, label_id)
);
CREATE INDEX task_label_assignments_label_task_idx
	ON task_label_assignments(label_id, task_id);`); err != nil {
		t.Fatalf("create query-plan fixture: %v", err)
	}

	args := []any{
		"project-1",
		nil,
		nil,
		nil,
		int64(0),
		nil,
		nil,
		"[]",
		int64(0),
		"[]",
		"none",
		"",
		"[]",
		int64(0),
		int64(0),
		int64(0),
		int64(0),
		int64(0),
		int64(0),
		"",
		"",
		"updated",
		int64(1),
		"title",
		int64(0),
		"",
		int64(0),
		"",
		int64(0),
		"",
		int64(0),
		int64(101),
	}
	requireQueryUsesAnyTableIndex(t, db, listWorkflowTaskListRows, "project_workflow_links", args...)
	requireQueryUsesIndex(t, db, listWorkflowTaskListRows, "tasks_project_workflow_link_idx", args...)
	requireQueryUsesIndex(t, db, listWorkflowTaskListRows, "sqlite_autoindex_task_label_assignments_1", args...)
	requireQueryUsesIndex(t, db, listWorkflowTaskListRows, "task_label_assignments_label_task_idx", args...)
	requireQueryPlanDoesNotGroupIntoTemporaryTree(t, db, listWorkflowTaskListRows, args...)
}

func requireQueryPlanDoesNotGroupIntoTemporaryTree(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	rows, err := db.Query("EXPLAIN QUERY PLAN "+query, args...)
	if err != nil {
		t.Fatalf("explain query plan: %v", err)
	}
	defer closeQueryRows(t, rows)
	groupingStructures := 0
	for rows.Next() {
		var id, parent, unused int64
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatalf("scan query plan: %v", err)
		}
		if detail == "USE TEMP B-TREE FOR GROUP BY" {
			groupingStructures++
		}
		if detail == "MATERIALIZE selected_rows" {
			t.Fatalf("task-list eligibility authority was materialized before pagination")
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate query plan: %v", err)
	}
	if groupingStructures > 1 {
		t.Fatalf("task-list cardinality introduced an extra unbounded grouping structure: count=%d", groupingStructures)
	}
}
