package sqlitegen

import (
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

func TestListBoardNodeTasksUsesIndexedOrderingInBothDirections(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
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
	canceled_at_unix_ms INTEGER,
	cancellation_reason TEXT,
	created_at_unix_ms INTEGER NOT NULL,
	updated_at_unix_ms INTEGER NOT NULL,
	metadata_json TEXT NOT NULL
);
CREATE INDEX tasks_project_workflow_updated_idx
	ON tasks(project_id, workflow_id, updated_at_unix_ms DESC, id DESC);
CREATE VIEW task_records AS SELECT * FROM tasks;
CREATE TABLE task_node_placements (
	task_id TEXT NOT NULL,
	node_id TEXT,
	state TEXT NOT NULL
);
CREATE TABLE workflow_nodes (
	id TEXT PRIMARY KEY,
	kind TEXT NOT NULL
);
CREATE TABLE task_transition_records (
	task_id TEXT NOT NULL,
	source_node_id TEXT,
	state TEXT NOT NULL
);`); err != nil {
		t.Fatalf("create query-plan fixture: %v", err)
	}

	for _, direction := range []string{"older", "newer"} {
		t.Run(direction, func(t *testing.T) {
			requireQueryUsesIndexWithoutSort(
				t,
				db,
				listBoardNodeTasks,
				"tasks_project_workflow_updated_idx",
				"project-1",
				"workflow-1",
				"node-1",
				"node-done",
				direction,
				int64(100),
				"task-anchor",
				int64(26),
			)
		})
	}
}
