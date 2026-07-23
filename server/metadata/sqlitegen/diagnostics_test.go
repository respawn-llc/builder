package sqlitegen

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"testing"
)

var (
	errQueryRowsIteration     = errors.New("query rows iteration failed")
	registerFailingRowsDriver sync.Once
)

func TestQueryFailureDiagnosticsRecordArgumentCountWithoutValues(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	output := captureQueryFailureDiagnostics(t)

	_, err = New(db).GetTaskRun(
		WithQueryFailureDiagnostics(context.Background()),
		"repository-secret",
	)
	if err == nil {
		t.Fatal("GetTaskRun unexpectedly succeeded without its table")
	}

	var entry map[string]any
	if err := json.NewDecoder(output).Decode(&entry); err != nil {
		t.Fatalf("decode diagnostic log: %v", err)
	}
	if _, present := entry["arguments"]; present {
		t.Fatalf("diagnostic log exposed raw arguments: %+v", entry)
	}
	if got, ok := entry["argument_count"].(float64); !ok || got != 1 {
		t.Fatalf("diagnostic argument count = %#v, want 1", entry["argument_count"])
	}
}

func TestQueryRowsIterationFailureRecordsDiagnostics(t *testing.T) {
	const driverName = "kent_sqlitegen_failing_rows"
	registerFailingRowsDriver.Do(func() {
		sql.Register(driverName, failingRowsDriver{})
	})
	db, err := sql.Open(driverName, "")
	if err != nil {
		t.Fatalf("open failing rows database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	output := captureQueryFailureDiagnostics(t)
	_, err = New(db).ListProjectLabels(
		WithQueryFailureDiagnostics(context.Background()),
		"project-secret",
	)
	if !errors.Is(err, errQueryRowsIteration) {
		t.Fatalf("ListProjectLabels error = %v, want iteration failure", err)
	}

	var entry map[string]any
	if err := json.NewDecoder(output).Decode(&entry); err != nil {
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

func captureQueryFailureDiagnostics(t *testing.T) *bytes.Buffer {
	t.Helper()
	var output bytes.Buffer
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previousLogger) })
	return &output
}

type failingRowsDriver struct{}

func (failingRowsDriver) Open(string) (driver.Conn, error) {
	return failingRowsConnection{}, nil
}

type failingRowsConnection struct{}

func (failingRowsConnection) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepare is unsupported")
}

func (failingRowsConnection) Close() error {
	return nil
}

func (failingRowsConnection) Begin() (driver.Tx, error) {
	return nil, errors.New("transactions are unsupported")
}

func (failingRowsConnection) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	return failingRows{}, nil
}

type failingRows struct{}

func (failingRows) Columns() []string {
	return []string{"id", "name"}
}

func (failingRows) Close() error {
	return nil
}

func (failingRows) Next([]driver.Value) error {
	return errQueryRowsIteration
}
