package sqlitegen

import (
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

func TestUpdatedBoardQueriesUseTaskSequenceUpdatedIndexWithoutTemporarySort(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`
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
CREATE INDEX tasks_project_workflow_link_updated_idx
	ON tasks(project_workflow_link_id, updated_at_unix_ms DESC, task_seq DESC);
CREATE TABLE project_workflow_links (
	id TEXT PRIMARY KEY,
	project_id TEXT NOT NULL,
	workflow_id TEXT NOT NULL
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
	ON task_label_assignments(label_id, task_id);
CREATE TABLE project_labels (
	id TEXT PRIMARY KEY,
	project_id TEXT NOT NULL,
	name TEXT NOT NULL,
	ordinal INTEGER NOT NULL
);`); err != nil {
		t.Fatalf("create query-plan fixture: %v", err)
	}

	tests := []struct {
		name  string
		query string
		args  []any
	}{
		{
			name:  "updated desc first",
			query: listBoardNodeTasksUpdatedDesc,
			args:  updatedBoardQueryArgs(nil, "older", nil),
		},
		{
			name:  "updated desc next",
			query: listBoardNodeTasksUpdatedDesc,
			args:  updatedBoardQueryArgs(int64(2), "older", int64(100)),
		},
		{
			name:  "updated desc previous",
			query: listBoardNodeTasksUpdatedDescPrevious,
			args:  updatedPreviousBoardQueryArgs(int64(2), int64(100)),
		},
		{
			name:  "updated asc first",
			query: listBoardNodeTasksUpdatedAsc,
			args:  updatedBoardQueryArgs(nil, "older", nil),
		},
		{
			name:  "updated asc next",
			query: listBoardNodeTasksUpdatedAsc,
			args:  updatedBoardQueryArgs(int64(2), "older", int64(100)),
		},
		{
			name:  "updated asc previous",
			query: listBoardNodeTasksUpdatedAscPrevious,
			args:  updatedPreviousBoardQueryArgs(int64(2), int64(100)),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			requireQueryUsesIndexWithoutSort(
				t,
				db,
				test.query,
				"tasks_project_workflow_link_updated_idx",
				test.args...,
			)
		})
	}
}

func updatedBoardQueryArgs(cursorTaskSeq any, direction string, cursorUpdatedAt any) []any {
	return []any{
		"project-1",
		"workflow-1",
		"project-workflow-link-1",
		"none",
		"",
		"[]",
		"[]",
		"node-1",
		cursorTaskSeq,
		direction,
		cursorUpdatedAt,
		int64(26),
	}
}

func updatedPreviousBoardQueryArgs(cursorTaskSeq any, cursorUpdatedAt any) []any {
	return []any{
		"project-1",
		"workflow-1",
		"project-workflow-link-1",
		"none",
		"",
		"[]",
		"[]",
		"node-1",
		cursorTaskSeq,
		cursorUpdatedAt,
		int64(26),
	}
}
