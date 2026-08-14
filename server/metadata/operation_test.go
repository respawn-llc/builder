package metadata

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"sync/atomic"
	"testing"

	"core/server/metadata/sqlitegen"

	sqlite3 "modernc.org/sqlite/lib"
)

var operationDriverSequence atomic.Uint64

type operationDriver struct {
	beginErr    error
	commitErr   error
	rollbackErr error
	queryErr    error
	rowsErr     error
}

func (d operationDriver) Open(string) (driver.Conn, error) {
	return &operationConnection{driver: d}, nil
}

func (d operationDriver) Connect(context.Context) (driver.Conn, error) {
	return d.Open("")
}

func (d operationDriver) Driver() driver.Driver {
	return d
}

type operationConnection struct {
	driver operationDriver
}

func (c *operationConnection) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepare is not supported")
}
func (c *operationConnection) Close() error { return nil }
func (c *operationConnection) Begin() (driver.Tx, error) {
	if c.driver.beginErr != nil {
		return nil, c.driver.beginErr
	}
	return operationTransaction{driver: c.driver}, nil
}
func (c *operationConnection) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	return c.Begin()
}
func (c *operationConnection) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	if c.driver.queryErr != nil {
		return nil, c.driver.queryErr
	}
	return operationRows{nextErr: c.driver.rowsErr}, nil
}

type operationTransaction struct {
	driver operationDriver
}

func (tx operationTransaction) Commit() error   { return tx.driver.commitErr }
func (tx operationTransaction) Rollback() error { return tx.driver.rollbackErr }

type operationRows struct {
	nextErr error
}

func (operationRows) Columns() []string {
	return []string{
		"id",
		"display_name",
		"project_key",
		"created_at_unix_ms",
		"updated_at_unix_ms",
		"workspace_count",
		"session_count",
	}
}
func (operationRows) Close() error { return nil }
func (rows operationRows) Next([]driver.Value) error {
	if rows.nextErr != nil {
		return rows.nextErr
	}
	return io.EOF
}

func openOperationTestStore(t *testing.T, failures operationDriver) (*Store, *recordingFatalReporter) {
	t.Helper()
	_ = fmt.Sprintf("metadata-operation-%d", operationDriverSequence.Add(1))
	db := sql.OpenDB(failures)
	t.Cleanup(func() { _ = db.Close() })
	reporter := &recordingFatalReporter{}
	store := &Store{
		databasePath:  "/metadata/main.sqlite3",
		db:            db,
		fatalReporter: reporter,
	}
	store.queries = sqlitegen.New(monitoredDBTX{DBTX: db, monitor: store})
	return store, reporter
}

func operationCause(code int, message string) error {
	return &SQLiteCause{
		ResultCode: SQLiteResultCode{
			Primary:  code & 0xff,
			Extended: code,
		},
		Cause: errors.New(message),
	}
}

func TestMonitoredTransactionClassifiesEverySettlementOnce(t *testing.T) {
	tests := []struct {
		name         string
		driver       operationDriver
		body         error
		commit       bool
		wantClass    FailureClass
		wantRollback bool
		wantFatal    int
	}{
		{
			name:      "critical begin failure",
			driver:    operationDriver{beginErr: operationCause(sqlite3.SQLITE_IOERR, "begin failed")},
			wantClass: FailureCritical,
			wantFatal: 1,
		},
		{
			name:      "critical statement failure",
			body:      &sqlitegen.DatabaseCause{Cause: operationCause(sqlite3.SQLITE_FULL, "statement failed")},
			wantClass: FailureCritical,
			wantFatal: 1,
		},
		{
			name:      "critical commit failure",
			driver:    operationDriver{commitErr: operationCause(sqlite3.SQLITE_IOERR_FSYNC, "commit failed")},
			commit:    true,
			wantClass: FailureCritical,
			wantFatal: 1,
		},
		{
			name: "critical rollback after contention",
			driver: operationDriver{
				rollbackErr: operationCause(sqlite3.SQLITE_IOERR_ROLLBACK_ATOMIC, "rollback failed"),
			},
			body:         &sqlitegen.DatabaseCause{Cause: operationCause(sqlite3.SQLITE_BUSY, "statement busy")},
			wantClass:    FailureCritical,
			wantRollback: true,
			wantFatal:    1,
		},
		{
			name:      "contention remains noncritical",
			body:      &sqlitegen.DatabaseCause{Cause: operationCause(sqlite3.SQLITE_BUSY_TIMEOUT, "statement busy")},
			wantClass: FailureNoncritical,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, reporter := openOperationTestStore(t, test.driver)
			tx, err := store.BeginTransaction(context.Background(), test.name, nil)
			if test.driver.beginErr != nil {
				assertOperationFailure(t, err, test.wantClass, test.wantRollback)
			} else {
				if err != nil {
					t.Fatal(err)
				}
				resultErr := test.body
				if test.commit {
					resultErr = tx.Commit()
				}
				tx.Settle(context.Background(), &resultErr)
				assertOperationFailure(t, resultErr, test.wantClass, test.wantRollback)
			}
			if len(reporter.failures) != test.wantFatal {
				t.Fatalf("fatal submissions = %d, want %d", len(reporter.failures), test.wantFatal)
			}
		})
	}
}

