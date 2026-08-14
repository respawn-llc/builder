package metadata_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"testing"
	"time"

	"core/server/metadata"

	sqlite3 "modernc.org/sqlite/lib"
)

type sqliteCodeError struct {
	code int
}

func (e sqliteCodeError) Error() string {
	return "structured SQLite failure"
}

func (e sqliteCodeError) Code() int {
	return e.code
}

func TestClassifyFailureByStructuredCause(t *testing.T) {
	t.Parallel()

	canceledContext, cancel := context.WithCancel(context.Background())
	cancel()
	deadlineContext, deadlineCancel := context.WithDeadline(
		context.Background(),
		time.Now().Add(-time.Second),
	)
	defer deadlineCancel()

	tests := []struct {
		name      string
		ctx       context.Context
		cause     error
		wantClass metadata.FailureClass
		wantCode  *metadata.SQLiteResultCode
	}{
		{
			name:      "expected absence",
			ctx:       context.Background(),
			cause:     fmt.Errorf("lookup: %w", sql.ErrNoRows),
			wantClass: metadata.FailureNoncritical,
		},
		{
			name:      "declared constraint",
			ctx:       context.Background(),
			cause:     fmt.Errorf("insert: %w", sqliteCodeError{code: sqlite3.SQLITE_CONSTRAINT_UNIQUE}),
			wantClass: metadata.FailureNoncritical,
			wantCode:  sqliteResultCode(sqlite3.SQLITE_CONSTRAINT_UNIQUE),
		},
		{
			name:      "caller cancellation",
			ctx:       canceledContext,
			cause:     context.Canceled,
			wantClass: metadata.FailureNoncritical,
		},
		{
			name:      "caller deadline",
			ctx:       deadlineContext,
			cause:     context.DeadlineExceeded,
			wantClass: metadata.FailureNoncritical,
		},
		{
			name:      "SQLite interrupt caused by caller cancellation",
			ctx:       canceledContext,
			cause:     sqliteCodeError{code: sqlite3.SQLITE_INTERRUPT},
			wantClass: metadata.FailureNoncritical,
			wantCode:  sqliteResultCode(sqlite3.SQLITE_INTERRUPT),
		},
		{
			name:      "busy extended variant",
			ctx:       context.Background(),
			cause:     sqliteCodeError{code: sqlite3.SQLITE_BUSY_TIMEOUT},
			wantClass: metadata.FailureNoncritical,
			wantCode:  sqliteResultCode(sqlite3.SQLITE_BUSY_TIMEOUT),
		},
		{
			name:      "locked extended variant",
			ctx:       context.Background(),
			cause:     sqliteCodeError{code: sqlite3.SQLITE_LOCKED_SHAREDCACHE},
			wantClass: metadata.FailureNoncritical,
			wantCode:  sqliteResultCode(sqlite3.SQLITE_LOCKED_SHAREDCACHE),
		},
		{
			name:      "database full",
			ctx:       context.Background(),
			cause:     sqliteCodeError{code: sqlite3.SQLITE_FULL},
			wantClass: metadata.FailureCritical,
			wantCode:  sqliteResultCode(sqlite3.SQLITE_FULL),
		},
		{
			name:      "corrupt extended variant",
			ctx:       context.Background(),
			cause:     sqliteCodeError{code: sqlite3.SQLITE_CORRUPT_INDEX},
			wantClass: metadata.FailureCritical,
			wantCode:  sqliteResultCode(sqlite3.SQLITE_CORRUPT_INDEX),
		},
		{
			name:      "not a database",
			ctx:       context.Background(),
			cause:     sqliteCodeError{code: sqlite3.SQLITE_NOTADB},
			wantClass: metadata.FailureCritical,
			wantCode:  sqliteResultCode(sqlite3.SQLITE_NOTADB),
		},
		{
			name:      "cannot open extended variant",
			ctx:       context.Background(),
			cause:     sqliteCodeError{code: sqlite3.SQLITE_CANTOPEN_ISDIR},
			wantClass: metadata.FailureCritical,
			wantCode:  sqliteResultCode(sqlite3.SQLITE_CANTOPEN_ISDIR),
		},
		{
			name:      "unexpected readonly extended variant",
			ctx:       context.Background(),
			cause:     sqliteCodeError{code: sqlite3.SQLITE_READONLY_DBMOVED},
			wantClass: metadata.FailureCritical,
			wantCode:  sqliteResultCode(sqlite3.SQLITE_READONLY_DBMOVED),
		},
		{
			name:      "missing database path",
			ctx:       context.Background(),
			cause:     &fs.PathError{Op: "open", Path: "/metadata/db.sqlite", Err: fs.ErrNotExist},
			wantClass: metadata.FailureCritical,
		},
		{
			name:      "inaccessible database path",
			ctx:       context.Background(),
			cause:     &fs.PathError{Op: "open", Path: "/metadata/db.sqlite", Err: fs.ErrPermission},
			wantClass: metadata.FailureCritical,
		},
		{
			name:      "closed live database connection",
			ctx:       context.Background(),
			cause:     sql.ErrConnDone,
			wantClass: metadata.FailureCritical,
		},
		{
			name:      "domain absence wrapping filesystem sentinel",
			ctx:       context.Background(),
			cause:     fmt.Errorf("requested metadata record is absent: %w", fs.ErrNotExist),
			wantClass: metadata.FailureNoncritical,
		},
		{
			name:      "domain rejection wrapping permission sentinel",
			ctx:       context.Background(),
			cause:     fmt.Errorf("caller may not mutate this record: %w", fs.ErrPermission),
			wantClass: metadata.FailureNoncritical,
		},
		{
			name:      "unknown SQLite primary code",
			ctx:       context.Background(),
			cause:     sqliteCodeError{code: 31},
			wantClass: metadata.FailureCritical,
			wantCode:  sqliteResultCode(31),
		},
		{
			name:      "SQLite interrupt without caller cancellation",
			ctx:       context.Background(),
			cause:     sqliteCodeError{code: sqlite3.SQLITE_INTERRUPT},
			wantClass: metadata.FailureCritical,
			wantCode:  sqliteResultCode(sqlite3.SQLITE_INTERRUPT),
		},
		{
			name:      "ordinary non-SQLite operation failure",
			ctx:       context.Background(),
			cause:     errors.New("validation failed"),
			wantClass: metadata.FailureNoncritical,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			classified := metadata.ClassifyFailure(
				tt.ctx,
				"update task",
				"/metadata/db.sqlite",
				tt.cause,
			)

			if classified.Class != tt.wantClass {
				t.Fatalf("Class = %v, want %v", classified.Class, tt.wantClass)
			}
			if classified.Operation != "update task" {
				t.Fatalf("Operation = %q, want update task", classified.Operation)
			}
			if classified.DatabasePath != "/metadata/db.sqlite" {
				t.Fatalf("DatabasePath = %q, want /metadata/db.sqlite", classified.DatabasePath)
			}
			if !errors.Is(classified, tt.cause) {
				t.Fatalf("classified failure does not wrap cause %v", tt.cause)
			}
			requireSQLiteResultCode(t, classified.SQLite, tt.wantCode)
		})
	}
}

