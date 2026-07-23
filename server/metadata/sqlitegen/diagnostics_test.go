package sqlitegen

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"testing"
)

func TestQueryFailureDiagnosticsRecordArgumentCountWithoutValues(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	var output bytes.Buffer
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previousLogger) })

	_, err = New(db).GetTaskRun(
		WithQueryFailureDiagnostics(context.Background()),
		"repository-secret",
	)
	if err == nil {
		t.Fatal("GetTaskRun unexpectedly succeeded without its table")
	}

	var entry map[string]any
	if err := json.NewDecoder(&output).Decode(&entry); err != nil {
		t.Fatalf("decode diagnostic log: %v", err)
	}
	if _, present := entry["arguments"]; present {
		t.Fatalf("diagnostic log exposed raw arguments: %+v", entry)
	}
	if got, ok := entry["argument_count"].(float64); !ok || got != 1 {
		t.Fatalf("diagnostic argument count = %#v, want 1", entry["argument_count"])
	}
}

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
