package sqlitegen_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"core/internal/testharness/testsetup"
	"core/server/metadata/sqlitegen"
)

type noRowsQueryDB struct {
	*sql.DB
}

func (db noRowsQueryDB) QueryRowContext(ctx context.Context, _ string, _ ...interface{}) *sql.Row {
	return db.DB.QueryRowContext(ctx, "SELECT 1 WHERE 0")
}

type queryErrorDB struct {
	*sql.DB
}

func (db queryErrorDB) QueryRowContext(ctx context.Context, _ string, _ ...interface{}) *sql.Row {
	return db.DB.QueryRowContext(ctx, "SELECT no_such_function()")
}

func TestGeneratedSingleRowNoRowsDiagnosticsArePolicyScoped(t *testing.T) {
	if err := sqlitegen.RegisterSQLiteExtensions(); err != nil {
		t.Fatalf("register metadata SQLite extensions: %v", err)
	}
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	diagnostics := testsetup.CaptureSlog(t)
	ctx := sqlitegen.WithQueryFailureDiagnostics(context.Background())
	queries := sqlitegen.New(noRowsQueryDB{DB: db})

	if _, err := queries.GetTask(ctx, "missing"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("unexpected generated no-row error = %v, want sql.ErrNoRows", err)
	}
	if diagnostics.Len() == 0 {
		t.Fatal("unexpected generated no-row failure emitted no diagnostics")
	}

	diagnostics.Reset()
	if _, err := queries.GetTask(sqlitegen.WithExpectedNoRows(ctx), "missing"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected generated no-row error = %v, want sql.ErrNoRows", err)
	}
	if diagnostics.Len() != 0 {
		t.Fatalf("expected optional no-row diagnostics = %q, want none", diagnostics.String())
	}

	diagnostics.Reset()
	queries = sqlitegen.New(queryErrorDB{DB: db})
	if _, err := queries.GetTask(sqlitegen.WithExpectedNoRows(ctx), "unexpected"); err == nil {
		t.Fatal("unexpected generated query failure returned nil error")
	}
	if diagnostics.Len() == 0 {
		t.Fatal("unexpected generated query failure emitted no diagnostics")
	}
}
