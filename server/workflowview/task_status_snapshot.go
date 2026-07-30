package workflowview

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"core/server/metadata"
	"core/server/metadata/sqlitegen"
	"core/server/sessionruntime"
	"core/server/workflow"
	"core/server/workflowexecution"
)

const taskStatusSnapshotCaptureAttempts = 3

// TaskStatusSnapshotCoordinator owns coherent durable Task status and live
// workflow activity captures. It fences Workflow mutations while it anchors
// the durable read transaction, then verifies that the complete live
// observation stayed unchanged.
type TaskStatusSnapshotCoordinator struct {
	db        *sql.DB
	queries   *sqlitegen.Queries
	permit    *workflowexecution.MutationPermit
	authority *sessionruntime.Authority
}

// TaskStatusSnapshot is one immutable live-observation/durable-query pair.
// Its transaction remains open until Close so every consumer query sees the
// durable state that was anchored with the captured live observation.
type TaskStatusSnapshot struct {
	queries *sqlitegen.Queries
	live    taskStatusLiveSnapshot
	anchor  func(context.Context) error
	close   func() error
}

type taskStatusLiveSnapshot struct {
	executions map[workflow.TaskID]sessionruntime.TaskExecutionSnapshot
	revision   uint64
}

// TaskStatusSnapshotConsistencyError reports that current live workflow
// activity changed while its durable state was being anchored.
type TaskStatusSnapshotConsistencyError struct {
	Attempts       int
	BeforeRevision uint64
	AfterRevision  uint64
}

func (e *TaskStatusSnapshotConsistencyError) Error() string {
	if e == nil {
		return "task status snapshot consistency error"
	}
	return fmt.Sprintf(
		"task status snapshot live observation changed during capture after %d attempts: before=%d after=%d",
		e.Attempts,
		e.BeforeRevision,
		e.AfterRevision,
	)
}

func NewTaskStatusSnapshotCoordinator(
	metadataStore *metadata.Store,
	permit *workflowexecution.MutationPermit,
	authority *sessionruntime.Authority,
) (*TaskStatusSnapshotCoordinator, error) {
	if metadataStore == nil {
		return nil, errors.New("metadata store is required")
	}
	switch {
	case metadataStore.DB() == nil:
		return nil, errors.New("metadata database is required")
	case metadataStore.Queries() == nil:
		return nil, errors.New("metadata queries are required")
	case permit == nil:
		return nil, errors.New("workflow mutation permit is required")
	case authority == nil:
		return nil, errors.New("session runtime authority is required")
	default:
		return &TaskStatusSnapshotCoordinator{
			db:        metadataStore.DB(),
			queries:   metadataStore.Queries(),
			permit:    permit,
			authority: authority,
		}, nil
	}
}

func (c *TaskStatusSnapshotCoordinator) Capture(ctx context.Context) (*TaskStatusSnapshot, error) {
	if c == nil {
		return nil, errors.New("task status snapshot coordinator is required")
	}
	if ctx == nil {
		return nil, errors.New("task status snapshot context is required")
	}
	return taskStatusSnapshotCapture(
		ctx,
		c.permit,
		func() (taskStatusLiveSnapshot, error) {
			executions, revision, err := c.authority.CurrentWorkflowTaskExecutionSnapshotsWithRevision()
			if err != nil {
				return taskStatusLiveSnapshot{}, err
			}
			return taskStatusLiveSnapshot{executions: executions, revision: revision}, nil
		},
		func(ctx context.Context) (*TaskStatusSnapshot, error) {
			tx, err := c.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
			if err != nil {
				return nil, fmt.Errorf("begin task status read transaction: %w", err)
			}
			queries := c.queries.WithTx(tx)
			snapshot := &TaskStatusSnapshot{
				queries: queries,
				close: func() error {
					rollbackErr := tx.Rollback()
					if errors.Is(rollbackErr, sql.ErrTxDone) {
						return nil
					}
					return rollbackErr
				},
			}
			snapshot.anchor = func(ctx context.Context) error {
				_, err := queries.AnchorWorkflowTaskStatusSnapshot(ctx)
				return err
			}
			return snapshot, nil
		},
	)
}

func taskStatusSnapshotCapture(
	ctx context.Context,
	permit *workflowexecution.MutationPermit,
	captureLive func() (taskStatusLiveSnapshot, error),
	openRead func(context.Context) (*TaskStatusSnapshot, error),
) (*TaskStatusSnapshot, error) {
	if permit == nil {
		return nil, errors.New("workflow mutation permit is required")
	}
	if captureLive == nil {
		return nil, errors.New("task status live observation is required")
	}
	if openRead == nil {
		return nil, errors.New("task status durable snapshot opener is required")
	}
	for attempt := 1; attempt <= taskStatusSnapshotCaptureAttempts; attempt++ {
		if err := context.Cause(ctx); err != nil {
			return nil, err
		}
		var (
			snapshot *TaskStatusSnapshot
			retry    bool
		)
		err := permit.Run(ctx, func(ctx context.Context) error {
			before, err := captureLive()
			if err != nil {
				return err
			}
			if err := context.Cause(ctx); err != nil {
				return err
			}
			candidate, openErr := openRead(ctx)
			if openErr != nil {
				if candidate == nil {
					return openErr
				}
				return errors.Join(openErr, candidate.Close())
			}
			if candidate == nil {
				return errors.New("task status durable snapshot opener returned nil")
			}
			if candidate.anchor == nil {
				return errors.Join(errors.New("task status durable snapshot anchor is required"), candidate.Close())
			}
			if err := candidate.anchor(ctx); err != nil {
				return errors.Join(err, candidate.Close())
			}
			if err := context.Cause(ctx); err != nil {
				return errors.Join(err, candidate.Close())
			}
			after, err := captureLive()
			if err != nil {
				return errors.Join(err, candidate.Close())
			}
			if err := context.Cause(ctx); err != nil {
				return errors.Join(err, candidate.Close())
			}
			if before.revision != after.revision {
				consistencyErr := &TaskStatusSnapshotConsistencyError{
					Attempts:       attempt,
					BeforeRevision: before.revision,
					AfterRevision:  after.revision,
				}
				closeErr := candidate.Close()
				if closeErr != nil || attempt == taskStatusSnapshotCaptureAttempts {
					return errors.Join(consistencyErr, closeErr)
				}
				retry = true
				return nil
			}
			candidate.live = after
			snapshot = candidate
			return nil
		})
		if err != nil {
			return nil, err
		}
		if snapshot != nil {
			return snapshot, nil
		}
		if !retry {
			return nil, errors.New("task status snapshot capture ended without a result")
		}
	}
	return nil, errors.New("task status snapshot capture exhausted without a result")
}

func (s *TaskStatusSnapshot) Close() error {
	if s == nil || s.close == nil {
		return nil
	}
	close := s.close
	s.close = nil
	s.queries = nil
	return close()
}
