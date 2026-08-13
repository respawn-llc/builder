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
	if err := s.projectSessionAppend(ctx, projection); err != nil {
		slog.ErrorContext(ctx, "Session append metadata projection failed",
			"session_id", projection.SessionID.String(),
			"first_sequence", projection.FirstSequence,
			"last_sequence", projection.LastSequence,
			"error", err,
		)
	}
	return nil
}

func (s *Store) projectSessionAppend(ctx context.Context, projection session.AppendProjection) error {
	if s == nil || s.db == nil {
		return nil
	}
	connection, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire metadata connection for Session append projection: %w", err)
	}
	defer func() { _ = connection.Close() }()

	lifecycle := sqlitelifecyclegen.New(connection)
	if err := lifecycle.BeginImmediate(ctx); err != nil {
		return fmt.Errorf("begin Session append projection: %w", err)
	}
	settled := false
	defer func() {
		if !settled {
			_ = lifecycle.Rollback(context.Background())
		}
	}()

	var preview sql.NullString
	if projection.FirstPromptPreview != nil {
		preview = sql.NullString{String: *projection.FirstPromptPreview, Valid: true}
	}
	affected, err := sqlitegen.New(connection).ProjectSessionAppend(ctx, sqlitegen.ProjectSessionAppendParams{
		FirstPromptPreview:              preview,
		UpdatedAtUnixMs:                 projection.AppendedAt.UTC().UnixMilli(),
		ConversationEstablished:         boolToInt64(projection.ConversationEstablished),
		GeneratedRecoveredWarningIssued: boolToInt64(projection.GeneratedRecoveredWarningIssued),
		ID:                              projection.SessionID.String(),
	})
	if err != nil {
		return fmt.Errorf("update Session append projection: %w", err)
	}
	if affected != 1 {
		return fmt.Errorf("update Session append projection affected %d rows, want 1", affected)
	}
	if err := lifecycle.Commit(ctx); err != nil {
		return fmt.Errorf("commit Session append projection: %w", err)
	}
	settled = true
	return nil
}

func boolToInt64(value bool) int64 {
	if value {
		return 1
	}
	return 0
}
