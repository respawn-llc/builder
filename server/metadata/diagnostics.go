package metadata

import (
	"context"

	"core/server/metadata/sqlitegen"
)

// WithQueryFailureDiagnostics enables query and stack diagnostics for database
// errors executed under ctx.
func WithQueryFailureDiagnostics(ctx context.Context) context.Context {
	return sqlitegen.WithQueryFailureDiagnostics(ctx)
}