func TestClassifyFailureTreatsEverySQLiteIOERRExtendedVariantAsCritical(t *testing.T) {
	t.Parallel()

	ioErrorCodes := []struct {
		name string
		code int
	}{
		{"primary", sqlite3.SQLITE_IOERR},
		{"access", sqlite3.SQLITE_IOERR_ACCESS},
		{"auth", sqlite3.SQLITE_IOERR_AUTH},
		{"bad key", sqlite3.SQLITE_IOERR_BADKEY},
		{"begin atomic", sqlite3.SQLITE_IOERR_BEGIN_ATOMIC},
		{"blocked", sqlite3.SQLITE_IOERR_BLOCKED},
		{"check reserved lock", sqlite3.SQLITE_IOERR_CHECKRESERVEDLOCK},
		{"close", sqlite3.SQLITE_IOERR_CLOSE},
		{"codec", sqlite3.SQLITE_IOERR_CODEC},
		{"commit atomic", sqlite3.SQLITE_IOERR_COMMIT_ATOMIC},
		{"convert path", sqlite3.SQLITE_IOERR_CONVPATH},
		{"corrupt filesystem", sqlite3.SQLITE_IOERR_CORRUPTFS},
		{"data", sqlite3.SQLITE_IOERR_DATA},
		{"delete", sqlite3.SQLITE_IOERR_DELETE},
		{"delete missing", sqlite3.SQLITE_IOERR_DELETE_NOENT},
		{"directory close", sqlite3.SQLITE_IOERR_DIR_CLOSE},
		{"directory sync", sqlite3.SQLITE_IOERR_DIR_FSYNC},
		{"stat", sqlite3.SQLITE_IOERR_FSTAT},
		{"sync", sqlite3.SQLITE_IOERR_FSYNC},
		{"temporary path", sqlite3.SQLITE_IOERR_GETTEMPPATH},
		{"in page", sqlite3.SQLITE_IOERR_IN_PAGE},
		{"lock", sqlite3.SQLITE_IOERR_LOCK},
		{"memory map", sqlite3.SQLITE_IOERR_MMAP},
		{"memory", sqlite3.SQLITE_IOERR_NOMEM},
		{"read lock", sqlite3.SQLITE_IOERR_RDLOCK},
		{"read", sqlite3.SQLITE_IOERR_READ},
		{"rollback atomic", sqlite3.SQLITE_IOERR_ROLLBACK_ATOMIC},
		{"seek", sqlite3.SQLITE_IOERR_SEEK},
		{"shared memory lock", sqlite3.SQLITE_IOERR_SHMLOCK},
		{"shared memory map", sqlite3.SQLITE_IOERR_SHMMAP},
		{"shared memory open", sqlite3.SQLITE_IOERR_SHMOPEN},
		{"shared memory size", sqlite3.SQLITE_IOERR_SHMSIZE},
		{"short read", sqlite3.SQLITE_IOERR_SHORT_READ},
		{"truncate", sqlite3.SQLITE_IOERR_TRUNCATE},
		{"unlock", sqlite3.SQLITE_IOERR_UNLOCK},
		{"vnode", sqlite3.SQLITE_IOERR_VNODE},
		{"write", sqlite3.SQLITE_IOERR_WRITE},
		{"future extended variant", sqlite3.SQLITE_IOERR | (255 << 8)},
	}

	for _, tt := range ioErrorCodes {
		t.Run(tt.name, func(t *testing.T) {
			cause := sqliteCodeError{code: tt.code}
			classified := metadata.ClassifyFailure(
				context.Background(),
				"read metadata",
				"/metadata/db.sqlite",
				cause,
			)

			if classified.Class != metadata.FailureCritical {
				t.Fatalf("Class = %v, want critical", classified.Class)
			}
			requireSQLiteResultCode(t, classified.SQLite, sqliteResultCode(tt.code))
			if !errors.Is(classified, cause) {
				t.Fatalf("classified failure does not wrap IOERR cause")
			}
		})
	}
}

func sqliteResultCode(extended int) *metadata.SQLiteResultCode {
	return &metadata.SQLiteResultCode{
		Primary:  extended & 0xff,
		Extended: extended,
	}
}

func requireSQLiteResultCode(t *testing.T, got, want *metadata.SQLiteResultCode) {
	t.Helper()
	if got == nil || want == nil {
		if got != want {
			t.Fatalf("SQLite = %#v, want %#v", got, want)
		}
		return
	}
	if *got != *want {
		t.Fatalf("SQLite = %#v, want %#v", got, want)
	}
}
