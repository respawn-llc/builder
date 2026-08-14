package metadata

import (
	"context"
	"database/sql"
	"errors"

	"core/server/metadata/sqlitegen"
	"core/server/metadata/sqlitelifecyclegen"
)

type Transaction struct {
	store         *Store
	operation     string
	tx            *sql.Tx
	settled       bool
	rolledBack    bool
	rollbackCause error
}

type startupOperationMonitor struct {
	databasePath string
}

func (startupOperationMonitor) BeforeOperation() error {
	return nil
}

func (monitor startupOperationMonitor) CompleteOperation(
	ctx context.Context,
	operation string,
	cause error,
) error {
	return ClassifyFailure(ctx, operation, monitor.databasePath, cause)
}

func runStartupDatabaseOperation(
	ctx context.Context,
	operation string,
	databasePath string,
	body func() error,
) error {
	if err := body(); err != nil {
		return ClassifyFailure(ctx, operation, databasePath, err)
	}
	return nil
}

func (s *Store) BeginTransaction(
	ctx context.Context,
	operation string,
	options *sql.TxOptions,
) (*Transaction, error) {
	if err := s.BeforeOperation(); err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, options)
	if err != nil {
		return nil, s.CompleteOperation(ctx, operation, err)
	}
	return &Transaction{store: s, operation: operation, tx: tx}, nil
}

func (tx *Transaction) Queries() *sqlitegen.Queries {
	return tx.store.queries.WithTx(tx.tx)
}

func (tx *Transaction) DeferForeignKeys(ctx context.Context) error {
	if err := sqlitelifecyclegen.New(tx.tx).DeferForeignKeys(ctx); err != nil {
		return &sqlitegen.DatabaseCause{Cause: err}
	}
	return nil
}

func (tx *Transaction) Commit() error {
	if err := tx.tx.Commit(); err != nil {
		return &sqlitegen.DatabaseCause{Cause: err}
	}
	tx.settled = true
	return nil
}

func (tx *Transaction) Settle(ctx context.Context, resultErr *error) {
	if tx == nil || resultErr == nil || tx.settled {
		return
	}
	rollback := tx.rollbackCause
	if !tx.rolledBack {
		rollback = tx.tx.Rollback()
		if errors.Is(rollback, sql.ErrTxDone) {
			rollback = nil
		}
		tx.rolledBack = true
		tx.rollbackCause = rollback
	}
	primary := *resultErr
	if primary == nil && rollback == nil {
		tx.settled = true
		return
	}
	var databaseCause *sqlitegen.DatabaseCause
	if primary != nil && !errors.As(primary, &databaseCause) && rollback == nil {
		tx.settled = true
		return
	}
	*resultErr = tx.store.completeTransaction(ctx, tx.operation, primary, rollback)
	tx.settled = true
}

func (tx *Transaction) Complete(ctx context.Context, primary error) error {
	tx.Settle(ctx, &primary)
	return primary
}

func RunTransaction[T any](
	ctx context.Context,
	store *Store,
	operation string,
	options *sql.TxOptions,
	body func(*sqlitegen.Queries) (T, error),
) (T, error) {
	var zero T
	tx, err := store.BeginTransaction(ctx, operation, options)
	if err != nil {
		return zero, err
	}
	result, primary := body(tx.Queries())
	if primary != nil {
		return zero, tx.Complete(ctx, primary)
	}
	if commit := tx.Commit(); commit != nil {
		return zero, tx.Complete(ctx, commit)
	}
	return result, nil
}

func RunReadTransaction[T any](
	ctx context.Context,
	store *Store,
	operation string,
	body func(*sqlitegen.Queries) (T, error),
) (T, error) {
	return RunTransaction(ctx, store, operation, &sql.TxOptions{ReadOnly: true}, body)
}

func RunOperation[T any](
	ctx context.Context,
	store *Store,
	operation string,
	body func(*sqlitegen.Queries) (T, error),
) (T, error) {
	var zero T
	if err := store.BeforeOperation(); err != nil {
		return zero, err
	}
	result, err := body(sqlitegen.NewRaw(store.db))
	if err != nil {
		return zero, store.CompleteOperation(ctx, operation, err)
	}
	return result, nil
}

func Monitor(
	ctx context.Context,
	store *Store,
	operation string,
	body func(*sqlitegen.Queries) error,
) error {
	_, err := RunOperation(ctx, store, operation, func(q *sqlitegen.Queries) (struct{}, error) {
		return struct{}{}, body(q)
	})
	return err
}

func (s *Store) completeTransaction(
	ctx context.Context,
	operation string,
	primary error,
	rollback error,
) error {
	failure := ClassifyOperationFailure(ctx, operation, s.databasePath, primary, rollback)
	if failure.Class == FailureCritical && s.fatalReporter != nil {
		s.fatalReporter.ReportMetadataFatal(failure)
	}
	return failure
}

type ImmediateTransaction struct {
	store      *Store
	operation  string
	connection *sql.Conn
	lifecycle  *sqlitelifecyclegen.Queries
	queries    *sqlitegen.Queries
	committed  bool
}

func (s *Store) BeginImmediateTransaction(ctx context.Context, operation string) (*ImmediateTransaction, error) {
	if err := s.BeforeOperation(); err != nil {
		return nil, err
	}
	connection, err := s.db.Conn(ctx)
	if err != nil {
		return nil, s.CompleteOperation(ctx, operation, err)
	}
	lifecycle := sqlitelifecyclegen.New(connection)
	fail := func(primary error) (*ImmediateTransaction, error) {
		reset := lifecycle.SetBusyTimeout5Seconds(context.Background())
		closeErr := connection.Close()
		return nil, s.completeTransaction(ctx, operation, primary, errors.Join(reset, closeErr))
	}
	if err := lifecycle.SetBusyTimeout15Seconds(ctx); err != nil {
		return fail(err)
	}
	if err := lifecycle.BeginImmediate(ctx); err != nil {
		return fail(err)
	}
	return &ImmediateTransaction{
		store:      s,
		operation:  operation,
		connection: connection,
		lifecycle:  lifecycle,
		queries:    sqlitegen.NewRaw(connection),
	}, nil
}

func (tx *ImmediateTransaction) Queries() *sqlitegen.Queries {
	return tx.queries
}

func (tx *ImmediateTransaction) Commit(ctx context.Context) error {
	if err := tx.lifecycle.Commit(ctx); err != nil {
		return &sqlitegen.DatabaseCause{Cause: err}
	}
	tx.committed = true
	return nil
}

func (tx *ImmediateTransaction) Settle(ctx context.Context, resultErr *error) {
	if tx == nil || resultErr == nil || tx.connection == nil {
		return
	}
	var rollback error
	if !tx.committed {
		rollback = tx.lifecycle.Rollback(context.Background())
	}
	cleanup := errors.Join(
		tx.lifecycle.SetBusyTimeout5Seconds(context.Background()),
		tx.connection.Close(),
	)
	tx.connection = nil
	secondary := errors.Join(rollback, cleanup)
	primary := *resultErr
	var databaseCause *sqlitegen.DatabaseCause
	if primary != nil && !errors.As(primary, &databaseCause) && secondary == nil {
		return
	}
	if primary == nil && secondary == nil {
		return
	}
	*resultErr = tx.store.completeTransaction(ctx, tx.operation, primary, secondary)
}
