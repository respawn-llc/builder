package metadata

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"

	"core/server/metadata/sqlitegen"
	"core/server/metadata/sqlitelifecyclegen"
	"core/server/session"
)

func (s *Store) observeSessionAppend(ctx context.Context, projection session.AppendProjection) error {
	failure := s.projectSessionAppend(ctx, projection)
	if failure == nil {
		return nil
	}
	if failure.Class == FailureCritical && s.fatalReporter != nil {
		s.fatalReporter.ReportMetadataFatal(failure)
		return nil
	}
	slog.ErrorContext(ctx, "Session append metadata projection failed",
		"operation", failure.Operation,
		"database_path", failure.DatabasePath,
		"session_id", projection.SessionID.String(),
		"first_sequence", projection.FirstSequence,
		"last_sequence", projection.LastSequence,
		"sqlite", failure.SQLite,
		"error", failure,
	)
	return nil
}

func (s *Store) projectSessionAppend(ctx context.Context, projection session.AppendProjection) *ClassifiedFailure {
	if s == nil || s.db == nil {
		return nil
	}
	if s.fatalReporter != nil {
		if fatal := s.fatalReporter.MetadataFatal(); fatal != nil {
			return fatal
		}
	}
	connection, err := s.db.Conn(ctx)
	if err != nil {
		return ClassifyOperationFailure(ctx, "Session append projection", s.databasePath, err, nil)
	}
	defer func() { _ = connection.Close() }()

	lifecycle := sqlitelifecyclegen.New(connection)
	if err := lifecycle.BeginImmediate(ctx); err != nil {
		return ClassifyOperationFailure(ctx, "Session append projection", s.databasePath, err, nil)
	}

	var preview sql.NullString
	if projection.FirstPromptPreview != nil {
		preview = sql.NullString{String: *projection.FirstPromptPreview, Valid: true}
	}
	affected, err := sqlitegen.NewRaw(connection).ProjectSessionAppend(ctx, sqlitegen.ProjectSessionAppendParams{
		FirstPromptPreview:              preview,
		UpdatedAtUnixMs:                 projection.AppendedAt.UTC().UnixMilli(),
		ConversationEstablished:         boolToInt64(projection.ConversationEstablished),
		GeneratedRecoveredWarningIssued: boolToInt64(projection.GeneratedRecoveredWarningIssued),
		ID:                              projection.SessionID.String(),
	})
	if err != nil {
		rollbackErr := lifecycle.Rollback(context.Background())
		return ClassifyOperationFailure(ctx, "Session append projection", s.databasePath, err, rollbackErr)
	}
	if affected != 1 {
		cause := fmt.Errorf("update Session append projection affected %d rows, want 1", affected)
		rollbackErr := lifecycle.Rollback(context.Background())
		return ClassifyOperationFailure(ctx, "Session append projection", s.databasePath, cause, rollbackErr)
	}
	if err := lifecycle.Commit(ctx); err != nil {
		rollbackErr := lifecycle.Rollback(context.Background())
		return ClassifyOperationFailure(ctx, "Session append projection", s.databasePath, err, rollbackErr)
	}
	return nil
}

func boolToInt64(value bool) int64 {
	if value {
		return 1
	}
	return 0
}
