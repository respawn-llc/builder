package sqlitegen

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"core/server/internal/testsupport"
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
	db := openSQLiteFixture(t, ":memory:")
	t.Cleanup(func() { _ = db.Close() })
	diagnostics := testsupport.CaptureSlog(t)
	ctx := WithQueryFailureDiagnostics(context.Background())
	queries := New(noRowsQueryDB{DB: db})

	if _, err := queries.GetTask(ctx, "missing"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("unexpected generated no-row error = %v, want sql.ErrNoRows", err)
	}
	if diagnostics.Len() == 0 {
		t.Fatal("unexpected generated no-row failure emitted no diagnostics")
	}

	diagnostics.Reset()
	if _, err := queries.GetTask(WithExpectedNoRows(ctx), "missing"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected generated no-row error = %v, want sql.ErrNoRows", err)
	}
	if diagnostics.Len() != 0 {
		t.Fatalf("expected optional no-row diagnostics = %q, want none", diagnostics.String())
	}

	diagnostics.Reset()
	queries = New(queryErrorDB{DB: db})
	if _, err := queries.GetTask(WithExpectedNoRows(ctx), "unexpected"); err == nil {
		t.Fatal("unexpected generated query failure returned nil error")
	}
	if diagnostics.Len() == 0 {
		t.Fatal("unexpected generated query failure emitted no diagnostics")
	}
}
