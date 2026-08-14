package metadata

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"

	sqlitedriver "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

type FailureClass uint8

const (
	FailureNoncritical FailureClass = iota
	FailureCritical
)

type SQLiteResultCode struct {
	Primary  int
	Extended int
}

// SQLiteCause carries a structured result code when a lower database boundary
// cannot retain the concrete driver error directly.
type SQLiteCause struct {
	ResultCode SQLiteResultCode
	Cause      error
}

func (e *SQLiteCause) Error() string {
	if e == nil || e.Cause == nil {
		return "SQLite operation failed"
	}
	return e.Cause.Error()
}

func (e *SQLiteCause) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

type ClassifiedFailure struct {
	Class         FailureClass
	Operation     string
	DatabasePath  string
	SQLite        *SQLiteResultCode
	Cause         error
	RollbackCause error
}

func (f *ClassifiedFailure) Error() string {
	if f.Cause == nil {
		if f.RollbackCause != nil {
			return fmt.Sprintf(
				"metadata operation %q on database %q failed during rollback: %v",
				f.Operation,
				f.DatabasePath,
				f.RollbackCause,
			)
		}
		return fmt.Sprintf(
			"metadata operation %q on database %q completed without a failure cause",
			f.Operation,
			f.DatabasePath,
		)
	}
	rollback := ""
	if f.RollbackCause != nil {
		rollback = fmt.Sprintf("; rollback failed: %v", f.RollbackCause)
	}
	if f.SQLite != nil {
		return fmt.Sprintf(
			"metadata operation %q on database %q failed (SQLite primary code %d, extended code %d): %v%s",
			f.Operation,
			f.DatabasePath,
			f.SQLite.Primary,
			f.SQLite.Extended,
			f.Cause,
			rollback,
		)
	}
	return fmt.Sprintf(
		"metadata operation %q on database %q failed: %v%s",
		f.Operation,
		f.DatabasePath,
		f.Cause,
		rollback,
	)
}

func (f *ClassifiedFailure) Unwrap() error {
	return errors.Join(f.Cause, f.RollbackCause)
}

func ClassifyOperationFailure(
	ctx context.Context,
	operation string,
	databasePath string,
	primary error,
	rollback error,
) *ClassifiedFailure {
	classified := &ClassifiedFailure{
		Class:         FailureNoncritical,
		Operation:     operation,
		DatabasePath:  databasePath,
		Cause:         primary,
		RollbackCause: rollback,
	}
	primaryClass, primaryCode := classifyCause(ctx, primary)
	rollbackClass, rollbackCode := classifyCause(ctx, rollback)
	classified.Class = primaryClass
	classified.SQLite = primaryCode
	if primary == nil {
		classified.Class = rollbackClass
		classified.SQLite = rollbackCode
	} else if rollbackClass == FailureCritical {
		classified.Class = FailureCritical
	}
	return classified
}

// ClassifyFailure classifies one settled metadata operation result.
func ClassifyFailure(
	ctx context.Context,
	operation string,
	databasePath string,
	cause error,
) *ClassifiedFailure {
	class, code := classifyCause(ctx, cause)
	return &ClassifiedFailure{
		Class:        class,
		Operation:    operation,
		DatabasePath: databasePath,
		SQLite:       code,
		Cause:        cause,
	}
}

func classifyCause(ctx context.Context, cause error) (FailureClass, *SQLiteResultCode) {
	if cause == nil {
		return FailureNoncritical, nil
	}

	if code, ok := sqliteResultCode(cause); ok {
		return classifySQLiteFailure(ctx, cause, code.Primary), &code
	}
	if isCallerContextFailure(ctx, cause) ||
		errors.Is(cause, sql.ErrNoRows) {
		return FailureNoncritical, nil
	}
	if errors.Is(cause, sql.ErrConnDone) {
		return FailureCritical, nil
	}

	var pathError *fs.PathError
	if errors.As(cause, &pathError) &&
		(errors.Is(pathError.Err, fs.ErrNotExist) ||
			errors.Is(pathError.Err, fs.ErrPermission)) {
		return FailureCritical, nil
	}
	return FailureNoncritical, nil
}

func classifySQLiteFailure(
	ctx context.Context,
	cause error,
	primaryCode int,
) FailureClass {
	switch primaryCode {
	case sqlite3.SQLITE_BUSY,
		sqlite3.SQLITE_LOCKED,
		sqlite3.SQLITE_CONSTRAINT:
		return FailureNoncritical
	case sqlite3.SQLITE_INTERRUPT:
		if ctx != nil && ctx.Err() != nil {
			return FailureNoncritical
		}
		return FailureCritical
	case sqlite3.SQLITE_FULL,
		sqlite3.SQLITE_IOERR,
		sqlite3.SQLITE_CORRUPT,
		sqlite3.SQLITE_NOTADB,
		sqlite3.SQLITE_CANTOPEN,
		sqlite3.SQLITE_READONLY:
		return FailureCritical
	default:
		return FailureCritical
	}
}

func sqliteResultCode(cause error) (SQLiteResultCode, bool) {
	var sqliteError *sqlitedriver.Error
	if errors.As(cause, &sqliteError) {
		extended := sqliteError.Code()
		return SQLiteResultCode{
			Primary:  extended & 0xff,
			Extended: extended,
		}, true
	}
	var structured *SQLiteCause
	if errors.As(cause, &structured) {
		return structured.ResultCode, true
	}
	return SQLiteResultCode{}, false
}

func isCallerContextFailure(ctx context.Context, cause error) bool {
	if ctx == nil || ctx.Err() == nil {
		return false
	}
	return errors.Is(cause, context.Canceled) ||
		errors.Is(cause, context.DeadlineExceeded) ||
		errors.Is(cause, ctx.Err()) ||
		errors.Is(context.Cause(ctx), cause)
}
