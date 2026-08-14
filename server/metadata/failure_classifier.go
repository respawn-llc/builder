package metadata

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"

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
	classified := ClassifyFailure(ctx, operation, databasePath, primary)
	classified.RollbackCause = rollback
	if rollback != nil &&
		ClassifyFailure(ctx, operation, databasePath, rollback).Class == FailureCritical {
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
	result := &ClassifiedFailure{
		Class:        FailureNoncritical,
		Operation:    operation,
		DatabasePath: databasePath,
		Cause:        cause,
	}
	if cause == nil {
		return result
	}

	if code, ok := sqliteResultCode(cause); ok {
		result.SQLite = &code
		result.Class = classifySQLiteFailure(ctx, cause, code.Primary)
		return result
	}
	if isCallerContextFailure(ctx, cause) ||
		errors.Is(cause, sql.ErrNoRows) {
		return result
	}
	if errors.Is(cause, sql.ErrConnDone) {
		result.Class = FailureCritical
		return result
	}

	var pathError *fs.PathError
	if errors.As(cause, &pathError) &&
		(errors.Is(pathError.Err, fs.ErrNotExist) ||
			errors.Is(pathError.Err, fs.ErrPermission)) {
		result.Class = FailureCritical
	}
	return result
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
	var sqliteError interface {
		Code() int
	}
	if !errors.As(cause, &sqliteError) {
		return SQLiteResultCode{}, false
	}
	extended := sqliteError.Code()
	return SQLiteResultCode{
		Primary:  extended & 0xff,
		Extended: extended,
	}, true
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
