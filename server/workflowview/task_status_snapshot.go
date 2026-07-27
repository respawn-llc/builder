package workflowview

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"core/server/metadata"
	"core/server/metadata/sqlitegen"
	"core/server/sessionruntime"
	"core/server/workflowexecution"
)

const taskStatusSnapshotCaptureAttempts = 3

type workflowExecutionObservationSource interface {
	AllWorkflowExecutionSnapshot() (sessionruntime.AllWorkflowExecutionSnapshot, error)
}

type schedulerActiveRunObservationSource interface {
	ActiveRunSnapshot() workflowexecution.SchedulerActiveRunSnapshot
}

type TaskStatusSnapshotCoordinator struct {
	db        *sql.DB
	queries   *sqlitegen.Queries
	permit    *workflowexecution.MutationPermit
	authority workflowExecutionObservationSource
	scheduler schedulerActiveRunObservationSource
}

type TaskStatusSnapshot struct {
	tx              *sql.Tx
	queries         *sqlitegen.Queries
	authority       sessionruntime.AllWorkflowExecutionSnapshot
	scheduler       workflowexecution.SchedulerActiveRunSnapshot
	currentRunFacts []sqlitegen.AnchorWorkflowTaskStatusSnapshotRow
}

type TaskStatusSnapshotConsistencyReason string

const (
	TaskStatusSnapshotConsistencyReasonLiveObservationChanged      TaskStatusSnapshotConsistencyReason = "live_observation_changed"
	TaskStatusSnapshotConsistencyReasonInvalidSchedulerObservation TaskStatusSnapshotConsistencyReason = "invalid_scheduler_observation"
	TaskStatusSnapshotConsistencyReasonInvalidAuthorityObservation TaskStatusSnapshotConsistencyReason = "invalid_authority_observation"
	TaskStatusSnapshotConsistencyReasonDuplicateSchedulerIdentity  TaskStatusSnapshotConsistencyReason = "duplicate_scheduler_identity"
	TaskStatusSnapshotConsistencyReasonDuplicateAuthorityIdentity  TaskStatusSnapshotConsistencyReason = "duplicate_authority_identity"
	TaskStatusSnapshotConsistencyReasonAuthorityMissingScheduler   TaskStatusSnapshotConsistencyReason = "authority_missing_scheduler"
)

type TaskStatusPromptObservationRevision struct {
	Ref      sessionruntime.WorkflowExecutionRef
	Revision sessionruntime.WorkflowExecutionPromptRevision
}

type TaskStatusObservationRevisions struct {
	AuthorityExecutionMapRevision sessionruntime.WorkflowExecutionMapRevision
	SchedulerActiveRunRevision    workflowexecution.SchedulerActiveRunRevision
	PromptRevisions               []TaskStatusPromptObservationRevision
}

type TaskStatusSnapshotConsistencyError struct {
	Reason   TaskStatusSnapshotConsistencyReason
	Attempts int
	Before   TaskStatusObservationRevisions
	After    TaskStatusObservationRevisions
}

func (e *TaskStatusSnapshotConsistencyError) Error() string {
	if e == nil {
		return "workflow task status snapshot consistency error"
	}
	return fmt.Sprintf(
		"workflow task status snapshot consistency error reason=%s attempts=%d authority_before=%d authority_after=%d scheduler_before=%d scheduler_after=%d",
		e.Reason,
		e.Attempts,
		e.Before.AuthorityExecutionMapRevision,
		e.After.AuthorityExecutionMapRevision,
		e.Before.SchedulerActiveRunRevision,
		e.After.SchedulerActiveRunRevision,
	)
}

func NewTaskStatusSnapshotCoordinator(
	metadataStore *metadata.Store,
	permit *workflowexecution.MutationPermit,
	authority *sessionruntime.Authority,
	scheduler *workflowexecution.SchedulerService,
) (*TaskStatusSnapshotCoordinator, error) {
	if metadataStore == nil {
		return nil, errors.New("metadata store is required")
	}
	if authority == nil {
		return nil, errors.New("session runtime authority is required")
	}
	if scheduler == nil {
		return nil, errors.New("workflow scheduler is required")
	}
	return newTaskStatusSnapshotCoordinator(metadataStore.DB(), metadataStore.Queries(), permit, authority, scheduler)
}

