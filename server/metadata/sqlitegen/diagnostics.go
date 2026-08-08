package sqlitegen

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"runtime/debug"
)

type queryFailureDiagnosticsContextKey struct{}
type expectedNoRowsDiagnosticsContextKey struct{}

func WithQueryFailureDiagnostics(ctx context.Context) context.Context {
	return context.WithValue(ctx, queryFailureDiagnosticsContextKey{}, true)
}

// WithExpectedNoRows marks an operation where sql.ErrNoRows is an expected
// optional absence. Other query failures remain eligible for diagnostics.
func WithExpectedNoRows(ctx context.Context) context.Context {
	return context.WithValue(ctx, expectedNoRowsDiagnosticsContextKey{}, true)
}

// recordQueryError emits enough context to diagnose database failures while
// returning the original error unchanged. Some existing callers compare
// sql.ErrNoRows by identity, so wrapping it would alter control flow.
func recordQueryError(ctx context.Context, cause error, query string, argumentCount int) error {
	if cause == nil {
		return nil
	}
	if errors.Is(cause, sql.ErrNoRows) &&
		ctx != nil &&
		ctx.Value(expectedNoRowsDiagnosticsContextKey{}) == true {
		return cause
	}
	if ctx == nil || ctx.Value(queryFailureDiagnosticsContextKey{}) != true {
		return cause
	}
	slog.Error(
		"sqlite query failed",
		"error", cause,
		"sql", query,
		"argument_count", argumentCount,
		"stack", string(debug.Stack()),
	)
	return cause
}
