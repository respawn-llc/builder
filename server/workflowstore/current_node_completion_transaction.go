package workflowstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"core/server/metadata/sqlitegen"
	"core/server/metadata/sqlitelifecyclegen"
)

type currentNodeCompletionTransaction struct {
	connection *sql.Conn
	lifecycle  *sqlitelifecyclegen.Queries
	queries    *sqlitegen.Queries
	done       bool
}

func beginCurrentNodeCompletionTransaction(ctx context.Context, db *sql.DB) (*currentNodeCompletionTransaction, error) {
	if db == nil {
		return nil, errors.New("metadata database is required")
	}
	connection, err := db.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire current node completion connection: %w", err)
	}
	lifecycle := sqlitelifecyclegen.New(connection)
	if err := lifecycle.SetBusyTimeout15Seconds(ctx); err != nil {
		_ = connection.Close()
		return nil, fmt.Errorf("configure current node completion busy timeout: %w", err)
	}
	if err := lifecycle.BeginImmediate(ctx); err != nil {
		cleanupErr := resetCurrentNodeCompletionConnection(lifecycle, connection)
		return nil, errors.Join(
			fmt.Errorf("begin current node completion transaction: %w", err),
			cleanupErr,
		)
	}
	return &currentNodeCompletionTransaction{
		connection: connection,
		lifecycle:  lifecycle,
		queries:    sqlitegen.New(connection),
	}, nil
}

func (t *currentNodeCompletionTransaction) Queries() *sqlitegen.Queries {
	if t == nil {
		return nil
	}
	return t.queries
}

func (t *currentNodeCompletionTransaction) Commit(ctx context.Context) error {
	if t == nil || t.connection == nil || t.done {
		return sql.ErrTxDone
	}
	t.done = true
	if err := t.lifecycle.Commit(ctx); err != nil {
		rollbackErr := t.lifecycle.Rollback(context.Background())
		cleanupErr := resetCurrentNodeCompletionConnection(t.lifecycle, t.connection)
		return errors.Join(
			fmt.Errorf("commit current node completion transaction: %w", err),
			wrapCurrentNodeCompletionCleanupError("rollback after commit failure", rollbackErr),
			cleanupErr,
		)
	}
	return resetCurrentNodeCompletionConnection(t.lifecycle, t.connection)
}

func (t *currentNodeCompletionTransaction) Rollback() error {
	if t == nil || t.connection == nil || t.done {
		return nil
	}
	t.done = true
	rollbackErr := t.lifecycle.Rollback(context.Background())
	cleanupErr := resetCurrentNodeCompletionConnection(t.lifecycle, t.connection)
	return errors.Join(
		wrapCurrentNodeCompletionCleanupError("rollback current node completion transaction", rollbackErr),
		cleanupErr,
	)
}

func resetCurrentNodeCompletionConnection(lifecycle *sqlitelifecyclegen.Queries, connection *sql.Conn) error {
	resetErr := lifecycle.SetBusyTimeout5Seconds(context.Background())
	closeErr := connection.Close()
	return errors.Join(
		wrapCurrentNodeCompletionCleanupError("restore metadata busy timeout", resetErr),
		wrapCurrentNodeCompletionCleanupError("release current node completion connection", closeErr),
	)
}

func wrapCurrentNodeCompletionCleanupError(operation string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", operation, err)
}
