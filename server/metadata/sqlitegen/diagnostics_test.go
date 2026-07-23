package sqlitegen

import (
	"context"
	"database/sql"
	"testing"
)

func TestGetTaskRunNoRowsPreservesSentinelIdentity(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`
CREATE TABLE task_run_records (
    id TEXT PRIMARY KEY,
    task_id TEXT NOT NULL,
    placement_id TEXT NOT NULL,
    node_id TEXT NOT NULL,
    session_id TEXT NOT NULL,
    run_generation INTEGER NOT NULL,
    workflow_revision_seen INTEGER NOT NULL,
    automation_requested_at_unix_ms INTEGER,
    created_at_unix_ms INTEGER NOT NULL,
    updated_at_unix_ms INTEGER NOT NULL,
    started_at_unix_ms INTEGER,
    completed_at_unix_ms INTEGER,
    interrupted_at_unix_ms INTEGER,
    interruption_reason TEXT NOT NULL,
    interruption_detail_json TEXT NOT NULL,
    waiting_ask_id TEXT,
    effective_completion_mode TEXT,
    invalid_completion_count INTEGER NOT NULL,
    run_start_snapshot_json TEXT NOT NULL,
    metadata_json TEXT NOT NULL
);`); err != nil {
		t.Fatalf("create task run table: %v", err)
	}

	_, err = New(db).GetTaskRun(context.Background(), "run-missing")
	if err != sql.ErrNoRows {
		t.Fatalf("GetTaskRun error = %v, want the sql.ErrNoRows sentinel", err)
	}
}
