package sqlitegen

import (
	"context"
	"log/slog"
	"runtime/debug"
)

type queryFailureDiagnosticsContextKey struct{}

func WithQueryFailureDiagnostics(ctx context.Context) context.Context {
	return context.WithValue(ctx, queryFailureDiagnosticsContextKey{}, true)
}

// recordQueryError emits enough context to diagnose database failures while
// returning the original error unchanged. Some existing callers compare
// sql.ErrNoRows by identity, so wrapping it would alter control flow.
func recordQueryError(ctx context.Context, cause error, query string, arguments ...any) error {
	if cause == nil {
		return nil
	}
	if ctx == nil || ctx.Value(queryFailureDiagnosticsContextKey{}) != true {
		return cause
	}
	slog.Error(
		"sqlite query failed",
		"error", cause,
		"sql", query,
		"arguments", arguments,
		"stack", string(debug.Stack()),
	)
	return cause
}