func newTaskStatusSnapshotCoordinator(
	db *sql.DB,
	queries *sqlitegen.Queries,
	permit *workflowexecution.MutationPermit,
	authority workflowExecutionObservationSource,
	scheduler schedulerActiveRunObservationSource,
) (*TaskStatusSnapshotCoordinator, error) {
	switch {
	case db == nil:
		return nil, errors.New("metadata database is required")
	case queries == nil:
		return nil, errors.New("metadata queries are required")
	case permit == nil:
		return nil, errors.New("workflow mutation permit is required")
	case authority == nil:
		return nil, errors.New("workflow execution observation source is required")
	case scheduler == nil:
		return nil, errors.New("scheduler active-run observation source is required")
	default:
		return &TaskStatusSnapshotCoordinator{
			db:        db,
			queries:   queries,
			permit:    permit,
			authority: authority,
			scheduler: scheduler,
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
	for attempt := 1; attempt <= taskStatusSnapshotCaptureAttempts; attempt++ {
		if err := context.Cause(ctx); err != nil {
			return nil, err
		}
		var snapshot *TaskStatusSnapshot
		var retry bool
		err := c.permit.Run(ctx, func(ctx context.Context) error {
			beforeAuthority, err := c.authority.AllWorkflowExecutionSnapshot()
			if err != nil {
				return err
			}
			beforeScheduler := c.scheduler.ActiveRunSnapshot()
			if err := context.Cause(ctx); err != nil {
				return err
			}

			observedRunIDs := taskStatusSnapshotObservedRunIDs(beforeAuthority, beforeScheduler)
			encodedRunIDs, err := json.Marshal(observedRunIDs)
			if err != nil {
				return fmt.Errorf("encode observed workflow run ids: %w", err)
			}
			tx, err := c.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
			if err != nil {
				return err
			}
			txQueries := c.queries.WithTx(tx)
			currentRunFacts, err := txQueries.AnchorWorkflowTaskStatusSnapshot(ctx, string(encodedRunIDs))
			if err != nil {
				return errors.Join(err, rollbackTaskStatusSnapshotTransaction(tx))
			}
			if err := context.Cause(ctx); err != nil {
				return errors.Join(err, rollbackTaskStatusSnapshotTransaction(tx))
			}

			afterAuthority, err := c.authority.AllWorkflowExecutionSnapshot()
			if err != nil {
				return errors.Join(err, rollbackTaskStatusSnapshotTransaction(tx))
			}
			afterScheduler := c.scheduler.ActiveRunSnapshot()
			if err := context.Cause(ctx); err != nil {
				return errors.Join(err, rollbackTaskStatusSnapshotTransaction(tx))
			}
			beforeRevisions := taskStatusObservationRevisions(beforeAuthority, beforeScheduler)
			afterRevisions := taskStatusObservationRevisions(afterAuthority, afterScheduler)
			if !beforeRevisions.equal(afterRevisions) {
				rollbackErr := rollbackTaskStatusSnapshotTransaction(tx)
				if rollbackErr != nil {
					return rollbackErr
				}
				if attempt == taskStatusSnapshotCaptureAttempts {
					return &TaskStatusSnapshotConsistencyError{
						Reason:   TaskStatusSnapshotConsistencyReasonLiveObservationChanged,
						Attempts: attempt,
						Before:   beforeRevisions,
						After:    afterRevisions,
					}
				}
				retry = true
				return nil
			}
			if reason := validateTaskStatusSnapshotObservations(afterAuthority, afterScheduler); reason != "" {
				return errors.Join(
					&TaskStatusSnapshotConsistencyError{
						Reason:   reason,
						Attempts: attempt,
						Before:   beforeRevisions,
						After:    afterRevisions,
					},
					rollbackTaskStatusSnapshotTransaction(tx),
				)
			}
			snapshot = &TaskStatusSnapshot{
				tx:              tx,
				queries:         txQueries,
				authority:       afterAuthority,
				scheduler:       afterScheduler,
				currentRunFacts: append([]sqlitegen.AnchorWorkflowTaskStatusSnapshotRow(nil), currentRunFacts...),
			}
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
	if s == nil || s.tx == nil {
		return nil
	}
	tx := s.tx
	s.tx = nil
	s.queries = nil
	return rollbackTaskStatusSnapshotTransaction(tx)
}

func rollbackTaskStatusSnapshotTransaction(tx *sql.Tx) error {
	if tx == nil {
		return nil
	}
	err := tx.Rollback()
	if errors.Is(err, sql.ErrTxDone) {
		return nil
	}
	return err
}

func taskStatusSnapshotObservedRunIDs(
	authority sessionruntime.AllWorkflowExecutionSnapshot,
	scheduler workflowexecution.SchedulerActiveRunSnapshot,
) []string {
	values := make([]string, 0, len(authority.Executions)+len(scheduler.ActiveRuns))
	for _, observed := range authority.Executions {
		values = append(values, string(observed.Execution.Ref.RunID))
	}
	for _, observed := range scheduler.ActiveRuns {
		values = append(values, string(observed.RunID))
	}
	sort.Strings(values)
	out := make([]string, 0, len(values))
	for _, value := range values {
		if len(out) == 0 || out[len(out)-1] != value {
			out = append(out, value)
		}
	}
	return out
}

func taskStatusObservationRevisions(
	authority sessionruntime.AllWorkflowExecutionSnapshot,
	scheduler workflowexecution.SchedulerActiveRunSnapshot,
) TaskStatusObservationRevisions {
	promptRevisions := make([]TaskStatusPromptObservationRevision, 0, len(authority.Executions))
	for _, observed := range authority.Executions {
		promptRevisions = append(promptRevisions, TaskStatusPromptObservationRevision{
			Ref:      observed.Execution.Ref,
			Revision: observed.PromptRevision,
		})
	}
	return TaskStatusObservationRevisions{
		AuthorityExecutionMapRevision: authority.ExecutionMapRevision,
		SchedulerActiveRunRevision:    scheduler.Revision,
		PromptRevisions:               promptRevisions,
	}
}

func (r TaskStatusObservationRevisions) equal(other TaskStatusObservationRevisions) bool {
	if r.AuthorityExecutionMapRevision != other.AuthorityExecutionMapRevision ||
		r.SchedulerActiveRunRevision != other.SchedulerActiveRunRevision ||
		len(r.PromptRevisions) != len(other.PromptRevisions) {
		return false
	}
	for index, revision := range r.PromptRevisions {
		if revision != other.PromptRevisions[index] {
			return false
		}
	}
	return true
}

func validateTaskStatusSnapshotObservations(
	authority sessionruntime.AllWorkflowExecutionSnapshot,
	scheduler workflowexecution.SchedulerActiveRunSnapshot,
) TaskStatusSnapshotConsistencyReason {
	schedulerByRef := make(map[sessionruntime.WorkflowExecutionRef]struct{}, len(scheduler.ActiveRuns))
	for _, observed := range scheduler.ActiveRuns {
		if err := observed.Validate(); err != nil {
			return TaskStatusSnapshotConsistencyReasonInvalidSchedulerObservation
		}
		ref := sessionruntime.WorkflowExecutionRef{
			TaskID:     observed.TaskID,
			RunID:      observed.RunID,
			Generation: observed.Generation,
		}
		if _, exists := schedulerByRef[ref]; exists {
			return TaskStatusSnapshotConsistencyReasonDuplicateSchedulerIdentity
		}
		schedulerByRef[ref] = struct{}{}
	}
	authorityByRef := make(map[sessionruntime.WorkflowExecutionRef]struct{}, len(authority.Executions))
	for _, observed := range authority.Executions {
		if err := observed.Validate(); err != nil {
			return TaskStatusSnapshotConsistencyReasonInvalidAuthorityObservation
		}
		ref := observed.Execution.Ref
		if _, exists := authorityByRef[ref]; exists {
			return TaskStatusSnapshotConsistencyReasonDuplicateAuthorityIdentity
		}
		authorityByRef[ref] = struct{}{}
		if _, exists := schedulerByRef[ref]; !exists {
			return TaskStatusSnapshotConsistencyReasonAuthorityMissingScheduler
		}
	}
	return ""
}
