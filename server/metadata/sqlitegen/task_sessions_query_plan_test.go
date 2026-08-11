package sqlitegen

import (
	"database/sql"
	"testing"
)

const taskSessionAssociationRecencyIndex = "session_workflow_node_associations_session_recency_idx"

func TestTaskSessionQueriesUseBoundedOrderingIndexesWithoutSorting(t *testing.T) {
	db := openSQLiteFixture(t, ":memory:")
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`
CREATE TABLE sessions (
	id TEXT PRIMARY KEY,
	task_id TEXT,
	name TEXT NOT NULL,
	continuation_json TEXT NOT NULL,
	created_at_unix_ms INTEGER NOT NULL
);
CREATE INDEX sessions_task_activity_idx
	ON sessions(task_id, created_at_unix_ms DESC, CAST('session_started:' || id AS TEXT) DESC)
	WHERE task_id IS NOT NULL;
CREATE TABLE workflow_nodes (
	id TEXT PRIMARY KEY,
	display_name TEXT NOT NULL
);
CREATE TABLE session_workflow_node_associations (
	session_id TEXT NOT NULL,
	node_id TEXT NOT NULL,
	transition_branch_key TEXT,
	associated_at_unix_ms INTEGER NOT NULL
);
CREATE INDEX session_workflow_node_associations_session_recency_idx
	ON session_workflow_node_associations(
		session_id,
		associated_at_unix_ms DESC,
		node_id DESC
	);`); err != nil {
		t.Fatalf("create query-plan fixture: %v", err)
	}

	t.Run("Idle page", func(t *testing.T) {
		args := []any{
			sql.NullString{String: "task-1", Valid: true},
			"[]",
			int64(0),
			int64(101),
		}
		requireQueryUsesIndexWithoutSorter(
			t,
			db,
			listIdleWorkflowTaskSessions,
			"sessions_task_activity_idx",
			args...,
		)
		requireQueryUsesIndexWithoutSorter(
			t,
			db,
			listIdleWorkflowTaskSessions,
			taskSessionAssociationRecencyIndex,
			args...,
		)
	})

	t.Run("active selection", func(t *testing.T) {
		args := []any{
			sql.NullString{String: "task-1", Valid: true},
			`["session-1"]`,
		}
		requireQueryUsesIndexWithoutSorter(
			t,
			db,
			listActiveWorkflowTaskSessions,
			taskSessionAssociationRecencyIndex,
			args...,
		)
	})
}