func assertOperationFailure(t *testing.T, err error, wantClass FailureClass, wantRollback bool) {
	t.Helper()
	var failure *ClassifiedFailure
	if !errors.As(err, &failure) {
		t.Fatalf("error %T is not a ClassifiedFailure: %v", err, err)
	}
	if failure.Class != wantClass {
		t.Fatalf("failure class = %v, want %v", failure.Class, wantClass)
	}
	if (failure.RollbackCause != nil) != wantRollback {
		t.Fatalf("rollback cause = %v, want present %v", failure.RollbackCause, wantRollback)
	}
}

func TestFirstMetadataFatalShortCircuitsGeneratedWork(t *testing.T) {
	store, reporter := openOperationTestStore(t, operationDriver{
		beginErr: operationCause(sqlite3.SQLITE_IOERR, "begin failed"),
	})
	_, err := store.BeginTransaction(context.Background(), "first", nil)
	var first *ClassifiedFailure
	if !errors.As(err, &first) {
		t.Fatalf("first error = %v", err)
	}
	if _, err := store.Queries().ListProjects(context.Background()); err != first {
		t.Fatalf("later generated operation error = %v, want retained first fatal", err)
	}
	if len(reporter.failures) != 1 {
		t.Fatalf("fatal submissions = %d, want 1", len(reporter.failures))
	}
}

func TestGeneratedQueryOwnsIterationFailure(t *testing.T) {
	store, reporter := openOperationTestStore(t, operationDriver{
		rowsErr: operationCause(sqlite3.SQLITE_CORRUPT, "row iteration failed"),
	})
	_, err := store.Queries().ListProjects(context.Background())
	var failure *ClassifiedFailure
	if !errors.As(err, &failure) || failure.Class != FailureCritical ||
		failure.Operation != "ListProjects" {
		t.Fatalf("generated query failure = %#v, want critical ListProjects operation", failure)
	}
	if len(reporter.failures) != 1 || reporter.failures[0] != failure {
		t.Fatalf("fatal submissions = %#v, want one exact generated failure", reporter.failures)
	}
}

func TestTransactionGeneratedQueryFailureSettlesAtOuterOwner(t *testing.T) {
	store, reporter := openOperationTestStore(t, operationDriver{
		rowsErr:     operationCause(sqlite3.SQLITE_FULL, "transaction row iteration failed"),
		rollbackErr: operationCause(sqlite3.SQLITE_IOERR_ROLLBACK_ATOMIC, "transaction rollback failed"),
	})
	tx, err := store.BeginTransaction(context.Background(), "Generated transaction query", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, resultErr := tx.Queries().ListProjects(context.Background())
	var raw *sqlitegen.DatabaseCause
	if !errors.As(resultErr, &raw) {
		t.Fatalf("generated transaction error = %T, want raw DatabaseCause", resultErr)
	}
	var premature *ClassifiedFailure
	if errors.As(resultErr, &premature) {
		t.Fatalf("generated transaction query classified before settlement: %#v", premature)
	}

	tx.Settle(context.Background(), &resultErr)
	var failure *ClassifiedFailure
	if !errors.As(resultErr, &failure) {
		t.Fatalf("settled transaction error = %T, want ClassifiedFailure", resultErr)
	}
	if failure.Class != FailureCritical ||
		failure.Operation != "Generated transaction query" ||
		failure.RollbackCause == nil {
		t.Fatalf("settled transaction failure = %#v", failure)
	}
	if len(reporter.failures) != 1 || reporter.failures[0] != failure {
		t.Fatalf("fatal submissions = %#v, want one exact outer failure", reporter.failures)
	}
}
