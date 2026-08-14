package sqlitegen

import (
	"context"
)

type OperationMonitor interface {
	BeforeOperation() error
	CompleteOperation(context.Context, string, error) error
}

type DatabaseCause struct {
	Cause error
}

func (e *DatabaseCause) Error() string { return e.Cause.Error() }
func (e *DatabaseCause) Unwrap() error { return e.Cause }

func (q *Queries) beforeOperation() error {
	monitor, ok := q.db.(OperationMonitor)
	if !ok {
		return nil
	}
	return monitor.BeforeOperation()
}

func (q *Queries) completeOperation(ctx context.Context, operation string, cause error) error {
	if cause == nil {
		return nil
	}
	monitor, ok := q.db.(OperationMonitor)
	if !ok {
		return &DatabaseCause{Cause: cause}
	}
	return monitor.CompleteOperation(ctx, operation, cause)
}

func (q *Queries) IsMonitored() bool {
	if q == nil {
		return false
	}
	_, ok := q.db.(OperationMonitor)
	return ok
}

func (q *Queries) IsRaw() bool {
	if q == nil {
		return false
	}
	if _, monitored := q.db.(OperationMonitor); monitored {
		return false
	}
	return true
}
