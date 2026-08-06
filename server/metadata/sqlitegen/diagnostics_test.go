package sqlitegen

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"testing"
)

func TestRecordQueryErrorSkipsMissingRowsButReportsDatabaseFailures(t *testing.T) {
	var diagnostics bytes.Buffer
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&diagnostics, nil)))
	t.Cleanup(func() { slog.SetDefault(previousLogger) })

	ctx := WithQueryFailureDiagnostics(context.Background())
	missing := errors.New("retained association absent: " + sql.ErrNoRows.Error())
	missing = errors.Join(missing, sql.ErrNoRows)
	if got := recordQueryError(ctx, missing, "missing-association", 1); got != missing {
		t.Fatalf("missing-row error = %v, want original error", got)
	}
	if diagnostics.Len() != 0 {
		t.Fatalf("missing-row diagnostics = %q, want none", diagnostics.String())
	}

	operational := errors.New("database unavailable")
	if got := recordQueryError(ctx, operational, "operational-query", 1); got != operational {
		t.Fatalf("operational error = %v, want original error", got)
	}
	if diagnostics.Len() == 0 {
		t.Fatal("operational query failure emitted no diagnostics")
	}
}
