package sqlitegen

import (
	"testing"
)

func TestListBoardColumnTaskCountsUsesLabelIndexes(t *testing.T) {
	db := openSQLiteFixture(t, ":memory:")
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`
CREATE TABLE tasks (
	id TEXT PRIMARY KEY,
	project_id TEXT NOT NULL,
	project_workflow_link_id TEXT NOT NULL,
	workflow_id TEXT NOT NULL,
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
CREATE INDEX tasks_project_workflow_updated_idx
	ON tasks(project_id, workflow_id, updated_at_unix_ms DESC, id DESC);
CREATE VIEW task_records AS SELECT * FROM tasks;
CREATE TABLE task_current_nodes (
	task_id TEXT NOT NULL,
	node_id TEXT NOT NULL
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
CREATE TABLE task_label_assignments (
	task_id TEXT NOT NULL,
	label_id TEXT NOT NULL,
	PRIMARY KEY (task_id, label_id)
);
CREATE INDEX task_label_assignments_label_task_idx
	ON task_label_assignments(label_id, task_id);`); err != nil {
		t.Fatalf("create query-plan fixture: %v", err)
	}

	requireQueryUsesIndex(t, db, listBoardColumnTaskCounts, "sqlite_autoindex_task_label_assignments_1", "none", "", "[]", "[]", "project-1", "workflow-1", "node-done")
	requireQueryUsesIndex(t, db, listBoardColumnTaskCounts, "task_label_assignments_label_task_idx", "none", "", "[]", "[]", "project-1", "workflow-1", "node-done")
}
