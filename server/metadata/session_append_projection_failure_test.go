package metadata

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"core/server/session"
	"core/shared/runtimeids"
	sqlite3 "modernc.org/sqlite/lib"
)

type recordingFatalReporter struct {
	failures []*ClassifiedFailure
}

type projectionLogRecords struct {
	mu      sync.Mutex
	records []map[string]any
}

type projectionLogHandler struct {
	records *projectionLogRecords
}

func (projectionLogHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h projectionLogHandler) Handle(_ context.Context, record slog.Record) error {
	fields := make(map[string]any, record.NumAttrs())
	record.Attrs(func(attr slog.Attr) bool {
		fields[attr.Key] = attr.Value.Resolve().Any()
		return true
	})
	h.records.mu.Lock()
	h.records.records = append(h.records.records, fields)
	h.records.mu.Unlock()
	return nil
}
func (h projectionLogHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h projectionLogHandler) WithGroup(string) slog.Handler      { return h }

func captureProjectionLogs(t *testing.T) *projectionLogRecords {
	t.Helper()
	records := &projectionLogRecords{}
	previous := slog.Default()
	slog.SetDefault(slog.New(projectionLogHandler{records: records}))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return records
}

func (r *projectionLogRecords) snapshot() []map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]map[string]any(nil), r.records...)
}

type projectionSQLiteCodeError struct {
	code int
}

func (e projectionSQLiteCodeError) Error() string { return "structured SQLite failure" }
func (e projectionSQLiteCodeError) Code() int     { return e.code }

func (r *recordingFatalReporter) ReportMetadataFatal(failure *ClassifiedFailure) bool {
	if len(r.failures) != 0 {
		return false
	}
	r.failures = append(r.failures, failure)
	return true
}

func TestSessionAppendProjectionOperationClassificationAggregatesRollbackOnce(t *testing.T) {
	primary := projectionSQLiteCodeError{code: sqlite3.SQLITE_BUSY}
	rollback := projectionSQLiteCodeError{code: sqlite3.SQLITE_IOERR_ROLLBACK_ATOMIC}
	failure := ClassifyOperationFailure(
		context.Background(),
		"Session append projection",
		"/metadata/main.sqlite3",
		primary,
		rollback,
	)
	if failure.Class != FailureCritical {
		t.Fatalf("aggregate class = %v, want critical", failure.Class)
	}
	if !errors.Is(failure, primary) || !errors.Is(failure, rollback) {
		t.Fatalf("aggregate does not retain primary and rollback causes: %#v", failure)
	}
	if got := failure.Error(); !strings.Contains(got, rollback.Error()) {
		t.Fatalf("aggregate diagnostic %q omits rollback cause", got)
	}
	if failure.SQLite == nil ||
		failure.SQLite.Primary != sqlite3.SQLITE_BUSY ||
		failure.SQLite.Extended != sqlite3.SQLITE_BUSY {
		t.Fatalf("primary SQLite classification = %#v, want BUSY", failure.SQLite)
	}
}

func (r *recordingFatalReporter) MetadataFatal() *ClassifiedFailure {
	if len(r.failures) == 0 {
		return nil
	}
	return r.failures[0]
}

func TestSessionAppendProjectionSignalsCriticalFailureOnceAndReturnsNil(t *testing.T) {
	logs := captureProjectionLogs(t)
	store := openInMemoryMetadataTestStore(t, t.TempDir())
	reporter := &recordingFatalReporter{}
	store.fatalReporter = reporter
	store.databasePath = filepath.Join(store.persistenceRoot, "main.sqlite3")
	if err := os.WriteFile(store.databasePath, []byte("not a SQLite database"), 0o600); err != nil {
		t.Fatalf("write invalid metadata database: %v", err)
	}
	if err := store.db.Close(); err != nil {
		t.Fatalf("close metadata database: %v", err)
	}
	dsn, err := metadataSQLiteReadOnlyDSN(store.databasePath)
	if err != nil {
		t.Fatalf("metadataSQLiteReadOnlyDSN: %v", err)
	}
	store.db, err = sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open read-only metadata database: %v", err)
	}
	projection := session.AppendProjection{
		SessionID:     runtimeids.NewSessionID(),
		FirstSequence: 4,
		LastSequence:  6,
		AppendedAt:    time.Now().UTC(),
	}

	if err := store.observeSessionAppend(context.Background(), projection); err != nil {
		t.Fatalf("observeSessionAppend error = %v, want nil", err)
	}
	if len(reporter.failures) != 1 {
		t.Fatalf("fatal submissions = %d, want 1", len(reporter.failures))
	}
	if reporter.failures[0].Class != FailureCritical ||
		reporter.failures[0].Operation != "Session append projection" {
		t.Fatalf("submitted failure = %#v", reporter.failures[0])
	}
	if err := store.observeSessionAppend(context.Background(), projection); err != nil {
		t.Fatalf("second observeSessionAppend error = %v, want nil", err)
	}
	if len(reporter.failures) != 1 {
		t.Fatalf("fatal submissions after second failure = %d, want 1", len(reporter.failures))
	}
	if got := len(logs.snapshot()); got != 0 {
		t.Fatalf("critical projection diagnostics = %d, want 0", got)
	}
}

func TestSessionAppendProjectionLogsNoncriticalFailureOnceAndReturnsNil(t *testing.T) {
	logs := captureProjectionLogs(t)
	store := openInMemoryMetadataTestStore(t, t.TempDir())
	reporter := &recordingFatalReporter{}
	store.fatalReporter = reporter
	store.databasePath = "/metadata/main.sqlite3"
	projection := session.AppendProjection{
		SessionID:     runtimeids.NewSessionID(),
		FirstSequence: 9,
		LastSequence:  11,
		AppendedAt:    time.Now().UTC(),
	}

	if err := store.observeSessionAppend(context.Background(), projection); err != nil {
		t.Fatalf("observeSessionAppend error = %v, want nil", err)
	}
	if len(reporter.failures) != 0 {
		t.Fatalf("fatal submissions = %d, want 0", len(reporter.failures))
	}
	records := logs.snapshot()
	if len(records) != 1 {
		t.Fatalf("projection diagnostics = %d, want 1", len(records))
	}
	fields := records[0]
	if fields["operation"] != "Session append projection" ||
		fields["database_path"] != store.databasePath ||
		fields["session_id"] != projection.SessionID.String() ||
		fields["first_sequence"] != projection.FirstSequence ||
		fields["last_sequence"] != projection.LastSequence {
		t.Fatalf("projection diagnostic fields = %#v", fields)
	}
}
